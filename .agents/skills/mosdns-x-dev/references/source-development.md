# 源码开发

先用 [架构地图](architecture.md) 找到改动层，再读取本节对应流程；配置用法见 [配置编排](configuration.md)，测试选择见 [验证与排障](validation.md)。

## 选择扩展点

| 需求 | 优先扩展点 | 已有参照 |
|---|---|---|
| 请求／响应处理或决定是否继续 | `coremain.ExecutablePlugin` | `sleep`、`ttl`、`blackhole`；复杂前后处理参考 `cache` |
| 条件判断 | `coremain.MatcherPlugin` | `query_matcher`、`response_matcher` 和 `pkg/matcher/msg_matcher` |
| 组合已有插件 | 配置中的 `sequence` | 先验证现有条件、parallel、fallback、load_balance 是否足够 |
| 改变执行图或调度 | `pkg/executable_seq` | 相应配置解析函数、链链接方法及现有测试 |
| 上游请求或协议 | `fast_forward` → `pkg/upstream` | 工厂、拨号器、传输层与相应协议实现 |
| 接受新的客户端协议／元数据 | `coremain/server.go` → `pkg/server` | DNS / HTTP Handler、监听器及 `RequestMeta` |
| 缓存策略或存储 | `plugin/executable/cache` / `pkg/cache` | 策略留在插件，后端遵守 `cache.Backend` 契约 |

## 添加可执行插件

先明确插件处理查询还是响应、是否短路、参数含义及失败行为。定义 `PluginType`、带 `yaml` 标签的 `Args` 和嵌入 `*coremain.BP` 的插件；在 `init()` 中注册类型工厂，最后在 `plugin/enabled_plugin.go` 添加空白导入。

以下完整示例放在新包 `plugin/executable/cap_ttl/cap_ttl.go`。它演示**后处理**：先执行后续节点，再限制返回响应的 TTL。`max_ttl` 沿用仓库弱类型参数解码，解码后的取值范围为 1～4294967295 秒；示例不持有外部资源。

```go
package capttl

import (
	"context"
	"fmt"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

const PluginType = "cap_ttl"

type Args struct {
	MaxTTL int64 `yaml:"max_ttl"`
}

type plugin struct {
	*coremain.BP
	maxTTL uint32
}

var _ coremain.ExecutablePlugin = (*plugin)(nil)

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() interface{} {
		return new(Args)
	})
}

func Init(bp *coremain.BP, args interface{}) (coremain.Plugin, error) {
	a := args.(*Args)
	if a.MaxTTL <= 0 || a.MaxTTL > int64(^uint32(0)) {
		return nil, fmt.Errorf("max_ttl must be between 1 and 4294967295")
	}
	return &plugin{BP: bp, maxTTL: uint32(a.MaxTTL)}, nil
}

func (p *plugin) Exec(ctx context.Context, qCtx *query_context.Context, next executable_seq.ExecutableChainNode) error {
	if err := executable_seq.ExecChainNode(ctx, qCtx, next); err != nil {
		return err
	}
	if r := qCtx.R(); r != nil {
		dnsutils.ApplyMaximumTTL(r, p.maxTTL)
	}
	return nil
}
```

在 `plugin/enabled_plugin.go` 的 import 组加入 `_ "github.com/pmkol/mosdns-x/plugin/executable/cap_ttl"`。重新编译后，用以下完整配置验证；请求 A 记录应返回 `192.0.2.10`，TTL 为 60。

```yaml
log:
  level: info
plugins:
  - tag: cap
    type: cap_ttl
    args:
      max_ttl: 60
  - tag: answer
    type: blackhole
    args:
      ipv4: [192.0.2.10]
  - tag: main
    type: sequence
    args:
      exec:
        - cap
        - answer
servers:
  - exec: main
    listeners:
      - protocol: udp
        addr: 127.0.0.1:15356
```

`Args` 由注册的 `NewArgs` 创建，框架解码后交给 `Init`；参数类型断言符合现有约定。参数默认值／范围检查需要在工厂里显式执行，框架不会自动调用一个任意命名的 `Args.Init()`。示例先解码为有符号数再校验和转换，因为当前弱类型解码可将负数转换为无符号大数，仅用 `uint32` 字段不能拒绝负数输入。

没有参数的预设插件可参考 `sleep` 的 `RegNewPersetPluginFunc`（名称按当前源码拼写），使用不冲突的 `_` 前缀 tag。普通插件的 `type` 是工厂名称，`tag` 是配置实例名称；两者用途不同。

## 匹配器与数据源

匹配器实现 `Match(ctx context.Context, qCtx *query_context.Context) (bool, error)`，嵌入 `BP`、注册工厂并导入启用的步骤相同，包放在 `plugin/matcher` 下。加入 `var _ coremain.MatcherPlugin = (*yourMatcher)(nil)` 编译断言。

只需要判断 qtype、域名、客户端 IP 或响应 IP 时，先使用 `query_matcher`／`response_matcher`。其多类匹配条件采用 AND 组合；查看具体匹配器如何处理空条件、空响应和格式规范化，不重新实现已有规则解析。

动态规则使用 `BP.M().GetDataManager()` 和现有 BatchLoadProvider 辅助函数。它们返回的 MatcherGroup 可能附带注销监听器的 closer；初始化部分成功后的失败路径、更新失败后的可见数据和实际关闭路径都要纳入该功能的测试。

## 执行链与上下文

- 正向处理后继续：返回 `ExecChainNode(ctx, qCtx, next)`。后处理：先调用它，按需求传播错误，再处理 `R()`。短路：产生所需响应后返回 `nil`，不调用 `next`。
- 没有响应时返回 `nil` 不代表“丢弃客户端数据包”；到达默认入口后会成为 SERVFAIL。`_drop_response` 仅清空上下文响应。
- 内部 `_return` 会让调用者返回，外层 `sequence` 和已经进入的插件后处理仍可能执行。改动嵌套链时测试内外两层，而不只测单一链。
- `ConditionNode` 和 `LBNode` 会把分支末尾链接到外部后续节点；parallel／fallback 的独立分支则将结果交给外层后续节点。修改链接逻辑时防止后续节点被遗漏或执行两次。
- 新建上下文使用 `NewContext`。`Q()` 非空但直接单元测试可能传入没有 Question 的消息；按照插件是否依赖正式入口的约束构建测试输入。
- 分支在启动 goroutine 前复制上下文；保持 `OriginalQuery()` 和 `ReqMeta()` 只读。`CopyTo` 不负责把任意旧目标清空，复用目标前需核对响应和标记的生命周期。
- 为后台工作设定可结束的生命周期，尊重前台请求的取消语义。现有 lazy cache 和快速 fallback 部分路径有意脱离父取消，仅继承／设置截止时间；修改前先确定是否需要让后台结果继续产生。

## 协议与缓存修改

上游新增选项需要核对 `fast_forward.UpstreamConfig` → `upstream.Opt` → 协议构造函数整个映射，以及秒／毫秒单位。`upstream.Upstream.ExchangeContext` 不得保留或修改传入请求；返回响应及 `Close()` 的所有权由实现和调用者共同落实。

修改协议分派时同时检查上游工厂和服务端监听分派：新增客户端能力不意味着自动获得服务端能力。沿现有 TCP/UDP/TLS/HTTP/QUIC 类型和适配层扩展；平台代码保留 `_linux.go`、`_other.go` 等文件边界。协议测试优先用回环监听器和临时证书，包含超时、截断、并发请求 ID 与关闭路径。

缓存键由到达缓存插件时的请求打包得到，并将 DNS ID 归零。默认只接受单 Question 且 Answer、Ns、Extra 为空的请求；`cache_everything` 才放宽这一限制。它不是单纯的域名／类型索引，调整 EDNS、ECS 或报文标志位置会改变命中行为。

缓存命中直接返回，可通过 `when_hit` 调用已定义插件。未命中先执行后续链，再考虑保存响应；lazy cache 使用独立上下文在后台刷新。基线只保存 NOERROR 且未截断的响应，TTL、压缩和后端序列化逻辑还需分别核对。涉及键或编码变更时，明确已有缓存能否继续读取及是否需要迁移，不静默更改格式。

## 接口交付

新增配置字段时同步提供真实 YAML 示例、单位／默认行为、错误条件及对应测试。保持日志和指标通过 `BP` 接入，HTTP 插件路径遵守实例 mux 的命名空间。参照 [维护者插件开发说明](https://github.com/pmkol/mosdns-x/wiki/新插件编写) 定位 `sleep` 示例，最终以当前源码为准。
