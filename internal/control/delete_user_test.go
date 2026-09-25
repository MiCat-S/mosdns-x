package control

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"go.etcd.io/bbolt"
)

// deletionFixture is a user with data in every place a user's data lives,
// next to a bystander whose data must survive the deletion.
type deletionFixture struct {
	admin, other, victim User
	victimToken          string
	victimSession        string
	bystanderToken       string
}

func populateForDeletion(t *testing.T, s Service) deletionFixture {
	t.Helper()
	ctx := context.Background()
	var f deletionFixture
	var err error
	if f.admin, err = s.InitializeAdmin(ctx, adminSpec()); err != nil {
		t.Fatal(err)
	}
	otherSpec := adminSpec()
	otherSpec.Username = "second-admin"
	if f.other, err = s.CreateUser(ctx, f.admin.ID, otherSpec); err != nil {
		t.Fatal(err)
	}
	if f.victim, err = s.CreateUser(ctx, f.admin.ID, userSpec("victim", 100, 10, 2)); err != nil {
		t.Fatal(err)
	}
	bystander, err := s.CreateUser(ctx, f.admin.ID, userSpec("bystander", 100, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.CreateCredential(ctx, bystander.ID, bystander.ID, "laptop", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	f.bystanderToken = issued.Token

	// The victim's own activity, plus admin actions that only name the
	// victim's records.
	kept, err := s.CreateCredential(ctx, f.victim.ID, f.victim.ID, "phone", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if kept, err = s.RotateCredential(ctx, f.admin.ID, f.victim.ID, kept.Credential.ID); err != nil {
		t.Fatal(err)
	}
	f.victimToken = kept.Token
	revoked, err := s.CreateCredential(ctx, f.victim.ID, f.victim.ID, "tablet", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeCredential(ctx, f.admin.ID, f.victim.ID, revoked.Credential.ID); err != nil {
		t.Fatal(err)
	}
	identity, err := s.AuthenticateCredential(ctx, f.victimToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(ctx, identity); err != nil {
		t.Fatal(err)
	}
	ended, _, err := s.Login(ctx, "victim", userSpec("", 0, 0, 0).Password, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, f.admin.ID, ended.ID); err != nil {
		t.Fatal(err)
	}
	_, f.victimSession, err = s.Login(ctx, "victim", userSpec("", 0, 0, 0).Password, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, f.victim.ID, userSpec("", 0, 0, 0).Password, "another horse battery"); err != nil {
		t.Fatal(err)
	}
	strip := true
	if _, err := s.UpdateDNSPolicySettings(ctx, f.victim.ID, f.victim.ID, DNSPolicySettingsPatch{StripECS: &strip}); err != nil {
		t.Fatal(err)
	}
	rule := DNSPolicyRuleSpec{Enabled: true, Priority: 10, Action: DNSPolicyRewrite, Match: DNSPolicyMatchExact, Pattern: "internal.example", RecordType: DNSPolicyRewriteA, Value: "192.0.2.10"}
	gone, err := s.CreateDNSPolicyRule(ctx, f.admin.ID, f.victim.ID, rule)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDNSPolicyRule(ctx, f.admin.ID, f.victim.ID, gone.ID); err != nil {
		t.Fatal(err)
	}
	rule.Pattern = "kept.example"
	if _, err := s.CreateDNSPolicyRule(ctx, f.victim.ID, f.victim.ID, rule); err != nil {
		t.Fatal(err)
	}
	enabled := true
	list, err := s.CommitPublicListSnapshot(ctx, f.admin.ID, "delete-user-list", time.Time{}, PublicListSpec{
		Name: "Ads", URL: "https://example.com/ads.txt", Format: PublicListFormatMosDNS, DefaultEnabled: &enabled, RefreshSeconds: 300,
	}, PublicListRefreshResult{Status: PublicListRefreshSuccess, EntryCount: 1, SHA256: strings.Repeat("a", 64), RefreshedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if err := s.SetUserPublicList(ctx, f.admin.ID, f.victim.ID, list.ID, &disabled); err != nil {
		t.Fatal(err)
	}
	return f
}

// checkUserDeleted verifies what DeleteUser promises through the service API.
func checkUserDeleted(t *testing.T, s Service, f deletionFixture, auditFrom time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetUser(ctx, f.victim.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted user err=%v", err)
	}
	if _, err := s.AuthenticateCredential(ctx, f.victimToken); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("deleted user's credential err=%v", err)
	}
	if _, _, err := s.AuthenticateSession(ctx, f.victimSession); err == nil {
		t.Fatal("deleted user's session still authenticates")
	}
	if _, err := s.AuthenticateCredential(ctx, f.bystanderToken); err != nil {
		t.Fatalf("bystander credential err=%v", err)
	}
	audit, err := s.ListAudit(ctx, auditFrom, time.Now().Add(time.Hour), Page{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var deletes, bystanderCreates int
	for _, r := range audit.Items {
		if r.Action == "delete_user" {
			deletes++
			if r.ActorID != f.admin.ID || r.TargetID != f.victim.ID || len(r.Metadata) != 0 {
				t.Fatalf("delete audit=%+v", r)
			}
			continue
		}
		if newUserAuditLinks(f.victim.ID).concerns(r) || r.TargetID == f.victim.ID {
			t.Fatalf("audit about deleted user survived: %+v", r)
		}
		if r.Action == "create_credential" {
			bystanderCreates++
		}
	}
	if deletes != 1 || bystanderCreates != 1 {
		t.Fatalf("delete audits=%d bystander credential audits=%d in %+v", deletes, bystanderCreates, audit.Items)
	}
	// The username is free again.
	if _, err := s.CreateUser(ctx, f.admin.ID, userSpec("victim", 100, 10, 2)); err != nil {
		t.Fatalf("reuse username: %v", err)
	}
}

func TestDeleteUserErasesEverythingAboutTheUser(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	s, _, _ := newTestStore(t, now)
	f := populateForDeletion(t, s)
	if err := s.DeleteUser(context.Background(), f.admin.ID, f.victim.ID); err != nil {
		t.Fatal(err)
	}

	// No key or value anywhere may still mention the user, except the one
	// delete_user audit.
	id := []byte(f.victim.ID)
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bbolt.Bucket) error {
			return b.ForEach(func(k, v []byte) error {
				if !bytes.Contains(k, id) && !bytes.Contains(v, id) {
					return nil
				}
				if string(name) == string(bAudit) && bytes.Contains(v, []byte(`"action":"delete_user"`)) {
					return nil
				}
				t.Errorf("bucket %s still holds %q = %q", name, k, v)
				return nil
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	checkUserDeleted(t, s, f, now.Add(-time.Hour))
}

func TestDeleteUserGuards(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestStore(t, time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC))
	f := populateForDeletion(t, s)
	if err := s.DeleteUser(ctx, f.victim.ID, f.other.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin delete err=%v", err)
	}
	if err := s.DeleteUser(ctx, f.admin.ID, f.admin.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("self delete err=%v", err)
	}
	if err := s.DeleteUser(ctx, f.admin.ID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user err=%v", err)
	}
	if err := s.DeleteUser(ctx, f.admin.ID, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty id err=%v", err)
	}
	disabled := false
	if _, err := s.UpdateUser(ctx, f.admin.ID, f.other.ID, UserPatch{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, f.other.ID, f.victim.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled admin delete err=%v", err)
	}
	// An admin may delete another admin while one enabled admin remains.
	if err := s.DeleteUser(ctx, f.admin.ID, f.other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUser(ctx, f.victim.ID); err != nil {
		t.Fatalf("failed deletes changed the victim: %v", err)
	}
	// The last enabled administrator is protected even from a path that
	// skips the actor checks.
	err := s.db.View(func(tx *bbolt.Tx) error { return requireAnotherEnabledAdmin(tx, f.admin.ID) })
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("last admin err=%v", err)
	}
}

func TestUserAuditLinks(t *testing.T) {
	links := newUserAuditLinks("u1")
	links.sessions["s1"] = struct{}{}
	links.credentials["c1"] = struct{}{}
	links.rules["r1"] = struct{}{}
	for _, tc := range []struct {
		name string
		r    AuditRecord
		want bool
	}{
		{"actor", AuditRecord{ActorID: "u1", TargetType: "public_list", TargetID: "l1"}, true},
		{"user target", AuditRecord{ActorID: "a", TargetType: "user", TargetID: "u1"}, true},
		{"settings target", AuditRecord{ActorID: "a", TargetType: "dns_policy_settings", TargetID: "u1"}, true},
		{"owned session", AuditRecord{ActorID: "a", TargetType: "session", TargetID: "s1"}, true},
		{"owned credential", AuditRecord{ActorID: "a", TargetType: "credential", TargetID: "c1"}, true},
		{"owned rule", AuditRecord{ActorID: "a", TargetType: "dns_policy_rule", TargetID: "r1"}, true},
		{"deleted rule", AuditRecord{ActorID: "a", TargetType: "dns_policy_rule", TargetID: "r9", Metadata: map[string]any{"rule": map[string]any{"user_id": "u1"}}}, true},
		{"purged credential", AuditRecord{ActorID: "a", TargetType: "credential", TargetID: "c9", Metadata: map[string]any{"user_id": "u1"}}, true},
		{"list override", AuditRecord{ActorID: "a", TargetType: "public_list", TargetID: "l1", Metadata: map[string]any{"user_id": "u1", "enabled": false}}, true},
		{"other user", AuditRecord{ActorID: "a", TargetType: "user", TargetID: "u2"}, false},
		{"id reused across types", AuditRecord{ActorID: "a", TargetType: "public_list", TargetID: "u1"}, false},
		{"other credential", AuditRecord{ActorID: "a", TargetType: "credential", TargetID: "c2", Metadata: map[string]any{"user_id": "u2"}}, false},
	} {
		if got := links.concerns(tc.r); got != tc.want {
			t.Errorf("%s: concerns=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestMySQLDeleteUserRemovesChildRowsBeforeUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	store := &MySQLStore{db: db, clock: &fakeClock{t: now}, operationTimeout: time.Second, closeCh: make(chan struct{})}
	adminRow := sqlmock.NewRows(mysqlUserTestColumns).AddRow("admin-1", "admin", "admin", true, nil, "monthly", "UTC", 100, 10, 2, 3, now.UnixNano(), now.UnixNano(), make([]byte, 16), make([]byte, 32), 1, 0, 0, 0, 1, now.UnixNano())
	byID := regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)
	mock.ExpectBegin()
	mock.ExpectQuery(byID).WithArgs("admin-1").WillReturnRows(adminRow)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT initialized FROM mosdns_control_meta WHERE id=1 FOR UPDATE`)).WillReturnRows(sqlmock.NewRows([]string{"initialized"}).AddRow(true))
	mock.ExpectQuery(byID).WithArgs("user-1").WillReturnRows(mysqlTestUserRow(now, 0, 1, now.UnixNano()))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM mosdns_sessions WHERE user_id=?`)).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("session-1"))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM mosdns_credentials WHERE user_id=?`)).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("credential-1"))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM mosdns_dns_policy_rules WHERE user_id=?`)).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	auditColumns := []string{"id", "actor_id", "action", "target_type", "target_id", "metadata_json"}
	mock.ExpectQuery(`SELECT id, actor_id, action, target_type, target_id, metadata_json\s+FROM mosdns_audit_logs`).
		WithArgs("user-1", "user-1", `%"user_id":"user-1"%`).
		WillReturnRows(sqlmock.NewRows(auditColumns).
			AddRow("a1", "admin-1", "revoke_session", "session", "session-1", nil).
			AddRow("a2", "admin-1", "revoke_session", "session", "session-2", `{"user_id":"user-2"}`).
			AddRow("a3", "admin-1", "rotate_credential", "credential", "credential-1", nil).
			AddRow("a4", "user-1", "change_password", "user", "user-1", nil))
	for _, table := range []string{"mosdns_sessions", "mosdns_credentials", "mosdns_dns_policy_rules", "mosdns_dns_policy_settings", "mosdns_user_public_lists"} {
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM ` + table + ` WHERE user_id=?`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_usage_minutes WHERE scope_kind IN ('u', 'd') AND scope_id=?`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_audit_logs WHERE id IN (?, ?, ?)`)).WithArgs("a1", "a3", "a4").WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_users WHERE id=?`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WithArgs(sqlmock.AnyArg(), "admin-1", "delete_user", "user", "user-1", nil, now.UnixNano()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := store.DeleteUser(context.Background(), "admin-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`a_b%c\\d`); got != `a\_b\%c\\\\d` {
		t.Fatalf("escapeLike=%q", got)
	}
}

func TestMySQLDeleteUserRefusesSelfBeforeTouchingRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	store := &MySQLStore{db: db, clock: &fakeClock{t: now}, operationTimeout: time.Second, closeCh: make(chan struct{})}
	adminRow := sqlmock.NewRows(mysqlUserTestColumns).AddRow("admin-1", "admin", "admin", true, nil, "monthly", "UTC", 100, 10, 2, 3, now.UnixNano(), now.UnixNano(), make([]byte, 16), make([]byte, 32), 1, 0, 0, 0, 1, now.UnixNano())
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("admin-1").WillReturnRows(adminRow)
	mock.ExpectRollback()
	if err := store.DeleteUser(context.Background(), "admin-1", "admin-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("self delete err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
