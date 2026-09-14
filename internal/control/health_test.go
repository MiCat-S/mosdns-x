package control

import (
	"context"
	"database/sql"
	"errors"
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
	if h, err := s.CollectHealth(context.Background()); err == nil || h.CredentialCountMismatches != nil {
		t.Fatalf("corrupt index treated as healthy: %+v, %v", h, err)
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
	if err == nil || h.ActiveSessions != nil || h.MySQL != nil {
		t.Fatal("failed SQL query presented as healthy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CollectHealth(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not respected")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
