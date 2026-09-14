# 监控指标快速启动指南

> 原始方案已被实际实现取代。请先阅读 [实际监控说明](../docs/monitoring.md)，无需再复制下文 Go 占位代码。脚本已改为读取 `security_health` 快照，缺失数据不会评分为健康。

**预计时间**: 1-2小时  
**难度**: ⭐⭐ (中等)  
**推荐**: 从这里开始，最小化侵入式实施

---

## 🎯 目标

快速建立基础监控，验证以下5项安全修复的效果：

1. ✅ 会话清理（是否有泄漏）
2. ✅ 事务回滚（连接池健康）
3. ✅ 限速器内存（是否增长）
4. ✅ Socket 权限（启动日志验证）
5. ✅ DNS 响应处理（错误率）

---

## 📋 方案：轻量级日志监控

**优点**：
- 无需修改大量代码
- 无需额外基础设施（Prometheus/Grafana）
- 1-2 小时完成
- 立即可用

**缺点**：
- 需要手动查看日志
- 无可视化界面
- 不适合长期生产环境

---

## 🚀 第1步：添加健康检查日志 (30分钟)

### 1.1 创建健康检查函数

创建文件 `internal/control/health.go`：

```go
package control

import (
	"context"
	"time"
	
	"go.uber.org/zap"
)

// HealthMetrics 健康检查指标
type HealthMetrics struct {
	ActiveSessions   int           `json:"active_sessions"`
	CredentialCount  uint32        `json:"credential_count"`
	BoltDBOpenTx     int           `json:"boltdb_open_tx"`
	BoltDBPendingPages int         `json:"boltdb_pending_pages"`
	CheckedAt        time.Time     `json:"checked_at"`
}

// CollectHealthMetrics 收集健康指标
func (s *Store) CollectHealthMetrics(ctx context.Context) HealthMetrics {
	metrics := HealthMetrics{
		CheckedAt: s.clock.Now(),
	}
	
	// 统计活跃会话
	_ = s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bSessions)
		c := b.Cursor()
		count := 0
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var sr sessionRecord
			if decode(v, &sr) == nil && sr.RevokedAt.IsZero() {
				count++
			}
		}
		metrics.ActiveSessions = count
		return nil
	})
	
	// BoltDB 统计
	stats := s.db.Stats()
	metrics.BoltDBOpenTx = stats.OpenTxN
	metrics.BoltDBPendingPages = stats.PendingPageN
	
	return metrics
}

// LogHealthMetrics 定期记录健康指标到日志
func (s *Store) LogHealthMetrics(logger *zap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	metrics := s.CollectHealthMetrics(ctx)
	
	logger.Info("control_store_health",
		zap.Int("active_sessions", metrics.ActiveSessions),
		zap.Int("boltdb_open_tx", metrics.BoltDBOpenTx),
		zap.Int("boltdb_pending_pages", metrics.BoltDBPendingPages),
		zap.Time("checked_at", metrics.CheckedAt),
	)
}
```

### 1.2 为 MySQL 添加健康检查

创建文件 `internal/control/mysql_health.go`：

```go
package control

import (
	"database/sql"
	"time"
	
	"go.uber.org/zap"
)

// MySQLHealthMetrics MySQL 健康指标
type MySQLHealthMetrics struct {
	OpenConnections int           `json:"open_connections"`
	InUse           int           `json:"in_use"`
	Idle            int           `json:"idle"`
	WaitCount       int64         `json:"wait_count"`
	WaitDuration    time.Duration `json:"wait_duration"`
	CheckedAt       time.Time     `json:"checked_at"`
}

// CollectHealthMetrics 收集 MySQL 健康指标
func (s *MySQLStore) CollectHealthMetrics() MySQLHealthMetrics {
	stats := s.db.Stats()
	
	return MySQLHealthMetrics{
		OpenConnections: stats.OpenConnections,
		InUse:          stats.InUse,
		Idle:           stats.Idle,
		WaitCount:      stats.WaitCount,
		WaitDuration:   stats.WaitDuration,
		CheckedAt:      s.clock.Now(),
	}
}

// LogHealthMetrics 记录健康指标到日志
func (s *MySQLStore) LogHealthMetrics(logger *zap.Logger) {
	metrics := s.CollectHealthMetrics()
	
	// 计算连接池使用率
	var utilizationPct float64
	if metrics.OpenConnections > 0 {
		utilizationPct = float64(metrics.InUse) / float64(metrics.OpenConnections) * 100
	}
	
	logger.Info("mysql_store_health",
		zap.Int("open_connections", metrics.OpenConnections),
		zap.Int("in_use", metrics.InUse),
		zap.Int("idle", metrics.Idle),
		zap.Float64("utilization_pct", utilizationPct),
		zap.Int64("wait_count", metrics.WaitCount),
		zap.Duration("wait_duration", metrics.WaitDuration),
		zap.Time("checked_at", metrics.CheckedAt),
	)
	
	// 警告：连接池使用率高
	if utilizationPct > 80 {
		logger.Warn("mysql_connection_pool_high_utilization",
			zap.Float64("utilization_pct", utilizationPct),
			zap.String("recommendation", "consider increasing max_open_connections"),
		)
	}
	
	// 警告：大量等待
	if metrics.WaitCount > 1000 {
		logger.Warn("mysql_connection_pool_high_wait_count",
			zap.Int64("wait_count", metrics.WaitCount),
			zap.Duration("total_wait", metrics.WaitDuration),
		)
	}
}
```

---

## 🔧 第2步：集成到主程序 (20分钟)

### 2.1 修改 `coremain/mosdns.go`

在 `RunMosdnsContext` 函数中添加定期健康检查：

```go
func RunMosdnsContext(ctx context.Context, cfg *Config) (retErr error) {
	// ... 现有代码 ...
	
	// 启动健康检查协程
	if m.control != nil {
		go m.runHealthCheck(ctx)
	}
	
	// ... 其余代码 ...
}

// 新增函数：定期健康检查
func (m *Mosdns) runHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)  // 每60秒检查一次
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			m.logHealthMetrics()
		case <-ctx.Done():
			return
		}
	}
}

func (m *Mosdns) logHealthMetrics() {
	// BoltDB Store
	if store, ok := m.control.(*control.Store); ok {
		store.LogHealthMetrics(m.logger)
	}
	
	// MySQL Store
	if mysqlStore, ok := m.control.(*control.MySQLStore); ok {
		mysqlStore.LogHealthMetrics(m.logger)
	}
}
```

---

## 📊 第3步：增强关键操作日志 (30分钟)

### 3.1 会话清理日志

修改 `internal/controlapi/handler.go`，在会话清理处添加日志：

```go
// 在 authenticate 函数中，会话创建失败时的清理
u, err := h.opts.Control.GetUser(r.Context(), ss.UserID)
if err != nil {
	// 记录清理开始
	cleanupStart := time.Now()
	revokeErr := h.opts.Control.RevokeSession(context.Background(), ss.UserID, ss.ID)
	cleanupDuration := time.Since(cleanupStart)
	
	// 记录清理结果
	if revokeErr != nil {
		h.opts.Logger.Error("session_cleanup_failed",
			zap.String("user_id", ss.UserID),
			zap.String("session_id", ss.ID),
			zap.Error(revokeErr),
			zap.Duration("cleanup_duration", cleanupDuration),
			zap.String("trigger", "auth_failure"),
			zap.String("metric", "session_leak_risk"),  // 用于监控
		)
	} else {
		h.opts.Logger.Debug("session_cleanup_success",
			zap.String("session_id", ss.ID),
			zap.Duration("cleanup_duration", cleanupDuration),
		)
	}
	
	h.clearCookie(w)
	h.serviceError(w, err)
	return
}
```

### 3.2 事务回滚日志

修改 `internal/control/mysql_store.go`：

```go
func (s *MySQLStore) withTx(parent context.Context, fn func(context.Context, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return mysqlStoreError(err)
	}
	
	// 使用 defer 确保回滚
	defer func() {
		if tx != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				// 记录潜在的连接泄漏
				s.logger.Warn("transaction_rollback_failed",
					zap.Error(rbErr),
					zap.String("metric", "db_connection_leak_risk"),
				)
			}
		}
	}()
	
	if err = fn(ctx, tx); err != nil {
		return mysqlStoreError(err)
	}
	
	if err = tx.Commit(); err != nil {
		return mysqlStoreError(err)
	}
	
	// 提交成功，标记 tx 为 nil 避免 defer 中回滚
	tx = nil
	return nil
}
```

### 3.3 限速器内存监控

修改 `internal/controlapi/handler.go` 中的 `ipLimiter`：

```go
func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	
	now := time.Now()
	
	// 定期清理和日志（每100次调用）
	if len(l.entries) > 0 && len(l.entries)%100 == 0 {
		evicted := 0
		for k, e := range l.entries {
			if now.Sub(e.start) >= l.window {
				delete(l.entries, k)
				evicted++
			}
		}
		
		// 记录清理结果
		if evicted > 0 || len(l.entries) > l.capacity/2 {
			log.Info("rate_limiter_cleanup",
				zap.Int("evicted", evicted),
				zap.Int("remaining", len(l.entries)),
				zap.Int("capacity", l.capacity),
				zap.Float64("utilization_pct", float64(len(l.entries))/float64(l.capacity)*100),
				zap.String("metric", "rate_limiter_memory"),
			)
		}
	}
	
	// 警告：接近容量上限
	if len(l.entries) >= l.capacity*9/10 {
		log.Warn("rate_limiter_near_capacity",
			zap.Int("entries", len(l.entries)),
			zap.Int("capacity", l.capacity),
			zap.String("metric", "rate_limiter_saturation"),
		)
	}
	
	// ... 现有限速逻辑 ...
}
```

---

## 📖 第4步：查看和分析日志 (10分钟)

### 4.1 实时监控日志

```bash
# 查看所有健康检查日志
tail -f /var/log/mosdns/mosdns.log | grep "health"

# 查看会话清理失败
tail -f /var/log/mosdns/mosdns.log | grep "session_cleanup_failed"

# 查看数据库连接问题
tail -f /var/log/mosdns/mosdns.log | grep "connection"

# 查看限速器状态
tail -f /var/log/mosdns/mosdns.log | grep "rate_limiter"
```

### 4.2 使用 jq 分析结构化日志（如果使用 JSON 格式）

```bash
# 统计会话清理失败次数
grep "session_cleanup_failed" mosdns.log | jq -r '.msg' | wc -l

# 查看平均数据库连接使用率
grep "mysql_store_health" mosdns.log | jq -r '.utilization_pct' | \
  awk '{sum+=$1; count++} END {print "Avg:", sum/count "%"}'

# 查看限速器峰值使用
grep "rate_limiter" mosdns.log | jq -r '.remaining' | sort -n | tail -1
```

### 4.3 创建简单的监控脚本

创建 `scripts/monitor-security.sh`：

```bash
#!/bin/bash

LOG_FILE="/var/log/mosdns/mosdns.log"
WINDOW_MINUTES=5

echo "=== mosdns-x 安全监控报告 ==="
echo "时间窗口: 最近 ${WINDOW_MINUTES} 分钟"
echo ""

# 会话清理失败
echo "1. 会话清理失败:"
count=$(grep -c "session_cleanup_failed" "$LOG_FILE" 2>/dev/null || echo 0)
if [ "$count" -gt 0 ]; then
  echo "   ⚠️  发现 $count 次失败（可能存在会话泄漏）"
else
  echo "   ✅ 无失败"
fi

# 数据库连接池
echo ""
echo "2. 数据库连接池:"
utilization=$(tail -100 "$LOG_FILE" | grep "mysql_store_health" | \
  tail -1 | grep -oP 'utilization_pct":\K[0-9.]+' || echo "N/A")
if [ "$utilization" != "N/A" ]; then
  if (( $(echo "$utilization > 80" | bc -l) )); then
    echo "   ⚠️  使用率: ${utilization}% (高)"
  else
    echo "   ✅ 使用率: ${utilization}%"
  fi
else
  echo "   ℹ️  暂无数据"
fi

# 限速器内存
echo ""
echo "3. 限速器内存:"
entries=$(tail -100 "$LOG_FILE" | grep "rate_limiter" | \
  tail -1 | grep -oP 'remaining":\K[0-9]+' || echo "N/A")
if [ "$entries" != "N/A" ]; then
  echo "   ℹ️  当前条目: $entries"
else
  echo "   ℹ️  暂无数据"
fi

# 事务回滚失败
echo ""
echo "4. 事务回滚:"
rollback_fails=$(grep -c "transaction_rollback_failed" "$LOG_FILE" 2>/dev/null || echo 0)
if [ "$rollback_fails" -gt 0 ]; then
  echo "   ⚠️  发现 $rollback_fails 次回滚失败（连接泄漏风险）"
else
  echo "   ✅ 无失败"
fi

echo ""
echo "=== 报告结束 ==="
```

使用：

```bash
chmod +x scripts/monitor-security.sh
./scripts/monitor-security.sh

# 定期运行
watch -n 60 ./scripts/monitor-security.sh
```

---

## 🧪 第5步：验证监控工作 (10分钟)

### 5.1 触发测试事件

```bash
# 1. 正常使用，观察日志
curl -X POST http://localhost:8080/api/login \
  -d '{"username":"test","password":"test"}'

# 2. 等待60秒，应该看到健康检查日志
sleep 60

# 3. 检查日志
tail -20 /var/log/mosdns/mosdns.log | grep "health"
```

### 5.2 预期输出

应该看到类似：

```json
{
  "level":"info",
  "ts":"2026-09-14T14:30:00.123Z",
  "msg":"control_store_health",
  "active_sessions":15,
  "boltdb_open_tx":0,
  "boltdb_pending_pages":0,
  "checked_at":"2026-09-14T14:30:00Z"
}

{
  "level":"info",
  "ts":"2026-09-14T14:30:00.456Z",
  "msg":"mysql_store_health",
  "open_connections":10,
  "in_use":2,
  "idle":8,
  "utilization_pct":20.0,
  "wait_count":0,
  "wait_duration":0,
  "checked_at":"2026-09-14T14:30:00Z"
}
```

---

## 🎯 成功指标

完成后，你应该能够：

- ✅ 每60秒看到一次健康检查日志
- ✅ 会话清理失败时立即看到错误日志
- ✅ 数据库连接使用率超过80%时看到警告
- ✅ 限速器接近容量时看到警告
- ✅ 使用监控脚本快速获取状态概览

---

## 📊 后续升级路径

当需要更强大的监控时：

1. **第2周**: 升级到 Prometheus（参考 `MONITORING_IMPLEMENTATION.md` 方案 A）
2. **第3周**: 添加 Grafana 仪表板
3. **第4周**: 配置告警规则
4. **持续**: 根据实际情况调整阈值

---

## 🔍 故障排查

### 问题：看不到健康检查日志

**检查**:
```bash
# 验证日志级别
grep "log_level" /etc/mosdns/config.yaml

# 检查日志文件权限
ls -l /var/log/mosdns/mosdns.log

# 手动触发（需要在代码中添加测试端点）
curl http://localhost:8080/debug/health
```

### 问题：日志太多

**解决**:
```bash
# 减少健康检查频率（从60秒改为300秒）
# 在 mosdns.go 中修改：
time.NewTicker(300 * time.Second)

# 或者提高日志级别为 WARN
# 在 config.yaml 中：
log:
  level: warn
```

---

## 📝 检查清单

实施前：
- [ ] 备份当前代码
- [ ] 创建 git 分支：`git checkout -b feature/basic-monitoring`
- [ ] 确认日志文件路径和权限

实施中：
- [ ] 创建 `internal/control/health.go`
- [ ] 创建 `internal/control/mysql_health.go`
- [ ] 修改 `coremain/mosdns.go` 添加健康检查
- [ ] 修改关键操作添加日志
- [ ] 创建监控脚本

实施后：
- [ ] 编译并测试：`go build ./cmd/mosdns`
- [ ] 运行5分钟，观察日志
- [ ] 运行监控脚本验证
- [ ] 提交代码：`git commit -am "feat: add basic security monitoring"`

---

## ⏱️ 时间分配

- 📝 阅读本文档: 10 分钟
- 💻 编写健康检查代码: 30 分钟
- 🔧 集成到主程序: 20 分钟
- 📊 增强关键日志: 30 分钟
- 🧪 测试和验证: 10 分钟

**总计**: 约 1.5-2 小时

---

## 🎉 完成后

恭喜！你现在有了基础的安全监控。

**下一步**:
1. 运行 24 小时观察
2. 根据实际情况调整频率和阈值
3. 考虑升级到 Prometheus（见 `MONITORING_IMPLEMENTATION.md`）

---

**文档创建**: 2026-09-14  
**预计更新**: 定期根据反馈优化

如有问题，请参考 `MONITORING_IMPLEMENTATION.md` 了解完整方案。
