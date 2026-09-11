package control

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

var mysqlDNSPolicyRuleTestColumns = []string{"id", "user_id", "enabled", "priority", "action", "match_kind", "pattern", "record_type", "rewrite_value", "created_at_ns", "updated_at_ns"}

func mysqlPolicyStore(t *testing.T, now time.Time) (*MySQLStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &MySQLStore{db: db, clock: &fakeClock{t: now}, operationTimeout: time.Second, closeCh: make(chan struct{})}, mock
}

func expectMySQLPolicyOwner(mock sqlmock.Sqlmock, now time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("user-1").WillReturnRows(mysqlTestUserRow(now, 0, 12, now.UnixNano()))
}

func mysqlPolicyRuleRow(rule DNSPolicyRule) *sqlmock.Rows {
	var recordType, value any
	if rule.Action == DNSPolicyRewrite {
		recordType, value = string(rule.RecordType), rule.Value
	}
	return sqlmock.NewRows(mysqlDNSPolicyRuleTestColumns).AddRow(rule.ID, rule.UserID, rule.Enabled, rule.Priority, string(rule.Action), string(rule.Match), rule.Pattern, recordType, value, rule.CreatedAt.UnixNano(), rule.UpdatedAt.UnixNano())
}

func TestMySQLDNSPolicySettingsReadAndUpdate(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	store, mock := mysqlPolicyStore(t, now)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, strip_ecs, block_private_answers, blocked_qtypes_json, updated_at_ns FROM mosdns_dns_policy_settings WHERE user_id=?`)).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"user_id", "strip_ecs", "block_private_answers", "blocked_qtypes_json", "updated_at_ns"}).AddRow("user-1", false, false, []byte(`[]`), now.Add(-time.Hour).UnixNano()))
	settings, err := store.GetDNSPolicySettings(context.Background(), "user-1")
	if err != nil || settings.BlockedQTypes == nil || len(settings.BlockedQTypes) != 0 {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}

	mock.ExpectBegin()
	expectMySQLPolicyOwner(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, strip_ecs, block_private_answers, blocked_qtypes_json, updated_at_ns FROM mosdns_dns_policy_settings WHERE user_id=? FOR UPDATE`)).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"user_id", "strip_ecs", "block_private_answers", "blocked_qtypes_json", "updated_at_ns"}).AddRow("user-1", false, false, []byte(`[]`), now.Add(-time.Hour).UnixNano()))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_dns_policy_settings SET strip_ecs=?, block_private_answers=?, blocked_qtypes_json=?, updated_at_ns=? WHERE user_id=?`)).WithArgs(true, false, []byte(`["AAAA","A"]`), now.UnixNano(), "user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	strip := true
	qtypes := []string{"aaaa", "A", "AAAA"}
	settings, err = store.UpdateDNSPolicySettings(context.Background(), "user-1", "user-1", DNSPolicySettingsPatch{StripECS: &strip, BlockedQTypes: &qtypes})
	if err != nil || !settings.StripECS || len(settings.BlockedQTypes) != 2 {
		t.Fatalf("updated settings=%+v err=%v", settings, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLDNSPolicyRuleCRUDAndPagination(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	store, mock := mysqlPolicyStore(t, now)
	mock.ExpectBegin()
	expectMySQLPolicyOwner(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM mosdns_dns_policy_rules WHERE user_id=?`)).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_dns_policy_rules`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	rule, err := store.CreateDNSPolicyRule(ctx, "user-1", "user-1", DNSPolicyRuleSpec{Enabled: true, Priority: 20, Action: DNSPolicyRewrite, Match: DNSPolicyMatchExact, Pattern: "Host.Example", RecordType: DNSPolicyRewriteAAAA, Value: "2001:0db8::1"})
	if err != nil || rule.Pattern != "host.example." || rule.Value != "2001:db8::1" {
		t.Fatalf("created rule=%+v err=%v", rule, err)
	}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlDNSPolicyRuleColumns + ` FROM mosdns_dns_policy_rules WHERE id=?`)).WithArgs(rule.ID).WillReturnRows(mysqlPolicyRuleRow(rule))
	if got, err := store.GetDNSPolicyRule(ctx, "user-1", rule.ID); err != nil || got.ID != rule.ID {
		t.Fatalf("get=%+v err=%v", got, err)
	}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ?`)).WithArgs("user-1").WillReturnRows(mysqlTestUserRow(now, 0, 12, now.UnixNano()))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+mysqlDNSPolicyRuleColumns+` FROM mosdns_dns_policy_rules WHERE user_id=? ORDER BY priority, id LIMIT ?`)).WithArgs("user-1", 2).WillReturnRows(mysqlPolicyRuleRow(rule))
	page, err := store.ListDNSPolicyRules(ctx, "user-1", Page{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != rule.ID {
		t.Fatalf("page=%+v err=%v", page, err)
	}

	mock.ExpectBegin()
	expectMySQLPolicyOwner(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlDNSPolicyRuleColumns + ` FROM mosdns_dns_policy_rules WHERE id=? FOR UPDATE`)).WithArgs(rule.ID).WillReturnRows(mysqlPolicyRuleRow(rule))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_dns_policy_rules SET`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	action := DNSPolicyBlock
	updated, err := store.UpdateDNSPolicyRule(ctx, "user-1", "user-1", rule.ID, DNSPolicyRulePatch{Action: &action})
	if err != nil || updated.Action != DNSPolicyBlock || updated.RecordType != "" || updated.Value != "" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}

	mock.ExpectBegin()
	expectMySQLPolicyOwner(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlDNSPolicyRuleColumns + ` FROM mosdns_dns_policy_rules WHERE id=? FOR UPDATE`)).WithArgs(rule.ID).WillReturnRows(mysqlPolicyRuleRow(updated))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_dns_policy_rules WHERE id=?`)).WithArgs(rule.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := store.DeleteDNSPolicyRule(ctx, "user-1", "user-1", rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
