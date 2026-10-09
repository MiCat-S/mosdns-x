package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	for _, d := range counts {
		result.Domains = append(result.Domains, *d)
	}
	sort.Slice(result.Domains, func(i, j int) bool {
		a, b := result.Domains[i], result.Domains[j]
		if a.Queries != b.Queries {
			return a.Queries > b.Queries
		}
		return a.Name < b.Name
	})
	if len(result.Domains) > limit {
		result.Domains = result.Domains[:limit]
	}
	return result, nil
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
	defer rows.Close()
	for rows.Next() {
		var d DomainStats
		if err := rows.Scan(&d.Name, &d.Queries, &d.CacheHits); err != nil {
			return result, err
		}
		result.Domains = append(result.Domains, d)
	}
	return result, rows.Err()
}
