package bundled_upstream

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/query_context"
)

type fakeUpstream struct {
	id      string
	rcode   int
	err     error
	trusted bool
	onQuery func(*dns.Msg)
}

func (u *fakeUpstream) Exchange(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	q.Compress = true
	if u.onQuery != nil {
		u.onQuery(q)
	}
	if u.err != nil {
		return nil, u.err
	}
	r := new(dns.Msg)
	r.SetReply(q)
	r.Rcode = u.rcode
	return r, nil
}
func (u *fakeUpstream) Trusted() bool      { return u.trusted }
func (u *fakeUpstream) Address() string    { return "contains-secret" }
func (u *fakeUpstream) ObserverID() string { return u.id }

func TestExchangeParallelCopiesQueriesAndObservesAttempts(t *testing.T) {
	q := new(dns.Msg).SetQuestion("example.org.", dns.TypeA)
	meta := query_context.NewRequestMeta(netip.Addr{})
	principal := query_context.Principal{UserID: "u", CredentialID: "c"}
	meta.SetPrincipal(principal)
	var mu sync.Mutex
	seen := make(map[*dns.Msg]struct{})
	var attempts []query_context.UpstreamAttempt
	meta.SetUpstreamObserver(func(a query_context.UpstreamAttempt) { mu.Lock(); attempts = append(attempts, a); mu.Unlock() })
	check := func(msg *dns.Msg) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := seen[msg]; ok {
			t.Error("upstreams shared query pointer")
		}
		seen[msg] = struct{}{}
	}
	upstreams := []Upstream{
		&fakeUpstream{id: "ff/0", rcode: dns.RcodeNameError, trusted: true, onQuery: check},
		&fakeUpstream{id: "ff/1", err: errors.New("failed"), onQuery: check},
	}
	r, selectedID, err := ExchangeParallel(context.Background(), query_context.NewContext(q, meta), upstreams, nil)
	if err != nil || r.Rcode != dns.RcodeNameError {
		t.Fatalf("response=%v err=%v", r, err)
	}
	if selectedID != "ff/0" {
		t.Fatalf("selected upstream=%q", selectedID)
	}
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := len(attempts)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("attempts=%d", len(attempts))
	}
	for _, a := range attempts {
		if a.Principal != principal {
			t.Fatalf("principal=%#v", a.Principal)
		}
		if a.UpstreamID == "contains-secret" {
			t.Fatal("raw address used as id")
		}
		if a.Rcode == dns.RcodeNameError && a.Failed {
			t.Fatal("NXDOMAIN marked failed")
		}
	}
	if q.Compress {
		t.Fatal("original query was mutated")
	}
}
