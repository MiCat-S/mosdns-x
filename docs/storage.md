# 控制服务存储

Mosdns-x 支持 `bbolt` 和 `mysql` 两种控制服务存储。未配置驱动时继续使用 bbolt，已有部署无需改动。

| 方案 | 适用场景 | 数据位置 | 备份方式 |
| --- | --- | --- | --- |
| bbolt | 单机、小规模、希望零外部依赖 | `control.db` 与 `stats.db` | 停止服务后复制两个数据库文件 |
| MySQL | 数据量持续增长、需要集中备份或后续扩展 | MySQL 的规范化表 | MySQL 原生备份、快照或主从复制 |

控制数据包括账户、密码派生值、会话、DNS 凭证哈希、额度状态、QPS 令牌桶、计费用量和审计记录。统计数据包括分钟聚合、上游状态及可选的查询明细。两类数据使用各自的表组；可以放在同一个数据库，也可以给统计数据配置单独的 DSN。

无论使用哪种后端，都应把数据库和备份按敏感数据保护。查询明细可能含客户端地址、查询域名、Answer IP 和 EDNS/ECS 信息，只有确有排障或审计需要时才启用 `query_log`。

管理员删除用户时，控制库和统计库中该用户的数据会立即删除，不等待维护任务；范围见[控制 API](control-api.md)的删除用户说明。已写入备份的数据不受影响，需要时应按备份保留策略另行处理。

## bbolt

bbolt 使用两个文件：`control.database` 保存控制数据，`control.stats_database` 保存统计数据。两条路径必须不同。程序会以 `0700` 创建缺失的父目录，并把数据库文件设为 `0600`。

当前 bbolt 控制库 schema 为 v6。打开 v1～v5 的旧库时会自动升级到 v6，例如为 UUIDv4 设备 token 创建索引；升级后的数据库不能再由只支持更低 schema 的旧二进制打开。升级前先按下文备份。

运行中的维护任务会清理：

- 已到期或已撤销的面板会话；
- 超过 35 天的计费分钟用量；
- 超过 90 天的操作审计；
- 到期或撤销超过 35 天的 DNS 凭证；
- 超过配置保留期的统计聚合和查询明细，以及超出配置条数上限的查询记录。

### 备份与恢复

bbolt 没有内置备份命令。备份时停止 Mosdns-x，复制 `control.database` 和 `control.stats_database` 指向的两个文件（如 `control.db`、`stats.db`），副本保持 `0600` 权限并存放在受限目录。不要复制正在运行的服务所用的数据库文件，复制结果可能不一致。

恢复时同样先停止服务，把当前文件改名保留，再把副本放回原路径（或修改配置指向副本），确认属主和权限后启动。恢复会回到备份时刻的账户、会话、设备凭证、额度已用量和用量记录；备份之后产生的扣费不会出现在恢复后的数据中。确认管理员登录、用户额度和设备凭证正确之前，保留被替换的文件。

## MySQL

当前兼容基线为 MySQL 5.7 和 8.4，两种版本都会在 CI 中运行完整控制存储与统计存储集成测试。表结构没有依赖 MySQL 8 专属的 JSON 类型、窗口函数或新排序规则，因此已有 MySQL 5.7 环境可以直接使用。

MySQL 后端按用户、会话、凭证、计费用量、审计、统计维度和查询明细拆表。当前 control schema 为 v5，telemetry schema 为 v3。查询明细表的 `upstream_label`（出站 DNS 名称）和 `trace_json`（分流过程与每次上游请求）是增量列，带默认值，启动时在 schema 锁内补齐，不提升版本；旧版本按显式列名读写，回滚后仍可使用，只是不再写入这两列；`trace_json` 会记录查询经过的每个分流条件，大小随条件数量变化，一般每条约 1～3 KB，估算 `max_query_records` 对应的磁盘占用时应计入；旧版本的表会在启动时于迁移锁内自动升级。升级器读取实际列或索引状态并只补充缺失项；即使旧表已创建但 schema 版本行尚未写入，下一次启动也会先收敛列结构，再记录版本。

结构升级使用一条独立连接执行，不受 DSN 中 `readTimeout`、`writeTimeout` 和启动超时的限制，最长等待 30 分钟。MySQL 5.7 为表增加列时会重建整张表，查询明细较多时需要数分钟；期间服务尚未开始应答，日志会先输出一条 `upgrading mysql schema` 警告并注明表名，完成后输出 `mysql schema upgraded` 和耗时。不要在这期间重启服务，否则升级会从头再来。MySQL 8.0 及以后的版本可以即时增加列，通常不受影响。额度扣减、用户令牌桶与三份计费用量（全局、用户、设备）在同一事务中更新；同一用户的并发请求通过行锁串行化，避免超额放行。统计写入仍通过内存队列批量落库，控制路径不会等待查询明细写入。

服务启动时使用命名锁串行初始化表，并校验 `control` 与 `telemetry` 的 schema 版本。运行账号需要目标库的 `SELECT`、`INSERT`、`UPDATE`、`DELETE`、`CREATE` 权限，并能调用 `GET_LOCK`/`RELEASE_LOCK`。建议由数据库管理员提前创建数据库和专用账号；不要授予全局管理权限。

推荐把 MySQL 放在同机或低延迟内网。控制后端每次 DNS 请求都需要执行带行锁的事务，数据库不可用时请求会被拒绝，不能把高延迟公网数据库放在这条路径上。连接池与操作超时可在配置中调整。

MySQL 消除了本地文件容量和独占锁的限制，但当前版本仍按单个 Mosdns-x 实例验收。后续扩展多实例时，需要单独压测热点用户行锁，并为跨实例突发 QPS 评估 Redis 令牌桶；Redis 不是当前单机部署的必需组件。

MySQL 数据使用数据库原生工具备份，例如 `mysqldump`、存储快照或主从复制。备份策略至少覆盖所有 `mosdns_*` 表，并定期执行恢复演练。

## 已移除功能的数据

本版只保留账户、鉴权和日志相关功能。用户 DNS 策略（自定义规则、安全与隐私选项、安全模式、策略暂停、响应优化、ECS 覆写、用户自己的查询日志开关和保留期）、公共列表、DNS Lookup，以及 `mosdns control backup`、`mosdns control restore`、`mosdns control migrate-mysql` 命令已移除。本版不提供把 bbolt 数据导入 MySQL 的工具。

数据库 schema 版本没有因此改变：bbolt control schema 仍为 v6，MySQL control schema 仍为 v5，MySQL telemetry schema 仍为 v3。已移除功能的表和 bucket 保留但不再读取，因此旧二进制仍能打开升级后的数据库：

- MySQL：`mosdns_dns_policy_settings`、`mosdns_dns_policy_rules`、`mosdns_public_lists`、`mosdns_user_public_lists`；
- bbolt：`dns_policy_settings`、`dns_policy_rules`、`user_dns_policy_rules`、`public_lists`、`public_list_names`、`user_public_lists`。

删除用户时仍会清除该用户在这些表或 bucket 中的记录。查询明细不再写入 `matched_rule_id`、`matched_public_list_id`，API 也不再返回这两个字段；`custom_block`、`custom_rewrite`、`public_list`、`family_preference` 这些响应来源也不再产生；升级前写入的明细仍可能带有这些值。原有的 `public-lists` 快照目录不再使用，可以手动删除。

回滚到旧二进制后，这些功能会重新出现，并使用升级前留下的数据。本版除删除用户时清除对应记录外，不会修改这些数据。
