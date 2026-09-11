package dnspolicy

import (
	"context"
	"net"
	"path/filepath"
	"testing"

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
	_, err = store.CreateDNSPolicyRule(ctx, principal.UserID, principal.UserID, control.DNSPolicyRuleSpec{
		Enabled: true, Priority: 20, Action: control.DNSPolicyBlock, Match: control.DNSPolicyMatchSuffix, Pattern: "example.org",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateDNSPolicyRule(ctx, principal.UserID, principal.UserID, control.DNSPolicyRuleSpec{
		Enabled: true, Priority: 10, Action: control.DNSPolicyAllow, Match: control.DNSPolicyMatchExact, Pattern: "safe.example.org",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := question("safe.example.org", dns.TypeA)
	req.SetEdns0(1232, false)
	req.IsEdns0().Option = append(req.IsEdns0().Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP("192.0.2.1")})
	response, err := engine.Before(ctx, principal, req)
	if err != nil || response != nil || len(req.IsEdns0().Option) != 0 {
		t.Fatalf("allowed response=%v options=%v err=%v", response, req.IsEdns0().Option, err)
	}

	response, err = engine.Before(ctx, principal, question("blocked.example.org", dns.TypeA))
	if err != nil || response == nil || response.Rcode != dns.RcodeNameError {
		t.Fatalf("blocked response=%v err=%v", response, err)
	}
	response, err = engine.Before(ctx, principal, question("example.org", dns.TypeHTTPS))
	if err != nil || response == nil || response.Rcode != dns.RcodeNameError {
		t.Fatalf("qtype response=%v err=%v", response, err)
	}
}

func TestBeforeRewritesSupportedQuestion(t *testing.T) {
	engine, store, principal := testEngine(t)
	_, err := store.CreateDNSPolicyRule(context.Background(), principal.UserID, principal.UserID, control.DNSPolicyRuleSpec{
		Enabled: true, Priority: 1, Action: control.DNSPolicyRewrite, Match: control.DNSPolicyMatchExact,
		Pattern: "example.org", RecordType: control.DNSPolicyRewriteA, Value: "192.0.2.8",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := engine.Before(context.Background(), principal, question("example.org", dns.TypeA))
	if err != nil || response == nil || len(response.Answer) != 1 || response.Answer[0].(*dns.A).A.String() != "192.0.2.8" {
		t.Fatalf("response=%v err=%v", response, err)
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
	filtered, err := engine.After(context.Background(), principal, req, response)
	if err != nil || filtered.Rcode != dns.RcodeNameError || len(filtered.Answer) != 0 {
		t.Fatalf("filtered=%v err=%v", filtered, err)
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

func boolPtr(value bool) *bool { return &value }
