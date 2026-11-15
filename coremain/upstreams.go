package coremain

import "github.com/pmkol/mosdns-x/internal/runtimeconfig"

// UpstreamLister is implemented by plugins that forward to upstreams. It
// lists them under the identifiers telemetry records (tag/index) with the
// names query logs show.
type UpstreamLister interface {
	ListUpstreams() []runtimeconfig.ActiveUpstream
}

// ActiveUpstreams lists the upstreams of the running generation, so callers
// can tell recorded upstreams that still exist from ones a configuration
// change removed, and name them as the configuration does now.
func (m *RuntimeManager) ActiveUpstreams() ([]runtimeconfig.ActiveUpstream, error) {
	generation, ok := m.acquireCurrent()
	if !ok {
		return nil, runtimeconfig.ErrRuntimeUnavailable
	}
	defer generation.releaseRequest()
	var upstreams []runtimeconfig.ActiveUpstream
	for _, plugin := range generation.plugins {
		if lister, ok := plugin.(UpstreamLister); ok {
			upstreams = append(upstreams, lister.ListUpstreams()...)
		}
	}
	return upstreams, nil
}
