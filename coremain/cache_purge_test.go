package coremain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pmkol/mosdns-x/internal/runtimeconfig"
	"github.com/pmkol/mosdns-x/pkg/cache"
)

type purgeTestPlugin struct {
	tag     string
	typ     string
	removed int
	err     error
	got     string
}

func (p *purgeTestPlugin) Tag() string { return p.tag }
func (p *purgeTestPlugin) Type() string {
	if p.typ != "" {
		return p.typ
	}
	return "cache"
}
func (p *purgeTestPlugin) Close() error { return nil }
func (p *purgeTestPlugin) PurgeDomain(_ context.Context, domain string, _ bool) (int, error) {
	p.got = domain
	return p.removed, p.err
}

type otherTestPlugin struct{}

func (otherTestPlugin) Tag() string  { return "other" }
func (otherTestPlugin) Type() string { return "other" }
func (otherTestPlugin) Close() error { return nil }

func TestRuntimeManagerPurgesEveryCacheOfTheRunningGeneration(t *testing.T) {
	manager := NewRuntimeManager()
	if _, err := manager.PurgeDomainCache(context.Background(), "example.com", false); !errors.Is(err, runtimeconfig.ErrRuntimeUnavailable) {
		t.Fatalf("purge without runtime: %v", err)
	}

	old := &purgeTestPlugin{tag: "old", removed: 9}
	generation := runtimeTestGeneration(runtimeTestHandler{answer: 1}, nil)
	generation.plugins = []Plugin{old}
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, generation)); err != nil {
		t.Fatal(err)
	}
	mem := &purgeTestPlugin{tag: "mem", removed: 2}
	redis := &purgeTestPlugin{tag: "redis", removed: 1}
	preset := &purgeTestPlugin{tag: "_default_cache", typ: "preset", removed: 4}
	generation = runtimeTestGeneration(runtimeTestHandler{answer: 2}, nil)
	generation.plugins = []Plugin{otherTestPlugin{}, mem, redis, preset}
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, generation)); err != nil {
		t.Fatal(err)
	}

	result, err := manager.PurgeDomainCache(context.Background(), " Example.COM. ", true)
	// The preset cache is purged (4 of the 7) but not counted.
	want := runtimeconfig.CachePurge{Domain: "example.com.", Subdomains: true, Caches: 2, Removed: 7}
	if err != nil || result != want {
		t.Fatalf("purge = %+v, %v; want %+v", result, err, want)
	}
	if old.got != "" || mem.got != "example.com." || redis.got != "example.com." || preset.got != "example.com." {
		t.Fatalf("purged old=%q mem=%q redis=%q preset=%q", old.got, mem.got, redis.got, preset.got)
	}

	redis.err = errors.New("redis scan: timeout")
	result, err = manager.PurgeDomainCache(context.Background(), "example.com", false)
	if err == nil || !strings.Contains(err.Error(), "plugin redis") || result.Removed != 7 || result.Caches != 2 {
		t.Fatalf("partial failure = %+v, %v", result, err)
	}

	redis.err = cache.ErrPurgeBusy
	if _, err := manager.PurgeDomainCache(context.Background(), "example.com", false); !errors.Is(err, runtimeconfig.ErrCachePurgeBusy) {
		t.Fatalf("busy: %v", err)
	}

	if _, err := manager.PurgeDomainCache(context.Background(), "*.example.com", false); !errors.Is(err, runtimeconfig.ErrInvalidDomain) {
		t.Fatalf("wildcard: %v", err)
	}
	if err := manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}
