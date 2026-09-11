package executable_seq

import (
	"context"
	"testing"

	"github.com/miekg/dns"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/query_context"
)

type cacheHitExecutable struct{}

func (cacheHitExecutable) Exec(_ context.Context, qCtx *query_context.Context, _ ExecutableChainNode) error {
	qCtx.SetResponse(new(dns.Msg))
	qCtx.SetCacheHit(true)
	return nil
}

func TestAsyncWaitTransfersCacheHit(t *testing.T) {
	base := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	selected := base.Copy()
	selected.SetResponse(new(dns.Msg))
	selected.SetCacheHit(true)
	c := make(chan *parallelECSResult, 1)
	c <- &parallelECSResult{qCtx: selected}
	if err := asyncWait(context.Background(), base, zap.NewNop(), c, 1); err != nil {
		t.Fatal(err)
	}
	if !base.CacheHit() {
		t.Fatal("selected parallel marker was lost")
	}
}

func TestIsolatePrimaryTransfersCacheHit(t *testing.T) {
	base := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	f := &FallbackNode{primary: WrapExecutable(cacheHitExecutable{}), logger: zap.NewNop()}
	if err := f.isolateDoPrimary(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if !base.CacheHit() {
		t.Fatal("isolated fallback marker was lost")
	}
}
