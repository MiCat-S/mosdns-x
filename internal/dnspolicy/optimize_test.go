package dnspolicy

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func principalFor(settings control.DNSPolicySettings) query_context.Principal {
	return query_context.Principal{UserID: settings.UserID}
}

func cname(name, target string) dns.RR {
	return &dns.CNAME{Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 300}, Target: dns.Fqdn(target)}
}

func aTTL(name, ip string, ttl uint32) dns.RR {
	return &dns.A{Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl}, A: net.ParseIP(ip)}
}

func withSettings(t *testing.T, patch control.DNSPolicySettingsPatch) (*Engine, control.DNSPolicySettings) {
	t.Helper()
	engine, store, principal := testEngine(t)
	settings, err := store.UpdateDNSPolicySettings(context.Background(), principal.UserID, principal.UserID, patch)
	if err != nil {
		t.Fatal(err)
	}
	engine.Invalidate(principal.UserID)
	return engine, settings
}

func TestFlattenCNAMEKeepsAddressesUnderQueriedName(t *testing.T) {
	req := question("www.example.test", dns.TypeA)
	msg := answered(req, cname("www.example.test", "edge.cdn.test"), aTTL("edge.cdn.test", "192.0.2.1", 60))
	flattenCNAME(msg, req.Question[0].Name)
	if len(msg.Answer) != 1 || msg.Answer[0].Header().Rrtype != dns.TypeA || msg.Answer[0].Header().Name != "www.example.test." {
		t.Fatalf("answer = %v", msg.Answer)
	}
}

func TestFlattenCNAMELeavesChainWithoutAddresses(t *testing.T) {
	req := question("alias.test", dns.TypeA)
	msg := answered(req, cname("alias.test", "nowhere.test"))
	flattenCNAME(msg, req.Question[0].Name)
	if len(msg.Answer) != 1 || msg.Answer[0].Header().Rrtype != dns.TypeCNAME {
		t.Fatalf("a chain with no address was flattened to nothing: %v", msg.Answer)
	}
}

func TestFlattenCNAMELeavesDNAMEAnswers(t *testing.T) {
	req := question("host.old.test", dns.TypeA)
	dname := &dns.DNAME{Hdr: dns.RR_Header{Name: "old.test.", Rrtype: dns.TypeDNAME, Class: dns.ClassINET, Ttl: 300}, Target: "new.test."}
	msg := answered(req, dname, cname("host.old.test", "host.new.test"), aTTL("host.new.test", "192.0.2.1", 60))
	flattenCNAME(msg, req.Question[0].Name)
	if len(msg.Answer) != 3 {
		t.Fatalf("a DNAME answer was rewritten: %v", msg.Answer)
	}
}

func TestShuffleKeepsCNAMEInFrontAndPreservesRecords(t *testing.T) {
	req := question("www.example.test", dns.TypeA)
	ips := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5"}
	seen := map[string]bool{}
	for range 64 {
		msg := answered(req, cname("www.example.test", "edge.test"))
		for _, ip := range ips {
			msg.Answer = append(msg.Answer, aTTL("edge.test", ip, 60))
		}
		shuffleAddresses(msg)
		if msg.Answer[0].Header().Rrtype != dns.TypeCNAME {
			t.Fatal("the CNAME moved out of first position")
		}
		got := map[string]bool{}
		for _, rr := range msg.Answer[1:] {
			got[rr.(*dns.A).A.String()] = true
		}
		if len(got) != len(ips) {
			t.Fatalf("records lost or duplicated: %v", msg.Answer)
		}
		seen[msg.Answer[1].(*dns.A).A.String()] = true
	}
	if len(seen) < 2 {
		t.Fatal("the first address never changed across 64 shuffles")
	}
}

func TestClampTTLBoundsAnswersButNotAuthority(t *testing.T) {
	req := question("example.test", dns.TypeA)
	msg := answered(req, aTTL("example.test", "192.0.2.1", 5), aTTL("example.test", "192.0.2.2", 90000))
	soa := &dns.SOA{Hdr: dns.RR_Header{Name: "example.test.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 5}, Minttl: 5}
	msg.Ns = []dns.RR{soa}
	clampTTL(msg, 60, 3600)
	if msg.Answer[0].Header().Ttl != 60 || msg.Answer[1].Header().Ttl != 3600 {
		t.Fatalf("ttls = %d, %d", msg.Answer[0].Header().Ttl, msg.Answer[1].Header().Ttl)
	}
	if msg.Ns[0].Header().Ttl != 5 {
		t.Fatal("the authority SOA was clamped, changing negative caching")
	}
}

// The engine must return the same response object. The entry handler treats a
// different object as a local replacement and would log the upstream result
// as discarded.
func TestOptimizationsEditInPlaceSoAttributionHolds(t *testing.T) {
	yes := true
	floor := uint32(60)
	engine, settings := withSettings(t, control.DNSPolicySettingsPatch{FlattenCNAME: &yes, ShuffleAnswers: &yes, TTLMin: &floor})
	req := question("www.example.test", dns.TypeA)
	original := answered(req, cname("www.example.test", "edge.test"), aTTL("edge.test", "192.0.2.1", 5))
	got, decision, err := engine.AfterWithDecision(context.Background(), principalFor(settings), req, original)
	if err != nil || got != original || decision != (Decision{}) {
		t.Fatalf("response replaced: same=%v decision=%+v err=%v", got == original, decision, err)
	}
	if len(got.Answer) != 1 || got.Answer[0].Header().Ttl != 60 || got.Answer[0].Header().Name != "www.example.test." {
		t.Fatalf("optimizations not applied: %v", got.Answer)
	}
}

func TestOptimizationsSkippedWhenPolicyPaused(t *testing.T) {
	yes := true
	engine, store, principal := testEngine(t)
	pause := time.Now().Add(time.Hour)
	if _, err := store.UpdateDNSPolicySettings(context.Background(), principal.UserID, principal.UserID,
		control.DNSPolicySettingsPatch{FlattenCNAME: &yes, PolicyPausedUntil: &pause}); err != nil {
		t.Fatal(err)
	}
	engine.Invalidate(principal.UserID)
	req := question("www.example.test", dns.TypeA)
	original := answered(req, cname("www.example.test", "edge.test"), aTTL("edge.test", "192.0.2.1", 60))
	got, _, _ := engine.AfterWithDecision(context.Background(), principal, req, original)
	if len(got.Answer) != 2 {
		t.Fatal("a paused policy still flattened the answer")
	}
}

func TestTTLBoundsValidated(t *testing.T) {
	_, store, principal := testEngine(t)
	ctx := context.Background()
	tooHigh := uint32(control.MaxPolicyTTL + 1)
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{TTLMin: &tooHigh}); err == nil {
		t.Fatal("a ttl above the maximum was accepted")
	}
	low, high := uint32(600), uint32(60)
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{TTLMax: &high}); err != nil {
		t.Fatal(err)
	}
	// Only the floor is sent; it must still be checked against the stored ceiling.
	if _, err := store.UpdateDNSPolicySettings(ctx, principal.UserID, principal.UserID, control.DNSPolicySettingsPatch{TTLMin: &low}); err == nil {
		t.Fatal("a floor above the stored ceiling was accepted")
	}
}
