package coremain

import "github.com/pmkol/mosdns-x/internal/runtimeconfig"

// UpstreamIDLister is implemented by plugins that forward to upstreams and
// know the identifiers telemetry records them under (tag/index).
type UpstreamIDLister interface {
	UpstreamIDs() []string
}

// ActiveUpstreamIDs lists the upstream identifiers of the running
// generation, so callers can tell recorded upstreams that still exist from
// ones a configuration change removed.
func (m *RuntimeManager) ActiveUpstreamIDs() ([]string, error) {
	generation, ok := m.acquireCurrent()
	if !ok {
		return nil, runtimeconfig.ErrRuntimeUnavailable
	}
	defer generation.releaseRequest()
	var ids []string
	for _, plugin := range generation.plugins {
		if lister, ok := plugin.(UpstreamIDLister); ok {
			ids = append(ids, lister.UpstreamIDs()...)
		}
	}
	return ids, nil
}
