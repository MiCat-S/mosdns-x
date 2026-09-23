package dnspolicy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
)

func ednsQuestion(name string, qtype uint16, clientSubnet string) *dns.Msg {
	m := question(name, qtype)
	m.SetEdns0(1232, false)
	if clientSubnet != "" {
		ip, network, _ := net.ParseCIDR(clientSubnet)
		ones, _ := network.Mask.Size()
		dnsutils.AddECS(m.IsEdns0(), dnsutils.NewEDNS0Subnet(ip, uint8(ones), ip.To4() == nil), true)
	}
	return m
}

func subnetOf(m *dns.Msg) string {
	ecs := dnsutils.GetMsgECS(m)
	if ecs == nil {
		return ""
	}
	return (&net.IPNet{IP: ecs.Address, Mask: net.CIDRMask(int(ecs.SourceNetmask), map[uint16]int{1: 32, 2: 128}[ecs.Family])}).String()
}

func ecsEngine(t *testing.T, v4, v6 string, strip bool) (*Engine, control.DNSPolicySettings) {
	t.Helper()
	patch := control.DNSPolicySettingsPatch{ECSIPv4: &v4, ECSIPv6: &v6, StripECS: &strip}
	return withSettings(t, patch)
}

func TestECSOverrideReplacesClientSubnetByFamily(t *testing.T) {
	engine, settings := ecsEngine(t, "203.0.113.0/24", "2001:db8:1::/48", false)
	ctx := context.Background()
	principal := principalFor(settings)

	req := ednsQuestion("cdn.test", dns.TypeA, "198.51.100.0/24")
	if _, _, err := engine.BeforeWithDecision(ctx, principal, req); err != nil {
		t.Fatal(err)
	}
	if got := subnetOf(req); got != "203.0.113.0/24" {
		t.Fatalf("A query sent %q, want the IPv4 override replacing the client's", got)
	}
	req = ednsQuestion("cdn.test", dns.TypeAAAA, "")
	engine.BeforeWithDecision(ctx, principal, req)
	if got := subnetOf(req); got != "2001:db8:1::/48" {
		t.Fatalf("AAAA query sent %q, want the IPv6 override", got)
	}
}

func TestECSOverrideFallsBackToTheOtherFamily(t *testing.T) {
	engine, settings := ecsEngine(t, "203.0.113.0/24", "", false)
	req := ednsQuestion("cdn.test", dns.TypeAAAA, "")
	engine.BeforeWithDecision(context.Background(), principalFor(settings), req)
	if got := subnetOf(req); got != "203.0.113.0/24" {
		t.Fatalf("AAAA with only an IPv4 override sent %q", got)
	}
}

func TestECSOverrideLeavesOtherTypesAlone(t *testing.T) {
	engine, settings := ecsEngine(t, "203.0.113.0/24", "", false)
	req := ednsQuestion("cdn.test", dns.TypeTXT, "198.51.100.0/24")
	engine.BeforeWithDecision(context.Background(), principalFor(settings), req)
	if got := subnetOf(req); got != "198.51.100.0/24" {
		t.Fatalf("TXT query sent %q, want the client's own subnet untouched", got)
	}
}

// A client that sent no OPT must not have one added: RFC 6891 forbids an OPT
// in the response to such a query.
func TestECSOverrideNeverAddsEDNSToAPlainQuery(t *testing.T) {
	engine, settings := ecsEngine(t, "203.0.113.0/24", "", false)
	req := question("cdn.test", dns.TypeA)
	engine.BeforeWithDecision(context.Background(), principalFor(settings), req)
	if req.IsEdns0() != nil {
		t.Fatal("an OPT was added to a query that had none")
	}
}

func TestStripECSTakesPrecedenceOverOverride(t *testing.T) {
	engine, settings := ecsEngine(t, "203.0.113.0/24", "", true)
	req := ednsQuestion("cdn.test", dns.TypeA, "198.51.100.0/24")
	engine.BeforeWithDecision(context.Background(), principalFor(settings), req)
	if got := subnetOf(req); got != "" {
		t.Fatalf("stripping was overridden: sent %q", got)
	}
}

// The upstream echoes the override, which no longer matches the client's
// query. RFC 7871 requires them to match, so the ECS option is dropped while
// the OPT the client sent stays.
func TestECSOverrideClearsEchoFromResponse(t *testing.T) {
	engine, settings := ecsEngine(t, "203.0.113.0/24", "", false)
	ctx := context.Background()
	principal := principalFor(settings)
	req := ednsQuestion("cdn.test", dns.TypeA, "198.51.100.0/24")
	engine.BeforeWithDecision(ctx, principal, req)

	upstream := answered(req, aTTL("cdn.test", "192.0.2.1", 60))
	upstream.SetEdns0(1232, false)
	dnsutils.AddECS(upstream.IsEdns0(), dnsutils.GetMsgECS(req), true)
	got, _, err := engine.AfterWithDecision(ctx, principal, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if subnetOf(got) != "" {
		t.Fatal("the response still carries the override subnet")
	}
	if got.IsEdns0() == nil {
		t.Fatal("the OPT the client sent was removed")
	}
}

func TestECSOverrideSkippedWhilePaused(t *testing.T) {
	v4 := "203.0.113.0/24"
	pause := time.Now().Add(time.Hour)
	engine, settings := withSettings(t, control.DNSPolicySettingsPatch{ECSIPv4: &v4, PolicyPausedUntil: &pause})
	req := ednsQuestion("cdn.test", dns.TypeA, "198.51.100.0/24")
	engine.BeforeWithDecision(context.Background(), principalFor(settings), req)
	if got := subnetOf(req); got != "198.51.100.0/24" {
		t.Fatalf("a paused policy overrode the subnet: %q", got)
	}
}

func TestECSPrefixValidation(t *testing.T) {
	_, store, principal := testEngine(t)
	ctx := context.Background()
	set := func(v4, v6 string) (control.DNSPolicySettings, error) {
		return store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID,
			control.DNSPolicySettingsPatch{ECSIPv4: &v4, ECSIPv6: &v6})
	}
	got, err := set(" 203.0.113.77/24 ", "2001:db8:1:2::9/48")
	if err != nil || got.ECSIPv4 != "203.0.113.0/24" || got.ECSIPv6 != "2001:db8:1::/48" {
		t.Fatalf("host bits not masked: %+v err=%v", got, err)
	}
	for _, bad := range [][2]string{
		{"2001:db8::/48", ""},          // IPv6 in the IPv4 field
		{"", "203.0.113.0/24"},         // IPv4 in the IPv6 field
		{"203.0.113.1", ""},            // bare address, not a prefix
		{"203.0.113.0/33", ""},         // impossible length
		{"::ffff:203.0.113.0/120", ""}, // IPv4-mapped is not IPv4
	} {
		if _, err := set(bad[0], bad[1]); !errors.Is(err, control.ErrInvalidInput) {
			t.Fatalf("accepted %q / %q: err=%v", bad[0], bad[1], err)
		}
	}
	got, err = set("", "")
	if err != nil || got.ECSIPv4 != "" || got.ECSIPv6 != "" {
		t.Fatalf("empty did not clear: %+v err=%v", got, err)
	}
}
