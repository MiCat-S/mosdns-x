package data_provider

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/pmkol/mosdns-x/pkg/safe_close"
)

type recordListener struct {
	mu   sync.Mutex
	data [][]byte
}

func (l *recordListener) Update(b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data = append(l.data, append([]byte(nil), b...))
	return nil
}

func (l *recordListener) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.data)
}

func (l *recordListener) last() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.data) == 0 {
		return nil
	}
	return l.data[len(l.data)-1]
}

func (l *recordListener) waitFor(t *testing.T, want []byte, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Equal(l.last(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listener did not receive %q, last: %q", want, l.last())
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// atomicWrite writes b to a temp file in the same directory and renames it over p.
func atomicWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.Name(), p); err != nil {
		t.Fatal(err)
	}
}

func closeWithTimeout(t *testing.T, dp *DataProvider) {
	t.Helper()
	closed := make(chan struct{})
	go func() { dp.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() did not return")
	}
}

func newTestProvider(t *testing.T, initial []byte) (*DataProvider, *recordListener, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "data.txt")
	writeFile(t, file, initial)
	dp, err := NewDataProvider(zaptest.NewLogger(t), DataProviderConfig{File: file, AutoReload: true})
	if err != nil {
		t.Fatal(err)
	}
	l := new(recordListener)
	if err := dp.LoadAndAddListener(l); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(l.last(), initial) {
		t.Fatalf("unexpected initial data %q", l.last())
	}
	return dp, l, file
}

func TestDataProviderWrite(t *testing.T) {
	dp, l, file := newTestProvider(t, []byte("v1"))
	defer closeWithTimeout(t, dp)

	writeFile(t, file, []byte("v2"))
	l.waitFor(t, []byte("v2"), 3*time.Second)
}

func TestDataProviderAtomicReplace(t *testing.T) {
	dp, l, file := newTestProvider(t, []byte("v1"))
	defer closeWithTimeout(t, dp)

	atomicWrite(t, file, []byte("v2"))
	l.waitFor(t, []byte("v2"), 3*time.Second)
	// A second replacement proves the watch survived the first rename.
	atomicWrite(t, file, []byte("v3"))
	l.waitFor(t, []byte("v3"), 3*time.Second)
}

func TestDataProviderRemoveThenCreate(t *testing.T) {
	dp, l, file := newTestProvider(t, []byte("v1"))
	defer closeWithTimeout(t, dp)

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if n := l.count(); n != 1 {
		t.Fatalf("listener called %d times while the file was missing", n)
	}
	writeFile(t, file, []byte("v2"))
	l.waitFor(t, []byte("v2"), 3*time.Second)
}

func TestDataProviderIgnoresSiblings(t *testing.T) {
	dp, l, file := newTestProvider(t, []byte("v1"))
	defer closeWithTimeout(t, dp)

	writeFile(t, filepath.Join(filepath.Dir(file), "other.txt"), []byte("x"))
	atomicWrite(t, filepath.Join(filepath.Dir(file), "other2.txt"), []byte("x"))
	time.Sleep(1500 * time.Millisecond)
	if n := l.count(); n != 1 {
		t.Fatalf("listener called %d times for unrelated files", n)
	}
}

func TestDataProviderCoalesce(t *testing.T) {
	dp, l, file := newTestProvider(t, []byte("v0"))
	defer closeWithTimeout(t, dp)

	const writes = 20
	for i := 1; i <= writes; i++ {
		writeFile(t, file, []byte(fmt.Sprintf("v%d", i)))
		time.Sleep(10 * time.Millisecond)
	}
	want := []byte(fmt.Sprintf("v%d", writes))
	l.waitFor(t, want, 3*time.Second)
	// Wait for a possible trailing reload before counting.
	time.Sleep(1500 * time.Millisecond)
	reloads := l.count() - 1
	if reloads < 1 || reloads >= writes {
		t.Fatalf("expected coalesced reloads, got %d for %d writes", reloads, writes)
	}
}

func TestDataProviderNoPushAfterClose(t *testing.T) {
	dp, l, file := newTestProvider(t, []byte("v1"))

	writeFile(t, file, []byte("v2"))
	closeWithTimeout(t, dp)
	n := l.count()

	writeFile(t, file, []byte("v3"))
	time.Sleep(1500 * time.Millisecond)
	if got := l.count(); got != n {
		t.Fatalf("listener called after Close(): %d -> %d", n, got)
	}
	// Close must be safe to call again.
	closeWithTimeout(t, dp)
}

func TestDataProviderWatcherMissingDir(t *testing.T) {
	ds := &DataProvider{
		logger: zaptest.NewLogger(t),
		file:   filepath.Join(t.TempDir(), "nope", "data.txt"),
		sc:     safe_close.NewSafeClose(),
	}
	if err := ds.startFsWatcher(); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
	closeWithTimeout(t, ds)
}
