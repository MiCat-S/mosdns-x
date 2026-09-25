package control

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
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
	if err != nil || settings.BlockedQTypes == nil || len(settings.BlockedQTypes) != 0 || !settings.CustomBlockEnabled || !settings.CustomAllowEnabled || !settings.CustomRewriteEnabled || settings.PolicyPausedUntil != nil {
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
	defaultEnabled := true
	publicList, err := store.CommitPublicListSnapshot(ctx, admin.ID, "mysql-public-list", time.Time{}, PublicListSpec{
		Name: "Ads", URL: "https://example.com/ads.txt", Format: PublicListFormatMosDNS,
		DefaultEnabled: &defaultEnabled, RefreshSeconds: 300,
	}, PublicListRefreshResult{
		Status: PublicListRefreshSuccess, EntryCount: 12, SHA256: strings.Repeat("a", 64), RefreshedAt: time.Now().UTC(),
	})
	if err != nil || !publicList.Published || !publicList.DefaultEnabled || publicList.SnapshotStatus != PublicListSnapshotCurrent {
		t.Fatalf("public list=%+v err=%v", publicList, err)
	}
	userLists, err := store.ListUserPublicLists(ctx, user.ID, Page{})
	if err != nil || len(userLists.Items) != 1 || !userLists.Items[0].Enabled || userLists.Items[0].Overridden {
		t.Fatalf("user public lists=%+v err=%v", userLists, err)
	}
	disabled := false
	if err := store.SetUserPublicList(ctx, user.ID, user.ID, publicList.ID, &disabled); err != nil {
		t.Fatal(err)
	}
	userLists, err = store.ListUserPublicLists(ctx, user.ID, Page{})
	if err != nil || len(userLists.Items) != 1 || userLists.Items[0].Enabled || !userLists.Items[0].Overridden {
		t.Fatalf("overridden public list=%+v err=%v", userLists, err)
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
		`DROP TABLE IF EXISTS mosdns_user_public_lists`,
		`DROP TABLE IF EXISTS mosdns_public_lists`,
		`DROP TABLE IF EXISTS mosdns_users`,
		`DROP TABLE IF EXISTS mosdns_usage_minutes`,
		`DROP TABLE IF EXISTS mosdns_audit_logs`,
		`DROP TABLE IF EXISTS mosdns_control_meta`,
		`DELETE FROM mosdns_schema_migrations WHERE component='control'`,
	} {
		_, _ = db.Exec(statement)
	}
}

// The statements below are what the previous release sends, copied verbatim
// with no answer_family. Rolling the binary back relies on them working
// against the upgraded table: the column must be optional to insert, invisible
// to explicit selects, and untouched by an update that does not name it.
const (
	previousReleaseSettingsSelect = `SELECT user_id, strip_ecs, block_private_answers, blocked_qtypes_json, custom_block_enabled, custom_allow_enabled, custom_rewrite_enabled, policy_paused_until_ns, updated_at_ns FROM mosdns_dns_policy_settings WHERE user_id=?`
	previousReleaseSettingsUpdate = `UPDATE mosdns_dns_policy_settings SET strip_ecs=?, block_private_answers=?, blocked_qtypes_json=?, custom_block_enabled=?, custom_allow_enabled=?, custom_rewrite_enabled=?, policy_paused_until_ns=?, updated_at_ns=? WHERE user_id=?`
)

func TestMySQLIntegrationAnswerFamilyKeepsRollbackCompatible(t *testing.T) {
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
	defer store.Close()
	admin, err := store.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	family := AnswerFamilyIPv4
	if _, err := store.UpdateDNSPolicySettings(ctx, admin.ID, admin.ID, DNSPolicySettingsPatch{AnswerFamily: &family}); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The version row must not move, or the previous release refuses to start.
	var version int
	if err := db.QueryRowContext(ctx, `SELECT version FROM mosdns_schema_migrations WHERE component='control'`).Scan(&version); err != nil || version != mysqlControlSchemaVersion {
		t.Fatalf("schema version=%d err=%v, want %d so the previous release still opens it", version, err, mysqlControlSchemaVersion)
	}

	// The previous release's explicit select and update keep working.
	var userID, qtypes string
	var strip, private, block, allow, rewrite bool
	var paused sql.NullInt64
	var updated int64
	if err := db.QueryRowContext(ctx, previousReleaseSettingsSelect, admin.ID).Scan(&userID, &strip, &private, &qtypes, &block, &allow, &rewrite, &paused, &updated); err != nil {
		t.Fatalf("previous release select: %v", err)
	}
	if _, err := db.ExecContext(ctx, previousReleaseSettingsUpdate, true, private, qtypes, block, allow, rewrite, nil, time.Now().UnixNano(), admin.ID); err != nil {
		t.Fatalf("previous release update: %v", err)
	}
	// An update that does not name the column leaves the preference in place,
	// so rolling forward again does not lose it.
	settings, err := store.GetDNSPolicySettings(ctx, admin.ID)
	if err != nil || settings.AnswerFamily != AnswerFamilyIPv4 || !settings.StripECS {
		t.Fatalf("after previous release update: settings=%+v err=%v", settings, err)
	}

	// A row the previous release inserts, without the column, reads as no
	// preference. Reusing the admin's id is avoided by inserting for a new user.
	user, err := store.CreateUser(ctx, admin.ID, userSpec("rollback-user", 10, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM mosdns_dns_policy_settings WHERE user_id=?`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO mosdns_dns_policy_settings
		(user_id, strip_ecs, block_private_answers, blocked_qtypes_json, custom_block_enabled,
		 custom_allow_enabled, custom_rewrite_enabled, policy_paused_until_ns, updated_at_ns)
		VALUES (?, FALSE, FALSE, '[]', TRUE, TRUE, TRUE, NULL, ?)`, user.ID, time.Now().UnixNano()); err != nil {
		t.Fatalf("previous release insert: %v", err)
	}
	if settings, err := store.GetDNSPolicySettings(ctx, user.ID); err != nil || settings.AnswerFamily != AnswerFamilyAny {
		t.Fatalf("row from previous release: settings=%+v err=%v", settings, err)
	}
}

func TestMySQLIntegrationDeleteUser(t *testing.T) {
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
	defer store.Close()
	started := time.Now().Add(-time.Hour)
	f := populateForDeletion(t, store)
	if err := store.DeleteUser(ctx, f.admin.ID, f.admin.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("self delete err=%v", err)
	}
	if err := store.DeleteUser(ctx, f.victim.ID, f.other.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin delete err=%v", err)
	}
	if err := store.DeleteUser(ctx, f.admin.ID, f.victim.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(ctx, f.admin.ID, f.victim.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err=%v", err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"mosdns_users", "mosdns_sessions", "mosdns_credentials", "mosdns_usage_minutes", "mosdns_audit_logs", "mosdns_dns_policy_settings", "mosdns_dns_policy_rules", "mosdns_public_lists", "mosdns_user_public_lists"} {
		rows, err := db.QueryContext(ctx, `SELECT * FROM `+table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		values := make([]sql.RawBytes, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			row := strings.Builder{}
			for _, v := range values {
				row.Write(v)
				row.WriteByte('|')
			}
			if !strings.Contains(row.String(), f.victim.ID) {
				continue
			}
			if table == "mosdns_audit_logs" && strings.Contains(row.String(), "|delete_user|") {
				continue
			}
			t.Errorf("%s still holds %q", table, row.String())
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	checkUserDeleted(t, store, f, started)
}
