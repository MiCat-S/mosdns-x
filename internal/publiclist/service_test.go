package publiclist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
)

type fakeStore struct {
	mu          sync.Mutex
	list        control.PublicList
	lists       []control.PublicList
	enabled     bool
	refreshes   []control.PublicListRefreshResult
	refreshErr  error
	createErr   error
	updateErr   error
	listStarted chan struct{}
	listRelease chan struct{}
}

func (s *fakeStore) CreatePublicList(_ context.Context, _ string, spec control.PublicListSpec) (control.PublicList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return control.PublicList{}, s.createErr
	}
	s.list = control.PublicList{ID: "list-1", Name: spec.Name, Category: spec.Category, URL: spec.URL, Format: spec.Format, Enabled: spec.Enabled, DefaultEnabled: *spec.DefaultEnabled, Published: *spec.Published, SHA256: spec.SHA256, RefreshSeconds: spec.RefreshSeconds}
	return s.list, nil
}
func (s *fakeStore) UpdatePublicList(_ context.Context, _ string, _ string, patch control.PublicListPatch) (control.PublicList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return control.PublicList{}, s.updateErr
	}
	if patch.Published != nil {
		s.list.Published = *patch.Published
	}
	if patch.DefaultEnabled != nil {
		s.list.DefaultEnabled, s.list.Enabled = *patch.DefaultEnabled, *patch.DefaultEnabled
	}
	if patch.Name != nil {
		s.list.Name = *patch.Name
	}
	if patch.Category != nil {
		s.list.Category = *patch.Category
	}
	if patch.URL != nil {
		s.list.URL = *patch.URL
	}
	if patch.Format != nil {
		s.list.Format = *patch.Format
	}
	if patch.SHA256 != nil {
		s.list.SHA256 = *patch.SHA256
	}
	if patch.RefreshSeconds != nil {
		s.list.RefreshSeconds = *patch.RefreshSeconds
	}
	now := time.Now().UTC()
	if !now.After(s.list.UpdatedAt) {
		now = s.list.UpdatedAt.Add(time.Nanosecond)
	}
	s.list.UpdatedAt = now
	return s.list, nil
}

func (s *fakeStore) GetPublicList(_ context.Context, id string) (control.PublicList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, list := range s.lists {
		if list.ID == id {
			return list, nil
		}
	}
	if s.list.ID == "" || s.list.ID != id {
		return control.PublicList{}, control.ErrNotFound
	}
	return s.list, nil
}

func (s *fakeStore) CommitPublicListSnapshot(_ context.Context, _ string, id string, expectedUpdatedAt time.Time, spec control.PublicListSpec, result control.PublicListRefreshResult) (control.PublicList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return control.PublicList{}, s.updateErr
	}
	if s.refreshErr != nil {
		return control.PublicList{}, s.refreshErr
	}
	if s.list.ID == id && (expectedUpdatedAt.IsZero() || !s.list.UpdatedAt.Equal(expectedUpdatedAt)) {
		return control.PublicList{}, control.ErrConflict
	}
	if s.list.ID != id && !expectedUpdatedAt.IsZero() {
		return control.PublicList{}, control.ErrConflict
	}
	refreshedAt := result.RefreshedAt.UTC()
	s.list = control.PublicList{
		ID: id, Name: spec.Name, Category: spec.Category, URL: spec.URL, Format: spec.Format,
		Enabled: *spec.DefaultEnabled, DefaultEnabled: *spec.DefaultEnabled, Published: true,
		SHA256: spec.SHA256, RefreshSeconds: spec.RefreshSeconds, EntryCount: result.EntryCount,
		LastRefreshStatus: control.PublicListRefreshSuccess, LastRefreshedAt: &refreshedAt,
		SnapshotStatus: control.PublicListSnapshotCurrent, SnapshotSHA256: result.SHA256, LastSuccessfulAt: &refreshedAt,
	}
	return s.list, nil
}

func (s *fakeStore) DeletePublicList(_ context.Context, _ string, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.list.ID != id {
		return control.ErrNotFound
	}
	s.list = control.PublicList{}
	return nil
}
func (s *fakeStore) ListPublicLists(context.Context, control.Page) (control.PageResult[control.PublicList], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lists != nil {
		return control.PageResult[control.PublicList]{Items: append([]control.PublicList(nil), s.lists...)}, nil
	}
	return control.PageResult[control.PublicList]{Items: []control.PublicList{s.list}}, nil
}
func (s *fakeStore) ListUserPublicLists(context.Context, string, control.Page) (control.PageResult[control.UserPublicList], error) {
	s.mu.Lock()
	item := control.UserPublicList{List: s.list, Enabled: s.enabled}
	started, release := s.listStarted, s.listRelease
	s.listStarted = nil
	s.listRelease = nil
	s.mu.Unlock()
	if started != nil {
		close(started)
		<-release
	}
	return control.PageResult[control.UserPublicList]{Items: []control.UserPublicList{item}}, nil
}
func (s *fakeStore) RecordPublicListRefresh(_ context.Context, _ string, result control.PublicListRefreshResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes = append(s.refreshes, result)
	if s.refreshErr != nil {
		return s.refreshErr
	}
	refreshedAt := result.RefreshedAt.UTC()
	s.list.LastRefreshStatus = result.Status
	s.list.LastRefreshedAt = &refreshedAt
	if result.Status == control.PublicListRefreshSuccess {
		s.list.EntryCount = result.EntryCount
		s.list.SnapshotStatus = control.PublicListSnapshotCurrent
		s.list.SnapshotSHA256 = result.SHA256
		s.list.LastSuccessfulAt = &refreshedAt
	} else if result.SnapshotMissing {
		s.list.EntryCount = 0
		s.list.SnapshotStatus = control.PublicListSnapshotMissing
		s.list.SnapshotSHA256 = ""
		s.list.LastRefreshError = result.Error
	} else if s.list.SnapshotStatus == control.PublicListSnapshotCurrent || s.list.SnapshotStatus == control.PublicListSnapshotStale {
		s.list.SnapshotStatus = control.PublicListSnapshotStale
	}
	return nil
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

type routeTransport struct{}

func (routeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/bad" {
		return nil, errors.New("upstream unavailable")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ads.example\n")), Header: make(http.Header), Request: req}, nil
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
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Enabled: true, DefaultEnabled: true, Published: true, SHA256: hex.EncodeToString(sum[:]), RefreshSeconds: 300}, enabled: true}
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
	store.mu.Lock()
	active := store.list
	store.mu.Unlock()
	info, err := os.Stat(service.activeSnapshotPath(active))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	before, err := os.ReadFile(service.activeSnapshotPath(active))
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
	after, err := os.ReadFile(service.activeSnapshotPath(active))
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
	if listID, err := service.MatchID(context.Background(), "user-1", "x.ads.example"); err == nil || listID != "" || !strings.Contains(err.Error(), "format mismatch") {
		t.Fatalf("catalog/snapshot format mismatch was accepted: list_id=%q err=%v", listID, err)
	}
	store.mu.Lock()
	store.list.Format = control.PublicListFormatMosDNS
	store.mu.Unlock()
	service.InvalidateID("list-1")
	matched, err := service.Match(context.Background(), "user-1", "x.ads.example")
	if err != nil || !matched {
		t.Fatalf("last-good matched=%v err=%v", matched, err)
	}
	if err := os.WriteFile(service.activeSnapshotPath(active), []byte("replacement.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.attempts["list-1"] = time.Now()
	service.mu.Unlock()
	store.mu.Lock()
	store.list.Format = control.PublicListFormatMosDNS
	store.mu.Unlock()
	service.InvalidateID("list-1")
	if listID, err := service.MatchID(context.Background(), "user-1", "replacement.example"); err == nil || listID != "" || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered snapshot list_id=%q err=%v", listID, err)
	}
	service.mu.RLock()
	attempts := len(service.attempts)
	service.mu.RUnlock()
	if attempts != 0 {
		t.Fatalf("attempts=%d", attempts)
	}
	if err := service.Delete(context.Background(), "admin-1", "list-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.activeSnapshotPath(active)); !errors.Is(err, os.ErrNotExist) {
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
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Enabled: true, DefaultEnabled: true, Published: true, SHA256: strings.Repeat("0", 64), RefreshSeconds: 300}, enabled: true}
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
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Enabled: true, DefaultEnabled: true, Published: true, RefreshSeconds: 300}, enabled: true}
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
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Published: true, RefreshSeconds: 300}, enabled: true}
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

func TestValidateAndPublishUsesExactCandidateSnapshot(t *testing.T) {
	store := &fakeStore{}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	body := "domain:ads.example\nregexp:[\nfull:exact.example\n"
	transport := &queuedTransport{bodies: []string{body}}
	service.client = &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: checkRedirect}
	defaultEnabled := true
	validation, err := service.Validate(context.Background(), "", control.PublicListSpec{
		Name: "Ads", Category: "ads", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS,
		DefaultEnabled: &defaultEnabled, RefreshSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || validation.ValidationToken == "" || validation.EntryCount != 2 || validation.InvalidEntryCount != 1 || len(validation.InvalidEntries) != 1 || validation.InvalidEntries[0].Line != 2 || len(validation.Samples) != 2 {
		t.Fatalf("validation=%+v", validation)
	}
	list, err := service.Publish(context.Background(), "admin-1", "", validation.ValidationToken)
	if err != nil {
		t.Fatal(err)
	}
	if !list.Published || !list.DefaultEnabled || list.SnapshotStatus != control.PublicListSnapshotCurrent || list.SnapshotSHA256 != validation.SHA256 {
		t.Fatalf("list=%+v", list)
	}
	encoded, err := os.ReadFile(service.activeSnapshotPath(list))
	if err != nil {
		t.Fatal(err)
	}
	format, contents, err := decodeSnapshot(encoded, "")
	if err != nil || format != control.PublicListFormatMosDNS || string(contents) != body {
		t.Fatalf("format=%s contents=%q err=%v", format, contents, err)
	}
	if _, err := service.Publish(context.Background(), "admin-1", "", validation.ValidationToken); !errors.Is(err, ErrValidationTokenInvalid) {
		t.Fatalf("reused token error=%v", err)
	}
}

func TestValidationTokenCanOnlyBeConsumedOnceConcurrently(t *testing.T) {
	store := &fakeStore{}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: &queuedTransport{bodies: []string{"ads.example\n"}}, Timeout: time.Second, CheckRedirect: checkRedirect}
	validation, err := service.Validate(context.Background(), "", control.PublicListSpec{
		Name: "Ads", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, RefreshSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Publish(context.Background(), "admin-1", "", validation.ValidationToken)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	successes, rejected := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrValidationTokenInvalid):
			rejected++
		default:
			t.Fatalf("unexpected publish error: %v", err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("successes=%d rejected=%d", successes, rejected)
	}
}

func TestPublishRejectsCandidateAfterListMetadataChanges(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	store := &fakeStore{list: control.PublicList{
		ID: "list-1", Name: "Before validation", URL: "https://public.example/list",
		Format: control.PublicListFormatMosDNS, RefreshSeconds: 300, UpdatedAt: updatedAt,
	}}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: &queuedTransport{bodies: []string{"ads.example\n"}}, Timeout: time.Second, CheckRedirect: checkRedirect}
	validation, err := service.Validate(context.Background(), "list-1", control.PublicListSpec{
		Name: "Validated name", URL: "https://public.example/list",
		Format: control.PublicListFormatMosDNS, RefreshSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := "Changed after validation"
	if _, err := service.Update(context.Background(), "admin-1", "list-1", control.PublicListPatch{Name: &replacement}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(context.Background(), "admin-1", "list-1", validation.ValidationToken); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("publish error=%v", err)
	}
	current, err := store.GetPublicList(context.Background(), "list-1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Name != replacement || current.Published {
		t.Fatalf("stale validation overwrote current metadata: %+v", current)
	}
}

func TestConcurrencyOneRefreshAndPublishUseConsistentLockOrder(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	store := &fakeStore{list: control.PublicList{
		ID: "list-1", Name: "Ads", URL: "https://public.example/list",
		Format: control.PublicListFormatMosDNS, Published: true,
		RefreshSeconds: 300, SnapshotStatus: control.PublicListSnapshotMissing, UpdatedAt: updatedAt,
	}}
	service, err := New(store, Options{Directory: t.TempDir(), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: &queuedTransport{bodies: []string{"candidate.example\n", "refresh.example\n"}}, Timeout: time.Second, CheckRedirect: checkRedirect}
	validation, err := service.Validate(context.Background(), "list-1", control.PublicListSpec{
		Name: "Ads", URL: "https://public.example/list",
		Format: control.PublicListFormatMosDNS, RefreshSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}

	unlock := service.lockList("list-1")
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- service.Refresh(context.Background(), "list-1") }()
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	for len(service.sem) != 1 {
		select {
		case <-deadline.C:
			unlock()
			t.Fatal("refresh waited on the list lock before acquiring the shared work slot")
		case <-ticker.C:
		}
	}
	ticker.Stop()
	deadline.Stop()

	publishDone := make(chan error, 1)
	go func() {
		_, err := service.Publish(context.Background(), "admin-1", "list-1", validation.ValidationToken)
		publishDone <- err
	}()
	select {
	case err := <-publishDone:
		unlock()
		t.Fatalf("publish bypassed the occupied work slot: %v", err)
	default:
	}
	unlock()

	for name, done := range map[string]<-chan error{"refresh": refreshDone, "publish": publishDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s error=%v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s deadlocked", name)
		}
	}
}

func TestNewRemovesOrphanedValidationCandidates(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, ".candidate-orphan")
	if err := os.WriteFile(orphan, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(&fakeStore{}, Options{Directory: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan candidate remains: %v", err)
	}
}

func TestNewRecoversInterruptedDeleteAndRemovesUnreferencedSnapshot(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "list-1.list")
	tombstone := filepath.Join(dir, ".deleted-public-list-list-1.list")
	if err := os.WriteFile(tombstone, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "orphan-"+strings.Repeat("a", 64)+".list")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{list: control.PublicList{ID: "list-1", Published: true}}
	if _, err := New(store, Options{Directory: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("interrupted delete was not restored: %v", err)
	}
	if _, err := os.Stat(tombstone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tombstone remains: %v", err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan snapshot remains: %v", err)
	}
}

func TestNewMarksPublishedRecordMissingWhenSnapshotIsUnavailable(t *testing.T) {
	store := &fakeStore{list: control.PublicList{
		ID: "list-1", Published: true, SnapshotStatus: control.PublicListSnapshotCurrent,
		SnapshotSHA256: strings.Repeat("a", 64), LastRefreshStatus: control.PublicListRefreshSuccess,
	}}
	if _, err := New(store, Options{Directory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.list.SnapshotStatus != control.PublicListSnapshotMissing || store.list.LastRefreshStatus != control.PublicListRefreshError || store.list.SnapshotSHA256 != "" {
		t.Fatalf("list=%+v", store.list)
	}
}

func TestNewPreservesSnapshotPointerOnTransientReadError(t *testing.T) {
	digest := strings.Repeat("a", 64)
	store := &fakeStore{list: control.PublicList{
		ID: "list-1", Format: control.PublicListFormatMosDNS, Published: true,
		SnapshotStatus: control.PublicListSnapshotCurrent, SnapshotSHA256: digest,
		EntryCount: 1, LastRefreshStatus: control.PublicListRefreshSuccess,
	}}
	dir := t.TempDir()
	path := filepath.Join(dir, "list-1-mosdns-"+digest+".list")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(store, Options{Directory: dir}); err == nil {
		t.Fatal("snapshot read error did not stop startup")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.list.SnapshotStatus != control.PublicListSnapshotCurrent || store.list.SnapshotSHA256 != digest || store.list.EntryCount != 1 || len(store.refreshes) != 0 {
		t.Fatalf("transient read error destroyed snapshot state: list=%+v refreshes=%+v", store.list, store.refreshes)
	}
}

func TestValidateRejectsEntryLimitAndEmptyCandidate(t *testing.T) {
	store := &fakeStore{}
	service, err := New(store, Options{Directory: t.TempDir(), MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	transport := &queuedTransport{bodies: []string{"one.example\ntwo.example\n", "regexp:[\n"}}
	service.client = &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: checkRedirect}
	spec := control.PublicListSpec{Name: "Ads", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, RefreshSeconds: 300}
	if _, err := service.Validate(context.Background(), "", spec); err == nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("entry limit error=%v", err)
	}
	validation, err := service.Validate(context.Background(), "", spec)
	if err != nil {
		t.Fatal(err)
	}
	if validation.Valid || validation.ValidationToken != "" || validation.InvalidEntryCount != 1 {
		t.Fatalf("validation=%+v", validation)
	}
}

func TestUnpublishedListIsNeverSelected(t *testing.T) {
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Published: false, RefreshSeconds: 300}, enabled: true}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.writeSnapshot("list-1", control.PublicListFormatMosDNS, []byte("ads.example\n")); err != nil {
		t.Fatal(err)
	}
	if id, err := service.MatchID(context.Background(), "user-1", "ads.example"); err != nil || id != "" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestValidationTokenExpires(t *testing.T) {
	store := &fakeStore{}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: &queuedTransport{bodies: []string{"ads.example\n"}}, Timeout: time.Second, CheckRedirect: checkRedirect}
	validation, err := service.Validate(context.Background(), "", control.PublicListSpec{Name: "Ads", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, RefreshSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	candidate := service.candidates[validation.ValidationToken]
	candidate.validation.ExpiresAt = time.Now().Add(-time.Second)
	service.candidates[validation.ValidationToken] = candidate
	service.mu.Unlock()
	if _, err := service.Publish(context.Background(), "admin-1", "", validation.ValidationToken); !errors.Is(err, ErrValidationTokenExpired) {
		t.Fatalf("expired token error=%v", err)
	}
	if _, err := os.Stat(candidate.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate cleanup error=%v", err)
	}
}

func TestPublishDatabaseFailureKeepsCandidateRetryable(t *testing.T) {
	store := &fakeStore{refreshErr: errors.New("database unavailable"), enabled: true}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: &queuedTransport{bodies: []string{"ads.example\n"}}, Timeout: time.Second, CheckRedirect: checkRedirect}
	validation, err := service.Validate(context.Background(), "", control.PublicListSpec{Name: "Ads", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Enabled: true, RefreshSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	list, err := service.Publish(context.Background(), "admin-1", "", validation.ValidationToken)
	if err == nil || list.ID != "" {
		t.Fatalf("publish list=%+v err=%v", list, err)
	}
	store.mu.Lock()
	store.refreshErr = nil
	store.mu.Unlock()
	list, err = service.Publish(context.Background(), "admin-1", "", validation.ValidationToken)
	if err != nil || !list.Published {
		t.Fatalf("retry list=%+v err=%v", list, err)
	}
}

func TestRefreshAllReportsEveryPublishedList(t *testing.T) {
	store := &fakeStore{lists: []control.PublicList{
		{ID: "good", URL: "https://public.example/good", Format: control.PublicListFormatMosDNS, Published: true, RefreshSeconds: 300},
		{ID: "bad", URL: "https://public.example/bad", Format: control.PublicListFormatMosDNS, Published: true, RefreshSeconds: 300},
		{ID: "draft", URL: "https://public.example/draft", Format: control.PublicListFormatMosDNS, Published: false, RefreshSeconds: 300},
	}}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: routeTransport{}, Timeout: time.Second, CheckRedirect: checkRedirect}
	result, err := service.RefreshAllDetailed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "bad" || result.Items[0].Error == "" || result.Items[1].ID != "good" || result.Items[1].Error != "" {
		t.Fatalf("result=%+v", result)
	}
}

func TestInvalidateStopsCachedPublishedSelection(t *testing.T) {
	store := &fakeStore{list: control.PublicList{ID: "list-1", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS, Published: true, RefreshSeconds: 300}, enabled: true}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.writeSnapshot("list-1", control.PublicListFormatMosDNS, []byte("ads.example\n")); err != nil {
		t.Fatal(err)
	}
	if id, err := service.MatchID(context.Background(), "user-1", "ads.example"); err != nil || id != "list-1" {
		t.Fatalf("initial id=%q err=%v", id, err)
	}
	store.mu.Lock()
	store.list.Published = false
	store.mu.Unlock()
	service.Invalidate("")
	if id, err := service.MatchID(context.Background(), "user-1", "ads.example"); err != nil || id != "" {
		t.Fatalf("cached unpublished id=%q err=%v", id, err)
	}
}

func TestInvalidateDuringSelectionLoadCannotRestoreStaleChoice(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	store := &fakeStore{
		list:        control.PublicList{ID: "list-1", Format: control.PublicListFormatMosDNS, Published: true},
		enabled:     true,
		listStarted: started,
		listRelease: release,
	}
	service, err := New(store, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.writeSnapshot("list-1", control.PublicListFormatMosDNS, []byte("ads.example\n")); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	go func() {
		id, _ := service.MatchID(context.Background(), "user-1", "ads.example")
		result <- id
	}()
	<-started
	store.mu.Lock()
	store.enabled = false
	store.mu.Unlock()
	service.Invalidate("user-1")
	close(release)
	if id := <-result; id != "" {
		t.Fatalf("stale selection was restored: %q", id)
	}
}

func TestFailedRefreshKeepsCommittedSnapshotAndCompiledRules(t *testing.T) {
	oldBody := []byte("old.example\n")
	oldSum := sha256.Sum256(oldBody)
	oldDigest := hex.EncodeToString(oldSum[:])
	store := &fakeStore{list: control.PublicList{
		ID: "list-1", Name: "List", URL: "https://public.example/list", Format: control.PublicListFormatMosDNS,
		Published: true, SnapshotStatus: control.PublicListSnapshotCurrent, SnapshotSHA256: oldDigest, RefreshSeconds: 300,
	}, enabled: true}
	dir := t.TempDir()
	encoded, _ := encodeSnapshot(control.PublicListFormatMosDNS, oldBody)
	if err := os.WriteFile(filepath.Join(dir, "list-1-mosdns-"+oldDigest+".list"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(store, Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := service.MatchID(context.Background(), "user-1", "old.example"); err != nil || id != "list-1" {
		t.Fatalf("initial match id=%q err=%v", id, err)
	}
	service.client = &http.Client{Transport: &queuedTransport{bodies: []string{"new.example\n"}}, Timeout: time.Second}
	store.mu.Lock()
	store.refreshErr = errors.New("database unavailable")
	store.mu.Unlock()
	if err := service.Refresh(context.Background(), "list-1"); err == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}
	if id, err := service.MatchID(context.Background(), "user-1", "old.example"); err != nil || id != "list-1" {
		t.Fatalf("old snapshot no longer active id=%q err=%v", id, err)
	}
	if id, err := service.MatchID(context.Background(), "user-1", "new.example"); err != nil || id != "" {
		t.Fatalf("uncommitted snapshot matched id=%q err=%v", id, err)
	}
}

func TestFailedPublishKeepsExistingPublishedSnapshot(t *testing.T) {
	oldBody := []byte("old.example\n")
	oldSum := sha256.Sum256(oldBody)
	oldDigest := hex.EncodeToString(oldSum[:])
	store := &fakeStore{list: control.PublicList{
		ID: "list-1", Name: "Old", URL: "https://public.example/old", Format: control.PublicListFormatMosDNS,
		Published: true, SnapshotStatus: control.PublicListSnapshotCurrent, SnapshotSHA256: oldDigest, RefreshSeconds: 300,
		UpdatedAt: time.Now().UTC(),
	}, enabled: true}
	dir := t.TempDir()
	encoded, _ := encodeSnapshot(control.PublicListFormatMosDNS, oldBody)
	if err := os.WriteFile(filepath.Join(dir, "list-1-mosdns-"+oldDigest+".list"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(store, Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: &queuedTransport{bodies: []string{"new.example\n"}}, Timeout: time.Second}
	validation, err := service.Validate(context.Background(), "list-1", control.PublicListSpec{
		Name: "New", URL: "https://public.example/new", Format: control.PublicListFormatMosDNS, RefreshSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.refreshErr = errors.New("database unavailable")
	store.mu.Unlock()
	if _, err := service.Publish(context.Background(), "admin-1", "list-1", validation.ValidationToken); err == nil {
		t.Fatal("publish unexpectedly succeeded")
	}
	store.mu.Lock()
	current := store.list
	store.mu.Unlock()
	if !current.Published || current.Name != "Old" || current.SnapshotSHA256 != oldDigest {
		t.Fatalf("existing list changed after failed publish: %+v", current)
	}
	if id, err := service.MatchID(context.Background(), "user-1", "old.example"); err != nil || id != "list-1" {
		t.Fatalf("old snapshot not active id=%q err=%v", id, err)
	}
}

func TestRejectsExcessiveLinearRules(t *testing.T) {
	var body strings.Builder
	for i := 0; i <= maxLinearRules; i++ {
		fmt.Fprintf(&body, "keyword:tracker-%d\n", i)
	}
	if _, _, err := analyze([]byte(body.String()), control.PublicListFormatMosDNS, maxLinearRules+1); err == nil || !strings.Contains(err.Error(), "keyword and regexp") {
		t.Fatalf("linear rule limit error=%v", err)
	}
}
