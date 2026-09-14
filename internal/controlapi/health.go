package controlapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
	"go.uber.org/zap"
)

const healthInterval = time.Minute

type HealthMetric struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Value  *float64 `json:"value"`
	Unit   string   `json:"unit"`
	Status string   `json:"status"`
	Reason string   `json:"reason,omitempty"`
}

type CleanupHealth struct {
	Attempts uint64  `json:"attempts"`
	Errors   uint64  `json:"errors"`
	Seconds  float64 `json:"duration_seconds_total"`
}

type LimiterHealth struct {
	Name     string `json:"name"`
	Entries  int    `json:"entries"`
	Active   int    `json:"active_entries"`
	Capacity int    `json:"capacity"`
	Evicted  uint64 `json:"evicted_total"`
	Rejected uint64 `json:"rejected_total"`
}

type HealthReport struct {
	Timestamp         time.Time             `json:"timestamp"`
	StartedAt         time.Time             `json:"started_at"`
	StorageCheckedAt  *time.Time            `json:"storage_checked_at"`
	StorageStatus     string                `json:"storage_status"`
	StorageError      string                `json:"storage_error,omitempty"`
	StorageAgeSeconds *float64              `json:"storage_age_seconds"`
	RuntimeStatus     string                `json:"runtime_status"`
	Storage           control.StorageHealth `json:"storage"`
	Cleanup           CleanupHealth         `json:"cleanup"`
	RateLimiters      []LimiterHealth       `json:"rate_limiters"`
	Metrics           []HealthMetric        `json:"metrics"`
	OverallStatus     string                `json:"overall_status"`
	OverallScore      *int                  `json:"overall_score"`
}

type healthState struct {
	mu           sync.Mutex
	refreshMu    sync.Mutex
	startedAt    time.Time
	checkedAt    *time.Time
	checkedMono  time.Time
	monotonicNow func() time.Time
	storage      control.StorageHealth
	errCode      string
	cleanup      CleanupHealth
}

// RunHealthMonitor is owned by coremain's maintenance lifecycle. It must stop
// before the control store closes. HTTP requests and scrapes never scan the DB.
func (h *Handler) RunHealthMonitor(ctx context.Context) {
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		h.refreshHealth(ctx)
		if ctx.Err() != nil {
			return
		}
		h.opts.Logger.Info("security_health", zap.Any("health", h.healthSnapshot()))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) refreshHealth(ctx context.Context) {
	h.health.refreshMu.Lock()
	defer h.health.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var storage control.StorageHealth
	code := ""
	if provider, ok := h.opts.Control.(control.HealthProvider); ok {
		var err error
		storage, err = provider.CollectHealth(ctx)
		switch {
		case errors.Is(err, control.ErrHealthScanLimit):
			code = "scan_limit"
		case err != nil:
			code = "collection_failed"
		}
	} else {
		code = "unsupported"
	}
	now := h.opts.Now().UTC()
	h.health.mu.Lock()
	defer h.health.mu.Unlock()
	h.health.storage, h.health.checkedAt, h.health.errCode = storage, &now, code
	h.health.checkedMono = h.health.monotonicNow()
}

func (h *Handler) recordCleanup(start time.Time, err error) {
	h.health.mu.Lock()
	defer h.health.mu.Unlock()
	h.health.cleanup.Attempts++
	if err != nil {
		h.health.cleanup.Errors++
	}
	h.health.cleanup.Seconds += max(0, time.Since(start).Seconds())
}

func (l *ipLimiter) healthSnapshot(name string) LimiterHealth {
	l.mu.Lock()
	defer l.mu.Unlock()
	active := 0
	now := l.now()
	for _, entry := range l.entries {
		if now.Sub(entry.start) < l.window {
			active++
		}
	}
	return LimiterHealth{Name: name, Entries: len(l.entries), Active: active, Capacity: l.capacity, Evicted: l.evicted, Rejected: l.rejected}
}

func healthMetric(key, label, unit string, value *float64, warning, critical float64, reason string) HealthMetric {
	m := HealthMetric{Key: key, Label: label, Unit: unit, Value: value, Reason: reason, Status: "unknown"}
	if value == nil {
		return m
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		m.Value, m.Reason = nil, "invalid_value"
		return m
	}
	m.Status = "healthy"
	if *value >= critical {
		m.Status = "critical"
	} else if *value >= warning {
		m.Status = "warning"
	}
	return m
}

func number(value float64) *float64 { return &value }

func (h *Handler) healthSnapshot() HealthReport {
	now := h.opts.Now().UTC()
	h.health.mu.Lock()
	r := HealthReport{
		Timestamp: now, StartedAt: h.health.startedAt, StorageCheckedAt: h.health.checkedAt,
		Storage: h.health.storage, StorageError: h.health.errCode, Cleanup: h.health.cleanup,
	}
	if r.StorageCheckedAt != nil {
		r.StorageAgeSeconds = number(h.health.monotonicNow().Sub(h.health.checkedMono).Seconds())
	}
	h.health.mu.Unlock()
	r.StorageStatus = "healthy"
	if r.StorageCheckedAt == nil {
		r.StorageStatus, r.StorageError = "unknown", "not_collected"
	} else if *r.StorageAgeSeconds < 0 || *r.StorageAgeSeconds > (2*healthInterval).Seconds() {
		r.StorageStatus, r.StorageError = "unknown", "stale"
	} else if r.StorageError != "" {
		r.StorageStatus = "unknown"
	}
	// Cheap runtime counters are independent of the periodic database scan.
	// The optional provider must only read in-memory driver statistics.
	r.RuntimeStatus = "unknown"
	if provider, ok := h.opts.Control.(control.RuntimeHealthProvider); ok {
		stats := provider.RuntimeHealth()
		r.Storage.Driver, r.Storage.MySQL, r.Storage.Bolt = stats.Driver, stats.MySQL, stats.Bolt
		if stats.MySQL != nil || stats.Bolt != nil {
			r.RuntimeStatus = "healthy"
		}
	} else if r.StorageAgeSeconds != nil && *r.StorageAgeSeconds >= 0 &&
		*r.StorageAgeSeconds <= (2*healthInterval).Seconds() && (r.Storage.MySQL != nil || r.Storage.Bolt != nil) {
		r.RuntimeStatus = "healthy"
	}
	r.RateLimiters = []LimiterHealth{h.limiter.healthSnapshot("login"), h.lookupLimiter.healthSnapshot("lookup")}
	cleanup := healthMetric("session_cleanup", "累计会话清理失败率", "%", nil, 1, 5, "no_samples")
	cleanup.Status = "no_samples"
	if r.Cleanup.Attempts > 0 {
		cleanup.Value = number(float64(r.Cleanup.Errors) * 100 / float64(r.Cleanup.Attempts))
		cleanup.Status, cleanup.Reason = "info", "cumulative_only"
	}
	limiter := healthMetric("rate_limiter", "IP 限速器最高占用", "%", nil, 80, 95, "invalid_capacity")
	var highest float64
	valid := true
	for _, l := range r.RateLimiters {
		if l.Capacity <= 0 {
			valid = false
			break
		}
		highest = max(highest, float64(l.Active)*100/float64(l.Capacity))
	}
	if valid {
		limiter = healthMetric("rate_limiter", "IP 限速器最高占用", "%", number(highest), 80, 95, "")
	}
	pool := healthMetric("db_connections", "控制库连接池占用", "%", nil, 80, 90, "runtime_unavailable")
	integrity := healthMetric("credential_count", "凭证一致性异常", "处", nil, 1, 1, r.StorageError)
	rollback := healthMetric("transaction_rollback", "累计 MySQL 回滚失败", "次", nil, 1, 1, "runtime_unavailable")
	if r.Storage.Driver == "bbolt" {
		pool.Status, pool.Reason = "not_applicable", "bbolt_backend"
		rollback.Status, rollback.Reason = "not_applicable", "bbolt_backend"
		if count := r.Storage.CredentialCountMismatches; r.StorageStatus == "healthy" && count != nil {
			integrity = healthMetric("credential_count", integrity.Label, integrity.Unit, number(float64(*count)), 1, 1, "")
		}
	} else if r.Storage.Driver == "mysql" {
		integrity.Status, integrity.Reason = "not_applicable", "no_materialized_count"
		if p := r.Storage.MySQL; r.RuntimeStatus == "healthy" && p != nil {
			if p.MaxOpen > 0 {
				pool = healthMetric("db_connections", pool.Label, pool.Unit, number(float64(p.InUse)*100/float64(p.MaxOpen)), 80, 90, "")
			} else {
				pool.Reason = "unlimited_pool"
			}
			rollback.Value = number(float64(p.RollbackErrors))
			rollback.Status, rollback.Reason = "info", "cumulative_only"
		}
	}
	r.Metrics = []HealthMetric{cleanup, pool, limiter, integrity, rollback}
	score, complete := 100, r.StorageStatus == "healthy" && r.RuntimeStatus == "healthy"
	r.OverallStatus = "healthy"
	for _, m := range r.Metrics {
		switch m.Status {
		case "critical":
			r.OverallStatus = "critical"
			score -= 25
		case "warning":
			if r.OverallStatus != "critical" {
				r.OverallStatus = "warning"
			}
			score -= 10
		case "unknown":
			complete = false
		}
	}
	if complete {
		score = max(score, 0)
		r.OverallScore = &score
	} else if r.OverallStatus == "healthy" {
		r.OverallStatus = "unknown"
	}
	return r
}

func (h *Handler) serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, h.healthSnapshot())
}
