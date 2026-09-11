# 多用户 DNS 服务配置

配置顶层加入 `control` 后启用单实例多用户模式。启动前先用 `control init-admin` 初始化账户数据库；空数据库会拒绝启动。账户库与统计库必须使用不同路径。程序会以 `0700` 创建缺失的统计库父目录，并将数据库文件设为 `0600`。

`api.http` 是管理 API 与面板监听地址，必须绑定回环地址。生产环境由本机反向代理提供 HTTPS。`public_dns_url` 的路径必须与每个 DoH/DoH3 listener 的 `url_path` 相同，省略时均为 `/dns-query`。公开 DNS listener 只允许 DoH/DoH3；UDP、TCP、DoT、DoQ 仅可绑定回环地址用于诊断，且不进入账户计量。控制模式不支持 PROXY protocol；可信代理地址通过 `trusted_proxies` 明确配置。

`development: true` 只允许管理 API、面板、公共 DNS URL 和所有 DNS listener 使用回环地址。该模式允许本地明文 HTTP。生产模式应保持 `development: false`，使用 TLS listener，并由反向代理保护管理 API。

```yaml
control:
  database: /var/lib/mosdns/control.db
  stats_database: /var/lib/mosdns/stats.db
  public_dns_url: https://dns.example.com/dns-query
  panel_origin: https://panel.example.com
  development: false
  query_log: false
  enable_pprof: false
  trusted_proxies:
    - 127.0.0.1/32
    - ::1/128
```

控制模式禁用 TLS early data 与 QUIC 0-RTT，避免可重放请求重复扣减额度。停止服务时会先关闭 listener、拒绝新请求并等待在途 DNS/API 请求完成，随后才关闭插件、统计库和账户库。
