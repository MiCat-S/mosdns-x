package dnspolicy

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func testEngine(t *testing.T) (*Engine, *control.Store, query_context.Principal) {
	t.Helper()
	store, err := control.Open(filepath.Join(t.TempDir(), "control.db"), control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	user, err := store.InitializeAdmin(context.Background(), control.UserSpec{
		Username: "admin", Password: "password-for-admin", Role: control.RoleAdmin, Enabled: true,
		Period: control.PeriodDaily, Timezone: "UTC", Limit: 1000, QPS: 100, Burst: 10, MaxCredentials: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(store), store, query_context.Principal{UserID: user.ID}
}

func question(name string, qtype uint16) *dns.Msg {
	return new(dns.Msg).SetQuestion(dns.Fqdn(name), qtype)
}

func TestBeforeAppliesSettingsAndFirstMatchingRule(t *testing.T) {
	engine, store, principal := testEngine(t)
	ctx := context.Background()
	settings, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{
		StripECS:      boolPtr(true),
		BlockedQTypes: &[]string{"HTTPS"},
	})
	if err != nil || !settings.StripECS {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
	blockRule, err := store.CreateDNSPolicyRule(ctx, principal.UserID, principal.UserID, control.DNSPolicyRuleSpec{
		Enabled: true, Priority: 20, Action: control.DNSPolicyBlock, Match: control.DNSPolicyMatchSuffix, Pattern: "example.org",
	})
	if err != nil {
		t.Fatal(err)
	}
	allowRule, err := store.CreateDNSPolicyRule(ctx, principal.UserID, principal.UserID, control.DNSPolicyRuleSpec{
		Enabled: true, Priority: 10, Action: control.DNSPolicyAllow, Match: control.DNSPolicyMatchExact, Pattern: "safe.example.org",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := question("safe.example.org", dns.TypeA)
	req.SetEdns0(1232, false)
	req.IsEdns0().Option = append(req.IsEdns0().Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP("192.0.2.1")})
	response, decision, err := engine.BeforeWithDecision(ctx, principal, req)
	if err != nil || response != nil || len(req.IsEdns0().Option) != 0 || decision.Action != control.DNSPolicyAllow || decision.RuleID != allowRule.ID {
		t.Fatalf("allowed response=%v decision=%+v options=%v err=%v", response, decision, req.IsEdns0().Option, err)
	}

	response, decision, err = engine.BeforeWithDecision(ctx, principal, question("blocked.example.org", dns.TypeA))
	if err != nil || response == nil || response.Rcode != dns.RcodeNameError || decision.Action != control.DNSPolicyBlock || decision.RuleID != blockRule.ID {
		t.Fatalf("blocked response=%v decision=%+v err=%v", response, decision, err)
	}
	response, decision, err = engine.BeforeWithDecision(ctx, principal, question("example.org", dns.TypeHTTPS))
	if err != nil || response == nil || response.Rcode != dns.RcodeNameError || decision.Action != control.DNSPolicyBlock || decision.RuleID != "" {
		t.Fatalf("qtype response=%v decision=%+v err=%v", response, decision, err)
	}
}

func TestBeforeRewritesSupportedQuestion(t *testing.T) {
	engine, store, principal := testEngine(t)
	rule, err := store.CreateDNSPolicyRule(context.Background(), principal.UserID, principal.UserID, control.DNSPolicyRuleSpec{
		Enabled: true, Priority: 1, Action: control.DNSPolicyRewrite, Match: control.DNSPolicyMatchExact,
		Pattern: "example.org", RecordType: control.DNSPolicyRewriteA, Value: "192.0.2.8",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, decision, err := engine.BeforeWithDecision(context.Background(), principal, question("example.org", dns.TypeA))
	if err != nil || response == nil || len(response.Answer) != 1 || response.Answer[0].(*dns.A).A.String() != "192.0.2.8" || decision.Action != control.DNSPolicyRewrite || decision.RuleID != rule.ID {
		t.Fatalf("response=%v decision=%+v err=%v", response, decision, err)
	}
}

func TestAfterBlocksPrivateAnswers(t *testing.T) {
	engine, store, principal := testEngine(t)
	_, err := store.UpdateDNSPolicySettings(context.Background(), principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{BlockPrivateAnswers: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	req := question("example.org", dns.TypeA)
	response := new(dns.Msg)
	response.SetReply(req)
	response.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("192.168.1.1")}}
	filtered, decision, err := engine.AfterWithDecision(context.Background(), principal, req, response)
	if err != nil || filtered.Rcode != dns.RcodeNameError || len(filtered.Answer) != 0 || decision.Action != control.DNSPolicyBlock || decision.RuleID != "" || decision.PublicListID != "" {
		t.Fatalf("filtered=%v decision=%+v err=%v", filtered, decision, err)
	}
}

func TestInvalidateAppliesSavedSettingsOnNextQuery(t *testing.T) {
	engine, store, principal := testEngine(t)
	ctx := context.Background()
	if response, err := engine.Before(ctx, principal, question("example.org", dns.TypeTXT)); err != nil || response != nil {
		t.Fatalf("initial response=%v err=%v", response, err)
	}
	qtypes := []string{"TXT"}
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{BlockedQTypes: &qtypes}); err != nil {
		t.Fatal(err)
	}
	engine.Invalidate(principal.UserID)
	response, err := engine.Before(ctx, principal, question("example.org", dns.TypeTXT))
	if err != nil || response == nil || response.Rcode != dns.RcodeNameError {
		t.Fatalf("updated response=%v err=%v", response, err)
	}
}

func TestPolicyPauseSkipsRequestAndResponsePolicyThenExpires(t *testing.T) {
	engine, store, principal := testEngine(t)
	ctx := context.Background()
	base := time.Now().UTC()
	engine.now = func() time.Time { return base }
	pausedUntil := base.Add(time.Hour)
	qtypes := []string{"A"}
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{
		StripECS: boolPtr(true), BlockPrivateAnswers: boolPtr(true), BlockedQTypes: &qtypes, PolicyPausedUntil: &pausedUntil,
	}); err != nil {
		t.Fatal(err)
	}
	req := question("example.org", dns.TypeA)
	req.SetEdns0(1232, false)
	req.IsEdns0().Option = append(req.IsEdns0().Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP("192.0.2.1")})
	if response, err := engine.Before(ctx, principal, req); err != nil || response != nil || len(req.IsEdns0().Option) != 1 {
		t.Fatalf("paused before response=%v options=%v err=%v", response, req.IsEdns0().Option, err)
	}
	upstream := new(dns.Msg)
	upstream.SetReply(req)
	upstream.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("192.168.1.1")}}
	if response, err := engine.After(ctx, principal, req, upstream); err != nil || response != upstream {
		t.Fatalf("paused after response=%v err=%v", response, err)
	}
	base = pausedUntil.Add(time.Nanosecond)
	response, err := engine.Before(ctx, principal, question("example.org", dns.TypeA))
	if err != nil || response == nil || response.Rcode != dns.RcodeNameError {
		t.Fatalf("expired pause response=%v err=%v", response, err)
	}
}

func TestRuleActionSwitches(t *testing.T) {
	engine, store, principal := testEngine(t)
	ctx := context.Background()
	for _, spec := range []control.DNSPolicyRuleSpec{
		{Enabled: true, Priority: 1, Action: control.DNSPolicyAllow, Match: control.DNSPolicyMatchExact, Pattern: "safe.block.example"},
		{Enabled: true, Priority: 2, Action: control.DNSPolicyBlock, Match: control.DNSPolicyMatchSuffix, Pattern: "block.example"},
		{Enabled: true, Priority: 3, Action: control.DNSPolicyRewrite, Match: control.DNSPolicyMatchExact, Pattern: "rewrite.example", RecordType: control.DNSPolicyRewriteA, Value: "192.0.2.7"},
	} {
		if _, err := store.CreateDNSPolicyRule(ctx, principal.UserID, principal.UserID, spec); err != nil {
			t.Fatal(err)
		}
	}
	disabled := false
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{CustomBlockEnabled: &disabled, CustomAllowEnabled: &disabled, CustomRewriteEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"safe.block.example", "bad.block.example", "rewrite.example"} {
		if response, err := engine.Before(ctx, principal, question(name, dns.TypeA)); err != nil || response != nil {
			t.Fatalf("disabled name=%s response=%v err=%v", name, response, err)
		}
	}
	enabled := true
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{CustomBlockEnabled: &enabled, CustomRewriteEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	engine.Invalidate(principal.UserID)
	if response, err := engine.Before(ctx, principal, question("bad.block.example", dns.TypeA)); err != nil || response == nil || response.Rcode != dns.RcodeNameError {
		t.Fatalf("block enabled response=%v err=%v", response, err)
	}
	if response, err := engine.Before(ctx, principal, question("rewrite.example", dns.TypeA)); err != nil || response == nil || len(response.Answer) != 1 {
		t.Fatalf("rewrite enabled response=%v err=%v", response, err)
	}
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{CustomAllowEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	engine.Invalidate(principal.UserID)
	if response, err := engine.Before(ctx, principal, question("safe.block.example", dns.TypeA)); err != nil || response != nil {
		t.Fatalf("allow enabled response=%v err=%v", response, err)
	}
}

type publicListMatcherStub struct {
	calls  int
	listID string
}

func (m *publicListMatcherStub) MatchID(context.Context, string, string) (string, error) {
	m.calls++
	return m.listID, nil
}

func TestPublicListsRunAfterCustomAllowAndRespectPause(t *testing.T) {
	_, store, principal := testEngine(t)
	matcher := &publicListMatcherStub{listID: "public-1"}
	engine := NewWithPublicLists(store, matcher)
	ctx := context.Background()
	_, err := store.CreateDNSPolicyRule(ctx, principal.UserID, principal.UserID, control.DNSPolicyRuleSpec{Enabled: true, Priority: 1, Action: control.DNSPolicyAllow, Match: control.DNSPolicyMatchExact, Pattern: "safe.example"})
	if err != nil {
		t.Fatal(err)
	}
	response, decision, err := engine.BeforeWithDecision(ctx, principal, question("safe.example", dns.TypeA))
	if err != nil || response != nil || matcher.calls != 0 || decision.Action != control.DNSPolicyAllow || decision.RuleID == "" || decision.PublicListID != "" {
		t.Fatalf("allow response=%v decision=%+v calls=%d err=%v", response, decision, matcher.calls, err)
	}
	response, decision, err = engine.BeforeWithDecision(ctx, principal, question("listed.example", dns.TypeA))
	if err != nil || response == nil || response.Rcode != dns.RcodeNameError || matcher.calls != 1 || decision.Action != control.DNSPolicyBlock || decision.RuleID != "" || decision.PublicListID != "public-1" {
		t.Fatalf("listed response=%v decision=%+v calls=%d err=%v", response, decision, matcher.calls, err)
	}
	pausedUntil := time.Now().Add(time.Hour)
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{PolicyPausedUntil: &pausedUntil}); err != nil {
		t.Fatal(err)
	}
	engine.Invalidate(principal.UserID)
	if response, err := engine.Before(ctx, principal, question("listed.example", dns.TypeA)); err != nil || response != nil || matcher.calls != 1 {
		t.Fatalf("paused response=%v calls=%d err=%v", response, matcher.calls, err)
	}
}

func boolPtr(value bool) *bool { return &value }
