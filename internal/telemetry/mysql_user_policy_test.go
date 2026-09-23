package telemetry

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

type fakeLogPolicy map[string]struct {
	enabled   bool
	retention time.Duration
	err       error
}

func (f fakeLogPolicy) QueryLogFor(_ context.Context, userID string) (bool, time.Duration, error) {
	c, ok := f[userID]
	if !ok {
		return true, 0, nil
	}
	return c.enabled, c.retention, c.err
}

func queryResult(now time.Time, userID string) event {
	return event{time: now, result: &dns_handler.Result{
		Admitted: true, Principal: query_context.Principal{UserID: userID, CredentialID: "c"},
		QuestionName: "example.test.", QuestionType: dns.TypeA, Rcode: dns.RcodeSuccess,
	}}
}

func expectAggregates(mock sqlmock.Sqlmock) {
	for _, table := range []string{"mosdns_telemetry_minutes", "mosdns_telemetry_rcodes", "mosdns_telemetry_latency"} {
		expectTelemetryExecutions(mock, `INSERT INTO `+table, 2)
	}
}

// A user who turned logging off keeps their usage aggregates, which quota and
// charts need, but gets no detailed record.
func TestWriteMySQLBatchSkipsDetailForOptedOutUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, now: func() time.Time { return now }}
	store.mysqlPrunedAt.Store(now.Truncate(time.Minute).Unix())
	store.SetUserLogPolicy(fakeLogPolicy{"private": {enabled: false}})

	mock.ExpectBegin()
	expectAggregates(mock)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.writeMySQLBatch([]event{queryResult(now, "private")}); err != nil {
		t.Fatal(err)
	}
	// sqlmock fails on the unexpected INSERT INTO mosdns_query_logs.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// When a user's choice cannot be read, the record is not written: the user
// may have turned logging off.
func TestWriteMySQLBatchFailsClosedWhenChoiceUnreadable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, now: func() time.Time { return now }}
	store.mysqlPrunedAt.Store(now.Truncate(time.Minute).Unix())
	store.SetUserLogPolicy(fakeLogPolicy{"u": {enabled: true, err: errors.New("control store unavailable")}})

	mock.ExpectBegin()
	expectAggregates(mock)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.writeMySQLBatch([]event{queryResult(now, "u")}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The minute sweep enforces the ceiling, each user's own cutoff, and then the
// total cap, taking only from the user holding the most records.
func TestWriteMySQLBatchPrunesPerUserThenFairly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, maxQueryRecords: 1000, now: func() time.Time { return now }}
	store.SetUserLogPolicy(fakeLogPolicy{
		"keeper": {enabled: true, retention: 168 * time.Hour},
		"flaky":  {enabled: true, err: errors.New("unavailable")},
	})

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT user_id FROM mosdns_query_logs`)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow("keeper").AddRow("default").AddRow("flaky"))
	mock.ExpectBegin()
	for _, table := range []string{"mosdns_telemetry_minutes", "mosdns_telemetry_rcodes", "mosdns_telemetry_latency", "mosdns_telemetry_upstreams"} {
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM ` + table + ` WHERE minute_epoch<?`)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_query_logs WHERE time_ns<?`)).
		WithArgs(now.Add(-maxQueryRetention).UnixNano()).WillReturnResult(sqlmock.NewResult(0, 0))
	// keeper keeps 168h, default follows the 24h server default, and flaky,
	// whose choice is unreadable, is left for the next minute.
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_query_logs WHERE user_id=? AND time_ns<?`)).
		WithArgs("keeper", now.Add(-168*time.Hour).UnixNano()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_query_logs WHERE user_id=? AND time_ns<?`)).
		WithArgs("default", now.Add(-queryRetention).UnixNano()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM mosdns_query_logs`)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1100))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, COUNT(*) FROM mosdns_query_logs GROUP BY user_id`)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "count"}).AddRow("keeper", 60).AddRow("default", 1000).AddRow("flaky", 40))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_query_logs WHERE user_id=? ORDER BY time_ns, id LIMIT ?`)).
		WithArgs("default", uint64(100)).WillReturnResult(sqlmock.NewResult(0, 100))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.writeMySQLBatch(nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := store.mysqlPrunedAt.Load(); got != now.Truncate(time.Minute).Unix() {
		t.Fatalf("prune minute not recorded: %d", got)
	}
}
