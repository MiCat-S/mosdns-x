package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"go.etcd.io/bbolt"
)

func TestBoltHealthReadOnlyCountsAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	s, clock, _ := newTestStore(t, now)
	ctx := context.Background()
	user, _ := setupUser(t, s, userSpec("health-user", 100, 10, 10))
	if _, _, err := s.CreateSession(ctx, user.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	expired, _, err := s.CreateSession(ctx, user.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	revoked, _, err := s.CreateSession(ctx, user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, user.ID, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCredential(ctx, user.ID, user.ID, "expires", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	clock.t = expired.ExpiresAt.Add(time.Second)
	health, err := s.CollectHealth(ctx)
	if err != nil || health.ActiveSessions == nil || *health.ActiveSessions != 1 ||
		health.CredentialCountMismatches == nil || *health.CredentialCountMismatches != 0 {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	if health.Bolt == nil || health.Bolt.OpenReadTransactions != 0 || health.MySQL != nil {
		t.Fatalf("wrong backend stats: %+v", health)
	}
	// Inject a count mismatch without changing the active index.
	if err := s.update(ctx, func(tx *bbolt.Tx) error {
		var record userRecord
		if err := decode(tx.Bucket(bUsers).Get([]byte(user.ID)), &record); err != nil {
			return err
		}
		record.CredentialCount++
		return marshalPut(tx.Bucket(bUsers), []byte(user.ID), record)
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		health, err = s.CollectHealth(ctx)
		if err != nil || health.CredentialCountMismatches == nil || *health.CredentialCountMismatches != 1 {
			t.Fatalf("mismatch must be reported, not repaired: %+v, %v", health, err)
		}
	}
}

func TestBoltHealthFailureDoesNotReportPartialZeroes(t *testing.T) {
	s, _, _ := newTestStore(t, time.Now())
	user, cred := setupUser(t, s, userSpec("health-user", 100, 10, 10))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name string
		call func() (StorageHealth, error)
	}{
		{"canceled", func() (StorageHealth, error) { return s.CollectHealth(ctx) }},
		{"scan limit", func() (StorageHealth, error) { return s.collectHealth(context.Background(), 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, err := test.call()
			if err == nil || h.ActiveSessions != nil || h.CredentialCountMismatches != nil {
				t.Fatalf("partial collection returned known values: %+v, %v", h, err)
			}
			if test.name == "scan limit" && !errors.Is(err, ErrHealthScanLimit) {
				t.Fatal(err)
			}
		})
	}
	if err := s.update(context.Background(), func(tx *bbolt.Tx) error {
		return tx.Bucket(bActiveCredentials).Put(userCredentialKey(user.ID, cred.Credential.ID), []byte("missing-record"))
	}); err != nil {
		t.Fatal(err)
	}
	if h, err := s.CollectHealth(context.Background()); err != nil || h.CredentialCountMismatches == nil ||
		*h.CredentialCountMismatches != 1 || h.ActiveSessions == nil || h.Bolt == nil {
		t.Fatalf("dangling index must be reported without discarding the scan: %+v, %v", h, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CollectHealth(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed store: %v", err)
	}
}

func TestMySQLHealthAndRollbackCounters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(7)
	now := time.Now()
	s := &MySQLStore{db: db, clock: &fakeClock{t: now}, operationTimeout: time.Second}
	mock.ExpectQuery("SELECT COUNT").WithArgs(now.UTC().UnixNano()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	h, err := s.CollectHealth(context.Background())
	if err != nil || h.ActiveSessions == nil || *h.ActiveSessions != 3 || h.MySQL == nil ||
		h.MySQL.MaxOpen != 7 || h.CredentialCountMismatches != nil {
		t.Fatalf("health=%+v err=%v", h, err)
	}
	mock.ExpectBegin()
	mock.ExpectRollback().WillReturnError(errors.New("rollback failed"))
	if err := s.withTx(context.Background(), func(context.Context, *sql.Tx) error { return ErrForbidden }); !errors.Is(err, ErrForbidden) {
		t.Fatalf("changed business error: %v", err)
	}
	if s.rollbackErrors.Load() != 1 {
		t.Fatal("rollback failure was not observed")
	}
	mock.ExpectQuery("SELECT COUNT").WillReturnError(errors.New("private DSN detail"))
	h, err = s.CollectHealth(context.Background())
	if err == nil || h.ActiveSessions != nil || h.MySQL == nil || h.MySQL.RollbackErrors != 1 {
		t.Fatal("failed SQL query must retain independent driver counters, not session counts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CollectHealth(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not respected")
	}
	// No SQL expectation is installed: runtime collection must not issue
	// queries, ping the server, or start a transaction after a failed scan.
	if runtime := s.RuntimeHealth(); runtime.MySQL == nil || runtime.MySQL.RollbackErrors != 1 {
		t.Fatal("independent runtime counters unavailable")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHealthIndexCorruptionIsCountedWithoutRepair(t *testing.T) {
	for _, kind := range []string{"missing record", "invalid record", "wrong owner", "wrong id", "revoked record", "unattributable index", "unknown owner", "multiple faults for one user"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _ := newTestStore(t, time.Now())
			u, c := setupUser(t, s, userSpec("integrity", 100, 10, 10))
			ctx := context.Background()
			if _, _, err := s.CreateSession(ctx, u.ID, time.Hour); err != nil {
				t.Fatal(err)
			}
			if err := s.update(ctx, func(tx *bbolt.Tx) error {
				key := []byte(c.Credential.ID)
				switch kind {
				case "missing record":
					return tx.Bucket(bCredentials).Delete(key)
				case "invalid record":
					return tx.Bucket(bCredentials).Put(key, []byte("{bad json"))
				case "unattributable index":
					return tx.Bucket(bActiveCredentials).Put([]byte("broken-key"), key)
				case "unknown owner":
					return tx.Bucket(bActiveCredentials).Put(userCredentialKey("absent-user", c.Credential.ID), key)
				case "multiple faults for one user":
					if err := tx.Bucket(bCredentials).Delete(key); err != nil {
						return err
					}
					return tx.Bucket(bActiveCredentials).Put(userCredentialKey(u.ID, "another-missing"), []byte("another-missing"))
				default:
					var record credentialRecord
					if err := decode(tx.Bucket(bCredentials).Get(key), &record); err != nil {
						return err
					}
					if kind == "wrong owner" {
						record.UserID = "absent-user"
					} else if kind == "revoked record" {
						record.RevokedAt = time.Now()
					} else {
						record.ID = "different-credential"
					}
					return marshalPut(tx.Bucket(bCredentials), key, record)
				}
			}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				h, err := s.CollectHealth(ctx)
				if err != nil || h.CredentialCountMismatches == nil || *h.CredentialCountMismatches != 1 ||
					h.ActiveSessions == nil || *h.ActiveSessions != 1 || h.Bolt == nil {
					t.Fatalf("corruption disappeared or discarded unrelated data: %+v %v", h, err)
				}
			}
		})
	}
}

func TestHealthConfigurableBudgetForLargeStore(t *testing.T) {
	now := time.Now()
	s, err := Open(filepath.Join(t.TempDir(), "large.db"), Options{HealthScanLimit: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// 10k users + 40k active indexes + 10k sessions exceeds the old 50k cap.
	if err := s.update(context.Background(), func(tx *bbolt.Tx) error {
		for i := range 10_000 {
			id := fmt.Sprintf("user-%d", i)
			if err := marshalPut(tx.Bucket(bUsers), []byte(id), userRecord{
				User: User{ID: id, Enabled: true}, CredentialCount: 4,
			}); err != nil {
				return err
			}
			for j := range 4 {
				cid := fmt.Sprintf("cred-%d-%d", i, j)
				if err := marshalPut(tx.Bucket(bCredentials), []byte(cid), credentialRecord{
					Credential: Credential{ID: cid, UserID: id},
				}); err != nil {
					return err
				}
				if err := tx.Bucket(bActiveCredentials).Put(userCredentialKey(id, cid), []byte(cid)); err != nil {
					return err
				}
			}
			if err := marshalPut(tx.Bucket(bSessions), []byte(id), sessionRecord{
				Session: Session{UserID: id, ExpiresAt: now.Add(time.Hour)},
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h, err := s.collectHealth(context.Background(), DefaultHealthScanLimit)
	if !errors.Is(err, ErrHealthScanLimit) || h.ActiveSessions != nil || h.CredentialCountMismatches != nil || h.Bolt == nil {
		t.Fatalf("bounded scan should retain only runtime stats: %+v %v", h, err)
	}
	h, err = s.CollectHealth(context.Background())
	if err != nil || h.ActiveSessions == nil || *h.ActiveSessions != 10_000 ||
		h.CredentialCountMismatches == nil || *h.CredentialCountMismatches != 0 {
		t.Fatalf("configured scan failed: %+v %v", h, err)
	}
	for _, limit := range []int{-1, MaxHealthScanLimit + 1} {
		if _, err := Open(filepath.Join(t.TempDir(), "invalid.db"), Options{HealthScanLimit: limit}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid scan limit accepted: %d %v", limit, err)
		}
	}
}

// Scrapes and /admin/health call RuntimeHealth on the request path, so it must
// not queue behind a Close that is itself waiting on a long read transaction.
func TestRuntimeHealthNeverBlocksBehindPendingClose(t *testing.T) {
	s, _, _ := newTestStore(t, time.Now())
	if h := s.RuntimeHealth(); h.Bolt == nil {
		t.Fatal("open store reported no runtime stats")
	}
	closed := make(chan error, 1)
	func() {
		// Hold the lock a long read transaction such as Backup would hold, then
		// queue Close behind it. sync.RWMutex hands the pending writer priority.
		s.mu.RLock()
		// Release before waiting for Close, including when Fatal/Fatalf exits
		// the test and newTestStore's cleanup also tries to close the store.
		defer s.mu.RUnlock()
		go func() { closed <- s.Close() }()
		time.Sleep(50 * time.Millisecond)
		done := make(chan StorageHealth, 1)
		go func() { done <- s.RuntimeHealth() }()
		select {
		case h := <-done:
			if h.Driver != "bbolt" || h.Bolt == nil {
				t.Fatalf("runtime stats lost while a close was pending: %+v", h)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("RuntimeHealth blocked behind a pending Close")
		}
	}()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if h := s.RuntimeHealth(); h.Bolt != nil || h.Driver != "bbolt" {
		t.Fatalf("closed store must not report bbolt stats: %+v", h)
	}
}
