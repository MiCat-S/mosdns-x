package control

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestDNSPolicySettingsDefaultsUpdateAndPersistence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	s, _, path := newTestStore(t, now)
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, admin.ID, userSpec("policy-user", 100, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.GetDNSPolicySettings(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.StripECS || settings.BlockPrivateAnswers || settings.BlockedQTypes == nil || len(settings.BlockedQTypes) != 0 || !settings.CustomBlockEnabled || !settings.CustomAllowEnabled || !settings.CustomRewriteEnabled || settings.PolicyPausedUntil != nil {
		t.Fatalf("unexpected defaults: %+v", settings)
	}
	strip, block := true, true
	customBlock, customAllow, customRewrite := false, false, false
	pausedUntil := now.Add(time.Hour)
	qtypes := []string{"aaaa", " A ", "AAAA"}
	settings, err = s.UpdateDNSPolicySettings(ctx, user.ID, user.ID, DNSPolicySettingsPatch{
		StripECS: &strip, BlockPrivateAnswers: &block, BlockedQTypes: &qtypes,
		CustomBlockEnabled: &customBlock, CustomAllowEnabled: &customAllow, CustomRewriteEnabled: &customRewrite,
		PolicyPausedUntil: &pausedUntil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.StripECS || !settings.BlockPrivateAnswers || settings.CustomBlockEnabled || settings.CustomAllowEnabled || settings.CustomRewriteEnabled || settings.PolicyPausedUntil == nil || !settings.PolicyPausedUntil.Equal(pausedUntil) || !reflect.DeepEqual(settings.BlockedQTypes, []string{"AAAA", "A"}) {
		t.Fatalf("normalized settings: %+v", settings)
	}
	tooFar := now.Add(maxDNSPolicyPause + time.Second)
	if _, err := s.UpdateDNSPolicySettings(ctx, user.ID, user.ID, DNSPolicySettingsPatch{PolicyPausedUntil: &tooFar}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("excessive pause error=%v", err)
	}
	past := now.Add(-time.Second)
	settings, err = s.UpdateDNSPolicySettings(ctx, user.ID, user.ID, DNSPolicySettingsPatch{PolicyPausedUntil: &past})
	if err != nil || settings.PolicyPausedUntil != nil {
		t.Fatalf("cancel pause settings=%+v err=%v", settings, err)
	}
	invalid := []string{"NOT-A-QTYPE"}
	if _, err := s.UpdateDNSPolicySettings(ctx, user.ID, user.ID, DNSPolicySettingsPatch{BlockedQTypes: &invalid}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid qtype error=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err = reopened.GetDNSPolicySettings(ctx, user.ID)
	if err != nil || settings.CustomBlockEnabled || settings.CustomAllowEnabled || settings.CustomRewriteEnabled || settings.PolicyPausedUntil != nil || !reflect.DeepEqual(settings.BlockedQTypes, []string{"AAAA", "A"}) {
		t.Fatalf("reopened settings=%+v err=%v", settings, err)
	}
}

func TestDNSPolicySchemaV3UpgradeEnablesExistingRuleActions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control-v3.db")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		var settings DNSPolicySettings
		if err := decode(tx.Bucket(bDNSPolicySettings).Get([]byte(admin.ID)), &settings); err != nil {
			return err
		}
		settings.CustomBlockEnabled, settings.CustomAllowEnabled, settings.CustomRewriteEnabled = false, false, false
		if err := marshalPut(tx.Bucket(bDNSPolicySettings), []byte(admin.ID), settings); err != nil {
			return err
		}
		var version [8]byte
		binary.BigEndian.PutUint64(version[:], policySchemaVersion)
		return tx.Bucket(bMeta).Put(kSchema, version[:])
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err := reopened.GetDNSPolicySettings(ctx, admin.ID)
	if err != nil || !settings.CustomBlockEnabled || !settings.CustomAllowEnabled || !settings.CustomRewriteEnabled {
		t.Fatalf("upgraded settings=%+v err=%v", settings, err)
	}
}

func TestDNSPolicyRuleCRUDValidationPaginationAndAudit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	s, clock, _ := newTestStore(t, now)
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, admin.ID, userSpec("rules-user", 100, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser(ctx, admin.ID, userSpec("other-user", 100, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	rules := []DNSPolicyRuleSpec{
		{Enabled: true, Priority: 20, Action: DNSPolicyAllow, Match: DNSPolicyMatchExact, Pattern: "WWW.Example.COM"},
		{Enabled: true, Priority: 10, Action: DNSPolicyRewrite, Match: DNSPolicyMatchSuffix, Pattern: ".Example.NET", RecordType: DNSPolicyRewriteA, Value: "192.0.2.1"},
		{Enabled: false, Priority: 30, Action: DNSPolicyBlock, Match: DNSPolicyMatchKeyword, Pattern: " TRACKER "},
	}
	created := make([]DNSPolicyRule, 0, len(rules))
	for _, spec := range rules {
		rule, err := s.CreateDNSPolicyRule(ctx, user.ID, user.ID, spec)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, rule)
	}
	if created[0].Pattern != "www.example.com." || created[1].Pattern != "example.net." || created[2].Pattern != "tracker" {
		t.Fatalf("normalized rules: %+v", created)
	}
	if _, err := s.CreateDNSPolicyRule(ctx, user.ID, user.ID, DNSPolicyRuleSpec{Enabled: true, Action: DNSPolicyRewrite, Match: DNSPolicyMatchRegexp, Pattern: "[", RecordType: DNSPolicyRewriteAAAA, Value: "not-an-ip"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid regexp error=%v", err)
	}
	page1, err := s.ListDNSPolicyRules(ctx, user.ID, Page{Limit: 2})
	if err != nil || len(page1.Items) != 2 || page1.Items[0].Priority != 10 || page1.Items[1].Priority != 20 || page1.NextCursor == "" {
		t.Fatalf("page1=%+v err=%v", page1, err)
	}
	page2, err := s.ListDNSPolicyRules(ctx, user.ID, Page{Limit: 2, Cursor: page1.NextCursor})
	if err != nil || len(page2.Items) != 1 || page2.Items[0].Priority != 30 || page2.NextCursor != "" {
		t.Fatalf("page2=%+v err=%v", page2, err)
	}
	if _, err := s.GetDNSPolicyRule(ctx, other.ID, created[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user get error=%v", err)
	}
	clock.Set(now.Add(time.Minute))
	action := DNSPolicyRewrite
	recordType := DNSPolicyRewriteCNAME
	value := "Target.Example."
	priority := uint32(5)
	updated, err := s.UpdateDNSPolicyRule(ctx, admin.ID, user.ID, created[0].ID, DNSPolicyRulePatch{Action: &action, RecordType: &recordType, Value: &value, Priority: &priority})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RecordType != DNSPolicyRewriteCNAME || updated.Value != "target.example." || updated.Priority != 5 || !updated.UpdatedAt.Equal(clock.Now()) {
		t.Fatalf("updated=%+v", updated)
	}
	if err := s.DeleteDNSPolicyRule(ctx, user.ID, user.ID, created[2].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDNSPolicyRule(ctx, user.ID, created[2].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted rule error=%v", err)
	}
	audit, err := s.ListAudit(ctx, now.Add(-time.Hour), clock.Now().Add(time.Hour), Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"create_dns_policy_rule": 3, "update_dns_policy_rule": 1, "delete_dns_policy_rule": 1}
	for _, record := range audit.Items {
		if _, ok := want[record.Action]; ok {
			want[record.Action]--
		}
	}
	for action, remaining := range want {
		if remaining != 0 {
			t.Errorf("audit action %s remaining=%d", action, remaining)
		}
	}
}

func TestDNSPolicySchemaV2UpgradeBackfillsDefaults(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		for _, bucket := range [][]byte{bDNSPolicySettings, bDNSPolicyRules, bUserDNSPolicyRules} {
			if err := tx.DeleteBucket(bucket); err != nil {
				return err
			}
		}
		var version [8]byte
		binary.BigEndian.PutUint64(version[:], credentialSchemaVersion)
		return tx.Bucket(bMeta).Put(kSchema, version[:])
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err := reopened.GetDNSPolicySettings(ctx, admin.ID)
	if err != nil || settings.BlockedQTypes == nil || len(settings.BlockedQTypes) != 0 {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}

func TestDNSPolicyStrictDomainValidation(t *testing.T) {
	for _, value := range []string{"bad name.example", " bad.example", "bad.example ", "bad/name.example", "bad\x01name.example", "-bad.example", "bad-.example"} {
		if _, err := normalizePolicyDomain(value, false); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("value=%q error=%v", value, err)
		}
	}
	for _, value := range []string{"example.com", "_dns-sd._udp.example"} {
		if _, err := normalizePolicyDomain(value, false); err != nil {
			t.Errorf("value=%q error=%v", value, err)
		}
	}
}

func TestDNSPolicyRuleLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	s, _, _ := newTestStore(t, now)
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, admin.ID, userSpec("rule-limit-user", 100, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		for i := range maxDNSPolicyRulesPerUser {
			id := fmt.Sprintf("rule-%04d", i)
			rule := DNSPolicyRule{ID: id, UserID: user.ID, Enabled: true, Priority: uint32(i), Action: DNSPolicyBlock, Match: DNSPolicyMatchExact, Pattern: "example.com.", CreatedAt: now, UpdatedAt: now}
			if err := marshalPut(tx.Bucket(bDNSPolicyRules), []byte(id), rule); err != nil {
				return err
			}
			if err := tx.Bucket(bUserDNSPolicyRules).Put(dnsPolicyRuleIndexKey(user.ID, rule.Priority, id), []byte(id)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateDNSPolicyRule(ctx, user.ID, user.ID, DNSPolicyRuleSpec{Enabled: true, Priority: 1001, Action: DNSPolicyBlock, Match: DNSPolicyMatchExact, Pattern: "overflow.example"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("limit error=%v", err)
	}
}
