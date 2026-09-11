package dns_handler

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/query_access"
)

type testExecutable struct {
	calls    int
	err      error
	nilR     bool
	cacheHit bool
}

type snapshotExecutable struct{}

func (*snapshotExecutable) Exec(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
	qCtx.Q().Extra = nil
	r := new(dns.Msg)
	r.SetReply(qCtx.Q())
	r.Answer = []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: "example.org.", Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("192.0.2.1")},
		&dns.AAAA{Hdr: dns.RR_Header{Name: "example.org.", Rrtype: dns.TypeAAAA, Class: dns.ClassINET}, AAAA: net.ParseIP("2001:db8::1")},
		&dns.A{Hdr: dns.RR_Header{Name: "example.org.", Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("192.0.2.1")},
	}
	qCtx.SetResponse(r)
	return nil
}

func (e *testExecutable) Exec(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
	e.calls++
	if e.err != nil {
		return e.err
	}
	if !e.nilR {
		r := new(dns.Msg)
		r.SetReply(qCtx.Q())
		qCtx.SetResponse(r)
		qCtx.SetCacheHit(e.cacheHit)
	}
	return nil
}

func TestResultIncludesFinalCacheHit(t *testing.T) {
	var got Result
	h, _ := NewEntryHandler(EntryHandlerOpts{Entry: &testExecutable{cacheHit: true}, Observe: func(result Result) { got = result }})
	if _, err := h.ServeDNS(context.Background(), validQuery(), nil); err != nil {
		t.Fatal(err)
	}
	if !got.CacheHit {
		t.Fatal("cache hit missing from final result")
	}
}

func TestResultSnapshotsOriginalEDNSAndFinalAnswerIPs(t *testing.T) {
	q := validQuery()
	q.SetEdns0(1232, true)
	opt := q.IsEdns0()
	opt.Option = append(opt.Option,
		&dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, SourceScope: 0, Address: net.ParseIP("192.0.2.129")},
		&dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "0011223344556677"},
	)
	var got Result
	h, _ := NewEntryHandler(EntryHandlerOpts{Entry: new(snapshotExecutable), CaptureQueryDetails: true, Observe: func(result Result) { got = result }})
	if _, err := h.ServeDNS(context.Background(), q, query_context.NewRequestMeta(netip.MustParseAddr("192.0.2.44"))); err != nil {
		t.Fatal(err)
	}
	if diff := len(got.AnswerIPs); diff != 2 || got.AnswerIPs[0] != "192.0.2.1" || got.AnswerIPs[1] != "2001:db8::1" {
		t.Fatalf("answer IPs=%v", got.AnswerIPs)
	}
	if !got.EDNS.Present || got.EDNS.Version != 0 || got.EDNS.UDPSize != 1232 || !got.EDNS.DNSSECOK {
		t.Fatalf("EDNS=%+v", got.EDNS)
	}
	if len(got.EDNS.OptionCodes) != 2 || got.EDNS.OptionCodes[0] != dns.EDNS0SUBNET || got.EDNS.OptionCodes[1] != dns.EDNS0COOKIE {
		t.Fatalf("option codes=%v", got.EDNS.OptionCodes)
	}
	if got.EDNS.ECS == nil || got.EDNS.ECS.Address != "192.0.2.0" || got.EDNS.ECS.Family != 1 || got.EDNS.ECS.SourcePrefix != 24 || got.EDNS.ECS.ScopePrefix != 0 {
		t.Fatalf("ECS=%+v", got.EDNS.ECS)
	}
}

func TestQueryDetailsAreNotCapturedWhenDisabled(t *testing.T) {
	q := validQuery()
	q.SetEdns0(1232, true)
	var got Result
	h, _ := NewEntryHandler(EntryHandlerOpts{Entry: new(snapshotExecutable), Observe: func(result Result) { got = result }})
	if _, err := h.ServeDNS(context.Background(), q, nil); err != nil {
		t.Fatal(err)
	}
	if got.AnswerIPs != nil || got.EDNS.Present || got.EDNS.OptionCodes != nil {
		t.Fatalf("query details captured while disabled: %+v", got)
	}
}

func TestSnapshotEDNSMalformedECS(t *testing.T) {
	for _, ecs := range []*dns.EDNS0_SUBNET{
		{Code: dns.EDNS0SUBNET, Family: 999, SourceNetmask: 24, Address: net.ParseIP("192.0.2.1")},
		{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 64, Address: net.ParseIP("192.0.2.1")},
		{Code: dns.EDNS0SUBNET, Family: 2, SourceNetmask: 64, Address: net.ParseIP("192.0.2.1")},
	} {
		q := validQuery()
		q.SetEdns0(512, false)
		q.IsEdns0().Option = append(q.IsEdns0().Option, ecs)
		got := snapshotEDNS(q)
		if got.ECS == nil || got.ECS.Address != "" {
			t.Fatalf("malformed ECS %#v produced %+v", ecs, got.ECS)
		}
	}
}

func validQuery() *dns.Msg {
	return new(dns.Msg).SetQuestion("example.org.", dns.TypeAAAA)
}

func TestAdmitAndObserve(t *testing.T) {
	principal := query_context.Principal{UserID: "u1", CredentialID: "c1", CredentialVersion: 7}
	meta := query_context.NewRequestMeta(netip.MustParseAddr("2001:db8::1"))
	meta.SetProtocol(query_context.ProtocolH3)
	meta.SetPrincipal(principal)
	exec := new(testExecutable)
	var results []Result
	h, err := NewEntryHandler(EntryHandlerOpts{
		Entry: exec,
		Admit: func(_ context.Context, got query_context.Principal) error {
			if got != principal {
				t.Fatalf("principal = %#v", got)
			}
			return nil
		},
		Observe: func(result Result) { results = append(results, result) },
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.ServeDNS(context.Background(), validQuery(), meta)
	if err != nil || r.Rcode != dns.RcodeSuccess {
		t.Fatalf("ServeDNS() = rcode %v, %v", r.Rcode, err)
	}
	if exec.calls != 1 || len(results) != 1 {
		t.Fatalf("exec=%d observe=%d", exec.calls, len(results))
	}
	got := results[0]
	if !got.Admitted || got.Rejected || got.Principal != principal || got.Protocol != query_context.ProtocolH3 || got.ClientAddr.String() != "2001:db8::1" || got.QuestionName != "example.org." || got.QuestionType != dns.TypeAAAA || got.Rcode != dns.RcodeSuccess {
		t.Fatalf("unexpected result: %#v", got)
	}
	meta.SetPrincipal(query_context.Principal{UserID: "changed"})
	if got.Principal != principal {
		t.Fatal("observed result shares mutable request metadata")
	}
}

func TestInvalidDNSDoesNotAdmit(t *testing.T) {
	exec := new(testExecutable)
	admitCalls := 0
	var got Result
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry:   exec,
		Admit:   func(context.Context, query_context.Principal) error { admitCalls++; return nil },
		Observe: func(result Result) { got = result },
	})
	r, err := h.ServeDNS(context.Background(), new(dns.Msg), nil)
	if err != nil || r.Rcode != dns.RcodeFormatError || admitCalls != 0 || exec.calls != 0 {
		t.Fatalf("rcode=%d err=%v admit=%d exec=%d", r.Rcode, err, admitCalls, exec.calls)
	}
	if got.Admitted || got.Rcode != dns.RcodeFormatError {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestAdmitRejectionSkipsExec(t *testing.T) {
	exec := new(testExecutable)
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: exec,
		Admit: func(context.Context, query_context.Principal) error {
			return query_access.New(query_access.QuotaExceeded)
		},
		Observe: func(result Result) {
			if !result.Rejected || result.Admitted || result.Rcode != -1 || result.AccessKind != query_access.QuotaExceeded {
				t.Fatalf("unexpected result: %#v", result)
			}
		},
	})
	r, err := h.ServeDNS(context.Background(), validQuery(), nil)
	if r != nil || query_access.HTTPStatus(err) != 429 || exec.calls != 0 {
		t.Fatalf("response=%v err=%v exec=%d", r, err, exec.calls)
	}
}

func TestExecFailureAndNilResponseBecomeSERVFAIL(t *testing.T) {
	for _, tc := range []struct {
		name string
		exec *testExecutable
	}{
		{"error", &testExecutable{err: errors.New("boom")}},
		{"nil response", &testExecutable{nilR: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Result
			h, _ := NewEntryHandler(EntryHandlerOpts{Entry: tc.exec, Observe: func(result Result) { got = result }})
			r, err := h.ServeDNS(context.Background(), validQuery(), nil)
			if err != nil || r.Rcode != dns.RcodeServerFailure || got.Rcode != dns.RcodeServerFailure {
				t.Fatalf("response=%v err=%v result=%#v", r, err, got)
			}
			if got.ExecError != (tc.exec.err != nil) {
				t.Fatalf("ExecError=%v", got.ExecError)
			}
		})
	}
}
