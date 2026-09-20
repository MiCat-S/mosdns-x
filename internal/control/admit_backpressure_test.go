package control

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func admitTestStore(t *testing.T, opts Options) (*Store, Identity) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "control.db"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, UserSpec{
		Username: "admit-admin", Password: "admit-test-password",
		Limit: 1_000_000, QPS: 1_000_000, Burst: 1_000_000, MaxCredentials: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.CreateCredential(ctx, admin.ID, admin.ID, "device", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

// A burst wider than the slot pool must be paced rather than rejected. The
// previous non-blocking acquire turned any overshoot into an immediate
// unavailable error, which reaches a DoH client as HTTP 503.
func TestAdmitQueuesBurstInsteadOfRejecting(t *testing.T) {
	s, id := admitTestStore(t, Options{AdmitQueueSize: 2, AdmitWait: 5 * time.Second})
	const callers = 64
	var wg sync.WaitGroup
	errs := make([]error, callers)
	wg.Add(callers)
	for i := range callers {
		go func() {
			defer wg.Done()
			errs[i] = s.Admit(context.Background(), id)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("admit %d rejected a burst that fits the wait budget: %v", i, err)
		}
	}
	quota, err := s.CurrentQuota(context.Background(), id.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if quota.Used != callers {
		t.Fatalf("quota used = %d, want %d: every admitted query must be counted exactly once", quota.Used, callers)
	}
}

// The wait is bounded: a caller that cannot get a slot within AdmitWait is
// rejected rather than queueing without limit.
func TestAdmitWaitIsBounded(t *testing.T) {
	s, id := admitTestStore(t, Options{AdmitQueueSize: 1, AdmitWait: 20 * time.Millisecond})
	release := make(chan struct{})
	occupied := make(chan struct{})
	go func() {
		s.admitSlots <- struct{}{}
		close(occupied)
		<-release
		<-s.admitSlots
	}()
	<-occupied
	defer close(release)

	started := time.Now()
	err := s.Admit(context.Background(), id)
	elapsed := time.Since(started)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable once the wait budget is spent", err)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("returned after %s, want at least the full 20ms wait", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("returned after %s, want a bounded wait", elapsed)
	}
}

// A cancelled request must not keep occupying the queue.
func TestAdmitHonorsContextCancellation(t *testing.T) {
	s, id := admitTestStore(t, Options{AdmitQueueSize: 1, AdmitWait: 5 * time.Second})
	release := make(chan struct{})
	occupied := make(chan struct{})
	go func() {
		s.admitSlots <- struct{}{}
		close(occupied)
		<-release
		<-s.admitSlots
	}()
	<-occupied
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := s.Admit(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestOpenRejectsInvalidAdmitTuning(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts Options
	}{
		{"negative wait", Options{AdmitWait: -1}},
		{"excessive wait", Options{AdmitWait: MaxAdmitWait + time.Second}},
		{"negative batch delay", Options{BatchDelay: -1}},
		{"excessive batch delay", Options{BatchDelay: MaxBatchDelay + time.Second}},
		{"negative batch size", Options{BatchSize: -1}},
		{"excessive batch size", Options{BatchSize: MaxBatchSize + 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "control.db"), tt.opts)
			if err == nil {
				s.Close()
				t.Fatal("invalid admit tuning was accepted")
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}
