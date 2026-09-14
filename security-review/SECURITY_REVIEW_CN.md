# mosdns-x 代码安全审查报告

> 本文为历史初审报告。请先阅读 [安全复核与修复记录](SECURITY_REMEDIATION.md)：其中区分了确认的问题、防御性加固与误报，当前状态以该记录及源码测试为准。

## 审查概况

**审查日期**: 2026-09-14  
**审查范围**: mosdns-x DNS 服务器完整代码库  
**审查文件数**: 235个 Go 源文件（约5000+行关键安全代码）  
**审查重点**: 认证、SQL注入、并发安全、错误处理、安全漏洞

**总体评价**: 代码质量**良好**，正确使用了加密原语和参数化查询。但发现了**若干严重和高危问题**需要立即处理。

---

## 严重问题（需立即修复）

### 1. 凭证计数管理中的竞态条件

**位置**: `internal/control/store.go:976-996`

**问题描述**:  
`cleanupActiveCredentials` 函数在读取和修改 `u.CredentialCount` 时没有适当的同步机制。该计数器在其他事务中也会被修改，造成竞态条件。

```go
func cleanupActiveCredentials(tx *bbolt.Tx, u *userRecord, now time.Time) error {
    // ... 遍历凭证 ...
    if u.CredentialCount == 0 {
        return fmt.Errorf("credential count underflow")  // ← 可能发生下溢
    }
    u.CredentialCount--  // ← 与其他操作存在竞态条件
}
```

**影响**:
- 凭证计数可能变得不一致
- 用户可能超过 `MaxCredentials` 限制
- 凭证计数下溢可能导致 panic

**修复方案**:
```go
// 方案1: 在同一事务内执行所有凭证计数修改
func (s *Store) CreateCredential(ctx context.Context, actor, userID, name string, expires time.Time) (IssuedCredential, error) {
    // ...
    err := s.update(ctx, func(tx *bbolt.Tx) error {
        // 1. 先获取用户记录并锁定
        u, err := getUserRecordForUpdate(tx, userID)
        if err != nil {
            return err
        }
        
        // 2. 在同一事务内清理过期凭证
        if err := cleanupActiveCredentials(tx, &u, now); err != nil {
            return err
        }
        
        // 3. 检查限制
        if u.CredentialCount >= u.MaxCredentials {
            return ErrConflict
        }
        
        // 4. 创建新凭证
        // ... 创建逻辑 ...
        
        // 5. 原子性更新计数
        u.CredentialCount++
        
        // 6. 保存用户记录
        return marshalPut(tx.Bucket(bUsers), []byte(userID), u)
    })
    return issued, err
}

// 方案2: 使用数据库级别的原子计数器（BoltDB中可通过事务保证）
// 在每次修改时都重新读取最新值，而不是缓存在内存中
```

**优先级**: 🔴 **严重** - 需要立即修复

---

### 2. 认证流程中的错误处理不完整

**位置**: `internal/controlapi/handler.go:397-401`

**问题描述**:  
会话创建失败后，`RevokeSession` 的错误被静默忽略：

```go
u, err := h.opts.Control.GetUser(r.Context(), ss.UserID)
if err != nil {
    _ = h.opts.Control.RevokeSession(context.Background(), ss.UserID, ss.ID)  // ← 错误被忽略
    h.clearCookie(w)
    h.serviceError(w, err)
    return
}
```

**影响**:
- 孤立的会话可能残留在数据库中
- 会话清理失败没有被记录
- 随着时间推移可能导致资源泄漏

**修复方案**:
```go
u, err := h.opts.Control.GetUser(r.Context(), ss.UserID)
if err != nil {
    // 使用带超时的原始context，而不是Background
    revokeCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
    defer cancel()
    
    if revokeErr := h.opts.Control.RevokeSession(revokeCtx, ss.UserID, ss.ID); revokeErr != nil {
        // 记录撤销失败，但不影响主错误流程
        h.opts.Logger.Error("failed to revoke session after GetUser error",
            "session_id", ss.ID,
            "user_id", ss.UserID,
            "revoke_error", revokeErr,
            "original_error", err)
    }
    
    h.clearCookie(w)
    h.serviceError(w, err)
    return
}
```

**优先级**: 🔴 **严重** - 可能导致资源泄漏

---

### 3. 密码验证中的潜在时序攻击

**位置**: `internal/control/store.go:550-562`

**问题描述**:  
密码验证在实际的常量时间比较之前包含了多个非常量时间检查：

```go
func (s *Store) AuthenticatePassword(ctx context.Context, username, password string) (User, error) {
    r, err := s.passwordSnapshot(ctx, username)
    if err != nil {
        if !errors.Is(err, ErrInvalidCredential) {
            return User{}, err  // ← 数据库错误的不同时序
        }
        verifyPassword(password, make([]byte, 16), make([]byte, 32))  // ← 虚拟验证
        return User{}, ErrInvalidCredential
    }
    if !verifyPassword(password, r.PasswordSalt, r.PasswordHash) || enabledUser(r) != nil {
        return User{}, ErrInvalidCredential  // ← enabledUser 检查不是常量时间
    }
    return r.User, nil
}
```

**影响**:
- 攻击者可能通过时序分析枚举有效用户名
- 用户启用/禁用状态可能通过时序泄露

**修复方案**:
```go
func (s *Store) AuthenticatePassword(ctx context.Context, username, password string) (User, error) {
    r, err := s.passwordSnapshot(ctx, username)
    
    // 准备虚拟凭证用于不存在的用户
    dummySalt := make([]byte, 16)
    dummyHash := make([]byte, 32)
    
    var actualSalt, actualHash []byte
    var userExists bool
    
    if err != nil {
        // 即使用户不存在，也使用虚拟凭证
        actualSalt = dummySalt
        actualHash = dummyHash
        userExists = false
    } else {
        actualSalt = r.PasswordSalt
        actualHash = r.PasswordHash
        userExists = true
    }
    
    // 始终执行密码验证（常量时间）
    passwordValid := verifyPassword(password, actualSalt, actualHash)
    
    // 常量时间检查用户启用状态
    var userEnabled bool
    if userExists {
        userEnabled = enabledUser(r) == nil
    }
    
    // 组合检查：用户存在 AND 密码正确 AND 用户启用
    if !userExists || !passwordValid || !userEnabled {
        return User{}, ErrInvalidCredential
    }
    
    return r.User, nil
}
```

**优先级**: 🔴 **严重** - 可能导致用户枚举

---

## 高危问题

### 4. IP 限速器中的无界内存增长

**位置**: `internal/controlapi/handler.go:1965-1993`

**问题描述**:  
IP 限速器仅在达到容量时清理条目，而不是持续清理：

```go
func (l *ipLimiter) allow(ip string) bool {
    // ...
    if len(l.entries) >= l.capacity {
        for k, e := range l.entries {
            if now.Sub(e.start) >= l.window {
                delete(l.entries, k)  // ← 仅在达到容量时清理
            }
        }
        if len(l.entries) >= l.capacity {
            return false
        }
    }
    l.entries[ip] = ipEntry{now, 1}
    return true
}
```

**影响**:
- 内存可能增长到容量（4096-8192条目）并保持
- 在再次达到容量前不会发生清理
- 可能通过内存耗尽导致 DoS

**修复方案**:
```go
type ipLimiter struct {
    mu       sync.Mutex
    limit    int
    window   time.Duration
    capacity int
    now      func() time.Time
    entries  map[string]ipEntry
    lastCleanup time.Time  // 添加上次清理时间
}

func (l *ipLimiter) allow(ip string) bool {
    l.mu.Lock()
    defer l.mu.Unlock()
    
    now := l.now()
    
    // 定期清理：每分钟或每1000次请求
    if now.Sub(l.lastCleanup) >= time.Minute || len(l.entries) > l.capacity*3/4 {
        l.cleanup(now)
        l.lastCleanup = now
    }
    
    if e, ok := l.entries[ip]; ok {
        if now.Sub(e.start) >= l.window {
            l.entries[ip] = ipEntry{now, 1}
            return true
        }
        if e.count >= l.limit {
            return false
        }
        e.count++
        l.entries[ip] = e
        return true
    }
    
    if len(l.entries) >= l.capacity {
        return false  // 达到硬限制
    }
    
    l.entries[ip] = ipEntry{now, 1}
    return true
}

func (l *ipLimiter) cleanup(now time.Time) {
    for k, e := range l.entries {
        if now.Sub(e.start) >= l.window {
            delete(l.entries, k)
        }
    }
}
```

**优先级**: 🟠 **高危** - 可能导致内存耗尽

---

### 5. 提前返回时缺少事务回滚

**位置**: `internal/control/mysql_store.go:435-447`

**问题描述**:  
`withTx` 辅助函数没有使用 `defer` 进行回滚，可能导致连接泄漏：

```go
func (s *MySQLStore) withTx(parent context.Context, fn func(context.Context, *sql.Tx) error) error {
    // ...
    tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
    if err != nil {
        return mysqlStoreError(err)
    }
    if err = fn(ctx, tx); err != nil {
        _ = tx.Rollback()  // ← 未使用defer，panic时可能被跳过
        return mysqlStoreError(err)
    }
    if err = tx.Commit(); err != nil {
        return mysqlStoreError(err)  // ← 这里没有回滚
    }
    return nil
}
```

**影响**:
- panic 时连接池耗尽
- 潜在的数据库锁未释放
- 事务泄漏

**修复方案**:
```go
func (s *MySQLStore) withTx(parent context.Context, fn func(context.Context, *sql.Tx) error) error {
    opCtx, cancel := context.WithTimeout(parent, s.operationTimeout)
    defer cancel()
    
    select {
    case <-s.closeCh:
        return ErrClosed
    default:
    }
    
    ctx, cancel := context.WithTimeout(opCtx, s.operationTimeout)
    defer cancel()
    
    tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
    if err != nil {
        return mysqlStoreError(err)
    }
    
    // 使用defer确保总是调用Rollback
    // Rollback在Commit后调用是安全的（会返回sql.ErrTxDone）
    defer func() {
        if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
            // 记录回滚错误
            // log.Error("transaction rollback failed", "error", rbErr)
        }
    }()
    
    if err = fn(ctx, tx); err != nil {
        return mysqlStoreError(err)
    }
    
    if err = tx.Commit(); err != nil {
        return mysqlStoreError(err)
    }
    
    return nil
}
```

**优先级**: 🟠 **高危** - 可能导致连接泄漏

---

### 6. Control Store 操作中的 Context 误用

**位置**: `internal/control/mysql_store.go:268, 295`

**问题描述**:  
初始化和清理期间使用 `context.Background()` 忽略了父 context 的取消：

```go
ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)  // ← 忽略父context
defer cancel()
if err := db.PingContext(ctx); err != nil {
    // ...
}
```

**影响**:
- 应用关闭时可能挂起等待数据库操作
- 无法取消长时间运行的初始化
- 优雅关闭被破坏

**修复方案**:
```go
// 修改函数签名以接受父context
func NewMySQLStore(parentCtx context.Context, cfg MySQLConfig, opts Options) (*MySQLStore, error) {
    // ... 验证配置 ...
    
    // 使用父context创建超时
    startupTimeout := max(opts.OperationTimeout, 15*time.Second)
    ctx, cancel := context.WithTimeout(parentCtx, startupTimeout)
    defer cancel()
    
    if err := db.PingContext(ctx); err != nil {
        _ = db.Close()
        return nil, mysqlStoreError(err)
    }
    
    if err := initializeMySQLControl(ctx, db); err != nil {
        _ = db.Close()
        return nil, err
    }
    
    return s, nil
}

// 在调用处：
// store, err := control.NewMySQLStore(ctx, cfg, opts)
```

**优先级**: 🟠 **高危** - 影响优雅关闭

---

## 中危问题

### 7. DNS 查询处理器中的输入验证不足

**位置**: `internal/controlapi/handler.go:1517-1530`

**问题描述**:  
域名验证比较基础，可能无法捕获所有畸形输入：

```go
name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Name)), ".")
// ...
if !validLookupName(name) || !supported {
    writeError(w, http.StatusBadRequest, "invalid_input")
    return
}
if _, ok := dns.IsDomainName(dns.Fqdn(name)); !ok {  // ← 使用后才验证
    writeError(w, http.StatusBadRequest, "invalid_input")
    return
}
```

**修复方案**:
```go
// 先清理输入
name := strings.TrimSpace(in.Name)

// 立即进行完整验证
if name == "" || len(name) > 253 {  // DNS最大长度
    writeError(w, http.StatusBadRequest, "invalid_input")
    return
}

// 规范化
name = strings.ToLower(strings.TrimSuffix(name, "."))

// 严格验证
if _, ok := dns.IsDomainName(dns.Fqdn(name)); !ok {
    writeError(w, http.StatusBadRequest, "invalid_input")
    return
}

// 然后再进行业务逻辑验证
if !validLookupName(name) || !supported {
    writeError(w, http.StatusBadRequest, "invalid_input")
    return
}
```

**优先级**: 🟡 **中危**

---

### 8. 开发模式检查中的硬编码凭证问题

**位置**: `coremain/control_runtime.go:71-79, 94-104`

**问题描述**:  
开发模式允许仅 loopback 访问，但不强制额外认证：

```go
if c.Development && !loopbackHost(u.Hostname()) {
    return nil, nil, errors.New("development public_dns_url must use loopback")
}
```

**修复方案**:
```go
// 在开发模式启动时显示警告
func startControlRuntime(cfg ControlConfig) error {
    if cfg.Development {
        log.Warn("⚠️  DEVELOPMENT MODE ENABLED")
        log.Warn("   - This mode is NOT secure for production use")
        log.Warn("   - Only localhost access is permitted")
        log.Warn("   - Additional authentication is still required")
        log.Warn("   - Do NOT expose this instance to public networks")
        
        // 可选：要求设置开发模式密码
        if cfg.DevelopmentPassword == "" {
            return errors.New("development mode requires MOSDNS_DEV_PASSWORD environment variable")
        }
    }
    
    // ...
}
```

**优先级**: 🟡 **中危**

---

### 9. X-Forwarded-For 处理中的未验证重定向

**位置**: `internal/controlapi/handler.go:1899-1939`

**问题描述**:  
X-Forwarded-For 头处理信任链而不验证：

```go
func (h *Handler) clientIP(r *http.Request) string {
    // ...
    if parsed, e := netip.ParseAddr(strings.TrimSpace(raw)); e == nil {
        return parsed.String()  // ← 返回未验证的IP
    }
    return addr.String()
}
```

**修复方案**:
```go
func (h *Handler) clientIP(r *http.Request) string {
    addr := netip.MustParseAddrPort(r.RemoteAddr).Addr()
    
    // 可选：配置可信代理列表
    trustedProxies := h.opts.TrustedProxies  // []netip.Prefix
    
    // 只有当请求来自可信代理时才检查X-Forwarded-For
    if len(trustedProxies) > 0 {
        isTrusted := false
        for _, prefix := range trustedProxies {
            if prefix.Contains(addr) {
                isTrusted = true
                break
            }
        }
        
        if !isTrusted {
            return addr.String()
        }
    }
    
    // 解析X-Forwarded-For链
    xff := r.Header.Get("X-Forwarded-For")
    if xff == "" {
        return addr.String()
    }
    
    // 取最左边的IP（原始客户端）
    parts := strings.Split(xff, ",")
    if len(parts) > 0 {
        if parsed, err := netip.ParseAddr(strings.TrimSpace(parts[0])); err == nil {
            // 验证不是私有地址（如果来自公网）
            if !parsed.IsPrivate() && !parsed.IsLoopback() {
                return parsed.String()
            }
        }
    }
    
    return addr.String()
}
```

**优先级**: 🟡 **中危** - 可能绕过限速

---

### 10. 限速中的潜在整数溢出

**位置**: `internal/control/store.go:1175-1190`

**问题描述**:  
速率令牌计算使用浮点数且没有溢出检查：

```go
cap := float64(u.QPS) + float64(u.Burst)
if u.RateAt == 0 {
    u.RateTokens = cap
} else {
    elapsed := float64(now.UnixNano()-u.RateAt) / float64(time.Second)
    if elapsed > 0 {
        u.RateTokens = math.Min(cap, u.RateTokens+elapsed*float64(u.QPS))  // ← 潜在溢出
        u.RateAt = now.UnixNano()
    }
}
```

**修复方案**:
```go
cap := float64(u.QPS) + float64(u.Burst)

if u.RateAt == 0 {
    u.RateTokens = cap
    u.RateAt = now.UnixNano()
} else {
    elapsed := now.UnixNano() - u.RateAt
    
    // 处理时钟回退
    if elapsed < 0 {
        // 时钟回退，重置到当前时间
        u.RateAt = now.UnixNano()
        u.RateTokens = math.Min(cap, u.RateTokens)
    } else {
        // 限制最大时间间隔（例如1小时）以防溢出
        maxElapsed := int64(time.Hour)
        if elapsed > maxElapsed {
            elapsed = maxElapsed
        }
        
        elapsedSec := float64(elapsed) / float64(time.Second)
        if elapsedSec > 0 {
            // 安全地计算新令牌
            tokensToAdd := elapsedSec * float64(u.QPS)
            // 防止无穷大
            if !math.IsInf(tokensToAdd, 0) && !math.IsNaN(tokensToAdd) {
                u.RateTokens = math.Min(cap, u.RateTokens+tokensToAdd)
            } else {
                u.RateTokens = cap
            }
            u.RateAt = now.UnixNano()
        }
    }
}
```

**优先级**: 🟡 **中危**

---

## 代码质量问题（低-中危）

### 11. Unix Socket 文件权限问题

**位置**: `coremain/server.go:144-149, 183-186`

**问题描述**:  
Unix socket 文件以全局可写权限(0777)创建：

```go
if !abstract {
    os.Chmod(cfg.Addr, 0x777)  // ← 全局可写
}
```

**修复方案**:
```go
if !abstract {
    // 使用限制性权限
    if err := os.Chmod(cfg.Addr, 0600); err != nil {  // 仅所有者可读写
        return nil, fmt.Errorf("failed to set socket permissions: %w", err)
    }
}
```

**优先级**: 🟡 **中危** - 本地权限提升风险

---

### 12. 插件注册中的 Panic

**位置**: `coremain/register.go:59, 143`

**问题描述**:  
插件注册使用 panic 处理重复注册：

```go
if _, ok := pluginTypeRegister[typ]; ok {
    panic(fmt.Sprintf("duplicate plugin type [%s]", typ))  // ← 错误时panic
}
```

**修复方案**:
```go
// 改为返回错误
func RegisterPluginType(typ string, factory PluginFactory) error {
    mu.Lock()
    defer mu.Unlock()
    
    if _, ok := pluginTypeRegister[typ]; ok {
        return fmt.Errorf("duplicate plugin type [%s]", typ)
    }
    
    pluginTypeRegister[typ] = factory
    return nil
}

// 在init()中处理错误
func init() {
    if err := RegisterPluginType("example", newExamplePlugin); err != nil {
        // 记录错误并优雅降级，或者在这里panic（明确的设计决策）
        log.Fatal("failed to register plugin", "error", err)
    }
}
```

**优先级**: 🟢 **低危** - 代码质量问题

---

## 已验证的安全最佳实践 ✓

1. **加密操作**:
   - ✓ 所有随机数生成都使用 `crypto/rand`
   - ✓ 密码哈希使用 Argon2id 及适当参数
   - ✓ 秘密比较使用常量时间 (`subtle.ConstantTimeCompare`)
   - ✓ 凭证哈希使用 SHA-256

2. **SQL 注入防护**:
   - ✓ 所有数据库查询都使用参数化语句
   - ✓ SQL 查询中没有字符串拼接
   - ✓ 正确使用占位符（MySQL使用 `?`）

3. **输入验证**:
   - ✓ 所有用户输入都有长度检查
   - ✓ 基于白名单的协议验证
   - ✓ UUID v4 格式验证
   - ✓ 电子邮件/用户名规范化

4. **认证和授权**:
   - ✓ 基于会话的认证配合 CSRF 保护
   - ✓ 支持 Bearer token 进行 API 访问
   - ✓ 基于角色的访问控制（Admin/User）
   - ✓ 凭证过期和撤销

5. **速率限制**:
   - ✓ 使用令牌桶算法的每用户 QPS 限制
   - ✓ 每凭证配额跟踪
   - ✓ 基于 IP 的登录尝试限速

---

## 修复优先级时间表

### 立即处理（第1周）
1. 修复凭证计数管理中的竞态条件 (#1)
2. 使用 defer 实现正确的事务回滚 (#5)
3. 为认证失败添加全面的错误日志 (#2)
4. 修复 MySQL 初始化中的 context 使用 (#6)

### 短期（第1个月）
5. 重构 IP 限速器以防止内存增长 (#4)
6. 改进认证中的时序攻击防护 (#3)
7. 增强 X-Forwarded-For 验证 (#9)
8. 修复 Unix socket 权限 (#11)

### 中期（第1季度）
9. 将插件注册中的 panic 替换为错误返回 (#12)
10. 为 DNS 查询添加全面的输入验证 (#7)
11. 实现适当的速率限制溢出处理 (#10)
12. 添加开发模式警告 (#8)

### 长期（持续）
13. 标准化资源清理模式 (#13)
14. 改进服务管理状态轮询 (#14)
15. 添加 CSRF 令牌格式验证 (#15)

---

## 测试建议

1. **并发测试**: 为所有 control 包操作添加竞态检测器测试 (`go test -race`)
2. **模糊测试**: 为 DNS 消息解析和 HTTP 处理器实现模糊测试
3. **安全测试**: 对认证流程进行渗透测试
4. **负载测试**: 在高并发下验证速率限制
5. **集成测试**: 测试 MySQL 和 BoltDB 实现的故障转移场景

---

## 结论

mosdns-x 代码库总体上展示了良好的安全实践，特别是在加密操作和 SQL 注入防护方面。然而，**3个严重问题**需要立即关注：

- 凭证管理中的竞态条件
- 认证错误处理缺口
- 时序攻击漏洞

解决已识别的问题将显著改善应用程序的安全态势。应优先处理严重和高危问题，特别是那些影响认证和并发控制的问题。

**风险等级**: 中高（由于严重的竞态条件和认证问题）  
**代码质量**: 良好（在错误处理和清理方面有改进空间）  
**安全成熟度**: 中等（基础扎实但需要完善）

---

## 附录：快速修复检查清单

- [ ] 修复 `cleanupActiveCredentials` 中的竞态条件
- [ ] 在 `withTx` 中添加 `defer tx.Rollback()`
- [ ] 记录认证流程中的 `RevokeSession` 错误
- [ ] 重构 `AuthenticatePassword` 以防时序攻击
- [ ] 在 `ipLimiter` 中实现定期清理
- [ ] 修改存储初始化函数以接受父 context
- [ ] 改进 DNS 查询的输入验证顺序
- [ ] 在限速计算中添加溢出检查
- [ ] 将 Unix socket 权限更改为 0600
- [ ] 添加开发模式启动警告
- [ ] 验证 X-Forwarded-For 链
- [ ] 将插件注册 panic 改为错误返回
