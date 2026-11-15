package executable_seq

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/query_context"
)

type cacheHitExecutable struct{}

func (cacheHitExecutable) Exec(_ context.Context, qCtx *query_context.Context, _ ExecutableChainNode) error {
	qCtx.SetResponseWithTrace(new(dns.Msg), query_context.ResponseTrace{Source: query_context.ResponseSourceCache, SourceID: "cache"})
	qCtx.SetCacheHit(true)
	return nil
}

func TestAsyncWaitTransfersCacheHit(t *testing.T) {
	base := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	selected := base.Copy()
	selected.SetResponse(new(dns.Msg))
	selected.SetCacheHit(true)
	selected.SetResponseTrace(query_context.ResponseTrace{Source: query_context.ResponseSourceCache, SourceID: "cache"})
	c := make(chan *parallelECSResult, 1)
	c <- &parallelECSResult{qCtx: selected}
	if err := asyncWait(context.Background(), base, zap.NewNop(), c, 1); err != nil {
		t.Fatal(err)
	}
	if !base.CacheHit() {
		t.Fatal("selected parallel marker was lost")
	}
	if got := base.ResponseTrace(); got.Source != query_context.ResponseSourceCache || got.SourceID != "cache" {
		t.Fatalf("selected parallel trace was lost: %+v", got)
	}
}

type plainResponseExecutable struct{}

func (plainResponseExecutable) Exec(_ context.Context, qCtx *query_context.Context, _ ExecutableChainNode) error {
	qCtx.SetResponse(new(dns.Msg))
	return nil
}

func TestFallbackPrimaryKeepsCacheHit(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fastFallback time.Duration
	}{
		{name: "in place", fastFallback: 0},
		{name: "fast fallback copy", fastFallback: time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
			f := &FallbackNode{
				primary:              WrapExecutable(cacheHitExecutable{}),
				secondary:            WrapExecutable(plainResponseExecutable{}),
				fastFallbackDuration: tc.fastFallback,
				logger:               zap.NewNop(),
			}
			if err := f.exec(context.Background(), base); err != nil {
				t.Fatal(err)
			}
			if !base.CacheHit() {
				t.Fatal("primary cache hit marker was lost")
			}
			if got := base.ResponseTrace(); got.Source != query_context.ResponseSourceCache || got.SourceID != "cache" {
				t.Fatalf("primary cache trace was lost: %+v", got)
			}
		})
	}
}
