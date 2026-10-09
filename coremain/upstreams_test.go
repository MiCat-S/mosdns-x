package coremain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pmkol/mosdns-x/internal/runtimeconfig"
)

type upstreamTestPlugin struct {
	tag string
	ids []string
}

func (p *upstreamTestPlugin) Tag() string           { return p.tag }
func (p *upstreamTestPlugin) Type() string          { return "fast_forward" }
func (p *upstreamTestPlugin) Close() error          { return nil }
func (p *upstreamTestPlugin) UpstreamIDs() []string { return p.ids }

func TestRuntimeManagerListsUpstreamsOfTheRunningGeneration(t *testing.T) {
	manager := NewRuntimeManager()
	if _, err := manager.ActiveUpstreamIDs(); !errors.Is(err, runtimeconfig.ErrRuntimeUnavailable) {
		t.Fatalf("without runtime: %v", err)
	}
	generation := runtimeTestGeneration(runtimeTestHandler{answer: 1}, nil)
	generation.plugins = []Plugin{&upstreamTestPlugin{tag: "forward_old", ids: []string{"forward_old/0", "forward_old/1"}}}
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, generation)); err != nil {
		t.Fatal(err)
	}
	generation = runtimeTestGeneration(runtimeTestHandler{answer: 2}, nil)
	generation.plugins = []Plugin{
		otherTestPlugin{},
		&upstreamTestPlugin{tag: "forward_a", ids: []string{"forward_a/0"}},
		&upstreamTestPlugin{tag: "forward_b", ids: []string{"forward_b/0", "forward_b/1"}},
	}
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, generation)); err != nil {
		t.Fatal(err)
	}
	// Only the running generation counts; the swapped-out one is gone.
	ids, err := manager.ActiveUpstreamIDs()
	if got := strings.Join(ids, " "); err != nil || got != "forward_a/0 forward_b/0 forward_b/1" {
		t.Fatalf("ids = %q, %v", got, err)
	}
	if err := manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}
