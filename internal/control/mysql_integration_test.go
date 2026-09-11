package control

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestMySQLIntegrationControlLifecycle(t *testing.T) {
	dsn := os.Getenv("MOSDNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MOSDNS_TEST_MYSQL_DSN is not set")
	}
	ctx := context.Background()
	cleanupMySQLControlTables(t, dsn)
	t.Cleanup(func() { cleanupMySQLControlTables(t, dsn) })
	store, err := OpenMySQL(MySQLOptions{DSN: dsn, OperationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticatePassword(ctx, admin.Username, adminSpec().Password); err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, admin.ID, userSpec("mysql-user", 10, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.GetDNSPolicySettings(ctx, user.ID)
	if err != nil || settings.BlockedQTypes == nil || len(settings.BlockedQTypes) != 0 {
		t.Fatalf("default policy settings=%+v err=%v", settings, err)
	}
	rule, err := store.CreateDNSPolicyRule(ctx, user.ID, user.ID, DNSPolicyRuleSpec{Enabled: true, Priority: 10, Action: DNSPolicyRewrite, Match: DNSPolicyMatchExact, Pattern: "internal.example", RecordType: DNSPolicyRewriteA, Value: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := store.ListDNSPolicyRules(ctx, user.ID, Page{})
	if err != nil || len(rules.Items) != 1 || rules.Items[0].ID != rule.ID {
		t.Fatalf("policy rules=%+v err=%v", rules, err)
	}
	issued, err := store.CreateCredential(ctx, user.ID, user.ID, "phone", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !validUUIDv4(issued.Token) {
		t.Fatalf("token=%q", issued.Token)
	}
	identity, err := store.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Admit(ctx, identity); err != nil {
		t.Fatal(err)
	}
	quota, err := store.CurrentQuota(ctx, user.ID)
	if err != nil || quota.Used != 1 {
		t.Fatalf("quota=%+v err=%v", quota, err)
	}
	usage, err := store.Usage(ctx, user.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), Page{})
	if err != nil || len(usage.Items) != 1 || usage.Items[0].Count != 1 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	rotated, err := store.RotateCredential(ctx, user.ID, user.ID, issued.Credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticateCredential(ctx, issued.Token); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("old token error=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenMySQL(MySQLOptions{DSN: dsn, OperationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.AuthenticateCredential(ctx, rotated.Token); err != nil {
		t.Fatal(err)
	}
	quota, err = reopened.CurrentQuota(ctx, user.ID)
	if err != nil || quota.Used != 1 {
		t.Fatalf("reopened quota=%+v err=%v", quota, err)
	}
}

func cleanupMySQLControlTables(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`DROP TABLE IF EXISTS mosdns_sessions`,
		`DROP TABLE IF EXISTS mosdns_credentials`,
		`DROP TABLE IF EXISTS mosdns_dns_policy_rules`,
		`DROP TABLE IF EXISTS mosdns_dns_policy_settings`,
		`DROP TABLE IF EXISTS mosdns_users`,
		`DROP TABLE IF EXISTS mosdns_usage_minutes`,
		`DROP TABLE IF EXISTS mosdns_audit_logs`,
		`DROP TABLE IF EXISTS mosdns_control_meta`,
		`DELETE FROM mosdns_schema_migrations WHERE component='control'`,
	} {
		_, _ = db.Exec(statement)
	}
}
