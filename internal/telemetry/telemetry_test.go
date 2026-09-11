package telemetry

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	bolt "go.etcd.io/bbolt"

	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

func openTestStore(t *testing.T, queryLog bool, now time.Time) *Store {
	t.Helper()
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "telemetry.db"), QueueSize: 64, BatchSize: 64, FlushInterval: time.Hour, QueryLogEnabled: queryLog, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func flush(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func result(user, credential string, rcode int) dns_handler.Result {
	return dns_handler.Result{Principal: query_context.Principal{UserID: user, CredentialID: credential}, QuestionName: "example.org.", QuestionType: dns.TypeA, Protocol: query_context.ProtocolH3, Duration: 10 * time.Millisecond, Rcode: rcode, Admitted: true}
}

func TestSnapshotClassificationAndUserIsolation(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 34, 0, 0, time.UTC)
	s := openTestStore(t, true, now)
	r := result("u1", "c1", dns.RcodeSuccess)
	r.CacheHit = true
	s.Observe(r)
	s.Observe(result("u1", "c1", dns.RcodeNameError))
	s.Observe(result("u2", "c2", dns.RcodeServerFailure))
	rejected := result("u1", "c1", dns.RcodeSuccess)
	rejected.Admitted = false
	s.Observe(rejected)
	s.ObserveUpstream(query_context.UpstreamAttempt{Principal: r.Principal, UpstreamID: "ff/0", Duration: 5 * time.Millisecond, Rcode: dns.RcodeNameError})
	s.ObserveUpstream(query_context.UpstreamAttempt{Principal: r.Principal, UpstreamID: "ff/1", Duration: 7 * time.Millisecond, Rcode: -1, Failed: true})
	flush(t, s)
	from, to := now.Add(-time.Minute), now.Add(time.Minute)
	global, err := s.Snapshot(context.Background(), "", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if global.Completed != 3 || global.Failed != 1 || global.CacheHits != 1 || global.RcodeCounts["NXDOMAIN"] != 1 || len(global.Upstreams) != 2 {
		t.Fatalf("global=%#v", global)
	}
	u1, err := s.Snapshot(context.Background(), "u1", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if u1.Completed != 2 || u1.Failed != 0 || u1.CacheHits != 1 || len(u1.Upstreams) != 2 {
		t.Fatalf("u1=%#v", u1)
	}
	u2, err := s.Snapshot(context.Background(), "u2", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if u2.Completed != 1 || u2.Failed != 1 || len(u2.Upstreams) != 0 {
		t.Fatalf("u2=%#v", u2)
	}
}

func TestQueriesEnabledDisabledAndPagination(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	off := openTestStore(t, false, now)
	off.Observe(result("u1", "c1", 0))
	flush(t, off)
	p, err := off.Queries(context.Background(), "u1", now.Add(-time.Minute), now.Add(time.Minute), Page{})
	if err != nil || p.Items == nil || len(p.Items) != 0 {
		t.Fatalf("off=%#v err=%v", p, err)
	}
	on := openTestStore(t, true, now)
	on.Observe(result("u1", "c1", 0))
	on.Observe(result("u1", "c2", 3))
	on.Observe(result("u2", "c3", 0))
	flush(t, on)
	p, err = on.Queries(context.Background(), "u1", now.Add(-time.Minute), now.Add(time.Minute), Page{Limit: 1})
	if err != nil || len(p.Items) != 1 || p.NextCursor == "" {
		t.Fatalf("page1=%#v err=%v", p, err)
	}
	p2, err := on.Queries(context.Background(), "u1", now.Add(-time.Minute), now.Add(time.Minute), Page{Limit: 1, Cursor: p.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.Items[0].CredentialID == p.Items[0].CredentialID {
		t.Fatalf("page2=%#v err=%v", p2, err)
	}
	all, err := on.Queries(context.Background(), "", now.Add(-time.Minute), now.Add(time.Minute), Page{})
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("all=%#v err=%v", all, err)
	}
	if _, err = on.Queries(context.Background(), "", now.Add(-32*24*time.Hour), now, Page{}); err == nil {
		t.Fatal("accepted range over 31 days")
	}
}

func TestQueueFullAndClosedCallbacksDropWithoutPanic(t *testing.T) {
	now := time.Now().UTC()
	s := &Store{queue: make(chan event, 1), droppedByWindow: make(map[string]uint64)}
	s.enqueue(event{result: ptrResult(result("u1", "c", 0)), time: now})
	s.enqueue(event{result: ptrResult(result("u1", "c", 0)), time: now})
	if s.dropped.Load() != 1 {
		t.Fatalf("dropped=%d", s.dropped.Load())
	}
	if s.droppedFor("u1", now.Add(-time.Minute), now.Add(time.Minute)) != 1 || s.droppedFor("u2", now.Add(-time.Minute), now.Add(time.Minute)) != 0 {
		t.Fatal("dropped leaked across users")
	}

	live := openTestStore(t, false, now)
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			live.Observe(result("u", "c", 0))
			live.ObserveUpstream(query_context.UpstreamAttempt{})
		}()
	}
	wg.Wait()
	if live.dropped.Load() != 200 {
		t.Fatalf("late dropped=%d", live.dropped.Load())
	}
}

func TestWriteFailureIncreasesDropped(t *testing.T) {
	now := time.Now().UTC()
	s := openTestStore(t, false, now)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.Observe(result("u1", "c", 0))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Flush(ctx); err == nil {
		t.Fatal("Flush succeeded after database close")
	}
	if s.droppedFor("u1", now.Add(-time.Minute), now.Add(time.Minute)) != 1 {
		t.Fatalf("dropped=%d", s.droppedFor("u1", now.Add(-time.Minute), now.Add(time.Minute)))
	}
}

func TestCloseReportsFinalFlushFailure(t *testing.T) {
	now := time.Now().UTC()
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "close.db"), Now: func() time.Time { return now }, BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	s.Observe(result("u", "c", 0))
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	first := s.Close()
	if first == nil {
		t.Fatal("Close hid final flush failure")
	}
	second := s.Close()
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("unstable repeated Close: first=%v second=%v", first, second)
	}
}

func TestDroppedWindowPrunesOncePerMinute(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	s := &Store{droppedByWindow: make(map[string]uint64)}
	old := now.Add(-aggregateRetention - time.Minute)
	boundary := now.Add(-aggregateRetention)
	s.addDropped("u", old)
	s.addDropped("u", boundary)
	s.addDropped("u", now)
	if got := s.droppedFor("u", old.Add(-time.Minute), old.Add(time.Minute)); got != 0 {
		t.Fatalf("expired dropped retained: %d", got)
	}
	if got := s.droppedFor("u", boundary, boundary.Add(time.Minute)); got != 1 {
		t.Fatalf("boundary dropped pruned: %d", got)
	}
	before := len(s.droppedByWindow)
	s.addDropped("u", now.Add(30*time.Second))
	if len(s.droppedByWindow) != before {
		t.Fatal("same-minute add unexpectedly pruned map")
	}
}

func TestCloseConcurrentWithCallbacks(t *testing.T) {
	now := time.Now().UTC()
	s := openTestStore(t, false, now)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Observe(result("u", "c", 0))
			}
		}()
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func ptrResult(r dns_handler.Result) *dns_handler.Result { return &r }

func TestRetentionPrunesOldAggregatesAndQueries(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := now.Add(-8 * 24 * time.Hour)
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "t.db"), QueryLogEnabled: true, Now: func() time.Time { return clock }, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Observe(result("u", "old", 0))
	flush(t, s)
	clock = now
	s.Observe(result("u", "new", 0))
	flush(t, s)
	snap, err := s.Snapshot(context.Background(), "", now.Add(-30*24*time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Completed != 1 {
		t.Fatalf("completed=%d", snap.Completed)
	}
	queries, err := s.Queries(context.Background(), "", now.Add(-30*24*time.Hour), now.Add(time.Minute), Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries.Items) != 1 || queries.Items[0].CredentialID != "new" || queries.Items[0].QType != "A" || queries.Items[0].Rcode != "NOERROR" {
		t.Fatalf("queries=%#v", queries)
	}
}

func TestEventTimeAndUpdatedAtPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	eventTime := time.Date(2026, 9, 11, 12, 0, 30, 0, time.UTC)
	clock := eventTime
	s, err := Open(Options{Path: path, Now: func() time.Time { return clock }, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s.Observe(result("u", "c", 0))
	clock = eventTime.Add(2 * time.Minute)
	flush(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Path: path, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.Snapshot(context.Background(), "", eventTime.Truncate(time.Minute), eventTime.Truncate(time.Minute).Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Completed != 1 {
		t.Fatalf("event assigned to flush minute: %#v", snap)
	}
	if !snap.UpdatedAt.Equal(clock) {
		t.Fatalf("updated_at=%s want %s", snap.UpdatedAt, clock)
	}
}

func TestQueryCountInitializesOnceAndSurvivesPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "count.db")
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := now.Add(-2 * queryRetention)
	s, err := Open(Options{Path: path, Now: func() time.Time { return clock }, QueryLogEnabled: true, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	s.Observe(result("u", "old", 0))
	flush(t, s)
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketMeta).Delete(keyQueryCount) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Path: path, Now: func() time.Time { return clock }, QueryLogEnabled: true, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		if got := metaUint64(tx, keyQueryCount); got != 1 {
			t.Fatalf("initialized count=%d", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock = now
	s.Observe(result("u", "new", 0))
	flush(t, s)
	if err := s.db.View(func(tx *bolt.Tx) error {
		if got := metaUint64(tx, keyQueryCount); got != 1 {
			t.Fatalf("count after prune=%d", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Path: path, Now: func() time.Time { return clock }, QueryLogEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.db.View(func(tx *bolt.Tx) error {
		if got := metaUint64(tx, keyQueryCount); got != 1 {
			t.Fatalf("reopened count=%d", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
