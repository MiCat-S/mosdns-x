package coremain

import "github.com/pmkol/mosdns-x/internal/runtimeconfig"

// TTLExtender is implemented by plugins that lengthen the TTL of responses.
// ExtendedTTLs maps each name whose TTL is lengthened right now to the TTL.
type TTLExtender interface {
	ExtendedTTLs() map[string]uint32
}

// ActiveTTLExtensions reports, for every name a plugin of the running
// generation lengthens the TTL of, the longest such TTL.
func (m *RuntimeManager) ActiveTTLExtensions() (map[string]uint32, error) {
	generation, ok := m.acquireCurrent()
	if !ok {
		return nil, runtimeconfig.ErrRuntimeUnavailable
	}
	defer generation.releaseRequest()
	ttls := make(map[string]uint32)
	for _, plugin := range generation.plugins {
		extender, ok := plugin.(TTLExtender)
		if !ok {
			continue
		}
		for name, ttl := range extender.ExtendedTTLs() {
			ttls[name] = max(ttls[name], ttl)
		}
	}
	return ttls, nil
}
