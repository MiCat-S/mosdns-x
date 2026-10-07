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
| GET | `/me` | `{user: User, quota: QuotaStatus, public_dns_url: string}` |
| POST | `/me/password` | `{current_password,new_password}`；成功撤销会话，重新登录 |
| GET | `/me/credentials` | `PageResult<Credential>` |
| POST | `/me/credentials` | `{name,expires_at?}` → `IssuedCredential` 加 `doh_url` |
| POST | `/me/credentials/{id}/rotate` | 新的 `IssuedCredential` 加 `doh_url`，旧秘密立即失效 |
| DELETE | `/me/credentials/{id}` | 撤销 |
| GET | `/admin/users` | `PageResult<User>` |
| POST | `/admin/users` | `UserSpec` → `User` |
| GET | `/admin/users/{id}` | `{user: User, quota: QuotaStatus}` |
| PATCH | `/admin/users/{id}` | `UserPatch` → `User`；额度修改保留已用量 |
| DELETE | `/admin/users/{id}` | 永久删除用户及其全部数据，成功 204；不能删除自己（409） |
| POST | `/admin/users/{id}/password` | `{new_password}`，撤销该用户全部会话 |
| GET/POST | `/admin/users/{id}/credentials` | 与 `/me/credentials` 相同 |
| POST/DELETE | `/admin/users/{id}/credentials/{credential_id}[/rotate]` | 与用户端对应操作相同 |

`IssuedCredential` 的 token 是规范小写 RFC 4122 UUIDv4，只展示一次，并与 `Credential.id` 分离。数据库只保存完整 token 的 SHA-256；升级前签发的 `id.secret` token 继续有效，轮换后改为 UUIDv4。`doh_url` 是服务器配置的公共 DNS 基础 URL 加凭证路径；固定基础 URL 可搭配 `Authorization: Bearer <token>` 使用。创建完成后，列表只显示 Credential 元数据。零值到期时间表示未单独设置，由账户服务到期约束；前端对 `0001-01-01T00:00:00Z` 显示为未单独设置。

删除用户会在一个事务中移除账户、会话、设备凭证、计费用量、旧版遗留的 DNS 策略设置与规则及公共列表覆盖，以及该用户执行的或以该用户、其会话、凭证、规则为对象的审计记录；随后清除该用户的查询明细和按用户统计的聚合。全站聚合不含用户标识，予以保留。删除后只留下一条 `delete_user` 审计，记录执行删除的管理员和被删除的用户 ID，不含用户名。管理员不能删除自己，最后一个启用的管理员因此始终保留。若账户已删除但查询日志清除失败，接口返回 503；对同一 ID 再次调用会补做清除，然后返回 404。

审计记录只能按对象 ID 关联到用户。维护任务按保留期清理过期会话和凭证后，更早的、只以这些会话或凭证为对象的审计便无法再关联到用户，删除用户时不会一并移除，直到 90 天审计保留期到期。本版起，凭证和会话审计的 `metadata` 附带 `user_id`，之后产生的记录不受此限制。

## 已移除的接口

以下接口已移除：`/me/settings`、`/me/rules[/{id}]`、`/me/public-lists[/{id}]`、`/me/lookup`、`/admin/public-lists` 及其子路径、`/admin/users/{id}/rules`。用户 DNS 策略、公共列表和 Lookup 不再提供；遗留数据的处理见[存储文档](storage.md#已移除功能的数据)。

## 用量、结果统计与运维

| 方法 | 路径 | 响应 |
|---|---|---|
| GET | `/me/usage` | `PageResult<UsagePoint>`，当前用户持久化分钟用量 |
| GET | `/admin/usage` | 全局持久化分钟用量；可用 `user_id` 选定用户 |
| GET | `/me/device-usage` | 设备分钟用量；支持时间范围和分页 |
| GET | `/admin/users/{id}/device-usage` | 对应用户设备分钟用量 |
| GET | `/me/stats` | 当前用户 `StatsSnapshot` |
| GET | `/admin/stats` | 全局 `StatsSnapshot`；可用 `user_id` 选定用户 |
| GET | `/me/queries` | 当前用户的查询日志 `PageResult<QueryRecord>`；需启用 `query_log` |
| GET | `/admin/queries` | 管理员查询日志；可用 `user_id` 筛选 |
| GET | `/admin/audit` | `PageResult<AuditRecord>` |
| GET | `/admin/system` | 安全的系统与配置概览，省略原始插件参数及秘密 |

`StatsSnapshot`：`from`、`to`、`completed`、`failed`、`cache_hits`、`avg_latency_ms`、`p95_latency_ms`、`rcode_counts`、`series`、`upstreams`、`dropped`、`updated_at`、`query_log_enabled`。

- `series` 每项：`time`、`completed`、`failed`、`cache_hits`、`avg_latency_ms`。
- `upstreams` 每项：`id`、`attempts`、`failures`、`avg_latency_ms`。id 使用安全的配置标识，不能包含上游 URL 中的凭证。
- `QueryRecord`：`id`、`time`、`user_id`、`credential_id`、`client_ip`、`name`、`qtype`、`rcode`、`duration_ms`、`cache_hit`、`protocol`、`answer_ips`、`edns`、`response_source`、`response_source_id`、`upstream_id`、`upstream_label`、`trace`、`username`、`device_name`。`username` 和 `device_name` 是读取日志时按 `user_id`、`credential_id` 查到的当前账户名和设备名；账户或设备已删除、或暂时无法读取时不返回，面板改为显示 ID。`answer_ips` 是最终返回 Answer 区中的 A/AAAA 地址，按报文顺序去重；没有地址时为空数组。
- `response_source` 可能是 `cache`、`upstream`、`hosts`、`sequence` 或 `servfail`。升级前写入的记录还可能带有 `custom_block`、`custom_rewrite`、`public_list` 或 `family_preference`，本版不再产生这些值。并发和 fallback 中最终选中的响应决定 `response_source` 和 `upstream_id`；每一次实际上游请求都记录在 `trace` 中，并进入聚合统计。
- `upstream_label` 是这次查询走的出站 DNS。缓存命中时，它是当初产生这条缓存应答的上游；升级前写入的缓存条目没有这项信息，此时为空。上游配置了 `label` 时显示该名称；否则内置名单中的公共 DNS（阿里、腾讯 DNSPod、114、百度、CNNIC、360、Google、Cloudflare、Quad9、OpenDNS、AdGuard）显示为“地址 (协议)”，例如 `223.5.5.5 (UDP)`；其他上游一律显示为“插件 tag #序号 (协议)”，例如 `forward_remote #1 (DoH)`，不包含私有服务器的域名、IP 或 URL 路径。
- `trace` 记录这次查询的处理过程，升级前的记录没有这项：
  - `steps`：按时间顺序记录的处理步骤。`kind` 为 `condition` 时是一次分流条件判断：`detail` 是条件表达式，`matched` 为判断结果，`hits` 和 `misses` 分别是取值为真和为假的 matcher（表达式中两者都不含的 matcher 因短路未检查），`then` 为接下来执行的部分：`exec`（条件下的分支）、`else`（`else_exec`）或 `continue`（继续执行后续节点）。其他 `kind`：`cache_hit`、`lazy_refresh`（缓存过期，先回旧结果并在后台刷新）、`secondary_started`（`detail` 为 `primary_failed`、`fast_fallback` 或 `always_standby`）、`branch_selected`（采用哪个分支的结果）、`primary_unhealthy`、`load_balance`。`v26.10.07` 写入的记录只含命中的条件（`kind` 为 `if`）和走 `else` 分支的条件（`kind` 为 `else`）。
  - `attempts`：每一次上游请求，含 `upstream`（同 `upstream_label` 的命名规则）、`plugin`、`branch`（如 `primary`、`secondary`、`parallel#1`、`lazy_refresh`，嵌套时以 `/` 连接）、相对查询开始的 `start_ms`、`duration_ms`、`rcode` 或 `error`，以及 `selected`（最终采用的那次）。`done` 为 `false` 表示查询返回时这次请求尚未结束。
  - `error` 只记录分类：`timeout`、`canceled`、`tls`、`connection_refused`、`connection_reset`、`connect_failed`、`resolve_failed`、`http_status`、`bad_response`、`empty_response`、`error`。原始错误信息常包含服务器地址，因此不写入日志。
  - 每条记录最多保留 32 个步骤和 32 次请求，超出时 `truncated` 为 `true`。
  - `/me/queries` 与 `/admin/queries` 返回相同的 `trace`。普通用户因此能看到分流条件表达式、matcher 与插件的 tag，以及主备切换过程，但看不到任何上游的私有域名、IP 或 URL 路径。
- 查询日志按 `time`、`id` 从新到旧返回。除通用的 `from`、`to`、`limit`、`cursor` 外，还支持 `name`（不区分大小写的包含匹配）、`qtype`、`rcode`、`credential_id`、`protocol`、`address`（客户端 IP 或 Answer IP）、`source`、`upstream_id`、`upstream`（按 `upstream_label` 精确匹配）和 `cache=all|hit|miss`。继续分页时必须保持时间范围和筛选条件不变。
- `edns` 包含 `present`、`version`、`udp_size`、`dnssec_ok`、`option_codes`，以及可选的 `ecs`。`ecs` 包含规范化网络地址 `address`、`family`、`source_prefix`、`scope_prefix`。系统只记录 EDNS option code，不保存 Cookie、Padding、NSID 或其他 option 载荷。
- `completed` 是结果统计采集量，`failed` 是其中的失败量；扣费次数以事务保存的 usage / quota 为准。`dropped` 是当前进程观测到的异步事件丢弃量，包含响应或上游尝试事件，重启后不能据此判断历史数据完整性。统计窗口和更新时刻必须展示。
- 响应聚合默认保留 7 天。上述客户端 IP、Answer IP、EDNS/ECS 字段仅在 `query_log` 启用时写入查询明细；查询明细默认保留 24 小时且最多 100,000 条，均可通过 `control.telemetry` 在允许范围内调整。查询更久区间不会补齐已清理的数据。客户端 IP 和 ECS 可能属于个人或网络识别信息，启用前应按部署所在地要求限制面板访问并告知用户。P95 是直方图桶上界估算。
- `system` 至少提供 `version`、`started_at`、`public_dns_url`、`query_log_enabled`、`config`（配置白名单概览）。`config` 包含 `dns_protocols`、`management_enabled`、`pprof_enabled`、`control_storage` 和 `telemetry_storage`，不会返回数据库路径或 MySQL DSN。查询明细默认关闭，关闭时接口返回空数组和页面说明。

## 托管运行配置

`GET /admin/runtime/config` 始终尝试返回服务端脱敏的运行摘要与能力信息。配置 `control.managed_config` 后才开放写入、历史和探测接口；未配置时这些管理接口返回 `managed_config_disabled`：

| 方法 | 路径 | 请求 / 响应 |
|---|---|---|
| GET | `/admin/runtime/config` | `running`、最近加载的 `base`、候选状态、能力及兼容字段 `config/revision` |
| POST | `/admin/runtime/config/validate` | `{revision,config}` → 五分钟一次性验证令牌及缓存清空提示 |
| POST | `/admin/runtime/config/apply` | `{token}` → 新状态；令牌绑定管理员会话与修订 |
| POST | `/admin/runtime/config/reload` | 重读主配置；不可热更新项通过 `restart_required` 返回 |
| GET | `/admin/runtime/history` | 最近 10 个可回滚旧修订 |
| POST | `/admin/runtime/rollback` | `{revision,target_revision}` → 回滚后的新状态 |
| POST | `/admin/runtime/upstreams/{tag}/probe` | 每个上游的安全标识、耗时、RCODE 和成功状态 |
| GET | `/admin/data-providers` | 主配置数据源声明、文件状态和可用的运行状态；未知值为 `null` 或 `unsupported` |

安全视图只包含不含敏感参数的 `fast_forward`、非 Redis 内存缓存、`query_log` 和统计保留策略。应用前完整构建候选运行代；失败时当前运行代保持不变。成功后所有入口一次切换，旧运行代等待在途请求和后台上游工作完成再关闭。修改任何需要重建运行代的配置且当前存在内存缓存时，验证结果会提示缓存清空。

## 受理与兼容边界

HTTP 层先验证 DNS 凭证，再解析和校验报文；有效问题随后检查账户到期、用户 QPS 和额度。事务提交额度扣减后进入现有执行链。缓存命中扣一次，fallback、上游并发与缓存后台刷新不重复扣额。受理后上游失败仍计次数，客户端重试作为新请求。

启用多用户服务后，旧 `/metrics`、插件 API 也需管理权限；pprof 默认关闭。未启用时保留原有 DNS 配置行为。各协议类型与 HTTP/TLS 扩展库保持现有实现。

新增资源显式接入实例生命周期：停止接收请求，再结束受理队列和统计任务，最后关闭数据库；初始化部分失败时也回收。不得仅依靠存在 `Close` 方法推断自动回收。
