# 多用户 DoH / DoH3 运维

多用户控制服务支持 bbolt 与 MySQL，当前仍按单机、单 Mosdns-x 实例部署和验收。bbolt 适合零依赖运行；MySQL 适合数据持续增长以及使用数据库集中备份的部署。MySQL 的额度扣减和 QPS 状态在行锁事务中更新，但多实例的热点用户性能及故障切换尚未作为生产能力验收。

## 本地初始化与启动

首次启动前创建管理员。密码只从标准输入读取，长度为 12～1024 字节；命令不接受密码参数或环境变量，也不会输出密码。标准输入必须只有一行。

### 控制存储状态

安装器会在启动 systemd 服务前执行以下检查。也可以在维护窗口中手动执行：

```bash
mosdns control status --config /etc/mosdns/config.yaml
```

命令只输出一个状态词，不会显示 DSN、密码或其他秘密。其稳定退出码如下：

| 输出 | 退出码 | 含义 |
| --- | ---: | --- |
| `ready` | 0 | 控制模式已启用，且至少有一个已启用的管理员。 |
| `disabled` | 10 | 配置没有启用控制模式，DNS 服务可正常启动。 |
| `uninitialized` | 11 | 控制存储可用，但尚未创建管理员。 |
| `storage_error` | 12 | 配置无法读取、存储不可用或存储格式无效。 |

bbolt 状态检查需要独占打开数据库，因此请在停止 Mosdns-x 后手动执行。安装器升级正在运行的 bbolt 实例时会先停止服务，确认状态或完成初始化后再启动。MySQL 状态检查会连接配置中的控制存储。

在脚本中判断状态时应直接比较退出码，不要匹配人类可读的错误文本。例如：

```bash
if mosdns control status --config /etc/mosdns/config.yaml; then
  echo '控制模式已就绪'
else
  case $? in
    10) echo '控制模式未启用，允许启动普通 DNS 服务' ;;
    11) echo '请先执行 control init-admin' ;;
    12) echo '请检查配置、数据库路径或 MySQL 连通性' ;;
    *) echo 'status 命令调用错误' ;;
  esac
fi
```

以下示例使用 Bash 的静默读取，密码不会出现在命令历史中：

```bash
read -r -s -p '管理员密码: ' MOSDNS_ADMIN_PASSWORD
printf '\n'
printf '%s\n' "$MOSDNS_ADMIN_PASSWORD" | mosdns control init-admin \
  --database /tmp/mosdns-control-local/control.db \
  --username admin
unset MOSDNS_ADMIN_PASSWORD
```

命令会以 `0700` 创建缺失的父目录，但不会修改已有目录的权限；数据库文件为 `0600`。`init-admin` 只允许空数据库。初始管理员启用，周期为每日、时区 UTC、周期额度 1,000,000、QPS 1,000、突发量 100、最多 10 个活跃设备凭证。启动后可在管理面板调整这些值。

控制数据使用 MySQL 时，把同一段密码输入改为：

```bash
printf '%s\n' "$MOSDNS_ADMIN_PASSWORD" | mosdns control init-admin \
  --config /etc/mosdns/config.yaml \
  --username admin
```

`--config` 会按 `control.storage.driver` 选择 bbolt 或 MySQL。也可直接使用 `--database` 或 `--mysql-dsn`，三者必须选择一个。MySQL DSN 放在权限受限的 Mosdns 配置中；面板管理员密码仍只从 stdin 输入，不能写进 YAML。完整参数和迁移步骤见[存储文档](storage.md)。

上面的数据库路径与仓库内的 [本地示例](../examples/control-local.yaml) 一致，仅用于回环地址测试。完成初始化后，在仓库根目录启动：

```sh
mosdns start -c examples/control-local.yaml
```

打开 `http://127.0.0.1:18081/login`，使用刚创建的管理员登录并创建用户。用户在凭证页创建专属地址后，将客户端指向该地址；本地示例对合法查询返回 `NOERROR`、空 Answer，不依赖公共上游。缺少凭证返回 HTTP 401；每个合法受理请求增加一次用户用量。生产环境将数据放在持久目录（以下备份示例使用 `./data/control.db`），并使用 HTTPS 公共 DNS URL、HTTPS 面板 Origin 和 Secure Cookie。

## 生产反向代理

从零安装、systemd 启动、双域名 HTTPS 和 HTTP/3 接入，见 [Ubuntu / Debian 部署教程](deployment.md)。配套提供 Mosdns、Caddy 和 systemd 示例。

面板和管理 API 必须通过 HTTPS 暴露，可以由 TLS 反向代理终止连接，并把请求转发给回环监听器。DNS 服务可以直接使用 Mosdns-x 的原生 DoH 和 DoH3 监听器；仅在部署架构需要时才让反向代理终止 DNS 的 HTTP/2 或 HTTP/3。只有明确配置的可信代理 CIDR 可以传递客户端地址；默认 `X-Forwarded-For` 会从右向左剥离可信代理。不要把服务直接配置为信任任意来源的转发头。

设备专属 DoH URL 和 `Authorization: Bearer` 值都是秘密。代理访问日志、错误日志、追踪系统和监控标签应删除 URL 中的设备 token，并删除 `Authorization`、Cookie 和 `X-CSRF-Token`。不要让 CDN、共享代理或浏览器缓存缓存 DNS 响应、管理 API、会话响应或含凭证的页面。管理 API 响应已发送 `Cache-Control: no-store`，外围代理仍需遵守该响应头。

查询配额在请求被持久化受理时扣减一次。之后的缓存命中、上游失败或 DNS 执行失败不会退款；客户端重试是新的请求。统计数据异步批量落库，存储故障时可能丢失统计明细，但不会撤销已经提交的计费扣减。

## 公共列表

管理员在面板的“公共列表”页创建、编辑、删除或手动刷新列表；相同名称不能重复。每项都需要名称、分类、HTTPS URL 和格式。格式仅支持 `mosdns` 与 `hosts`；URL 不接受用户信息或片段。可选的 SHA-256 必须为 64 位十六进制，用于校验下载内容。

`refresh_seconds` 未填写时默认 3600 秒，允许范围为 300～86400 秒。目录中的列表都会按该间隔刷新，因为用户可以单独启用管理员默认关闭的项目；面板会显示条目数、最近一次刷新时间、状态及安全处理后的错误摘要。刷新失败会保留上一次可用快照，包括生成该快照时使用的列表格式，修复来源后可在面板手动刷新。

用户在“订阅的公共列表”页按分类查看列表。开关默认继承管理员设置；用户切换后形成自己的启用或停用覆盖，不影响其他用户。管理员的启用状态是默认值，已有个人覆盖仍优先；删除列表才会对所有用户移除。管理员更新、删除或刷新成功，以及用户修改覆盖后，策略选择缓存会失效；新请求使用最新结果。

API 客户端可通过 `PATCH /api/v1/me/public-lists/{id}` 发送 `{"enabled":true}` 或 `{"enabled":false}` 设置覆盖；发送 `{"enabled":null}` 清除覆盖并恢复继承。管理员接口为 `GET/POST /api/v1/admin/public-lists`、`PATCH/DELETE /api/v1/admin/public-lists/{id}` 和 `POST /api/v1/admin/public-lists/{id}/refresh`。这些接口和面板写操作均需要管理员会话或当前用户会话以及 CSRF Token。

## 统计保留策略

`control.telemetry` 的以下三项只控制统计数据保留，不影响用户额度、账户、凭证或审计记录。省略或设置为 `0` 时使用默认值：

| 字段 | 默认值 | 允许范围 | 含义 |
| --- | ---: | ---: | --- |
| `aggregate_retention_days` | 7 天 | 1～31 天 | 分钟聚合、响应码和上游聚合的保留期。 |
| `query_retention_hours` | 24 小时 | 1～720 小时 | 仅在 `query_log: true` 时写入的查询明细保留期。 |
| `max_query_records` | 100000 条 | 1000～5000000 条 | 查询明细的总条数上限；即使时间尚未到期也会从最旧记录开始清理。 |

清理在统计写入后的维护过程中异步执行，缩短保留期不会立即删除全部旧数据；下一次刷新或维护后会逐步收敛。增大上限前先估算数据库容量，查询明细包含客户端地址、域名、Answer IP 和 EDNS 元数据，应按隐私要求限制访问。

## 托管运行配置与热重载

在 `control` 中设置 `managed_config` 后，管理员面板可以管理一份与主配置分离的运行参数。例如生产环境可使用：

```yaml
control:
  # 绝对路径；首次成功保存时由服务创建 managed.yaml 和历史目录。
  managed_config: /var/lib/mosdns/managed.yaml
```

主配置仍是监听器、TLS、API、控制数据库及其他基础设施的唯一来源。`managed_config` 不保存数据库 DSN、管理员密码、令牌或私有上游凭证。服务在目录不存在时以 `0700` 创建，并将配置及修订文件以 `0600` 原子替换；运行服务的账户必须能写入该目录。应将该路径置于受限的持久目录，禁止其他用户读取或手动编辑。

面板只会将以下项目列为可编辑项：

- 不含 SOCKS 凭证、用户名或 URL userinfo 的 `fast_forward` 上游；
- 没有 Redis 后端的 `cache` 插件的大小、懒缓存 TTL、应答 TTL 与压缩开关；
- `query_log` 和 `control.telemetry` 的三项保留策略。

其他插件，含有未知参数或敏感参数的上游，以及 Redis 缓存均显示为只读，响应中也不会返回其参数。托管文件不能新增、删除、重排插件，不能改变数据提供者、日志、监听器、TLS、API、安全配置或控制存储。需要这些变更时编辑主配置并执行 `systemctl restart mosdns`。

### 热重载和回滚

建议在面板依次执行“读取当前修订 → 验证 → 应用”。验证会构建候选运行代，不会切换当前 DNS 服务；验证令牌仅与当前登录会话和修订匹配，并在 5 分钟后失效。应用时会再次检查修订，若其他管理员已保存变更，则重新读取当前配置并重新验证，避免覆盖对方的修改。

应用成功后会先将运行代构建完成，再持久化修订并切换；候选构建或写入失败时，当前运行代保持服务。修改缓存参数会清空运行缓存，面板会在结果中明确显示该状态。

“重载主配置”只接受上述可热切换范围内的主配置变更。若响应包含 `restart_required`，表示监听器、TLS/API、控制存储、数据提供者、日志或不可托管插件已变更；当前 DNS 配置不会切换，应在维护窗口复核后执行 `systemctl restart mosdns`。

每次保存会将旧版本归档到 `managed.yaml.history/`，最多保留 10 个修订。发生错误时，在面板历史中选择目标修订并执行回滚；回滚同样会构建候选运行代，成功后才切换。不要通过复制历史文件直接覆盖 `managed.yaml`，这会绕过修订冲突检查并降低可审计性。

## 备份

本节命令仅适用于 bbolt。MySQL 部署使用数据库原生备份与恢复工具，并覆盖所有 `mosdns_*` 表。

CLI 备份要求数据库处于离线、未加锁状态；命令不会自动停止生产实例。先进入维护窗口并正常停止 Mosdns-x，再运行：

```sh
mosdns control backup \
  --database ./data/control.db \
  --output ./backup/control-$(date +%Y%m%d-%H%M%S).db
```

输出必须是新路径，命令不会覆盖已有文件。备份以 `0600` 创建，并验证数据库 schema。源文件不存在时命令失败且不会创建空数据库；仍有进程持锁时会明确失败。完成后再启动服务。

## 恢复

恢复也必须在维护窗口中执行。目标路径必须全新，命令不会覆盖当前数据库：

```sh
mosdns control restore \
  --input ./backup/control-20260911-120000.db \
  --database ./data/control-restored.db
```

验证恢复库后，在配置中切换数据库路径并启动服务。保留原数据库，直到确认管理员登录、用户状态、额度和设备凭证都正确。

恢复会同时恢复备份时刻的账户、会话、设备凭证、额度已用量和分钟用量。备份之后产生的扣费不会出现在恢复库中，因此恢复可能让额度回到旧值。安排完整维护窗口，确保停止服务后再选定恢复点，并在切换前核对维护窗口期间是否仍有其他入口产生用量。

## 定期维护

运行中的维护任务会清理过期会话、超过 35 天的已过期或已撤销凭证，以及超过各自保留期的分钟用量和审计记录，同时保留当前额度和有效设备 token。维护和关闭顺序由服务生命周期统一管理；不要在运行实例旁另启一个进程直接操作同一数据库。需要手工排障时，先正常停止实例并保存备份。
