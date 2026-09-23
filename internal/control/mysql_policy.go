package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	mysqlDNSPolicySettingsColumns = `user_id, strip_ecs, block_private_answers, blocked_qtypes_json, custom_block_enabled, custom_allow_enabled, custom_rewrite_enabled, policy_paused_until_ns, answer_family, ttl_min, ttl_max, flatten_cname, shuffle_answers, ecs_ipv4, ecs_ipv6, updated_at_ns`
	mysqlDNSPolicyRuleColumns     = `id, user_id, enabled, priority, action, match_kind, pattern, record_type, rewrite_value, created_at_ns, updated_at_ns`
)

func insertMySQLDNSPolicySettings(ctx context.Context, tx *sql.Tx, settings DNSPolicySettings) error {
	qtypes := settings.BlockedQTypes
	if qtypes == nil {
		qtypes = []string{}
	}
	encoded, err := json.Marshal(qtypes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_dns_policy_settings
		(user_id, strip_ecs, block_private_answers, blocked_qtypes_json, custom_block_enabled,
		 custom_allow_enabled, custom_rewrite_enabled, policy_paused_until_ns, answer_family,
		 ttl_min, ttl_max, flatten_cname, shuffle_answers, ecs_ipv4, ecs_ipv6, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, settings.UserID, settings.StripECS, settings.BlockPrivateAnswers, encoded,
		settings.CustomBlockEnabled, settings.CustomAllowEnabled, settings.CustomRewriteEnabled,
		mysqlPolicyPauseValue(settings.PolicyPausedUntil), string(settings.AnswerFamily),
		settings.TTLMin, settings.TTLMax, settings.FlattenCNAME, settings.ShuffleAnswers,
		settings.ECSIPv4, settings.ECSIPv6, settings.UpdatedAt.UnixNano())
	return err
}

func mysqlPolicyPauseValue(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UnixNano()
}

func scanMySQLDNSPolicySettings(row sqlScanner) (DNSPolicySettings, error) {
	var out DNSPolicySettings
	var encoded []byte
	var paused sql.NullInt64
	var family string
	var updated int64
	err := row.Scan(&out.UserID, &out.StripECS, &out.BlockPrivateAnswers, &encoded,
		&out.CustomBlockEnabled, &out.CustomAllowEnabled, &out.CustomRewriteEnabled, &paused, &family,
		&out.TTLMin, &out.TTLMax, &out.FlattenCNAME, &out.ShuffleAnswers, &out.ECSIPv4, &out.ECSIPv6, &updated)
	if err != nil {
		return out, err
	}
	out.AnswerFamily = AnswerFamily(family)
	if err := json.Unmarshal(encoded, &out.BlockedQTypes); err != nil {
		return out, fmt.Errorf("decode blocked qtypes: %w", err)
	}
	if out.BlockedQTypes == nil {
		out.BlockedQTypes = []string{}
	}
	if paused.Valid {
		value := time.Unix(0, paused.Int64).UTC()
		out.PolicyPausedUntil = &value
	}
	out.UpdatedAt = time.Unix(0, updated).UTC()
	return out, nil
}

func mysqlDNSPolicySettings(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID string, lock bool) (DNSPolicySettings, error) {
	query := `SELECT ` + mysqlDNSPolicySettingsColumns + ` FROM mosdns_dns_policy_settings WHERE user_id=?`
	if lock {
		query += ` FOR UPDATE`
	}
	out, err := scanMySQLDNSPolicySettings(q.QueryRowContext(ctx, query, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

func (s *MySQLStore) GetDNSPolicySettings(ctx context.Context, userID string) (DNSPolicySettings, error) {
	if s.closed.Load() {
		return DNSPolicySettings{}, ErrUnavailable
	}
	if userID == "" {
		return DNSPolicySettings{}, ErrInvalidInput
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	out, err := mysqlDNSPolicySettings(opCtx, s.db, userID, false)
	return out, mysqlStoreError(err)
}

func (s *MySQLStore) UpdateDNSPolicySettings(ctx context.Context, actor, userID string, patch DNSPolicySettingsPatch) (DNSPolicySettings, error) {
	if userID == "" {
		return DNSPolicySettings{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	var out DNSPolicySettings
	err := s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		current, err := mysqlDNSPolicySettings(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		out, err = applyDNSPolicySettingsPatch(current, patch, now)
		if err != nil {
			return err
		}
		out.UserID, out.UpdatedAt = userID, now
		encoded, err := json.Marshal(out.BlockedQTypes)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mosdns_dns_policy_settings SET strip_ecs=?, block_private_answers=?, blocked_qtypes_json=?, custom_block_enabled=?, custom_allow_enabled=?, custom_rewrite_enabled=?, policy_paused_until_ns=?, answer_family=?, ttl_min=?, ttl_max=?, flatten_cname=?, shuffle_answers=?, ecs_ipv4=?, ecs_ipv6=?, updated_at_ns=? WHERE user_id=?`,
			out.StripECS, out.BlockPrivateAnswers, encoded, out.CustomBlockEnabled, out.CustomAllowEnabled,
			out.CustomRewriteEnabled, mysqlPolicyPauseValue(out.PolicyPausedUntil), string(out.AnswerFamily),
			out.TTLMin, out.TTLMax, out.FlattenCNAME, out.ShuffleAnswers, out.ECSIPv4, out.ECSIPv6, now.UnixNano(), userID); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "update_dns_policy_settings", "dns_policy_settings", userID, map[string]any{"before": current, "after": out}, now)
	})
	return out, err
}

func scanMySQLDNSPolicyRule(row sqlScanner) (DNSPolicyRule, error) {
	var out DNSPolicyRule
	var action, match string
	var recordType, value sql.NullString
	var priority uint64
	var created, updated int64
	err := row.Scan(&out.ID, &out.UserID, &out.Enabled, &priority, &action, &match, &out.Pattern, &recordType, &value, &created, &updated)
	if err != nil {
		return out, err
	}
	if priority > uint64(^uint32(0)) {
		return out, fmt.Errorf("dns policy priority out of range")
	}
	out.Priority = uint32(priority)
	out.Action, out.Match = DNSPolicyAction(action), DNSPolicyMatchType(match)
	if recordType.Valid {
		out.RecordType = DNSPolicyRewriteType(recordType.String)
	}
	if value.Valid {
		out.Value = value.String
	}
	out.CreatedAt, out.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return out, nil
}

func mysqlDNSPolicyRule(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ruleID string, lock bool) (DNSPolicyRule, error) {
	query := `SELECT ` + mysqlDNSPolicyRuleColumns + ` FROM mosdns_dns_policy_rules WHERE id=?`
	if lock {
		query += ` FOR UPDATE`
	}
	out, err := scanMySQLDNSPolicyRule(q.QueryRowContext(ctx, query, ruleID))
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

func mysqlDNSPolicyRuleValues(rule DNSPolicyRule) (any, any) {
	if rule.Action != DNSPolicyRewrite {
		return nil, nil
	}
	return string(rule.RecordType), rule.Value
}

func insertMySQLDNSPolicyRule(ctx context.Context, tx *sql.Tx, rule DNSPolicyRule) error {
	recordType, value := mysqlDNSPolicyRuleValues(rule)
	_, err := tx.ExecContext(ctx, `INSERT INTO mosdns_dns_policy_rules
		(id, user_id, enabled, priority, action, match_kind, pattern, record_type, rewrite_value, created_at_ns, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rule.ID, rule.UserID, rule.Enabled, rule.Priority, string(rule.Action), string(rule.Match), rule.Pattern, recordType, value, rule.CreatedAt.UnixNano(), rule.UpdatedAt.UnixNano())
	return err
}

func (s *MySQLStore) CreateDNSPolicyRule(ctx context.Context, actor, userID string, spec DNSPolicyRuleSpec) (DNSPolicyRule, error) {
	spec, err := normalizeDNSPolicyRuleSpec(spec)
	if err != nil || userID == "" {
		if err == nil {
			err = ErrInvalidInput
		}
		return DNSPolicyRule{}, err
	}
	id, err := randomText(16)
	if err != nil {
		return DNSPolicyRule{}, err
	}
	now := s.clock.Now().UTC()
	out := DNSPolicyRule{ID: id, UserID: userID, Enabled: spec.Enabled, Priority: spec.Priority, Action: spec.Action, Match: spec.Match, Pattern: spec.Pattern, RecordType: spec.RecordType, Value: spec.Value, CreatedAt: now, UpdatedAt: now}
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		if actor != userID {
			if _, err := mysqlUser(ctx, tx, userID, true); err != nil {
				return err
			}
		}
		var count uint64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_dns_policy_rules WHERE user_id=?`, userID).Scan(&count); err != nil {
			return err
		}
		if count >= maxDNSPolicyRulesPerUser {
			return fmt.Errorf("%w: dns policy rule limit reached", ErrConflict)
		}
		if err := insertMySQLDNSPolicyRule(ctx, tx, out); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "create_dns_policy_rule", "dns_policy_rule", id, map[string]any{"rule": out}, now)
	})
	return out, err
}

func (s *MySQLStore) GetDNSPolicyRule(ctx context.Context, userID, ruleID string) (DNSPolicyRule, error) {
	if s.closed.Load() {
		return DNSPolicyRule{}, ErrUnavailable
	}
	if userID == "" || ruleID == "" {
		return DNSPolicyRule{}, ErrInvalidInput
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	out, err := mysqlDNSPolicyRule(opCtx, s.db, ruleID, false)
	if err == nil && out.UserID != userID {
		err = ErrNotFound
	}
	return out, mysqlStoreError(err)
}

func (s *MySQLStore) UpdateDNSPolicyRule(ctx context.Context, actor, userID, ruleID string, patch DNSPolicyRulePatch) (DNSPolicyRule, error) {
	if userID == "" || ruleID == "" {
		return DNSPolicyRule{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	var out DNSPolicyRule
	err := s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		current, err := mysqlDNSPolicyRule(ctx, tx, ruleID, true)
		if err != nil {
			return err
		}
		if current.UserID != userID {
			return ErrForbidden
		}
		out, err = applyDNSPolicyRulePatch(current, patch)
		if err != nil {
			return err
		}
		out.UpdatedAt = now
		recordType, value := mysqlDNSPolicyRuleValues(out)
		if _, err := tx.ExecContext(ctx, `UPDATE mosdns_dns_policy_rules SET enabled=?, priority=?, action=?, match_kind=?, pattern=?, record_type=?, rewrite_value=?, updated_at_ns=? WHERE id=?`, out.Enabled, out.Priority, string(out.Action), string(out.Match), out.Pattern, recordType, value, now.UnixNano(), ruleID); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "update_dns_policy_rule", "dns_policy_rule", ruleID, map[string]any{"before": current, "after": out}, now)
	})
	return out, err
}

func (s *MySQLStore) DeleteDNSPolicyRule(ctx context.Context, actor, userID, ruleID string) error {
	if userID == "" || ruleID == "" {
		return ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		rule, err := mysqlDNSPolicyRule(ctx, tx, ruleID, true)
		if err != nil {
			return err
		}
		if rule.UserID != userID {
			return ErrForbidden
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mosdns_dns_policy_rules WHERE id=?`, ruleID); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "delete_dns_policy_rule", "dns_policy_rule", ruleID, map[string]any{"rule": rule}, now)
	})
}

func (s *MySQLStore) ListDNSPolicyRules(ctx context.Context, userID string, p Page) (PageResult[DNSPolicyRule], error) {
	out := PageResult[DNSPolicyRule]{Items: []DNSPolicyRule{}}
	if s.closed.Load() {
		return out, ErrUnavailable
	}
	if userID == "" {
		return out, ErrInvalidInput
	}
	limit, err := pageLimit(p)
	if err != nil {
		return out, err
	}
	priority, id, err := parseDNSPolicyRuleCursor(p.Cursor)
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	if _, err := mysqlUser(opCtx, s.db, userID, false); err != nil {
		return out, mysqlStoreError(err)
	}
	query := `SELECT ` + mysqlDNSPolicyRuleColumns + ` FROM mosdns_dns_policy_rules WHERE user_id=?`
	args := []any{userID}
	if p.Cursor != "" {
		query += ` AND (priority>? OR (priority=? AND id>?))`
		args = append(args, priority, priority, id)
	}
	query += ` ORDER BY priority, id LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(opCtx, query, args...)
	if err != nil {
		return out, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		rule, err := scanMySQLDNSPolicyRule(rows)
		if err != nil {
			return out, mysqlStoreError(err)
		}
		out.Items = append(out.Items, rule)
	}
	if err := rows.Err(); err != nil {
		return out, mysqlStoreError(err)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.NextCursor = dnsPolicyRuleCursor(last.Priority, last.ID)
	}
	return out, nil
}
