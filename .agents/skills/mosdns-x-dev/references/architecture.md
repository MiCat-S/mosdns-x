# 架构与调用链

适用于需要定位模块或理解跨层行为的任务。基线提交见 [SKILL.md](../SKILL.md)，所有源码路径相对仓库根。

## 架构结论

Mosdns-x 是 Go 单进程 DNS 转发器，组合静态插件注册、中间件式执行链和独立协议实现。它将已有插件在初始化时组装成执行逻辑；新增 Go 插件需要重新编译。

```mermaid
flowchart LR
    A[协议监听] --> B[EntryHandler]
    B --> C[查询上下文]
    C --> D[sequence 与功能插件]
    D --> E[fast_forward]
    E --> F[bundled_upstream]
    F --> G[upstream 协议与连接]
    G --> H[响应沿调用栈返回]
    I[DataProvider] --> J[域名与 IP 匹配器]
    J --> D
```

插件也可以直接产生响应，因此请求不一定经过上游。缓存、域名分流和响应修改在执行链内完成。

## 模块地图

| 模块 | 职责与定位符号 |
|---|---|
| `main.go`、`coremain/run.go` | CLI 入口、插件包导入；`StartServer`、`loadConfig`、`mergeInclude` |
| `coremain/config.go`、`coremain/mosdns.go` | 顶层配置、实例初始化、插件索引、API 与服务生命周期；`Config`、`RunMosdns` |
| `coremain/interface.go`、`coremain/register.go` | `Plugin`、`ExecutablePlugin`、`MatcherPlugin`、`BP` 和工厂注册 |
| `plugin/enabled_plugin.go` | 通过空白导入启用内置插件包，使其 `init()` 被执行 |
| `plugin/executable`、`plugin/matcher` | 把具体功能接入插件接口，并解释插件参数 |
| `pkg/executable_seq` | `BuildExecutableLogicTree`、`ExecChainNode`、条件、并发、fallback 和轮询分支 |
| `pkg/query_context` | 单个请求的可变查询、响应、标记及只读元数据 |
| `coremain/server.go`、`pkg/server` | 根据监听配置构建 UDP/TCP/DoT/DoH/DoQ/DoH3 服务；DNS 与 HTTP 处理入口 |
| `pkg/bundled_upstream`、`pkg/upstream` | 多上游响应选择；协议工厂、bootstrap、拨号器、连接复用与传输 |
| `pkg/data_provider`、`pkg/matcher` | 文件加载与变更通知；域名、IP 和报文特征匹配 |
| `pkg/cache`、`pkg/dnsutils`、`pkg/pool` | 缓存后端、DNS 消息辅助操作和缓冲区／定时器复用 |
| `mlog`、`tools`、`release.py` | 日志、配置生成等 CLI 工具、跨平台构建打包 |

## 初始化路径

1. `main.go` 导入插件和工具包，执行各包注册函数，然后进入 `coremain.Run()`。
2. `start` 调用 `StartServer`；`-d` 先改变工作目录，再读取配置。`loadConfig` 使用 Viper，按 `yaml` 标签解码并拒绝未使用字段。
3. `mergeInclude` 递归合并子配置的 `data_providers`、`plugins`、`servers`，放在当前配置条目之前；子配置的 `log` 和 `api` 不作为覆盖项合并。
4. `RunMosdns` 创建日志、数据管理器、插件索引、HTTP mux 和指标注册器；先加载数据源，再加载预设插件，最后按配置数组顺序初始化普通插件。
5. `NewPlugin` 查类型注册表、创建 `BP`、解码参数并调用工厂。可执行插件和匹配器分别进入 `execs`／`matchers` 索引；一个插件可以实现两类接口。
6. `sequence` 工厂使用当时已存在的索引构建执行链。因此不存在自动依赖排序，也不能引用稍后才定义的插件。
7. `startServers` 将 `servers[].exec` 绑定到入口插件，再启动监听器及可选管理 API。

`Plugin` 要求 `Tag()`、`Type()`、`Close()`；嵌入 `*coremain.BP` 可获得这些方法及日志、Mosdns 实例访问能力。`BP.Close()` 本身为空操作。

## 请求及应答路径

`pkg/server/dns_handler/entry_handler.go` 的 `EntryHandler.ServeDNS` 设置请求截止时间、检查 Question，创建上下文，然后调用入口 `Exec(ctx, qCtx, nil)`。默认请求超时为 5 秒，已有更早截止时间时保留它。

| 执行结果 | 当前入口行为 |
|---|---|
| 没有 Question 或域名格式非法 | 返回 FORMERR |
| 入口返回错误 | 返回 SERVFAIL，即使上下文中已存在响应 |
| 入口无错误但响应为空 | 返回 SERVFAIL |
| 存在响应且无错误 | 恢复客户端请求 ID，并按服务选项设置 RA 后返回 |

文件注释仍有“无响应返回 REFUSED”的描述，基线实现实际为 SERVFAIL。不要仅引用注释得出行为结论。

`sequence.Exec` 先执行内部链，成功返回后再调用外部 `next`。节点可以先处理查询、调用后续节点、再处理响应。不存在独立的“逆向调度器”；所谓反向处理就是 Go 调用栈返回后继续执行插件代码。

## 并发和响应选择

| 位置 | 状态隔离与选择规则 |
|---|---|
| `ParallelNode` / 快速 fallback | 分支复制查询上下文；`asyncWait` 选择首个没有 Go 错误且响应非空的结果，没有额外的 RCODE=0 筛选；只把选中的响应写回外层 |
| `fast_forward` 多上游 | `bundled_upstream.ExchangeParallel` 接受 trusted 上游的任意 RCODE，或非 trusted 上游的 NOERROR；第一个配置的上游被强制视为 trusted |
| `fast_forward` 单上游 | 直接执行 `Exchange`，不走多上游的 RCODE 筛选循环 |

因此，收到 NXDOMAIN、SERVFAIL DNS 报文，与 Go 函数返回错误不是同一件事。设计 fallback 前，先确定业务希望对哪种情况切换。

普通 fallback 统计主分支近期错误／空响应，在后续请求选择进入 fallback 模式；它不是每次主分支失败就立即重试 secondary。快速 fallback 才根据本次失败或 `fast_fallback` 延时启动／采用备用分支。

## 共享设施及边界

- 数据源加载原始文件，通过 `DataListener.Update` 通知动态匹配器。域名和 IP 匹配器负责解析和切换数据；文件热更新不等于完整配置或插件图热重载。
- `qCtx.Copy()` 复制查询、已有响应及标记，但共享原始请求和请求元数据。并发插件自身的共享字段也需要单独分析同步方式。
- 管理 API 使用实例 mux，提供 `/metrics`、`/debug/pprof/` 等入口。普通插件实现 `http.Handler` 时，初始化流程注册 `/plugins/<tag>/`；插件可以通过 `BP` 获取实例 mux 和带前缀的指标注册器。
- 基线 `RunMosdns` / `addPlugin` 没有统一遍历插件并调用 `Close()` 的流程；部分插件只有 `Shutdown()`。新增持有连接、监听器或后台任务的能力时，追踪实际关闭路径和初始化失败的回收，按任务范围补齐必要接入。
- DoH／DoT 部分路径使用 `gitlab.com/go-extension/http` 和 `gitlab.com/go-extension/tls`，QUIC 路径还使用标准 TLS。修改协议时保留类型适配、证书及平台差异，不把这些导入直接替换成标准库。

## 外部资料

维护者的 [配置说明](https://github.com/pmkol/mosdns-x/wiki/Mosdns%E2%80%90x-%E9%85%8D%E7%BD%AE%E8%AF%B4%E6%98%8E)、[插件说明](https://github.com/pmkol/mosdns-x/wiki/插件及其参数) 和 [插件开发入口](https://github.com/pmkol/mosdns-x/wiki/新插件编写) 可补充使用背景。配置说明页面标注更新于 2025-10-09，插件说明为 2026-04-19，插件开发入口为 2025-08-10；均可能滞后于本地实现。
