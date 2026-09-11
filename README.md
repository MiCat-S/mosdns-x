## Mosdns-x

Mosdns-x 是一个用 Go 编写的高性能 DNS 转发器，支持运行插件流水线，用户可以按需定制 DNS 处理逻辑。

**支持监听与请求以下类型的 DNS：**

* UDP
* TCP
* DNS over TLS - DoT
* DNS over QUIC - DoQ
* DNS over HTTP/2 - DoH
* DNS over HTTP/3 - DoH3

功能概述、配置方式、教程，详见：[wiki](https://github.com/pmkol/mosdns-x/wiki)

下载预编译文件、更新日志，详见：[release](https://github.com/pmkol/mosdns-x/releases)

### 本 Fork 的多用户服务扩展

提供管理端与用户端网页、DoH / DoH3 设备专属 URL 和 Bearer 凭证、每日／每月额度、到期与用户 QPS 限制，以及按用户和设备记录的用量。管理 API、统计和网页资源集成在一个 Mosdns 进程中。

上述上游发布链接对应原版。构建本 Fork 的网页版本及初始化账户，请阅读：

- [部署教程：Ubuntu / Debian + Caddy](docs/deployment.md)
- [构建说明](docs/building.md)
- [配置与本地示例](docs/service-config.md)
- [初始化、运行与备份恢复](docs/operations.md)
- [服务架构](docs/service-architecture.md)与 [API 契约](docs/control-api.md)
- [开发审核与验收](docs/development-review.md)、[性能验证](docs/performance.md)

首版适用于单机单实例。管理面板和用户面板通过 `/admin`、`/app` 访问；DNS 配置通过文件维护，面板提供安全的配置概览。

#### 电报社区：

**[Mosdns-x Group](https://t.me/mosdns)**

#### 关联项目：

**[easymosdns](https://github.com/pmkol/easymosdns)**

适用于 Linux 的辅助脚本。借助 Mosdns-x，仅需几分钟即可搭建一台支持 ECS 的无污染 DNS 服务器。内置中国大陆地区的优化规则，满足DNS日常使用场景，开箱即用。

**[mosdns-v4](https://github.com/IrineSistiana/mosdns/tree/v4)**

一个插件化的 DNS 转发器。是 Mosdns-x 的上游项目。
