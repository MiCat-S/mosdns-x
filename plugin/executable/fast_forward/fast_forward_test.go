package fastforward

import (
	"context"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/bundled_upstream"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	upstreamtrace "github.com/pmkol/mosdns-x/pkg/upstream/trace"
)

type pathUpstream struct {
	normalCalls   int
	detailedCalls int
}

func (u *pathUpstream) ExchangeContext(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	u.normalCalls++
	return new(dns.Msg).SetReply(q), nil
}

func (u *pathUpstream) ExchangeContextDetailed(_ context.Context, q *dns.Msg) (upstreamtrace.Result, error) {
	u.detailedCalls++
	snapshot := dnsutils.SnapshotEDNS(q)
	return upstreamtrace.NewResult(new(dns.Msg).SetReply(q), snapshot), nil
}

func (*pathUpstream) Close() error { return nil }

type legacyPathUpstream struct{ calls int }

func (u *legacyPathUpstream) ExchangeContext(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	u.calls++
	return new(dns.Msg).SetReply(q), nil
}
func (*legacyPathUpstream) Close() error { return nil }

func TestFastForwardUsesLegacyPathWhenQueryDetailsDisabled(t *testing.T) {
	u := new(pathUpstream)
	f := &fastForward{
		BP: coremain.NewBP("forward", PluginType, nil, nil),
		upstreamWrappers: []bundled_upstream.Upstream{&upstreamWrapper{
			address: "test", observerID: "forward/0", trusted: true, u: u,
		}},
	}
	qCtx := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	if err := f.exec(context.Background(), qCtx); err != nil {
		t.Fatal(err)
	}
	if u.normalCalls != 1 || u.detailedCalls != 0 {
		t.Fatalf("normal calls=%d detailed calls=%d", u.normalCalls, u.detailedCalls)
	}
	trace := qCtx.ResponseTrace()
	if trace.UpstreamID != "forward/0" || trace.UpstreamRequestEDNS != nil || trace.UpstreamResponseEDNS != nil {
		t.Fatalf("legacy trace = %+v", trace)
	}
}

func TestFastForwardDetailedModeFallsBackWithUnavailableSnapshots(t *testing.T) {
	u := new(legacyPathUpstream)
	f := &fastForward{
		BP: coremain.NewBP("forward", PluginType, nil, nil),
		upstreamWrappers: []bundled_upstream.Upstream{&upstreamWrapper{
			address: "test", observerID: "forward/0", trusted: true, u: u,
		}},
	}
	qCtx := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	qCtx.SetCaptureQueryDetails(true)
	if err := f.exec(context.Background(), qCtx); err != nil {
		t.Fatal(err)
	}
	trace := qCtx.ResponseTrace()
	if u.calls != 1 || qCtx.R() == nil || trace.UpstreamID != "forward/0" || trace.UpstreamStageStatus != query_context.UpstreamStageUnavailable || trace.UpstreamRequestEDNS != nil || trace.UpstreamResponseEDNS != nil {
		t.Fatalf("calls=%d response=%v trace=%+v", u.calls, qCtx.R(), trace)
	}
}
