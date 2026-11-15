package recursor

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// blockingExchanger holds every exchange until release is closed, ignoring
// the context on purpose: it stands for an exchange that is slow to notice
// cancellation.
type blockingExchanger struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	closed  atomic.Bool
	late    atomic.Int32
}

func (b *blockingExchanger) exchange(ctx context.Context, addr netip.Addr, q *dns.Msg, p proto) (*dns.Msg, error) {
	if b.closed.Load() {
		b.late.Add(1)
	}
	b.once.Do(func() { close(b.started) })
	<-b.release
	return nil, errors.New("blocked exchange released")
}

func (b *blockingExchanger) probeDoT(ctx context.Context, addr netip.Addr) error {
	return errors.New("no dot")
}

func (b *blockingExchanger) close() error {
	b.closed.Store(true)
	return nil
}

func TestCloseWaitsForSharedResolution(t *testing.T) {
	ex := &blockingExchanger{started: make(chan struct{}), release: make(chan struct{})}
	r, err := New(Opts{exchanger: ex, NoIPv6: true, DisableDNSSEC: true})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := r.Resolve(ctx, query("example.com.", dns.TypeA, false))
		errCh <- err
	}()
	<-ex.started
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller got %v, want context.Canceled", err)
	}

	closed := make(chan struct{})
	go func() {
		r.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a shared resolution was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(ex.release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the shared resolution ended")
	}
	if n := ex.late.Load(); n != 0 {
		t.Fatalf("%d exchanges started after the exchanger was closed", n)
	}
	if _, err := r.Resolve(context.Background(), query("example.com.", dns.TypeA, false)); !errors.Is(err, errClosed) {
		t.Fatalf("resolve after close got %v, want errClosed", err)
	}
}
