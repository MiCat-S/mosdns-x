package control

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrateBoltToMySQLPreservesControlRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	source, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := source.InitializeAdmin(ctx, UserSpec{Username: "admin", Password: "admin-password-value", Role: RoleAdmin, Enabled: true, Period: PeriodDaily, Timezone: "UTC", Limit: 100, QPS: 10, Burst: 2, MaxCredentials: 3})
	if err != nil {
		t.Fatal(err)
	}
	user, err := source.CreateUser(ctx, admin.ID, UserSpec{Username: "user", Password: "user-password-value", Role: RoleUser, Enabled: true, Period: PeriodMonthly, Timezone: "Asia/Shanghai", Limit: 50, QPS: 5, Burst: 1, MaxCredentials: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = source.CreateSession(ctx, user.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	issued, err := source.CreateCredential(ctx, user.ID, user.ID, "phone", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := source.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Admit(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	destination := &MySQLStore{db: db, clock: realClock{}, operationTimeout: time.Second, closeCh: make(chan struct{})}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT initialized,")).WillReturnRows(sqlmock.NewRows([]string{"initialized", "users", "sessions", "credentials", "usage", "audit"}).AddRow(false, 0, 0, 0, 0, 0))
	expectExecutions(mock, `INSERT INTO mosdns_users`, 2)
	expectExecutions(mock, `INSERT INTO mosdns_sessions`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_credentials`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_usage_minutes`, 3)
	expectExecutions(mock, `INSERT INTO mosdns_audit_logs`, 3)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_control_meta SET initialized=? WHERE id=1`)).WithArgs(true).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	report, err := MigrateBoltToMySQL(ctx, path, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report != (MySQLMigrationReport{Users: 2, Sessions: 1, Credentials: 1, Usage: 3, Audit: 3}) {
		t.Fatalf("report=%+v", report)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectExecutions(mock sqlmock.Sqlmock, query string, count int) {
	for range count {
		mock.ExpectExec(regexp.QuoteMeta(query)).WillReturnResult(sqlmock.NewResult(1, 1))
	}
}
