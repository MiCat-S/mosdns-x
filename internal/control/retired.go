package control

import (
	"context"
	"database/sql"
	"time"

	"go.etcd.io/bbolt"
)

// The per-user DNS policy and public list features were removed. Their
// buckets and tables stay in the schema, unread, so an older binary can still
// open the database after a rollback. That binary expects every user to have
// a policy settings record, so new users still get one with the defaults it
// used, and deleting a user still clears the user's rows from those stores.

type retiredPolicySettings struct {
	UserID               string    `json:"user_id"`
	BlockedQTypes        []string  `json:"blocked_qtypes"`
	CustomBlockEnabled   bool      `json:"custom_block_enabled"`
	CustomAllowEnabled   bool      `json:"custom_allow_enabled"`
	CustomRewriteEnabled bool      `json:"custom_rewrite_enabled"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func putRetiredPolicyDefaults(tx *bbolt.Tx, userID string, now time.Time) error {
	b := tx.Bucket(bDNSPolicySettings)
	if b.Get([]byte(userID)) != nil {
		return nil
	}
	return marshalPut(b, []byte(userID), retiredPolicySettings{
		UserID: userID, BlockedQTypes: []string{},
		CustomBlockEnabled: true, CustomAllowEnabled: true, CustomRewriteEnabled: true,
		UpdatedAt: now.UTC(),
	})
}

// ensureRetiredPolicyDefaults gives users created before the policy schema a
// settings record, as the removed policy migration did.
func ensureRetiredPolicyDefaults(tx *bbolt.Tx, previousVersion uint64) error {
	if previousVersion >= policySchemaVersion {
		return nil
	}
	return tx.Bucket(bUsers).ForEach(func(k, v []byte) error {
		var r userRecord
		if err := decode(v, &r); err != nil {
			return err
		}
		return putRetiredPolicyDefaults(tx, string(k), r.CreatedAt)
	})
}

func insertMySQLRetiredPolicyDefaults(ctx context.Context, tx *sql.Tx, userID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mosdns_dns_policy_settings
		(user_id, blocked_qtypes_json, updated_at_ns) VALUES (?, '[]', ?)`, userID, now.UnixNano())
	return err
}
