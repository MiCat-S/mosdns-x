package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func identityTestSnapshot(now time.Time) IdentitySnapshot {
	return IdentitySnapshot{Version: 1, Users: []IdentityUser{{User: User{ID: "user-1", Username: "user", Role: RoleAdmin, Enabled: true, Period: PeriodDaily, Timezone: "UTC", Limit: 100, QPS: 10, Burst: 2, MaxCredentials: 3, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}, PasswordSalt: make([]byte, 16), PasswordHash: make([]byte, 32), PasswordVersion: 1}}, Credentials: []IdentityCredential{}}
}

func TestDecodeIdentitySnapshotRequiresCompleteAuthoritativeDocument(t *testing.T) {
	snapshot := identityTestSnapshot(time.Now().UTC())
	snapshot.Credentials = append(snapshot.Credentials, IdentityCredential{Credential: Credential{ID: "credential-1", UserID: "user-1", Name: "phone", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, TokenHash: make([]byte, 32), Version: 1})
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeIdentitySnapshot(bytes.NewReader(encoded)); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	var base map[string]any
	if err = json.Unmarshal(encoded, &base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing users", func(v map[string]any) { delete(v, "users") }},
		{"missing credentials", func(v map[string]any) { delete(v, "credentials") }},
		{"missing enabled", func(v map[string]any) { delete(v["users"].([]any)[0].(map[string]any), "enabled") }},
		{"missing revoked at", func(v map[string]any) { delete(v["credentials"].([]any)[0].(map[string]any), "revoked_at") }},
		{"bad owner", func(v map[string]any) { v["credentials"].([]any)[0].(map[string]any)["user_id"] = "missing" }},
		{"no administrator", func(v map[string]any) { v["users"].([]any)[0].(map[string]any)["enabled"] = false }},
		{"duplicate user", func(v map[string]any) { v["users"] = append(v["users"].([]any), v["users"].([]any)[0]) }},
		{"unsupported version", func(v map[string]any) { v["version"] = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v map[string]any
			_ = json.Unmarshal(encoded, &v)
			tc.mutate(v)
			bad, _ := json.Marshal(v)
			if _, err := DecodeIdentitySnapshot(bytes.NewReader(bad)); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := DecodeIdentitySnapshot(strings.NewReader(string(encoded) + ` {}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("trailing JSON: %v", err)
	}
}

func TestFollowerRejectsEveryIdentityMutationBeforeDatabaseOrKDF(t *testing.T) {
	s := &MySQLStore{identityMode: "follower"}
	ctx := context.Background()
	checks := []func() error{
		func() error { _, e := s.InitializeAdmin(ctx, UserSpec{}); return e },
	}
	// The methods below intentionally receive invalid input and no database: the
	// follower restriction must be applied before any write or password hashing.
	checks = append(checks, func() error { _, e := s.CreateUser(ctx, "", UserSpec{}); return e }, func() error { _, e := s.UpdateUser(ctx, "", "", UserPatch{}); return e }, func() error { return s.DeleteUser(ctx, "", "") }, func() error { return s.SetPassword(ctx, "", "", "") }, func() error { return s.ChangePassword(ctx, "", "", "") }, func() error { _, e := s.CreateCredential(ctx, "", "", "", time.Time{}); return e }, func() error { _, e := s.RotateCredential(ctx, "", "", ""); return e }, func() error { return s.RevokeCredential(ctx, "", "", "") })
	for i, check := range checks {
		if err := check(); !errors.Is(err, ErrIdentityManagedByMaster) {
			t.Fatalf("operation %d: %v", i, err)
		}
	}
}

func TestApplyIdentityPreservesLocalStateAndRevokesPasswordSessions(t *testing.T) {
	for _, passwordChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "password changed"}[passwordChange], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
			s := &MySQLStore{db: db, identityMode: "follower", clock: &fakeClock{t: now}, operationTimeout: time.Second}
			snapshot := identityTestSnapshot(now)
			snapshot.Users[0].Role = RoleUser
			// A second, unchanged administrator satisfies the full snapshot contract.
			admin := snapshot.Users[0]
			admin.PasswordHash = append([]byte(nil), admin.PasswordHash...)
			admin.PasswordSalt = append([]byte(nil), admin.PasswordSalt...)
			admin.ID = "admin-1"
			admin.Username = "admin"
			admin.Role = RoleAdmin
			snapshot.Users = append(snapshot.Users, admin)
			if passwordChange {
				snapshot.Users[0].Period = PeriodMonthly
				snapshot.Users[0].PasswordVersion++
				snapshot.Users[0].PasswordHash[0] = 1
			}
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT bootstrapped FROM mosdns_identity_sync_state`).WillReturnRows(sqlmock.NewRows([]string{"bootstrapped"}).AddRow(true))
			users := mysqlTestUserRow(now, 42, 0.25, now.UnixNano()).AddRow(admin.ID, admin.Username, "admin", true, nil, "daily", "UTC", 100, 10, 2, 3, now.Add(-time.Hour).UnixNano(), now.Add(-time.Hour).UnixNano(), make([]byte, 16), make([]byte, 32), 1, now.Truncate(24*time.Hour).Unix(), now.Truncate(24*time.Hour).Unix(), 0, 12, now.UnixNano())
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users ORDER BY id FOR UPDATE`)).WillReturnRows(users)
			if passwordChange {
				mock.ExpectExec(`UPDATE mosdns_sessions SET revoked_at_ns=\? WHERE user_id=\?`).WithArgs(now.UnixNano(), "user-1").WillReturnResult(sqlmock.NewResult(0, 1))
				// Match the exact definition-only statement: quota/rate fields must be absent.
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_users SET username=?,username_normalized=?,role=?,enabled=?,expires_at_ns=?,period=?,timezone=?,quota_limit=?,qps=?,burst=?,max_credentials=?,created_at_ns=?,updated_at_ns=?,password_salt=?,password_hash=?,password_version=? WHERE id=?`)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlCredentialColumns + ` FROM mosdns_credentials ORDER BY user_id,id FOR UPDATE`)).WillReturnRows(sqlmock.NewRows(mysqlCredentialTestColumns))
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT credential_id,token,version FROM mosdns_credential_tokens`)).WillReturnRows(sqlmock.NewRows([]string{"credential_id", "token", "version"}))
			mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_identity_sync_nodes SET nodes_json=? WHERE id=1 AND nodes_json<>?`)).WithArgs([]byte("[]"), []byte("[]")).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(`UPDATE mosdns_control_meta SET initialized=TRUE`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE mosdns_identity_sync_state SET bootstrapped=TRUE,last_success_at_ns=\?`).WithArgs(now.UnixNano()).WillReturnResult(sqlmock.NewResult(0, 1))
			if passwordChange {
				mock.ExpectExec(`INSERT INTO mosdns_audit_logs`).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			if mismatch, err := s.ApplyIdentitySnapshot(context.Background(), snapshot); err != nil || mismatch != passwordChange {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdentityImportFailureRollsBackInsteadOfReplacingGoodSnapshot(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	now := time.Now().UTC()
	s := &MySQLStore{db: db, identityMode: "follower", clock: &fakeClock{t: now}, operationTimeout: time.Second}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT bootstrapped`).WillReturnRows(sqlmock.NewRows([]string{"bootstrapped"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users ORDER BY id FOR UPDATE`)).WillReturnError(errors.New("database interrupted"))
	mock.ExpectRollback()
	if _, err := s.ApplyIdentitySnapshot(context.Background(), identityTestSnapshot(now)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
