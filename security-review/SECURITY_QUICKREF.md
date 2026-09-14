# 🔒 mosdns-x 安全审查快速参考

> 这是安全审查的快速参考卡。完整细节请查看其他文档。

## 📊 概览

- **审查日期**: 2026-09-14
- **代码库**: mosdns-x DNS 服务器
- **文件数**: 235个 Go 源文件
- **发现问题**: 15个
- **风险等级**: 中高

## 🚨 严重问题（立即修复）

### #1: 凭证计数竞态条件
```
文件: internal/control/store.go:976-996
问题: cleanupActiveCredentials 修改计数时没有同步
风险: 用户可超过凭证限制，计数可能下溢
修复: 在同一事务内完成所有计数修改
时间: 2-3天
```

### #2: 认证错误处理不完整
```
文件: internal/controlapi/handler.go:397-401
问题: RevokeSession 错误被忽略
风险: 会话泄漏，资源耗尽
修复: 记录错误并使用正确的 context
时间: 1天
```

### #3: 密码验证时序攻击
```
文件: internal/control/store.go:550-562
问题: 非常量时间检查可泄露用户信息
风险: 用户名枚举，账户状态泄露
修复: 所有路径使用常量时间
时间: 2天
```

## 🔴 高危问题（第1个月）

### #4: IP 限速器内存增长
```
文件: internal/controlapi/handler.go:1965-1993
问题: 仅在达到容量时清理
风险: 内存耗尽 DoS
修复: 实现定期清理
```

### #5: 事务回滚缺失
```
文件: internal/control/mysql_store.go:435-447
问题: withTx 不使用 defer 回滚
风险: panic 时连接泄漏
修复: defer tx.Rollback()
```

### #6: Context 误用
```
文件: internal/control/mysql_store.go:268, 295
问题: 使用 context.Background() 忽略取消
风险: 无法优雅关闭
修复: 接受并使用父 context
```

## 🟡 中危问题

- **#7**: DNS 查询输入验证不足 (handler.go:1517)
- **#8**: 开发模式缺少警告 (control_runtime.go:71)
- **#9**: X-Forwarded-For 未验证 (handler.go:1899)
- **#10**: 速率限制整数溢出 (store.go:1175)

## 🟢 低危/代码质量

- **#11**: Unix socket 权限过宽 0777 → 0600
- **#12**: 插件注册使用 panic

## ✅ 做得好的地方

- ✓ 使用 crypto/rand 生成随机数
- ✓ Argon2id 密码哈希
- ✓ SQL 参数化查询（无 SQL 注入）
- ✓ 常量时间秘密比较
- ✓ CSRF 保护
- ✓ 令牌桶限速

## 📋 修复优先级

### 本周必须完成
1. 凭证计数竞态 (#1) - 最严重
2. 时序攻击防护 (#3) - 信息泄露
3. 事务回滚 (#5) - 资源泄漏

### 本月必须完成
4. 认证错误处理 (#2)
5. Context 修复 (#6)
6. IP 限速器 (#4)

### 本季度完成
7-12. 其他中低危问题

## 🔧 快速修复检查

### 修复 #1 (竞态)
```go
// ❌ 错误: 在循环中直接修改
for k, id := c.Seek(prefix); ... {
    u.CredentialCount--  // 竞态!
}

// ✅ 正确: 先收集再批量处理
var toDelete [][]byte
for k, id := c.Seek(prefix); ... {
    toDelete = append(toDelete, k)
}
for _, k := range toDelete {
    delete(k)
}
u.CredentialCount -= uint32(len(toDelete))
```

### 修复 #5 (回滚)
```go
// ❌ 错误
tx, _ := db.BeginTx(...)
if err := fn(tx); err != nil {
    tx.Rollback()  // panic 时不会执行
    return err
}

// ✅ 正确
tx, _ := db.BeginTx(...)
defer tx.Rollback()  // 总是执行
if err := fn(tx); err != nil {
    return err
}
return tx.Commit()
```

### 修复 #11 (权限)
```go
// ❌ 错误
os.Chmod(socketPath, 0x777)  // 全局可写!

// ✅ 正确
os.Chmod(socketPath, 0600)  // 仅所有者
```

## 🧪 测试命令

```bash
# 竞态检测（必须运行！）
go test -race ./...

# 覆盖率
go test -cover ./...

# 详细输出
go test -v -race ./internal/control/...

# 基准测试
go test -bench=. -benchmem ./...
```

## 📈 成功指标

修复后应达到：
- ✅ `go test -race` 无错误
- ✅ 凭证计数不一致事件 = 0
- ✅ 会话泄漏 = 0
- ✅ 内存稳定（无增长）
- ✅ 认证时序一致（±10ms）

## 📞 帮助资源

| 文档 | 用途 |
|------|------|
| `SECURITY_SUMMARY.md` | 完整总结和计划 |
| `SECURITY_REVIEW_CN.md` | 详细审查报告 |
| `SECURITY_FIXES.md` | 具体修复代码 |

## ⚡ 紧急修复顺序

如果只能修复3个问题，按此顺序：

1. **#1 凭证竞态** - 影响账户安全
2. **#5 事务回滚** - 可能导致服务崩溃
3. **#3 时序攻击** - 信息泄露

## 🎯 下一步行动

1. [ ] 阅读完整审查报告 (`SECURITY_REVIEW_CN.md`)
2. [ ] 组建修复团队（2-3名开发）
3. [ ] 创建修复分支
4. [ ] 从 #1 开始修复
5. [ ] 每个修复后运行 `go test -race`
6. [ ] 更新测试覆盖
7. [ ] 代码审查
8. [ ] 部署到测试环境
9. [ ] 负载测试
10. [ ] 部署到生产

## ⏱️ 时间估算

- **严重问题**: 10-12天（开发+测试）
- **高危问题**: 7天
- **中低危**: 5-6天
- **总计**: 约4-5周全职工作

## 📊 风险矩阵

```
影响
高 │ #1 #3    │ #2 #4 #5 #6
   │          │
中 │          │ #7 #8 #9 #10
   │          │
低 │          │ #11 #12
   └──────────┴──────────
     低         高
         可能性
```

---

**重要**: 这只是快速参考。实施修复前请阅读完整文档！

**最后更新**: 2026-09-14
