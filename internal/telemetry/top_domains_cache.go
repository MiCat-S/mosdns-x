package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// A ranking is reused for topDomainsReuse when a request asks for the same
// user and limit and a window whose ends are each within topDomainsReuse of
// the cached one. Panels send a window ending at "now", so exact keys would
// never repeat; the response carries the window it was computed for, so the
// reuse is visible rather than silent.
const (
	topDomainsReuse      = 5 * time.Second
	topDomainsCacheLimit = 256
)

type topDomainsKey struct {
	userID string
	limit  int
}

type topDomainsEntry struct {
	from, to   time.Time // as requested, before the retention clamp
	computedAt time.Time
	generation uint64
	result     TopDomains
}

type topDomainsCache struct {
	mu      sync.Mutex
	entries map[topDomainsKey]topDomainsEntry
	flight  singleflight.Group
}

// invalidateTopDomains drops every cached ranking. Erasing a user or
// changing the settings calls it so their effect shows at once.
func (s *Store) invalidateTopDomains() {
	s.topDomainsGeneration.Add(1)
	s.topDomains.mu.Lock()
	clear(s.topDomains.entries)
	s.topDomains.mu.Unlock()
}

func (c *topDomainsCache) get(key topDomainsKey, from, to time.Time, generation uint64) (TopDomains, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.generation != generation || time.Since(e.computedAt) > topDomainsReuse ||
		absDuration(e.from.Sub(from)) > topDomainsReuse || absDuration(e.to.Sub(to)) > topDomainsReuse {
		return TopDomains{}, false
	}
	return cloneTopDomains(e.result), true
}

func (c *topDomainsCache) put(key topDomainsKey, e topDomainsEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[topDomainsKey]topDomainsEntry)
	}
	if _, ok := c.entries[key]; !ok && len(c.entries) >= topDomainsCacheLimit {
		var oldest topDomainsKey
		var oldestAt time.Time
		for k, v := range c.entries {
			if oldestAt.IsZero() || v.computedAt.Before(oldestAt) {
				oldest, oldestAt = k, v.computedAt
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = e
}

// cachedTopDomains serves a recent ranking or computes one, merging
// concurrent identical requests. Callers get their own copy, since the
// control API fills in ExtendedTTL.
func (s *Store) cachedTopDomains(ctx context.Context, userID string, from, to time.Time, limit int) (TopDomains, error) {
	key := topDomainsKey{userID: userID, limit: limit}
	generation := s.topDomainsGeneration.Load()
	if v, ok := s.topDomains.get(key, from, to, generation); ok {
		return v, nil
	}
	flightKey := fmt.Sprintf("%s\x00%d\x00%d\x00%d", userID, limit, from.UnixNano(), to.UnixNano())
	v, err, _ := s.topDomains.flight.Do(flightKey, func() (any, error) {
		computedAt := time.Now()
		// A computation that does not finish inside the request is not
		// shared: another caller may still want it, so ignore one ctx.
		result, err := s.rankDomains(context.WithoutCancel(ctx), userID, from, to, limit)
		if err != nil {
			return nil, err
		}
		s.topDomains.put(key, topDomainsEntry{from: from, to: to, computedAt: computedAt, generation: generation, result: result})
		return result, nil
	})
	if err != nil {
		return TopDomains{}, err
	}
	return cloneTopDomains(v.(TopDomains)), nil
}

func cloneTopDomains(v TopDomains) TopDomains {
	v.Domains = append([]DomainStats{}, v.Domains...)
	return v
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
