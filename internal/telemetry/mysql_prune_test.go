package telemetry

import (
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectTelemetryExecutions(mock sqlmock.Sqlmock, query string, count int) {
	for range count {
		mock.ExpectExec(regexp.QuoteMeta(query)).WillReturnResult(sqlmock.NewResult(1, 1))
	}
}

// The minute sweep enforces the query retention and then the total cap,
// taking only from the user holding the most records.
func TestWriteMySQLBatchPrunesByRetentionThenFairly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, queryRetention: queryRetention, maxQueryRecords: 1000, now: func() time.Time { return now }}

	mock.ExpectBegin()
	for _, table := range []string{"mosdns_telemetry_minutes", "mosdns_telemetry_rcodes", "mosdns_telemetry_latency", "mosdns_telemetry_upstreams"} {
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM ` + table + ` WHERE minute_epoch<?`)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_query_logs WHERE time_ns<?`)).
		WithArgs(now.Add(-queryRetention).UnixNano()).WillReturnResult(sqlmock.NewResult(0, 0))
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
