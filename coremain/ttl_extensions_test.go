package coremain

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/pmkol/mosdns-x/internal/runtimeconfig"
)

type ttlTestPlugin struct {
	tag  string
	ttls map[string]uint32
}

func (p *ttlTestPlugin) Tag() string                     { return p.tag }
func (p *ttlTestPlugin) Type() string                    { return "adaptive_ttl" }
func (p *ttlTestPlugin) Close() error                    { return nil }
func (p *ttlTestPlugin) ExtendedTTLs() map[string]uint32 { return p.ttls }

func TestRuntimeManagerMergesTTLExtensionsOfTheRunningGeneration(t *testing.T) {
	manager := NewRuntimeManager()
	if _, err := manager.ActiveTTLExtensions(); !errors.Is(err, runtimeconfig.ErrRuntimeUnavailable) {
		t.Fatalf("without runtime: %v", err)
	}
	generation := runtimeTestGeneration(runtimeTestHandler{answer: 1}, nil)
	generation.plugins = []Plugin{
		otherTestPlugin{},
		&ttlTestPlugin{tag: "adaptive_a", ttls: map[string]uint32{"a.example.": 1800, "b.example.": 600}},
		&ttlTestPlugin{tag: "adaptive_b", ttls: map[string]uint32{"b.example.": 3600}},
	}
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, generation)); err != nil {
		t.Fatal(err)
	}
	ttls, err := manager.ActiveTTLExtensions()
	if want := map[string]uint32{"a.example.": 1800, "b.example.": 3600}; err != nil || !maps.Equal(ttls, want) {
		t.Fatalf("ttls = %v, %v", ttls, err)
	}
	if err := manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}
