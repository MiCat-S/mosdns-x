package control

import (
	"context"
	"database/sql"
	"encoding/json"

	"go.etcd.io/bbolt"
)

// Audit field limits, the MySQL column widths. Fields are ASCII because the
// columns are, and the bbolt store keeps the same rule so both stores accept
// the same records.
const (
	maxAuditAction     = 64
	maxAuditTargetType = 32
	maxAuditTarget     = 64
	maxAuditMetadata   = 4096
)

func validateAuditInput(action, targetType, target string, metadata map[string]any) error {
	if action == "" || len(action) > maxAuditAction || targetType == "" || len(targetType) > maxAuditTargetType || len(target) > maxAuditTarget {
		return ErrInvalidInput
	}
	for _, s := range []string{action, targetType, target} {
		for i := 0; i < len(s); i++ {
			if s[i] < 0x21 || s[i] > 0x7e {
				return ErrInvalidInput
			}
		}
	}
	if metadata != nil {
		b, err := json.Marshal(metadata)
		if err != nil || len(b) > maxAuditMetadata {
			return ErrInvalidInput
		}
	}
	return nil
}

// RecordAudit stores an audit record for an action the caller already
// authorized and performed outside the store, such as a runtime change.
// The actor must be an enabled admin.
func (s *Store) RecordAudit(ctx context.Context, actor, action, targetType, target string, metadata map[string]any) error {
	if err := validateAuditInput(action, targetType, target, metadata); err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		if err := requireAdmin(tx, actor, now); err != nil {
			return err
		}
		return s.audit(tx, actor, action, targetType, target, metadata, now)
	})
}

// RecordAudit is the MySQL counterpart of Store.RecordAudit.
func (s *MySQLStore) RecordAudit(ctx context.Context, actor, action, targetType, target string, metadata map[string]any) error {
	if err := validateAuditInput(action, targetType, target, metadata); err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, action, targetType, target, metadata, now)
	})
}
