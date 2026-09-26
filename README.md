# Mosdns-x（多用户 DoH 服务版）

[![Release](https://img.shields.io/github/v/release/MiCat-S/mosdns-x?include_prereleases&label=release)](https://github.com/MiCat-S/mosdns-x/releases)

本仓库 Fork 自 [pmkol/mosdns-x](https://github.com/pmkol/mosdns-x)。Mosdns-x 是用 Go 编写的高性能 DNS 转发器，通过插件流水线定制 DNS 处理逻辑，支持 UDP、TCP、DoT、DoQ、DoH 和 DoH3。

本 Fork 在此基础上增加了**多用户 DoH / DoH3 服务**：一个 Mosdns 进程同时提供 DNS、管理 API 和网页面板。管理员开设账户、分配额度；用户为每台设备生成专属的加密 DNS 地址，并在自己的面板里调整拦截规则、隐私选项和查询日志。

## 功能

**账户与计量**

- 管理员开设、编辑、停用和删除账户，设置到期时间、每日／每月额度、QPS 与突发量、设备数上限。删除账户会一并清除该用户的全部数据。
- 每台设备一个 UUID 凭证，可用专属 URL 或 `Authorization: Bearer` 接入；凭证可随时轮换或撤销。
- 用量按全局、用户、设备三级准确计量，额度扣减与请求受理在同一事务中完成。

**用户自助设置**

- 安全与隐私：DNS 重绑定防护、按查询类型拦截（包括 HTTPS／SVCB）、移除 ECS，以及一键开启管理员提供的恶意域名情报列表。
- 自定义规则：拦截、放行，以及 A／AAAA／CNAME 重写；支持精确、后缀、关键字和正则匹配。
- 公共订阅列表：管理员维护目录，用户按需开关。
- 响应优化：IPv4／IPv6 地址族偏好、TTL 上下限、展平 CNAME 链、打乱应答顺序、ECS 地址覆写。
- 一键安全模式，以及临时暂停全部个人策略。
- 自己决定是否记录详细查询日志、保留多久。
- DNS Lookup：用当前生效的完整策略链测试任意域名。

**管理与运维**

- 控制数据和统计数据可存 bbolt（零依赖），也可存 MySQL 5.7 / 8.4。
- 统计与查询日志：可以看出每个应答来自上游、缓存、规则还是公共列表。
- 审计日志、系统健康监控，以及受保护的 Prometheus `/metrics`。
- 托管运行配置：在面板中校验、热更新和回滚上游、缓存及统计保留策略，失败时不影响正在运行的配置。
- 离线备份与恢复、bbolt 到 MySQL 迁移，以及用于找回管理员的离线 `reset-password`。

## 快速开始

在使用 systemd 的 Linux 主机上安装或升级：

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | sudo bash
```

脚本会：

- 识别 CPU 架构，下载本 Fork 的 Release 并校验 SHA-256。
- 安装 `/usr/local/bin/mosdns` 和 systemd 服务，然后启动服务。
- 已有的 `/etc/mosdns/config.yaml` 不会被覆盖。
- 在中国大陆出口下，安装包默认经 `gh-proxy.com` 下载，但校验文件始终直连 GitHub。

它不会安装反向代理、创建域名或设置管理员密码，详见[一键安装](docs/installation.md)。

默认配置只是一个监听 `127.0.0.1:5533` 的普通 DNS 转发器，**不含多用户模式**。启用多用户服务：

1. 在配置中加入 `control` 段，写明公共 DNS 地址和面板地址，见[多用户配置](docs/service-config.md)。
2. 用 `mosdns control init-admin` 创建第一个管理员，密码只从标准输入读取，见[运维文档](docs/operations.md#本地初始化与启动)。
3. 用同机反向代理为面板和 DoH 提供 HTTPS，见[部署教程：Ubuntu / Debian + Caddy](docs/deployment.md)。
4. 以管理员身份打开 `/admin` 开设用户。用户登录 `/app` 后，在账户页创建设备凭证，把得到的地址填进系统或浏览器的加密 DNS 设置。

检查多用户模式是否就绪：

```bash
mosdns control status --config /etc/mosdns/config.yaml
```

## 文档

| 主题 | 文档 |
|---|---|
| 安装与升级 | [一键安装](docs/installation.md)、[部署教程](docs/deployment.md) |
| 配置 | [多用户配置](docs/service-config.md)、[存储与迁移](docs/storage.md) |
| 日常运维 | [初始化、备份恢复、托管配置与密码重置](docs/operations.md)、[安全与运行监控](docs/monitoring.md) |
| 开发 | [服务架构](docs/service-architecture.md)、[API 契约](docs/control-api.md)、[构建](docs/building.md)、[发布](docs/releasing.md) |
| 验收 | [开发审核与验收](docs/development-review.md)、[性能验证](docs/performance.md) |

## 适用范围

- 按单机、单实例部署和验收。MySQL 后端的多实例并发与故障切换尚未作为生产能力验证。
- Release 由维护者本地构建后上传，没有签名或 provenance 证明。对供应链有更高要求的部署请按[构建说明](docs/building.md)自行构建。
- 主配置仍以文件为准；面板只能修改不含敏感参数的托管部分。

## 上游与社区

- 原版 Mosdns-x 的功能说明、插件配置和教程见 [上游 Wiki](https://github.com/pmkol/mosdns-x/wiki)，原版预编译文件见 [上游 Release](https://github.com/pmkol/mosdns-x/releases)。本 Fork 的安装包请使用[本仓库 Release](https://github.com/MiCat-S/mosdns-x/releases)。
- 电报社区（上游）：[Mosdns-x Group](https://t.me/mosdns)
- [easymosdns](https://github.com/pmkol/easymosdns)：适用于 Linux 的辅助脚本，几分钟即可搭建支持 ECS 的无污染 DNS 服务器，内置中国大陆优化规则。
- [mosdns v4](https://github.com/IrineSistiana/mosdns/tree/v4)：插件化 DNS 转发器，Mosdns-x 的上游项目。
