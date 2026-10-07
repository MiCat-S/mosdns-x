package telemetry

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
	bolt "go.etcd.io/bbolt"
)

func userRecordCount(t *testing.T, s *Store, userID string) int {
	t.Helper()
	// Within the 31-day query range limit, reaching back past any retention used here.
	now := s.now()
	page, err := s.Queries(context.Background(), userID, now.Add(-30*24*time.Hour), now.Add(time.Minute), QueryFilter{}, Page{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Items)
}

func openClocked(t *testing.T, clock *time.Time, max int) *Store {
	t.Helper()
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "t.db"), QueryLogEnabled: true, BatchSize: 1,
		MaxQueryRecords: max, Now: func() time.Time { return *clock }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Over the cap, records come from the largest holder, so a light user keeps
// their whole history.
func TestCapEvictsFromTheLargestUser(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "t.db"), QueryLogEnabled: true, BatchSize: 256,
		QueueSize: 4096, MaxQueryRecords: 1000, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	longFlush := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		s.Observe(result("light", "c", dns.RcodeSuccess))
	}
	longFlush()
	for i := 0; i < 1100; i++ {
		s.Observe(result("heavy", "c", dns.RcodeSuccess))
	}
	longFlush()
	if got := userRecordCount(t, s, "light"); got != 5 {
		t.Fatalf("light user lost records to another user's volume: %d", got)
	}
	var total uint64
	if err := s.db.View(func(tx *bolt.Tx) error { total = metaUint64(tx, keyQueryCount); return nil }); err != nil {
		t.Fatal(err)
	}
	if total != 1000 {
		t.Fatalf("total = %d, want the cap of 1000", total)
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		if got := userQueryCount(tx, "heavy"); got != 995 {
			t.Fatalf("heavy count = %d, want 995", got)
		}
		if got := userQueryCount(tx, "light"); got != 5 {
			t.Fatalf("light count = %d, want 5", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A database written before per-user counts existed gets them rebuilt on
// open, or fair eviction would see every user as empty.
func TestUserCountsRebuiltForExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	s, err := Open(Options{Path: path, QueryLogEnabled: true, BatchSize: 1, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s.Observe(result("a", "c", dns.RcodeSuccess))
	}
	s.Observe(result("b", "c", dns.RcodeSuccess))
	flush(t, s)
	// Simulate the previous release: no counts bucket, no built flag.
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketUserQueryCounts); err != nil {
			return err
		}
		return tx.Bucket(bucketMeta).Delete(keyUserCountsBuilt)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(Options{Path: path, QueryLogEnabled: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.db.View(func(tx *bolt.Tx) error {
		if a, b := userQueryCount(tx, "a"), userQueryCount(tx, "b"); a != 3 || b != 1 {
			t.Fatalf("rebuilt counts a=%d b=%d, want 3 and 1", a, b)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// bbolt rejects a zero-length key. A result without a user must not fail the
// batch, which would drop every other user's data in it.
func TestResultWithoutUserDoesNotFailTheBatch(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	s := openClocked(t, &now, 0)
	err := s.writeBatch([]event{
		{time: now, result: ptrResult(result("", "c", dns.RcodeSuccess))},
		{time: now, result: ptrResult(result("alice", "c", dns.RcodeSuccess))},
	})
	if err != nil {
		t.Fatalf("batch failed: %v", err)
	}
	if got := userRecordCount(t, s, "alice"); got != 1 {
		t.Fatalf("alice's record lost alongside the userless one: %d", got)
	}
}

// After a rollback, the older release keeps the total but not the per-user
// counts. Eviction must notice and recount, or the cap stops being enforced.
func TestEvictionRecoversFromDriftedCounts(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "t.db"), QueryLogEnabled: true, BatchSize: 256,
		QueueSize: 4096, MaxQueryRecords: 1000, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	events := func(user string, n int) []event {
		out := make([]event, n)
		for i := range out {
			out[i] = event{time: now, result: ptrResult(result(user, "c", dns.RcodeSuccess))}
		}
		return out
	}
	if err := s.writeBatch(append(events("light", 5), events("heavy", 900)...)); err != nil {
		t.Fatal(err)
	}
	// Simulate the older release: counts wiped, total still accurate.
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketUserQueryCounts); err != nil {
			return err
		}
		_, err := tx.CreateBucket(bucketUserQueryCounts)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.writeBatch(events("heavy", 200)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		if got := metaUint64(tx, keyQueryCount); got != 1000 {
			t.Fatalf("total = %d, want the cap of 1000 enforced", got)
		}
		if got := tx.Bucket(bucketQueries).Stats().KeyN; got != 1000 {
			t.Fatalf("records = %d, want 1000", got)
		}
		if got := userQueryCount(tx, "light"); got != 5 {
			t.Fatalf("light = %d, want 5 kept", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
