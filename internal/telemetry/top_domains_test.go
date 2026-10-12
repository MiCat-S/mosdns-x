package telemetry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func observeNames(s *Store, user string, names ...string) {
	for _, name := range names {
		r := result(user, "c-"+user, dns.RcodeSuccess)
		r.QuestionName = name
		r.CacheHit = name == "cached.example."
		s.Observe(r)
	}
}

func TestTopDomainsRanksTheQueryLog(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := openTestStore(t, true, now)
	observeNames(s, "u1", "a.example.", "A.Example.", "cached.example.", "cached.example.", "b.example.")
	observeNames(s, "u2", "cached.example.", "b.example.", "c.example.")
	flush(t, s)
	from, to := now.Add(-time.Hour), now.Add(time.Minute)

	global, err := s.TopDomains(context.Background(), "", from, to, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []DomainStats{{Name: "cached.example.", Queries: 3, CacheHits: 3}, {Name: "a.example.", Queries: 2, CacheHits: 0}, {Name: "b.example.", Queries: 2, CacheHits: 0}}
	if !reflect.DeepEqual(global.Domains, want) || global.Queries != 8 || !global.QueryLogEnabled || !global.From.Equal(from) {
		t.Fatalf("global = %+v", global)
	}
	u2, err := s.TopDomains(context.Background(), "u2", from, to, 15)
	if err != nil {
		t.Fatal(err)
	}
	want = []DomainStats{{Name: "b.example.", Queries: 1, CacheHits: 0}, {Name: "c.example.", Queries: 1, CacheHits: 0}, {Name: "cached.example.", Queries: 1, CacheHits: 1}}
	if !reflect.DeepEqual(u2.Domains, want) || u2.Queries != 3 {
		t.Fatalf("u2 = %+v", u2)
	}
	later, err := s.TopDomains(context.Background(), "", now.Add(time.Second), to, 15)
	if err != nil || len(later.Domains) != 0 || later.Domains == nil || later.Queries != 0 {
		t.Fatalf("window after the queries = %+v err=%v", later, err)
	}
}

func TestTopDomainsStartsAtTheQueryRetention(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := openTestStore(t, true, now)
	observeNames(s, "u1", "a.example.")
	flush(t, s)
	got, err := s.TopDomains(context.Background(), "", now.Add(-7*24*time.Hour), now.Add(time.Minute), 15)
	if err != nil {
		t.Fatal(err)
	}
	if !got.From.Equal(now.Add(-queryRetention)) || len(got.Domains) != 1 {
		t.Fatalf("got = %+v", got)
	}
}

func TestTopDomainsWithoutQueryLogOrValidLimit(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	off := openTestStore(t, false, now)
	observeNames(off, "u1", "a.example.")
	flush(t, off)
	got, err := off.TopDomains(context.Background(), "", now.Add(-time.Hour), now, 15)
	if err != nil || got.QueryLogEnabled || got.Domains == nil || len(got.Domains) != 0 {
		t.Fatalf("query log off = %+v err=%v", got, err)
	}
	for _, limit := range []int{0, MaxTopDomains + 1} {
		if _, err := off.TopDomains(context.Background(), "", now.Add(-time.Hour), now, limit); err == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	if _, err := off.TopDomains(context.Background(), "", now, now.Add(-time.Hour), 15); err == nil {
		t.Fatal("accepted an inverted range")
	}
}

func TestTopDomainsReusesARecentRankingUntilInvalidated(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := openTestStore(t, true, now)
	observeNames(s, "u1", "a.example.")
	flush(t, s)
	from, to := now.Add(-time.Hour), now.Add(time.Minute)
	first, err := s.TopDomains(context.Background(), "u1", from, to, 15)
	if err != nil || len(first.Domains) != 1 {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	// Callers own their copy.
	first.Domains[0].ExtendedTTL = 99

	observeNames(s, "u1", "b.example.")
	flush(t, s)
	// A window a second later is served from the cache, with its window.
	reused, err := s.TopDomains(context.Background(), "u1", from.Add(time.Second), to.Add(time.Second), 15)
	if err != nil || len(reused.Domains) != 1 || reused.Domains[0].ExtendedTTL != 0 || !reused.To.Equal(to) {
		t.Fatalf("reused = %+v err=%v", reused, err)
	}
	// Another user or limit is never served from it.
	if other, _ := s.TopDomains(context.Background(), "u2", from, to, 15); len(other.Domains) != 0 {
		t.Fatalf("u2 = %+v", other)
	}
	if fresh, _ := s.TopDomains(context.Background(), "u1", from, to, 14); len(fresh.Domains) != 2 {
		t.Fatalf("limit 14 = %+v", fresh)
	}
	// A distant window is computed afresh.
	if fresh, _ := s.TopDomains(context.Background(), "u1", from.Add(-time.Minute), to, 15); len(fresh.Domains) != 2 {
		t.Fatalf("wider window = %+v", fresh)
	}
	if err := s.DeleteUser(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if erased, _ := s.TopDomains(context.Background(), "u1", from, to, 14); len(erased.Domains) != 0 {
		t.Fatalf("after erase = %+v", erased)
	}
}

func TestTopDomainStatsMatchesAFullSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	counts := make(map[string]*DomainStats)
	for i := range 500 {
		name := fmt.Sprintf("d%03d.example.", i)
		counts[name] = &DomainStats{Name: name, Queries: uint64(rng.IntN(20))}
	}
	all := make([]DomainStats, 0, len(counts))
	for _, d := range counts {
		all = append(all, *d)
	}
	sort.Slice(all, func(i, j int) bool { return ranksBefore(&all[i], &all[j]) })
	for _, limit := range []int{1, 7, 100, 500, 600} {
		want := all[:min(limit, len(all))]
		if got := topDomainStats(counts, limit); !reflect.DeepEqual(got, want) {
			t.Fatalf("limit %d: got %v", limit, got[:min(5, len(got))])
		}
	}
}

// BenchmarkRankDomains measures the bbolt ranking without the result cache:
// 10,000 logged queries over 1,000 names, top 100.
func BenchmarkRankDomains(b *testing.B) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := openTestStore(b, true, now)
	for i := range 10000 {
		r := result("u1", "c1", dns.RcodeSuccess)
		r.QuestionName = fmt.Sprintf("d%d.example.", i%1000)
		s.Observe(r)
		if i%50 == 49 {
			flush(b, s)
		}
	}
	flush(b, s)
	from, to := now.Add(-time.Hour), now.Add(time.Minute)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.rankDomains(context.Background(), "", from, to, MaxTopDomains); err != nil {
			b.Fatal(err)
		}
	}
}

// A request whose caller has gone neither computes nor caches a ranking,
// and leaves no flight behind for the next request to join.
func TestTopDomainsCanceledRequestIsNotShared(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := openTestStore(t, true, now)
	observeNames(s, "u1", "a.example.")
	flush(t, s)
	from, to := now.Add(-time.Hour), now.Add(time.Minute)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.TopDomains(canceled, "u1", from, to, 15); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.topDomains.mu.Lock()
		n := len(s.topDomains.flights)
		s.topDomains.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("flight left behind")
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, err := s.TopDomains(context.Background(), "u1", from, to, 15)
	if err != nil || len(got.Domains) != 1 {
		t.Fatalf("after cancel = %+v err=%v", got, err)
	}
}

// When the only caller leaves mid-scan it gets its context error at once,
// and the flight is dropped so a later request starts afresh.
func TestTopDomainsAbandonedScanIsDropped(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := openTestStore(t, true, now)
	for i := range 5000 {
		r := result("u1", "c1", dns.RcodeSuccess)
		r.QuestionName = fmt.Sprintf("d%d.example.", i%500)
		s.Observe(r)
		if i%50 == 49 {
			flush(t, s)
		}
	}
	flush(t, s)
	from, to := now.Add(-time.Hour), now.Add(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := s.TopDomains(ctx, "", from, to, 15); !errors.Is(err, context.DeadlineExceeded) {
		t.Skipf("scan finished before the deadline: %v", err)
	}
	s.topDomains.mu.Lock()
	n := len(s.topDomains.flights)
	s.topDomains.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d flights left after the caller left", n)
	}
	got, err := s.TopDomains(context.Background(), "", from, to, 15)
	if err != nil || len(got.Domains) != 15 || got.Queries != 5000 {
		t.Fatalf("fresh request = %d domains, %d queries, err=%v", len(got.Domains), got.Queries, err)
	}
}
