package bundled_upstream

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	upstreamtrace "github.com/pmkol/mosdns-x/pkg/upstream/trace"
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

type detailedFakeUpstream struct {
	*fakeUpstream
	delay       time.Duration
	requestECS  string
	responseECS string
}

func (u *detailedFakeUpstream) ExchangeDetailed(ctx context.Context, q *dns.Msg) (upstreamtrace.Result, error) {
	select {
	case <-time.After(u.delay):
	case <-ctx.Done():
		return upstreamtrace.Result{}, ctx.Err()
	}
	q.SetEdns0(1232, false)
	q.IsEdns0().Option = append(q.IsEdns0().Option, &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP(u.requestECS),
	})
	requestSnapshot := dnsutils.SnapshotEDNS(q)
	r := new(dns.Msg)
	r.SetReply(q)
	r.Rcode = u.rcode
	r.SetEdns0(1232, false)
	r.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP(u.responseECS),
	}}
	return upstreamtrace.NewResult(r, requestSnapshot), nil
}

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

func TestExchangeParallelDetailedKeepsSelectedExchangeTogether(t *testing.T) {
	qCtx := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	upstreams := []Upstream{
		&detailedFakeUpstream{fakeUpstream: &fakeUpstream{id: "ff/slow", rcode: dns.RcodeSuccess, trusted: true}, delay: 50 * time.Millisecond, requestECS: "192.0.2.1", responseECS: "192.0.2.2"},
		&detailedFakeUpstream{fakeUpstream: &fakeUpstream{id: "ff/fast", rcode: dns.RcodeSuccess}, delay: time.Millisecond, requestECS: "198.51.100.1", responseECS: "203.0.113.2"},
	}
	result, err := ExchangeParallelDetailed(context.Background(), qCtx, upstreams, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond) // let the unselected branch finish
	if result.UpstreamID != "ff/fast" || result.RequestEDNS == nil || result.ResponseEDNS == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.RequestEDNS.ECS.Address != "198.51.100.0" || result.ResponseEDNS.ECS.Address != "203.0.113.0" {
		t.Fatalf("mismatched selected snapshots: request=%+v response=%+v", result.RequestEDNS, result.ResponseEDNS)
	}
}

func TestExchangeParallelDetailedKeepsSelectedSERVFAIL(t *testing.T) {
	qCtx := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	u := &detailedFakeUpstream{fakeUpstream: &fakeUpstream{id: "ff/0", rcode: dns.RcodeServerFailure, trusted: true}, requestECS: "192.0.2.1", responseECS: "203.0.113.1"}
	result, err := ExchangeParallelDetailed(context.Background(), qCtx, []Upstream{u}, nil)
	if err != nil || result.Response == nil || result.Response.Rcode != dns.RcodeServerFailure || result.UpstreamID != "ff/0" || !result.DetailsAvailable || result.RequestEDNS == nil || result.ResponseEDNS == nil {
		t.Fatalf("SERVFAIL result=%+v err=%v", result, err)
	}
}

type pathFakeUpstream struct {
	normalCalls   int
	detailedCalls int
}

func (u *pathFakeUpstream) Exchange(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	u.normalCalls++
	return new(dns.Msg).SetReply(q), nil
}
func (u *pathFakeUpstream) ExchangeDetailed(_ context.Context, q *dns.Msg) (upstreamtrace.Result, error) {
	u.detailedCalls++
	snapshot := dnsutils.SnapshotEDNS(q)
	return upstreamtrace.NewResult(new(dns.Msg).SetReply(q), snapshot), nil
}
func (*pathFakeUpstream) Trusted() bool      { return true }
func (*pathFakeUpstream) Address() string    { return "test" }
func (*pathFakeUpstream) ObserverID() string { return "ff/path" }

func TestExchangeParallelLegacyPathDoesNotCaptureDetails(t *testing.T) {
	u := new(pathFakeUpstream)
	qCtx := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	if _, _, err := ExchangeParallel(context.Background(), qCtx, []Upstream{u}, nil); err != nil {
		t.Fatal(err)
	}
	if u.normalCalls != 1 || u.detailedCalls != 0 {
		t.Fatalf("normal calls=%d detailed calls=%d", u.normalCalls, u.detailedCalls)
	}
}

func TestExchangeParallelDetailedFallsBackWithoutGuessingUnsupportedBoundary(t *testing.T) {
	qCtx := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	result, err := ExchangeParallelDetailed(context.Background(), qCtx, []Upstream{&fakeUpstream{id: "ff/0", trusted: true}}, nil)
	if err != nil || result.Response == nil || !result.Attempted || result.DetailsAvailable || result.RequestEDNS != nil || result.ResponseEDNS != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
