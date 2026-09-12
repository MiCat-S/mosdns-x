package query_context

import (
	"net"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
)

func TestPrincipalValueAndNilGetter(t *testing.T) {
	principal := Principal{UserID: "user", CredentialID: "credential", CredentialVersion: 9}
	meta := new(RequestMeta)
	meta.SetPrincipal(principal)
	if got := meta.GetPrincipal(); got != principal {
		t.Fatalf("GetPrincipal() = %#v", got)
	}
	var nilMeta *RequestMeta
	if got := nilMeta.GetPrincipal(); got != (Principal{}) {
		t.Fatalf("nil GetPrincipal() = %#v", got)
	}
}

func TestCacheHitLifecycle(t *testing.T) {
	ctx := NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	ctx.SetResponse(new(dns.Msg))
	ctx.SetCacheHit(true)
	copy := ctx.Copy()
	if !copy.CacheHit() {
		t.Fatal("Copy did not preserve cache hit")
	}
	ctx.SetResponse(new(dns.Msg))
	if ctx.CacheHit() {
		t.Fatal("SetResponse did not clear stale cache hit")
	}
}

func TestResponseTraceCopyAndAdopt(t *testing.T) {
	ctx := NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	ctx.SetResponseWithTrace(new(dns.Msg), ResponseTrace{Source: ResponseSourceUpstream, SourceID: "remote", UpstreamID: "remote/1"})
	ctx.SetCacheHit(true)

	branch := ctx.Copy()
	branch.SetResponseWithTrace(new(dns.Msg), ResponseTrace{Source: ResponseSourceCache, SourceID: "cache_wan"})
	branch.SetCacheHit(true)
	if ctx.ResponseTrace().Source != ResponseSourceUpstream {
		t.Fatalf("copy changed parent trace: %+v", ctx.ResponseTrace())
	}

	ctx.AdoptResponse(branch)
	if got := ctx.ResponseTrace(); got.Source != ResponseSourceCache || got.SourceID != "cache_wan" || !ctx.CacheHit() {
		t.Fatalf("adopted state=%+v cache=%v", got, ctx.CacheHit())
	}
	ctx.SetResponse(new(dns.Msg))
	if got := ctx.ResponseTrace(); got != (ResponseTrace{}) || ctx.CacheHit() {
		t.Fatalf("stale response metadata: %+v cache=%v", got, ctx.CacheHit())
	}
}

func TestResponseTraceSnapshotsAndCaptureFlagAreBranchPrivate(t *testing.T) {
	ctx := NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	ctx.SetCaptureQueryDetails(true)
	request := new(dns.Msg).SetQuestion("example.org.", dns.TypeA)
	request.SetEdns0(1232, false)
	request.IsEdns0().Option = append(request.IsEdns0().Option, &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP("192.0.2.1"),
	})
	reqSnapshot := dnsutils.SnapshotEDNS(request)
	ctx.SetResponseWithTrace(new(dns.Msg), ResponseTrace{
		Source: ResponseSourceUpstream, UpstreamID: "forward/0",
		UpstreamStageStatus: UpstreamStageSelected, UpstreamRequestEDNS: &reqSnapshot,
	})

	branch := ctx.Copy()
	if !branch.CaptureQueryDetails() {
		t.Fatal("capture choice was not copied")
	}
	trace := branch.ResponseTrace()
	trace.UpstreamRequestEDNS.OptionCodes[0] = dns.EDNS0COOKIE
	trace.UpstreamRequestEDNS.ECS.Address = "198.51.100.0"
	if got := ctx.ResponseTrace().UpstreamRequestEDNS; got.OptionCodes[0] != dns.EDNS0SUBNET || got.ECS.Address != "192.0.2.0" {
		t.Fatalf("branch shared trace state: %+v", got)
	}

	ctx.AdoptResponse(branch)
	trace = branch.ResponseTrace()
	trace.UpstreamRequestEDNS.ECS.Address = "203.0.113.0"
	if got := ctx.ResponseTrace().UpstreamRequestEDNS.ECS.Address; got != "192.0.2.0" {
		t.Fatalf("adopted trace shares branch state: %q", got)
	}
}

func TestSetResponseMarksSelectedUpstreamDiscardedDuringDetailedCapture(t *testing.T) {
	ctx := NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	ctx.SetCaptureQueryDetails(true)
	ctx.SetResponseWithTrace(new(dns.Msg), ResponseTrace{
		Source: ResponseSourceUpstream, UpstreamID: "forward/0", UpstreamStageStatus: UpstreamStageSelected,
	})
	ctx.SetResponse(new(dns.Msg))
	trace := ctx.ResponseTrace()
	if trace.UpstreamStageStatus != UpstreamStageDiscarded || trace.UpstreamID != "" || trace.UpstreamRequestEDNS != nil || trace.UpstreamResponseEDNS != nil {
		t.Fatalf("replacement trace = %+v", trace)
	}
}

func TestSetResponseWithLocalTraceKeepsDiscardedStatus(t *testing.T) {
	ctx := NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	ctx.SetCaptureQueryDetails(true)
	ctx.SetResponseWithTrace(new(dns.Msg), ResponseTrace{
		Source: ResponseSourceUpstream, UpstreamID: "forward/0", UpstreamStageStatus: UpstreamStageSelected,
	})
	ctx.SetResponseWithTrace(new(dns.Msg), ResponseTrace{Source: ResponseSourceHosts, SourceID: "hosts"})
	trace := ctx.ResponseTrace()
	if trace.Source != ResponseSourceHosts || trace.UpstreamStageStatus != UpstreamStageDiscarded || trace.UpstreamID != "" {
		t.Fatalf("local replacement trace = %+v", trace)
	}

	ctx.SetResponseWithTrace(new(dns.Msg), ResponseTrace{
		Source: ResponseSourceUpstream, UpstreamID: "forward/1", UpstreamStageStatus: UpstreamStageSelected,
	})
	trace = ctx.ResponseTrace()
	if trace.UpstreamStageStatus != UpstreamStageSelected || trace.UpstreamID != "forward/1" {
		t.Fatalf("explicit upstream trace was not adopted: %+v", trace)
	}
}

func TestSetResponseDiscardsSelectedUpstreamWhenSnapshotsUnavailable(t *testing.T) {
	ctx := NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	ctx.SetCaptureQueryDetails(true)
	ctx.SetResponseWithTrace(new(dns.Msg), ResponseTrace{
		Source: ResponseSourceUpstream, UpstreamID: "legacy-upstream/0", UpstreamStageStatus: UpstreamStageUnavailable,
	})
	ctx.SetResponseWithTrace(new(dns.Msg), ResponseTrace{Source: ResponseSourceHosts})
	trace := ctx.ResponseTrace()
	if trace.Source != ResponseSourceHosts || trace.UpstreamStageStatus != UpstreamStageDiscarded || trace.UpstreamID != "" {
		t.Fatalf("local replacement retained unavailable selected upstream: %+v", trace)
	}
}
