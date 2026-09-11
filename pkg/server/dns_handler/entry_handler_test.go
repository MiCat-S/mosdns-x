package dns_handler

import (
	"context"
	"errors"
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
