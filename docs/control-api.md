# 多用户服务 API 契约

本文约束首版管理端、用户端及 Go API 的实现。部署为单机单实例，DNS 接入使用 DoH / DoH3。账户与配额类型以 `internal/control/types.go` 为准。所有时间为 RFC3339，周期枚举为 `daily` / `monthly`；时区默认 UTC。

## HTTP 约定

- 基础路径 `/api/v1`，请求和响应为 JSON；成功直接返回下列对象，不套额外 `data` 层。列表返回 `{items: [], next_cursor?: string}`，空列表必须是数组。写入成功返回 200 或创建时 201，删除/退出成功 204。
- 失败返回 `{error: {code: string, message: string}}`。code 使用 `invalid_input`、`invalid_credential`、`forbidden`、`not_found`、`conflict`、`rate_limited`、`quota_exceeded`、`unavailable`。错误不包含秘密或内部数据库错误。
- 面板通过 HttpOnly 会话 Cookie 鉴权；写请求须携带 `X-CSRF-Token`。登录校验同源 Origin。API 不允许跨域读取；Cookie、CSRF 和 DNS Token 不保存到浏览器持久存储。
- `GET /session` 和成功登录返回 `{user: User, csrf_token: string, expires_at: string}`。前端刷新后重新获取会话。
- `/me/*` 的用户身份来自会话；`/admin/*` 需要服务端管理员授权。服务端对凭证归属再次检查。
- 分页使用 `limit`（默认 100，上限 1000）和 `cursor`。时间查询用 `from`、`to`，左闭右开，最长 31 天。前端不得将单页用量冒充整个区间的用量。
- 用量和响应聚合按分钟桶时间筛选；需要完整分钟区间时将边界对齐到整分钟，当前分钟仍可能增长。查询明细按请求事件时间筛选。当前周期已用额度直接读取 `quota.used`。

## 账户与凭证

| 方法 | 路径 | 请求 / 响应 |
|---|---|---|
| POST | `/session` | `{username,password}` → 会话对象 |
| GET | `/session` | 会话对象 |
| DELETE | `/session` | 注销当前会话 |
| GET | `/me` | `{user: User, quota: QuotaStatus}` |
| POST | `/me/password` | `{current_password,new_password}`；成功撤销会话，重新登录 |
| GET | `/me/credentials` | `PageResult<Credential>` |
| POST | `/me/credentials` | `{name,expires_at?}` → `IssuedCredential` 加 `doh_url` |
| POST | `/me/credentials/{id}/rotate` | 新的 `IssuedCredential` 加 `doh_url`，旧秘密立即失效 |
| DELETE | `/me/credentials/{id}` | 撤销 |
| GET | `/admin/users` | `PageResult<User>` |
| POST | `/admin/users` | `UserSpec` → `User` |
| GET | `/admin/users/{id}` | `{user: User, quota: QuotaStatus}` |
| PATCH | `/admin/users/{id}` | `UserPatch` → `User`；额度修改保留已用量 |
| POST | `/admin/users/{id}/password` | `{new_password}`，撤销该用户全部会话 |
| GET/POST | `/admin/users/{id}/credentials` | 与 `/me/credentials` 相同 |
| POST/DELETE | `/admin/users/{id}/credentials/{credential_id}[/rotate]` | 与用户端对应操作相同 |

`IssuedCredential` 的 token 是规范小写 RFC 4122 UUIDv4，只展示一次，并与 `Credential.id` 分离。数据库只保存完整 token 的 SHA-256；升级前签发的 `id.secret` token 继续有效，轮换后改为 UUIDv4。`doh_url` 是服务器配置的公共 DNS 基础 URL 加凭证路径；固定基础 URL 可搭配 `Authorization: Bearer <token>` 使用。创建完成后，列表只显示 Credential 元数据。零值到期时间表示未单独设置，由账户服务到期约束；前端对 `0001-01-01T00:00:00Z` 显示为未单独设置。

## 用量、结果统计与运维

| 方法 | 路径 | 响应 |
|---|---|---|
| GET | `/me/usage` | `PageResult<UsagePoint>`，当前用户持久化分钟用量 |
| GET | `/admin/usage` | 全局持久化分钟用量；可用 `user_id` 选定用户 |
| GET | `/me/device-usage` | 设备分钟用量；支持时间范围和分页 |
| GET | `/admin/users/{id}/device-usage` | 对应用户设备分钟用量 |
| GET | `/me/stats` | 当前用户 `StatsSnapshot` |
| GET | `/admin/stats` | 全局 `StatsSnapshot`；可用 `user_id` 选定用户 |
| GET | `/me/queries` | 当前用户的可选查询日志 `PageResult<QueryRecord>` |
| GET | `/admin/queries` | 管理员查询日志；可用 `user_id` 筛选 |
| GET | `/admin/audit` | `PageResult<AuditRecord>` |
| GET | `/admin/system` | 安全的系统与配置概览，省略原始插件参数及秘密 |

`StatsSnapshot`：`from`、`to`、`completed`、`failed`、`cache_hits`、`avg_latency_ms`、`p95_latency_ms`、`rcode_counts`、`series`、`upstreams`、`dropped`、`updated_at`、`query_log_enabled`。

- `series` 每项：`time`、`completed`、`failed`、`cache_hits`、`avg_latency_ms`。
- `upstreams` 每项：`id`、`attempts`、`failures`、`avg_latency_ms`。id 使用安全的配置标识，不能包含上游 URL 中的凭证。
- `QueryRecord`：`id`、`time`、`user_id`、`credential_id`、`client_ip`、`name`、`qtype`、`rcode`、`duration_ms`、`cache_hit`、`protocol`、`answer_ips`、`edns`。`answer_ips` 是最终返回 Answer 区中的 A/AAAA 地址，按报文顺序去重；没有地址时为空数组。
- 查询日志按 `time`、`id` 从新到旧返回。除通用的 `from`、`to`、`limit`、`cursor` 外，还支持 `name`（不区分大小写的包含匹配）、`qtype`、`rcode`、`credential_id`、`protocol`、`address`（客户端 IP 或 Answer IP）和 `cache=all|hit|miss`。继续分页时必须保持时间范围和筛选条件不变。
- `edns` 包含 `present`、`version`、`udp_size`、`dnssec_ok`、`option_codes`，以及可选的 `ecs`。`ecs` 包含规范化网络地址 `address`、`family`、`source_prefix`、`scope_prefix`。系统只记录 EDNS option code，不保存 Cookie、Padding、NSID 或其他 option 载荷。
- `completed` 是结果统计采集量，`failed` 是其中的失败量；扣费次数以事务保存的 usage / quota 为准。`dropped` 是当前进程观测到的异步事件丢弃量，包含响应或上游尝试事件，重启后不能据此判断历史数据完整性。统计窗口和更新时刻必须展示。
- 响应聚合保留 7 天。上述客户端 IP、Answer IP、EDNS/ECS 字段仅在 `query_log` 启用时写入查询明细；查询明细继续保留 24 小时且最多 100,000 条，查询更久区间不会凭空补齐已清理的数据。客户端 IP 和 ECS 可能属于个人或网络识别信息，启用前应按部署所在地要求限制面板访问并告知用户。P95 是直方图桶上界估算。
- `system` 至少提供 `version`、`started_at`、`public_dns_url`、`query_log_enabled`、`config`（配置白名单概览）。`config` 包含 `dns_protocols`、`management_enabled`、`pprof_enabled`、`control_storage` 和 `telemetry_storage`，不会返回数据库路径或 MySQL DSN。查询明细默认关闭，关闭时接口返回空数组和页面说明。

## 受理与兼容边界

先鉴权，再校验报文、账户到期和用户 QPS，事务提交额度扣减后进入现有执行链。缓存命中扣一次，fallback、上游并发与缓存后台刷新不重复扣额。受理后上游失败仍计次数，客户端重试作为新请求。

启用多用户服务后，旧 `/metrics`、插件 API 也需管理权限；pprof 默认关闭。未启用时保留原有 DNS 配置行为。各协议类型与 HTTP/TLS 扩展库保持现有实现。

新增资源显式接入实例生命周期：停止接收请求，再结束受理队列和统计任务，最后关闭数据库；初始化部分失败时也回收。不得仅依靠存在 `Close` 方法推断自动回收。
