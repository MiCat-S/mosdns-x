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
