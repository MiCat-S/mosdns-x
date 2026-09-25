package telemetry

import (
	"bytes"
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/miekg/dns"
	bolt "go.etcd.io/bbolt"

	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func TestDeleteUserErasesRecordsAndAggregates(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	s := openClocked(t, &now, 0)
	for _, user := range []string{"gone", "gone", "kept"} {
		r := result(user, "c", dns.RcodeSuccess)
		s.Observe(r)
		s.ObserveUpstream(query_context.UpstreamAttempt{Principal: r.Principal, UpstreamID: "up/0", Duration: time.Millisecond})
	}
	flush(t, s)
	s.addDropped("gone", now)
	s.addDropped("kept", now)

	if err := s.DeleteUser(context.Background(), "gone"); err != nil {
		t.Fatal(err)
	}
	if got := userRecordCount(t, s, "gone"); got != 0 {
		t.Fatalf("deleted user still has %d records", got)
	}
	if got := userRecordCount(t, s, "kept"); got != 1 {
		t.Fatalf("other user has %d records", got)
	}
	from, to := now.Add(-time.Hour), now.Add(time.Minute)
	gone, err := s.Snapshot(context.Background(), "gone", from, to)
	if err != nil || gone.Completed != 0 || len(gone.Upstreams) != 0 || gone.Dropped != 0 {
		t.Fatalf("deleted user snapshot=%+v err=%v", gone, err)
	}
	kept, err := s.Snapshot(context.Background(), "kept", from, to)
	if err != nil || kept.Completed != 1 || len(kept.Upstreams) != 1 || kept.Dropped != 1 {
		t.Fatalf("other user snapshot=%+v err=%v", kept, err)
	}
	// Server-wide totals still count the deleted user's queries; they hold
	// no user identifier.
	global, err := s.Snapshot(context.Background(), "", from, to)
	if err != nil || global.Completed != 3 {
		t.Fatalf("global snapshot=%+v err=%v", global, err)
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		if n := metaUint64(tx, keyQueryCount); n != 1 {
			t.Errorf("query count=%d", n)
		}
		if n := userQueryCount(tx, "gone"); n != 0 {
			t.Errorf("deleted user count=%d", n)
		}
		for _, name := range [][]byte{bucketMinutes, bucketUpstream, bucketQueries, bucketUserQ, bucketUserQueryCounts} {
			_ = tx.Bucket(name).ForEach(func(k, v []byte) error {
				if bytes.Contains(k, []byte("gone")) || bytes.Contains(v, []byte(`"gone"`)) {
					t.Errorf("bucket %s still holds %q", name, k)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMySQLDeleteUserErasesEveryUserScopedTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{mysql: db, mysqlTimeout: time.Second}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_query_logs WHERE user_id=?`)).WithArgs("gone").WillReturnResult(sqlmock.NewResult(0, 2))
	for _, table := range []string{"minutes", "rcodes", "latency", "upstreams"} {
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_telemetry_` + table + ` WHERE scope_kind='u' AND scope_id=?`)).WithArgs("gone").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	if err := store.mysqlDeleteUser(context.Background(), "gone"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
