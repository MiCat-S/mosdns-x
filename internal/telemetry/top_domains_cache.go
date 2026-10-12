package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"
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
	flights map[string]*topDomainsFlight
}

// topDomainsFlight is one ranking being computed for every request that
// asked for it. The computation is canceled once all of them have left.
type topDomainsFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int // guarded by topDomainsCache.mu
	result  TopDomains
	err     error
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
	if err := ctx.Err(); err != nil {
		return TopDomains{}, err
	}
	// The generation is part of the flight key, so a request made after an
	// invalidation never joins a computation that started before it.
	flightKey := fmt.Sprintf("%d\x00%s\x00%d\x00%d\x00%d", generation, userID, limit, from.UnixNano(), to.UnixNano())
	c := &s.topDomains
	c.mu.Lock()
	f := c.flights[flightKey]
	if f == nil {
		flightCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &topDomainsFlight{done: make(chan struct{}), cancel: cancel}
		if c.flights == nil {
			c.flights = make(map[string]*topDomainsFlight)
		}
		c.flights[flightKey] = f
		go func() {
			defer cancel()
			computedAt := time.Now()
			result, err := s.rankDomains(flightCtx, userID, from, to, limit)
			if err == nil {
				c.put(key, topDomainsEntry{from: from, to: to, computedAt: computedAt, generation: generation, result: result})
			}
			c.mu.Lock()
			if c.flights[flightKey] == f {
				delete(c.flights, flightKey)
			}
			c.mu.Unlock()
			f.result, f.err = result, err
			close(f.done)
		}()
	}
	f.waiters++
	c.mu.Unlock()
	select {
	case <-f.done:
		c.mu.Lock()
		f.waiters--
		c.mu.Unlock()
		if f.err != nil {
			return TopDomains{}, f.err
		}
		return cloneTopDomains(f.result), nil
	case <-ctx.Done():
		c.mu.Lock()
		if f.waiters--; f.waiters == 0 {
			// Nobody wants it any more: stop the scan, and let a later
			// request start afresh rather than join a canceled one.
			f.cancel()
			if c.flights[flightKey] == f {
				delete(c.flights, flightKey)
			}
		}
		c.mu.Unlock()
		return TopDomains{}, ctx.Err()
	}
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
