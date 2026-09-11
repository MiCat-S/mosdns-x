# 控制服务存储

Mosdns-x 支持 `bbolt` 和 `mysql` 两种控制服务存储。未配置驱动时继续使用 bbolt，已有部署无需改动。

| 方案 | 适用场景 | 数据位置 | 备份方式 |
| --- | --- | --- | --- |
| bbolt | 单机、小规模、希望零外部依赖 | `control.db` 与 `stats.db` | `mosdns control backup` 加离线复制统计库 |
| MySQL | 数据量持续增长、需要集中备份或后续扩展 | MySQL 的规范化表 | MySQL 原生备份、快照或主从复制 |

控制数据包括账户、密码派生值、会话、DNS 凭证哈希、额度状态、QPS 令牌桶、计费用量和审计记录。统计数据包括分钟聚合、上游状态及可选的查询明细。两类数据使用各自的表组；可以放在同一个数据库，也可以给统计数据配置单独的 DSN。

无论使用哪种后端，都应把数据库和备份按敏感数据保护。查询明细可能含客户端地址、查询域名、Answer IP 和 EDNS/ECS 信息，只有确有排障或审计需要时才启用 `query_log`。

## bbolt

bbolt 使用两个文件：`control.database` 保存控制数据，`control.stats_database` 保存统计数据。两条路径必须不同。程序会以 `0700` 创建缺失的父目录，并把数据库文件设为 `0600`。

首次用支持 UUIDv4 设备 token 的版本打开旧版 schema v1 控制库时，服务会在同一事务中创建 token 索引并升级到 schema v2。升级前先离线备份；升级后的数据库不能再由只支持 schema v1 的旧二进制打开。

运行中的维护任务会清理：

- 已到期或已撤销的面板会话；
- 超过 35 天的计费分钟用量；
- 超过 90 天的操作审计；
- 到期或撤销超过 35 天的 DNS 凭证；
- 超过 7 天的统计聚合和超过 24 小时的查询明细。

`mosdns control backup` 对控制库创建一致的离线备份。目标必须是新文件。统计库需要在服务停止后单独复制。恢复命令也只创建新目标，不覆盖现有文件。

## MySQL

MySQL 后端按用户、会话、凭证、计费用量、审计、统计维度和查询明细拆表。额度扣减、用户令牌桶与三份计费用量（全局、用户、设备）在同一事务中更新；同一用户的并发请求通过行锁串行化，避免超额放行。统计写入仍通过内存队列批量落库，控制路径不会等待查询明细写入。

服务启动时使用命名锁串行初始化表，并校验 `control` 与 `telemetry` 的 schema 版本。运行账号需要目标库的 `SELECT`、`INSERT`、`UPDATE`、`DELETE`、`CREATE` 权限，并能调用 `GET_LOCK`/`RELEASE_LOCK`。建议由数据库管理员提前创建数据库和专用账号；不要授予全局管理权限。

推荐把 MySQL 放在同机或低延迟内网。控制后端每次 DNS 请求都需要执行带行锁的事务，数据库不可用时请求会被拒绝，不能把高延迟公网数据库放在这条路径上。连接池与操作超时可在配置中调整。

MySQL 消除了本地文件容量和独占锁的限制，但当前版本仍按单个 Mosdns-x 实例验收。后续扩展多实例时，需要单独压测热点用户行锁，并为跨实例突发 QPS 评估 Redis 令牌桶；Redis 不是当前单机部署的必需组件。

MySQL 数据应使用数据库原生工具备份。内置 `control backup`/`restore` 只处理 bbolt 文件。备份策略至少覆盖所有 `mosdns_*` 表，并定期执行恢复演练。

## 从 bbolt 迁移到 MySQL

迁移要求 Mosdns-x 已停止，两个 bbolt 源文件处于离线状态，MySQL 目标表为空。先检查源库和记录数量，不连接 MySQL：

```sh
mosdns control migrate-mysql \
  --database /var/lib/mosdns/control.db \
  --stats-database /var/lib/mosdns/stats.db \
  --component all \
  --dry-run
```

确认数量后执行迁移：

```sh
mosdns control migrate-mysql \
  --database /var/lib/mosdns/control.db \
  --stats-database /var/lib/mosdns/stats.db \
  --config /etc/mosdns/config.yaml \
  --component all
```

`--config` 从受限配置文件读取控制与统计 DSN；两者配置为不同数据库时也会分别导入。也可直接传 `--mysql-dsn`，并用可选的 `--telemetry-mysql-dsn` 指定统计目标。每个组件在独立事务中导入，并拒绝写入非空目标。`all` 先提交控制数据，再提交统计数据；若第二步失败，可修复原因后用 `--component telemetry` 只重试统计数据。切换配置并启动后，检查管理员登录、用户数、设备凭证、当前额度、统计概览和查询明细。保留原 bbolt 文件，直到验证完成。

迁移保留现有密码摘要、会话、UUID 与旧格式设备凭证哈希、配额状态、计费用量、审计、统计聚合和查询明细。迁移不会产生新的明文 token；已有客户端继续使用原 token。
