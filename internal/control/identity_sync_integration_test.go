package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMySQLIntegrationIdentitySyncLifecycle(t *testing.T) {
	dsn := os.Getenv("MOSDNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MOSDNS_TEST_MYSQL_DSN is not set")
	}
	ctx := context.Background()
	cleanupMySQLControlTables(t, dsn)
	t.Cleanup(func() { cleanupMySQLControlTables(t, dsn) })
	open := func(mode string) *MySQLStore {
		t.Helper()
		s, err := OpenMySQL(MySQLOptions{DSN: dsn, IdentityMode: mode, OperationTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	master := open("master")
	admin, err := master.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	user, err := master.CreateUser(ctx, admin.ID, userSpec("shared-user", 100, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := master.CreateCredential(ctx, user.ID, user.ID, "phone", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	monthly := PeriodMonthly
	if _, err = master.UpdateUser(ctx, admin.ID, user.ID, UserPatch{Period: &monthly}); !errors.Is(err, ErrIdentityPeriodLocked) {
		t.Fatalf("master cycle edit=%v", err)
	}
	snapshot, err := master.ExportIdentitySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = DecodeIdentitySnapshot(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	if err = master.Close(); err != nil {
		t.Fatal(err)
	}
	// Reuse the isolated test database as the second node. The source snapshot
	// stays in memory while a pre-existing Chengdu identity is seeded.
	cleanupMySQLControlTables(t, dsn)
	local := open("standalone")
	old, err := local.InitializeAdmin(ctx, UserSpec{Username: "old-chengdu", Password: "old-password-for-chengdu", Period: PeriodDaily, Timezone: "UTC", Limit: 100, QPS: 10, MaxCredentials: 3})
	if err != nil {
		t.Fatal(err)
	}
	oldCred, err := local.CreateCredential(ctx, old.ID, old.ID, "old", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity, err := local.AuthenticateCredential(ctx, oldCred.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = local.Admit(ctx, oldIdentity); err != nil {
		t.Fatal(err)
	}
	seedRetiredMySQLRows(t, dsn, old.ID)
	if err = local.Close(); err != nil {
		t.Fatal(err)
	}
	follower := open("follower")
	defer func() {
		if follower != nil {
			_ = follower.Close()
		}
	}()
	invalid := snapshot
	invalid.Credentials = nil
	if _, err = follower.ApplyIdentitySnapshot(ctx, invalid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid bootstrap=%v", err)
	}
	if _, err = follower.GetUser(ctx, old.ID); err != nil {
		t.Fatalf("failed bootstrap erased local identity: %v", err)
	}
	if _, err = follower.ApplyIdentitySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = follower.GetUser(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old identity survives: %v", err)
	}
	if _, err = follower.AuthenticateCredential(ctx, oldCred.Token); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("old credential survives: %v", err)
	}
	identity, err := follower.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = follower.Admit(ctx, identity); err != nil {
		t.Fatal(err)
	}
	_, sessionToken, err := follower.Login(ctx, user.Username, userSpec("", 100, 10, 2).Password, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	auditCount := func() int {
		t.Helper()
		var count int
		if err := follower.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_audit_logs WHERE action='identity_sync'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	beforeAudit := auditCount()
	for range 2 {
		if _, err = follower.ApplyIdentitySnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if afterAudit := auditCount(); afterAudit != beforeAudit {
		t.Fatalf("unchanged import wrote audit: before=%d after=%d", beforeAudit, afterAudit)
	}

	quota, err := follower.CurrentQuota(ctx, user.ID)
	if err != nil || quota.Used != 1 {
		t.Fatalf("repeat import quota=%+v err=%v", quota, err)
	}
	if _, _, err = follower.AuthenticateSession(ctx, sessionToken); err != nil {
		t.Fatalf("unchanged import revoked session: %v", err)
	}
	// New password invalidates local sessions; central credential rotation also
	// rejects an Identity obtained before the import committed.
	newToken := "a31cf743-023b-405c-8d20-5975e8cc0566"
	newHash := hashSecret(newToken)
	for i := range snapshot.Credentials {
		if snapshot.Credentials[i].ID == issued.Credential.ID {
			snapshot.Credentials[i].TokenHash = newHash[:]
			snapshot.Credentials[i].Version++
		}
	}
	salt, hash, err := hashPassword("new-shared-password")
	if err != nil {
		t.Fatal(err)
	}
	for i := range snapshot.Users {
		if snapshot.Users[i].ID == user.ID {
			snapshot.Users[i].PasswordSalt = salt
			snapshot.Users[i].PasswordHash = hash
			snapshot.Users[i].PasswordVersion++
			snapshot.Users[i].Limit = 50
			snapshot.Users[i].Period = PeriodMonthly
		}
	}
	if mismatch, applyErr := follower.ApplyIdentitySnapshot(ctx, snapshot); applyErr != nil || !mismatch {
		t.Fatalf("cycle mismatch should warn and commit: mismatch=%v err=%v", mismatch, applyErr)
	}
	if _, _, err = follower.AuthenticateSession(ctx, sessionToken); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("old session valid: %v", err)
	}
	if err = follower.Admit(ctx, identity); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale identity admitted: %v", err)
	}
	if _, err = follower.AuthenticateCredential(ctx, issued.Token); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("old token valid: %v", err)
	}
	if _, err = follower.AuthenticateCredential(ctx, newToken); err != nil {
		t.Fatal(err)
	}
	if _, _, err = follower.Login(ctx, user.Username, "new-shared-password", time.Hour); err != nil {
		t.Fatal(err)
	}
	quota, err = follower.CurrentQuota(ctx, user.ID)
	if err != nil || quota.Used != 1 || quota.Limit != 50 || quota.Period != PeriodDaily {
		t.Fatalf("updated quota=%+v err=%v", quota, err)
	}
	_, freshSession, err := follower.Login(ctx, user.Username, "new-shared-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := range snapshot.Users {
		if snapshot.Users[i].ID == user.ID {
			snapshot.Users[i].Enabled = false
		}
	}
	if _, err = follower.ApplyIdentitySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	for i := range snapshot.Users {
		if snapshot.Users[i].ID == user.ID {
			snapshot.Users[i].Enabled = true
		}
	}
	if _, err = follower.ApplyIdentitySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, _, err = follower.AuthenticateSession(ctx, freshSession); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("disabled/re-enabled revived session: %v", err)
	}
	activeIdentity, err := follower.AuthenticateCredential(ctx, newToken)
	if err != nil {
		t.Fatal(err)
	}
	// Imports and queries contend on the same user row. Counts must remain
	// exact when the definition snapshot lands during admission.
	errs := make(chan error, 10)
	for range 5 {
		go func() { errs <- follower.Admit(ctx, activeIdentity) }()
		go func() { _, err := follower.ApplyIdentitySnapshot(ctx, snapshot); errs <- err }()
	}
	for range 10 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	quota, err = follower.CurrentQuota(ctx, user.ID)
	if err != nil || quota.Used != 6 {
		t.Fatalf("concurrent import quota=%+v err=%v", quota, err)
	}

	// Persisted follower mode also protects raw-DSN offline commands.
	offline := open("")
	if err = offline.SetPassword(ctx, user.ID, user.ID, "must-not-be-applied"); !errors.Is(err, ErrIdentityManagedByMaster) {
		t.Fatalf("offline password mutation=%v", err)
	}
	if err = offline.Close(); err != nil {
		t.Fatal(err)
	}
	// Delete the shared user, then fail telemetry erasure. The queue must survive
	// restart and retain the existing control deletion behavior for retired rows.
	for i, u := range snapshot.Users {
		if u.ID == user.ID {
			snapshot.Users = append(snapshot.Users[:i], snapshot.Users[i+1:]...)
			break
		}
	}
	snapshot.Credentials = []IdentityCredential{}
	if _, err = follower.ApplyIdentitySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = follower.GetUser(ctx, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user survives: %v", err)
	}
	if err = follower.DrainUserErases(ctx, func(context.Context, string) (bool, error) { return false, errors.New("telemetry unavailable") }); err == nil {
		t.Fatal("erase failure swallowed")
	}
	if err = follower.Close(); err != nil {
		t.Fatal(err)
	}
	follower = open("")
	erased := map[string]bool{}
	if err = follower.DrainUserErases(ctx, func(_ context.Context, id string) (bool, error) { erased[id] = true; return true, nil }); err != nil {
		t.Fatal(err)
	}
	if !erased[old.ID] || !erased[user.ID] {
		t.Fatalf("missing cleanup: %v", erased)
	}
	state, err := follower.IdentitySyncState(ctx)
	if err != nil || !state.Bootstrapped || state.LastSuccessAt == nil || state.Users != 1 || state.Credentials != 0 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

// The production database was cloned from the master: every imported ID is
// reused, with ~250k historical rows and a 1500ms SQL operation timeout.
func TestMySQLIntegrationIdentitySyncLargeHistory(t *testing.T) {
	dsn := os.Getenv("MOSDNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MOSDNS_TEST_MYSQL_DSN is not set")
	}
	ctx := context.Background()
	cleanupMySQLControlTables(t, dsn)
	t.Cleanup(func() { cleanupMySQLControlTables(t, dsn) })
	open := func(mode string) *MySQLStore {
		t.Helper()
		s, e := OpenMySQL(MySQLOptions{DSN: dsn, IdentityMode: mode, OperationTimeout: 1500 * time.Millisecond})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	store := open("master")
	admin, err := store.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, admin.ID, userSpec("large-history", 100, 100, 10))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := store.CreateCredential(ctx, user.ID, user.ID, "phone", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ExportIdentitySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seed := func() {
		t.Helper()
		for batch := 0; batch < 250; batch++ {
			args := make([]any, 0, 5000)
			for i := 0; i < 1000; i++ {
				n := batch*1000 + i
				kind := "u"
				if n%2 == 1 {
					kind = "d"
				}
				args = append(args, kind, user.ID, issued.Credential.ID, int64(n)*60, 1)
			}
			query := `INSERT INTO mosdns_usage_minutes (scope_kind,scope_id,credential_id,minute_epoch,query_count) VALUES ` + strings.TrimSuffix(strings.Repeat("(?,?,?,?,?),", 1000), ",")
			if _, e := store.db.ExecContext(ctx, query, args...); e != nil {
				t.Fatal(e)
			}
		}
	}
	seed()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open("follower")
	defer func() { _ = store.Close() }()
	started := time.Now()
	if _, err = store.ApplyIdentitySnapshot(ctx, snapshot); err != nil {
		t.Fatalf("short bootstrap transaction: %v", err)
	}
	t.Logf("bootstrap identity commit: %s", time.Since(started))
	var rows int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_usage_minutes`).Scan(&rows); err != nil || rows != 250000 {
		t.Fatalf("history must remain queued: rows=%d err=%v", rows, err)
	}
	if n, e := store.ReusedUserEraseCount(ctx); e != nil || n != 2 {
		t.Fatalf("same-ID gate: n=%d err=%v", n, e)
	}
	drain := func() {
		t.Helper()
		for pass := 0; pass < 1000; pass++ {
			if e := store.DrainUserErases(ctx, func(context.Context, string) (bool, error) { return true, nil }); e != nil {
				t.Fatal(e)
			}
			var pending int
			if e := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_identity_sync_erases`).Scan(&pending); e != nil {
				t.Fatal(e)
			}
			if pending == 0 {
				return
			}
		}
		t.Fatal("cleanup did not finish")
	}
	drain()
	identity, err := store.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Admit(ctx, identity); err != nil {
		t.Fatal(err)
	}
	// A later background pass must never erase post-bootstrap same-ID usage.
	drain()
	quota, err := store.CurrentQuota(ctx, user.ID)
	if err != nil || quota.Used != 1 {
		t.Fatalf("new quota=%+v err=%v", quota, err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_usage_minutes WHERE scope_kind IN ('u','d') AND scope_id=?`, user.ID).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("new history erased: rows=%d err=%v", rows, err)
	}
	// Ordinary master deletion uses the same short transaction and queue.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open("master")
	seed()
	if err = store.DeleteUser(ctx, admin.ID, user.ID); err != nil {
		t.Fatalf("large master delete: %v", err)
	}
	if _, err = store.GetUser(ctx, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted identity: %v", err)
	}
	drain()
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_usage_minutes WHERE scope_kind IN ('u','d') AND scope_id=?`, user.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("old history remains: rows=%d err=%v", rows, err)
	}
}
