package control

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.etcd.io/bbolt"
)

// MySQLMigrationReport describes records copied from a bbolt control database.
type MySQLMigrationReport struct {
	Users          uint64 `json:"users"`
	PolicySettings uint64 `json:"policy_settings"`
	PolicyRules    uint64 `json:"policy_rules"`
	Sessions       uint64 `json:"sessions"`
	Credentials    uint64 `json:"credentials"`
	Usage          uint64 `json:"usage"`
	Audit          uint64 `json:"audit"`
	PublicLists    uint64 `json:"public_lists"`
	ListOverrides  uint64 `json:"list_overrides"`
}

// InspectBoltForMySQL validates an offline bbolt database and counts the rows
// that a MySQL migration would create.
func InspectBoltForMySQL(ctx context.Context, source string) (MySQLMigrationReport, error) {
	var report MySQLMigrationReport
	err := withBoltMigrationSource(ctx, source, func(tx *bbolt.Tx) error {
		policyRules := uint64(0)
		publicLists, listOverrides := uint64(0), uint64(0)
		if bucket := tx.Bucket(bDNSPolicyRules); bucket != nil {
			policyRules = uint64(bucket.Stats().KeyN)
		}
		if bucket := tx.Bucket(bPublicLists); bucket != nil {
			publicLists = uint64(bucket.Stats().KeyN)
		}
		if bucket := tx.Bucket(bUserPublicLists); bucket != nil {
			listOverrides = uint64(bucket.Stats().KeyN)
		}
		report = MySQLMigrationReport{
			Users:          uint64(tx.Bucket(bUsers).Stats().KeyN),
			PolicySettings: uint64(tx.Bucket(bUsers).Stats().KeyN),
			PolicyRules:    policyRules,
			Sessions:       uint64(tx.Bucket(bSessions).Stats().KeyN),
			Credentials:    uint64(tx.Bucket(bCredentials).Stats().KeyN),
			Usage:          uint64(tx.Bucket(bUsage).Stats().KeyN),
			Audit:          uint64(tx.Bucket(bAudit).Stats().KeyN),
			PublicLists:    publicLists, ListOverrides: listOverrides,
		}
		return nil
	})
	return report, err
}

// MigrateBoltToMySQL copies an offline bbolt control database into an empty
// MySQL control schema. The copy is committed atomically.
func MigrateBoltToMySQL(ctx context.Context, source string, destination *MySQLStore) (MySQLMigrationReport, error) {
	var report MySQLMigrationReport
	if destination == nil || destination.closed.Load() {
		return report, ErrUnavailable
	}
	err := withBoltMigrationSource(ctx, source, func(sourceTx *bbolt.Tx) error {
		return destination.withTx(ctx, func(ctx context.Context, targetTx *sql.Tx) error {
			if err := requireEmptyMySQLControl(ctx, targetTx); err != nil {
				return err
			}
			if err := migrateUsers(ctx, sourceTx, targetTx, &report); err != nil {
				return err
			}
			if err := migrateDNSPolicies(ctx, sourceTx, targetTx, &report); err != nil {
				return err
			}
			if err := migratePublicLists(ctx, sourceTx, targetTx, &report); err != nil {
				return err
			}
			if err := migrateSessions(ctx, sourceTx, targetTx, &report); err != nil {
				return err
			}
			if err := migrateCredentials(ctx, sourceTx, targetTx, &report); err != nil {
				return err
			}
			if err := migrateUsage(ctx, sourceTx, targetTx, &report); err != nil {
				return err
			}
			if err := migrateAudit(ctx, sourceTx, targetTx, &report); err != nil {
				return err
			}
			_, err := targetTx.ExecContext(ctx, `UPDATE mosdns_control_meta SET initialized=? WHERE id=1`, report.Users > 0)
			return err
		})
	})
	return report, err
}

func withBoltMigrationSource(ctx context.Context, source string, fn func(*bbolt.Tx) error) error {
	if source == "" {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := bbolt.Open(source, 0o600, &bbolt.Options{ReadOnly: true, Timeout: 100 * time.Millisecond})
	if err != nil {
		return fmt.Errorf("%w: open migration source: %v", ErrUnavailable, err)
	}
	defer db.Close()
	return db.View(func(tx *bbolt.Tx) error {
		if err := validateSchema(tx); err != nil {
			return fmt.Errorf("%w: invalid migration source: %v", ErrInvalidInput, err)
		}
		return fn(tx)
	})
}

func requireEmptyMySQLControl(ctx context.Context, tx *sql.Tx) error {
	var initialized bool
	var users, policySettings, policyRules, publicLists, listOverrides, sessions, credentials, usage, audit uint64
	err := tx.QueryRowContext(ctx, `SELECT initialized,
		(SELECT COUNT(*) FROM mosdns_users),
		(SELECT COUNT(*) FROM mosdns_dns_policy_settings),
		(SELECT COUNT(*) FROM mosdns_dns_policy_rules),
		(SELECT COUNT(*) FROM mosdns_public_lists),
		(SELECT COUNT(*) FROM mosdns_user_public_lists),
		(SELECT COUNT(*) FROM mosdns_sessions),
		(SELECT COUNT(*) FROM mosdns_credentials),
		(SELECT COUNT(*) FROM mosdns_usage_minutes),
		(SELECT COUNT(*) FROM mosdns_audit_logs)
		FROM mosdns_control_meta WHERE id=1 FOR UPDATE`).Scan(&initialized, &users, &policySettings, &policyRules, &publicLists, &listOverrides, &sessions, &credentials, &usage, &audit)
	if err != nil {
		return err
	}
	if initialized || users+policySettings+policyRules+publicLists+listOverrides+sessions+credentials+usage+audit != 0 {
		return fmt.Errorf("%w: mysql control destination is not empty", ErrConflict)
	}
	return nil
}

func migratePublicLists(ctx context.Context, source *bbolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	lists := source.Bucket(bPublicLists)
	if lists == nil {
		return nil
	}
	if err := lists.ForEach(func(_, value []byte) error {
		var list PublicList
		if err := json.Unmarshal(value, &list); err != nil {
			return err
		}
		if err := insertMySQLPublicList(ctx, target, list); err != nil {
			return err
		}
		report.PublicLists++
		return nil
	}); err != nil {
		return err
	}
	overrides := source.Bucket(bUserPublicLists)
	if overrides == nil {
		return nil
	}
	return overrides.ForEach(func(key, value []byte) error {
		parts := strings.Split(string(key), "\x00")
		if len(parts) != 2 || len(value) != 1 {
			return fmt.Errorf("invalid public list override")
		}
		_, err := target.ExecContext(ctx, `INSERT INTO mosdns_user_public_lists (user_id,list_id,enabled) VALUES (?,?,?)`, parts[0], parts[1], value[0] == 1)
		if err == nil {
			report.ListOverrides++
		}
		return err
	})
}

func migrateDNSPolicies(ctx context.Context, source *bbolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	settingsBucket := source.Bucket(bDNSPolicySettings)
	sourceVersion := binary.BigEndian.Uint64(source.Bucket(bMeta).Get(kSchema))
	if err := source.Bucket(bUsers).ForEach(func(userID, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var user userRecord
		if err := json.Unmarshal(value, &user); err != nil {
			return err
		}
		settings := defaultDNSPolicySettings(string(userID), user.CreatedAt)
		if settingsBucket != nil {
			if encoded := settingsBucket.Get(userID); encoded != nil {
				if err := json.Unmarshal(encoded, &settings); err != nil {
					return err
				}
				if settings.BlockedQTypes == nil {
					settings.BlockedQTypes = []string{}
				}
			}
		}
		settings.UserID = string(userID)
		if sourceVersion < policySwitchSchemaVersion {
			settings.CustomBlockEnabled = true
			settings.CustomAllowEnabled = true
			settings.CustomRewriteEnabled = true
		}
		if err := insertMySQLDNSPolicySettings(ctx, target, settings); err != nil {
			return err
		}
		report.PolicySettings++
		return nil
	}); err != nil {
		return err
	}
	rulesBucket := source.Bucket(bDNSPolicyRules)
	if rulesBucket == nil {
		return nil
	}
	return rulesBucket.ForEach(func(_, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rule DNSPolicyRule
		if err := json.Unmarshal(value, &rule); err != nil {
			return err
		}
		if err := insertMySQLDNSPolicyRule(ctx, target, rule); err != nil {
			return err
		}
		report.PolicyRules++
		return nil
	})
}

func migrateUsers(ctx context.Context, source *bbolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	return source.Bucket(bUsers).ForEach(func(_, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record userRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if err := insertMySQLUser(ctx, target, record); err != nil {
			return err
		}
		report.Users++
		return nil
	})
}

func migrateSessions(ctx context.Context, source *bbolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	return source.Bucket(bSessions).ForEach(func(id, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record sessionRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		_, err := target.ExecContext(ctx, `INSERT INTO mosdns_sessions
			(id, user_id, csrf_token, secret_hash, expires_at_ns, revoked_at_ns, created_at_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, string(id), record.UserID, record.CSRFToken, record.SecretHash[:],
			record.ExpiresAt.UnixNano(), mysqlTimeValue(record.RevokedAt), record.CreatedAt.UnixNano())
		if err == nil {
			report.Sessions++
		}
		return err
	})
}

func migrateCredentials(ctx context.Context, source *bbolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	return source.Bucket(bCredentials).ForEach(func(id, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record credentialRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		var legacyHash any
		if record.SecretHash != ([32]byte{}) {
			legacyHash = record.SecretHash[:]
		}
		var tokenHash any
		if len(record.TokenHash) > 0 {
			if len(record.TokenHash) != 32 {
				return fmt.Errorf("invalid credential token hash for %q", id)
			}
			tokenHash = record.TokenHash
		}
		_, err := target.ExecContext(ctx, `INSERT INTO mosdns_credentials
			(id, user_id, name, expires_at_ns, revoked_at_ns, created_at_ns, updated_at_ns, legacy_secret_hash, token_hash, version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, string(id), record.UserID, record.Name,
			mysqlTimeValue(record.ExpiresAt), mysqlTimeValue(record.RevokedAt), record.CreatedAt.UnixNano(),
			record.UpdatedAt.UnixNano(), legacyHash, tokenHash, record.Version)
		if err == nil {
			report.Credentials++
		}
		return err
	})
}

func migrateUsage(ctx context.Context, source *bbolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	return source.Bucket(bUsage).ForEach(func(key, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(value) != 8 {
			return fmt.Errorf("invalid usage value for %q", key)
		}
		parts := strings.Split(string(key), "\x00")
		if len(parts) < 2 || len(parts) > 3 {
			return fmt.Errorf("invalid usage key %q", key)
		}
		minute, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid usage key %q: %w", key, err)
		}
		kind, scopeID, credentialID := "", "", ""
		switch {
		case parts[0] == "g" && len(parts) == 2:
			kind = "g"
		case strings.HasPrefix(parts[0], "u:") && len(parts) == 2:
			kind, scopeID = "u", strings.TrimPrefix(parts[0], "u:")
		case strings.HasPrefix(parts[0], "d:") && len(parts) == 3:
			kind, scopeID, credentialID = "d", strings.TrimPrefix(parts[0], "d:"), parts[2]
		default:
			return fmt.Errorf("invalid usage key %q", key)
		}
		_, err = target.ExecContext(ctx, `INSERT INTO mosdns_usage_minutes
			(scope_kind, scope_id, credential_id, minute_epoch, query_count) VALUES (?, ?, ?, ?, ?)`,
			kind, scopeID, credentialID, minute, binary.BigEndian.Uint64(value))
		if err == nil {
			report.Usage++
		}
		return err
	})
}

func migrateAudit(ctx context.Context, source *bbolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	return source.Bucket(bAudit).ForEach(func(_, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record AuditRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		var metadata any
		if record.Metadata != nil {
			encoded, err := json.Marshal(record.Metadata)
			if err != nil {
				return err
			}
			metadata = encoded
		}
		_, err := target.ExecContext(ctx, `INSERT INTO mosdns_audit_logs
			(id, actor_id, action, target_type, target_id, metadata_json, created_at_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, record.ID, record.ActorID, record.Action, record.TargetType,
			record.TargetID, metadata, record.CreatedAt.UnixNano())
		if err == nil {
			report.Audit++
		}
		return err
	})
}
