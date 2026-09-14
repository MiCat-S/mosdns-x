package controlapi

import (
	"context"
	"encoding/json"
	"errors"
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
	if r.Metrics[0].Reason != "no_samples" || r.Metrics[0].Value != nil || r.OverallScore != nil {
		t.Fatal("no cleanup samples must not be a zero-percent measurement")
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
	if r := f.handler.healthSnapshot(); r.Cleanup.Attempts != 2 || r.Cleanup.Errors != 1 || r.Metrics[0].Value == nil || *r.Metrics[0].Value != 50 || r.OverallStatus != "critical" {
		t.Fatal("cleanup error not reflected in health")
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
	provider.value.MySQL.MaxOpen = 0
	f.handler.refreshHealth(context.Background())
	r = f.handler.healthSnapshot()
	if r.Metrics[1].Value != nil || r.Metrics[1].Reason != "unlimited_pool" {
		t.Fatal("unlimited pool must not divide by zero")
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

func TestHealthMonitorStopsWithLifecycle(t *testing.T) {
	f := newFixture(t)
	provider := &healthService{Service: f.store, started: make(chan struct{})}
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
