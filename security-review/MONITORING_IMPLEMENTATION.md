# 安全监控指标实施方案

> 原始设计资料，示例不保证可直接编译。已实现的指标、权限、采集口径与生命周期以 [实际监控说明](../docs/monitoring.md) 为准；不要按下文新增第二套采集器或取消 `/metrics` 鉴权。

**项目**: mosdns-x DNS Server  
**文档版本**: 1.0  
**创建日期**: 2026-09-14  
**状态**: 实施指南

---

## 📊 概述

本文档详细说明如何为已修复的安全问题建立监控指标，确保修复效果可持续验证。

### 现有监控基础设施

mosdns-x 已具备以下监控能力：

✅ **Prometheus 集成**
- 端点：`/metrics` (已内置)
- 注册器：`prometheus.Registry`
- 包装器：`WrapRegistererWithPrefix`

✅ **现有指标**
- DNS 查询指标（`query_total`, `err_total`, `thread`, `response_latency`）
- 插件级指标（通过 `metrics_collector`）
- pprof 性能分析（`/debug/pprof/`）

✅ **数据库支持**
- BoltDB：内置统计信息（`db.Stats()`）
- MySQL：连接池统计（`db.Stats()`）

---

## 🎯 需要监控的3个核心领域

根据已完成的安全修复，重点监控：

### 1. 会话清理（修复 #2）
### 2. 内存使用（修复 #4）
### 3. 连接池状态（修复 #5）

---

## 📐 实施方案

### 方案概览

| 方案 | 复杂度 | 时间 | 推荐场景 |
|------|--------|------|----------|
| **A. 扩展现有 Prometheus** | 中 | 2-3天 | 生产环境，需要持久化和可视化 |
| **B. 内部日志监控** | 低 | 1天 | 快速验证，轻量级部署 |
| **C. 健康检查端点** | 低 | 1天 | 容器化环境，自动化健康检查 |

**推荐**: 先实施方案 B（1天内完成），再逐步升级到方案 A。

---

## 🔧 方案 A: 扩展 Prometheus 指标（推荐）

### 1. 创建安全指标收集器

创建新文件：`internal/control/metrics.go`

```go
package control

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.etcd.io/bbolt"
)

// SecurityMetrics 收集安全相关的运行时指标
type SecurityMetrics struct {
	// 会话指标
	sessionCleanupTotal   prometheus.Counter
	sessionCleanupErrors  prometheus.Counter
	sessionCleanupLatency prometheus.Histogram
	activeSessionsGauge   prometheus.Gauge

	// 凭证指标
	credentialCountGauge prometheus.Gauge
	credentialMismatch   prometheus.Counter

	// 限速器指标
	rateLimiterSizeGauge      prometheus.Gauge
	rateLimiterCleanupTotal   prometheus.Counter
	rateLimiterEvictionsTotal prometheus.Counter

	// 数据库指标（MySQL）
	dbOpenConnectionsGauge prometheus.Gauge
	dbInUseConnectionsGauge prometheus.Gauge
	dbIdleConnectionsGauge prometheus.Gauge
	dbWaitCountTotal       prometheus.Counter
	dbWaitDuration         prometheus.Counter

	mu sync.RWMutex
}

// NewSecurityMetrics 创建新的安全指标收集器
func NewSecurityMetrics(reg prometheus.Registerer) *SecurityMetrics {
	m := &SecurityMetrics{
		// 会话清理指标
		sessionCleanupTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mosdns_control_session_cleanup_total",
			Help: "Total number of session cleanup operations",
		}),
		sessionCleanupErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mosdns_control_session_cleanup_errors_total",
			Help: "Total number of session cleanup errors (resource leaks)",
		}),
		sessionCleanupLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "mosdns_control_session_cleanup_duration_seconds",
			Help:    "Session cleanup operation latency",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
		}),
		activeSessionsGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mosdns_control_active_sessions",
			Help: "Current number of active user sessions",
		}),

		// 凭证指标
		credentialCountGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mosdns_control_user_credentials",
			Help: "Current number of user credentials",
		}),
		credentialMismatch: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mosdns_control_credential_count_mismatch_total",
			Help: "Total number of credential count mismatches detected",
		}),

		// 限速器指标
		rateLimiterSizeGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mosdns_control_rate_limiter_entries",
			Help: "Current number of entries in IP rate limiter",
		}),
		rateLimiterCleanupTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mosdns_control_rate_limiter_cleanup_total",
			Help: "Total number of rate limiter cleanup operations",
		}),
		rateLimiterEvictionsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mosdns_control_rate_limiter_evictions_total",
			Help: "Total number of expired entries evicted from rate limiter",
		}),

		// MySQL 连接池指标
		dbOpenConnectionsGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mosdns_control_db_connections_open",
			Help: "Number of established connections both in use and idle",
		}),
		dbInUseConnectionsGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mosdns_control_db_connections_in_use",
			Help: "Number of connections currently in use",
		}),
		dbIdleConnectionsGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mosdns_control_db_connections_idle",
			Help: "Number of idle connections",
		}),
		dbWaitCountTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mosdns_control_db_connection_wait_total",
			Help: "Total number of connections waited for",
		}),
		dbWaitDuration: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mosdns_control_db_connection_wait_duration_seconds_total",
			Help: "Total time blocked waiting for connections",
		}),
	}

	// 注册所有指标
	if reg != nil {
		reg.MustRegister(
			m.sessionCleanupTotal,
			m.sessionCleanupErrors,
			m.sessionCleanupLatency,
			m.activeSessionsGauge,
			m.credentialCountGauge,
			m.credentialMismatch,
			m.rateLimiterSizeGauge,
			m.rateLimiterCleanupTotal,
			m.rateLimiterEvictionsTotal,
			m.dbOpenConnectionsGauge,
			m.dbInUseConnectionsGauge,
			m.dbIdleConnectionsGauge,
			m.dbWaitCountTotal,
			m.dbWaitDuration,
		)
	}

	return m
}

// RecordSessionCleanup 记录会话清理操作
func (m *SecurityMetrics) RecordSessionCleanup(duration time.Duration, err error) {
	m.sessionCleanupTotal.Inc()
	m.sessionCleanupLatency.Observe(duration.Seconds())
	if err != nil {
		m.sessionCleanupErrors.Inc()
	}
}

// UpdateActiveSessions 更新活跃会话数
func (m *SecurityMetrics) UpdateActiveSessions(count int) {
	m.activeSessionsGauge.Set(float64(count))
}

// UpdateCredentialCount 更新凭证计数
func (m *SecurityMetrics) UpdateCredentialCount(count uint32) {
	m.credentialCountGauge.Set(float64(count))
}

// RecordCredentialMismatch 记录凭证计数不匹配
func (m *SecurityMetrics) RecordCredentialMismatch() {
	m.credentialMismatch.Inc()
}

// UpdateRateLimiterSize 更新限速器大小
func (m *SecurityMetrics) UpdateRateLimiterSize(size int) {
	m.rateLimiterSizeGauge.Set(float64(size))
}

// RecordRateLimiterCleanup 记录限速器清理
func (m *SecurityMetrics) RecordRateLimiterCleanup(evicted int) {
	m.rateLimiterCleanupTotal.Inc()
	m.rateLimiterEvictionsTotal.Add(float64(evicted))
}

// UpdateDatabaseStats 更新数据库连接池统计
func (m *SecurityMetrics) UpdateDatabaseStats(stats sql.DBStats) {
	m.dbOpenConnectionsGauge.Set(float64(stats.OpenConnections))
	m.dbInUseConnectionsGauge.Set(float64(stats.InUse))
	m.dbIdleConnectionsGauge.Set(float64(stats.Idle))
	m.dbWaitCountTotal.Add(float64(stats.WaitCount))
	m.dbWaitDuration.Add(stats.WaitDuration.Seconds())
}

// CollectBoltDBStats 收集 BoltDB 统计信息（用于日志）
func CollectBoltDBStats(db *bbolt.DB) map[string]interface{} {
	stats := db.Stats()
	return map[string]interface{}{
		"freelist_free_pages":   stats.FreePageN,
		"freelist_pending_pages": stats.PendingPageN,
		"freelist_allocated":    stats.FreeAlloc,
		"freelist_in_use":       stats.FreelistInuse,
		"tx_read":               stats.TxN,
		"tx_open_read":          stats.OpenTxN,
	}
}
```

---

### 2. 集成到 Store

修改 `internal/control/store.go`，添加指标收集：

```go
type Store struct {
	db         *bbolt.DB
	clock      Clock
	metrics    *SecurityMetrics  // 新增
	mu         sync.RWMutex
	closed     bool
	closeCh    chan struct{}
	admitSlots chan struct{}
}

// 在 Open 函数中初始化指标
func Open(path string, opts Options) (*Store, error) {
	// ... 现有代码 ...
	
	s := &Store{
		db:         db,
		clock:      opts.Clock,
		metrics:    opts.Metrics,  // 从配置传入
		closeCh:    make(chan struct{}),
		admitSlots: admitSlots,
	}
	
	// 启动后台指标收集
	if s.metrics != nil {
		go s.collectMetricsPeriodically()
	}
	
	return s, nil
}

// 定期收集指标
func (s *Store) collectMetricsPeriodically() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			s.collectMetrics()
		case <-s.closeCh:
			return
		}
	}
}

func (s *Store) collectMetrics() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	// 收集会话统计
	if count, err := s.countActiveSessions(ctx); err == nil {
		s.metrics.UpdateActiveSessions(count)
	}
	
	// 收集 BoltDB 统计（仅日志）
	if s.db != nil {
		stats := CollectBoltDBStats(s.db)
		// 可选：记录到日志
		_ = stats
	}
}

// 在会话清理时记录指标
func (s *Store) RevokeSession(ctx context.Context, userID, sessionID string) error {
	start := time.Now()
	err := s.revokeSession(ctx, userID, sessionID)
	
	if s.metrics != nil {
		s.metrics.RecordSessionCleanup(time.Since(start), err)
	}
	
	return err
}
```

---

### 3. 集成到 MySQL Store

修改 `internal/control/mysql_store.go`：

```go
type MySQLStore struct {
	db      *sql.DB
	clock   Clock
	metrics *SecurityMetrics  // 新增
	mu      sync.RWMutex
	closed  bool
	closeCh chan struct{}
}

// 在 OpenMySQL 中初始化
func OpenMySQL(opts MySQLOptions) (*MySQLStore, error) {
	// ... 现有代码 ...
	
	s := &MySQLStore{
		db:      db,
		clock:   opts.Clock,
		metrics: opts.Metrics,  // 从配置传入
		closeCh: make(chan struct{}),
	}
	
	// 启动后台指标收集
	if s.metrics != nil {
		go s.collectDBStatsPeriodically()
	}
	
	return s, nil
}

// 定期收集数据库连接池统计
func (s *MySQLStore) collectDBStatsPeriodically() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			stats := s.db.Stats()
			s.metrics.UpdateDatabaseStats(stats)
		case <-s.closeCh:
			return
		}
	}
}
```

---

### 4. 集成到 IP 限速器

修改 `internal/controlapi/handler.go` 中的 `ipLimiter`：

```go
type ipLimiter struct {
	mu       sync.Mutex
	entries  map[string]ipEntry
	capacity int
	window   time.Duration
	metrics  *control.SecurityMetrics  // 新增
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	
	now := time.Now()
	
	// 定期清理过期条目（修复 #4 的增强）
	evicted := 0
	if len(l.entries) > 0 && len(l.entries)%100 == 0 {
		for k, e := range l.entries {
			if now.Sub(e.start) >= l.window {
				delete(l.entries, k)
				evicted++
			}
		}
		
		// 记录清理指标
		if l.metrics != nil && evicted > 0 {
			l.metrics.RecordRateLimiterCleanup(evicted)
		}
	}
	
	// 更新当前大小
	if l.metrics != nil {
		l.metrics.UpdateRateLimiterSize(len(l.entries))
	}
	
	// ... 现有限速逻辑 ...
}
```

---

### 5. 配置和启动

修改 `coremain/mosdns.go`，在启动时初始化安全指标：

```go
func RunMosdnsContext(ctx context.Context, cfg *Config) (retErr error) {
	// ... 现有代码 ...
	
	m := &Mosdns{
		logger:     lg,
		httpAPIMux: http.NewServeMux(),
		metricsReg: newMetricsReg(),
		// ...
	}
	
	// 创建安全指标收集器
	securityMetrics := control.NewSecurityMetrics(m.metricsReg)
	
	// 打开 control store 时传入指标
	if cfg.Control != nil {
		m.control, err = openControlStoreWithMetrics(ctx, cfg.Control, securityMetrics)
		// ...
	}
	
	// ... 其余代码 ...
}
```

---

## 📊 方案 B: 内部日志监控（快速实施）

如果暂不想修改太多代码，可以先通过结构化日志监控：

### 1. 增强现有日志

在关键位置添加结构化日志：

```go
// 在 handler.go 的会话清理处
if err := h.opts.Control.RevokeSession(bgCtx, ss.UserID, ss.ID); err != nil {
	h.opts.Logger.Error("session cleanup failed after auth error",
		zap.String("user_id", ss.UserID),
		zap.String("session_id", ss.ID),
		zap.Error(err),
		zap.String("metric", "session_cleanup_error"),  // 用于监控
	)
}

// 在 mysql_store.go 的事务回滚处
func (s *MySQLStore) withTx(parent context.Context, fn func(context.Context, *sql.Tx) error) error {
	// ... 现有代码 ...
	defer func() {
		if tx != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				// 记录连接泄漏风险
				s.logger.Warn("transaction rollback failed in defer",
					zap.Error(rbErr),
					zap.String("metric", "db_connection_leak_risk"),
				)
			}
		}
	}()
	// ...
}

// 在 handler.go 的限速器中
func (l *ipLimiter) allow(ip string) bool {
	// ... 清理逻辑 ...
	if evicted > 0 {
		log.Info("rate limiter cleanup",
			zap.Int("evicted", evicted),
			zap.Int("remaining", len(l.entries)),
			zap.String("metric", "rate_limiter_cleanup"),
		)
	}
}
```

### 2. 定期健康检查日志

添加定期健康检查：

```go
// 在 store.go 中
func (s *Store) LogHealthMetrics() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	// 会话统计
	sessions, _ := s.countActiveSessions(ctx)
	
	// BoltDB 统计
	stats := s.db.Stats()
	
	log.Info("control_store_health",
		zap.Int("active_sessions", sessions),
		zap.Int("boltdb_open_tx", stats.OpenTxN),
		zap.Int("boltdb_read_tx", stats.TxN),
		zap.String("metric", "health_check"),
	)
}

// 在 MySQL store 中
func (s *MySQLStore) LogHealthMetrics() {
	stats := s.db.Stats()
	
	log.Info("mysql_store_health",
		zap.Int("db_open_conns", stats.OpenConnections),
		zap.Int("db_in_use", stats.InUse),
		zap.Int("db_idle", stats.Idle),
		zap.Int64("db_wait_count", stats.WaitCount),
		zap.Duration("db_wait_duration", stats.WaitDuration),
		zap.String("metric", "health_check"),
	)
}
```

### 3. 启动定期健康检查

```go
// 在 RunMosdnsContext 中
go func() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			if m.control != nil {
				if store, ok := m.control.(*Store); ok {
					store.LogHealthMetrics()
				} else if mysqlStore, ok := m.control.(*MySQLStore); ok {
					mysqlStore.LogHealthMetrics()
				}
			}
		case <-ctx.Done():
			return
		}
	}
}()
```

---

## 🏥 方案 C: 健康检查端点

添加专用的健康检查端点：

### 创建 `internal/controlapi/health.go`

```go
package controlapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type HealthResponse struct {
	Status  string                 `json:"status"`  // "healthy", "degraded", "unhealthy"
	Checks  map[string]HealthCheck `json:"checks"`
	Version string                 `json:"version"`
}

type HealthCheck struct {
	Status  string                 `json:"status"`
	Message string                 `json:"message,omitempty"`
	Metrics map[string]interface{} `json:"metrics,omitempty"`
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	
	resp := HealthResponse{
		Status:  "healthy",
		Checks:  make(map[string]HealthCheck),
		Version: constant.Version,
	}
	
	// 检查数据库连接
	if mysqlStore, ok := h.opts.Control.(*MySQLStore); ok {
		stats := mysqlStore.DB().Stats()
		
		check := HealthCheck{
			Status: "healthy",
			Metrics: map[string]interface{}{
				"open_connections": stats.OpenConnections,
				"in_use":          stats.InUse,
				"idle":            stats.Idle,
				"wait_count":      stats.WaitCount,
			},
		}
		
		// 检测连接泄漏
		if stats.InUse > stats.MaxOpenConnections*8/10 {
			check.Status = "degraded"
			check.Message = "high connection usage"
			resp.Status = "degraded"
		}
		
		resp.Checks["database"] = check
	}
	
	// 检查会话数
	sessionCount, err := h.opts.Control.CountActiveSessions(ctx)
	if err != nil {
		resp.Checks["sessions"] = HealthCheck{
			Status:  "unhealthy",
			Message: err.Error(),
		}
		resp.Status = "unhealthy"
	} else {
		resp.Checks["sessions"] = HealthCheck{
			Status: "healthy",
			Metrics: map[string]interface{}{
				"active_sessions": sessionCount,
			},
		}
	}
	
	w.Header().Set("Content-Type", "application/json")
	if resp.Status != "healthy" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(resp)
}
```

注册端点：

```go
// 在 controlapi 初始化中
mux.HandleFunc("/health", handler.handleHealth)
mux.HandleFunc("/health/ready", handler.handleReadiness)
```

---

## 📈 Grafana 仪表板配置

### 创建 `security-review/grafana-dashboard.json`

```json
{
  "dashboard": {
    "title": "mosdns-x Security Metrics",
    "panels": [
      {
        "title": "Session Cleanup Errors",
        "targets": [
          {
            "expr": "rate(mosdns_control_session_cleanup_errors_total[5m])"
          }
        ],
        "alert": {
          "conditions": [
            {
              "evaluator": {
                "params": [0],
                "type": "gt"
              }
            }
          ]
        }
      },
      {
        "title": "Database Connection Pool",
        "targets": [
          {
            "expr": "mosdns_control_db_connections_in_use",
            "legendFormat": "In Use"
          },
          {
            "expr": "mosdns_control_db_connections_idle",
            "legendFormat": "Idle"
          }
        ]
      },
      {
        "title": "Rate Limiter Memory",
        "targets": [
          {
            "expr": "mosdns_control_rate_limiter_entries"
          }
        ]
      }
    ]
  }
}
```

---

## 🚨 告警规则

### Prometheus 告警配置

创建 `security-review/prometheus-alerts.yml`：

```yaml
groups:
  - name: mosdns_security
    interval: 30s
    rules:
      # 会话清理失败
      - alert: SessionCleanupFailure
        expr: rate(mosdns_control_session_cleanup_errors_total[5m]) > 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Session cleanup failures detected"
          description: "{{ $value }} session cleanup failures per second"

      # 数据库连接池耗尽
      - alert: DatabaseConnectionPoolExhaustion
        expr: |
          mosdns_control_db_connections_in_use / 
          mosdns_control_db_connections_open > 0.9
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Database connection pool nearly exhausted"
          description: "{{ $value | humanizePercentage }} of connections in use"

      # 限速器内存增长
      - alert: RateLimiterMemoryGrowth
        expr: |
          rate(mosdns_control_rate_limiter_entries[10m]) > 10
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Rate limiter memory growing"
          description: "Rate limiter has {{ $value }} entries"

      # 凭证计数不匹配
      - alert: CredentialCountMismatch
        expr: rate(mosdns_control_credential_count_mismatch_total[5m]) > 0
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "Credential count mismatch detected"
          description: "Data integrity issue in credential management"
```

---

## 🧪 验证监控工作正常

### 1. 手动触发测试

```bash
# 检查 Prometheus 指标端点
curl http://localhost:8080/metrics | grep mosdns_control

# 应该看到类似输出：
# mosdns_control_session_cleanup_total 42
# mosdns_control_active_sessions 15
# mosdns_control_db_connections_open 10
# mosdns_control_rate_limiter_entries 234
```

### 2. 触发告警测试

```bash
# 模拟会话清理失败
# （需要在测试环境中断会话清理逻辑）

# 模拟连接池压力
# 运行并发测试工具
go test -run TestConcurrentAuth -race -count=100
```

### 3. 查看 Grafana 仪表板

访问 Grafana：`http://localhost:3000`
- 导入 `grafana-dashboard.json`
- 验证所有面板显示数据
- 检查时间序列是否连续

---

## 📅 实施时间表

### 第1天：基础指标（方案 B）
- ✅ 添加结构化日志
- ✅ 实施定期健康检查
- ✅ 验证日志输出

### 第2天：Prometheus 集成（方案 A 第1部分）
- ✅ 创建 `metrics.go`
- ✅ 集成到 Store
- ✅ 集成到 MySQL Store

### 第3天：完善集成（方案 A 第2部分）
- ✅ 集成限速器指标
- ✅ 添加健康检查端点（方案 C）
- ✅ 测试所有指标

### 第4天：可视化和告警
- ✅ 配置 Grafana 仪表板
- ✅ 设置 Prometheus 告警
- ✅ 端到端测试

---

## 🎯 成功指标

监控系统成功的标志：

### 技术指标
- ✅ 所有安全指标每15秒更新
- ✅ Prometheus `/metrics` 端点响应 < 100ms
- ✅ 告警在2分钟内触发
- ✅ 仪表板实时刷新（无数据空白）

### 业务指标
- ✅ 会话清理失败率 < 0.1%
- ✅ 数据库连接利用率 < 80%
- ✅ 限速器内存 < 4096 条目
- ✅ 凭证计数不匹配 = 0

---

## 🔧 故障排查

### 问题：指标未出现在 Prometheus

**检查**：
```bash
# 验证指标注册
curl http://localhost:8080/metrics | grep mosdns_control

# 检查 Prometheus 配置
cat prometheus.yml | grep mosdns

# 检查日志
grep "metrics" /var/log/mosdns/mosdns.log
```

**解决**：
- 确认 `NewSecurityMetrics` 被调用
- 检查 `reg.MustRegister` 是否 panic
- 验证端口未被防火墙阻止

### 问题：Grafana 无数据

**检查**：
```bash
# 测试 Prometheus 查询
curl 'http://localhost:9090/api/v1/query?query=mosdns_control_active_sessions'

# 检查 Grafana 数据源
curl http://localhost:3000/api/datasources
```

**解决**：
- 在 Grafana 中添加 Prometheus 数据源
- 验证时间范围（"Last 5 minutes"）
- 检查查询语法

### 问题：告警不触发

**检查**：
```bash
# 查看 Prometheus 告警状态
curl http://localhost:9090/api/v1/alerts

# 检查 Alertmanager
curl http://localhost:9093/api/v2/status
```

**解决**：
- 验证告警规则语法
- 检查 `for:` 持续时间
- 确认 Alertmanager 配置

---

## 📖 相关文档

- [Prometheus 最佳实践](https://prometheus.io/docs/practices/)
- [Grafana 仪表板指南](https://grafana.com/docs/grafana/latest/dashboards/)
- [Go 应用监控](https://prometheus.io/docs/guides/go-application/)

---

## 🤝 下一步行动

1. **选择实施方案**：根据环境选择 A、B 或 C
2. **分配资源**：1名开发 + 0.5名运维
3. **创建分支**：`git checkout -b feature/security-monitoring`
4. **逐步实施**：先方案 B，再升级到 A
5. **验证效果**：运行 24 小时后评估

---

**文档维护者**: Claude Fable 5.1  
**最后更新**: 2026-09-14  
**状态**: 待实施

---

**开始实施**: 建议从方案 B 开始，1天内完成基础监控 🚀
