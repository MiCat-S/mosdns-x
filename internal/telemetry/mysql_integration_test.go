package telemetry

import (
	"context"
	"database/sql"
	"net/netip"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

func TestMySQLIntegrationTelemetryLifecycle(t *testing.T) {
	dsn := os.Getenv("MOSDNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MOSDNS_TEST_MYSQL_DSN is not set")
	}
	ctx := context.Background()
	cleanupMySQLTelemetryTables(t, dsn)
	t.Cleanup(func() { cleanupMySQLTelemetryTables(t, dsn) })
	now := time.Now().UTC()
	store, err := OpenMySQL(MySQLOptions{DSN: dsn, OperationTimeout: 5 * time.Second, QueryLogEnabled: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	principal := query_context.Principal{UserID: "user-1", CredentialID: "credential-1", CredentialVersion: 1}
	store.Observe(dns_handler.Result{Admitted: true, Principal: principal, ClientAddr: netip.MustParseAddr("192.0.2.10"), QuestionName: "example.test.", QuestionType: dns.TypeA, Rcode: dns.RcodeSuccess, Duration: 8 * time.Millisecond, CacheHit: true, Protocol: "doh", AnswerIPs: []string{"192.0.2.11"}, EDNS: dns_handler.EDNSInfo{Present: true, UDPSize: 1232, DNSSECOK: true, OptionCodes: []uint16{8}}})
	store.ObserveUpstream(query_context.UpstreamAttempt{Principal: principal, UpstreamID: "remote", Duration: 4 * time.Millisecond})
	if err := store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx, userID(principal), now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Completed != 1 || snapshot.CacheHits != 1 || len(snapshot.Upstreams) != 1 || snapshot.Upstreams[0].ID != "remote" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	queries, err := store.Queries(ctx, userID(principal), now.Add(-time.Hour), now.Add(time.Minute), QueryFilter{}, Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries.Items) != 1 || queries.Items[0].ClientIP != "192.0.2.10" || len(queries.Items[0].AnswerIPs) != 1 || !queries.Items[0].EDNS.DNSSECOK {
		t.Fatalf("queries=%+v", queries)
	}
	cacheHit := true
	filtered, err := store.Queries(ctx, userID(principal), now.Add(-time.Hour), now.Add(time.Minute), QueryFilter{Name: "EXAMPLE.TEST", QType: "a", Rcode: "noerror", CredentialID: "credential-1", Protocol: "DOH", Address: "192.0.2.11", CacheHit: &cacheHit}, Page{})
	if err != nil || len(filtered.Items) != 1 {
		t.Fatalf("filtered queries=%+v err=%v", filtered, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenMySQL(MySQLOptions{DSN: dsn, OperationTimeout: 5 * time.Second, QueryLogEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err = reopened.Snapshot(ctx, userID(principal), now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || snapshot.Completed != 1 {
		t.Fatalf("reopened snapshot=%+v err=%v", snapshot, err)
	}
}

func userID(principal query_context.Principal) string { return principal.UserID }

func cleanupMySQLTelemetryTables(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`DROP TABLE IF EXISTS mosdns_query_logs`,
		`DROP TABLE IF EXISTS mosdns_telemetry_upstreams`,
		`DROP TABLE IF EXISTS mosdns_telemetry_latency`,
		`DROP TABLE IF EXISTS mosdns_telemetry_rcodes`,
		`DROP TABLE IF EXISTS mosdns_telemetry_minutes`,
		`DROP TABLE IF EXISTS mosdns_telemetry_meta`,
		`DELETE FROM mosdns_schema_migrations WHERE component='telemetry'`,
	} {
		_, _ = db.Exec(statement)
	}
}
