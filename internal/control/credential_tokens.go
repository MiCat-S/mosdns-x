package control

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"net/url"
	"strings"

	"go.etcd.io/bbolt"
)

func restoredToken(input string) (string, error) {
	input = strings.TrimSpace(input)
	if len(input) > 4096 {
		return "", ErrTokenMismatch
	}
	if strings.HasPrefix(strings.ToLower(input), "https://") || strings.HasPrefix(strings.ToLower(input), "http://") {
		u, err := url.Parse(input)
		if err != nil {
			return "", ErrTokenMismatch
		}
		path := strings.TrimRight(u.Path, "/")
		input = path[strings.LastIndex(path, "/")+1:]
	}
	if input == "" || len(input) > 512 {
		return "", ErrTokenMismatch
	}
	return input, nil
}

// Match display metadata to the credential's existing authentication contract.
// Restoring a token never changes the authentication hash or version.
func credentialTokenMatches(id, token string, tokenHash, legacyHash []byte) bool {
	if validUUIDv4(token) {
		h := hashSecret(token)
		return subtle.ConstantTimeCompare(h[:], tokenHash) == 1
	}
	owner, secret, ok := splitToken(token)
	if !ok || owner != id {
		return false
	}
	h := hashSecret(secret)
	return subtle.ConstantTimeCompare(h[:], legacyHash) == 1
}

func putMySQLCredentialToken(ctx context.Context, tx *sql.Tx, id, token string, version uint64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mosdns_credential_tokens (credential_id,token,version) VALUES (?,?,?) ON DUPLICATE KEY UPDATE token=VALUES(token),version=VALUES(version)`, id, token, version)
	return err
}

func (s *MySQLStore) CredentialTokens(ctx context.Context, userID string, ids []string) (map[string]string, error) {
	tokens := map[string]string{}
	if len(ids) == 0 {
		return tokens, nil
	}
	args := []any{userID}
	for _, id := range ids {
		args = append(args, id)
	}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT t.credential_id,t.token FROM mosdns_credential_tokens t JOIN mosdns_credentials c ON c.id=t.credential_id AND c.version=t.version WHERE c.user_id=? AND c.revoked_at_ns IS NULL AND c.id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, token string
		if err := rows.Scan(&id, &token); err != nil {
			return nil, mysqlStoreError(err)
		}
		tokens[id] = token
	}
	return tokens, mysqlStoreError(rows.Err())
}

func (s *Store) CredentialTokens(ctx context.Context, userID string, ids []string) (map[string]string, error) {
	tokens := map[string]string{}
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		for _, id := range ids {
			var c credentialRecord
			raw := tx.Bucket(bCredentials).Get([]byte(id))
			if raw == nil {
				continue
			} // A credential may be deleted after the preceding list read.
			if err := decode(raw, &c); err != nil {
				return err
			}
			if c.UserID == userID && c.RevokedAt.IsZero() && c.Token != "" {
				tokens[id] = c.Token
			}
		}
		return nil
	})
	return tokens, err
}

func (s *MySQLStore) RestoreCredentialToken(ctx context.Context, actor, userID, id, input string) (IssuedCredential, error) {
	if s.identityMode == "follower" {
		return IssuedCredential{}, ErrIdentityManagedByMaster
	}
	token, err := restoredToken(input)
	if err != nil {
		return IssuedCredential{}, err
	}
	now := s.clock.Now().UTC()
	var out Credential
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		c, err := mysqlCredential(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if c.UserID != userID {
			return ErrForbidden
		}
		if !c.RevokedAt.IsZero() {
			return ErrConflict
		}
		if !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
			return ErrForbidden
		}
		if !credentialTokenMatches(c.ID, token, c.TokenHash, c.LegacySecretHash) {
			return ErrTokenMismatch
		}
		if err := putMySQLCredentialToken(ctx, tx, c.ID, token, c.Version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mosdns_credentials SET updated_at_ns=? WHERE id=?`, now.UnixNano(), id); err != nil {
			return err
		}
		c.UpdatedAt = now
		out = c.Credential
		return mysqlAudit(ctx, tx, actor, "restore_credential_token", "credential", id, map[string]any{"user_id": userID}, now)
	})
	return IssuedCredential{Credential: out, Token: token}, err
}

func (s *Store) RestoreCredentialToken(ctx context.Context, actor, userID, id, input string) (IssuedCredential, error) {
	token, err := restoredToken(input)
	if err != nil {
		return IssuedCredential{}, err
	}
	now := s.clock.Now().UTC()
	var out Credential
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		if err := authorizeCredentialOwner(tx, actor, userID, now); err != nil {
			return err
		}
		var c credentialRecord
		if err := decode(tx.Bucket(bCredentials).Get([]byte(id)), &c); err != nil {
			return err
		}
		if c.UserID != userID {
			return ErrForbidden
		}
		if !c.RevokedAt.IsZero() {
			return ErrConflict
		}
		if !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
			return ErrForbidden
		}
		if !credentialTokenMatches(c.ID, token, c.TokenHash, c.SecretHash[:]) {
			return ErrTokenMismatch
		}
		c.Token = token
		c.UpdatedAt = now
		if err := marshalPut(tx.Bucket(bCredentials), []byte(id), c); err != nil {
			return err
		}
		out = c.Credential
		return s.audit(tx, actor, "restore_credential_token", "credential", id, map[string]any{"user_id": userID}, now)
	})
	return IssuedCredential{Credential: out, Token: token}, err
}
