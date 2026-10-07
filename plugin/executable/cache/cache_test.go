package cache

import (
	"bytes"
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/cache/mem_cache"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

type execFunc func(context.Context, *query_context.Context, executable_seq.ExecutableChainNode) error

func (f execFunc) Exec(ctx context.Context, q *query_context.Context, next executable_seq.ExecutableChainNode) error {
	return f(ctx, q, next)
}

func TestCacheHitMarker(t *testing.T) {
	p := &cachePlugin{BP: coremain.NewBP("cache", "cache", zap.NewNop(), nil), args: &Args{}, backend: mem_cache.NewMemCache(8, 0), queryTotal: prometheus.NewCounter(prometheus.CounterOpts{}), hitTotal: prometheus.NewCounter(prometheus.CounterOpts{}), lazyHitTotal: prometheus.NewCounter(prometheus.CounterOpts{})}
	defer p.backend.Close()
	q := new(dns.Msg).SetQuestion("example.org.", dns.TypeA)
	next := executable_seq.WrapExecutable(execFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		r := new(dns.Msg)
		r.SetReply(qCtx.Q())
		rr, _ := dns.NewRR("example.org. 60 IN A 192.0.2.1")
		r.Answer = []dns.RR{rr}
		qCtx.SetResponse(r)
		return nil
	}))
	miss := query_context.NewContext(q.Copy(), nil)
	if err := p.Exec(context.Background(), miss, next); err != nil {
		t.Fatal(err)
	}
	if miss.CacheHit() {
		t.Fatal("cache miss marked as hit")
	}
	hit := query_context.NewContext(q.Copy(), nil)
	if err := p.Exec(context.Background(), hit, next); err != nil {
		t.Fatal(err)
	}
	if !hit.CacheHit() {
		t.Fatal("cache hit marker missing")
	}
}

func newTestCache(compress bool) *cachePlugin {
	return &cachePlugin{BP: coremain.NewBP("cache_wan", "cache", zap.NewNop(), nil), args: &Args{CompressResp: compress}, backend: mem_cache.NewMemCache(8, 0), queryTotal: prometheus.NewCounter(prometheus.CounterOpts{}), hitTotal: prometheus.NewCounter(prometheus.CounterOpts{}), lazyHitTotal: prometheus.NewCounter(prometheus.CounterOpts{})}
}

func TestCacheHitKeepsTheUpstreamThatAnswered(t *testing.T) {
	for _, compress := range []bool{false, true} {
		p := newTestCache(compress)
		q := new(dns.Msg).SetQuestion("example.org.", dns.TypeA)
		next := executable_seq.WrapExecutable(execFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
			r := new(dns.Msg)
			r.SetReply(qCtx.Q())
			rr, _ := dns.NewRR("example.org. 60 IN A 192.0.2.1")
			r.Answer = []dns.RR{rr}
			qCtx.SetResponseWithTrace(r, query_context.ResponseTrace{Source: query_context.ResponseSourceUpstream, SourceID: "forward_local", UpstreamLabel: "223.5.5.5 (UDP)"})
			return nil
		}))
		if err := p.Exec(context.Background(), query_context.NewContext(q.Copy(), nil), next); err != nil {
			t.Fatal(err)
		}
		journal := query_context.NewJournal(time.Now())
		meta := query_context.NewRequestMeta(netip.Addr{})
		meta.SetJournal(journal)
		hit := query_context.NewContext(q.Copy(), meta)
		if err := p.Exec(context.Background(), hit, next); err != nil {
			t.Fatal(err)
		}
		trace := hit.ResponseTrace()
		if !hit.CacheHit() || trace.Source != query_context.ResponseSourceCache || trace.CacheOrigin != "223.5.5.5 (UDP)" {
			t.Fatalf("compress=%v hit=%v trace=%+v", compress, hit.CacheHit(), trace)
		}
		if hit.R() == nil || len(hit.R().Answer) != 1 {
			t.Fatalf("compress=%v cached response=%v", compress, hit.R())
		}
		steps := journal.Snapshot(0).Steps
		if len(steps) != 1 || steps[0].Kind != query_context.RouteStepCacheHit || steps[0].Detail != "cache_wan" {
			t.Fatalf("steps = %+v", steps)
		}
		p.backend.Close()
	}
}

func TestCacheReadsValuesWithoutOrigin(t *testing.T) {
	r := new(dns.Msg).SetQuestion("example.org.", dns.TypeA)
	r.Response = true
	packed, err := r.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if origin, msg := unwrapOrigin(packed); origin != "" || !bytes.Equal(msg, packed) {
		t.Fatalf("legacy value: origin=%q", origin)
	}
	origin, msg := unwrapOrigin(wrapOrigin("forward #1 (DoH)", packed))
	if origin != "forward #1 (DoH)" || !bytes.Equal(msg, packed) {
		t.Fatalf("origin=%q", origin)
	}
	if got := wrapOrigin("", packed); !bytes.Equal(got, packed) {
		t.Fatal("empty origin changed the value")
	}
}
