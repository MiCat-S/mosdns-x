package telemetry

import (
	"context"
	"net/netip"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

func TestMigrateBoltToMySQLPreservesTelemetryRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "stats.db")
	at := time.Date(2026, 9, 11, 12, 34, 0, 0, time.UTC)
	source, err := Open(Options{Path: path, QueryLogEnabled: true, Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	principal := query_context.Principal{UserID: "user-1", CredentialID: "credential-1", CredentialVersion: 1}
	source.Observe(dns_handler.Result{Admitted: true, Principal: principal, ClientAddr: netip.MustParseAddr("192.0.2.1"), QuestionName: "example.test.", QuestionType: dns.TypeA, Rcode: dns.RcodeSuccess, Duration: 8 * time.Millisecond, Protocol: "doh", AnswerIPs: []string{"192.0.2.2"}})
	source.ObserveUpstream(query_context.UpstreamAttempt{Principal: principal, UpstreamID: "upstream-1", Duration: 4 * time.Millisecond})
	if err := source.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	destination := &Store{mysql: db, mysqlTimeout: time.Second, now: time.Now}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT updated_at_ns,")).WillReturnRows(sqlmock.NewRows([]string{"updated", "minutes", "rcodes", "latency", "upstreams", "queries"}).AddRow(0, 0, 0, 0, 0, 0))
	for range 2 {
		expectTelemetryExecutions(mock, `INSERT INTO mosdns_telemetry_minutes`, 1)
		expectTelemetryExecutions(mock, `INSERT INTO mosdns_telemetry_rcodes`, 1)
		expectTelemetryExecutions(mock, `INSERT INTO mosdns_telemetry_latency`, 1)
	}
	expectTelemetryExecutions(mock, `INSERT INTO mosdns_telemetry_upstreams`, 2)
	expectTelemetryExecutions(mock, `INSERT INTO mosdns_query_logs`, 1)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta SET updated_at_ns=? WHERE id=1`)).WithArgs(at.UnixNano()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	report, err := MigrateBoltToMySQL(ctx, path, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report != (MySQLMigrationReport{Minutes: 2, Upstreams: 2, Queries: 1}) {
		t.Fatalf("report=%+v", report)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectTelemetryExecutions(mock sqlmock.Sqlmock, query string, count int) {
	for range count {
		mock.ExpectExec(regexp.QuoteMeta(query)).WillReturnResult(sqlmock.NewResult(1, 1))
	}
}
