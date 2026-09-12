package dns_handler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
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

type executableFunc func(context.Context, *query_context.Context, executable_seq.ExecutableChainNode) error

func (f executableFunc) Exec(ctx context.Context, qCtx *query_context.Context, next executable_seq.ExecutableChainNode) error {
	return f(ctx, qCtx, next)
}

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

func TestResultIncludesSelectedResponseTrace(t *testing.T) {
	var got Result
	exec := executableFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		r := new(dns.Msg)
		r.SetReply(qCtx.Q())
		qCtx.SetResponseWithTrace(r, query_context.ResponseTrace{Source: query_context.ResponseSourceUpstream, SourceID: "forward_remote", UpstreamID: "forward_remote/1"})
		return nil
	})
	h, _ := NewEntryHandler(EntryHandlerOpts{Entry: exec, Observe: func(result Result) { got = result }})
	if _, err := h.ServeDNS(context.Background(), validQuery(), nil); err != nil {
		t.Fatal(err)
	}
	if got.ResponseSource != query_context.ResponseSourceUpstream || got.ResponseSourceID != "forward_remote" || got.UpstreamID != "forward_remote/1" {
		t.Fatalf("response trace=%+v", got)
	}
}

func TestResultIncludesPolicyResponseTrace(t *testing.T) {
	var got Result
	exec := new(testExecutable)
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: exec,
		BeforeExecWithTrace: func(_ context.Context, _ query_context.Principal, request *dns.Msg) (*dns.Msg, query_context.ResponseTrace, error) {
			response := new(dns.Msg)
			response.SetRcode(request, dns.RcodeNameError)
			return response, query_context.ResponseTrace{Source: query_context.ResponseSourcePublicList, SourceID: "list-1", MatchedPublicListID: "list-1"}, nil
		},
		Observe: func(result Result) { got = result },
	})
	if _, err := h.ServeDNS(context.Background(), validQuery(), nil); err != nil {
		t.Fatal(err)
	}
	if exec.calls != 0 || got.ResponseSource != query_context.ResponseSourcePublicList || got.MatchedPublicListID != "list-1" {
		t.Fatalf("exec calls=%d trace=%+v", exec.calls, got)
	}
}

func TestResultPreservesAllowRuleAndSelectedUpstreamTrace(t *testing.T) {
	var got Result
	exec := executableFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		response := new(dns.Msg)
		response.SetReply(qCtx.Q())
		qCtx.SetResponseWithTrace(response, query_context.ResponseTrace{
			Source: query_context.ResponseSourceUpstream, SourceID: "forward", UpstreamID: "forward/1",
		})
		return nil
	})
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: exec,
		BeforeExecWithTrace: func(context.Context, query_context.Principal, *dns.Msg) (*dns.Msg, query_context.ResponseTrace, error) {
			return nil, query_context.ResponseTrace{MatchedRuleID: "allow-rule"}, nil
		},
		AfterExecWithTrace: func(_ context.Context, _ query_context.Principal, request, _ *dns.Msg) (*dns.Msg, query_context.ResponseTrace, error) {
			response := new(dns.Msg)
			response.SetRcode(request, dns.RcodeNameError)
			return response, query_context.ResponseTrace{Source: query_context.ResponseSourceCustomBlock}, nil
		},
		Observe: func(result Result) { got = result },
	})
	if _, err := h.ServeDNS(context.Background(), validQuery(), nil); err != nil {
		t.Fatal(err)
	}
	if got.ResponseSource != query_context.ResponseSourceCustomBlock || got.UpstreamID != "forward/1" || got.MatchedRuleID != "allow-rule" {
		t.Fatalf("response trace=%+v", got)
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
		{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, SourceScope: 64, Address: net.ParseIP("192.0.2.1")},
		{Code: dns.EDNS0SUBNET, Family: 2, SourceNetmask: 64, Address: net.ParseIP("192.0.2.1")},
	} {
		q := validQuery()
		q.SetEdns0(512, false)
		q.IsEdns0().Option = append(q.IsEdns0().Option, ecs)
		got := snapshotEDNS(q)
		if got.ECS != nil || len(got.Anomalies) == 0 {
			t.Fatalf("malformed ECS %#v produced %+v", ecs, got.ECS)
		}
	}
}

func TestResultIncludesFourStageEDNSSnapshots(t *testing.T) {
	request := validQuery()
	var got Result
	exec := executableFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		if !qCtx.CaptureQueryDetails() {
			t.Fatal("detailed capture choice not propagated")
		}
		upstreamRequest := qCtx.Q().Copy()
		upstreamRequest.SetEdns0(1232, false)
		requestSnapshot := dnsutils.SnapshotEDNS(upstreamRequest)
		upstreamResponse := new(dns.Msg)
		upstreamResponse.SetReply(upstreamRequest)
		upstreamResponse.SetEdns0(1232, false)
		responseSnapshot := dnsutils.SnapshotEDNS(upstreamResponse)
		finalResponse := upstreamResponse.Copy()
		dnsutils.RemoveEDNS0(finalResponse)
		qCtx.SetResponseWithTrace(finalResponse, query_context.ResponseTrace{
			Source: query_context.ResponseSourceUpstream, UpstreamID: "forward/0",
			UpstreamStageStatus: query_context.UpstreamStageSelected,
			UpstreamRequestEDNS: &requestSnapshot, UpstreamResponseEDNS: &responseSnapshot,
		})
		return nil
	})
	h, _ := NewEntryHandler(EntryHandlerOpts{Entry: exec, CaptureQueryDetails: true, Observe: func(result Result) { got = result }})
	if _, err := h.ServeDNS(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if got.EDNS.Present || got.UpstreamRequestEDNS == nil || !got.UpstreamRequestEDNS.Present ||
		got.UpstreamResponseEDNS == nil || !got.UpstreamResponseEDNS.Present ||
		got.ResponseEDNS == nil || got.ResponseEDNS.Present || got.UpstreamStageStatus != query_context.UpstreamStageSelected {
		t.Fatalf("four-stage result = %+v", got)
	}
}

func TestFinalLocalReplacementDiscardsSelectedUpstreamSnapshots(t *testing.T) {
	var got Result
	exec := executableFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		response := new(dns.Msg)
		response.SetReply(qCtx.Q())
		snapshot := dnsutils.SnapshotEDNS(qCtx.Q())
		qCtx.SetResponseWithTrace(response, query_context.ResponseTrace{
			Source: query_context.ResponseSourceUpstream, UpstreamID: "forward/0",
			UpstreamStageStatus: query_context.UpstreamStageSelected,
			UpstreamRequestEDNS: &snapshot, UpstreamResponseEDNS: &snapshot,
		})
		return nil
	})
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: exec, CaptureQueryDetails: true,
		AfterExecWithTrace: func(_ context.Context, _ query_context.Principal, request, _ *dns.Msg) (*dns.Msg, query_context.ResponseTrace, error) {
			response := new(dns.Msg)
			response.SetRcode(request, dns.RcodeNameError)
			return response, query_context.ResponseTrace{Source: query_context.ResponseSourceCustomBlock}, nil
		},
		Observe: func(result Result) { got = result },
	})
	if _, err := h.ServeDNS(context.Background(), validQuery(), nil); err != nil {
		t.Fatal(err)
	}
	if got.UpstreamStageStatus != query_context.UpstreamStageDiscarded || got.UpstreamID != "" || got.UpstreamRequestEDNS != nil || got.UpstreamResponseEDNS != nil {
		t.Fatalf("replacement inherited discarded upstream: %+v", got)
	}
}

func TestLocalSERVFAILKeepsNoSelectedUpstreamStatus(t *testing.T) {
	var got Result
	exec := executableFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		qCtx.SetResponseTrace(query_context.ResponseTrace{UpstreamStageStatus: query_context.UpstreamStageAttemptedNoSelection})
		return errors.New("all upstreams failed")
	})
	h, _ := NewEntryHandler(EntryHandlerOpts{Entry: exec, CaptureQueryDetails: true, Observe: func(result Result) { got = result }})
	response, err := h.ServeDNS(context.Background(), validQuery(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Rcode != dns.RcodeServerFailure || got.UpstreamStageStatus != query_context.UpstreamStageAttemptedNoSelection || got.UpstreamRequestEDNS != nil || got.UpstreamResponseEDNS != nil || got.ResponseEDNS == nil {
		t.Fatalf("SERVFAIL result = %+v", got)
	}
}

func TestLocalSERVFAILWithoutUpstreamTraceIsNotLinked(t *testing.T) {
	var got Result
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: &testExecutable{err: errors.New("execution failed")}, CaptureQueryDetails: true,
		Observe: func(result Result) { got = result },
	})
	response, err := h.ServeDNS(context.Background(), validQuery(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Rcode != dns.RcodeServerFailure || got.UpstreamStageStatus != query_context.UpstreamStageNotLinked || got.ResponseEDNS == nil {
		t.Fatalf("SERVFAIL result = %+v", got)
	}
}

func TestLocalSERVFAILDiscardsSelectedUpstreamWithUnavailableSnapshots(t *testing.T) {
	var got Result
	exec := executableFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		response := new(dns.Msg)
		response.SetReply(qCtx.Q())
		qCtx.SetResponseWithTrace(response, query_context.ResponseTrace{
			Source: query_context.ResponseSourceUpstream, UpstreamID: "legacy-upstream/0",
			UpstreamStageStatus: query_context.UpstreamStageUnavailable,
		})
		return errors.New("post-forward execution failed")
	})
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: exec, CaptureQueryDetails: true, Observe: func(result Result) { got = result },
	})
	response, err := h.ServeDNS(context.Background(), validQuery(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Rcode != dns.RcodeServerFailure || got.UpstreamStageStatus != query_context.UpstreamStageDiscarded || got.UpstreamID != "" || got.ResponseEDNS == nil {
		t.Fatalf("SERVFAIL result = %+v", got)
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

func TestPolicyRunsAfterAdmissionAroundExecutable(t *testing.T) {
	principal := query_context.Principal{UserID: "u1"}
	meta := query_context.NewRequestMeta(netip.Addr{})
	meta.SetPrincipal(principal)
	req := validQuery()
	req.SetEdns0(1232, false)
	execCalls := 0
	exec := executableFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		execCalls++
		if qCtx.Q().IsEdns0() != nil || qCtx.OriginalQuery().IsEdns0() == nil {
			t.Fatal("policy request change leaked into the original query snapshot")
		}
		response := new(dns.Msg)
		response.SetReply(qCtx.Q())
		qCtx.SetResponse(response)
		return nil
	})
	var order []string
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: exec,
		Admit: func(context.Context, query_context.Principal) error {
			order = append(order, "admit")
			return nil
		},
		BeforeExec: func(_ context.Context, got query_context.Principal, working *dns.Msg) (*dns.Msg, error) {
			order = append(order, "before")
			if got != principal || working == req {
				t.Fatal("policy did not receive the principal and an isolated request")
			}
			working.Extra = nil
			return nil, nil
		},
		AfterExec: func(_ context.Context, got query_context.Principal, working, response *dns.Msg) (*dns.Msg, error) {
			order = append(order, "after")
			if got != principal || working.Extra != nil || response == nil {
				t.Fatal("response policy received unexpected values")
			}
			response.AuthenticatedData = true
			return response, nil
		},
	})
	response, err := h.ServeDNS(context.Background(), req, meta)
	if err != nil || !response.AuthenticatedData || req.IsEdns0() == nil {
		t.Fatalf("response=%v err=%v original EDNS=%v", response, err, req.IsEdns0())
	}
	if got := fmt.Sprint(order); got != "[admit before after]" || execCalls != 1 {
		t.Fatalf("order=%s", got)
	}
}

func TestPolicyCanAnswerWithoutExecuting(t *testing.T) {
	exec := new(testExecutable)
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: exec,
		BeforeExec: func(_ context.Context, _ query_context.Principal, request *dns.Msg) (*dns.Msg, error) {
			response := new(dns.Msg)
			response.SetReply(request)
			response.Rcode = dns.RcodeNameError
			return response, nil
		},
	})
	response, err := h.ServeDNS(context.Background(), validQuery(), nil)
	if err != nil || response.Rcode != dns.RcodeNameError || exec.calls != 0 {
		t.Fatalf("response=%v err=%v exec calls=%d", response, err, exec.calls)
	}
}

func TestPolicyErrorBecomesSERVFAIL(t *testing.T) {
	exec := new(testExecutable)
	h, _ := NewEntryHandler(EntryHandlerOpts{
		Entry: exec,
		BeforeExec: func(context.Context, query_context.Principal, *dns.Msg) (*dns.Msg, error) {
			return nil, errors.New("policy unavailable")
		},
	})
	response, err := h.ServeDNS(context.Background(), validQuery(), nil)
	if err != nil || response.Rcode != dns.RcodeServerFailure || exec.calls != 0 {
		t.Fatalf("response=%v err=%v exec calls=%d", response, err, exec.calls)
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
			if got.ResponseSource != query_context.ResponseSourceServfail {
				t.Fatalf("ResponseSource=%q", got.ResponseSource)
			}
		})
	}
}
