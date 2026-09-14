# 安全与运行监控

本页对应实际实现。`security-review/MONITORING_*.md` 保留原始设计供追溯，不能直接照抄其中占位代码、公开路由或假定存在的配置项。

## 内嵌面板与权限

构建前端后使用 `go build -tags ui`。管理员登录现有面板，进入「系统概览」，即可查看安全与运行监控；不需要第二套 HTML 面板、账号或监听端口。

- JSON API：`GET /api/v1/admin/health`。
- 复用现有管理员会话。未登录返回 401，普通用户返回 403，写操作不受支持。
- 响应设置 `Cache-Control: no-store`。不提供 `/health-data`、`/health-ui` 等公共旁路。
- 面板每 5 秒请求一次快照，不重叠轮询；可以暂停，后台标签页暂停请求，离开页面取消未完成请求。
- 无 UI 构建仍可使用 JSON API、受保护的 `/metrics` 和日志，不包含网页资源。

没有增加 `control.tls` 或其他配置字段。生产 HTTPS 继续使用现有的原生监听器或可信反向代理方案，见 [部署文档](deployment.md)。

## 数据来源与采集限制

控制模式下启动一个监控任务，立即采集一次，此后每分钟采集控制数据库。每次使用 5 秒 context；关闭服务时先取消并等待监控任务，再关闭数据库。API 和 Prometheus 的监控数据来自内存快照，不触发监控扫描；请求鉴权仍照常检查持久化会话。

| 指标 | 实际口径 |
| --- | --- |
| 会话清理 | 登录事务成功、但响应阶段读取用户失败后的会话撤销。尝试数、失败数、总耗时从进程启动累计；不包含正常退出、改密或小时维护清理。无尝试时失败率为 `null`，不是 0%。 |
| IP 限速器 | 面板登录与面板 DNS 查询两个限速器；显示内存保留条目、容量、累计拒绝及清理。占用指标取两个限速器中较高的比例。它不是 DNS 用户配额限速器。 |
| 有效会话数 | 未撤销、未过期、所属账户仍启用的面板会话。服务订阅到期不禁止登录面板，因此不作为失效条件。 |
| MySQL 连接池 | 仅控制库，比例为 `InUse / MaxOpenConnections`，不是除以当前打开连接数。上限为 0 表示无限制，比例不可用。等待次数和耗时为进程内累计值。 |
| MySQL 回滚失败 | 显式事务清理返回非 `sql.ErrTxDone` 的错误次数；不覆盖驱动内部不可见的自动回滚。监控不改变业务错误和事务提交行为。 |
| bbolt 状态 | 监控读事务结束后的打开只读事务数、待复用页数。不是 MySQL 连接池。 |
| 凭证计数一致性 | bbolt 同一只读事务内比较用户计数与 `active_credentials` 索引，并检查索引引用。过期但尚待惰性清理的凭证仍属于该计数，不能误报。指标是计数不一致的用户数，不是全数据库完整性审计。 |
| MySQL 凭证一致性 | MySQL 没有独立计数缓存，该项标记 `not_applicable`，不伪造为 0。 |

bbolt 单次扫描最多处理 50,000 个用户、活动凭证索引和会话条目（合计），逐条检查取消。超过上限、记录损坏或读失败时不返回部分计数，API 显示未知并提供 `scan_limit` 或 `collection_failed`。检查只读，不自动修复或清理记录。

5 秒是传递到存储操作的截止时间，不是所有情况下的硬执行上限：已进入 bbolt 的事务/锁等待不能被 context 强制抢占。没有为超时另外启动可能泄漏的扫描 goroutine。

快照超过 120 秒、时钟回退或采集错误后，相关指标状态为 `unknown`；不继续显示旧指标为正常。未知值在 JSON 中为 `null`（存储详情可能保留旧快照，应同时检查 `storage_status`）。不适用指标标记 `not_applicable`。无数据不等于健康。

阈值沿用方案：清理失败率 1% 警告、5% 异常；连接池 80%/90%；IP 限速器 80%/95%；凭证不一致或意外回滚错误大于 0 为异常。每个警告扣 10 分、异常扣 25 分，最低 0；只在所有适用指标都有数据时给出评分，否则为 `null`。异常优先于警告/未知显示。评分不是安全认证、SLA 或漏洞已修复的证明。

## Prometheus

现有 `/metrics` 新增 `mosdns_control_*` 指标，不另开公开端口。主要包括：

- `session_cleanup_total`、`session_cleanup_errors_total`、`session_cleanup_duration_seconds_total`
- `health_collection_success`、`health_checked_timestamp_seconds`
- `health_metric_available{metric}`、`health_metric_value{metric}`
- `rate_limiter_entries{limiter}`、`rate_limiter_capacity{limiter}`、`rate_limiter_rejected_total{limiter}`、`rate_limiter_evicted_total{limiter}`
- `active_sessions`、`db_connections{state}`、`db_max_open_connections`、`db_wait_total`、`db_wait_seconds_total`、`db_rollback_errors_total`
- `boltdb_open_read_transactions`、`boltdb_pending_pages`

以上名称均带 `mosdns_control_` 前缀。标签只允许固定指标名、`login`/`lookup` 和 `open`/`in_use`/`idle`，不包含用户、设备、域名、IP 或凭证。未知/不适用指标的值不导出；使用对应 available 指标判断缺失，不能补零。

例如：

```promql
increase(mosdns_control_session_cleanup_errors_total[5m]) > 0
mosdns_control_rate_limiter_entries / mosdns_control_rate_limiter_capacity > 0.8
mosdns_control_health_collection_success == 0
absent(mosdns_control_health_collection_success)
```

`/metrics` 仍要求管理员会话 Cookie；DNS Bearer Token 不能用于抓取。自动化抓取需要部署方安全维护管理会话或已有受控认证适配层。本次未增加机器令牌、未部署 Prometheus/Grafana/Alertmanager，不把取消鉴权当作抓取配置步骤。

## 日志与只读脚本

后台每分钟通过实例 logger 记录一次 `security_health`，结构化数据位于 `health` 字段。需要日志级别为 `info`；建议启用现有的 JSON 日志格式：

```yaml
log:
  level: info
  production: true
  file: /var/log/mosdns/mosdns.log
```

目录需由部署方预先创建并限制权限。没有日志文件时也可由 systemd 收集输出；脚本本身读取文件，不自动访问 systemd 或更改日志配置。

```sh
bash security-review/monitoring-script.sh /path/to/mosdns.log
bash security-review/monitoring-script.sh /path/to/mosdns.log --json
bash security-review/monitoring-script.sh /path/to/mosdns.log watch
bash security-review/monitoring-demo.sh
```

脚本使用 Bash 和 Python 3 标准库，不依赖 GNU grep、bc、jq 或终端 `clear`。按带时区的快照时间筛选；支持 JSON 和实际 zap console 日志。默认只选择最近 5 分钟内的最新快照（`--window` 可调整），数据库仍必须在 120 秒内采集。

时间窗口用于检查快照新鲜度，不把进程累计计数冒充窗口增量。最多读取日志末尾 8 MiB；缺失数据、无读权限、旧版本日志、无效值均为未知。退出码：0 正常、1 警告、2 异常、3 未知；watch 每 60 秒读取一次，Ctrl+C 退出。

演示只生成明确标注的模拟数据，使用私有临时目录，退出后清理，不覆盖固定 `/tmp` 文件。脚本不重启服务、不改数据库、不安装定时任务、不发送外部消息。

## 验证命令

```sh
go test -mod=readonly -race ./internal/control ./internal/controlapi ./coremain
go test -mod=readonly -race -p 1 ./...
go vet -mod=readonly ./...
npm --prefix web test
npm --prefix web run build
go test -mod=readonly -tags ui ./web
python3 -m unittest discover -s security-review -p 'monitoring_report_test.py'
```

MySQL 单元测试使用 sqlmock。真实 MySQL 5.7/8.4 的集成测试仍需要独立测试数据库；不要用生产数据库进行故障注入。

### 本次验证记录（2026-09-14）

- Go 1.26.6、Node.js 22.22.2；全仓 `-race -count=1 -p 1`、`go vet`、普通/内嵌 UI 二进制构建及 UI 内嵌测试通过。
- 前端 6 个测试文件、45 项测试通过；类型检查、格式检查及生产构建通过。保留既有 ECharts 约 514 KB 分块的构建提示，未增加图表依赖。
- Python 日志解析 8 项测试、两个 Bash 入口的语法检查通过，覆盖无数据、过期、无效值、时区、异常 JSON 和真实日志格式。
- 隔离回环实例验证：匿名 401、普通用户 403、管理员读取、`no-store`、只读接口、无公开旁路、请求不触发重采集、每分钟更新、Prometheus 指标及实际 JSON 日志解析。
- 同一实例的 DoH URL/Bearer、缓存命中和配额计量通过；正常终止后数据库可重新打开，临时凭证与数据库已清理。
- 浏览器检查管理员系统页的桌面与 390×844 布局、暂停刷新及窄屏说明换行，无浏览器错误。
- 未运行真实 MySQL 5.7/8.4 实例测试，未部署外部监控设施，也未发布或重启生产服务。
