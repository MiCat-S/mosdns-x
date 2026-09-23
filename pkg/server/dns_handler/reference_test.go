package dns_handler

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

// The reference resolver a response policy receives must run the handler's
// own chain, must not be charged through Admit, and must not re-enter the
// request or response policy.
func TestResponsePolicyReferenceUsesChainWithoutAdmitOrPolicy(t *testing.T) {
	var chainCalls, admits, befores, afters atomic.Int32
	chain := executableFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		chainCalls.Add(1)
		q := qCtx.Q()
		r := new(dns.Msg).SetReply(q)
		hdr := dns.RR_Header{Name: q.Question[0].Name, Class: dns.ClassINET, Ttl: 60, Rrtype: q.Question[0].Qtype}
		switch q.Question[0].Qtype {
		case dns.TypeA:
			r.Answer = []dns.RR{&dns.A{Hdr: hdr, A: net.ParseIP("192.0.2.1")}}
		case dns.TypeAAAA:
			r.Answer = []dns.RR{&dns.AAAA{Hdr: hdr, AAAA: net.ParseIP("2001:db8::1")}}
		}
		qCtx.SetResponse(r)
		return nil
	})

	var referenced *dns.Msg
	h, err := NewEntryHandler(EntryHandlerOpts{
		Entry: chain,
		Admit: func(context.Context, query_context.Principal) error { admits.Add(1); return nil },
		BeforeExec: func(context.Context, query_context.Principal, *dns.Msg) (*dns.Msg, error) {
			befores.Add(1)
			return nil, nil
		},
		AfterExec: func(ctx context.Context, _ query_context.Principal, req, resp *dns.Msg) (*dns.Msg, error) {
			afters.Add(1)
			resolve := query_context.ReferenceResolverFrom(ctx)
			if resolve == nil {
				t.Fatal("response policy received no reference resolver")
			}
			ref := req.Copy()
			ref.Question[0].Qtype = dns.TypeA
			got, err := resolve(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			referenced = got
			return resp, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	meta := query_context.NewRequestMeta(netip.MustParseAddr("198.51.100.7"))
	meta.SetPrincipal(query_context.Principal{UserID: "u1"})
	if _, err := h.ServeDNS(context.Background(), new(dns.Msg).SetQuestion("dual.test.", dns.TypeAAAA), meta); err != nil {
		t.Fatal(err)
	}

	if referenced == nil || len(referenced.Answer) != 1 || referenced.Answer[0].Header().Rrtype != dns.TypeA {
		t.Fatalf("reference lookup did not run the chain: %v", referenced)
	}
	if got := chainCalls.Load(); got != 2 {
		t.Fatalf("chain ran %d times, want 2: the query and its reference", got)
	}
	if got := admits.Load(); got != 1 {
		t.Fatalf("Admit ran %d times, want 1: the reference must not be charged", got)
	}
	if b, a := befores.Load(), afters.Load(); b != 1 || a != 1 {
		t.Fatalf("policy ran before=%d after=%d, want 1 each: the reference re-entered policy", b, a)
	}
}
