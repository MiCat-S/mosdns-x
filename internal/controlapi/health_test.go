package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/prometheus/client_golang/prometheus"
)

func TestHealthAdminAccessAndUnknownState(t *testing.T) {
	f := newFixture(t)
	f.handler.health.monotonicNow = f.clock.Now
	admin, csrf := login(t, f.handler, "admin", "password-for-admin")
	alice, _ := login(t, f.handler, "alice", "password-for-alice")
	path := "/api/v1/admin/health"
	for _, tc := range []struct {
		cookie *http.Cookie
		status int
	}{{nil, 401}, {alice, 403}, {admin, 200}} {
		w := req(f.handler, "GET", path, "", tc.cookie, "")
		if w.Code != tc.status {
			t.Fatalf("health status %d, want %d", w.Code, tc.status)
		}
		if tc.status == 200 {
			var report HealthReport
			if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.StorageStatus != "unknown" || report.OverallScore != nil {
				t.Fatal("unsampled storage must not be healthy")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing private response cache policy")
			}
		}
	}
	if w := req(f.handler, "POST", path, `{}`, admin, csrf); w.Code != 405 {
		t.Fatalf("health must be read-only: %d", w.Code)
	}
	// The examples' unprotected aliases must not bypass the admin API.
	for _, p := range []string{"/health-data", "/health-ui", "/api/v1/me/health"} {
		if w := req(f.handler, "GET", p, "", alice, ""); w.Code != 404 {
			t.Fatalf("unexpected public health alias %s: %d", p, w.Code)
		}
	}
	f.handler.refreshHealth(context.Background())
	r := f.handler.healthSnapshot()
	if r.StorageStatus != "healthy" || r.Storage.ActiveSessions == nil || *r.Storage.ActiveSessions != 2 {
		t.Fatalf("bad storage snapshot: %+v", r.Storage)
	}
	if r.Metrics[0].Status != "no_samples" || r.Metrics[0].Value != nil ||
		r.OverallScore == nil || *r.OverallScore != 100 || r.OverallStatus != "healthy" {
		t.Fatal("no samples must stay null without blocking an otherwise healthy score")
	}
	for _, metric := range r.Metrics {
		if metric.Key == "db_connections" && metric.Status != "not_applicable" {
			t.Fatal("bbolt reported as a MySQL pool")
		}
	}
	f.handler.recordCleanup(time.Now(), nil)
	if r := f.handler.healthSnapshot(); r.OverallStatus != "healthy" || r.OverallScore == nil || *r.OverallScore != 100 {
		t.Fatalf("valid observed health: %+v", r)
	}
	f.handler.recordCleanup(time.Now(), errors.New("test error"))
	if r := f.handler.healthSnapshot(); r.Cleanup.Attempts != 2 || r.Cleanup.Errors != 1 ||
		r.Metrics[0].Value == nil || *r.Metrics[0].Value != 50 || r.Metrics[0].Status != "info" || r.OverallStatus != "healthy" {
		t.Fatal("historical cleanup error should remain visible without locking current health")
	}
	f.clock.Set(f.clock.Now().Add(3 * time.Minute))
	if r := f.handler.healthSnapshot(); r.StorageStatus != "unknown" || r.StorageError != "stale" || r.OverallScore != nil {
		t.Fatal("stale data retained a complete score")
	}
}

type healthService struct {
	control.Service
	value   control.StorageHealth
	err     error
	started chan struct{}
}

func (s *healthService) CollectHealth(ctx context.Context) (control.StorageHealth, error) {
	if s.started != nil {
		close(s.started)
		<-ctx.Done()
		return control.StorageHealth{}, ctx.Err()
	}
	return s.value, s.err
}

func (s *healthService) RuntimeHealth() control.StorageHealth { return s.value }

func TestHealthMySQLIdlePoolAndCollectionFailure(t *testing.T) {
	f := newFixture(t)
	provider := &healthService{Service: f.store, value: control.StorageHealth{
		Driver: "mysql", MySQL: &control.PoolHealth{MaxOpen: 32},
	}}
	f.handler.opts.Control = provider
	f.handler.refreshHealth(context.Background())
	r := f.handler.healthSnapshot()
	if r.Metrics[1].Value == nil || *r.Metrics[1].Value != 0 || r.Metrics[1].Status != "healthy" {
		t.Fatal("an unused bounded pool should have 0% utilization")
	}
	provider.value.MySQL.RollbackErrors = 1
	f.handler.recordCleanup(time.Now(), errors.New("historical failure"))
	if r := f.handler.healthSnapshot(); r.OverallStatus != "healthy" ||
		r.Metrics[4].Status != "info" || r.Metrics[4].Value == nil || *r.Metrics[4].Value != 1 {
		t.Fatal("cumulative rollback errors must not lock health to critical")
	}
	provider.value.MySQL.MaxOpen = 0
	f.handler.refreshHealth(context.Background())
	r = f.handler.healthSnapshot()
	if r.Metrics[1].Value != nil || r.Metrics[1].Reason != "unlimited_pool" {
		t.Fatal("unlimited pool must not divide by zero")
	}
	// An unobservable ratio is not applicable, not unknown: leaving it unknown
	// would suppress the score forever, the defect no_samples once caused.
	if r.Metrics[1].Status != "not_applicable" || r.OverallStatus != "healthy" ||
		r.OverallScore == nil || *r.OverallScore != 100 {
		t.Fatalf("unlimited pool suppressed the overall score: %+v %v", r.Metrics[1], r.OverallScore)
	}
	provider.err = errors.New("mysql://secret:password@private")
	f.handler.refreshHealth(context.Background())
	body, err := json.Marshal(f.handler.healthSnapshot())
	if err != nil || strings.Contains(string(body), "password") {
		t.Fatalf("collection error leaked details or JSON failed: %s %v", body, err)
	}
	if f.handler.healthSnapshot().StorageStatus != "unknown" {
		t.Fatal("failed collection is not unknown")
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), -1} {
		m := healthMetric("test", "test", "", &v, 1, 2, "")
		if m.Value != nil || m.Status != "unknown" {
			t.Fatal("invalid floating point metric accepted")
		}
	}
}

func TestHealthExpiredLimiterEntriesDoNotLockUtilization(t *testing.T) {
	f := newFixture(t)
	f.handler.refreshHealth(context.Background())
	l := f.handler.limiter
	for i := range 3900 {
		l.entries[fmt.Sprintf("test-ip-%d", i)] = ipEntry{start: f.clock.Now(), count: 1}
	}
	if r := f.handler.healthSnapshot(); r.Metrics[2].Status != "critical" || r.RateLimiters[0].Active != 3900 {
		t.Fatal("active limiter pressure was not observed")
	}
	f.clock.Set(f.clock.Now().Add(l.window))
	r := f.handler.healthSnapshot()
	if r.Metrics[2].Value == nil || *r.Metrics[2].Value != 0 || r.OverallStatus != "healthy" ||
		r.RateLimiters[0].Active != 0 || r.RateLimiters[0].Entries != 3900 || len(l.entries) != 3900 {
		t.Fatal("read-only health must ignore expired entries and retain the actual memory count")
	}
	if !l.allow("next-request") {
		t.Fatal("health read changed subsequent admission")
	}
}

func TestHealthStalenessUsesIndependentMonotonicTime(t *testing.T) {
	f := newFixture(t)
	mono := time.Now()
	f.handler.health.monotonicNow = func() time.Time { return mono }
	f.handler.refreshHealth(context.Background())
	for _, wallJump := range []time.Duration{-24 * time.Hour, 48 * time.Hour} {
		f.clock.Set(f.clock.Now().Add(wallJump))
		r := f.handler.healthSnapshot()
		if r.StorageStatus != "healthy" || r.StorageAgeSeconds == nil || *r.StorageAgeSeconds != 0 {
			t.Fatal("wall-clock adjustment incorrectly expired the scan")
		}
	}
	if !f.handler.health.checkedMono.Equal(mono) || f.handler.health.checkedMono != mono {
		t.Fatal("monotonic component was stripped")
	}
	mono = mono.Add(2*time.Minute + time.Nanosecond)
	if r := f.handler.healthSnapshot(); r.StorageError != "stale" || r.OverallScore != nil || r.RuntimeStatus != "healthy" {
		t.Fatal("stale scan must be unknown without suppressing live driver stats")
	}
}

func TestHealthPrometheusStatesAndRuntimeContinuity(t *testing.T) {
	f := newFixture(t)
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(f.handler); err != nil {
		t.Fatal(err)
	}
	sample := func(name string, labels map[string]string) (float64, bool) {
		t.Helper()
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() != "mosdns_control_"+name {
				continue
			}
			for _, m := range family.Metric {
				match := len(m.Label) == len(labels)
				for _, label := range m.Label {
					match = match && labels[label.GetName()] == label.GetValue()
				}
				if match {
					if m.Gauge != nil {
						return m.Gauge.GetValue(), true
					}
					return m.Counter.GetValue(), true
				}
			}
		}
		return 0, false
	}
	want := func(name string, labels map[string]string, value float64) {
		t.Helper()
		if got, ok := sample(name, labels); !ok || got != value {
			t.Fatalf("%s %v = %v (present=%v), want %v", name, labels, got, ok, value)
		}
	}
	want("health_overall_status", map[string]string{"status": "unknown"}, 1)
	want("health_metric_status", map[string]string{"metric": "session_cleanup", "status": "no_samples"}, 1)
	want("health_metric_status", map[string]string{"metric": "db_connections", "status": "not_applicable"}, 1)
	want("health_score_available", nil, 0)
	f.handler.refreshHealth(context.Background())
	want("health_score", nil, 100)
	want("health_overall_status", map[string]string{"status": "healthy"}, 1)
	mismatches, active := uint64(3), uint64(2)
	provider := &healthService{Service: f.store, value: control.StorageHealth{
		Driver: "bbolt", ActiveSessions: &active, CredentialCountMismatches: &mismatches, Bolt: &control.BoltHealth{},
	}}
	f.handler.opts.Control = provider
	f.handler.refreshHealth(context.Background())
	want("health_metric_status", map[string]string{"metric": "credential_count", "status": "critical"}, 1)
	want("health_overall_status", map[string]string{"status": "critical"}, 1)
	want("health_score", nil, 75)
	provider.value = control.StorageHealth{Driver: "mysql", MySQL: &control.PoolHealth{
		MaxOpen: 10, InUse: 2, Open: 3, Idle: 1, WaitCount: 9, RollbackErrors: 1,
	}}
	for _, err := range []error{nil, errors.New("SQL scan failed"), nil} {
		provider.err = err
		f.handler.refreshHealth(context.Background())
		want("db_wait_total", nil, 9)
		want("db_rollback_errors_total", nil, 1)
		want("db_connections", map[string]string{"state": "in_use"}, 2)
		want("health_metric_status", map[string]string{"metric": "transaction_rollback", "status": "info"}, 1)
		if err != nil {
			want("health_collection_success", nil, 0)
			want("health_runtime_available", nil, 1)
			want("health_overall_status", map[string]string{"status": "unknown"}, 1)
			if _, ok := sample("health_score", nil); ok {
				t.Fatal("failed scan must not export a fabricated score")
			}
		}
	}
}

func TestHealthMonitorStopsWithLifecycle(t *testing.T) {
	f := newFixture(t)
	provider := &healthService{Service: f.store, started: make(chan struct{}),
		value: control.StorageHealth{Driver: "bbolt", Bolt: &control.BoltHealth{PendingPages: 7}}}
	f.handler.opts.Control = provider
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.handler.RunHealthMonitor(ctx) }()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("monitor did not collect on start")
	}
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(f.handler); err != nil {
		t.Fatal(err)
	}
	scraped := make(chan error, 1)
	go func() {
		families, err := reg.Gather()
		if err == nil {
			found := false
			for _, family := range families {
				if family.GetName() == "mosdns_control_boltdb_pending_pages" {
					found = len(family.Metric) == 1 && family.Metric[0].GetGauge().GetValue() == 7
				}
			}
			if !found {
				err = errors.New("scrape lost runtime stats while scan was blocked")
			}
		}
		scraped <- err
	}()
	select {
	case err := <-scraped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scrape waited for a blocked database scan")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop before store shutdown")
	}
}

func TestHealthConcurrentCollectionAndPrometheus(t *testing.T) {
	f := newFixture(t)
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(f.handler); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				f.handler.limiter.allow("192.0.2.1")
				f.handler.recordCleanup(time.Now(), nil)
				f.handler.refreshHealth(context.Background())
				metrics, err := reg.Gather()
				if err != nil {
					t.Error(err)
				}
				for _, family := range metrics {
					for _, metric := range family.Metric {
						for _, label := range metric.Label {
							if label.GetValue() == "192.0.2.1" {
								t.Error("private client address became a metric label")
							}
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	r := f.handler.healthSnapshot()
	if r.Cleanup.Attempts != 80 || r.RateLimiters[0].Rejected == 0 {
		t.Fatalf("lost concurrent counter updates: %+v", r.Cleanup)
	}
}
