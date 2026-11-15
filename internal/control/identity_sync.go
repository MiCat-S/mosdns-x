package control

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

// IdentitySnapshot contains authentication material. It belongs only on the
// authenticated node export endpoint, never on panel responses or in logs.
type IdentitySnapshot struct {
	Nodes       []DNSNode            `json:"nodes,omitempty"`
	Version     int                  `json:"version"`
	Users       []IdentityUser       `json:"users"`
	Credentials []IdentityCredential `json:"credentials"`
}

type IdentityUser struct {
	User
	PasswordSalt    []byte `json:"password_salt"`
	PasswordHash    []byte `json:"password_hash"`
	PasswordVersion uint64 `json:"password_version"`
}

type IdentityCredential struct {
	Token string `json:"token,omitempty"`
	Credential
	LegacySecretHash []byte `json:"legacy_secret_hash"`
	TokenHash        []byte `json:"token_hash"`
	Version          uint64 `json:"version"`
}

// DecodeIdentitySnapshot rejects incomplete documents before omissions can be
// interpreted as account deletions or cleared revocations.
func DecodeIdentitySnapshot(r io.Reader) (IdentitySnapshot, error) {
	var raw json.RawMessage
	d := json.NewDecoder(r)
	if err := d.Decode(&raw); err != nil {
		return IdentitySnapshot{}, ErrInvalidInput
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return IdentitySnapshot{}, ErrInvalidInput
	}
	var wire struct {
		Version     int               `json:"version"`
		Users       []json.RawMessage `json:"users"`
		Credentials []json.RawMessage `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Version != 1 || wire.Users == nil || wire.Credentials == nil {
		return IdentitySnapshot{}, ErrInvalidInput
	}
	userFields := strings.Fields("id username role enabled expires_at period timezone limit qps burst max_credentials created_at updated_at password_salt password_hash password_version")
	credentialFields := strings.Fields("id user_id name expires_at revoked_at created_at updated_at legacy_secret_hash token_hash version")
	for _, group := range []struct {
		rows   []json.RawMessage
		fields []string
	}{{wire.Users, userFields}, {wire.Credentials, credentialFields}} {
		for _, row := range group.rows {
			var fields map[string]json.RawMessage
			if json.Unmarshal(row, &fields) != nil {
				return IdentitySnapshot{}, ErrInvalidInput
			}
			for _, key := range group.fields {
				if value, ok := fields[key]; !ok || bytes.Equal(value, []byte("null")) && key != "token_hash" && key != "legacy_secret_hash" {
					return IdentitySnapshot{}, ErrInvalidInput
				}
			}
		}
	}
	var snapshot IdentitySnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, ErrInvalidInput
	}
	return snapshot, snapshot.Validate()
}

func validIdentityID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (v IdentitySnapshot) Validate() error {
	// Loopback HTTP is a supported wire value for development masters;
	// production configuration rejects it at the source boundary.
	if err := ValidateDNSNodes(v.Nodes, true); err != nil {
		return err
	}
	if v.Version != 1 || v.Users == nil || v.Credentials == nil {
		return ErrInvalidInput
	}
	users, names, tokens, credentials := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	admin := false
	for _, u := range v.Users {
		normalized, err := normalizeSpec(UserSpec{Username: u.Username, Role: u.Role, Period: u.Period, Timezone: u.Timezone, Limit: u.Limit, QPS: u.QPS, Burst: u.Burst, MaxCredentials: u.MaxCredentials})
		name := strings.ToLower(u.Username)
		if err != nil || normalized.Username != u.Username || u.Role == "" || u.Period == "" || u.Timezone == "" || !validIdentityID(u.ID) || users[u.ID] || names[name] || len(u.PasswordSalt) != 16 || len(u.PasswordHash) != 32 || u.PasswordVersion == 0 || u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
			return ErrInvalidInput
		}
		users[u.ID], names[name] = true, true
		admin = admin || u.Role == RoleAdmin && u.Enabled
	}
	if !admin {
		return ErrInvalidInput
	}
	for _, c := range v.Credentials {
		if c.Token != "" && (len(c.Token) > 512 || !c.RevokedAt.IsZero() || !credentialTokenMatches(c.ID, c.Token, c.TokenHash, c.LegacySecretHash)) {
			return ErrInvalidInput
		}
		if !validIdentityID(c.ID) || credentials[c.ID] || !users[c.UserID] || c.Version == 0 || len(c.Name) == 0 || len(c.Name) > 128 || c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
			return ErrInvalidInput
		}
		if !(len(c.TokenHash) == 32 && len(c.LegacySecretHash) == 0 || len(c.TokenHash) == 0 && len(c.LegacySecretHash) == 32) {
			return ErrInvalidInput
		}
		if len(c.TokenHash) > 0 {
			key := string(c.TokenHash)
			if tokens[key] {
				return ErrInvalidInput
			}
			tokens[key] = true
		}
		credentials[c.ID] = true
	}
	return nil
}

func (s *MySQLStore) configureIdentityMode(ctx context.Context, mode string) error {
	if mode != "" && mode != "standalone" && mode != "master" && mode != "follower" {
		return ErrInvalidInput
	}
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT mode FROM mosdns_identity_sync_state WHERE id=1 FOR UPDATE`).Scan(&stored); err != nil {
			return err
		}
		if mode == "" {
			mode = stored
		} else if mode != stored {
			query := `UPDATE mosdns_identity_sync_state SET mode=? WHERE id=1`
			if mode == "follower" {
				query = `UPDATE mosdns_identity_sync_state SET mode=?,bootstrapped=FALSE,last_success_at_ns=NULL WHERE id=1`
			}
			if _, err := tx.ExecContext(ctx, query, mode); err != nil {
				return err
			}
		}
		s.identityMode = mode
		return nil
	})
}

func (s *MySQLStore) ExportIdentitySnapshot(ctx context.Context) (IdentitySnapshot, error) {
	v := IdentitySnapshot{Version: 1, Users: []IdentityUser{}, Credentials: []IdentityCredential{}}
	if s.identityMode != "master" {
		return v, ErrForbidden
	}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return v, mysqlStoreError(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+mysqlUserColumns+` FROM mosdns_users ORDER BY id`)
	if err != nil {
		return v, mysqlStoreError(err)
	}
	for rows.Next() {
		u, e := scanMySQLUser(rows)
		if e != nil {
			rows.Close()
			return v, mysqlStoreError(e)
		}
		v.Users = append(v.Users, IdentityUser{u.User, u.PasswordSalt, u.PasswordHash, u.PasswordVersion})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, mysqlStoreError(err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT `+mysqlCredentialColumns+` FROM mosdns_credentials ORDER BY user_id,id`)
	if err != nil {
		return v, mysqlStoreError(err)
	}
	for rows.Next() {
		c, e := scanMySQLCredential(rows)
		if e != nil {
			rows.Close()
			return v, mysqlStoreError(e)
		}
		v.Credentials = append(v.Credentials, IdentityCredential{Credential: c.Credential, LegacySecretHash: c.LegacySecretHash, TokenHash: c.TokenHash, Version: c.Version})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, mysqlStoreError(err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT t.credential_id,t.token FROM mosdns_credential_tokens t JOIN mosdns_credentials c ON c.id=t.credential_id AND c.version=t.version WHERE c.revoked_at_ns IS NULL`)
	if err != nil {
		return v, mysqlStoreError(err)
	}
	indexes := map[string]int{}
	for i, c := range v.Credentials {
		indexes[c.ID] = i
	}
	for rows.Next() {
		var id, token string
		if err := rows.Scan(&id, &token); err != nil {
			rows.Close()
			return v, mysqlStoreError(err)
		}
		v.Credentials[indexes[id]].Token = token
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, mysqlStoreError(err)
	}
	if err = tx.Commit(); err != nil {
		return v, mysqlStoreError(err)
	}
	if err = v.Validate(); err != nil {
		return v, fmt.Errorf("%w: invalid identity export", ErrUnavailable)
	}
	return v, nil
}

type IdentitySyncState struct {
	Nodes         []DNSNode
	Bootstrapped  bool
	LastSuccessAt *time.Time
	Users         int
	Credentials   int
}

func (s *MySQLStore) IdentitySyncState(ctx context.Context) (IdentitySyncState, error) {
	var state IdentitySyncState
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	var last sql.NullInt64
	var nodesJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT bootstrapped,last_success_at_ns,(SELECT COUNT(*) FROM mosdns_users),(SELECT COUNT(*) FROM mosdns_credentials),(SELECT nodes_json FROM mosdns_identity_sync_nodes WHERE id=1) FROM mosdns_identity_sync_state WHERE id=1`).Scan(&state.Bootstrapped, &last, &state.Users, &state.Credentials, &nodesJSON)
	if last.Valid {
		t := mysqlNullTime(last)
		state.LastSuccessAt = &t
	}
	if err == nil {
		err = json.Unmarshal(nodesJSON, &state.Nodes)
	}
	return state, mysqlStoreError(err)
}

// ApplyIdentitySnapshot serializes against Admit by locking users before
// credentials. Definition updates deliberately exclude local quota/rate state.
// A cycle mismatch keeps local cycle definitions and reports a warning, while
// password changes, revocations and deletions still commit.
func (s *MySQLStore) ApplyIdentitySnapshot(ctx context.Context, v IdentitySnapshot) (periodMismatch bool, err error) {
	if s.identityMode != "follower" {
		return false, ErrForbidden
	}
	if err := v.Validate(); err != nil {
		return false, err
	}
	nodes := v.Nodes
	if nodes == nil {
		nodes = []DNSNode{}
	}
	nodesJSON, err := json.Marshal(nodes)
	if err != nil {
		return false, err
	}
	now := s.clock.Now().UTC()
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var bootstrapped bool
		if err := tx.QueryRowContext(ctx, `SELECT bootstrapped FROM mosdns_identity_sync_state WHERE id=1 FOR UPDATE`).Scan(&bootstrapped); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+mysqlUserColumns+` FROM mosdns_users ORDER BY id FOR UPDATE`)
		if err != nil {
			return err
		}
		old := map[string]userRecord{}
		for rows.Next() {
			u, e := scanMySQLUser(rows)
			if e != nil {
				rows.Close()
				return e
			}
			old[u.ID] = u
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		changed := !bootstrapped
		wanted := map[string]bool{}
		for _, u := range v.Users {
			wanted[u.ID] = true
		}
		// On bootstrap ALL old identities and usage are discarded, including an ID
		// that also happens to exist on the source.
		for id := range old {
			if !bootstrapped || !wanted[id] {
				changed = true
				if err := deleteMySQLUser(ctx, tx, id); err != nil {
					return err
				}
				delete(old, id)
			}
		}
		for _, u := range v.Users {
			existing, ok := old[u.ID]
			if !ok {
				changed = true
				r := userRecord{User: u.User, PasswordSalt: u.PasswordSalt, PasswordHash: u.PasswordHash, PasswordVersion: u.PasswordVersion, RateTokens: float64(u.QPS) + float64(u.Burst), RateAt: now.UnixNano()}
				if err := insertMySQLUser(ctx, tx, r); err != nil {
					return err
				}
				if err := insertMySQLRetiredPolicyDefaults(ctx, tx, u.ID, now); err != nil {
					return err
				}
				continue
			}
			if existing.Period != u.Period || existing.Timezone != u.Timezone {
				periodMismatch = true
				u.Period = existing.Period
				u.Timezone = existing.Timezone
			}
			passwordChanged := existing.PasswordVersion != u.PasswordVersion || !bytes.Equal(existing.PasswordHash, u.PasswordHash) || !bytes.Equal(existing.PasswordSalt, u.PasswordSalt)
			if passwordChanged || existing.Enabled && !u.Enabled || existing.Role != u.Role {
				if err := revokeMySQLUserSessions(ctx, tx, u.ID, now); err != nil {
					return err
				}
			}
			if reflect.DeepEqual(existing.User, u.User) && !passwordChanged {
				continue
			}
			changed = true
			if _, err := tx.ExecContext(ctx, `UPDATE mosdns_users SET username=?,username_normalized=?,role=?,enabled=?,expires_at_ns=?,period=?,timezone=?,quota_limit=?,qps=?,burst=?,max_credentials=?,created_at_ns=?,updated_at_ns=?,password_salt=?,password_hash=?,password_version=? WHERE id=?`, u.Username, strings.ToLower(u.Username), string(u.Role), u.Enabled, mysqlTimeValue(u.ExpiresAt), string(u.Period), u.Timezone, u.Limit, u.QPS, u.Burst, u.MaxCredentials, u.CreatedAt.UnixNano(), u.UpdatedAt.UnixNano(), u.PasswordSalt, u.PasswordHash, u.PasswordVersion, u.ID); err != nil {
				return err
			}
		}
		rows, err = tx.QueryContext(ctx, `SELECT `+mysqlCredentialColumns+` FROM mosdns_credentials ORDER BY user_id,id FOR UPDATE`)
		if err != nil {
			return err
		}
		oldCreds := map[string]mysqlCredentialRecord{}
		for rows.Next() {
			c, e := scanMySQLCredential(rows)
			if e != nil {
				rows.Close()
				return e
			}
			oldCreds[c.ID] = c
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		creds := map[string]bool{}
		for _, c := range v.Credentials {
			creds[c.ID] = true
		}
		for id := range oldCreds {
			if !creds[id] {
				changed = true
				if _, err := tx.ExecContext(ctx, `DELETE FROM mosdns_credentials WHERE id=?`, id); err != nil {
					return err
				}
			}
		}
		for _, c := range v.Credentials {
			old, ok := oldCreds[c.ID]
			if ok && reflect.DeepEqual(old, mysqlCredentialRecord{c.Credential, c.LegacySecretHash, c.TokenHash, c.Version}) {
				continue
			}
			if ok && old.UserID != c.UserID {
				return ErrInvalidInput
			}
			changed = true
			args := []any{c.Name, mysqlTimeValue(c.ExpiresAt), mysqlTimeValue(c.RevokedAt), c.CreatedAt.UnixNano(), c.UpdatedAt.UnixNano(), c.LegacySecretHash, c.TokenHash, c.Version, c.ID}
			query := `UPDATE mosdns_credentials SET name=?,expires_at_ns=?,revoked_at_ns=?,created_at_ns=?,updated_at_ns=?,legacy_secret_hash=?,token_hash=?,version=? WHERE id=?`
			if !ok {
				query = `INSERT INTO mosdns_credentials (name,expires_at_ns,revoked_at_ns,created_at_ns,updated_at_ns,legacy_secret_hash,token_hash,version,id,user_id) VALUES (?,?,?,?,?,?,?,?,?,?)`
				args = append(args, c.UserID)
			}
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return err
			}
		}
		rows, err = tx.QueryContext(ctx, `SELECT credential_id,token,version FROM mosdns_credential_tokens`)
		if err != nil {
			return err
		}
		type savedToken struct {
			token   string
			version uint64
		}
		oldTokens := map[string]savedToken{}
		for rows.Next() {
			var id string
			var saved savedToken
			if err := rows.Scan(&id, &saved.token, &saved.version); err != nil {
				rows.Close()
				return err
			}
			oldTokens[id] = saved
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range v.Credentials {
			saved, exists := oldTokens[c.ID]
			if c.Token == "" {
				if exists {
					if _, err := tx.ExecContext(ctx, `DELETE FROM mosdns_credential_tokens WHERE credential_id=?`, c.ID); err != nil {
						return err
					}
					changed = true
				}
			} else if !exists || saved.token != c.Token || saved.version != c.Version {
				if err := putMySQLCredentialToken(ctx, tx, c.ID, c.Token, c.Version); err != nil {
					return err
				}
				changed = true
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE mosdns_identity_sync_nodes SET nodes_json=? WHERE id=1 AND nodes_json<>?`, nodesJSON, nodesJSON)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = changed || updated > 0
		if _, err := tx.ExecContext(ctx, `UPDATE mosdns_control_meta SET initialized=TRUE WHERE id=1`); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mosdns_identity_sync_state SET bootstrapped=TRUE,last_success_at_ns=? WHERE id=1`, now.UnixNano()); err != nil {
			return err
		}
		if changed {
			return mysqlAudit(ctx, tx, "", "identity_sync", "node", "", map[string]any{"users": len(v.Users), "credentials": len(v.Credentials), "bootstrap": !bootstrapped, "nodes": len(v.Nodes)}, now)
		}
		return nil
	})
	return periodMismatch, err
}

// DrainUserErases makes one bounded pass over historical control usage and
// telemetry. Every statement gets its own operation timeout; user rows stay unlocked.
// Jobs survive restarts and are acknowledged only after both stores finish.
func (s *MySQLStore) DrainUserErases(ctx context.Context, erase func(context.Context, string) (bool, error)) error {
	opCtx, cancel := s.operationContext(ctx)
	rows, err := s.db.QueryContext(opCtx, `SELECT user_id FROM mosdns_identity_sync_erases ORDER BY user_id`)
	if err != nil {
		cancel()
		return mysqlStoreError(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	cancel()
	if err != nil {
		return mysqlStoreError(err)
	}

	for _, id := range ids {
		usageDone := true
		for _, kind := range []string{"u", "d"} {
			opCtx, cancel := s.operationContext(ctx)
			result, err := s.db.ExecContext(opCtx, `DELETE FROM mosdns_usage_minutes WHERE scope_kind=? AND scope_id=? LIMIT 1000`, kind, id)
			cancel()
			if err != nil {
				return mysqlStoreError(err)
			}
			removed, err := result.RowsAffected()
			if err != nil {
				return mysqlStoreError(err)
			}
			usageDone = usageDone && removed < 1000
		}
		if !usageDone {
			continue
		}
		done, err := erase(ctx, id)
		if err != nil {
			return err
		}
		if !done {
			continue
		}
		opCtx, cancel := s.operationContext(ctx)
		_, err = s.db.ExecContext(opCtx, `DELETE FROM mosdns_identity_sync_erases WHERE user_id=?`, id)
		cancel()
		if err != nil {
			return mysqlStoreError(err)
		}
	}
	return nil
}

// UserEraseQueue separates short identity transactions from historical cleanup.
// Reused IDs must be drained before opening listeners during bootstrap/restart.
type UserEraseQueue interface {
	DrainUserErases(context.Context, func(context.Context, string) (bool, error)) error
	ReusedUserEraseCount(context.Context) (int, error)
}

func (s *MySQLStore) ReusedUserEraseCount(ctx context.Context) (int, error) {
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	var pending int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_identity_sync_erases e JOIN mosdns_users u ON u.id=e.user_id`).Scan(&pending)
	return pending, mysqlStoreError(err)
}
