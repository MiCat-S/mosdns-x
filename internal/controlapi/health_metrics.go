package controlapi

import "github.com/prometheus/client_golang/prometheus"

// Fixed labels only: never expose user IDs, session IDs, client IPs or domains.
var healthDescriptors = map[string]*prometheus.Desc{
	"session_cleanup_total":                  prometheus.NewDesc("mosdns_control_session_cleanup_total", "Login-response cleanup attempts since process start.", nil, nil),
	"session_cleanup_errors_total":           prometheus.NewDesc("mosdns_control_session_cleanup_errors_total", "Failed login-response cleanup attempts since process start.", nil, nil),
	"session_cleanup_duration_seconds_total": prometheus.NewDesc("mosdns_control_session_cleanup_duration_seconds_total", "Total time spent in login-response cleanup.", nil, nil),
	"health_collection_success":              prometheus.NewDesc("mosdns_control_health_collection_success", "One if the latest control-store collection succeeded and is fresh.", nil, nil),
	"health_checked_timestamp_seconds":       prometheus.NewDesc("mosdns_control_health_checked_timestamp_seconds", "Unix time of the latest completed control-store collection.", nil, nil),
	"health_metric_available":                prometheus.NewDesc("mosdns_control_health_metric_available", "One if the metric has an observed value; zero does not mean healthy.", []string{"metric"}, nil),
	"health_metric_value":                    prometheus.NewDesc("mosdns_control_health_metric_value", "Observed health metric value, using the documented unit for each fixed metric key.", []string{"metric"}, nil),
	"rate_limiter_entries":                   prometheus.NewDesc("mosdns_control_rate_limiter_entries", "Retained IP limiter entries, including entries pending expiry cleanup.", []string{"limiter"}, nil),
	"rate_limiter_capacity":                  prometheus.NewDesc("mosdns_control_rate_limiter_capacity", "Maximum retained IP limiter entries.", []string{"limiter"}, nil),
	"rate_limiter_evicted_total":             prometheus.NewDesc("mosdns_control_rate_limiter_evicted_total", "Expired IP limiter entries removed since process start.", []string{"limiter"}, nil),
	"rate_limiter_rejected_total":            prometheus.NewDesc("mosdns_control_rate_limiter_rejected_total", "Requests rejected by an IP limiter since process start.", []string{"limiter"}, nil),
	"active_sessions":                        prometheus.NewDesc("mosdns_control_active_sessions", "Unexpired, unrevoked sessions belonging to enabled users at collection time.", nil, nil),
	"db_connections":                         prometheus.NewDesc("mosdns_control_db_connections", "Control MySQL pool connections at collection time.", []string{"state"}, nil),
	"db_max_open_connections":                prometheus.NewDesc("mosdns_control_db_max_open_connections", "Configured control MySQL pool maximum; zero means unlimited.", nil, nil),
	"db_wait_total":                          prometheus.NewDesc("mosdns_control_db_wait_total", "Cumulative waits for a control MySQL connection.", nil, nil),
	"db_wait_seconds_total":                  prometheus.NewDesc("mosdns_control_db_wait_seconds_total", "Cumulative control MySQL connection wait duration.", nil, nil),
	"db_rollback_errors_total":               prometheus.NewDesc("mosdns_control_db_rollback_errors_total", "Unexpected control MySQL transaction rollback errors since process start.", nil, nil),
	"boltdb_open_read_transactions":          prometheus.NewDesc("mosdns_control_boltdb_open_read_transactions", "Open control bbolt read transactions after the health scan.", nil, nil),
	"boltdb_pending_pages":                   prometheus.NewDesc("mosdns_control_boltdb_pending_pages", "Control bbolt pages pending reuse.", nil, nil),
}

func (h *Handler) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range healthDescriptors {
		ch <- d
	}
}

func (h *Handler) Collect(ch chan<- prometheus.Metric) {
	report := h.healthSnapshot()
	emit := func(name string, kind prometheus.ValueType, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(healthDescriptors[name], kind, value, labels...)
	}
	gauge := func(name string, value float64, labels ...string) {
		emit(name, prometheus.GaugeValue, value, labels...)
	}
	counter := func(name string, value float64, labels ...string) {
		emit(name, prometheus.CounterValue, value, labels...)
	}
	counter("session_cleanup_total", float64(report.Cleanup.Attempts))
	counter("session_cleanup_errors_total", float64(report.Cleanup.Errors))
	counter("session_cleanup_duration_seconds_total", report.Cleanup.Seconds)
	success := float64(0)
	if report.StorageStatus == "healthy" {
		success = 1
	}
	gauge("health_collection_success", success)
	if report.StorageCheckedAt != nil {
		gauge("health_checked_timestamp_seconds", float64(report.StorageCheckedAt.Unix()))
	}
	for _, metric := range report.Metrics {
		available := float64(0)
		if metric.Value != nil {
			available = 1
			gauge("health_metric_value", *metric.Value, metric.Key)
		}
		gauge("health_metric_available", available, metric.Key)
	}
	for _, limiter := range report.RateLimiters {
		gauge("rate_limiter_entries", float64(limiter.Entries), limiter.Name)
		gauge("rate_limiter_capacity", float64(limiter.Capacity), limiter.Name)
		counter("rate_limiter_evicted_total", float64(limiter.Evicted), limiter.Name)
		counter("rate_limiter_rejected_total", float64(limiter.Rejected), limiter.Name)
	}
	if report.StorageStatus != "healthy" {
		return
	}
	if report.Storage.ActiveSessions != nil {
		gauge("active_sessions", float64(*report.Storage.ActiveSessions))
	}
	if p := report.Storage.MySQL; p != nil {
		gauge("db_connections", float64(p.Open), "open")
		gauge("db_connections", float64(p.InUse), "in_use")
		gauge("db_connections", float64(p.Idle), "idle")
		gauge("db_max_open_connections", float64(p.MaxOpen))
		counter("db_wait_total", float64(p.WaitCount))
		counter("db_wait_seconds_total", p.WaitSeconds)
		counter("db_rollback_errors_total", float64(p.RollbackErrors))
	}
	if b := report.Storage.Bolt; b != nil {
		gauge("boltdb_open_read_transactions", float64(b.OpenReadTransactions))
		gauge("boltdb_pending_pages", float64(b.PendingPages))
	}
}

var _ prometheus.Collector = (*Handler)(nil)
