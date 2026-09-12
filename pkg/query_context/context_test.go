package query_context

import (
	"testing"

	"github.com/miekg/dns"
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
