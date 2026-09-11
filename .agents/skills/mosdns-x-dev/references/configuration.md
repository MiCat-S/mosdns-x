# 配置编排

用于新增或排查 Mosdns-x 配置。先检查目标二进制对应的源码／版本；下文使用当前项目的 `sequence.args.exec` 结构。

## 组织配置与规则

| 配置项 | 使用规则 |
|---|---|
| `log` | 日志级别和可选文件；调试时优先缩小到单个请求观察 |
| `include` | 加载子文件；递归合并的数据源、插件、服务器在当前条目之前初始化；最大递归深度由 `mergeInclude` 控制 |
| `data_providers` | `tag`、`file`、`auto_reload`；以 `provider:<tag>` 供支持的匹配器引用 |
| `plugins` | `tag` 标识实例、`type` 查注册工厂、`args` 对应插件参数；依赖实例先定义 |
| `servers` | `exec` 引用可执行插件，`timeout` 单位为秒，`listeners` 决定协议和地址 |
| `api.http` | 可选管理接口地址，调试示例只绑定回环地址 |

`-d` 改变整个进程工作目录；配置、include、数据文件等相对路径按该工作目录解析，不自动相对各个 YAML 文件。分拆配置时将被依赖插件放进先加载的文件，保留根配置的日志和 API 设置。

插件参数和顶层配置都启用未使用字段检查；`config gen` 生成模板，`config conv` 转换格式，它们不验证插件图的完整运行行为。实际启动才能发现未注册类型、未定义引用、数据加载和监听失败。

域名规则常用 `full:example.test`（精确域名）、`domain:example.test`（域名及子域名）、`keyword:`、`regexp:`。用匹配器实现确认规范化与具体语法。预设匹配器在 `if` 表达式里使用方括号，如 `"[_qtype_AAAA]"`；普通 tag 可直接参与 `&&`、`||`、`!` 和括号运算。

## 执行顺序和策略

- 生成响应不总是意味着停止。`blackhole`、`fast_forward` 和现有 `ttl` 都会继续调用后续节点；需要停止当前链时显式接 `_return`。`arbitrary` 命中则会自行短路。
- 现有 `ttl` 在调用后续节点**之前**修改已有响应，因此通常放在转发之后。要对缓存命中也处理 TTL，可配置已定义的 `when_hit` 插件，或在外层使用有后处理语义的插件，不能直接把现有 `ttl` 放在最前面期待回程生效。
- 缓存未命中时包住后续调用并保存其结果，命中则跳过后续节点。会影响现有缓存回答的阻断策略，通常放在缓存之前；需要动态策略时验证变更后的实际返回。
- ECS 插件改变到达缓存时的请求形态。默认缓存不接收带 Extra 的请求；把缓存放在 ECS 前可能让不同客户端共享同一答案。按业务是否需要区分客户端／ECS 决定顺序和 `cache_everything`，不要机械套固定位置。
- `parallel` 的分支状态隔离，最终只写回选中的响应。分支完成条件和 `fast_forward` 的 trusted 策略见 [架构说明](architecture.md#并发和响应选择)。
- `load_balance` 每次选择一个分支，成功后继续外部后续链；不要将它当成健康检查或失败重试机制。
- fallback 的 `fast_fallback` 为毫秒，`stat_length`／`threshold` 表示常规统计窗口和触发阈值。快速 fallback 针对 Go 错误、无响应或延时；DNS RCODE 本身不自动等于分支失败。

以下是嵌入 `sequence.args.exec` 的两个独立片段，假设 `forward_a`、`forward_b` 已先定义。使用其中一个节点；将两者连续使用会执行两轮查询。

```yaml
- parallel:
    - [forward_a]
    - [forward_b]
```

```yaml
- load_balance:
    - [forward_a]
    - [forward_b]
```

## 完整示例的运行条件

先按 [本地验证环境](validation.md#本地配置验证) 启动 `upstreams.yaml`。它提供回环测试上游：15453 返回 `192.0.2.53`，15454 返回 `192.0.2.54`，15455 延迟 500ms 后返回 `192.0.2.53`。这些地址只用于观察策略，不需要访问公网。

将以下配置分别保存为 `simple.yaml`、`routing.yaml`、`fallback.yaml`，按验证文档的前台启动命令运行。`dig` 只是测试客户端，可用等价 DNS 客户端替代；不要把系统 DNS 改到测试实例。

### 基本转发：simple.yaml

```yaml
log:
  level: info
plugins:
  - tag: forward
    type: fast_forward
    args:
      upstream:
        - addr: udp://127.0.0.1:15453
servers:
  - exec: forward
    listeners:
      - protocol: udp
        addr: 127.0.0.1:15353
      - protocol: tcp
        addr: 127.0.0.1:15353
```

输入和验证：分别执行 `dig @127.0.0.1 -p 15353 ordinary.test A +noedns` 与同一命令加 `+tcp`。两者应为 NOERROR，A 地址为 `192.0.2.53`，测试上游的原始 TTL 为 3600。

### 域名分流与缓存：routing.yaml

在同一工作目录创建 `blocked.txt`，内容为：

```text
full:blocked.test
```

```yaml
log:
  level: debug
data_providers:
  - tag: blocked
    file: ./blocked.txt
    auto_reload: true
plugins:
  - tag: is_blocked
    type: query_matcher
    args:
      domain: ["provider:blocked"]
  - tag: is_internal
    type: query_matcher
    args:
      domain: ["domain:internal.test"]
  - tag: local_answer
    type: blackhole
    args:
      ipv4: [192.0.2.10]
  - tag: cache
    type: cache
    args:
      size: 1024
  - tag: forward
    type: fast_forward
    args:
      upstream:
        - addr: udp://127.0.0.1:15453
  - tag: ttl_limit
    type: ttl
    args:
      maximum_ttl: 60
  - tag: main
    type: sequence
    args:
      exec:
        - if: is_blocked
          exec:
            - _new_nxdomain_response
            - _return
        - if: is_internal
          exec:
            - local_answer
            - _return
        - cache
        - forward
        - ttl_limit
servers:
  - exec: main
    listeners:
      - protocol: udp
        addr: 127.0.0.1:15354
api:
  http: 127.0.0.1:19091
```

输入和预期：

| 查询／操作 | 预期 |
|---|---|
| `dig @127.0.0.1 -p 15354 blocked.test A +noedns` | NXDOMAIN，停止当前入口链 |
| `dig @127.0.0.1 -p 15354 host.internal.test A +noedns` | 本地应答 `192.0.2.10` |
| `dig @127.0.0.1 -p 15354 ordinary.test A +noedns` | 转发得到 `192.0.2.53`，TTL 不超过 60 |
| 立即重复 ordinary.test 查询 | 缓存命中；日志出现 `cache hit`，`mosdns_plugin_cache_hit_total` 增加 |
| 原地改写 blocked.txt 为 `full:changed.test`，等待重载日志 | changed.test 返回 NXDOMAIN；blocked.test 走默认转发 |

查看缓存指标可请求 `http://127.0.0.1:19091/metrics`。用 `+noedns` 验证默认缓存，避免客户端默认 EDNS 使请求跳过缓存。缓存保存的是 TTL 已被后续 `ttl_limit` 调整过的响应。

### 快速 fallback：fallback.yaml

```yaml
log:
  level: debug
plugins:
  - tag: primary
    type: fast_forward
    args:
      upstream:
        - addr: udp://127.0.0.1:15455
  - tag: secondary
    type: fast_forward
    args:
      upstream:
        - addr: udp://127.0.0.1:15454
  - tag: main
    type: sequence
    args:
      exec:
        - primary: [primary]
          secondary: [secondary]
          fast_fallback: 50
          always_standby: false
servers:
  - exec: main
    timeout: 2
    listeners:
      - protocol: udp
        addr: 127.0.0.1:15355
```

输入：`dig @127.0.0.1 -p 15355 ordinary.test A +noedns`。

预期：primary 延迟 500ms，secondary 在约 50ms 的触发阈值后参与，返回 `192.0.2.54`。调度和机器负载会影响耗时，验收以应答来源和日志为主，避免断言恰好 50ms。将主上游改为快速的 15453 后，应返回 `192.0.2.53`；`always_standby: true` 则让备用分支提前执行、按门控条件采用结果。

## 从示例到实际配置

替换上游时保持可用的 bootstrap 路径，避免解析上游域名又依赖同一个尚未可用的 DNS 入口。使用 TLS 类协议时区分证书校验名称、`addr` 与 `dial_addr`；不要通过关闭证书验证掩盖配置错误。

DNS 监听、规则文件和缓存边界依赖部署环境。只有任务确实涉及监听暴露、客户端来源识别、Redis 或 ipset/nftset 时，才读取对应插件参数和平台实现并制定验证；示例本身不执行服务安装或生产切换。
