package dnspolicy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func withFamily(t *testing.T, family control.AnswerFamily) (*Engine, query_context.Principal) {
	t.Helper()
	engine, store, principal := testEngine(t)
	if _, err := store.UpdateDNSPolicySettings(context.Background(), principal.UserID, principal.UserID,
		control.DNSPolicySettingsPatch{AnswerFamily: &family}); err != nil {
		t.Fatal(err)
	}
	engine.Invalidate(principal.UserID)
	return engine, principal
}

func answered(req *dns.Msg, rrs ...dns.RR) *dns.Msg {
	m := new(dns.Msg).SetReply(req)
	m.Answer = rrs
	return m
}

func aRR(name string) dns.RR {
	return &dns.A{Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("93.184.216.34")}
}

func aaaaRR(name string) dns.RR {
	return &dns.AAAA{Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60}, AAAA: net.ParseIP("2606:2800:220:1::1")}
}

// refs records every reference lookup and answers from a fixed table.
type refs struct {
	calls  []uint16
	answer map[uint16][]dns.RR
	err    error
}

func (r *refs) ctx() context.Context {
	return query_context.WithReferenceResolver(context.Background(), func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
		r.calls = append(r.calls, q.Question[0].Qtype)
		if r.err != nil {
			return nil, r.err
		}
		return answered(q, r.answer[q.Question[0].Qtype]...), nil
	})
}

func TestPreferIPv4SuppressesAAAAWhenAExists(t *testing.T) {
	engine, principal := withFamily(t, control.AnswerFamilyIPv4)
	r := &refs{answer: map[uint16][]dns.RR{dns.TypeA: {aRR("dual.test")}}}
	req := question("dual.test", dns.TypeAAAA)
	got, decision, err := engine.AfterWithDecision(r.ctx(), principal, req, answered(req, aaaaRR("dual.test")))
	if err != nil || !isNoData(got) || !decision.FamilyPreference {
		t.Fatalf("response=%v decision=%+v err=%v", got, decision, err)
	}
	if len(r.calls) != 1 || r.calls[0] != dns.TypeA {
		t.Fatalf("reference lookups = %v, want one A", r.calls)
	}
}

// An IPv6-only name must stay reachable: the preference is not a block.
func TestPreferIPv4KeepsAAAAForIPv6OnlyName(t *testing.T) {
	engine, principal := withFamily(t, control.AnswerFamilyIPv4)
	r := &refs{answer: map[uint16][]dns.RR{}}
	req := question("v6only.test", dns.TypeAAAA)
	original := answered(req, aaaaRR("v6only.test"))
	got, decision, err := engine.AfterWithDecision(r.ctx(), principal, req, original)
	if err != nil || got != original || decision.FamilyPreference {
		t.Fatalf("an IPv6-only name was suppressed: response=%v decision=%+v", got, decision)
	}
}

func TestPreferIPv6SuppressesAWhenAAAAExists(t *testing.T) {
	engine, principal := withFamily(t, control.AnswerFamilyIPv6)
	r := &refs{answer: map[uint16][]dns.RR{dns.TypeAAAA: {aaaaRR("dual.test")}}}
	req := question("dual.test", dns.TypeA)
	got, decision, err := engine.AfterWithDecision(r.ctx(), principal, req, answered(req, aRR("dual.test")))
	if err != nil || !isNoData(got) || !decision.FamilyPreference {
		t.Fatalf("response=%v decision=%+v err=%v", got, decision, err)
	}
}

// Queries the preference does not concern must not cost a reference lookup.
func TestFamilyPreferenceSkipsLookupsItDoesNotNeed(t *testing.T) {
	engine, principal := withFamily(t, control.AnswerFamilyIPv4)
	r := &refs{answer: map[uint16][]dns.RR{dns.TypeA: {aRR("x.test")}}}
	cases := []struct {
		name string
		req  *dns.Msg
		resp func(*dns.Msg) *dns.Msg
	}{
		{"preferred family itself", question("x.test", dns.TypeA), func(q *dns.Msg) *dns.Msg { return answered(q, aRR("x.test")) }},
		{"unrelated type", question("x.test", dns.TypeTXT), func(q *dns.Msg) *dns.Msg { return answered(q) }},
		{"empty AAAA answer", question("x.test", dns.TypeAAAA), func(q *dns.Msg) *dns.Msg { return answered(q) }},
		{"AAAA NXDOMAIN", question("x.test", dns.TypeAAAA), func(q *dns.Msg) *dns.Msg {
			m := answered(q)
			m.Rcode = dns.RcodeNameError
			return m
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r.calls = nil
			original := c.resp(c.req)
			got, _, err := engine.AfterWithDecision(r.ctx(), principal, c.req, original)
			if err != nil || got != original {
				t.Fatalf("response changed: %v err=%v", got, err)
			}
			if len(r.calls) != 0 {
				t.Fatalf("made %d reference lookups, want none", len(r.calls))
			}
		})
	}
}

// The preference fails open: a lookup it cannot complete leaves the answer.
func TestFamilyPreferenceFailsOpen(t *testing.T) {
	engine, principal := withFamily(t, control.AnswerFamilyIPv4)
	req := question("dual.test", dns.TypeAAAA)

	original := answered(req, aaaaRR("dual.test"))
	if got, _, err := engine.AfterWithDecision(context.Background(), principal, req, original); err != nil || got != original {
		t.Fatalf("no resolver: response=%v err=%v", got, err)
	}
	r := &refs{err: errors.New("upstream timeout")}
	if got, _, err := engine.AfterWithDecision(r.ctx(), principal, req, original); err != nil || got != original {
		t.Fatalf("lookup error: response=%v err=%v", got, err)
	}
}

func TestNoFamilyPreferenceMakesNoLookups(t *testing.T) {
	engine, principal := withFamily(t, control.AnswerFamilyAny)
	r := &refs{answer: map[uint16][]dns.RR{dns.TypeA: {aRR("dual.test")}}}
	req := question("dual.test", dns.TypeAAAA)
	original := answered(req, aaaaRR("dual.test"))
	if got, _, _ := engine.AfterWithDecision(r.ctx(), principal, req, original); got != original || len(r.calls) != 0 {
		t.Fatalf("response=%v lookups=%v", got, r.calls)
	}
}

func TestFamilyPreferenceRejectsUnknownValue(t *testing.T) {
	_, store, principal := testEngine(t)
	bad := control.AnswerFamily("ipv5")
	_, err := store.UpdateDNSPolicySettings(context.Background(), principal.UserID, principal.UserID,
		control.DNSPolicySettingsPatch{AnswerFamily: &bad})
	if !errors.Is(err, control.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestQueryLogForReportsTheUsersChoice(t *testing.T) {
	disabled := true
	hours := uint32(168)
	engine, settings := withSettings(t, control.DNSPolicySettingsPatch{QueryLogDisabled: &disabled, QueryRetentionHours: &hours})
	enabled, retention, err := engine.QueryLogFor(context.Background(), settings.UserID)
	if err != nil || enabled || retention != 168*time.Hour {
		t.Fatalf("enabled=%v retention=%s err=%v", enabled, retention, err)
	}
}

// A deleted user must report the defaults, not an error, or telemetry would
// hold their records back as unreadable and never age them out.
func TestQueryLogForUnknownUserReportsDefaults(t *testing.T) {
	engine, _, _ := testEngine(t)
	enabled, retention, err := engine.QueryLogFor(context.Background(), "no-such-user")
	if err != nil || !enabled || retention != 0 {
		t.Fatalf("enabled=%v retention=%s err=%v", enabled, retention, err)
	}
}
