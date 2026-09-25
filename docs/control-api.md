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

删除用户会在一个事务中移除账户、会话、设备凭证、DNS 策略设置与规则、公共列表覆盖、计费用量，以及该用户执行的或以该用户、其会话、凭证、规则为对象的审计记录；随后清除该用户的查询明细和按用户统计的聚合。全站聚合不含用户标识，予以保留。删除后只留下一条 `delete_user` 审计，记录执行删除的管理员和被删除的用户 ID，不含用户名。管理员不能删除自己，最后一个启用的管理员因此始终保留。若账户已删除但查询日志清除失败，接口返回 503；对同一 ID 再次调用会补做清除，然后返回 404。

审计记录只能按对象 ID 关联到用户。维护任务按保留期清理过期会话和凭证后，更早的、只以这些会话或凭证为对象的审计便无法再关联到用户，删除用户时不会一并移除，直到 90 天审计保留期到期。本版起，凭证和会话审计的 `metadata` 附带 `user_id`，之后产生的记录不受此限制。

## 用户 DNS 策略与诊断

| 方法 | 路径 | 请求 / 响应 |
|---|---|---|
| GET | `/me/settings` | 当前用户的 `DNSPolicySettings` |
| PATCH | `/me/settings` | `DNSPolicySettingsPatch` → 更新后的设置 |
| GET | `/me/rules` | `PageResult<DNSPolicyRule>`，按优先级升序 |
| POST | `/me/rules` | `DNSPolicyRuleSpec` → 新规则 |
| PATCH | `/me/rules/{id}` | `DNSPolicyRulePatch` → 更新后的规则 |
| DELETE | `/me/rules/{id}` | 删除规则 |
| GET | `/me/public-lists` | 用户安全视图，包含列表展示字段及继承或覆盖后的启用状态 |
| PATCH | `/me/public-lists/{id}` | `{enabled:true|false|null}`；`null` 恢复管理员默认值 |
| POST | `/me/lookup` | `{name,qtype}` → 当前执行链的结构化 DNS 结果 |

`DNSPolicySettings` 支持移除 ECS、拦截私有地址应答、拒绝指定 QTYPE、分别启停自定义拦截／放行／重写规则，以及临时暂停全部用户策略。对应字段为 `strip_ecs`、`block_private_answers`、`blocked_qtypes`、`custom_block_enabled`、`custom_allow_enabled`、`custom_rewrite_enabled` 和可空的 `policy_paused_until`。暂停截止时间使用 RFC3339，最长可设为服务端当前时间之后 24 小时；过去时间或 JSON `null` 表示取消暂停。

规则动作是 `allow`、`block`、`rewrite`，匹配方式是 `exact`、`suffix`、`keyword`、`regexp`；重写支持 A、AAAA、CNAME。每个用户最多 1000 条规则，按 `priority ASC, id ASC` 判断，第一条匹配规则生效。相同优先级的规则不保证创建顺序，存在覆盖关系时应使用不同优先级。拦截返回 NXDOMAIN；A/AAAA/CNAME 重写 TTL 为 60 秒。

DNS 请求通过凭证鉴权并完成配额受理后才应用用户策略，因此被用户规则或公共列表拦截的有效请求仍计入额度。处理顺序为安全 QTYPE 限制、第一条匹配的自定义规则、公共列表、sequence、私有地址应答检查、地址族偏好；自定义 `allow` 会跳过公共列表。QTYPE 限制返回 NOERROR 空应答（NODATA），表示域名存在但没有该类型记录；规则、公共列表和私有地址拦截返回 NXDOMAIN。QTYPE 限制若返回 NXDOMAIN，按 RFC 8020 解析器可将整个域名缓存为不存在，连 A 记录也一并失效，浏览器同时查询 HTTPS 记录时会因此打不开网站。ECS 从请求副本中移除，原始请求快照仍可用于查询明细。暂停期间这些请求与响应策略全部绕过，到期后无需后台任务即可恢复。ECS、私有地址和 QTYPE 设置默认关闭，三个自定义规则总开关默认开启；升级会保持已有规则继续生效。

`answer_family` 为空表示不偏好，`ipv4` 或 `ipv6` 表示偏好对应地址族。偏好 IPv4 时，AAAA 查询有应答的情况下会用同一执行链额外查询 A；A 存在才把 AAAA 置为空应答，因此只有 IPv6 地址的域名仍可访问。IPv6 偏好对称处理。这次额外查询不经过配额受理和用户策略，不计入额度，通常直接命中缓存。查询失败或上游错误时保留原应答。被置空的查询在查询明细中来源为 `family_preference`。

响应记录优化按用户生效，只改写发给该用户的应答，共享缓存保留上游原始结果。`flatten_cname` 去掉 CNAME 链，把剩余记录归到所查询的域名下；没有地址记录或含 DNAME 时保持原样。`shuffle_answers` 只在 A/AAAA 查询中打乱地址记录的顺序，CNAME 保持在前。`ttl_min`、`ttl_max` 限定应答区记录的 TTL，0 表示不限制，上限 86400 秒，同时设置时下限不得大于上限；授权区不受影响，SOA 仍决定否定缓存时长。改写直接作用于原应答，查询明细中的来源仍记为上游或缓存。暂停策略期间这些改写同样不生效。

`ecs_ipv4`、`ecs_ipv6` 是 CIDR 网段，保存时去掉主机位；留空表示不覆写。字段必须与地址族一致，IPv4 映射的 IPv6 网段不算 IPv4，裸地址不接受。设置后，A 查询优先发送 IPv4 网段，AAAA 优先发送 IPv6 网段，缺一项时用另一项代替，并强制替换客户端自带的 ECS；其他查询类型不改。覆写只作用于本身带 EDNS 的请求，不会为纯 DNS 请求添加 OPT（RFC 6891）。上游在应答中回显的是覆写网段，与客户端所发的对不上，因此应答里的 ECS 选项会被移除（RFC 7871），OPT 保留。`strip_ecs` 优先于覆写，同时开启时不发送任何网段。带 EDNS 的查询默认不进入缓存；开启 `cache_everything` 时，缓存键包含整条请求和 ECS，不同网段分开缓存，不会跨用户串用。

用户公共列表响应不会返回来源 URL、配置 SHA-256、快照 SHA-256 或原始刷新错误；这些字段只对管理员接口可见，避免签名 URL 和上游细节泄漏。用户仍可看到名称、分类、格式、条目数、发布与快照状态、刷新时间，以及自己的显式或继承选择。

管理员用 `GET/POST /admin/public-lists` 和 `GET/PATCH/DELETE /admin/public-lists/{id}` 管理 HTTPS 列表目录。直接 POST 保存草稿；`POST /admin/public-lists/validate` 返回内容预览和绑定候选快照的令牌，随后用 `POST /admin/public-lists/publish` 或 `POST /admin/public-lists/{id}/publish` 发布该快照。`POST /admin/public-lists/{id}/refresh` 刷新一项，`POST /admin/public-lists/refresh-all` 刷新全部已发布项。列表项分别包含 `published`、`default_enabled`、刷新结果及快照状态；下载失败时继续使用上一份有效快照。`GET /admin/data-providers` 只读返回主配置中的节点数据源，不会把它们导入为用户拦截规则。

Lookup 只接受 A、AAAA、CNAME、NS、MX、TXT，使用当前实例的同一入口 sequence 和用户策略，返回 `question`、`rcode`、`duration_ms`、`answers`、`authority`、`additional`、`edns`。它只供面板诊断，不扣周期额度，也不写查询统计；已到期用户不能调用。服务默认限制每个客户端地址每分钟 60 次、全局同时 8 次，避免把面板接口当作免费解析入口。

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
- `QueryRecord`：`id`、`time`、`user_id`、`credential_id`、`client_ip`、`name`、`qtype`、`rcode`、`duration_ms`、`cache_hit`、`protocol`、`answer_ips`、`edns`、`response_source`、`response_source_id`、`upstream_id`、`matched_rule_id`、`matched_public_list_id`。`answer_ips` 是最终返回 Answer 区中的 A/AAAA 地址，按报文顺序去重；没有地址时为空数组。
- `response_source` 至少可能是 `cache`、`upstream`、`custom_block`、`custom_rewrite`、`public_list`、`hosts`、`sequence` 或 `servfail`。并发和 fallback 只记录最终选中响应的来源及上游；所有实际上游尝试仍进入聚合统计。
- 查询日志按 `time`、`id` 从新到旧返回。除通用的 `from`、`to`、`limit`、`cursor` 外，还支持 `name`（不区分大小写的包含匹配）、`qtype`、`rcode`、`credential_id`、`protocol`、`address`（客户端 IP 或 Answer IP）、`source`、`upstream_id` 和 `cache=all|hit|miss`。继续分页时必须保持时间范围和筛选条件不变。
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

HTTP 层先验证 DNS 凭证，再解析和校验报文；有效问题随后检查账户到期、用户 QPS 和额度。事务提交额度扣减后应用用户策略并进入现有执行链。缓存命中扣一次，fallback、上游并发与缓存后台刷新不重复扣额。受理后被用户规则拦截或上游失败仍计次数，客户端重试作为新请求。

启用多用户服务后，旧 `/metrics`、插件 API 也需管理权限；pprof 默认关闭。未启用时保留原有 DNS 配置行为。各协议类型与 HTTP/TLS 扩展库保持现有实现。

新增资源显式接入实例生命周期：停止接收请求，再结束受理队列和统计任务，最后关闭数据库；初始化部分失败时也回收。不得仅依靠存在 `Close` 方法推断自动回收。
