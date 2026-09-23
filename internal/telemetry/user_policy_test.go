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

func TestOptedOutUserKeepsAggregatesButNoRecords(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	s := openClocked(t, &now, 0)
	s.SetUserLogPolicy(fakeLogPolicy{"private": {enabled: false}})
	s.Observe(result("private", "c", dns.RcodeSuccess))
	s.Observe(result("open", "c", dns.RcodeSuccess))
	flush(t, s)
	if got := userRecordCount(t, s, "private"); got != 0 {
		t.Fatalf("opted-out user has %d records", got)
	}
	if got := userRecordCount(t, s, "open"); got != 1 {
		t.Fatalf("logging user has %d records", got)
	}
	snap, err := s.Snapshot(context.Background(), "private", now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || snap.Completed != 1 {
		t.Fatalf("opted-out user lost aggregates: %+v err=%v", snap, err)
	}
}

// A user who chose a longer retention keeps records the default would drop;
// a user on the default loses them.
func TestPerUserRetentionOutlivesTheDefault(t *testing.T) {
	clock := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	s := openClocked(t, &clock, 0)
	s.SetUserLogPolicy(fakeLogPolicy{"keeper": {enabled: true, retention: 168 * time.Hour}})
	s.Observe(result("keeper", "c", dns.RcodeSuccess))
	s.Observe(result("default", "c", dns.RcodeSuccess))
	flush(t, s)

	clock = clock.Add(48 * time.Hour) // past the 24h default, inside 168h
	s.Observe(result("trigger", "c", dns.RcodeSuccess))
	flush(t, s)
	if got := userRecordCount(t, s, "keeper"); got != 1 {
		t.Fatalf("keeper lost a 48h-old record under 168h retention: %d", got)
	}
	if got := userRecordCount(t, s, "default"); got != 0 {
		t.Fatalf("default user kept a 48h-old record under 24h retention: %d", got)
	}
}

// A user whose choice cannot be read keeps their records this sweep rather
// than being cut to a default they did not pick.
func TestUnreadableRetentionDefersPruning(t *testing.T) {
	clock := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	s := openClocked(t, &clock, 0)
	policy := fakeLogPolicy{}
	s.SetUserLogPolicy(policy)
	s.Observe(result("flaky", "c", dns.RcodeSuccess))
	flush(t, s)
	policy["flaky"] = struct {
		enabled   bool
		retention time.Duration
		err       error
	}{enabled: true, err: context.DeadlineExceeded}
	clock = clock.Add(48 * time.Hour)
	s.Observe(result("trigger", "c", dns.RcodeSuccess))
	flush(t, s)
	// Checked in storage, not through Queries: a query for a user whose choice
	// is unreadable falls back to the default window for display, which is a
	// separate, transient matter from whether the record was deleted.
	if err := s.db.View(func(tx *bolt.Tx) error {
		if got := userQueryCount(tx, "flaky"); got != 1 {
			t.Fatalf("records pruned despite an unreadable choice: %d", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
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

// A user who keeps records longer must be able to see them. Clamping queries
// to the server default hid stored records and made the choice invisible.
func TestLongerRetentionIsVisibleToItsOwner(t *testing.T) {
	clock := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	s := openClocked(t, &clock, 0)
	s.SetUserLogPolicy(fakeLogPolicy{"keeper": {enabled: true, retention: 168 * time.Hour}})
	s.Observe(result("keeper", "c", dns.RcodeSuccess))
	flush(t, s)
	clock = clock.Add(48 * time.Hour)
	if got := userRecordCount(t, s, "keeper"); got != 1 {
		t.Fatalf("a stored 48h-old record is hidden under 168h retention: %d", got)
	}
	// The administrator's all-users view reaches it too.
	if got := userRecordCount(t, s, ""); got != 1 {
		t.Fatalf("admin view hid the record: %d", got)
	}
}
