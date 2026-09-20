# 安全与运行监控

本页是监控功能的唯一权威文档，描述已实现的行为。早期的设计草稿与审查报告已从仓库移除：它们含有占位代码、并不存在的公开路由和假定的配置项，照抄会得到跑不起来的配置。

## 内嵌面板与权限

构建前端后使用 `go build -tags ui`。管理员登录现有面板，进入「系统概览」，即可查看安全与运行监控；不需要第二套 HTML 面板、账号或监听端口。

- JSON API：`GET /api/v1/admin/health`。
- 复用现有管理员会话。未登录返回 401，普通用户返回 403，写操作不受支持。
- 响应设置 `Cache-Control: no-store`。不提供 `/health-data`、`/health-ui` 等公共旁路。
- 面板每 5 秒请求一次快照，不重叠轮询；可以暂停，后台标签页暂停请求，离开页面取消未完成请求。
- 无 UI 构建仍可使用 JSON API、受保护的 `/metrics` 和日志，不包含网页资源。

没有增加 `control.tls`。生产 HTTPS 继续使用现有的原生监听器或可信反向代理方案，见 [部署文档](deployment.md)。bbolt 扫描预算使用下面的 `control.health_scan_limit`。

## 数据来源与采集限制

控制模式下启动一个监控任务，立即采集一次，此后每分钟采集控制数据库。每次使用 5 秒 context；关闭服务时先取消并等待监控任务，再关闭数据库。API 和 Prometheus 使用缓存的扫描结果，并独立读取连接池、bbolt 和限速器的当前内存统计，不触发监控 SQL、数据库扫描或事务；请求鉴权仍照常检查持久化会话。

运行统计读取不获取存储读写锁：bbolt 侧改用原子关闭标志加 bbolt 自身的统计锁。备份等长事务持有存储读锁、同时有关闭在排队时，`sync.RWMutex` 会让等待中的写者优先，若此处再取读锁，健康接口与抓取就会一直阻塞到该事务结束。关闭后不再返回 bbolt 统计，也不补零。

| 指标 | 实际口径 |
| --- | --- |
| 会话清理 | 登录事务成功、但响应阶段读取用户失败后的会话撤销。尝试数、失败数、总耗时从进程启动累计；不包含正常退出、改密或小时维护清理。无尝试时失败率为 `null`，不是 0%。 |
| IP 限速器 | 面板登录与面板 DNS 查询两个限速器；`entries` 是内存保留数，`active_entries` 是在限速器锁内按当前窗口过滤后的未过期数。占用指标取两个限速器中较高的 `active_entries / capacity`，过期且未清理的条目不参与评分。读取不修改条目或计数；它不是 DNS 用户配额限速器。 |
| 有效会话数 | 未撤销、未过期、所属账户仍启用的面板会话。服务订阅到期不禁止登录面板，因此不作为失效条件。 |
| MySQL 连接池 | 仅控制库，比例为 `InUse / MaxOpenConnections`，不是除以当前打开连接数。上限为 0 表示无限制，比例不可用。等待次数和耗时为进程内累计值。 |
| MySQL 回滚失败 | 显式事务清理返回非 `sql.ErrTxDone` 的错误次数，进程累计值；不覆盖驱动内部不可见的自动回滚。仅作历史参考，近期故障由 Prometheus 窗口增量告警。监控不改变业务错误和事务提交行为。 |
| bbolt 状态 | 当前打开只读事务数、待复用页数，可能包含正在执行的监控读事务。不是 MySQL 连接池，也不代表数据库连通性检测。 |
| 凭证一致性异常 | bbolt 同一只读事务内比较用户计数与 `active_credentials` 索引，并检查索引引用。过期但尚待惰性清理的凭证仍属于该计数，不能误报。计数/引用损坏的同一用户只记一次；无法归属到现存用户的索引每条另记一次。不是全数据库完整性审计。 |
| MySQL 凭证一致性 | MySQL 没有独立计数缓存，该项标记 `not_applicable`，不伪造为 0。 |

bbolt 默认单次扫描最多处理 50,000 个用户、活动凭证索引和会话条目（合计），逐条检查取消。大型部署可在现有 `control` 中增加：

```yaml
control:
  # 保留已有 database、public_dns_url 等配置
  health_scan_limit: 100000
```

单位是条目数；省略或 0 使用默认 50,000，显式预算允许 1～1,000,000，负值或超过上限会拒绝启动。该预算仅作用于 bbolt，MySQL 不使用此项，但配置仍检查取值范围。修改后需重启，不属于在线运行配置编辑，也不改变数据库 schema。10,000 用户、40,000 个活动索引、10,000 会话合计 60,000 条，需高于默认的预算；调高预算同时增加扫描耗时和内存需求。

超过预算、用户/会话记录损坏或读失败时不返回部分扫描计数，API 显示 `unknown` 并提供 `scan_limit` 或 `collection_failed`。独立的驱动运行统计仍可用，不因扫描失败而丢失。活动索引指向缺失、无法解码、归属/ID 不一致或已撤销的凭证时，计入一致性异常并继续扫描，而不是让一次已完成扫描变成未知。检查只读，不自动修复或清理记录。

兼容说明：JSON 字段 `credential_count_mismatches` 和指标 key `credential_count` 保留原名，但从“计数不一致用户数”扩展为“计数/引用异常用户数 + 无法归属的索引条目数”，面板单位改为“处”。消费者不能将它理解为精确的损坏凭证总数。

5 秒是传递到存储操作的截止时间，不是所有情况下的硬执行上限：已进入 bbolt 的事务/锁等待不能被 context 强制抢占。没有为超时另外启动可能泄漏的扫描 goroutine。

扫描完成时保留单调时钟读数，`storage_age_seconds` 表示从该时刻起的经过秒数；`timestamp`、`storage_checked_at` 仅作墙钟展示。NTP 向前或向后跳变不影响扫描陈旧判断。超过 120 秒或采集错误后，扫描相关指标状态为 `unknown`；不继续显示旧扫描值为正常。

JSON 中 `storage_status` 表示缓存扫描是否成功且新鲜，`runtime_status` 独立表示驱动内存统计是否可读；后者不是数据库连通性结论。`metrics[].value` 的未知值为 `null`；`storage` 详情可能保留旧扫描值，消费者必须按对应状态判断。扫描过期/失败不抑制实时池/bbolt 数据，无法读取运行统计时则不补零。

| 单项状态 | 含义与评分 |
| --- | --- |
| `healthy` / `warning` / `critical` | 当前观测值正常 / 达到警告阈值 / 达到异常阈值 |
| `unknown` | 适用数据未采集、过期、无效或采集失败，阻断完整评分 |
| `not_applicable` | 本后端或当前配置不适用（例如连接池未设上限，无占用比例可算），不阻断评分，不伪造 0 |
| `no_samples` | 尚无会话清理尝试，失败率保持 `null`，不阻断评分 |
| `info` | 累计清理失败率、累计 MySQL 回滚失败，仅作历史参考，不扣分 |

当前阈值：连接池 80%/90%；IP 限速器有效占用 80%/95%；凭证一致性异常大于 0 为异常。每个警告扣 10 分、异常扣 25 分，最低 0；仅当扫描和运行统计可用且没有 `unknown` 单项时给出评分，否则为 `null`。异常优先于警告/未知显示。正常 bbolt 实例即使从未触发会话清理，也可为 `healthy / 100`。连接池未设上限时该项为 `not_applicable` 而不是 `unknown`，同样不影响评分。

本次选择审查建议中的“累计计数仅展示、由 `increase()` 告警”路径，不在服务端新增滚动时间窗。一次历史清理/回滚失败不会永久把当前健康锁为异常；面板仍保留累计值。需要近期故障通知时，应配置下面的窗口告警。评分不是安全认证、SLA 或漏洞已修复的证明。

## Prometheus

现有 `/metrics` 新增 `mosdns_control_*` 指标，不另开公开端口。主要包括：

- `session_cleanup_total`、`session_cleanup_errors_total`、`session_cleanup_duration_seconds_total`
- `health_collection_success`、`health_checked_timestamp_seconds`、`health_scan_age_seconds`、`health_runtime_available`
- `health_metric_available{metric}`、`health_metric_value{metric}`、`health_metric_status{metric,status}`
- `health_overall_status{status}`、`health_score`、`health_score_available`
- `rate_limiter_entries{limiter}`、`rate_limiter_active_entries{limiter}`、`rate_limiter_capacity{limiter}`、`rate_limiter_rejected_total{limiter}`、`rate_limiter_evicted_total{limiter}`
- `active_sessions`、`db_connections{state}`、`db_max_open_connections`、`db_wait_total`、`db_wait_seconds_total`、`db_rollback_errors_total`
- `boltdb_open_read_transactions`、`boltdb_pending_pages`

以上名称均带 `mosdns_control_` 前缀。标签只允许固定指标名、`login`/`lookup`、`open`/`in_use`/`idle` 和固定状态枚举，不包含用户、设备、域名、IP 或凭证。`health_metric_status` 导出上述 7 个状态的 one-hot 值（仅当前状态为 1，其余为 0），`health_overall_status` 导出 `healthy/warning/critical/unknown` 四态。评分未知时不导出 `health_score`，同时 `health_score_available=0`。

`health_metric_available` 只表示是否有数值，不区分未知、不适用、暂无样本；**不要以 `health_metric_available == 0` 笼统告警**。未知/不适用/暂无样本均不导出单项 value，不能补零。`info` 仍导出观测值，但不是当前异常。

`active_sessions` 仅在扫描成功且新鲜时导出；连接池、bbolt 和数据库累计计数器由 `health_runtime_available` 决定，扫描超预算、SQL 扫描失败或快照过期期间仍持续导出可读的运行统计。

例如：

```promql
increase(mosdns_control_session_cleanup_errors_total[5m]) > 0
increase(mosdns_control_db_rollback_errors_total[5m]) > 0
mosdns_control_health_metric_status{status="critical"} == 1
mosdns_control_health_metric_status{status="unknown"} == 1
mosdns_control_rate_limiter_active_entries / mosdns_control_rate_limiter_capacity > 0.8
mosdns_control_health_collection_success == 0
absent(mosdns_control_health_collection_success)
```

这些是独立的 PromQL 表达式，可按运维要求配置持续时间和通知；5 分钟可改为 15 分钟。`increase()` 需要连续抓取的样本，不能仅凭进程启动后的第一个累计读数推断近期增量。

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

脚本使用 Bash 和 Python 3 标准库，不依赖 GNU grep、bc、jq 或终端 `clear`。按带时区的快照时间筛选；支持 JSON 和实际 zap console 日志。默认只选择最近 5 分钟内的最新快照（`--window` 可调整），数据库仍必须在 120 秒内采集。新版日志优先使用服务端 `storage_age_seconds`，加上日志事件之后的经过时间；旧格式缺少该字段时回退到 `storage_checked_at`。离线脚本仍依赖读日志主机的墙钟来判断事件本身是否过期，不能保证跨主机时钟漂移场景下准确。

脚本区分 `runtime_status` 与扫描状态；扫描失败时保留仍有效的池指标和历史累计值。`no_samples`、`info` 不阻断完整评分，也不扣分；本次修复前日志仍按原记录的状态解释，不会重写历史 `unknown/critical`。

时间窗口用于检查快照新鲜度，不把进程累计计数冒充窗口增量。最多读取日志末尾 8 MiB；缺失数据、无读权限、缺少健康字段的旧版本日志、无效值均为未知。退出码：0 正常、1 警告、2 异常、3 未知；watch 每 60 秒读取一次，Ctrl+C 退出。

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

### 初次实现验证记录（71b1e76，2026-09-14）

- Go 1.26.6、Node.js 22.22.2；全仓 `-race -count=1 -p 1`、`go vet`、普通/内嵌 UI 二进制构建及 UI 内嵌测试通过。
- 前端 6 个测试文件、45 项测试通过；类型检查、格式检查及生产构建通过。保留既有 ECharts 约 514 KB 分块的构建提示，未增加图表依赖。
- Python 日志解析 8 项测试、两个 Bash 入口的语法检查通过，覆盖无数据、过期、无效值、时区、异常 JSON 和真实日志格式。
- 隔离回环实例验证：匿名 401、普通用户 403、管理员读取、`no-store`、只读接口、无公开旁路、请求不触发重采集、每分钟更新、Prometheus 指标及实际 JSON 日志解析。
- 同一实例的 DoH URL/Bearer、缓存命中和配额计量通过；正常终止后数据库可重新打开，临时凭证与数据库已清理。
- 浏览器检查管理员系统页的桌面与 390×844 布局、暂停刷新及窄屏说明换行，无浏览器错误。
- 未运行真实 MySQL 5.7/8.4 实例测试，未部署外部监控设施，也未发布或重启生产服务。

### 健康监控审查修复验证（2026-09-14）

以下记录对应基于 `868ccf1` 的七项修复，不把初次实现的验证结果当作本轮结果：

| 审查问题 | 本轮回归证据 |
| --- | --- |
| 无样本阻断评分 | 无清理尝试时保留 `null / no_samples`，正常 bbolt 和 MySQL 均能得到 100；前端与日志脚本同步识别 |
| 累计错误永久异常 | 注入一次清理/回滚失败后显示 `info` 与原始累计值，不影响当前健康评分 |
| 过期限速条目误报 | 3,900/4,096 有效条目时异常；窗口过后没有新请求也恢复 0%，内存保留数不被监控修改 |
| 索引损坏导致整次失败 | 缺失、坏 JSON、错误归属/ID、已撤销、不可归属及同用户多处损坏均有测试；重复扫描不修复数据、正常会话仍可计数 |
| 扫描预算与运行统计耦合 | 60,000 条数据在默认预算失败、100,000 预算成功；YAML 到实际存储、恰好达到预算、非法范围、扫描阻塞时仍可抓取均覆盖 |
| Prometheus 状态缺失 | 正常、异常、未知、不适用、暂无样本、历史参考及评分可用性被验证；SQL 扫描成功→失败→恢复期间池与累计计数持续导出 |
| 墙钟影响陈旧判断 | 测试中独立跳变展示时钟，扫描仍新鲜；单调经过时间超过 120 秒才过期，实时运行统计不受影响 |

- Go 1.26.6：全仓 `go test -mod=readonly -race -count=1 -p 1 ./...`、`go vet -mod=readonly ./...` 通过。
- Node.js 22.22.2：前端 6 个测试文件、46 项测试通过，类型、格式及生产构建通过；既有 ECharts 约 514 KB 分块提示仍保留。
- 普通和 `-tags ui` 本机二进制构建、UI 内嵌测试通过；Python 11 项测试和两个 Bash 入口的语法检查通过。
- 新二进制在隔离回环环境验证管理员权限、401/403、只读与 `no-store`、快照不触发重采集、定时刷新、Prometheus 新状态/评分、真实日志脚本、DoH URL/Bearer、缓存与扣额。
- 同一测试数据库以 `health_scan_limit: 1` 重启，API、Prometheus 和日志脚本均正确报告扫描未知，同时保留可读的 bbolt 运行统计及不适用状态。
- 浏览器确认正常实例为“正常 / 100”，桌面和 390×844 布局、新状态说明、限速表格与暂停刷新正常，无浏览器警告/错误。正常终止后数据库可重开；临时测试账户和数据库已清理。
- MySQL 查询/故障行为使用 sqlmock，未运行真实 MySQL 5.7/8.4 集成；未部署外部监控、发布二进制或重启生产服务。

### 回归审查修复验证（2026-09-14）

针对上一轮七项修复做对抗性回归审查，索引键解析、`monotonicNow` 空值、Prometheus 重复标签组合、`storage_age_seconds` 边界四类假设均未发现新缺陷，另外修复两项：

| 回归问题 | 修复与证据 |
| --- | --- |
| 连接池无上限时评分被永久压掉 | `unlimited_pool` 分支此前只设 `Reason`，`Status` 留在 `unknown`，`overall_score` 恒为 `null`。改为 `not_applicable`，与同函数其它不适用项一致；测试断言此时仍为 `healthy / 100`，回退实现即失败 |
| 健康接口与抓取会排在关闭之后 | `RuntimeHealth` 此前取 `Store.mu` 读锁，备份长事务加排队中的关闭会让所有后续读锁阻塞。改用原子关闭标志，读取 bbolt 统计不再进锁；测试在持有读锁并排队关闭的情况下要求 `RuntimeHealth` 及时返回，回退实现即超时失败 |

- 该并发路径不会 panic：bbolt 的 `stats` 指针在 DB 生命周期内不置空，`Stats()` 自带统计锁，关闭紧跟标志读取之后也只返回最后一次统计。
- `MySQLStore.RuntimeHealth` 原本已是无锁读取 `closed` 加 `db.Stats()`，`database/sql` 在已关闭连接池上 `Stats()` 安全，本轮未改动。

### 并发测试清理修复验证（2026-09-14）

- 修复 `TestRuntimeHealthNeverBlocksBehindPendingClose` 的失败清理路径：将持锁区放入局部函数并 `defer RUnlock`，确保正常返回、`Fatalf` 断言失败、`Fatal` 超时退出时都先释放读锁，避免测试清理中的 `Store.Close()` 挂住。不改变生产代码。
- 该测试以 `-race -count=20` 连续通过；存储包完整 `go test -mod=readonly -race ./internal/control -count=1`、对应 `go vet`、格式与差异检查通过。
- 临时 `go test -overlay` 分别强制触发断言失败和超时分支；两种情况均以预期失败退出，未触发全局测试超时或清理死锁。故障注入文件不纳入仓库。
