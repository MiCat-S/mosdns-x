package telemetry

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func expectTelemetryExecutions(mock sqlmock.Sqlmock, query string, count int) {
	for range count {
		mock.ExpectExec(regexp.QuoteMeta(query)).WillReturnResult(sqlmock.NewResult(1, 1))
	}
}

// The minute sweep runs after the batch commits, in its own transaction: it
// enforces the query retention and then the total cap, taking only from the
// user holding the most records.
func TestWriteMySQLBatchPrunesByRetentionThenFairly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, queryRetention: queryRetention, maxQueryRecords: 1000, now: func() time.Time { return now }}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	for _, table := range []string{"mosdns_telemetry_minutes", "mosdns_telemetry_rcodes", "mosdns_telemetry_latency", "mosdns_telemetry_upstreams"} {
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM ` + table + ` WHERE minute_epoch<?`)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_query_logs WHERE time_ns<? ORDER BY time_ns LIMIT 10000`)).
		WithArgs(now.Add(-queryRetention).UnixNano()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM mosdns_query_logs`)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1100))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, COUNT(*) FROM mosdns_query_logs GROUP BY user_id`)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "count"}).AddRow("keeper", 60).AddRow("default", 1000).AddRow("flaky", 40))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_query_logs WHERE user_id=? ORDER BY time_ns, id LIMIT ?`)).
		WithArgs("default", uint64(100)).WillReturnResult(sqlmock.NewResult(0, 100))
	mock.ExpectCommit()

	if err := store.writeMySQLBatch(nil); err != nil {
		t.Fatal(err)
	}
	store.maintenanceWG.Wait()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := store.mysqlPrunedAt.Load(); got != now.Truncate(time.Minute).Unix() {
		t.Fatalf("prune minute not recorded: %d", got)
	}
}

// A failed prune keeps the committed batch, is logged, and waits for the next
// minute instead of retrying on every flush.
func TestFailedPruneKeepsTheBatchAndWaitsAMinute(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	core, logs := observer.New(zap.WarnLevel)
	now := time.Date(2026, 9, 22, 12, 0, 30, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, queryRetention: queryRetention, maxQueryRecords: 1000, now: func() time.Time { return now }, logger: zap.New(core)}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_telemetry_minutes WHERE minute_epoch<?`)).WillReturnError(errors.New("lock wait timeout"))
	mock.ExpectRollback()
	if err := store.writeMySQLBatch(nil); err != nil {
		t.Fatalf("batch failed with the prune: %v", err)
	}
	store.maintenanceWG.Wait()
	if logs.FilterMessageSnippet("prune failed").Len() != 1 {
		t.Fatalf("logs = %+v", logs.All())
	}

	// The next flush in the same minute writes without pruning again.
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.writeMySQLBatch(nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The sweep runs beside the consumer: a slow prune neither delays the batch
// that started it nor starts a second sweep.
func TestSlowPruneDoesNotBlockTheBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, queryRetention: queryRetention, maxQueryRecords: 1000, now: func() time.Time { return now }}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin().WillDelayFor(300 * time.Millisecond)
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_telemetry_minutes`)).WillReturnError(errors.New("stop here"))
	mock.ExpectRollback()

	start := time.Now()
	if err := store.writeMySQLBatch(nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("batch waited %v for the prune", d)
	}
	if !store.maintenanceBusy.Load() {
		t.Fatal("prune not in flight")
	}
	// A new minute while the sweep is still running does not start another.
	now = now.Add(time.Minute)
	store.startMySQLPrune(now, now.Unix(), store.Settings())
	store.maintenanceWG.Wait()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := store.mysqlPrunedAt.Load(); got != now.Add(-time.Minute).Unix() {
		t.Fatalf("skipped minute recorded as pruned: %d", got)
	}
}

func TestExecMultiRowChunksByRowsAndBytes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows := make([][]any, 0, 130)
	for i := range 130 {
		rows = append(rows, []any{i, "x"})
	}
	// Two oversized rows each get a statement of their own.
	big := string(make([]byte, mysqlMultiRowMaxBytes))
	rows = append(rows, []any{1, big}, []any{2, big})
	mock.ExpectBegin()
	for _, n := range []int{64, 64, 2, 1, 1} {
		mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO t (a, b) VALUES (?, ?)` + strings.Repeat(`, (?, ?)`, n-1) + ` ON DUP`)).
			WillReturnResult(sqlmock.NewResult(0, int64(n)))
	}
	mock.ExpectCommit()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := execMultiRow(context.Background(), tx, `INSERT INTO t (a, b) VALUES `, `(?, ?)`, ` ON DUP`, rows); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A batch chosen as a deadlock victim was rolled back whole, so it runs
// once more instead of dropping its events; other errors are not retried.
func TestWriteMySQLBatchRetriesADeadlockOnce(t *testing.T) {
	for _, c := range []struct {
		name    string
		second  error
		wantErr bool
	}{
		{"recovers", nil, false},
		{"gives up after one retry", &mysqlDriver.MySQLError{Number: 1213}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
			store := &Store{mysql: db, mysqlTimeout: time.Second, now: func() time.Time { return now }}
			store.mysqlPrunedAt.Store(now.Unix())
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnError(&mysqlDriver.MySQLError{Number: 1213, Message: "Deadlock found"})
			mock.ExpectRollback()
			mock.ExpectBegin()
			if c.second == nil {
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			} else {
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta`)).WillReturnError(c.second)
				mock.ExpectRollback()
			}
			if err := store.writeMySQLBatch(nil); (err != nil) != c.wantErr {
				t.Fatalf("err=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if retryableMySQLError(errors.New("connection reset")) || retryableMySQLError(&mysqlDriver.MySQLError{Number: 1062}) {
		t.Fatal("non-lock error treated as retryable")
	}
}
