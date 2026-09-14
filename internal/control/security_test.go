package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"go.etcd.io/bbolt"
)

func TestConcurrentCredentialLimitAndRevocation(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	spec := adminSpec()
	spec.MaxCredentials = 3
	u, err := s.InitializeAdmin(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	issued := make(chan IssuedCredential, 32)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, err := s.CreateCredential(ctx, u.ID, u.ID, fmt.Sprintf("device-%d", i), time.Time{})
			switch {
			case err == nil:
				accepted.Add(1)
				issued <- c
			case errors.Is(err, ErrConflict):
			default:
				t.Errorf("create credential: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(issued)
	if accepted.Load() != 3 {
		t.Fatalf("accepted=%d want=3", accepted.Load())
	}
	for c := range issued {
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.RevokeCredential(ctx, u.ID, u.ID, c.Credential.ID); err != nil {
					t.Errorf("revoke credential: %v", err)
				}
			}()
		}
	}
	wg.Wait()
	if err := s.view(ctx, func(tx *bbolt.Tx) error {
		current, err := getUserRecord(tx, u.ID)
		if err != nil {
			return err
		}
		if current.CredentialCount != 0 || tx.Bucket(bActiveCredentials).Stats().KeyN != 0 {
			t.Errorf("count=%d active=%d", current.CredentialCount, tx.Bucket(bActiveCredentials).Stats().KeyN)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialCleanupUnderflowRollsBack(t *testing.T) {
	s, clock, _ := newTestStore(t, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	u, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateCredential(ctx, u.ID, u.ID, "expired", clock.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(ctx, func(tx *bbolt.Tx) error {
		current, err := getUserRecord(tx, u.ID)
		if err != nil {
			return err
		}
		current.CredentialCount = 0
		return marshalPut(tx.Bucket(bUsers), []byte(u.ID), current)
	}); err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(time.Hour))
	if _, err := s.CreateCredential(ctx, u.ID, u.ID, "new", time.Time{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("corrupt count must fail closed: %v", err)
	}
	if err := s.view(ctx, func(tx *bbolt.Tx) error {
		if tx.Bucket(bActiveCredentials).Get(userCredentialKey(u.ID, c.Credential.ID)) == nil {
			t.Error("failed transaction committed an index deletion")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordFailuresPreserveAuthenticationAndStorageErrors(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	spec := userSpec("disabled-user", 100, 100, 0)
	spec.Enabled = false
	if _, err := s.CreateUser(ctx, admin.ID, spec); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, password string }{
		{"missing-user", "correct horse battery"},
		{"admin", "incorrect-password"},
		{"disabled-user", spec.Password},
	} {
		if _, err := s.AuthenticatePassword(ctx, tc.name, tc.password); !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("password auth for %s: %v", tc.name, err)
		}
		if _, _, err := s.Login(ctx, tc.name, tc.password, time.Hour); !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("login for %s: %v", tc.name, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := s.Login(canceled, admin.Username, adminSpec().Password, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled login: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, admin.Username, adminSpec().Password, time.Hour); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed storage login: %v", err)
	}
}

func TestConsumeRateTokenBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		tokens    float64
		rateAt    int64
		want      float64
		wantError error
	}{
		{"initialize", 0, 0, 11, nil},
		{"fractional refill", 0, now.Add(-150 * time.Millisecond).UnixNano(), 0.5, nil},
		{"clamp to capacity", 100, now.UnixNano(), 11, nil},
		{"long elapsed interval", 0, math.MinInt64, 11, nil},
		{"rollback empty", 0, now.Add(time.Hour).UnixNano(), 0, ErrRateLimited},
		{"rollback existing tokens", 3, now.Add(time.Hour).UnixNano(), 2, nil},
		{"same instant empty", 0, now.UnixNano(), 0, ErrRateLimited},
		{"nan", math.NaN(), now.UnixNano(), 0, ErrUnavailable},
		{"infinity", math.Inf(1), now.UnixNano(), 0, ErrUnavailable},
		{"negative", -1, now.UnixNano(), 0, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := userRecord{User: User{QPS: 10, Burst: 2}, RateTokens: tc.tokens, RateAt: tc.rateAt}
			if err := consumeRateToken(&u, now); !errors.Is(err, tc.wantError) {
				t.Fatalf("error=%v want=%v", err, tc.wantError)
			}
			if tc.wantError == nil && math.Abs(u.RateTokens-tc.want) > 0.000001 {
				t.Fatalf("tokens=%v want=%v", u.RateTokens, tc.want)
			}
			if tc.rateAt > now.UnixNano() && u.RateAt != tc.rateAt {
				t.Fatal("clock rollback reset the refill high-water mark")
			}
		})
	}
}

func TestInvalidRateStateDoesNotConsumeQuota(t *testing.T) {
	s, _, _ := newTestStore(t, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	u, issued := setupUser(t, s, userSpec("invalid-rate", 100, 100, 0))
	id, err := s.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(ctx, func(tx *bbolt.Tx) error {
		current, err := getUserRecord(tx, u.ID)
		if err != nil {
			return err
		}
		current.RateTokens = -1
		return marshalPut(tx.Bucket(bUsers), []byte(u.ID), current)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(ctx, id); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("admission=%v", err)
	}
	quota, err := s.CurrentQuota(ctx, u.ID)
	if err != nil || quota.Used != 0 {
		t.Fatalf("quota=%+v err=%v", quota, err)
	}
}

func TestMySQLInvalidRateStateRollsBackWithoutConsumingQuota(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	s := &MySQLStore{db: db, clock: &fakeClock{t: now}, operationTimeout: time.Second}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).
		WithArgs("user-1").WillReturnRows(mysqlTestUserRow(now, 0, -1, now.UnixNano()))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlCredentialColumns + ` FROM mosdns_credentials WHERE id=? FOR UPDATE`)).
		WithArgs("credential-1").WillReturnRows(mysqlTestCredentialRow(now, make([]byte, 32)))
	mock.ExpectRollback()
	if err := s.Admit(context.Background(), Identity{UserID: "user-1", CredentialID: "credential-1", CredentialVersion: 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("admission=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLTransactionAlwaysFinishes(t *testing.T) {
	for _, outcome := range []string{"commit", "domain_error", "panic", "commit_error"} {
		t.Run(outcome, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := &MySQLStore{db: db, operationTimeout: time.Second}
			mock.ExpectBegin()
			switch outcome {
			case "commit":
				mock.ExpectCommit()
			case "commit_error":
				mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			default:
				mock.ExpectRollback()
			}
			var got error
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				got = s.withTx(context.Background(), func(context.Context, *sql.Tx) error {
					if outcome == "panic" {
						panic("transaction-test")
					}
					if outcome == "domain_error" {
						return ErrForbidden
					}
					return nil
				})
			}()
			if outcome == "panic" && recovered != "transaction-test" || outcome != "panic" && recovered != nil {
				t.Fatalf("panic=%v", recovered)
			}
			if outcome == "domain_error" && !errors.Is(got, ErrForbidden) || outcome == "commit_error" && !errors.Is(got, ErrUnavailable) || outcome == "commit" && got != nil {
				t.Fatalf("error=%v", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if got := db.Stats().InUse; got != 0 {
				t.Fatalf("transaction retained %d connections", got)
			}
		})
	}
}

func TestOpenMySQLContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenMySQLContext(ctx, MySQLOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled startup=%v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	const network = "control-security-test"
	started := make(chan struct{})
	mysqlDriver.RegisterDialContext(network, func(ctx context.Context, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer mysqlDriver.DeregisterDialContext(network)
	done := make(chan error, 1)
	go func() {
		store, err := OpenMySQLContext(ctx, MySQLOptions{DSN: "test:test@" + network + "(local)/mosdns"})
		if store != nil {
			_ = store.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("startup ended before dialing: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("startup did not dial")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled dial=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startup ignored cancellation")
	}
}
