# Mosdns-x (multi-user DoH service edition)

[简体中文](README.md) | **English** | [繁體中文](README.zh-TW.md) | [日本語](README.ja.md)

[![Release](https://img.shields.io/github/v/release/MiCat-S/mosdns-x?include_prereleases&label=release)](https://github.com/MiCat-S/mosdns-x/releases)

This repository is a fork of [pmkol/mosdns-x](https://github.com/pmkol/mosdns-x). Mosdns-x is a high-performance DNS forwarder written in Go. You shape how it handles DNS with a plugin pipeline, and it speaks UDP, TCP, DoT, DoQ, DoH and DoH3.

This fork adds a **multi-user DoH / DoH3 service**: one Mosdns process serves DNS, a management API and a web panel. Admins create accounts and assign quotas. Users get a private encrypted-DNS address for each of their devices, and tune their own blocking rules, privacy options and query logging from their own panel.

> The web panel and the documentation under `docs/` are in Simplified Chinese.

## Features

**Accounts and metering**

- Admins create, edit, disable and delete accounts, and set an expiry date, a daily or monthly quota, QPS and burst limits, and a device limit. Deleting an account also erases all of that user's data.
- Each device gets its own UUID credential and connects with a private URL or `Authorization: Bearer`. Credentials can be rotated or revoked at any time.
- Usage is metered exactly at three levels (server, user and device). The quota is charged in the same transaction that admits the query.

**Self-service settings for users**

- Security and privacy: DNS rebinding protection, blocking by query type (including HTTPS and SVCB), ECS removal, and a one-click switch for the threat intelligence lists the admin provides.
- Custom rules: block, allow, and A / AAAA / CNAME rewrites, matched by exact name, suffix, keyword or regular expression.
- Public blocklists: the admin curates a catalog and each user turns lists on or off.
- Answer tuning: IPv4 or IPv6 preference, TTL floor and ceiling, CNAME flattening, shuffled answers, and ECS address override.
- A one-click safe mode, and a switch that pauses all of a user's own policies for a while.
- Users decide whether detailed query logs are kept, and for how long.
- DNS Lookup: test any name against the complete policy chain currently in effect.

**Administration and operations**

- Control and statistics data live in bbolt (no dependencies) or in MySQL 5.7 / 8.4.
- Statistics and query logs show whether each answer came from an upstream, the cache, a rule or a public list.
- Audit log, system health monitoring, and an authenticated Prometheus `/metrics`.
- Managed runtime config: validate, hot-reload and roll back upstreams, caching and statistics retention from the panel. A failed change leaves the running config untouched.
- Offline backup and restore, migration from bbolt to MySQL, and an offline `reset-password` for a locked-out admin.

## Quick start

Install or upgrade on a Linux host that runs systemd:

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | sudo bash
```

The script:

- Detects the CPU architecture, downloads this fork's release and checks its SHA-256.
- Installs `/usr/local/bin/mosdns` and a systemd service, then starts it.
- Leaves an existing `/etc/mosdns/config.yaml` alone.
- From a mainland China connection, downloads the package through `gh-proxy.com` by default, but always fetches the checksums directly from GitHub.

It does not install a reverse proxy, create domains or set an admin password. See [one-command install](docs/installation.md) (Chinese).

The default config is a plain DNS forwarder on `127.0.0.1:5533`, **without multi-user mode**. To turn the multi-user service on:

1. Add a `control` section to the config with the public DNS URL and the panel origin. See [multi-user configuration](docs/service-config.md).
2. Create the first admin with `mosdns control init-admin`. The password is read from standard input only. See [operations](docs/operations.md#本地初始化与启动).
3. Serve the panel and DoH over HTTPS through a reverse proxy on the same host. See [deployment: Ubuntu / Debian + Caddy](docs/deployment.md).
4. Sign in as the admin at `/admin` and create users. Each user signs in at `/app`, creates a device credential on the account page, and enters the resulting address in the system's or browser's encrypted DNS setting.

Check whether multi-user mode is ready:

```bash
mosdns control status --config /etc/mosdns/config.yaml
```

## Documentation

All documents are in Simplified Chinese.

| Topic | Documents |
|---|---|
| Install and upgrade | [One-command install](docs/installation.md), [deployment guide](docs/deployment.md) |
| Configuration | [Multi-user configuration](docs/service-config.md), [storage and migration](docs/storage.md) |
| Operations | [Initialization, backup and restore, managed config, password reset](docs/operations.md), [security and health monitoring](docs/monitoring.md) |
| Development | [Service architecture](docs/service-architecture.md), [API contract](docs/control-api.md), [building](docs/building.md), [releasing](docs/releasing.md) |
| Acceptance | [Development review and acceptance](docs/development-review.md), [performance](docs/performance.md) |

## Scope

- Deployed and tested as a single instance on a single host. Running several instances against MySQL, and failover between them, have not been validated for production.
- Releases are built locally by the maintainer and uploaded by hand, with no signature or provenance attestation. If your supply-chain requirements are stricter, build from source as described in [building](docs/building.md).
- The main config file remains the source of truth. The panel can only change the managed part, which holds no secrets.

## Upstream and community

- For the original Mosdns-x features, plugin configuration and tutorials, see the [upstream wiki](https://github.com/pmkol/mosdns-x/wiki). The original prebuilt binaries are in the [upstream releases](https://github.com/pmkol/mosdns-x/releases). Packages for this fork are in [this repository's releases](https://github.com/MiCat-S/mosdns-x/releases).
- Telegram community (upstream): [Mosdns-x Group](https://t.me/mosdns)
- [easymosdns](https://github.com/pmkol/easymosdns): helper scripts for Linux that set up an ECS-capable, unpolluted DNS server in minutes, with built-in rules tuned for mainland China.
- [mosdns v4](https://github.com/IrineSistiana/mosdns/tree/v4): a plugin-based DNS forwarder, and the project Mosdns-x is built on.
