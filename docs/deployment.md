# Ubuntu / Debian 单机部署

只需要安装 Mosdns-x 和 systemd 服务时，使用[一键安装脚本](installation.md)。本页余下内容用于需要自行配置多用户面板、Caddy 公网 HTTPS 和 HTTP/3 的部署。

本文部署一个 Mosdns-x 进程，由同机 Caddy 统一对外提供管理面板和公共 DoH。Caddy 在公网 `443/TCP` 接收 HTTPS/HTTP/2，在 `443/UDP` 接收 HTTP/3，并把两个域名分别反向代理到 Mosdns-x 的回环 HTTP 监听器。

此架构的 HTTP/3 终止在 Caddy。Mosdns-x 的 DNS 监听器必须使用明文 `http` 协议和回环地址，不要同时套用原生 `doh`/`doh3` 监听器、证书或公网 `:443` 配置。单机额度依赖单个进程和单份控制数据库，不要启动多个实例共享数据库。

## 前提与网络规划

准备一台 Ubuntu 或 Debian 主机，以及两个解析到该主机公网地址的域名。生产二进制应在可信的本地构建机生成并发布到 [MiCat-S/mosdns-x Releases](https://github.com/MiCat-S/mosdns-x/releases)；部署机不安装 Git、Go、Node.js，也不 clone 或编译源码。若 GitHub 目前没有所需 Release，发布人应先按[发布流程](releasing.md)生成并发布资产。

部署命令使用 Bash、sudo、curl、Python 3、ca-certificates、unzip 和 sha256sum。全文中的 `panel.example.com` 和 `dns.example.com` 都是占位符，部署前必须替换为自己控制且已正确解析的真实域名。

```bash
sudo apt update
sudo apt install -y bash sudo curl python3 ca-certificates unzip coreutils
```

部署前确认公网入口和云防火墙允许 `80/TCP`、`443/TCP` 与 `443/UDP`。Caddy 的 [Automatic HTTPS](https://caddyserver.com/docs/automatic-https)使用 80/443 端口完成重定向、证书验证和 HTTPS 服务；`443/UDP` 用于 HTTP/3。两个域名的 A/AAAA 记录都必须指向实际可达的地址。

| 用途 | 地址 | 监听者 | 可见范围 |
| --- | --- | --- | --- |
| HTTP 重定向与证书验证 | `80/TCP` | Caddy | 公网 |
| 管理面板与 API | `https://panel.example.com` | Caddy `:443` | 公网 |
| 公共 DoH | `https://dns.example.com/dns-query` | Caddy `:443` | 公网 |
| 管理端回源 | `127.0.0.1:18081` | Mosdns-x | 仅本机 |
| DoH 明文回源 | `127.0.0.1:18443` | Mosdns-x | 仅本机 |

多用户控制功能的代码基线是 `e2f134d`。部署时使用明确的 `<RELEASE_TAG>`，并按机器架构选择资产：

| Linux 架构 | Release 资产 |
| --- | --- |
| x86-64 通用 | `mosdns-linux-amd64.zip` |
| x86-64 且确认支持 GOAMD64 v3 | `mosdns-linux-amd64-v3.zip` |
| ARM64 / aarch64 | `mosdns-linux-arm64.zip` |
| 32 位 ARM v5/v6/v7 | `mosdns-linux-arm-5.zip` / `-6.zip` / `-7.zip` |
| MIPS little-endian softfloat | `mosdns-linux-mipsle-softfloat.zip` |
| MIPS64 little-endian hardfloat | `mosdns-linux-mips64le-hardfloat.zip` |
| ppc64le | `mosdns-linux-ppc64le.zip` |

`uname -m` 可帮助识别架构。只有明确确认 CPU 支持 x86-64-v3 时才选 v3 包；不确定时选择通用 amd64。下载 zip 和同一 Release 的 `SHA256SUMS`，提取且只校验所选资产对应的一行：

```bash
(
set -euo pipefail
RELEASE_TAG='<RELEASE_TAG>'
ASSET='mosdns-linux-amd64.zip' # 按上表修改
RELEASE_BASE="https://github.com/MiCat-S/mosdns-x/releases/download/$RELEASE_TAG"
INSTALL_DIR=$(mktemp -d)
chmod 0700 "$INSTALL_DIR"
trap 'rm -rf "$INSTALL_DIR"' EXIT
cd "$INSTALL_DIR"
curl --fail --location --proto '=https' --proto-redir '=https' \
  --output "$ASSET" "$RELEASE_BASE/$ASSET"
curl --fail --location --proto '=https' --proto-redir '=https' \
  --output SHA256SUMS "$RELEASE_BASE/SHA256SUMS"
CHECKSUM_LINE=$(awk -v asset="$ASSET" '$2 == asset { print }' SHA256SUMS)
test "$(printf '%s\n' "$CHECKSUM_LINE" | grep -c .)" -eq 1
printf '%s\n' "$CHECKSUM_LINE" | sha256sum -c -
mkdir extracted
unzip -q "$ASSET" -d extracted
sudo install -o root -g root -m 0755 extracted/mosdns /usr/local/bin/mosdns
)
```

校验必须显示所选 zip 为 `OK` 后才能安装。Release 包已经嵌入管理 UI，部署机无需 Node.js 或单独的静态文件。把 Release tag、资产名和校验结果记入变更记录。

## 创建运行账户和目录

创建无登录 shell 的专用账户。数据目录只允许该账户和 root 访问；配置由 root 管理，Mosdns-x 只需读取：

```bash
sudo useradd --system --home-dir /var/lib/mosdns --create-home \
  --user-group --shell /usr/sbin/nologin mosdns
sudo install -d -o mosdns -g mosdns -m 0700 /var/lib/mosdns
sudo install -d -o root -g mosdns -m 0750 /etc/mosdns
sudo install -d -o mosdns -g mosdns -m 0700 /var/backups/mosdns
```

首次启动前初始化唯一的管理员。密码必须为 12～1024 字节，只在这次初始化时从标准输入读取，不能写进 YAML。配置文件经常进入 Git、备份和主机分发流程，把明文密码放入配置会扩大泄露范围。初始化后，密码属于可在线修改的账户状态；若 YAML 仍保留初始密码，还会产生重启时是否覆盖数据库现有密码的歧义。控制数据库只保存使用随机盐计算的 Argon2id 密码摘要，不保存可还原的明文。

以下 Bash 命令采用静默输入，密码不会作为命令参数或出现在 shell 历史中：

```bash
sudo -v
read -r -s -p '管理员密码: ' MOSDNS_ADMIN_PASSWORD
printf '\n'
printf '%s\n' "$MOSDNS_ADMIN_PASSWORD" | \
  sudo -u mosdns /usr/local/bin/mosdns control init-admin \
    --database /var/lib/mosdns/control.db \
    --username admin
unset MOSDNS_ADMIN_PASSWORD
```

标准输入只能有一行。初始化只允许空数据库；重复执行会失败，不会重置现有管理员。

自动化初始化时，可由受控的 secret manager 在运行时把秘密直接写入该命令的 stdin，并在使用后清理临时变量或文件；不要把密码硬编码到配置、脚本、命令参数、环境清单或 CI 日志中。

## Mosdns-x 配置

仓库中的 [生产反代示例](../examples/control-production-proxy.yaml)与下文一致。部署机没有源码 checkout，因此先创建受限文件，再用 `sudoedit` 粘贴下文并修改真实域名和上游：

```bash
sudo install -m 0640 -o root -g mosdns /dev/null /etc/mosdns/config.yaml
sudoedit /etc/mosdns/config.yaml
```

文件内容如下。示例上游 `1.1.1.1:53` 和 `9.9.9.9:53` 使用明文 UDP，仅用于展示可运行的转发配置，不代表 DNS 链路全程加密。部署者应根据所在网络、隐私要求和解析策略替换上游。生产配置使用缓存和真实转发，不使用返回空应答的 `blackhole`：

```yaml
log:
  level: info

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

api:
  http: 127.0.0.1:18081

plugins:
  - tag: cache
    type: cache
    args:
      size: 10240

  - tag: upstream
    type: fast_forward
    args:
      upstream:
        - addr: udp://1.1.1.1:53
        - addr: udp://9.9.9.9:53

  - tag: main
    type: sequence
    args:
      exec:
        - cache
        - upstream

servers:
  - exec: main
    listeners:
      - protocol: http
        addr: 127.0.0.1:18443
        url_path: /dns-query
```

`public_dns_url` 决定面板生成的设备专属地址，`panel_origin` 用于管理写操作的 Origin 校验，两者必须和公网 HTTPS 域名完全一致。`trusted_proxies` 只信任本机 Caddy；不要加入任意公网网段。控制库与统计库必须是不同文件。`query_log: false` 仍会保留聚合统计；若确需逐条查询明细，可改为 `true`，但应同时评估数据保留、磁盘占用和域名隐私。

以后修改该配置文件需要执行 `sudo systemctl restart mosdns` 才会生效。通过管理面板修改账户、额度或凭证则实时生效，无需重启。

## systemd 服务

仓库中的 [systemd 示例](../examples/mosdns.service)是本部署的标准单元。部署机没有源码 checkout，因此创建文件并用 `sudoedit` 写入以下内容：

```bash
sudo install -o root -g root -m 0644 /dev/null /etc/systemd/system/mosdns.service
sudoedit /etc/systemd/system/mosdns.service
```

```ini
[Unit]
Description=mosdns multi-user DNS service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mosdns
Group=mosdns
WorkingDirectory=/var/lib/mosdns
ExecStart=/usr/local/bin/mosdns start -c /etc/mosdns/config.yaml
Restart=on-failure
RestartSec=3
TimeoutStopSec=30
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/mosdns
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

该单元以 `mosdns:mosdns` 运行，工作目录为 `/var/lib/mosdns`，将持久化写入限制在数据目录，同时通过 `PrivateTmp` 提供隔离的临时目录；它还设置 `UMask=0077`、`LimitNOFILE=65536` 和 30 秒停止超时，并启用基础 systemd 隔离。

加载并启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now mosdns
sudo systemctl status mosdns --no-pager
```

先确认两个回环入口已监听，再配置公网代理：

```bash
sudo ss -lntp | grep -E '127\.0\.0\.1:(18081|18443)'
```

## 安装和配置 Caddy

按 [Caddy 官方 Debian/Ubuntu 安装说明](https://caddyserver.com/docs/install#debian-ubuntu-raspbian)添加官方软件源并安装发行包。要求使用 Caddy 2.11.4，或支持下列指令的更新版本；安装后用 `caddy version` 核对版本。官方软件包提供 systemd 服务。Caddy 默认支持 HTTPS，并在客户端和网络支持时提供 HTTP/3；反向代理语法见 [Caddy `reverse_proxy` 文档](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)。

仓库中的 [Caddyfile 示例](../examples/Caddyfile)可用于一台新装且尚未承载其他网站的 Caddy 主机。部署机没有源码 checkout，因此创建文件后用 `sudoedit` 写入下文：

```bash
sudo install -o root -g root -m 0644 /dev/null /etc/caddy/Caddyfile
sudoedit /etc/caddy/Caddyfile
```

编辑时把两个 `example.com` 站点名换成与 Mosdns-x 配置一致的真实域名。

如果 Caddy 已经承载网站，不要覆盖现有文件；把下面的全局选项与两个站点块审慎合并进现有配置，并先在测试环境校验。示例的完整内容是：

```caddyfile
{
	servers {
		protocols h1 h2 h3
		# DoH 请求会扣减额度，禁用 QUIC 0-RTT 以避免早期数据重放。
		0rtt off
	}

	# 即使未启用访问日志，运行时错误也可能包含请求信息。
	log default {
		format filter {
			wrap json
			fields {
				request>uri delete
				request>headers delete
				resp_headers delete
			}
		}
	}
}

panel.example.com {
	reverse_proxy 127.0.0.1:18081
}

dns.example.com {
	# 设备 token 可能位于 URL 路径中，因此不要为此站点开启访问日志。
	reverse_proxy 127.0.0.1:18443
}
```

`0rtt off` 的含义和可用位置见 [Caddy 全局选项文档](https://caddyserver.com/docs/caddyfile/options#0rtt)，日志过滤语法见 [全局 `log` 选项](https://caddyserver.com/docs/caddyfile/options#log)。Caddy 默认不会信任客户端传入的 `X-Forwarded-*`，并会把 Host、Origin 和 Authorization 正常传给回源，因此这里不添加自定义 `header_up`。不要启用全局 `debug` 或另外配置未脱敏的访问日志。

校验配置并平滑加载：

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
sudo systemctl status caddy --no-pager
```

不要在 Caddy、CDN、WAF 或其他代理中记录完整设备 URL、`Authorization`、Cookie 或 `X-CSRF-Token`。不要缓存 `/dns-query`、管理 API、会话响应或含凭证的页面。若前方还有代理，需重新设计可信代理链；不要直接扩大 Mosdns-x 的 `trusted_proxies`。

## 开户和验证

访问 `https://panel.example.com/login`，使用初始化管理员登录，在管理页创建普通用户并设置启用状态、服务到期时间、日/月额度、QPS、突发量和最大活跃设备数。用户登录后可创建设备凭证。专属 URL 和 Bearer token 只在创建或轮换时显示一次，应立即保存到客户端的秘密存储；列表不会再次显示秘密。

可用一个最小 DNS wire message 分别验证两种认证方式。每次请求只选择专属 URL 或 Bearer 其中一种，不要把两种凭证同时发送。下面的 Bash 流程把秘密写入权限为 `0600` 的 curl 配置文件，curl 的进程参数中只有临时文件路径；退出 shell 时自动删除查询、响应和配置：

```bash
(
set -euo pipefail
VERIFY_DIR=$(mktemp -d)
chmod 0700 "$VERIFY_DIR"
export VERIFY_DIR
trap 'rm -rf "$VERIFY_DIR"' EXIT

python3 - <<'PY'
from pathlib import Path
import os
Path(os.environ['VERIFY_DIR'], 'query.bin').write_bytes(
    bytes.fromhex('123401000001000000000000076578616d706c6503636f6d0000010001')
)
PY

read -r -s -p '设备专属 DoH URL: ' DEVICE_DOH_URL
printf '\n'
printf 'url = "%s"\n' "$DEVICE_DOH_URL" >"$VERIFY_DIR/url.conf"
chmod 0600 "$VERIFY_DIR/url.conf"
unset DEVICE_DOH_URL
H2_RESULT=$(curl --silent --show-error --fail-with-body --proto '=https' --http2 \
  --config "$VERIFY_DIR/url.conf" \
  -H 'Content-Type: application/dns-message' \
  -H 'Accept: application/dns-message' \
  --data-binary "@$VERIFY_DIR/query.bin" \
  --output "$VERIFY_DIR/response-h2.bin" \
  --write-out '%{http_code} %{http_version}')
printf '专属 URL: HTTP %s\n' "$H2_RESULT"
test "$H2_RESULT" = '200 2'

read -r -s -p 'Bearer token: ' DEVICE_BEARER_TOKEN
printf '\n'
printf 'header = "Authorization: Bearer %s"\n' "$DEVICE_BEARER_TOKEN" \
  >"$VERIFY_DIR/bearer.conf"
chmod 0600 "$VERIFY_DIR/bearer.conf"
unset DEVICE_BEARER_TOKEN
H3_RESULT=$(curl --silent --show-error --fail-with-body --proto '=https' --http3-only \
  --config "$VERIFY_DIR/bearer.conf" \
  -H 'Content-Type: application/dns-message' \
  -H 'Accept: application/dns-message' \
  --data-binary "@$VERIFY_DIR/query.bin" \
  https://dns.example.com/dns-query \
  --output "$VERIFY_DIR/response-h3.bin" \
  --write-out '%{http_code} %{http_version}')
printf 'Bearer: HTTP %s\n' "$H3_RESULT"
test "$H3_RESULT" = '200 3'

python3 - <<'PY'
import os, struct
from pathlib import Path
for name in ('response-h2.bin', 'response-h3.bin'):
    data = Path(os.environ['VERIFY_DIR'], name).read_bytes()
    if len(data) < 12:
        raise SystemExit(f'{name}: DNS 响应短于 12 字节')
    ident, flags, qd, an, ns, ar = struct.unpack('!6H', data[:12])
    qr, rcode = (flags >> 15) & 1, flags & 15
    print(f'{name}: id={ident:#06x} qr={qr} rcode={rcode} question={qd} answer={an} authority={ns} additional={ar}')
    if ident != 0x1234 or qr != 1 or rcode != 0 or qd != 1 or an < 1:
        raise SystemExit(f'{name}: 未得到带 Answer 的 NOERROR DNS 响应')
PY

rm -rf "$VERIFY_DIR"
unset VERIFY_DIR
trap - EXIT
)
```

`curl --http2` 允许协议协商回落，因此脚本明确要求 `%{http_version}` 为 `2`；`--http3-only` 不回落，并要求值为 `3`，具体选项见 [curl 官方手册](https://curl.se/docs/manpage.html#--http3-only)。HTTP/3 测试需要 curl 构建本身支持 HTTP/3。不要把真实 URL 或 token 直接写进命令历史、工单和聊天记录。每个被 Mosdns-x 正式受理的客户端请求都会计入额度，包括缓存命中；失败后的客户端重试是新请求。由于 HTTP/3 终止于 Caddy，Mosdns-x 的逐条查询明细会把 HTTP/1 回源归类为 `http`；客户端是否使用 HTTP/3 应以 curl 的 `http_version=3` 或 Caddy 侧的脱敏指标确认。

上线前还应使用专门创建的低额度测试用户完成以下验收，结束后撤销其凭证。统计采用异步批量写入，页面数字可能短暂滞后，不应据此重复发送大量请求：

| 场景 | 操作 | 预期 |
| --- | --- | --- |
| 无凭证 | 不带专属路径和 Bearer 请求 `/dns-query` | HTTP 401 |
| 正常凭证 | 分别按上面的专属 URL、Bearer 流程查询 | HTTP 200，DNS `rcode=0` 且有 Answer |
| 撤销凭证 | 在面板撤销后再次请求 | HTTP 403 |
| 额度耗尽 | 将测试用户周期额度设为小值，发送恰好超过额度的请求 | 额度内 HTTP 200，下一次 HTTP 429 |
| 服务到期 | 将测试用户服务到期时间设为过去，再请求 DNS 和登录面板 | DNS HTTP 403，用户仍可登录面板查看状态 |
| 聚合统计 | 等待短暂刷新后查看用户/全局统计 | 已受理数包含缓存命中，拒绝请求不增加额度 |

## 备份、恢复与升级

控制数据库 CLI 要求离线操作，不会自动停止服务。备份前进入维护窗口并停止 Mosdns-x：

```bash
sudo systemctl stop mosdns
sudo -u mosdns /usr/local/bin/mosdns control backup \
  --database /var/lib/mosdns/control.db \
  --output /var/backups/mosdns/control-$(date +%Y%m%d-%H%M%S).db
sudo systemctl start mosdns
```

备份目标必须是新文件。备份包含账户、会话、凭证、额度和用量；恢复旧备份也会恢复旧的已用额度，因此应保留完整维护窗口，避免停机后仍有其他实例受理请求。

恢复时保持 Mosdns-x 停止，并恢复到一个全新的数据库路径：

```bash
sudo systemctl stop mosdns
sudo -u mosdns /usr/local/bin/mosdns control restore \
  --input /var/backups/mosdns/control-20260911-120000.db \
  --database /var/lib/mosdns/control-restored.db
```

随后将 `/etc/mosdns/config.yaml` 的 `control.database` 改为新路径，检查属主和 `0600` 权限，再启动服务并验证管理员登录、用户额度和已有设备凭证。恢复不会覆盖当前数据库，确认无误前应保留原文件。

升级包应由发布人在可信构建机提前生成。维护窗口外先按本页下载流程取得新 tag 的架构匹配 zip，精确校验 SHA-256 并解压到单独临时目录。进入维护窗口后才停止服务、制作离线备份、安装已校验的新二进制并启动，然后检查日志、面板、DoH 和额度。生产机始终无需源码和构建工具。

回滚前必须先停止服务并判断数据兼容性。若新版本没有迁移数据库且旧程序能读取现库，恢复旧二进制可保留升级后的真实用量；若数据库已经发生不向后兼容的迁移，只能把升级前备份恢复到一个新路径。后者会丢失升级后已经受理的用量，不能无条件执行，应延长停服窗口、核对这段期间的计量并制定补偿方案，再由管理员决定恢复点。不要让旧程序直接试开可能已迁移的生产数据库。

## 故障排查

- **Caddy 无法签发证书**：检查两个域名的 A/AAAA 记录、主机公网可达性、`80/TCP` 与 `443/TCP`，并查看 `journalctl -u caddy`。存在错误 AAAA 记录时，CA 可能从不可达的 IPv6 地址验证。
- **HTTP/2 正常但 HTTP/3 不通**：检查安全组、主机防火墙和上游网络是否允许 `443/UDP`，并确认客户端 curl 支持 HTTP/3。此部署的 QUIC 由 Caddy 处理，Mosdns-x 不应监听公网 UDP 443。
- **面板返回 Origin 或 CSRF 错误**：确认浏览器访问地址恰好是 `https://panel.example.com`，配置中的 `panel_origin` 没有多余路径或端口，并避免从 IP 地址打开面板。
- **设备请求返回 401**：重新复制完整专属 URL 或 Bearer token；秘密只显示一次。不要把 DNS token 当成面板会话使用。
- **设备请求返回 403**：检查用户或凭证是否启用、是否撤销，以及用户服务期和凭证有效期是否到期。
- **返回 429**：检查用户共享 QPS、突发量和当前日/月额度。两个设备共享同一用户的限速和额度。
- **Caddy 返回 502**：先确认 Mosdns-x 正在运行，并从本机检查 Caddy 到 `127.0.0.1:18081` 或 `127.0.0.1:18443` 的回环连接；502 表示代理回源阶段失败，还没有到公共 DNS 上游。
- **DNS 返回 SERVFAIL**：查看 `journalctl -u mosdns`，从主机检查到示例上游 IP 的 UDP 53 可达性，并按网络策略更换上游。本配置没有要求上游 TCP 53。
- **Mosdns-x 无法启动**：运行 `systemctl status mosdns` 和 `journalctl -u mosdns`，检查两个数据库路径不同、父目录可写、回环端口未占用，以及 `public_dns_url`/`panel_origin` 均为 HTTPS。排障输出可能含路径、用户名或请求信息，分享前先脱敏。
- **数据库被锁**：确认只有一个 Mosdns-x 实例。执行备份或恢复前必须停止服务；不要复制一个正在写入的 bbolt 文件作为备份。

## 已完成的验证与环境边界

主模块已使用 Caddy 2.11.4 官方校验和核对二进制，并确认示例通过 `caddy validate`。在临时证书、回环高端口和本地假上游环境中同时运行 Caddy 与 Mosdns-x 后，以下项目已通过：HTTPS Secure Cookie 与 CSRF 登录、HTTP/2 专属 URL、HTTP/3 Bearer、两种 DNS 请求均返回 `NOERROR` 和 A 记录 `192.0.2.53`、两次请求计入额度 2 且第二次缓存命中，以及关闭 Mosdns-x 回源后的 502 日志不含测试 token。

上述验证没有覆盖 Ubuntu/Debian systemd 实机安装、真实域名的公开证书签发或实际云防火墙。上线前仍应在目标环境检查服务账户与目录权限、80/TCP、443/TCP、443/UDP、真实上游、额度以及备份恢复流程。
