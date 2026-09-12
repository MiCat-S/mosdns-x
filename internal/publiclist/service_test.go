package publiclist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
)

type fakeStore struct {
	mu         sync.Mutex
	list       control.PublicList
	enabled    bool
	refreshes  []control.PublicListRefreshResult
	refreshErr error
}

func (s *fakeStore) GetPublicList(context.Context, string) (control.PublicList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list, nil
}
func (s *fakeStore) ListPublicLists(context.Context, control.Page) (control.PageResult[control.PublicList], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return control.PageResult[control.PublicList]{Items: []control.PublicList{s.list}}, nil
}
func (s *fakeStore) ListUserPublicLists(context.Context, string, control.Page) (control.PageResult[control.UserPublicList], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return control.PageResult[control.UserPublicList]{Items: []control.UserPublicList{{List: s.list, Enabled: s.enabled}}}, nil
}
func (s *fakeStore) RecordPublicListRefresh(_ context.Context, _ string, result control.PublicListRefreshResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes = append(s.refreshes, result)
	return s.refreshErr
}

type queuedTransport struct {
	mu     sync.Mutex
	bodies []string
	wait   bool
}

type blockingTransport struct {
	started chan struct{}
	release chan struct{}
}

func (t *blockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case t.started <- struct{}{}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	select {
	case <-t.release:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("example.com\n")), Header: make(http.Header), Request: req}, nil
}

func (t *queuedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.wait {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.bodies) == 0 {
		return nil, errors.New("no response")
	}
	body := t.bodies[0]
	t.bodies = t.bodies[1:]
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

func TestRefreshCompileSnapshotAndLastGood(t *testing.T) {
	good := "domain:ads.example\nfull:exact.example\nkeyword:tracker\nregexp:^rx[0-9]+\\.example$\n"
	sum := sha256.Sum256([]byte(good))
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Enabled: true, SHA256: hex.EncodeToString(sum[:]), RefreshSeconds: 300}, enabled: true}
	transport := &queuedTransport{bodies: []string{good, "regexp:["}}
	dir := t.TempDir()
	service, err := New(store, Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: checkRedirect}
	if err := service.Refresh(context.Background(), "list-1"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	if len(store.refreshes) != 1 || store.refreshes[0].Status != control.PublicListRefreshSuccess || store.refreshes[0].EntryCount != 4 {
		t.Fatalf("refreshes=%+v", store.refreshes)
	}
	store.mu.Unlock()
	for _, name := range []string{"x.ads.example", "exact.example", "mytracker.example", "rx12.example"} {
		listID, err := service.MatchID(context.Background(), "user-1", name)
		if err != nil || listID != "list-1" {
			t.Fatalf("name=%s list_id=%q err=%v", name, listID, err)
		}
	}
	info, err := os.Stat(service.snapshotPath("list-1"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	before, err := os.ReadFile(service.snapshotPath("list-1"))
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.list.SHA256 = ""
	store.mu.Unlock()
	if err := service.Refresh(context.Background(), "list-1"); err == nil {
		t.Fatal("invalid refresh succeeded")
	}
	store.mu.Lock()
	if len(store.refreshes) != 2 || store.refreshes[1].Status != control.PublicListRefreshError || store.refreshes[1].Error == "" {
		t.Fatalf("refreshes=%+v", store.refreshes)
	}
	store.mu.Unlock()
	after, err := os.ReadFile(service.snapshotPath("list-1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed refresh replaced last-good snapshot")
	}
	store.mu.Lock()
	store.list.Format = control.PublicListFormatHosts
	store.mu.Unlock()
	service.InvalidateID("list-1")
	if listID, err := service.MatchID(context.Background(), "user-1", "x.ads.example"); err != nil || listID != "list-1" {
		t.Fatalf("snapshot format was not preserved after catalog edit: list_id=%q err=%v", listID, err)
	}
	matched, err := service.Match(context.Background(), "user-1", "x.ads.example")
	if err != nil || !matched {
		t.Fatalf("last-good matched=%v err=%v", matched, err)
	}
	if err := os.WriteFile(service.snapshotPath("list-1"), []byte("replacement.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.attempts["list-1"] = time.Now()
	service.mu.Unlock()
	store.mu.Lock()
	store.list.Format = control.PublicListFormatMosDNS
	store.mu.Unlock()
	service.InvalidateID("list-1")
	if listID, err := service.MatchID(context.Background(), "user-1", "replacement.example"); err != nil || listID != "list-1" {
		t.Fatalf("invalidated list_id=%q err=%v", listID, err)
	}
	service.mu.RLock()
	attempts := len(service.attempts)
	service.mu.RUnlock()
	if attempts != 0 {
		t.Fatalf("attempts=%d", attempts)
	}
	if err := service.Delete("list-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.snapshotPath("list-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot delete error=%v", err)
	}
}

func TestHostsCompileAndSecurityValidation(t *testing.T) {
	compiled, err := compile([]byte("0.0.0.0 ads.example alias.example # comment\n::1 v6.example\n"), control.PublicListFormatHosts)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ads.example", "alias.example", "v6.example"} {
		if !compiled.match(name) {
			t.Errorf("missing %s", name)
		}
	}
	for _, raw := range []string{"http://example.com/list", "https://127.0.0.1/list", "https://[::1]/list", "https://100.64.0.1/list", "https://192.0.2.1/list", "https://192.88.99.1/list", "https://240.0.0.1/list", "https://[2001:db8::1]/list", "https://user:pass@example.com/list"} {
		if err := validateListURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	req := &http.Request{URL: &url.URL{Scheme: "http", Host: "example.com"}}
	if err := checkRedirect(req, nil); err == nil {
		t.Fatal("accepted HTTP redirect")
	}
	req.URL = &url.URL{Scheme: "https", Host: "127.0.0.1"}
	if err := checkRedirect(req, nil); err == nil {
		t.Fatal("accepted private redirect")
	}
}

func TestFetchSizeHashAndTimeoutLimits(t *testing.T) {
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Enabled: true, SHA256: strings.Repeat("0", 64), RefreshSeconds: 300}, enabled: true}
	transport := &queuedTransport{bodies: []string{"example.com"}}
	service, err := New(store, Options{Directory: t.TempDir(), MaxSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: checkRedirect}
	if err := service.Refresh(context.Background(), "list-1"); err == nil {
		t.Fatal("sha mismatch succeeded")
	}
	store.mu.Lock()
	store.list.SHA256 = ""
	store.mu.Unlock()
	transport.mu.Lock()
	transport.bodies = []string{strings.Repeat("a", 1025)}
	transport.mu.Unlock()
	if err := service.Refresh(context.Background(), "list-1"); err == nil {
		t.Fatal("oversized fetch succeeded")
	}
	waiting := &queuedTransport{wait: true}
	service, err = New(store, Options{Directory: t.TempDir(), Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: waiting, Timeout: 20 * time.Millisecond, CheckRedirect: checkRedirect}
	started := time.Now()
	if err := service.Refresh(context.Background(), "list-1"); err == nil {
		t.Fatal("timeout fetch succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("timeout was not enforced")
	}
}

func TestRefreshConcurrencyLimit(t *testing.T) {
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Enabled: true, RefreshSeconds: 300}, enabled: true}
	transport := &blockingTransport{started: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	service, err := New(store, Options{Directory: t.TempDir(), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: checkRedirect}
	results := make(chan error, 2)
	go func() { results <- service.Refresh(context.Background(), "list-1") }()
	go func() { results <- service.Refresh(context.Background(), "list-1") }()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}
	select {
	case <-transport.started:
		t.Fatal("second refresh bypassed concurrency limit")
	case <-time.After(50 * time.Millisecond):
	}
	transport.release <- struct{}{}
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("second refresh did not start after release")
	}
	transport.release <- struct{}{}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunWaitsForCanceledRefreshes(t *testing.T) {
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Enabled: false, RefreshSeconds: 300}, enabled: true}
	transport := &blockingTransport{started: make(chan struct{}, 1), release: make(chan struct{})}
	service, err := New(store, Options{Directory: t.TempDir(), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: checkRedirect}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not wait for its canceled refresh to finish")
	}
}
