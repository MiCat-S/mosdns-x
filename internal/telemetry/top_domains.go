package telemetry

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// MaxTopDomains bounds the limit of a TopDomains request.
const MaxTopDomains = 100

// DomainRanker is implemented by stores that can rank query names. It is
// separate from Service so other implementations need not support it.
type DomainRanker interface {
	TopDomains(ctx context.Context, userID string, from, to time.Time, limit int) (TopDomains, error)
}

var _ DomainRanker = (*Store)(nil)

// DomainStats counts the logged queries for one name, matched case-insensitively.
type DomainStats struct {
	Name      string `json:"name"`
	Queries   uint64 `json:"queries"`
	CacheHits uint64 `json:"cache_hits"`
	// ExtendedTTL is the TTL in seconds a plugin such as adaptive_ttl gives
	// the name right now, or 0 when its TTL is not lengthened. The control
	// API fills it in; stores leave it 0.
	ExtendedTTL uint32 `json:"extended_ttl,omitempty"`
}

// TopDomains ranks the names in the query log. From is where the ranking
// starts: the requested start, or the query retention boundary when the log
// does not reach back that far. Queries counts every logged query in the
// window, ranked or not.
type TopDomains struct {
	From            time.Time     `json:"from"`
	To              time.Time     `json:"to"`
	Queries         uint64        `json:"queries"`
	Domains         []DomainStats `json:"domains"`
	QueryLogEnabled bool          `json:"query_log_enabled"`
}

// TopDomains returns the limit most queried names between from and to, most
// queried first and by name on a tie. The ranking reads the query log, so it
// is empty when query logging is off and covers only the retained records.
func (s *Store) TopDomains(ctx context.Context, userID string, from, to time.Time, limit int) (TopDomains, error) {
	result := TopDomains{From: from, To: to, Domains: []DomainStats{}}
	if err := validateRange(from, to); err != nil {
		return result, err
	}
	if limit < 1 || limit > MaxTopDomains {
		return result, fmt.Errorf("limit must be between 1 and %d", MaxTopDomains)
	}
	if !s.Settings().QueryLogEnabled {
		return result, nil
	}
	return s.cachedTopDomains(ctx, userID, from, to, limit)
}

// rankDomains computes the ranking TopDomains returns.
func (s *Store) rankDomains(ctx context.Context, userID string, from, to time.Time, limit int) (TopDomains, error) {
	result := TopDomains{From: from, To: to, Domains: []DomainStats{}}
	settings := s.Settings()
	result.QueryLogEnabled = settings.QueryLogEnabled
	if !settings.QueryLogEnabled {
		return result, nil
	}
	if retainedFrom := s.now().UTC().Add(-settings.QueryRetention); result.From.Before(retainedFrom) {
		result.From = retainedFrom
	}
	if !result.From.Before(to) {
		return result, nil
	}
	if s.mysql != nil {
		return s.mysqlTopDomains(ctx, userID, limit, result)
	}
	counts := make(map[string]*DomainStats)
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketQueries)
		prefix := ""
		if userID != "" {
			b = tx.Bucket(bucketUserQ)
			prefix = userID + "\x00"
		}
		c := b.Cursor()
		// Walk from the newest key before to back to from, as Queries does.
		k, _ := c.Seek([]byte(prefix + fmt.Sprintf("%020d", to.UnixNano())))
		var v []byte
		if k == nil {
			k, v = c.Last()
		} else {
			k, v = c.Prev()
		}
		for ; k != nil && (prefix == "" || strings.HasPrefix(string(k), prefix)); k, v = c.Prev() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			var r struct {
				Time     time.Time `json:"time"`
				Name     string    `json:"name"`
				CacheHit bool      `json:"cache_hit"`
			}
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if r.Time.Before(result.From) {
				break
			}
			if !r.Time.Before(to) {
				continue
			}
			name := strings.ToLower(r.Name)
			d := counts[name]
			if d == nil {
				d = &DomainStats{Name: name}
				counts[name] = d
			}
			d.Queries++
			if r.CacheHit {
				d.CacheHits++
			}
			result.Queries++
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	result.Domains = topDomainStats(counts, limit)
	return result, nil
}

// ranksBefore orders the ranking: most queried first, then by name.
func ranksBefore(a, b *DomainStats) bool {
	if a.Queries != b.Queries {
		return a.Queries > b.Queries
	}
	return a.Name < b.Name
}

// domainHeap is a min-heap on rank: its root is the lowest ranked name kept.
type domainHeap []*DomainStats

func (h domainHeap) Len() int           { return len(h) }
func (h domainHeap) Less(i, j int) bool { return ranksBefore(h[j], h[i]) }
func (h domainHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *domainHeap) Push(x any)        { *h = append(*h, x.(*DomainStats)) }
func (h *domainHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// topDomainStats selects the limit best ranked names in O(D log limit)
// rather than sorting all D names.
func topDomainStats(counts map[string]*DomainStats, limit int) []DomainStats {
	h := make(domainHeap, 0, min(limit, len(counts)))
	for _, d := range counts {
		if len(h) < limit {
			heap.Push(&h, d)
		} else if ranksBefore(d, h[0]) {
			h[0] = d
			heap.Fix(&h, 0)
		}
	}
	out := make([]DomainStats, len(h))
	for i := len(h) - 1; i >= 0; i-- {
		out[i] = *heap.Pop(&h).(*DomainStats)
	}
	return out
}

func (s *Store) mysqlTopDomains(parent context.Context, userID string, limit int, result TopDomains) (TopDomains, error) {
	ctx, cancel := s.mysqlContext(parent)
	defer cancel()
	where := ` FROM mosdns_query_logs WHERE time_ns>=? AND time_ns<?`
	args := []any{result.From.UnixNano(), result.To.UnixNano()}
	if userID != "" {
		where += ` AND user_id=?`
		args = append(args, userID)
	}
	if err := s.mysql.QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&result.Queries); err != nil {
		return result, err
	}
	rows, err := s.mysql.QueryContext(ctx, `SELECT LOWER(name) AS domain, COUNT(*) AS queries, SUM(cache_hit)`+where+
		` GROUP BY domain ORDER BY queries DESC, domain LIMIT ?`, append(args, limit)...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var d DomainStats
		if err := rows.Scan(&d.Name, &d.Queries, &d.CacheHits); err != nil {
			_ = rows.Close()
			return result, err
		}
		result.Domains = append(result.Domains, d)
	}
	return result, finishRows(rows)
}
