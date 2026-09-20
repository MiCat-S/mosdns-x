# 多用户 DNS 服务配置

配置顶层加入 `control` 后启用单实例多用户模式。启动前先用 `control init-admin` 初始化账户存储；没有管理员时会拒绝启动。默认使用 bbolt，此时账户库与统计库必须使用不同路径，程序会以 `0700` 创建缺失的父目录，并将数据库文件设为 `0600`。需要集中存储时可把控制数据和统计数据切换到 MySQL。

`api.http` 是管理 API 与面板监听地址，必须绑定回环地址。生产环境由本机反向代理提供 HTTPS。`public_dns_url` 的路径必须与每个 DoH/DoH3 listener 的 `url_path` 相同，省略时均为 `/dns-query`。公开 DNS listener 只允许 DoH/DoH3；UDP、TCP、DoT、DoQ 仅可绑定回环地址用于诊断，且不进入账户计量。控制模式不支持 PROXY protocol；可信代理地址通过 `trusted_proxies` 明确配置。

`development: true` 只允许管理 API、面板、公共 DNS URL 和所有 DNS listener 使用回环地址。该模式允许本地明文 HTTP。生产模式保持 `development: false`：DNS 可使用原生 TLS listener，或由同机 TLS 反向代理转发到回环 HTTP listener；管理 API 由反向代理提供 HTTPS。后一种方式的完整步骤见 [部署教程](deployment.md)，配套配置为 [control-production-proxy.yaml](../examples/control-production-proxy.yaml)。选择一种公网监听方式，避免 Mosdns 与代理争用同一 IP 的 443 端口。

```yaml
control:
  database: /var/lib/mosdns/control.db
  stats_database: /var/lib/mosdns/stats.db
  public_dns_url: https://dns.example.com/dns-query
  panel_origin: https://panel.example.com
  development: false
  query_log: false
  # 可选：启用管理端安全结构化配置和运行代热重载。
  managed_config: /var/lib/mosdns/managed.yaml
  enable_pprof: false
  trusted_proxies:
    - 127.0.0.1/32
    - ::1/128
```

使用 MySQL 5.7 或 8.4 时，`storage.driver: mysql` 会同时让未显式指定驱动的 `telemetry` 使用 MySQL，并复用控制存储的 DSN：

```yaml
control:
  public_dns_url: https://dns.example.com/dns-query
  panel_origin: https://panel.example.com
  development: false
  query_log: true
  enable_pprof: false
  trusted_proxies:
    - 127.0.0.1/32

  storage:
    driver: mysql
    mysql:
      dsn: "mosdns:CHANGE_ME@tcp(127.0.0.1:3306)/mosdns?charset=utf8mb4&timeout=5s&readTimeout=5s&writeTimeout=5s"
      max_open_conns: 32
      max_idle_conns: 8
      conn_max_lifetime_sec: 1800
      operation_timeout_ms: 1500

  telemetry:
    driver: mysql
    queue_size: 8192
    batch_size: 256
    flush_interval_ms: 1000
    aggregate_retention_days: 7
    query_retention_hours: 24
    max_query_records: 100000
```

`telemetry.mysql` 支持与 `storage.mysql` 相同的连接参数。省略 `telemetry.mysql.dsn` 时复用控制 DSN；配置独立 DSN 时可把高写入量的查询明细放到另一个数据库。`queue_size`、`batch_size` 和 `flush_interval_ms` 为零时使用内置默认值，不能为负数。聚合保留期允许 1～31 天，查询明细保留期允许 1～720 小时，查询明细条数上限允许 1,000～5,000,000；省略或设为零时分别使用 7 天、24 小时和 100,000 条。管理面板的系统页会显示实际生效的控制与统计驱动，不返回 DSN。

`managed_config` 应使用运行账户可写的绝对路径；文件可以在首次启动时不存在，第一次成功应用后由服务以 `0600` 创建。面板只会保存不含敏感参数的 `fast_forward` 上游、非 Redis 内存缓存、查询日志开关和上述保留策略；完整 YAML、监听地址、证书和数据库连接仍由主配置维护。省略 `managed_config` 时，运行配置页仍提供脱敏只读摘要和节点数据源状态，写入、历史、回滚与探测保持禁用。详细热重载与回滚流程见[运维文档](operations.md#托管运行配置与热重载)。

也可以混合使用后端。例如账户和严格额度放 MySQL，统计暂留 bbolt：

```yaml
control:
  stats_database: /var/lib/mosdns/stats.db
  storage:
    driver: mysql
    mysql:
      dsn: "mosdns:CHANGE_ME@tcp(127.0.0.1:3306)/mosdns"
  telemetry:
    driver: bbolt
```

MySQL 表会在首次连接时自动创建。首次管理员使用 `mosdns control init-admin --config /etc/mosdns/config.yaml --username admin` 初始化，命令从配置读取 DSN，管理员密码仍从 stdin 读取。已有 bbolt 数据应按[存储文档](storage.md)的离线迁移流程导入。

控制模式禁用 TLS early data 与 QUIC 0-RTT，避免可重放请求重复扣减额度。停止服务时会先关闭 listener、拒绝新请求并等待在途 DNS/API 请求完成，随后才关闭插件、统计库和账户库。

## 受理路径调优（`control.admit`）

每个经鉴权的 DoH/DoH3 查询在放行前都要提交一次受理事务（扣额度、扣 QPS 令牌、写用量）。这个约束不可调：`admit` 下的参数只改变"多少次受理共用一次提交"和"槽位占满时等多久"，不会跳过落盘、异步补扣或超额放行。

```yaml
control:
  admit:
    queue_size: 1024      # 并发在途受理上限
    wait_ms: 250          # 槽位占满时的最长排队时间，超时返回可重试的不可用错误
    batch_delay_ms: 1     # 一次提交为收集更多受理而等待的时间
    batch_size: 128       # 一次提交最多合并的受理数
```

全部省略时使用上面这组默认值。

调优方向：

- **突发被拒（日志出现 `admit queue is saturated`，客户端收到 503）**：先调大 `wait_ms`，让突发被排队而不是拒绝；再考虑调大 `queue_size`。`wait_ms` 应小于 server 的 `timeout`，否则查询会先超时。
- **吞吐受限于磁盘 fsync**：调大 `batch_delay_ms` 和 `batch_size`，用一次 fsync 摊薄更多受理。代价是每个查询固定增加最多 `batch_delay_ms` 的延迟。在高并发下这通常是划算的；低并发下只会徒增延迟。
- **延迟敏感且并发不高**：保持 `batch_delay_ms: 1`。

这些参数只对 bbolt 存储生效。`storage.driver: mysql` 的受理路径由连接池参数（`max_open_conns` 等）控制。

改动后请按 [性能验证](performance.md) 的方法在目标机器和磁盘上实测，不要沿用别处的数字。
