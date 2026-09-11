package telemetry

import (
	"net/netip"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

func TestWriteMySQLBatchPersistsAggregatesAndQueryDetail(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	now := time.Date(2026, 9, 11, 12, 34, 0, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, now: func() time.Time { return now }}
	store.mysqlPrunedAt.Store(now.Truncate(time.Minute).Unix())
	principal := query_context.Principal{UserID: "user-1", CredentialID: "credential-1", CredentialVersion: 1}
	events := []event{
		{time: now, result: &dns_handler.Result{Admitted: true, Principal: principal, ClientAddr: netip.MustParseAddr("192.0.2.10"), QuestionName: "example.test.", QuestionType: dns.TypeAAAA, Rcode: dns.RcodeSuccess, Duration: 12 * time.Millisecond, Protocol: "doh3", AnswerIPs: []string{"2001:db8::1"}, EDNS: dns_handler.EDNSInfo{Present: true, UDPSize: 1232}}},
		{time: now, attempt: &query_context.UpstreamAttempt{Principal: principal, UpstreamID: "remote", Duration: 6 * time.Millisecond}},
	}
	mock.ExpectBegin()
	expectTelemetryExecutions(mock, `INSERT INTO mosdns_telemetry_minutes`, 2)
	expectTelemetryExecutions(mock, `INSERT INTO mosdns_telemetry_rcodes`, 2)
	expectTelemetryExecutions(mock, `INSERT INTO mosdns_telemetry_latency`, 2)
	expectTelemetryExecutions(mock, `INSERT INTO mosdns_telemetry_upstreams`, 2)
	expectTelemetryExecutions(mock, `INSERT INTO mosdns_query_logs`, 1)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_telemetry_meta SET updated_at_ns=? WHERE id=1`)).WithArgs(now.UnixNano()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.writeMySQLBatch(events); err != nil {
		t.Fatal(err)
	}
	if got := store.updatedUnixNano.Load(); got != now.UnixNano() {
		t.Fatalf("updated=%d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
