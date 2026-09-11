# 验证与排障

用于选取源码测试和确认配置行为。完整示例见 [配置编排](configuration.md)，插件模板见 [源码开发](source-development.md)。

## 构建与测试选择

先查看 `go.mod` 和 `go version`。分析基线声明 Go 1.26.3，应使用满足当前模块要求的工具链；不为适应本机旧版本而随意降低模块要求。`.github/workflows/test.yml` 在基线只执行 `go build`，不能据此认为单元测试已覆盖。

在仓库根执行与改动相符的检查：

| 改动 | 检查 |
|---|---|
| 新增插件或注册变更 | 新插件包测试、`go test -mod=readonly ./plugin`、编译主程序并用配置实例化插件 |
| 执行链、条件、并发调度 | `go test -mod=readonly ./pkg/executable_seq`；并发改动对受影响包增加 `-race` |
| 域名／IP／报文匹配 | `go test -mod=readonly ./pkg/matcher/...` |
| 上游连接或协议 | `go test -mod=readonly ./pkg/upstream/... ./pkg/server/...`，补所改协议的回环集成用例 |
| 缓存策略／后端 | `go test -mod=readonly ./pkg/cache/... ./plugin/executable/cache`；补缓存命中、TTL、键及后台更新行为用例 |
| DNS 消息、EDNS、ECS | `go test -mod=readonly ./pkg/dnsutils ./plugin/executable/ecs` 及调用者测试 |
| 共享接口或跨模块行为 | 检查各调用者，并运行 `go test -mod=readonly ./...` 和主程序构建 |

命令中出现 `[no test files]` 只表明包可加载／编译，不证明该功能行为。上游已有测试主要覆盖 UDP/TCP/TLS；新增 DoH、DoH3、DoQ 行为需要对应实测。`pkg/cache/redis_cache` 的现有测试只验证序列化，不依赖 Redis，也不能代替 Redis 连接和故障测试。

`pkg/nftset_utils` 的集成测试通过 `TEST_NFTSET` 控制，会创建内核表／集合。只在任务授权的隔离 Linux 环境启用；跨平台编译不等于此类运行功能通过。常规开发检查不设置该变量。

如果缓存目录不可写，为当前命令设置独立的临时 `GOCACHE` 和 `GOMODCACHE`；这可能重新下载依赖。保持 `-mod=readonly`，区分依赖获取失败、编译错误与测试断言失败。不要为通过验证修改无关依赖。

## 插件示例验收

在临时源码副本中放入 cap_ttl 示例并添加空白导入，避免把演示功能当成正式产品需求加入工作树。至少验证：

- `coremain.NewPlugin` 能从 YAML 等价参数 map 构造插件；缺失、负数或超出 uint32 范围的 `max_ttl` 返回预期错误，未知参数被拒绝。
- 后续节点产生 TTL=3600 的响应时，cap_ttl 将其限制为 60；TTL=30 保持 30，OPT 不按普通 RR TTL 处理。
- `next == nil` 且无响应时插件正常返回；入口如何处理无响应要单独测试。
- 后续节点的错误被原样传播；短路节点后的节点不执行，而已进入的 cap_ttl 后处理仍执行。
- 嵌套 sequence 中 `_return` 停止内部剩余节点，外层后续节点仍可执行。
- 编译包含新导入的完整二进制，启动示例 YAML，查询实际 DNS 响应，以发现只测包本身无法发现的启用遗漏。

## 本地配置验证

在独立临时目录保存配置文件和测试二进制；若下列回环端口已被占用，为整组示例一致地改用空闲高端口。不要终止占用端口的未知进程。

在仓库根创建测试目录并构建：

```sh
mosdns_test_dir=$(mktemp -d /tmp/mosdns-x-check.XXXXXX)
go build -mod=readonly -o "$mosdns_test_dir/mosdns" .
```

把以下完整配置保存为测试目录内的 `upstreams.yaml`。它用 Mosdns-x 自身生成固定应答，并同时提供快、慢两类上游，无需公网 DNS 或附加服务。

```yaml
log:
  level: info
plugins:
  - tag: answer_a
    type: blackhole
    args:
      ipv4: [192.0.2.53]
  - tag: answer_b
    type: blackhole
    args:
      ipv4: [192.0.2.54]
  - tag: delay
    type: sleep
    args:
      duration: 500
  - tag: slow
    type: sequence
    args:
      exec:
        - delay
        - answer_a
servers:
  - exec: answer_a
    listeners:
      - protocol: udp
        addr: 127.0.0.1:15453
      - protocol: tcp
        addr: 127.0.0.1:15453
  - exec: answer_b
    listeners:
      - protocol: udp
        addr: 127.0.0.1:15454
  - exec: slow
    listeners:
      - protocol: udp
        addr: 127.0.0.1:15455
```

先在一个终端前台运行测试上游：

```sh
"$mosdns_test_dir/mosdns" start -d "$mosdns_test_dir" -c upstreams.yaml
```

在另一终端把 `mosdns_test_dir` 设为同一个临时目录，再前台启动需要验证的示例。以下以 simple.yaml 为例；routing.yaml 还需要同目录下的 blocked.txt。

```sh
"$mosdns_test_dir/mosdns" start -d "$mosdns_test_dir" -c simple.yaml
```

按各示例的输入检查 RCODE、Answer、TTL 和缓存指标。验证后停止自己启动的进程；自动化测试通过记录进程对象／PID 并在退出时回收，不按进程名批量杀进程。

验证额外失败场景时，复制示例再修改：

| 场景 | 预期与证据 |
|---|---|
| sequence 引用不存在的 tag | 启动失败，日志含缺失的 executable 名称；不是监听成功后才查询失败 |
| 引用插件放在 sequence 后面 | 初始化失败，体现配置加载顺序 |
| 未知顶层／插件参数 | 配置解码失败，定位具体字段 |
| 入口只执行 `_return` | 查询得到 SERVFAIL，而不是 REFUSED 或网络超时 |
| 数据源原地改写 | 收到文件重载日志后，新规则影响下一次匹配；按事件等待，不仅固定睡眠 |
| 快速 fallback 主分支先完成／主分支慢／两支失败 | 分别采用主应答、备用应答、返回错误并最终得到 SERVFAIL |
| 缓存重复查询 | Answer 一致、TTL 合理递减且 hit 指标增加；检查客户端是否附加 EDNS |

## 排障顺序

从实际报错或返回值定位：启动问题先检查配置路径、解码、注册与引用，再检查数据加载和监听；请求问题沿入口、条件分支、缓存、上游及返回后处理逐段确认。用唯一测试域名和 query 日志关联同一次查询。

性能问题先明确是查询耗时、命中率、连接数还是内存增长；使用现有插件指标、日志或已启用的 pprof 收集证据，再选择具体模块。资源泄漏问题追踪创建方、后台任务和实际关闭调用者，不能从 `Close` 方法存在就推断生命周期正常。

记录工具链、验证命令、关键结果和未覆盖环境。相关检查通过后，仅在新增改动、失败或尚未解决的疑点需要时扩大或重复验证。
