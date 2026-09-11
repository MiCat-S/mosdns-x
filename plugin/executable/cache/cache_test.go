package cache

import (
	"context"
	"testing"

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
