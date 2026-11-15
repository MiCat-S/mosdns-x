package dnsutils

import (
	"testing"

	"github.com/miekg/dns"
)

func TestGenEmptyReplyNamesTheFakeSOAAfterTheQuery(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("www.example.org.", dns.TypeAAAA)
	r := GenEmptyReply(q, dns.RcodeSuccess)
	if len(r.Ns) != 1 {
		t.Fatalf("want one authority record, got %v", r.Ns)
	}
	if got := r.Ns[0].Header().Name; got != "www.example.org." {
		t.Fatalf("fake SOA owner = %q, want the query name", got)
	}

	if got := GenEmptyReply(new(dns.Msg), dns.RcodeSuccess).Ns[0].Header().Name; got != "." {
		t.Fatalf("fake SOA owner without a question = %q, want the root", got)
	}
}
