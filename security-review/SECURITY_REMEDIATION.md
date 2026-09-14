# 安全复核与修复记录

日期：2026-09-14。审查基线：`bc91785`。

本记录按 Claude 初审文档逐项对照实际调用链、事务语义和测试结果。初审报告及其代码示例保留供追溯，不代表全部问题已经证实，也不是可直接应用的补丁。当前处理状态以本文件和仓库源码为准。

## 逐项处理

| 编号 | 复核结论 | 本次处理与证据 |
| --- | --- | --- |
| #1 凭证计数竞态 | 原报告的并发竞态判断不成立。读取、清理、上限检查及计数更新已在同一 bbolt 写事务内。 | 保留事务和下溢时回滚，新增 `TestConcurrentCredentialLimitAndRevocation`：32 个并发创建请求受限于 3 个凭证，每个凭证重复并发撤销 8 次后计数为零；`TestCredentialCleanupUnderflowRollsBack` 验证损坏状态不会提交部分删除。 |
| #2 认证错误处理 | 确认存在。登录已提交后读取用户失败，原流程会先发出有效 Cookie，随后发出删除 Cookie；撤销失败被忽略。 | Cookie 移到成功读取用户之后；清理使用脱离请求取消、保留请求值的 5 秒超时上下文；通过实例 logger 记录撤销失败，不记录密码、完整会话凭证、CSRF Token 或请求 URI。 |
| #3 密码时序攻击 | 未证实可利用的时序差异。已有 Argon2id 虚拟验证和 `subtle.ConstantTimeCompare`，不能仅凭存在分支认定严重漏洞。 | 保留现有哈希与登录事务；补充不存在用户、错误密码、停用账户、取消及存储故障的回归测试。不吞掉存储错误，不添加固定或随机休眠。没有声称整个网络登录流程具备严格恒定时延。 |
| #4 IP 限速器内存 | “无界增长”不成立，已有硬容量限制；可改善容量未满时的过期回收。 | 增加由请求触发的周期清理，间隔为限速窗口与 1 分钟中的较小值。保持容量限制和仍有效的请求计数，不增加后台 goroutine。无请求时不会主动清理，但存储仍有界。 |
| #5 MySQL 事务回滚 | 采用防御性修复。原代码已经有操作 context 的 `defer cancel()`，不能据此认定 panic 必然永久泄漏。 | `BeginTx` 成功后立即 `defer tx.Rollback()`，明确覆盖异常退出；保留原有业务错误及提交错误处理。测试覆盖成功提交、业务错误、panic、提交失败，确认连接已归还。 |
| #6 初始化 context | 确认 MySQL 初始化未继承调用方取消；原初始化仍有超时，并非无限等待。 | 控制和统计存储均新增兼容入口 `OpenMySQLContext`，运行时及 CLI 传入已有 context；原 `OpenMySQL` 保留。用受控拨号器验证初始化期间取消能中止连接。迁移锁释放仍使用独立、有限时长的清理 context。 |
| #7 DNS 输入验证 | 原有域名长度、标签、字符及 DNS 格式检查均发生在实际查询前。 | 保留合法域名和支持类型，不按示例封禁 `.test`、`.example`、反向域名等合法查询。补充畸形名称不进入查询执行器的测试；另外修复执行器返回 `(nil, nil)` 时可能解引用空响应的问题，返回 HTTP 503。 |
| #8 开发模式 | 没有发现“开发模式免鉴权”或硬编码开发密码；已有 loopback 检查。 | 通过应用日志增加开发模式警告，明确 HTTP 限于 loopback、鉴权仍有效、不可通过公网代理暴露。不增加第二套开发密码或破坏现有开发配置。 |
| #9 转发头 | 原报告忽略了已有可信代理检查和从右向左的链路解析。 | 保留实现；新增未信任直连、无可信代理、伪造左侧地址、多级代理、畸形链及自定义头测试。没有引入 `MustParseAddrPort`，也不无条件采用最左侧地址。 |
| #10 限速数值边界 | 正常参数下的浮点溢出结论缺乏依据；有符号纳秒减法及损坏持久化状态值得加固。 | 两个后端共享 `consumeRateToken`：使用饱和时间差，限制令牌容量，拒绝 NaN、无穷大、负令牌及越界参数。时钟回退不重置补充时间，避免重复补额；无效状态返回不可用，不重置为满桶。测试确认拒绝请求不扣配额。 |
| #11 Unix Socket 权限 | 确认需要修复。原代码实际是 `0x777`，不是报告所写的八进制 `0777`，且忽略权限设置错误。 | 路径型 Unix Socket 在监听成功后设为 `0600`，设置失败则关闭句柄并报错。替换旧路径前检查文件类型，拒绝覆盖普通文件、目录或符号链接。抽象命名空间 Socket 不执行文件权限操作。 |
| #12 插件注册 panic | 静态链接插件在启动期 `init()` 中的重复注册属于程序配置错误，不是客户端请求触发的入口。 | 保留加锁和 fail-fast 契约，不改全部插件接口，不跳过冲突后继续启动。 |
| #13 资源清理模式 | 初审仅列出泛化建议，未给出独立复现。 | 保留既有 `shutdown` 生命周期和资源所有权；本次在会话、MySQL 事务及 Socket 失败路径补齐具体清理，不进行无关的全仓重构。 |
| #14 服务状态轮询 | 初审没有给出具体缺陷；当前服务停止使用 channel 等待并有 15 秒上限。 | 保留当前行为，纳入既有生命周期测试。 |
| #15 CSRF 格式 | 当前已经要求 Origin 匹配、会话存在、长度受限，并对完整 Token 作恒定时间比较。 | 新增有效、缺失、错误、超长 Token、错误 Origin、缺失会话测试。不把字符串格式检查当作替代会话绑定的安全修复。 |

## 实现定位

- [管理 API 与会话清理](../internal/controlapi/handler.go)
- [控制存储及限速计算](../internal/control/store.go)
- [MySQL 控制存储](../internal/control/mysql_store.go)
- [MySQL 统计存储](../internal/telemetry/mysql.go)
- [运行时存储初始化](../coremain/control_runtime.go)
- [CLI 初始化与迁移](../coremain/control_commands.go)
- [Socket 监听与权限](../coremain/server.go)

回归测试集中在：

- [控制存储安全测试](../internal/control/security_test.go)
- [管理 API 安全测试](../internal/controlapi/security_test.go)
- [统计存储取消测试](../internal/telemetry/mysql_context_test.go)
- [Socket 权限测试](../coremain/socket_security_test.go)
- [运行时取消测试](../coremain/control_runtime_test.go)

## 验证

实施验收命令：

```sh
go test -mod=readonly -race -count=1 ./...
go test -mod=readonly -race -count=10 ./pkg/executable_seq -run '^Test_FallbackECS_fast_fallback$'
go test -mod=readonly -race -count=1 -p 1 ./...
go vet -mod=readonly ./...
go test -mod=readonly -tags ui ./web
go build -mod=readonly -o /tmp/mosdns-security-headless .
go build -mod=readonly -tags ui -o /tmp/mosdns-security-ui .
npm --prefix web test
```

执行结果：

- 全量竞态检测 `-race -count=1 -p 1 ./...` 通过，无数据竞态报告。`-p 1` 只让不同包顺序运行，包内并发用例保持不变。
- 首次并行全量运行时，既有 `Test_FallbackECS_fast_fallback/always_standby_p_failed` 用例耗时 81ms，超过 70ms 阈值。隔离连续复测 10 次通过，随后串行全量通过；未修改 fallback 实现或放宽阈值。该用例仍有受系统负载影响的计时抖动风险。
- `go vet -mod=readonly ./...`、内嵌 UI 测试和两种原生二进制构建均通过。
- 前端 5 个测试文件、39 项测试通过。未改动前端源码。

竞态检测只检查测试实际覆盖的运行路径，不能证明不存在所有逻辑竞态或时序侧信道。

本机未配置隔离的 MySQL 5.7 / 8.4 实例。本次使用 sqlmock 验证事务与失败回滚，使用 MySQL 驱动的受控拨号器验证取消；真实 MySQL 集成测试仍需 `MOSDNS_TEST_MYSQL_DSN` 指向独立测试数据库，不能指向生产数据。

## 兼容性与运维边界

- 不改变密码、Token、数据库 schema、DNS 配置格式或前端接口。
- Unix Socket 的 `0600` 是有意的安全行为变更：其他 UID 不再具有文件权限访问。使用不同系统账户的客户端需要部署方明确规划权限；建议将 Socket 放在服务账号拥有的私有目录，不能放在不可信用户可替换目录项的位置。抽象 Socket 没有文件权限保护。
- 清理超时通过 context 传递。MySQL 可以取消上下文支持的操作；bbolt 已经进入的写事务不会被 context 强制抢占。不增加不能可靠停止的清理 goroutine，也不承诺磁盘故障时绝对 5 秒内完成。
- 撤销落盘失败会返回原始登录失败并记录错误，不会向客户端发出新凭证；此类孤立会话仍由到期和既有维护流程回收。没有无限重试队列。
- 本轮仅修复和验证源码，未发布版本、未操作生产数据库、未部署或重启生产服务。
