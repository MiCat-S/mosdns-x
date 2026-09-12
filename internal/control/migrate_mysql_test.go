package control

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"go.etcd.io/bbolt"
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
	stripECS := true
	qtypes := []string{"AAAA"}
	if _, err := source.UpdateDNSPolicySettings(ctx, user.ID, user.ID, DNSPolicySettingsPatch{StripECS: &stripECS, BlockedQTypes: &qtypes}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateDNSPolicyRule(ctx, user.ID, user.ID, DNSPolicyRuleSpec{Enabled: true, Priority: 10, Action: DNSPolicyBlock, Match: DNSPolicyMatchSuffix, Pattern: "ads.example"}); err != nil {
		t.Fatal(err)
	}
	publicList, err := source.CreatePublicList(ctx, admin.ID, PublicListSpec{Name: "Primary", URL: "https://example.com/list", Format: PublicListFormatMosDNS, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if err := source.SetUserPublicList(ctx, user.ID, user.ID, publicList.ID, &disabled); err != nil {
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT initialized,")).WillReturnRows(sqlmock.NewRows([]string{"initialized", "users", "policy_settings", "policy_rules", "public_lists", "list_overrides", "sessions", "credentials", "usage", "audit"}).AddRow(false, 0, 0, 0, 0, 0, 0, 0, 0, 0))
	expectExecutions(mock, `INSERT INTO mosdns_users`, 2)
	expectExecutions(mock, `INSERT INTO mosdns_dns_policy_settings`, 2)
	expectExecutions(mock, `INSERT INTO mosdns_dns_policy_rules`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_public_lists`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_user_public_lists`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_sessions`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_credentials`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_usage_minutes`, 3)
	expectExecutions(mock, `INSERT INTO mosdns_audit_logs`, 7)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_control_meta SET initialized=? WHERE id=1`)).WithArgs(true).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	report, err := MigrateBoltToMySQL(ctx, path, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report != (MySQLMigrationReport{Users: 2, PolicySettings: 2, PolicyRules: 1, Sessions: 1, Credentials: 1, Usage: 3, Audit: 7, PublicLists: 1, ListOverrides: 1}) {
		t.Fatalf("report=%+v", report)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateBoltV2ToMySQLSynthesizesPolicyDefaults(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control-v2.db")
	source, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.InitializeAdmin(ctx, adminSpec()); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		for _, bucket := range [][]byte{bDNSPolicySettings, bDNSPolicyRules, bUserDNSPolicyRules} {
			if err := tx.DeleteBucket(bucket); err != nil {
				return err
			}
		}
		var version [8]byte
		binary.BigEndian.PutUint64(version[:], credentialSchemaVersion)
		return tx.Bucket(bMeta).Put(kSchema, version[:])
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	report, err := InspectBoltForMySQL(ctx, path)
	if err != nil || report.Users != 1 || report.PolicySettings != 1 || report.PolicyRules != 0 {
		t.Fatalf("inspect report=%+v err=%v", report, err)
	}
	mysqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mysqlDB.Close()
	destination := &MySQLStore{db: mysqlDB, clock: realClock{}, operationTimeout: time.Second, closeCh: make(chan struct{})}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT initialized,")).WillReturnRows(sqlmock.NewRows([]string{"initialized", "users", "policy_settings", "policy_rules", "public_lists", "list_overrides", "sessions", "credentials", "usage", "audit"}).AddRow(false, 0, 0, 0, 0, 0, 0, 0, 0, 0))
	expectExecutions(mock, `INSERT INTO mosdns_users`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_dns_policy_settings`, 1)
	expectExecutions(mock, `INSERT INTO mosdns_audit_logs`, 1)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_control_meta SET initialized=? WHERE id=1`)).WithArgs(true).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	report, err = MigrateBoltToMySQL(ctx, path, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report != (MySQLMigrationReport{Users: 1, PolicySettings: 1, Audit: 1}) {
		t.Fatalf("migration report=%+v", report)
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
