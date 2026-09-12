package telemetry

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
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
		{time: now, result: &dns_handler.Result{Admitted: true, Principal: principal, ClientAddr: netip.MustParseAddr("192.0.2.10"), QuestionName: "example.test.", QuestionType: dns.TypeAAAA, Rcode: dns.RcodeSuccess, Duration: 12 * time.Millisecond, Protocol: "doh3", AnswerIPs: []string{"2001:db8::1"}, EDNS: dns_handler.EDNSInfo{Present: true, UDPSize: 1232}, ResponseSource: query_context.ResponseSourceUpstream, ResponseSourceID: "remote", UpstreamID: "remote/0"}},
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

func TestEnsureMySQLTelemetryQueryLogSchemaRepairsPartialMigration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for i, column := range mysqlTelemetryQueryLogColumns {
		rows := sqlmock.NewRows([]string{"COLUMN_TYPE", "IS_NULLABLE", "COLUMN_DEFAULT"})
		if i == 0 {
			rows.AddRow(column.columnType, "NO", "")
		}
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT`)).WithArgs(column.name).WillReturnRows(rows)
		if i != 0 {
			mock.ExpectExec(regexp.QuoteMeta(`ALTER TABLE mosdns_query_logs ADD COLUMN ` + column.name)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
	}
	for _, index := range []string{"ix_mosdns_query_logs_source", "ix_mosdns_query_logs_upstream"} {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM information_schema.statistics`)).WithArgs(index).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta(`ALTER TABLE mosdns_query_logs ADD KEY ` + index)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	if err := ensureMySQLTelemetryQueryLogSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureMySQLTelemetryQueryLogSchemaRejectsIncompatibleColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	column := mysqlTelemetryQueryLogColumns[0]
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT`)).WithArgs(column.name).
		WillReturnRows(sqlmock.NewRows([]string{"COLUMN_TYPE", "IS_NULLABLE", "COLUMN_DEFAULT"}).AddRow("varchar(16)", "NO", ""))
	if err := ensureMySQLTelemetryQueryLogSchema(ctx, conn); err == nil {
		t.Fatal("incompatible column accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConvergeMySQLTelemetrySchemaSupportsKnownAndMissingVersions(t *testing.T) {
	for _, version := range []int{-1, 1, 2, 3} {
		name := "missing"
		if version >= 0 {
			name = fmt.Sprintf("v%d", version)
		}
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			versions := sqlmock.NewRows([]string{"version"})
			if version >= 0 {
				versions.AddRow(version)
			}
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component='telemetry'`)).WillReturnRows(versions)
			expectCurrentMySQLTelemetrySchema(mock)
			mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_schema_migrations (component, version) VALUES ('telemetry', ?)`)).
				WithArgs(mysqlTelemetrySchemaVersion).WillReturnResult(sqlmock.NewResult(1, 1))
			if err := convergeMySQLTelemetrySchema(ctx, conn); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConvergeMySQLTelemetrySchemaDoesNotAdvanceIncompleteSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component='telemetry'`)).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(2))
	column := mysqlTelemetryQueryLogColumns[0]
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT`)).WithArgs(column.name).
		WillReturnRows(sqlmock.NewRows([]string{"COLUMN_TYPE", "IS_NULLABLE", "COLUMN_DEFAULT"}).AddRow("varchar(16)", "NO", ""))
	if err := convergeMySQLTelemetrySchema(ctx, conn); err == nil {
		t.Fatal("incomplete schema advanced")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectCurrentMySQLTelemetrySchema(mock sqlmock.Sqlmock) {
	for _, column := range mysqlTelemetryQueryLogColumns {
		var defaultValue any
		if column.defaultVal.Valid {
			defaultValue = column.defaultVal.String
		}
		nullable := "NO"
		if column.nullable {
			nullable = "YES"
		}
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT`)).WithArgs(column.name).
			WillReturnRows(sqlmock.NewRows([]string{"COLUMN_TYPE", "IS_NULLABLE", "COLUMN_DEFAULT"}).AddRow(column.columnType, nullable, defaultValue))
	}
	for _, index := range []string{"ix_mosdns_query_logs_source", "ix_mosdns_query_logs_upstream"} {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM information_schema.statistics`)).WithArgs(index).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	}
}

func TestOptionalEDNSSnapshotSQLEncodingDistinguishesUnknownAndNoOPT(t *testing.T) {
	unknown, err := marshalOptionalEDNSSnapshot(nil)
	if err != nil || unknown != nil {
		t.Fatalf("unknown=%v err=%v", unknown, err)
	}
	encoded, err := marshalOptionalEDNSSnapshot(&dnsutils.EDNSSnapshot{Present: false, OptionCodes: []uint16{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := encoded.([]byte)
	if !ok || !strings.Contains(string(raw), `"present":false`) {
		t.Fatalf("encoded=%T %s", encoded, raw)
	}
	decoded, err := unmarshalOptionalEDNSSnapshot(raw)
	if err != nil || decoded == nil || decoded.Present || decoded.OptionCodes == nil {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	decoded, err = unmarshalOptionalEDNSSnapshot(nil)
	if err != nil || decoded != nil {
		t.Fatalf("unknown decoded=%+v err=%v", decoded, err)
	}
}

func TestMySQLQueriesReadsNullAndObservedNoOPTStages(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 11, 12, 34, 0, 0, time.UTC)
	store := &Store{mysql: db, mysqlTimeout: time.Second, queryLogEnabled: true, queryRetention: time.Hour, now: func() time.Time { return now }}
	columns := []string{
		"id", "time_ns", "user_id", "credential_id", "client_ip", "name", "qtype", "rcode", "duration_ms", "cache_hit", "protocol", "answer_ips_json", "edns_json",
		"edns_trace_version", "upstream_stage_status", "upstream_request_edns_json", "upstream_response_edns_json", "response_edns_json",
		"response_source", "response_source_id", "upstream_id", "matched_rule_id", "matched_public_list_id",
	}
	rows := sqlmock.NewRows(columns).AddRow(
		"00000000000000000001.query", now.UnixNano(), "user-1", "credential-1", "192.0.2.1", "example.test.", "A", "NOERROR", float64(1), false, "doh",
		`[]`, `{"present":false,"version":0,"udp_size":0,"dnssec_ok":false,"option_codes":[]}`,
		1, "selected", nil, `{"present":false,"version":0,"udp_size":0,"dnssec_ok":false,"option_codes":[]}`, `{"present":true,"version":0,"udp_size":1232,"dnssec_ok":false,"option_codes":[]}`,
		"upstream", "forward_remote", "forward_remote/0", "", "",
	)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, time_ns, user_id`)).WillReturnRows(rows)
	page, err := store.mysqlQueries(context.Background(), "user-1", now.Add(-time.Minute), now.Add(time.Minute), QueryFilter{}, Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%+v", page.Items)
	}
	record := page.Items[0]
	if record.UpstreamRequestEDNS != nil || record.UpstreamResponseEDNS == nil || record.UpstreamResponseEDNS.Present || record.ResponseEDNS == nil || !record.ResponseEDNS.Present {
		t.Fatalf("record=%+v", record)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
