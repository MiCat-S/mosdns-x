package control

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/miekg/dns"
	"go.etcd.io/bbolt"
)

const (
	maxDNSPolicyValueLength  = 1024
	maxDNSPolicyRulesPerUser = 1000
)

func defaultDNSPolicySettings(userID string, updatedAt time.Time) DNSPolicySettings {
	return DNSPolicySettings{UserID: userID, BlockedQTypes: []string{}, UpdatedAt: updatedAt.UTC()}
}

func putDefaultDNSPolicySettings(tx *bbolt.Tx, userID string, updatedAt time.Time) error {
	b := tx.Bucket(bDNSPolicySettings)
	if b.Get([]byte(userID)) != nil {
		return nil
	}
	return marshalPut(b, []byte(userID), defaultDNSPolicySettings(userID, updatedAt))
}

func initializeDNSPolicyData(tx *bbolt.Tx) error {
	if err := tx.Bucket(bUsers).ForEach(func(userID, value []byte) error {
		var user userRecord
		if err := decode(value, &user); err != nil {
			return err
		}
		return putDefaultDNSPolicySettings(tx, string(userID), user.CreatedAt)
	}); err != nil {
		return err
	}
	index := tx.Bucket(bUserDNSPolicyRules)
	cursor := index.Cursor()
	for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
		if err := cursor.Delete(); err != nil {
			return err
		}
	}
	return tx.Bucket(bDNSPolicyRules).ForEach(func(id, value []byte) error {
		var rule DNSPolicyRule
		if err := decode(value, &rule); err != nil {
			return err
		}
		if rule.ID != string(id) || rule.UserID == "" {
			return fmt.Errorf("invalid dns policy rule %q", id)
		}
		return index.Put(dnsPolicyRuleIndexKey(rule.UserID, rule.Priority, rule.ID), id)
	})
}

func applyDNSPolicySettingsPatch(current DNSPolicySettings, patch DNSPolicySettingsPatch) (DNSPolicySettings, error) {
	if patch.StripECS == nil && patch.BlockPrivateAnswers == nil && patch.BlockedQTypes == nil {
		return current, ErrInvalidInput
	}
	if patch.StripECS != nil {
		current.StripECS = *patch.StripECS
	}
	if patch.BlockPrivateAnswers != nil {
		current.BlockPrivateAnswers = *patch.BlockPrivateAnswers
	}
	if patch.BlockedQTypes != nil {
		seen := make(map[string]struct{}, len(*patch.BlockedQTypes))
		current.BlockedQTypes = make([]string, 0, len(*patch.BlockedQTypes))
		for _, qtype := range *patch.BlockedQTypes {
			qtype = strings.ToUpper(strings.TrimSpace(qtype))
			if qtype == "" {
				return current, fmt.Errorf("%w: blocked_qtypes contains an empty value", ErrInvalidInput)
			}
			if _, ok := dns.StringToType[qtype]; !ok {
				return current, fmt.Errorf("%w: unknown blocked qtype %q", ErrInvalidInput, qtype)
			}
			if _, ok := seen[qtype]; ok {
				continue
			}
			seen[qtype] = struct{}{}
			current.BlockedQTypes = append(current.BlockedQTypes, qtype)
		}
	}
	if current.BlockedQTypes == nil {
		current.BlockedQTypes = []string{}
	}
	return current, nil
}

func normalizeDNSPolicyRuleSpec(spec DNSPolicyRuleSpec) (DNSPolicyRuleSpec, error) {
	switch spec.Action {
	case DNSPolicyAllow, DNSPolicyBlock, DNSPolicyRewrite:
	default:
		return spec, fmt.Errorf("%w: invalid dns policy action", ErrInvalidInput)
	}
	pattern := strings.TrimSpace(spec.Pattern)
	if pattern == "" || len(pattern) > maxDNSPolicyValueLength {
		return spec, fmt.Errorf("%w: invalid dns policy pattern length", ErrInvalidInput)
	}
	switch spec.Match {
	case DNSPolicyMatchExact:
		var err error
		pattern, err = normalizePolicyDomain(spec.Pattern, false)
		if err != nil {
			return spec, err
		}
	case DNSPolicyMatchSuffix:
		var err error
		pattern, err = normalizePolicyDomain(spec.Pattern, true)
		if err != nil {
			return spec, err
		}
	case DNSPolicyMatchKeyword:
		pattern = strings.ToLower(pattern)
	case DNSPolicyMatchRegexp:
		if _, err := regexp.Compile(pattern); err != nil {
			return spec, fmt.Errorf("%w: invalid dns policy regexp: %v", ErrInvalidInput, err)
		}
	default:
		return spec, fmt.Errorf("%w: invalid dns policy match", ErrInvalidInput)
	}
	spec.Pattern = pattern
	if spec.Action != DNSPolicyRewrite {
		if spec.RecordType != "" || strings.TrimSpace(spec.Value) != "" {
			return spec, fmt.Errorf("%w: rewrite fields require rewrite action", ErrInvalidInput)
		}
		return spec, nil
	}
	value := strings.TrimSpace(spec.Value)
	switch spec.RecordType {
	case DNSPolicyRewriteA:
		addr, err := netip.ParseAddr(value)
		if err != nil || !addr.Is4() {
			return spec, fmt.Errorf("%w: A rewrite requires an IPv4 address", ErrInvalidInput)
		}
		value = addr.String()
	case DNSPolicyRewriteAAAA:
		addr, err := netip.ParseAddr(value)
		if err != nil || !addr.Is6() || addr.Is4In6() {
			return spec, fmt.Errorf("%w: AAAA rewrite requires an IPv6 address", ErrInvalidInput)
		}
		value = addr.String()
	case DNSPolicyRewriteCNAME:
		var err error
		value, err = normalizePolicyDomain(spec.Value, false)
		if err != nil {
			return spec, fmt.Errorf("%w: invalid CNAME rewrite", ErrInvalidInput)
		}
	default:
		return spec, fmt.Errorf("%w: record_type must be A, AAAA or CNAME", ErrInvalidInput)
	}
	spec.Value = value
	return spec, nil
}

func normalizePolicyDomain(value string, allowLeadingDot bool) (string, error) {
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("%w: dns policy domain contains whitespace or control characters", ErrInvalidInput)
	}
	if allowLeadingDot {
		value = strings.TrimPrefix(value, ".")
	}
	value = dns.CanonicalName(value)
	value = strings.ToLower(value)
	if len(value) > 255 {
		return "", fmt.Errorf("%w: dns policy domain is too long", ErrInvalidInput)
	}
	if _, ok := dns.IsDomainName(value); !ok {
		return "", fmt.Errorf("%w: invalid dns policy domain", ErrInvalidInput)
	}
	labels := dns.SplitDomainName(value)
	if len(labels) == 0 {
		return "", fmt.Errorf("%w: invalid dns policy domain", ErrInvalidInput)
	}
	for _, label := range labels {
		if !validDNSPolicyLabel(label) {
			return "", fmt.Errorf("%w: invalid dns policy domain label", ErrInvalidInput)
		}
	}
	return value, nil
}

func validDNSPolicyLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := range len(label) {
		c := label[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func dnsPolicyRuleIndexKey(userID string, priority uint32, ruleID string) []byte {
	key := make([]byte, len(userID)+1+4+len(ruleID))
	copy(key, userID)
	binary.BigEndian.PutUint32(key[len(userID)+1:], priority)
	copy(key[len(userID)+1+4:], ruleID)
	return key
}

func dnsPolicyRuleIndexPrefix(userID string) []byte { return []byte(userID + "\x00") }

func dnsPolicyRuleCursor(priority uint32, ruleID string) string {
	return strconv.FormatUint(uint64(priority), 10) + "." + ruleID
}

func parseDNSPolicyRuleCursor(value string) (uint32, string, error) {
	if value == "" {
		return 0, "", nil
	}
	priorityText, ruleID, ok := strings.Cut(value, ".")
	priority, err := strconv.ParseUint(priorityText, 10, 32)
	if !ok || ruleID == "" || err != nil {
		return 0, "", ErrInvalidInput
	}
	return uint32(priority), ruleID, nil
}

func getDNSPolicyRule(tx *bbolt.Tx, ruleID string) (DNSPolicyRule, error) {
	var rule DNSPolicyRule
	err := decode(tx.Bucket(bDNSPolicyRules).Get([]byte(ruleID)), &rule)
	return rule, err
}

func (s *Store) GetDNSPolicySettings(ctx context.Context, userID string) (DNSPolicySettings, error) {
	if userID == "" {
		return DNSPolicySettings{}, ErrInvalidInput
	}
	var out DNSPolicySettings
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		if _, err := getUserRecord(tx, userID); err != nil {
			return err
		}
		if err := decode(tx.Bucket(bDNSPolicySettings).Get([]byte(userID)), &out); err != nil {
			return err
		}
		if out.BlockedQTypes == nil {
			out.BlockedQTypes = []string{}
		}
		return nil
	})
	return out, err
}

func (s *Store) UpdateDNSPolicySettings(ctx context.Context, actor, userID string, patch DNSPolicySettingsPatch) (DNSPolicySettings, error) {
	if userID == "" {
		return DNSPolicySettings{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	var out DNSPolicySettings
	err := s.update(ctx, func(tx *bbolt.Tx) error {
		if err := authorizeCredentialOwner(tx, actor, userID, now); err != nil {
			return err
		}
		var current DNSPolicySettings
		if err := decode(tx.Bucket(bDNSPolicySettings).Get([]byte(userID)), &current); err != nil {
			return err
		}
		updated, err := applyDNSPolicySettingsPatch(current, patch)
		if err != nil {
			return err
		}
		out = updated
		out.UserID, out.UpdatedAt = userID, now
		if err := marshalPut(tx.Bucket(bDNSPolicySettings), []byte(userID), out); err != nil {
			return err
		}
		return s.audit(tx, actor, "update_dns_policy_settings", "dns_policy_settings", userID, map[string]any{"before": current, "after": out}, now)
	})
	return out, err
}

func (s *Store) CreateDNSPolicyRule(ctx context.Context, actor, userID string, spec DNSPolicyRuleSpec) (DNSPolicyRule, error) {
	spec, err := normalizeDNSPolicyRuleSpec(spec)
	if err != nil || userID == "" {
		if err == nil {
			err = ErrInvalidInput
		}
		return DNSPolicyRule{}, err
	}
	now := s.clock.Now().UTC()
	var out DNSPolicyRule
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		if err := authorizeCredentialOwner(tx, actor, userID, now); err != nil {
			return err
		}
		prefix := dnsPolicyRuleIndexPrefix(userID)
		count := 0
		cursor := tx.Bucket(bUserDNSPolicyRules).Cursor()
		for k, _ := cursor.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, _ = cursor.Next() {
			count++
			if count >= maxDNSPolicyRulesPerUser {
				return fmt.Errorf("%w: dns policy rule limit reached", ErrConflict)
			}
		}
		var id string
		for range maxCredentialGenerationAttempts {
			id, err = randomText(16)
			if err != nil {
				return err
			}
			if tx.Bucket(bDNSPolicyRules).Get([]byte(id)) == nil {
				break
			}
			id = ""
		}
		if id == "" {
			return errors.New("dns policy rule id collision limit reached")
		}
		out = DNSPolicyRule{ID: id, UserID: userID, Enabled: spec.Enabled, Priority: spec.Priority, Action: spec.Action, Match: spec.Match, Pattern: spec.Pattern, RecordType: spec.RecordType, Value: spec.Value, CreatedAt: now, UpdatedAt: now}
		if err := marshalPut(tx.Bucket(bDNSPolicyRules), []byte(id), out); err != nil {
			return err
		}
		if err := tx.Bucket(bUserDNSPolicyRules).Put(dnsPolicyRuleIndexKey(userID, out.Priority, id), []byte(id)); err != nil {
			return err
		}
		return s.audit(tx, actor, "create_dns_policy_rule", "dns_policy_rule", id, map[string]any{"rule": out}, now)
	})
	return out, err
}

func (s *Store) GetDNSPolicyRule(ctx context.Context, userID, ruleID string) (DNSPolicyRule, error) {
	if userID == "" || ruleID == "" {
		return DNSPolicyRule{}, ErrInvalidInput
	}
	var out DNSPolicyRule
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		rule, err := getDNSPolicyRule(tx, ruleID)
		if err != nil || rule.UserID != userID {
			return ErrNotFound
		}
		out = rule
		return nil
	})
	return out, err
}

func applyDNSPolicyRulePatch(rule DNSPolicyRule, patch DNSPolicyRulePatch) (DNSPolicyRule, error) {
	if patch.Enabled == nil && patch.Priority == nil && patch.Action == nil && patch.Match == nil && patch.Pattern == nil && patch.RecordType == nil && patch.Value == nil {
		return rule, ErrInvalidInput
	}
	if patch.Enabled != nil {
		rule.Enabled = *patch.Enabled
	}
	if patch.Priority != nil {
		rule.Priority = *patch.Priority
	}
	if patch.Action != nil {
		rule.Action = *patch.Action
		if rule.Action != DNSPolicyRewrite {
			rule.RecordType, rule.Value = "", ""
		}
	}
	if patch.Match != nil {
		rule.Match = *patch.Match
	}
	if patch.Pattern != nil {
		rule.Pattern = *patch.Pattern
	}
	if patch.RecordType != nil {
		rule.RecordType = *patch.RecordType
	}
	if patch.Value != nil {
		rule.Value = *patch.Value
	}
	normalized, err := normalizeDNSPolicyRuleSpec(DNSPolicyRuleSpec{Enabled: rule.Enabled, Priority: rule.Priority, Action: rule.Action, Match: rule.Match, Pattern: rule.Pattern, RecordType: rule.RecordType, Value: rule.Value})
	if err != nil {
		return rule, err
	}
	rule.Enabled, rule.Priority, rule.Action = normalized.Enabled, normalized.Priority, normalized.Action
	rule.Match, rule.Pattern = normalized.Match, normalized.Pattern
	rule.RecordType, rule.Value = normalized.RecordType, normalized.Value
	return rule, nil
}

func (s *Store) UpdateDNSPolicyRule(ctx context.Context, actor, userID, ruleID string, patch DNSPolicyRulePatch) (DNSPolicyRule, error) {
	if userID == "" || ruleID == "" {
		return DNSPolicyRule{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	var out DNSPolicyRule
	err := s.update(ctx, func(tx *bbolt.Tx) error {
		if err := authorizeCredentialOwner(tx, actor, userID, now); err != nil {
			return err
		}
		current, err := getDNSPolicyRule(tx, ruleID)
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
		if current.Priority != out.Priority {
			if err := tx.Bucket(bUserDNSPolicyRules).Delete(dnsPolicyRuleIndexKey(userID, current.Priority, ruleID)); err != nil {
				return err
			}
		}
		if err := marshalPut(tx.Bucket(bDNSPolicyRules), []byte(ruleID), out); err != nil {
			return err
		}
		if err := tx.Bucket(bUserDNSPolicyRules).Put(dnsPolicyRuleIndexKey(userID, out.Priority, ruleID), []byte(ruleID)); err != nil {
			return err
		}
		return s.audit(tx, actor, "update_dns_policy_rule", "dns_policy_rule", ruleID, map[string]any{"before": current, "after": out}, now)
	})
	return out, err
}

func (s *Store) DeleteDNSPolicyRule(ctx context.Context, actor, userID, ruleID string) error {
	if userID == "" || ruleID == "" {
		return ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		if err := authorizeCredentialOwner(tx, actor, userID, now); err != nil {
			return err
		}
		rule, err := getDNSPolicyRule(tx, ruleID)
		if err != nil {
			return err
		}
		if rule.UserID != userID {
			return ErrForbidden
		}
		if err := tx.Bucket(bDNSPolicyRules).Delete([]byte(ruleID)); err != nil {
			return err
		}
		if err := tx.Bucket(bUserDNSPolicyRules).Delete(dnsPolicyRuleIndexKey(userID, rule.Priority, ruleID)); err != nil {
			return err
		}
		return s.audit(tx, actor, "delete_dns_policy_rule", "dns_policy_rule", ruleID, map[string]any{"rule": rule}, now)
	})
}

func (s *Store) ListDNSPolicyRules(ctx context.Context, userID string, p Page) (PageResult[DNSPolicyRule], error) {
	out := PageResult[DNSPolicyRule]{Items: []DNSPolicyRule{}}
	if userID == "" {
		return out, ErrInvalidInput
	}
	limit, err := pageLimit(p)
	if err != nil {
		return out, err
	}
	cursorPriority, cursorID, err := parseDNSPolicyRuleCursor(p.Cursor)
	if err != nil {
		return out, err
	}
	prefix := dnsPolicyRuleIndexPrefix(userID)
	err = s.view(ctx, func(tx *bbolt.Tx) error {
		if _, err := getUserRecord(tx, userID); err != nil {
			return err
		}
		c := tx.Bucket(bUserDNSPolicyRules).Cursor()
		k, id := c.Seek(prefix)
		if p.Cursor != "" {
			cursorKey := dnsPolicyRuleIndexKey(userID, cursorPriority, cursorID)
			k, id = c.Seek(cursorKey)
			if string(k) == string(cursorKey) {
				k, id = c.Next()
			}
		}
		for ; k != nil && strings.HasPrefix(string(k), string(prefix)); k, id = c.Next() {
			rule, err := getDNSPolicyRule(tx, string(id))
			if err != nil {
				return err
			}
			out.Items = append(out.Items, rule)
			if len(out.Items) == limit+1 {
				break
			}
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			out.NextCursor = dnsPolicyRuleCursor(last.Priority, last.ID)
		}
		return nil
	})
	return out, err
}
