package coremain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/pmkol/mosdns-x/internal/control"
	"go.uber.org/zap"
)

type cleanupFixture struct {
	batches int
	pending bool
	fail    error
}

func (f *cleanupFixture) ReusedUserEraseCount(context.Context) (int, error) {
	if f.pending {
		return 1, nil
	}
	return 0, nil
}
func (f *cleanupFixture) DrainUserErases(ctx context.Context, erase func(context.Context, string) (bool, error)) error {
	done, err := erase(ctx, "reused-user")
	if err == nil && done {
		f.pending = false
	}
	return err
}
func (f *cleanupFixture) DeleteUserBatch(context.Context, string) (bool, error) {
	if f.fail != nil {
		return false, f.fail
	}
	f.batches--
	return f.batches <= 0, nil
}

func TestReusedIdentityStartupWaitsForEveryCleanupBatch(t *testing.T) {
	f := &cleanupFixture{batches: 4, pending: true}
	if err := drainReusedUserErases(context.Background(), f, f, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	if f.pending || f.batches != 0 {
		t.Fatalf("listener gate returned before cleanup: pending=%v batches=%d", f.pending, f.batches)
	}
	f = &cleanupFixture{batches: 4, pending: true, fail: context.DeadlineExceeded}
	if err := drainReusedUserErases(context.Background(), f, f, zap.NewNop()); !errors.Is(err, context.DeadlineExceeded) || !f.pending {
		t.Fatalf("failed cleanup must retain gate/job: pending=%v err=%v", f.pending, err)
	}
}

func TestIdentitySyncDiagnosticCausesNeverExposeErrorValues(t *testing.T) {
	err := fmt.Errorf("%w: %w", control.ErrUnavailable, &mysqlDriver.MySQLError{Number: 1205, Message: "do-not-log-token-hash-or-dsn"})
	if cause := identitySyncCause(err); cause != "mysql_1205" || strings.Contains(cause, "do-not-log") {
		t.Fatalf("cause=%s", cause)
	}
	if cause := identitySyncCause(context.DeadlineExceeded); cause != "deadline_exceeded" {
		t.Fatalf("cause=%s", cause)
	}
}
