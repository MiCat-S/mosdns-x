package control

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

type fakeClock struct {
	mu sync.RWMutex
	t  time.Time
}

type gateClock struct {
	t       time.Time
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (c *gateClock) Now() time.Time {
	if c.armed.CompareAndSwap(true, false) {
		close(c.entered)
		<-c.release
	}
	return c.t
}

func (c *fakeClock) Now() time.Time  { c.mu.RLock(); defer c.mu.RUnlock(); return c.t }
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func newTestStore(t *testing.T, now time.Time) (*Store, *fakeClock, string) {
	t.Helper()
	clock := &fakeClock{t: now}
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(path, Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, clock, path
}

func adminSpec() UserSpec {
	return UserSpec{Username: "admin", Password: "correct horse battery", Role: RoleAdmin, Enabled: true, Period: PeriodMonthly, Timezone: "UTC", Limit: 1000, QPS: 100, Burst: 10, MaxCredentials: 10}
}
func userSpec(name string, limit uint64, qps, burst uint32) UserSpec {
	return UserSpec{Username: name, Password: "correct horse battery", Role: RoleUser, Enabled: true, Period: PeriodDaily, Timezone: "UTC", Limit: limit, QPS: qps, Burst: burst, MaxCredentials: 10}
}
func setupUser(t *testing.T, s *Store, spec UserSpec) (User, IssuedCredential) {
	t.Helper()
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, admin.ID, spec)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.CreateCredential(ctx, u.ID, u.ID, "phone", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return u, cred
}

func TestPasswordAndSessionInvalidation(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticatePassword(ctx, "admin", "wrong password"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("wrong password: %v", err)
	}
	ss, token, err := s.CreateSession(ctx, admin.ID, time.Hour)
	if err != nil || ss.CSRFToken == "" {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err = s.AuthenticateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPassword(ctx, admin.ID, admin.ID, "another correct password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateSession(ctx, token); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("session survived password change: %v", err)
	}
	if _, err = s.AuthenticatePassword(ctx, "admin", "another correct password"); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialOwnershipRevocationAndExpiry(t *testing.T) {
	s, clock, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	u1, err := s.CreateUser(ctx, admin.ID, userSpec("one", 100, 100, 0))
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.CreateUser(ctx, admin.ID, userSpec("two", 100, 100, 0))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateCredential(ctx, u1.ID, u1.ID, "laptop", clock.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateCredential(ctx, c.Credential.ID+".wrong-secret"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("wrong secret: %v", err)
	}
	if err = s.RevokeCredential(ctx, u2.ID, u1.ID, c.Credential.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user revoke: %v", err)
	}
	if err = s.RevokeCredential(ctx, u1.ID, u1.ID, c.Credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateCredential(ctx, c.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked credential: %v", err)
	}
	exp, err := s.CreateCredential(ctx, u1.ID, u1.ID, "short", clock.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(time.Minute))
	if _, err = s.AuthenticateCredential(ctx, exp.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired credential: %v", err)
	}
}

func TestRotateInvalidatesPreviouslyAuthenticatedIdentity(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	u, issued := setupUser(t, s, userSpec("user", 100, 100, 0))
	oldIdentity, err := s.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := s.RotateCredential(ctx, u.ID, u.ID, issued.Credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, oldIdentity); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old identity admitted: %v", err)
	}
	newIdentity, err := s.AuthenticateCredential(ctx, rotated.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, newIdentity); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredServiceStillAllowsPanelLogin(t *testing.T) {
	s, clock, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	spec := userSpec("expired", 100, 100, 0)
	spec.ExpiresAt = clock.Now().Add(time.Minute)
	u, issued := setupUser(t, s, spec)
	identity, err := s.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(spec.ExpiresAt)
	if _, err = s.AuthenticatePassword(ctx, u.Username, spec.Password); err != nil {
		t.Fatalf("password login after service expiry: %v", err)
	}
	ss, token, err := s.Login(ctx, u.Username, spec.Password, time.Hour)
	if err != nil || ss.UserID != u.ID || token == "" {
		t.Fatalf("atomic login: session=%+v err=%v", ss, err)
	}
	if _, _, err = s.AuthenticateSession(ctx, token); err != nil {
		t.Fatalf("session after service expiry: %v", err)
	}
	if err = s.Admit(ctx, identity); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired entitlement admitted: %v", err)
	}
}

func TestLoginUsesCurrentPasswordVersion(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetPassword(ctx, admin.ID, admin.ID, "replacement password ok"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Login(ctx, "admin", "correct horse battery", time.Hour); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("old password login: %v", err)
	}
	if _, _, err = s.Login(ctx, "admin", "replacement password ok", time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestChangePasswordRejectsConcurrentResetAndRevokesAllSessions(t *testing.T) {
	s, base, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	gate := &gateClock{t: base.Now(), entered: make(chan struct{}), release: make(chan struct{})}
	gate.armed.Store(true)
	s.clock = gate
	done := make(chan error, 1)
	go func() { done <- s.ChangePassword(ctx, admin.ID, "correct horse battery", "user-attempted-password") }()
	<-gate.entered
	if err = s.SetPassword(ctx, admin.ID, admin.ID, "administrator-reset"); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if err = <-done; !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("racing change=%v", err)
	}
	if _, err = s.AuthenticatePassword(ctx, "admin", "administrator-reset"); err != nil {
		t.Fatalf("reset lost: %v", err)
	}
	if _, err = s.AuthenticatePassword(ctx, "admin", "user-attempted-password"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("racing password won: %v", err)
	}
	_, token1, err := s.CreateSession(ctx, admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, token2, err := s.CreateSession(ctx, admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, admin.ID, "administrator-reset", "final-user-password"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{token1, token2} {
		if _, _, err = s.AuthenticateSession(ctx, token); !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("session survived: %v", err)
		}
	}
}

func TestUserDisabledExpiredAndLastAdmin(t *testing.T) {
	s, clock, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	u, c := setupUser(t, s, userSpec("user", 100, 100, 0))
	id, err := s.AuthenticateCredential(ctx, c.Token)
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := s.AuthenticatePassword(ctx, "admin", "correct horse battery")
	no := false
	if _, err = s.UpdateUser(ctx, admin.ID, u.ID, UserPatch{Enabled: &no}); err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled admit: %v", err)
	}
	if _, err = s.UpdateUser(ctx, admin.ID, admin.ID, UserPatch{Enabled: &no}); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled last admin: %v", err)
	}
	yes := true
	expiry := clock.Now().Add(time.Minute)
	if _, err = s.UpdateUser(ctx, admin.ID, u.ID, UserPatch{Enabled: &yes, ExpiresAt: &expiry}); err != nil {
		t.Fatal(err)
	}
	clock.Set(expiry)
	if _, err = s.AuthenticateCredential(ctx, c.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired user auth: %v", err)
	}
}

func TestConcurrentQuotaNeverExceededAndSharedAcrossCredentials(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	u, c1 := setupUser(t, s, userSpec("user", 25, 1000, 1000))
	c2, err := s.CreateCredential(ctx, u.ID, u.ID, "tablet", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]Identity, 2)
	ids[0], err = s.AuthenticateCredential(ctx, c1.Token)
	if err != nil {
		t.Fatal(err)
	}
	ids[1], err = s.AuthenticateCredential(ctx, c2.Token)
	if err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.Admit(ctx, ids[i%2])
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, ErrQuotaExceeded) {
				t.Errorf("admit: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := admitted.Load(); got != 25 {
		t.Fatalf("admitted=%d want 25", got)
	}
	q, err := s.CurrentQuota(ctx, u.ID)
	if err != nil || q.Used != 25 || q.Remaining != 0 {
		t.Fatalf("quota=%+v err=%v", q, err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Minute)
	global, err := s.Usage(ctx, "", from, to, Page{})
	if err != nil || len(global.Items) != 1 || global.Items[0].Count != 25 {
		t.Fatalf("global=%+v err=%v", global, err)
	}
	user, err := s.Usage(ctx, u.ID, from, to, Page{})
	if err != nil || len(user.Items) != 1 || user.Items[0].Count != 25 {
		t.Fatalf("user=%+v err=%v", user, err)
	}
}

func TestQPSSharedAcrossDevices(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	u, c1 := setupUser(t, s, userSpec("user", 100, 2, 0))
	c2, err := s.CreateCredential(ctx, u.ID, u.ID, "tablet", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := s.AuthenticateCredential(ctx, c1.Token)
	id2, _ := s.AuthenticateCredential(ctx, c2.Token)
	if err = s.Admit(ctx, id1); err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, id2); err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, id1); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("shared qps: %v", err)
	}
}

func TestDailyMonthlyBoundaryAndRestart(t *testing.T) {
	start := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)
	s, clock, path := newTestStore(t, start)
	ctx := context.Background()
	u, c := setupUser(t, s, userSpec("user", 2, 100, 0))
	id, _ := s.AuthenticateCredential(ctx, c.Token)
	if err := s.Admit(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	s = reopened
	t.Cleanup(func() { _ = reopened.Close() })
	q, err := s.CurrentQuota(ctx, u.ID)
	if err != nil || q.Used != 1 {
		t.Fatalf("restart quota=%+v err=%v", q, err)
	}
	if err = s.Admit(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, id); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("pre-boundary quota: %v", err)
	}
	clock.Set(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err = s.Admit(ctx, id); err != nil {
		t.Fatalf("daily reset: %v", err)
	}
	monthly := PeriodMonthly
	if _, err = s.UpdateUser(ctx, mustAdmin(t, s).ID, u.ID, UserPatch{Period: &monthly}); !errors.Is(err, ErrConflict) {
		t.Fatalf("period switch after use: %v", err)
	}
}

func mustAdmin(t *testing.T, s *Store) User {
	t.Helper()
	u, err := s.AuthenticatePassword(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestMonthlyBoundary(t *testing.T) {
	s, clock, _ := newTestStore(t, time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC))
	ctx := context.Background()
	spec := userSpec("monthly", 1, 100, 0)
	spec.Period = PeriodMonthly
	u, c := setupUser(t, s, spec)
	id, _ := s.AuthenticateCredential(ctx, c.Token)
	if err := s.Admit(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(ctx, id); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatal(err)
	}
	clock.Set(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err := s.Admit(ctx, id); err != nil {
		t.Fatalf("monthly reset for %s: %v", u.ID, err)
	}
}

func TestClosedFailsClosedAndPermissions(t *testing.T) {
	s, _, path := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	_, c := setupUser(t, s, userSpec("user", 10, 10, 0))
	id, _ := s.AuthenticateCredential(ctx, c.Token)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, id); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed admit: %v", err)
	}
	if _, err = s.AuthenticateCredential(ctx, c.Token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed auth: %v", err)
	}
}

func TestSecretsAreNotStoredInPlaintextAndAdminInitializationIsOneShot(t *testing.T) {
	s, _, path := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.InitializeAdmin(ctx, adminSpec()); !errors.Is(err, ErrConflict) {
		t.Fatalf("second admin initialization: %v", err)
	}
	_, sessionToken, err := s.CreateSession(ctx, admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.CreateCredential(ctx, admin.ID, admin.ID, "device", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, sessionSecret, _ := splitToken(sessionToken)
	_, credentialSecret, _ := splitToken(issued.Token)
	for _, secret := range []string{adminSpec().Password, sessionSecret, credentialSecret, sessionToken, issued.Token} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("database contains plaintext secret of length %d", len(secret))
		}
	}
}

func TestQuotaHighWaterRejectsClockRollback(t *testing.T) {
	for _, period := range []Period{PeriodDaily, PeriodMonthly} {
		t.Run(string(period), func(t *testing.T) {
			start := time.Date(2026, 1, 31, 23, 59, 0, 0, time.UTC)
			s, clock, _ := newTestStore(t, start)
			ctx := context.Background()
			spec := userSpec("user", 10, 100, 0)
			spec.Period = period
			u, c := setupUser(t, s, spec)
			id, _ := s.AuthenticateCredential(ctx, c.Token)
			if err := s.Admit(ctx, id); err != nil {
				t.Fatal(err)
			}
			future := start.Add(2 * time.Hour)
			clock.Set(future)
			if err := s.Admit(ctx, id); err != nil {
				t.Fatal(err)
			}
			clock.Set(start)
			if _, err := s.CurrentQuota(ctx, u.ID); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("rollback quota: %v", err)
			}
			if err := s.Admit(ctx, id); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("rollback admit: %v", err)
			}
		})
	}
}

func TestUnchangedPeriodPatchPreservesUsage(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	u, c := setupUser(t, s, userSpec("user", 10, 100, 0))
	id, _ := s.AuthenticateCredential(ctx, c.Token)
	if err := s.Admit(ctx, id); err != nil {
		t.Fatal(err)
	}
	limit := uint64(20)
	period := PeriodDaily
	tz := "UTC"
	if _, err := s.UpdateUser(ctx, mustAdmin(t, s).ID, u.ID, UserPatch{Limit: &limit, Period: &period, Timezone: &tz}); err != nil {
		t.Fatal(err)
	}
	q, err := s.CurrentQuota(ctx, u.ID)
	if err != nil || q.Used != 1 || q.Limit != 20 {
		t.Fatalf("quota=%+v err=%v", q, err)
	}
}

func TestBoundedAdmitQueue(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path, Options{Clock: clock, AdmitQueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	_, c := setupUser(t, s, userSpec("user", 10, 100, 0))
	id, _ := s.AuthenticateCredential(ctx, c.Token)
	held := make(chan struct{})
	release := make(chan struct{})
	go func() { _ = s.db.Update(func(*bbolt.Tx) error { close(held); <-release; return nil }) }()
	<-held
	done := make(chan error, 1)
	cancelCtx, cancel := context.WithCancel(ctx)
	go func() { done <- s.Admit(cancelCtx, id) }()
	deadline := time.Now().Add(time.Second)
	for len(s.admitSlots) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err = s.Admit(ctx, id); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("full queue: %v", err)
	}
	cancel()
	close(release)
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled queued admit: %v", err)
	}
	if len(s.admitSlots) != 0 {
		t.Fatalf("slot leaked")
	}
}

func TestCredentialCapacityExpiryAndDeviceUsagePagination(t *testing.T) {
	s, clock, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	spec := userSpec("user", 100, 100, 0)
	spec.MaxCredentials = 1
	u, c1 := setupUser(t, s, spec)
	id1, _ := s.AuthenticateCredential(ctx, c1.Token)
	if err := s.Admit(ctx, id1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCredential(ctx, u.ID, u.ID, "past", clock.Now().Add(-time.Second)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("past expiry: %v", err)
	}
	if err := s.RevokeCredential(ctx, u.ID, u.ID, c1.Credential.ID); err != nil {
		t.Fatal(err)
	}
	c2, err := s.CreateCredential(ctx, u.ID, u.ID, "short", clock.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s.AuthenticateCredential(ctx, c2.Token)
	clock.Set(clock.Now().Add(time.Minute))
	if _, err = s.RotateCredential(ctx, u.ID, u.ID, c2.Credential.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rotate expired: %v", err)
	}
	c3, err := s.CreateCredential(ctx, u.ID, u.ID, "new", time.Time{})
	if err != nil {
		t.Fatalf("expired slot not released: %v", err)
	}
	id3, _ := s.AuthenticateCredential(ctx, c3.Token)
	if err = s.Admit(ctx, id3); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := clock.Now().Add(time.Minute)
	page1, err := s.CredentialUsage(ctx, u.ID, "", from, to, Page{Limit: 1})
	if err != nil || len(page1.Items) != 1 || page1.NextCursor == "" {
		t.Fatalf("page1=%+v err=%v", page1, err)
	}
	page2, err := s.CredentialUsage(ctx, u.ID, "", from, to, Page{Limit: 1, Cursor: page1.NextCursor})
	if err != nil || len(page2.Items) != 1 {
		t.Fatalf("page2=%+v err=%v", page2, err)
	}
	if page1.Items[0].CredentialID == page2.Items[0].CredentialID {
		t.Fatal("device pages duplicated")
	}
	if _, err = s.CredentialUsage(ctx, u.ID, "", from, to, Page{Cursor: "d:other\x00000000000000000000\x00x"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-scope cursor: %v", err)
	}
	tooLate := string(deviceUsageKey(u.ID, to.Add(time.Hour).Unix(), "x"))
	if _, err = s.CredentialUsage(ctx, u.ID, "", from, to, Page{Cursor: tooLate}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("out-of-range cursor: %v", err)
	}
	_ = id2
}

func TestEmptyPagesUseArrays(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	users, err := s.ListUsers(context.Background(), Page{})
	if err != nil || users.Items == nil {
		t.Fatalf("users=%+v err=%v", users, err)
	}
	usage, err := s.Usage(context.Background(), "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC), Page{})
	if err != nil || usage.Items == nil {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

func TestRevokingExpiredCredentialDoesNotReleaseAnotherSlot(t *testing.T) {
	s, clock, _ := newTestStore(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	spec := userSpec("user", 100, 100, 0)
	spec.MaxCredentials = 1
	u, old := setupUser(t, s, spec)
	expires := clock.Now().Add(time.Minute)
	if err := s.RevokeCredential(ctx, u.ID, u.ID, old.Credential.ID); err != nil {
		t.Fatal(err)
	}
	old, err := s.CreateCredential(ctx, u.ID, u.ID, "old", expires)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(expires)
	if _, err = s.CreateCredential(ctx, u.ID, u.ID, "active", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeCredential(ctx, u.ID, u.ID, old.Credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateCredential(ctx, u.ID, u.ID, "must-fail", time.Time{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired revoke released active slot: %v", err)
	}
}

func TestPeriodPolicyChangeResetsHighWaterIntentionally(t *testing.T) {
	dayOne := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	s, clock, _ := newTestStore(t, dayOne)
	ctx := context.Background()
	u, credential := setupUser(t, s, userSpec("user", 100, 100, 0))
	identity, _ := s.AuthenticateCredential(ctx, credential.Token)
	if err := s.Admit(ctx, identity); err != nil {
		t.Fatal(err)
	}
	clock.Set(dayOne.Add(24 * time.Hour))
	monthly := PeriodMonthly
	if _, err := s.UpdateUser(ctx, mustAdmin(t, s).ID, u.ID, UserPatch{Period: &monthly}); err != nil {
		t.Fatalf("policy change: %v", err)
	}
	if err := s.Admit(ctx, identity); err != nil {
		t.Fatalf("new policy admit: %v", err)
	}
	clock.Set(time.Date(2025, 12, 31, 12, 0, 0, 0, time.UTC))
	if err := s.Admit(ctx, identity); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("real rollback after policy change: %v", err)
	}
}
