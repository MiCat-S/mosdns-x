package control

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"go.etcd.io/bbolt"
)

// userAuditLinks holds the IDs of records owned by a user being deleted, so
// audit entries that only name those records can be traced back to the user.
type userAuditLinks struct {
	userID      string
	sessions    map[string]struct{}
	credentials map[string]struct{}
	rules       map[string]struct{}
}

func newUserAuditLinks(userID string) userAuditLinks {
	return userAuditLinks{userID: userID, sessions: map[string]struct{}{}, credentials: map[string]struct{}{}, rules: map[string]struct{}{}}
}

// concerns reports whether an audit record was performed by the user or is
// about the user or one of the user's records. Sessions and credentials that
// maintenance already purged cannot be linked unless the record carries the
// user_id metadata written since user deletion was introduced.
func (l userAuditLinks) concerns(r AuditRecord) bool {
	if r.ActorID == l.userID {
		return true
	}
	switch r.TargetType {
	case "user", "dns_policy_settings":
		if r.TargetID == l.userID {
			return true
		}
	case "session":
		if _, ok := l.sessions[r.TargetID]; ok {
			return true
		}
	case "credential":
		if _, ok := l.credentials[r.TargetID]; ok {
			return true
		}
	case "dns_policy_rule":
		if _, ok := l.rules[r.TargetID]; ok {
			return true
		}
	}
	if metadataUserID(r.Metadata) == l.userID {
		return true
	}
	for _, key := range []string{"rule", "before", "after"} {
		if nested, ok := r.Metadata[key].(map[string]any); ok && metadataUserID(nested) == l.userID {
			return true
		}
	}
	return false
}

func metadataUserID(m map[string]any) string {
	v, _ := m["user_id"].(string)
	return v
}

// DeleteUser permanently removes a user together with the user's sessions,
// credentials, DNS policy, public list overrides, usage counters and every
// audit record the user performed or was the subject of. The only trace left
// is a delete_user audit naming the administrator and the removed user ID.
// Query logs live in the telemetry store and are erased separately.
func (s *Store) DeleteUser(ctx context.Context, actor, userID string) error {
	if userID == "" {
		return ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		if err := requireAdmin(tx, actor, now); err != nil {
			return err
		}
		if actor == userID {
			return fmt.Errorf("%w: cannot delete yourself", ErrConflict)
		}
		target, err := getUserRecord(tx, userID)
		if err != nil {
			return err
		}
		if target.Role == RoleAdmin && enabledUser(target) == nil {
			if err := requireAnotherEnabledAdmin(tx, userID); err != nil {
				return err
			}
		}
		links := newUserAuditLinks(userID)
		prefix := []byte(userID + "\x00")

		sessionKeys, sessionIDs := prefixEntries(tx.Bucket(bUserSessions), prefix)
		for i, id := range sessionIDs {
			links.sessions[string(id)] = struct{}{}
			if err := tx.Bucket(bSessions).Delete(id); err != nil {
				return err
			}
			if err := tx.Bucket(bUserSessions).Delete(sessionKeys[i]); err != nil {
				return err
			}
		}

		credentialKeys, credentialIDs := prefixEntries(tx.Bucket(bUserCredentials), prefix)
		for i, id := range credentialIDs {
			links.credentials[string(id)] = struct{}{}
			if v := tx.Bucket(bCredentials).Get(id); v != nil {
				var r credentialRecord
				if err := json.Unmarshal(v, &r); err != nil {
					return err
				}
				if err := deleteCredentialTokenIndex(tx, r); err != nil {
					return err
				}
				if err := tx.Bucket(bCredentials).Delete(id); err != nil {
					return err
				}
			}
			if err := tx.Bucket(bUserCredentials).Delete(credentialKeys[i]); err != nil {
				return err
			}
		}
		if err := deletePrefix(tx.Bucket(bActiveCredentials), prefix); err != nil {
			return err
		}

		if b := tx.Bucket(bDNSPolicyRules); b != nil {
			ruleKeys, ruleIDs := prefixEntries(tx.Bucket(bUserDNSPolicyRules), prefix)
			for i, id := range ruleIDs {
				links.rules[string(id)] = struct{}{}
				if err := b.Delete(id); err != nil {
					return err
				}
				if err := tx.Bucket(bUserDNSPolicyRules).Delete(ruleKeys[i]); err != nil {
					return err
				}
			}
		}
		if b := tx.Bucket(bDNSPolicySettings); b != nil {
			if err := b.Delete([]byte(userID)); err != nil {
				return err
			}
		}
		if b := tx.Bucket(bUserPublicLists); b != nil {
			if err := deletePrefix(b, prefix); err != nil {
				return err
			}
		}
		for _, scope := range []string{"u:" + userID + "\x00", "d:" + userID + "\x00"} {
			if err := deletePrefix(tx.Bucket(bUsage), []byte(scope)); err != nil {
				return err
			}
		}

		var auditKeys [][]byte
		err = tx.Bucket(bAudit).ForEach(func(k, v []byte) error {
			var r AuditRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if links.concerns(r) {
				auditKeys = append(auditKeys, append([]byte(nil), k...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range auditKeys {
			if err := tx.Bucket(bAudit).Delete(k); err != nil {
				return err
			}
		}

		name := []byte(strings.ToLower(target.Username))
		if string(tx.Bucket(bUsernames).Get(name)) == userID {
			if err := tx.Bucket(bUsernames).Delete(name); err != nil {
				return err
			}
		}
		if err := tx.Bucket(bUsers).Delete([]byte(userID)); err != nil {
			return err
		}
		return s.audit(tx, actor, "delete_user", "user", userID, nil, now)
	})
}

func requireAnotherEnabledAdmin(tx *bbolt.Tx, userID string) error {
	found := false
	err := tx.Bucket(bUsers).ForEach(func(k, v []byte) error {
		if found || string(k) == userID {
			return nil
		}
		var other userRecord
		if err := json.Unmarshal(v, &other); err != nil {
			return err
		}
		found = other.Role == RoleAdmin && enabledUser(other) == nil
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: cannot delete the last administrator", ErrConflict)
	}
	return nil
}

// prefixEntries copies the keys and values under prefix so callers can delete
// them without mutating the bucket under an open cursor.
func prefixEntries(b *bbolt.Bucket, prefix []byte) (keys, values [][]byte) {
	if b == nil {
		return nil, nil
	}
	c := b.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		keys = append(keys, append([]byte(nil), k...))
		values = append(values, append([]byte(nil), v...))
	}
	return keys, values
}

func deletePrefix(b *bbolt.Bucket, prefix []byte) error {
	keys, _ := prefixEntries(b, prefix)
	for _, k := range keys {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// DeleteUser is the MySQL counterpart of (*Store).DeleteUser.
func (s *MySQLStore) DeleteUser(ctx context.Context, actor, userID string) error {
	if userID == "" {
		return ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
			return err
		}
		if actor == userID {
			return fmt.Errorf("%w: cannot delete yourself", ErrConflict)
		}
		// Serializes with UpdateUser's last-administrator check.
		var initialized bool
		if err := tx.QueryRowContext(ctx, `SELECT initialized FROM mosdns_control_meta WHERE id=1 FOR UPDATE`).Scan(&initialized); err != nil {
			return err
		}
		target, err := mysqlUser(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		if target.Role == RoleAdmin && enabledUser(target) == nil {
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_users WHERE role='admin' AND enabled=TRUE AND id<>?`, userID).Scan(&active); err != nil {
				return err
			}
			if active == 0 {
				return fmt.Errorf("%w: cannot delete the last administrator", ErrConflict)
			}
		}
		links := newUserAuditLinks(userID)
		for _, owned := range []struct {
			query string
			into  map[string]struct{}
		}{
			{`SELECT id FROM mosdns_sessions WHERE user_id=?`, links.sessions},
			{`SELECT id FROM mosdns_credentials WHERE user_id=?`, links.credentials},
			{`SELECT id FROM mosdns_dns_policy_rules WHERE user_id=?`, links.rules},
		} {
			ids, err := mysqlStrings(ctx, tx, owned.query, userID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				owned.into[id] = struct{}{}
			}
		}
		auditIDs, err := mysqlUserAuditIDs(ctx, tx, links)
		if err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM mosdns_sessions WHERE user_id=?`,
			`DELETE FROM mosdns_credentials WHERE user_id=?`,
			`DELETE FROM mosdns_dns_policy_rules WHERE user_id=?`,
			`DELETE FROM mosdns_dns_policy_settings WHERE user_id=?`,
			`DELETE FROM mosdns_user_public_lists WHERE user_id=?`,
			`DELETE FROM mosdns_usage_minutes WHERE scope_kind IN ('u', 'd') AND scope_id=?`,
		} {
			if _, err := tx.ExecContext(ctx, q, userID); err != nil {
				return err
			}
		}
		for start := 0; start < len(auditIDs); start += 500 {
			chunk := auditIDs[start:min(start+500, len(auditIDs))]
			args := make([]any, len(chunk))
			for i, id := range chunk {
				args[i] = id
			}
			q := `DELETE FROM mosdns_audit_logs WHERE id IN (?` + strings.Repeat(`, ?`, len(chunk)-1) + `)`
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mosdns_users WHERE id=?`, userID); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "delete_user", "user", userID, nil, now)
	})
}

func mysqlStrings(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// mysqlUserAuditIDs narrows the audit table in SQL to a superset of the
// records concerning the user, then applies the exact match in Go. Rows about
// public lists can carry whole list snapshots, so they are only fetched when
// their metadata names the user.
func mysqlUserAuditIDs(ctx context.Context, tx *sql.Tx, links userAuditLinks) ([]string, error) {
	pattern := `%"user_id":"` + escapeLike(links.userID) + `"%`
	rows, err := tx.QueryContext(ctx, `SELECT id, actor_id, action, target_type, target_id, metadata_json
		FROM mosdns_audit_logs WHERE actor_id=? OR target_id=?
		OR target_type IN ('session', 'credential', 'dns_policy_rule') OR metadata_json LIKE ?`,
		links.userID, links.userID, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r AuditRecord
		var metadata []byte
		if err := rows.Scan(&r.ID, &r.ActorID, &r.Action, &r.TargetType, &r.TargetID, &metadata); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &r.Metadata); err != nil {
				return nil, err
			}
		}
		if links.concerns(r) {
			out = append(out, r.ID)
		}
	}
	return out, rows.Err()
}

func escapeLike(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}
