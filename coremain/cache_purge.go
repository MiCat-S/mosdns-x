package coremain

import (
	"context"
	"errors"
	"fmt"

	"github.com/pmkol/mosdns-x/internal/runtimeconfig"
	"github.com/pmkol/mosdns-x/pkg/cache"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
)

// DomainCachePurger is implemented by plugins that keep DNS responses and can
// drop the ones for a domain.
type DomainCachePurger interface {
	PurgeDomain(ctx context.Context, domain string, subdomains bool) (int, error)
}

// PurgeDomainCache removes the cached responses for domain from every cache
// plugin of the running generation. A failing plugin does not stop the
// others; the result counts what was removed and the error names the
// plugins that failed. Preset caches are purged but not counted, so Caches
// reflects the configuration.
func (m *RuntimeManager) PurgeDomainCache(ctx context.Context, domain string, subdomains bool) (runtimeconfig.CachePurge, error) {
	name, err := dnsutils.NormalizeDomain(domain)
	if err != nil {
		return runtimeconfig.CachePurge{}, fmt.Errorf("%w: %v", runtimeconfig.ErrInvalidDomain, err)
	}
	result := runtimeconfig.CachePurge{Domain: name, Subdomains: subdomains}
	generation, ok := m.acquireCurrent()
	if !ok {
		return result, runtimeconfig.ErrRuntimeUnavailable
	}
	defer generation.releaseRequest()
	var errs []error
	for _, plugin := range generation.plugins {
		purger, ok := plugin.(DomainCachePurger)
		if !ok {
			continue
		}
		if plugin.Type() != "preset" {
			result.Caches++
		}
		removed, err := purger.PurgeDomain(ctx, name, subdomains)
		result.Removed += removed
		if errors.Is(err, cache.ErrPurgeBusy) {
			err = runtimeconfig.ErrCachePurgeBusy
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: %w", plugin.Tag(), err))
		}
	}
	return result, errors.Join(errs...)
}
