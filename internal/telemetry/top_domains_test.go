package telemetry

import (
	"context"
	"reflect"
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
	want := []DomainStats{{"cached.example.", 3, 3}, {"a.example.", 2, 0}, {"b.example.", 2, 0}}
	if !reflect.DeepEqual(global.Domains, want) || global.Queries != 8 || !global.QueryLogEnabled || !global.From.Equal(from) {
		t.Fatalf("global = %+v", global)
	}
	u2, err := s.TopDomains(context.Background(), "u2", from, to, 15)
	if err != nil {
		t.Fatal(err)
	}
	want = []DomainStats{{"b.example.", 1, 0}, {"c.example.", 1, 0}, {"cached.example.", 1, 1}}
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
