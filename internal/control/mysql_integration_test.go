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

// previousReleaseSettingsSelect is what a release with DNS policies sends for
// every query. Rolling the binary back relies on each user having a row.
const previousReleaseSettingsSelect = `SELECT user_id, strip_ecs, block_private_answers, blocked_qtypes_json, custom_block_enabled, custom_allow_enabled, custom_rewrite_enabled, policy_paused_until_ns, updated_at_ns FROM mosdns_dns_policy_settings WHERE user_id=?`

func TestMySQLIntegrationRetiredPolicyRowsKeepRollbackCompatible(t *testing.T) {
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
	user, err := store.CreateUser(ctx, admin.ID, userSpec("rollback-user", 10, 10, 2))
	if err != nil {
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
	for _, id := range []string{admin.ID, user.ID} {
		var userID, qtypes string
		var strip, private, block, allow, rewrite bool
		var paused sql.NullInt64
		var updated int64
		if err := db.QueryRowContext(ctx, previousReleaseSettingsSelect, id).Scan(&userID, &strip, &private, &qtypes, &block, &allow, &rewrite, &paused, &updated); err != nil {
			t.Fatalf("previous release select for %s: %v", id, err)
		}
		if qtypes != "[]" || strip || private || !block || !allow || !rewrite || paused.Valid {
			t.Fatalf("retired defaults for %s: qtypes=%q strip=%v private=%v block=%v allow=%v rewrite=%v paused=%v", id, qtypes, strip, private, block, allow, rewrite, paused)
		}
	}
}

// seedRetiredMySQLRows leaves rows in the retired policy and public list
// tables, as a database last used by an older binary would hold.
func seedRetiredMySQLRows(t *testing.T, dsn, userID string) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UnixNano()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO mosdns_dns_policy_rules (id, user_id, enabled, priority, action, match_kind, pattern, created_at_ns, updated_at_ns)
			VALUES (?, ?, TRUE, 10, 'block', 'exact', 'kept.example', ?, ?)`, []any{"rule-" + userID, userID, now, now}},
		{`INSERT INTO mosdns_public_lists (id, name, name_normalized, category, url, format, enabled, default_enabled, published, sha256, refresh_seconds, created_at_ns, updated_at_ns)
			VALUES ('list-1', 'Ads', 'ads', '', 'https://example.com/ads.txt', 'mosdns', TRUE, TRUE, TRUE, ?, 300, ?, ?)`, []any{strings.Repeat("a", 64), now, now}},
		{`INSERT INTO mosdns_user_public_lists (user_id, list_id, enabled) VALUES (?, 'list-1', FALSE)`, []any{userID}},
	}
	for _, st := range statements {
		if _, err := db.Exec(st.query, st.args...); err != nil {
			t.Fatal(err)
		}
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
	seedRetiredMySQLRows(t, dsn, f.victim.ID)
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
