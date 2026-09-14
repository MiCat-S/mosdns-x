# 健康监控代码审查（commit 71b1e76）

> **处置状态**：下列 7 项已在 `ffa37f0` 全部修复并逐项复核；本文保留原始问题描述供追溯，
> 不是待修清单。修复过程中新引入的 2 项回归见文末「回归审查」，已随本次提交修复。
> 实现口径以 [`docs/monitoring.md`](../docs/monitoring.md) 为准。

审查对象：`internal/control/health.go`、`internal/controlapi/health.go`、
`internal/controlapi/health_metrics.go`、`handler.go` 计数器、`coremain/mosdns.go` 生命周期、
`web/src/health-panel.tsx`。

## 验证通过的部分

| 检查项 | 结果 |
| --- | --- |
| `go build ./...` / `go build -tags ui ./...` | 通过 |
| `go vet` / `gofmt` | 无输出 |
| `go test -race ./internal/control/... ./internal/controlapi/...` | 通过 |
| `go test ./...` 全量 | 通过 |
| 前端 `vitest`（45 项）/ `tsc` / `prettier` | 通过 |
| `monitoring_report_test.py`（8 项） | 通过 |

并发、生命周期、Prometheus 注册、凭证一致性误报均已逐一核对，未发现问题：

- `refreshMu` 只在采集时持有，`healthSnapshot`/`Collect`/`recordCleanup` 都不取它，抓取不会被数据库扫描阻塞。
- 锁顺序无环：`health.mu` 只用于短字段拷贝，释放后才取 `ipLimiter.mu`；`Collect` 不接触存储，不会与 `Store.Close()` 死锁。
- `evicted`/`rejected` 在 `ipLimiter.mu` 下读取，`rollbackErrors` 为原子量。
- `maintenanceCtx` 与其使用点在同一 `cfg.Control != nil` 分支内，不会为 nil；`apiHandler.(*controlapi.Handler)` 在 `err != nil` 返回之后执行，不会 panic。
- `shutdown()` 先取消并 `maintenanceWG.Wait()`，之后才 `m.control.Close()`，顺序正确，无 goroutine 泄漏。
- 四个改动 `bActiveCredentials`/`CredentialCount` 的位置都在同一写事务内保持同步；过期未清理的凭证同时存在于计数和索引中，正常过期不会误报。
- 19 个指标名都存在于 `healthDescriptors`，标签数量匹配、取值有界，计数器确实单调，注册表为每实例新建且无双前缀。
- 三个严重安全问题已修复：凭证计数移入单写事务；`cleanupSession` 用 `context.WithoutCancel`；不存在的用户仍执行一次 `verifyPassword`，且 `enabledUser` 检查在其之后。

## 需要修复的问题

### 1. 健康服务器永远显示"数据不足"，评分为 null（影响最大）

`internal/controlapi/health.go:164` 与 `:204`

`recordCleanup` 只有一个调用点：`cleanupSession`，而它只在 `Login` 成功、紧接着 `GetUser`
失败这条罕见错误路径上触发（`handler.go:406`）。正常运行时 `Cleanup.Attempts` 恒为 0，
清理指标停留在 `unknown`，聚合循环里 `case "unknown": complete = false`，于是
`overall_status` 变成 `"unknown"`、`overall_score` 为 `null`。

结果：一台零不一致、刚采集成功、限速器空闲的 bbolt 部署，面板永远显示"数据不足"，
与"无法判断健康"无法区分。想拿到评分必须先发生一次错误。`health_test.go:66`
需要手动调用 `recordCleanup` 才能得到 `score == 100`，正说明了这一点。

`not_applicable` 已正确排除在 `complete` 之外，`no_samples` 没有——这是问题所在。
建议：`no_samples` 与 `not_applicable` 同等对待，不参与 `complete` 判定。

### 2. 进程累计计数器配水平阈值，一次瞬时故障永久锁定 critical

`internal/controlapi/health.go:199`（回滚）与 `:166`（清理失败率）

`p.RollbackErrors` 是 `MySQLStore.rollbackErrors` 这个只增不减的原子量
（`mysql_store.go:450`），`healthMetric` 在 `>= 1` 时判 critical。

MySQL 因 `wait_timeout`、重启或网络抖动丢掉一个池化连接，`withTx` 的 deferred
`tx.Rollback()` 返回非 `sql.ErrTxDone` 错误，计数变 1。此后即使 MySQL 已健康数天，
每次 `/api/v1/admin/health` 和每分钟的 `security_health` 日志都报
`overall_status: "critical"`、`overall_score: 75`，没有衰减、没有窗口、没有重置路径，
只能重启进程。

清理失败率同理：因为触发路径极罕见，现实序列是 1 次尝试 / 1 次失败 = 100%，
直接永久 critical；要降回 1% 警告带以下需要再发生约 100 次同类罕见事件。
`health_test.go:70` 正是这个锁定行为，但未被识别为缺陷。

建议：改用时间窗口速率（如最近 5/15 分钟增量），或对累计计数器只做趋势展示，
由 Prometheus 的 `increase()` 承担告警。

### 3. 限速器占用率被惰性清理锁定

`internal/controlapi/handler.go:1999` 与 `:2021`，消费于 `health.go:171`

过期条目只在 `allow()` 内部清除，`healthSnapshot` 直接上报 `len(l.entries)`。

分布式登录探测把 `limiter.entries` 填到容量 4096 中的 3900，然后流量停止。
此后没有 `allow()` 调用，就没有清理，`3900/4096 = 95.2%` 判 critical，
`overall_status` 永久 critical、扣 25 分——尽管每个条目都已过期数小时，
限速器实际会放行任何请求。即使在活跃系统上，该读数也会被最多
`min(window, 1m)` 的陈旧条目高估。

建议：在 `healthSnapshot` 里按 `now` 过滤未过期条目再计比例，或复用一次清理。

### 4. 50,000 扫描预算硬编码且三个桶共享，大部署永久 scan_limit

`internal/control/health.go:11`、`:45`、`:55`

`remaining` 只初始化一次，`bUsers`、`bActiveCredentials`、`bSessions` 的每条记录都递减它。
无 off-by-one（恰好允许 `limit` 条），无部分状态泄漏，但也没有配置项。

10,000 用户 × 4 个活动凭证 + 10,000 会话 = 60,000 条，每次扫描都中止，
`StorageStatus` 变 `unknown`，而 `Collect` 在 `health_metrics.go:70` 处
`if report.StorageStatus != "healthy" { return }` 直接返回。后果是
`mosdns_control_active_sessions`、`boltdb_*`、`db_*` 完全不出现在抓取结果里，
`health_collection_success` 钉在 0。计数器 `db_wait_total`、`db_rollback_errors_total`
在瞬时故障期间消失又重现，会破坏 `increase()` 的连续性。

建议：预算改为可配置或按桶分配；即使存储扫描失败，也应继续导出与扫描无关的
池/限速器指标。

### 5. 一个悬空索引项让整次采集变成硬失败

`internal/control/health.go:93`

`decode(tx.Bucket(bCredentials).Get(id), &c)` 对悬空索引项返回 `ErrNotFound`，
这是域错误，`Store.view` 原样透传，`refreshHealth` 映射为 `collection_failed`。

期望：`credential_count_mismatches >= 1` 判 critical，明确告知哪里损坏。
实际：该字段为 `nil`，`storage_status` 为 `unknown`，一致性指标读作 `unknown`，
`active_sessions` 和 bbolt 统计一并从 Prometheus 消失。这个扫描本该检测的
唯一一类损坏，反而产出最不可操作的输出，且没有修复路径所以是永久的。
`health_test.go:91` 把它写成了预期行为。

建议：把悬空索引单独计入 mismatch 并继续扫描，而不是中止整次采集。

### 6. Prometheus 无法区分 not_applicable 与 unknown，且不导出总体状态

`internal/controlapi/health_metrics.go:56`

`health_metric_available` 对"本后端不适用"和"采集失败"都是 0，`Status`
从不作为标签导出，`overall_status`/`overall_score` 完全不导出。
每个 bbolt 部署上 `health_metric_available{metric="db_connections"}` 和
`{metric="transaction_rollback"}` 恒为 0，显然的告警
`health_metric_available == 0` 会一直触发；而真正 critical 的
`credential_count = 3` 只有把阈值 1 硬编码进规则的人才看得到。
上面 1、2、3 三个问题对抓取端因此都是不可见的。

### 7. 陈旧判定使用墙钟时间（次要）

`internal/controlapi/health.go:105`、`:148`、`:158`

`h.opts.Now().UTC()` 剥掉了 Go 的单调时钟读数，`now.Before(*checkedAt)` 与
`now.Sub(*checkedAt) > 2*healthInterval` 都是纯墙钟比较。NTP 向后跳变、
虚拟机挂起恢复、或首次 NTP 同步向前跳超过两分钟，都会让 `storage_status`
翻成 `unknown`/`stale`，把 `health_collection_success` 打到 0，
并在最多一分钟内抑制存储相关指标。一个周期后自愈，严重度低。

## 修复优先级

1. 问题 1（健康服务器显示 unknown）——面板可用性前提
2. 问题 2（累计计数器永久 critical）——误报根源
3. 问题 3（限速器占用锁定）——同类误报
4. 问题 5（悬空索引掩盖真实损坏）——检测能力
5. 问题 4（扫描预算）——随规模恶化
6. 问题 6（Prometheus 语义）——告警可用性
7. 问题 7（墙钟）——可选

## 回归审查（`868ccf1..ffa37f0`）

对上述 7 项修复做了对抗性回归审查。以下四类假设均未发现新缺陷：

- **索引键解析误报**：`bytes.Cut(k, []byte{0})` 与 `userCredentialKey` 严格互逆，用户 ID 和
  凭证 ID 都来自 `randomText(16)` 的 base64url，字母表不含 0 字节，且没有任何路径把
  运维输入写进这两个位置。`u == nil` 需要用户记录被删而凭证留存，但包内不存在
  `DeleteUser`，用户只能停用。三个新增的严格检查比修复前更弱：旧代码检测同样条件
  但直接中断整次扫描。
- **`monotonicNow` 空指针**：只在 `handler.go` 的 `New` 里赋值，包内其它 `&Handler{}`
  字面量都在测试中且不走健康路径；零值 `Handler` 会更早在 `h.opts.Now()` 上崩溃。
- **Prometheus 重复标签组合**：`health_metric_status` 是固定 5 键 × 7 状态，
  `report.Metrics` 为硬编码字面量，限速器名硬编码；7 个状态枚举覆盖全部可赋值状态；
  `oneHot` 里每个调用点 `len == cap`，`append` 必然新分配，无切片别名。
- **`storage_age_seconds` 边界**：与 `StorageCheckedAt` 在同一 `mu` 临界区成对读写，
  所有解引用都有前置判空，Python 侧 `finite_number(None)` 先做 `isinstance` 检查。

另外发现并修复 2 项回归：

### R1. 连接池无上限时评分被永久压掉

`internal/controlapi/health.go:226`

`MaxOpen == 0` 分支只设 `pool.Reason = "unlimited_pool"`，`Status` 留在 `healthMetric`
初始化的 `"unknown"`，于是评分循环里 `complete = false`，`overall_score` 恒为 `null`、
`overall_status` 被强制成 `unknown`——正是问题 1 要消灭的那一类缺陷。同一函数里其它
真正不适用的项（`bbolt_backend`、`no_materialized_count`）都给了 `not_applicable`，
这个分支与兄弟分支不一致。今天从 `coremain` 不可达，因为 `OpenMySQLContext` 在
`MaxOpenConns <= 0` 时强制改成 32；原测试只断言 `Value`/`Reason`，没有守住评分。

修复：改为 `pool.Status, pool.Reason = "not_applicable", "unlimited_pool"`，
测试补断言此时仍为 `healthy / 100`。

### R2. 健康接口与每次抓取会排在 `Store.Close()` 之后

`internal/controlapi/health.go:182`、`internal/control/health.go:173`

`ffa37f0` 新增的 `RuntimeHealth()` 取 `Store.mu` 读锁。修复前 HTTP 处理与 `Collect`
完全不碰存储锁——本文第 21 行"`Collect` 不接触存储，不会与 `Store.Close()` 死锁"
对 `71b1e76` 成立，正是这次修复打破了它。

场景：`Backup` 为整库写盘长时间持有读锁，此时进入关机，`Close()` 的 `s.mu.Lock()`
排队；Go 的 `RWMutex` 给等待中的写者优先，后续所有 `RLock()` 阻塞，并发的
`/api/v1/admin/health` 与 Prometheus 抓取都卡在 `RuntimeHealth()` 里直到备份结束。
后果是延迟／抓取超时，不是死锁。

修复：`Store` 增加 `closedFlag atomic.Bool`，在 `Close()` 持有 `mu` 时与 `s.closed`
一同置位；`RuntimeHealth` 改读原子标志，不再进锁。`s.closed` 仍是权威值、仍由 `mu`
保护，保证 `Close` 之后不会有新事务开始，与 `MySQLStore` 已有的模式一致。
bbolt 的 `db.stats` 指针只在 `Open` 时赋值、`close()` 里不置 nil，`Stats()` 自带
`statlock`，所以关闭正好落在标志读取之后时只返回最后一次统计，不会 panic 也无竞争。

两个新测试都经过反向验证：把实现临时回退后分别以"blocked behind a pending Close"和
"unlimited pool suppressed the overall score"失败。全仓
`go test -race -count=1 -p 1 ./...`、`go vet`、`gofmt`、普通与 `-tags ui` 构建、
前端 46 项测试、Python 11 项测试均通过。未运行真实 MySQL 集成测试。
