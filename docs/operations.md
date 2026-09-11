# 多用户 DoH / DoH3 运维

多用户控制服务按单机、单 Mosdns-x 实例设计。账户状态、设备凭证、QPS 状态和额度扣减保存在同一个 bbolt 数据库中；不要让两个服务实例同时使用同一数据库，也不要用多副本负载均衡共享同一份额度。

## 本地初始化与启动

首次启动前创建管理员。密码只从标准输入读取，长度为 12～1024 字节；命令不接受密码参数或环境变量，也不会输出密码。标准输入必须只有一行。

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

上面的数据库路径与仓库内的 [本地示例](../examples/control-local.yaml) 一致，仅用于回环地址测试。完成初始化后，在仓库根目录启动：

```sh
mosdns start -c examples/control-local.yaml
```

打开 `http://127.0.0.1:18081/login`，使用刚创建的管理员登录并创建用户。用户在凭证页创建专属地址后，将客户端指向该地址；本地示例对合法查询返回 `NOERROR`、空 Answer，不依赖公共上游。缺少凭证返回 HTTP 401；每个合法受理请求增加一次用户用量。生产环境将数据放在持久目录（以下备份示例使用 `./data/control.db`），并使用 HTTPS 公共 DNS URL、HTTPS 面板 Origin 和 Secure Cookie。

## 生产反向代理

面板和管理 API 必须通过 HTTPS 暴露，可以由 TLS 反向代理终止连接，并把请求转发给回环监听器。DNS 服务可以直接使用 Mosdns-x 的原生 DoH 和 DoH3 监听器；仅在部署架构需要时才让反向代理终止 DNS 的 HTTP/2 或 HTTP/3。只有明确配置的可信代理 CIDR 可以传递客户端地址；默认 `X-Forwarded-For` 会从右向左剥离可信代理。不要把服务直接配置为信任任意来源的转发头。

设备专属 DoH URL 和 `Authorization: Bearer` 值都是秘密。代理访问日志、错误日志、追踪系统和监控标签应删除 URL 中的设备 token，并删除 `Authorization`、Cookie 和 `X-CSRF-Token`。不要让 CDN、共享代理或浏览器缓存缓存 DNS 响应、管理 API、会话响应或含凭证的页面。管理 API 响应已发送 `Cache-Control: no-store`，外围代理仍需遵守该响应头。

查询配额在请求被持久化受理时扣减一次。之后的缓存命中、上游失败或 DNS 执行失败不会退款；客户端重试是新的请求。系统只支持单实例严格额度，不能用多个独立数据库实例拼成共享额度。

## 备份

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
