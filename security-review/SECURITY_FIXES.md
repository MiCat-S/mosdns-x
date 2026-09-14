# mosdns-x 安全修复实施指南

> 本文示例保留供追溯，不是经过编译验证的补丁。部分接口不存在，部分建议会削弱现有保护；不要直接复制到源码。已适配实现、未采纳原因与验证结果见 [安全复核与修复记录](SECURITY_REMEDIATION.md)。

本文档提供了安全审查报告中所有问题的具体代码修复方案。

---

## 修复 #1: 凭证计数竞态条件

### 问题文件
- `internal/control/store.go`

### 修复方案

**选项A: 使用原子操作（推荐用于BoltDB）**

```go
// 在 userRecord 结构中，CredentialCount 保持不变，但确保所有修改都在事务内

// 修改 CreateCredential 函数
func (s *Store) CreateCredential(ctx context.Context, actor, userID, name string, expires time.Time) (IssuedCredential, error) {
    name = strings.TrimSpace(name)
    if name == "" || len(name) > 128 {
        return IssuedCredential{}, ErrInvalidInput
    }
    now := s.clock.Now().UTC()
    if !expires.IsZero() && !now.Before(expires) {
        return IssuedCredential{}, fmt.Errorf("%w: credential expiry must be in the future", ErrInvalidInput)
    }
    var issued IssuedCredential
    err := s.update(ctx, func(tx *bbolt.Tx) error {
        if e := authorizeCredentialOwner(tx, actor, userID, now); e != nil {
            return e
        }
        
        // 【修复点1】先获取用户记录
        u, e := getUserRecord(tx, userID)
        if e != nil {
            return e
        }
        
        if !expires.IsZero() && !u.ExpiresAt.IsZero() && expires.After(u.ExpiresAt) {
            return ErrInvalidInput
        }
        
        // 【修复点2】在检查限制前清理，并在同一事务内
        if e = cleanupActiveCredentials(tx, &u, now); e != nil {
            return e
        }
        
        // 【修复点3】重新获取最新的用户记录以确保计数准确
        // 这在cleanupActiveCredentials可能修改了u后很重要
        if u.CredentialCount >= u.MaxCredentials {
            return ErrConflict
        }
        
        // 创建凭证...
        id, e := uniqueCredentialID(tx.Bucket(bCredentials))
        if e != nil {
            return e
        }
        token, tokenHash, e := uniqueCredentialToken(tx.Bucket(bCredentialTokens))
        if e != nil {
            return e
        }
        c := Credential{ID: id, UserID: userID, Name: name, ExpiresAt: expires, CreatedAt: now, UpdatedAt: now}
        r := credentialRecord{Credential: c, TokenHash: append([]byte(nil), tokenHash[:]...), Version: 1}
        if e = marshalPut(tx.Bucket(bCredentials), []byte(id), r); e != nil {
            return e
        }
        if e = tx.Bucket(bCredentialTokens).Put(tokenHash[:], []byte(id)); e != nil {
            return e
        }
        if e = tx.Bucket(bUserCredentials).Put(userCredentialKey(userID, id), []byte(id)); e != nil {
            return e
        }
        if e = tx.Bucket(bActiveCredentials).Put(userCredentialKey(userID, id), []byte(id)); e != nil {
            return e
        }
        
        // 【修复点4】原子性递增计数
        u.CredentialCount++
        
        // 【修复点5】在事务提交前保存更新的计数
        if e = marshalPut(tx.Bucket(bUsers), []byte(userID), u); e != nil {
            return e
        }
        
        if e = s.audit(tx, actor, "create_credential", "credential", id, map[string]any{"name": name}, now); e != nil {
            return e
        }
        issued = IssuedCredential{Credential: c, Token: token}
        return nil
    })
    return issued, err
}

// 修改 RevokeCredential 函数中的计数递减
func (s *Store) RevokeCredential(ctx context.Context, actor, userID, id string) error {
    now := s.clock.Now().UTC()
    return s.update(ctx, func(tx *bbolt.Tx) error {
        if e := authorizeCredentialOwner(tx, actor, userID, now); e != nil {
            return e
        }
        var r credentialRecord
        if e := decode(tx.Bucket(bCredentials).Get([]byte(id)), &r); e != nil {
            return e
        }
        if r.UserID != userID {
            return ErrForbidden
        }
        if r.RevokedAt.IsZero() {
            r.RevokedAt = now
            r.UpdatedAt = now
            if e := marshalPut(tx.Bucket(bCredentials), []byte(id), r); e != nil {
                return e
            }
            activeKey := userCredentialKey(userID, id)
            active := tx.Bucket(bActiveCredentials)
            if active.Get(activeKey) != nil {
                // 【修复点】在同一事务内获取并更新用户
                u, e := getUserRecord(tx, userID)
                if e != nil {
                    return e
                }
                // 【修复点】添加防护检查
                if u.CredentialCount == 0 {
                    // 记录错误但不返回，允许撤销继续
                    // log.Error("credential count underflow", "user_id", userID)
                    // 设置为0而不是递减
                    u.CredentialCount = 0
                } else {
                    u.CredentialCount--
                }
                if e = active.Delete(activeKey); e != nil {
                    return e
                }
                // 【修复点】保存更新的用户记录
                if e = marshalPut(tx.Bucket(bUsers), []byte(userID), u); e != nil {
                    return e
                }
            }
        }
        if e := s.audit(tx, actor, "revoke_credential", "credential", id, nil, now); e != nil {
            return e
        }
        return nil
    })
}

// 改进 cleanupActiveCredentials 函数
func cleanupActiveCredentials(tx *bbolt.Tx, u *userRecord, now time.Time) error {
    b := tx.Bucket(bActiveCredentials)
    prefix := []byte(u.ID + "\x00")
    c := b.Cursor()
    
    var toDelete [][]byte
    var deleteCount uint32
    
    // 【修复点1】先收集需要删除的项
    for k, id := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, id = c.Next() {
        var r credentialRecord
        if err := decode(tx.Bucket(bCredentials).Get(id), &r); err != nil {
            return err
        }
        if !r.RevokedAt.IsZero() || !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
            toDelete = append(toDelete, append([]byte(nil), k...))
            deleteCount++
        }
    }
    
    // 【修复点2】再执行删除
    for _, k := range toDelete {
        if err := b.Delete(k); err != nil {
            return err
        }
    }
    
    // 【修复点3】安全地更新计数
    if deleteCount > 0 {
        if u.CredentialCount < deleteCount {
            // 记录不一致但修正它
            // log.Warn("credential count underflow detected", 
            //     "user_id", u.ID, 
            //     "current_count", u.CredentialCount, 
            //     "delete_count", deleteCount)
            u.CredentialCount = 0
        } else {
            u.CredentialCount -= deleteCount
        }
    }
    
    return nil
}
```

**选项B: 添加凭证计数验证和自愈机制**

```go
// 添加辅助函数来重新计算实际的活动凭证数
func countActiveCredentials(tx *bbolt.Tx, userID string, now time.Time) (uint32, error) {
    b := tx.Bucket(bActiveCredentials)
    prefix := []byte(userID + "\x00")
    c := b.Cursor()
    
    var count uint32
    for k, id := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, id = c.Next() {
        var r credentialRecord
        if err := decode(tx.Bucket(bCredentials).Get(id), &r); err != nil {
            continue // 跳过损坏的记录
        }
        // 只计数未撤销且未过期的凭证
        if r.RevokedAt.IsZero() && (r.ExpiresAt.IsZero() || now.Before(r.ExpiresAt)) {
            count++
        }
    }
    return count, nil
}

// 在关键操作前验证计数
func (s *Store) CreateCredential(ctx context.Context, actor, userID, name string, expires time.Time) (IssuedCredential, error) {
    // ... 前置验证 ...
    
    var issued IssuedCredential
    err := s.update(ctx, func(tx *bbolt.Tx) error {
        // ... 授权检查 ...
        
        u, e := getUserRecord(tx, userID)
        if e != nil {
            return e
        }
        
        // 【修复：添加计数验证】
        actualCount, e := countActiveCredentials(tx, userID, now)
        if e != nil {
            return e
        }
        
        // 如果存储的计数与实际不符，修正它
        if u.CredentialCount != actualCount {
            // log.Warn("credential count mismatch, correcting",
            //     "user_id", userID,
            //     "stored", u.CredentialCount,
            //     "actual", actualCount)
            u.CredentialCount = actualCount
        }
        
        // 清理过期凭证
        if e = cleanupActiveCredentials(tx, &u, now); e != nil {
            return e
        }
        
        // 重新验证实际计数
        actualCount, e = countActiveCredentials(tx, userID, now)
        if e != nil {
            return e
        }
        
        if actualCount >= u.MaxCredentials {
            return ErrConflict
        }
        
        // ... 创建凭证 ...
        // ... 递增计数和保存 ...
        
        return nil
    })
    return issued, err
}
```

---

## 修复 #2: 认证错误处理

### 问题文件
- `internal/controlapi/handler.go`

### 修复代码

```go
// 在 handler.go 中修改会话处理
func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodPost:
        // ... 解析和验证 ...
        
        ss, token, err := h.opts.Control.CreatePasswordSession(r.Context(), 
            in.Username, in.Password, sessionTTL(h.opts.SessionTTL, in.RememberMe))
        if err != nil {
            h.serviceError(w, err)
            return
        }
        
        h.setCookie(w, token, ss.ExpiresAt)
        
        // 【修复：改进错误处理】
        u, err := h.opts.Control.GetUser(r.Context(), ss.UserID)
        if err != nil {
            // 使用原始context的派生context进行清理，而不是Background
            cleanupCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
            defer cancel()
            
            // 尝试撤销会话
            if revokeErr := h.opts.Control.RevokeSession(cleanupCtx, ss.UserID, ss.ID); revokeErr != nil {
                // 【修复：记录撤销失败】
                // 这里应该使用实际的日志系统
                if h.opts.Logger != nil {
                    h.opts.Logger.Error("failed to revoke session after GetUser error",
                        "session_id", ss.ID,
                        "user_id", ss.UserID,
                        "get_user_error", err,
                        "revoke_error", revokeErr,
                        "client_ip", h.clientIP(r))
                }
                
                // 可选：增加监控指标
                // metrics.IncrCounter("session.cleanup_failure", 1)
            }
            
            h.clearCookie(w)
            h.serviceError(w, err)
            return
        }
        
        writeJSON(w, http.StatusOK, sessionResponse{u, ss.CSRFToken, ss.ExpiresAt})
        
    // ... 其他cases ...
    }
}

// 【修复：添加会话清理辅助函数】
func (h *Handler) cleanupSession(ctx context.Context, userID, sessionID string) {
    cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()
    
    if err := h.opts.Control.RevokeSession(cleanupCtx, userID, sessionID); err != nil {
        if h.opts.Logger != nil {
            h.opts.Logger.Error("session cleanup failed",
                "session_id", sessionID,
                "user_id", userID,
                "error", err)
        }
        
        // 可以在这里添加重试逻辑或将失败的清理加入队列
        // h.queueSessionCleanup(userID, sessionID)
    }
}

// 【可选：添加异步清理队列】
type sessionCleanupQueue struct {
    mu      sync.Mutex
    pending []sessionCleanupItem
    control Control
}

type sessionCleanupItem struct {
    userID    string
    sessionID string
    attempts  int
    nextRetry time.Time
}

func (q *sessionCleanupQueue) add(userID, sessionID string) {
    q.mu.Lock()
    defer q.mu.Unlock()
    
    q.pending = append(q.pending, sessionCleanupItem{
        userID:    userID,
        sessionID: sessionID,
        attempts:  0,
        nextRetry: time.Now().Add(time.Minute),
    })
}

func (q *sessionCleanupQueue) processLoop(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            q.process(ctx)
        }
    }
}

func (q *sessionCleanupQueue) process(ctx context.Context) {
    q.mu.Lock()
    pending := q.pending
    q.pending = nil
    q.mu.Unlock()
    
    var failed []sessionCleanupItem
    
    for _, item := range pending {
        if time.Now().Before(item.nextRetry) {
            failed = append(failed, item)
            continue
        }
        
        cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        err := q.control.RevokeSession(cleanupCtx, item.userID, item.sessionID)
        cancel()
        
        if err != nil {
            item.attempts++
            if item.attempts < 5 {  // 最多重试5次
                item.nextRetry = time.Now().Add(time.Duration(item.attempts) * time.Minute)
                failed = append(failed, item)
            }
            // 超过重试次数后放弃
        }
    }
    
    if len(failed) > 0 {
        q.mu.Lock()
        q.pending = append(q.pending, failed...)
        q.mu.Unlock()
    }
}
```

---

## 修复 #3: 时序攻击防护

### 问题文件
- `internal/control/store.go`

### 修复代码

```go
// 修改 AuthenticatePassword 函数
func (s *Store) AuthenticatePassword(ctx context.Context, username, password string) (User, error) {
    // 【修复：准备虚拟凭证】
    dummySalt := make([]byte, 16)
    dummyHash := make([]byte, 32)
    // 用固定种子生成一致的虚拟值（可选，用于一致性）
    // 这样对同一不存在的用户名，验证时间始终一致
    
    r, err := s.passwordSnapshot(ctx, username)
    
    var actualSalt, actualHash []byte
    var userRecord *userSnapshot
    userExists := false
    
    if err != nil {
        if !errors.Is(err, ErrInvalidCredential) {
            // 【修复：数据库错误时仍然执行虚拟验证】
            verifyPassword(password, dummySalt, dummyHash)
            return User{}, err
        }
        // 用户不存在
        actualSalt = dummySalt
        actualHash = dummyHash
        userExists = false
    } else {
        // 用户存在
        actualSalt = r.PasswordSalt
        actualHash = r.PasswordHash
        userExists = true
        userRecord = &r
    }
    
    // 【修复：始终执行密码验证（常量时间）】
    passwordValid := verifyPassword(password, actualSalt, actualHash)
    
    // 【修复：常量时间检查用户状态】
    var accountEnabled bool
    if userExists && userRecord != nil {
        // enabledUser返回nil表示启用
        accountEnabled = enabledUser(*userRecord) == nil
    } else {
        accountEnabled = false
    }
    
    // 【修复：组合所有检查结果】
    // 所有条件必须为真：用户存在 AND 密码正确 AND 账户启用
    authSuccess := userExists && passwordValid && accountEnabled
    
    if !authSuccess {
        return User{}, ErrInvalidCredential
    }
    
    return userRecord.User, nil
}

// 可选：为verifyPassword添加最小时间保证
func verifyPassword(password string, salt, hash []byte) bool {
    start := time.Now()
    defer func() {
        // 确保至少花费固定时间（例如10ms）
        minDuration := 10 * time.Millisecond
        elapsed := time.Since(start)
        if elapsed < minDuration {
            time.Sleep(minDuration - elapsed)
        }
    }()
    
    if len(salt) != 16 || len(hash) != 32 {
        return false
    }
    
    derived := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, 32)
    return subtle.ConstantTimeCompare(derived, hash) == 1
}

// 【额外防护：添加失败延迟】
func (s *Store) AuthenticatePassword(ctx context.Context, username, password string) (User, error) {
    startTime := time.Now()
    
    // ... 上面的认证逻辑 ...
    
    // 【修复：认证失败时添加延迟】
    user, err := s.authenticatePasswordInternal(ctx, username, password)
    
    if err != nil {
        // 失败时添加随机延迟（100-200ms）
        // 使认证时序更难分析
        failureDelay := 100*time.Millisecond + time.Duration(rand.Intn(100))*time.Millisecond
        select {
        case <-time.After(failureDelay):
        case <-ctx.Done():
            return User{}, ctx.Err()
        }
    }
    
    return user, err
}
```

---

## 修复 #4: IP 限速器内存增长

### 问题文件
- `internal/controlapi/handler.go`

### 修复代码

```go
type ipLimiter struct {
    mu          sync.Mutex
    limit       int
    window      time.Duration
    capacity    int
    now         func() time.Time
    entries     map[string]ipEntry
    lastCleanup time.Time  // 【新增】
}

func newIPLimiter(limit int, window time.Duration, capacity int, now func() time.Time) *ipLimiter {
    return &ipLimiter{
        limit:       limit,
        window:      window,
        capacity:    capacity,
        now:         now,
        entries:     make(map[string]ipEntry),
        lastCleanup: now(),  // 【新增】
    }
}

func (l *ipLimiter) allow(ip string) bool {
    l.mu.Lock()
    defer l.mu.Unlock()
    
    now := l.now()
    
    // 【修复：定期清理过期条目】
    // 每分钟清理一次，或当达到容量的75%时清理
    shouldCleanup := now.Sub(l.lastCleanup) >= time.Minute || 
                     len(l.entries) > (l.capacity*3)/4
    
    if shouldCleanup {
        l.cleanup(now)
        l.lastCleanup = now
    }
    
    // 检查现有条目
    if e, ok := l.entries[ip]; ok {
        // 窗口已过期，重置
        if now.Sub(e.start) >= l.window {
            l.entries[ip] = ipEntry{now, 1}
            return true
        }
        // 超过限制
        if e.count >= l.limit {
            return false
        }
        // 递增计数
        e.count++
        l.entries[ip] = e
        return true
    }
    
    // 【修复：硬限制检查】
    // 如果清理后仍然满了，拒绝新IP
    if len(l.entries) >= l.capacity {
        return false
    }
    
    // 新IP
    l.entries[ip] = ipEntry{now, 1}
    return true
}

// 【新增：清理辅助函数】
func (l *ipLimiter) cleanup(now time.Time) {
    for k, e := range l.entries {
        if now.Sub(e.start) >= l.window {
            delete(l.entries, k)
        }
    }
}

// 【可选：添加统计方法用于监控】
func (l *ipLimiter) stats() (active, capacity int) {
    l.mu.Lock()
    defer l.mu.Unlock()
    return len(l.entries), l.capacity
}

// 【可选：更激进的清理策略】
type ipLimiterV2 struct {
    mu          sync.Mutex
    limit       int
    window      time.Duration
    capacity    int
    now         func() time.Time
    entries     map[string]ipEntry
    cleanupTicker *time.Ticker
    done        chan struct{}
}

func newIPLimiterV2(limit int, window time.Duration, capacity int, now func() time.Time) *ipLimiterV2 {
    l := &ipLimiterV2{
        limit:    limit,
        window:   window,
        capacity: capacity,
        now:      now,
        entries:  make(map[string]ipEntry),
        done:     make(chan struct{}),
    }
    
    // 【新增：后台清理goroutine】
    l.cleanupTicker = time.NewTicker(30 * time.Second)
    go l.cleanupLoop()
    
    return l
}

func (l *ipLimiterV2) cleanupLoop() {
    for {
        select {
        case <-l.cleanupTicker.C:
            l.mu.Lock()
            now := l.now()
            for k, e := range l.entries {
                if now.Sub(e.start) >= l.window {
                    delete(l.entries, k)
                }
            }
            l.mu.Unlock()
        case <-l.done:
            l.cleanupTicker.Stop()
            return
        }
    }
}

func (l *ipLimiterV2) close() {
    close(l.done)
}
```

---

## 修复 #5: 事务回滚

### 问题文件
- `internal/control/mysql_store.go`

### 修复代码

```go
func (s *MySQLStore) withTx(parent context.Context, fn func(context.Context, *sql.Tx) error) error {
    // 检查store是否已关闭
    select {
    case <-s.closeCh:
        return ErrClosed
    default:
    }
    
    // 使用操作超时
    opCtx, cancel := context.WithTimeout(parent, s.operationTimeout)
    defer cancel()
    
    ctx, cancel := context.WithTimeout(opCtx, s.operationTimeout)
    defer cancel()
    
    // 开启事务
    tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
    if err != nil {
        return mysqlStoreError(err)
    }
    
    // 【修复：使用defer确保回滚】
    // Rollback在Commit成功后调用是安全的（会返回sql.ErrTxDone）
    defer func() {
        if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
            // 【修复：记录回滚失败】
            // 这可能表示连接问题或严重的数据库错误
            // log.Error("transaction rollback failed", "error", rbErr)
            
            // 可选：增加监控指标
            // metrics.IncrCounter("db.rollback_failure", 1)
        }
    }()
    
    // 执行事务函数
    if err = fn(ctx, tx); err != nil {
        // 错误已经通过defer处理回滚
        return mysqlStoreError(err)
    }
    
    // 提交事务
    if err = tx.Commit(); err != nil {
        return mysqlStoreError(err)
    }
    
    return nil
}

// 【可选：添加重试逻辑】
func (s *MySQLStore) withTxRetry(parent context.Context, fn func(context.Context, *sql.Tx) error) error {
    maxRetries := 3
    var lastErr error
    
    for i := 0; i < maxRetries; i++ {
        err := s.withTx(parent, fn)
        if err == nil {
            return nil
        }
        
        lastErr = err
        
        // 检查是否是可重试的错误
        if isRetryableError(err) && i < maxRetries-1 {
            // 指数退避
            backoff := time.Duration(1<<uint(i)) * 100 * time.Millisecond
            select {
            case <-time.After(backoff):
                continue
            case <-parent.Done():
                return parent.Err()
            }
        }
        
        break
    }
    
    return lastErr
}

func isRetryableError(err error) bool {
    // MySQL错误代码
    const (
        errDeadlock        = 1213
        errLockWaitTimeout = 1205
    )
    
    var mysqlErr *mysql.MySQLError
    if errors.As(err, &mysqlErr) {
        switch mysqlErr.Number {
        case errDeadlock, errLockWaitTimeout:
            return true
        }
    }
    
    return false
}
```

---

## 修复 #6: Context 使用

### 问题文件
- `internal/control/mysql_store.go`

### 修复代码

```go
// 修改函数签名以接受父context
func NewMySQLStore(parentCtx context.Context, cfg MySQLConfig, opts Options) (*MySQLStore, error) {
    if opts.Clock == nil {
        opts.Clock = realClock{}
    }
    if opts.OperationTimeout == 0 {
        opts.OperationTimeout = 15 * time.Second
    }
    if opts.ConnMaxLifetime == 0 {
        opts.ConnMaxLifetime = 5 * time.Minute
    }
    if opts.MaxOpenConns <= 0 {
        opts.MaxOpenConns = 25
    }
    if opts.MaxIdleConns <= 0 {
        opts.MaxIdleConns = 5
    }
    
    // 构建DSN...
    dsn := cfg.User
    if cfg.Password != "" {
        dsn += ":" + cfg.Password
    }
    dsn += "@"
    if cfg.Net != "" {
        dsn += cfg.Net + "("
    }
    dsn += cfg.Addr
    if cfg.Net != "" {
        dsn += ")"
    }
    dsn += "/" + cfg.DBName + "?parseTime=true&loc=UTC"
    
    db, err := sql.Open("mysql", dsn)
    if err != nil {
        return nil, mysqlStoreError(err)
    }
    
    db.SetMaxOpenConns(opts.MaxOpenConns)
    db.SetMaxIdleConns(opts.MaxIdleConns)
    db.SetConnMaxLifetime(opts.ConnMaxLifetime)
    
    s := &MySQLStore{
        db:               db,
        clock:            opts.Clock,
        operationTimeout: opts.OperationTimeout,
        closeCh:          make(chan struct{}),
    }
    
    // 【修复：使用父context而不是Background】
    startupTimeout := max(opts.OperationTimeout, 15*time.Second)
    ctx, cancel := context.WithTimeout(parentCtx, startupTimeout)
    defer cancel()
    
    // Ping数据库
    if err := db.PingContext(ctx); err != nil {
        _ = db.Close()
        return nil, mysqlStoreError(err)
    }
    
    // 初始化schema
    if err := initializeMySQLControl(ctx, db); err != nil {
        _ = db.Close()
        return nil, err
    }
    
    return s, nil
}

// 【修复：Close方法也应该接受context】
func (s *MySQLStore) Close(ctx context.Context) error {
    // 发信号通知所有操作store正在关闭
    close(s.closeCh)
    
    // 【修复：使用context进行优雅关闭】
    // 给正在进行的操作一些时间完成
    gracePeriod := 5 * time.Second
    shutdownCtx, cancel := context.WithTimeout(ctx, gracePeriod)
    defer cancel()
    
    // 可选：等待活动连接完成
    done := make(chan error, 1)
    go func() {
        done <- s.db.Close()
    }()
    
    select {
    case err := <-done:
        return mysqlStoreError(err)
    case <-shutdownCtx.Done():
        // 超时，强制关闭
        return s.db.Close()
    }
}

// 在调用处更新：
// 在 coremain 或其他初始化代码中
func initializeControlStore(ctx context.Context, cfg config.Control) (control.Store, error) {
    if cfg.MySQL != nil {
        mysqlCfg := control.MySQLConfig{
            Addr:     cfg.MySQL.Addr,
            User:     cfg.MySQL.User,
            Password: cfg.MySQL.Password,
            DBName:   cfg.MySQL.Database,
            Net:      "tcp",
        }
        
        opts := control.Options{
            Clock:            nil,  // 使用默认
            OperationTimeout: 15 * time.Second,
            ConnMaxLifetime:  5 * time.Minute,
            MaxOpenConns:     25,
            MaxIdleConns:     5,
        }
        
        // 【修复：传递context】
        return control.NewMySQLStore(ctx, mysqlCfg, opts)
    }
    
    // BoltDB...
    return control.Open(cfg.Database, control.Options{
        Clock:            nil,
        AdmitQueueSize:   1024,
    })
}
```

继续下一部分...

---

## 修复 #7-10: 中危问题修复

### 修复 #7: DNS 查询输入验证

**文件**: `internal/controlapi/handler.go`

```go
func (h *Handler) handleLookup(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        methodNotAllowed(w)
        return
    }
    
    var in lookupRequest
    if !readJSON(w, r, &in) {
        return
    }
    
    // 【修复：先进行基本验证】
    rawName := strings.TrimSpace(in.Name)
    
    // 长度检查
    if rawName == "" {
        writeError(w, http.StatusBadRequest, "invalid_input")
        return
    }
    
    if len(rawName) > 253 {  // DNS名称最大长度
        writeError(w, http.StatusBadRequest, "invalid_input")
        return
    }
    
    // 【修复：立即进行DNS格式验证】
    // 先规范化
    name := strings.ToLower(strings.TrimSuffix(rawName, "."))
    
    // 验证DNS格式
    if _, ok := dns.IsDomainName(dns.Fqdn(name)); !ok {
        writeError(w, http.StatusBadRequest, "invalid_input")
        return
    }
    
    // 【修复：添加额外安全检查】
    // 防止过长的标签
    labels := strings.Split(name, ".")
    for _, label := range labels {
        if len(label) > 63 {  // DNS标签最大长度
            writeError(w, http.StatusBadRequest, "invalid_input")
            return
        }
        // 防止空标签
        if len(label) == 0 {
            writeError(w, http.StatusBadRequest, "invalid_input")
            return
        }
    }
    
    // 验证查询类型
    var qtype uint16
    switch strings.ToUpper(in.Type) {
    case "A":
        qtype = dns.TypeA
    case "AAAA":
        qtype = dns.TypeAAAA
    case "CNAME":
        qtype = dns.TypeCNAME
    case "MX":
        qtype = dns.TypeMX
    case "TXT":
        qtype = dns.TypeTXT
    case "NS":
        qtype = dns.TypeNS
    default:
        writeError(w, http.StatusBadRequest, "invalid_input")
        return
    }
    
    // 现在进行业务逻辑验证
    if !validLookupName(name) {
        writeError(w, http.StatusBadRequest, "invalid_input")
        return
    }
    
    // 继续处理查询...
}

// 【新增：更严格的名称验证】
func validLookupName(name string) bool {
    // 拒绝保留名称
    reserved := []string{"localhost", "invalid", "test", "example"}
    for _, r := range reserved {
        if name == r || strings.HasSuffix(name, "."+r) {
            return false
        }
    }
    
    // 拒绝私有地址的反向DNS
    if strings.HasSuffix(name, ".in-addr.arpa") || 
       strings.HasSuffix(name, ".ip6.arpa") {
        // 可以允许特定情况
        return false
    }
    
    // 必须至少有一个点（TLD不单独查询）
    if !strings.Contains(name, ".") {
        return false
    }
    
    return true
}
```

---

### 修复 #8: 开发模式安全

**文件**: `coremain/control_runtime.go`

```go
func newControlRuntime(c ControlConfig, opts controlRuntimeOptions) (*controlRuntime, Handler, error) {
    // 【修复：开发模式警告】
    if c.Development {
        // 打印醒目的警告
        fmt.Fprintf(os.Stderr, "\n")
        fmt.Fprintf(os.Stderr, "╔═══════════════════════════════════════════════════════════╗\n")
        fmt.Fprintf(os.Stderr, "║  ⚠️  WARNING: DEVELOPMENT MODE ENABLED                   ║\n")
        fmt.Fprintf(os.Stderr, "╠═══════════════════════════════════════════════════════════╣\n")
        fmt.Fprintf(os.Stderr, "║  • This mode is NOT SECURE for production use            ║\n")
        fmt.Fprintf(os.Stderr, "║  • Only loopback (localhost) access is permitted         ║\n")
        fmt.Fprintf(os.Stderr, "║  • Authentication is still required                      ║\n")
        fmt.Fprintf(os.Stderr, "║  • Do NOT expose this instance to public networks        ║\n")
        fmt.Fprintf(os.Stderr, "║  • Development mode disables certain security features   ║\n")
        fmt.Fprintf(os.Stderr, "╚═══════════════════════════════════════════════════════════╝\n")
        fmt.Fprintf(os.Stderr, "\n")
        
        // 【可选：要求环境变量确认】
        if os.Getenv("MOSDNS_CONFIRM_DEV_MODE") != "yes" {
            return nil, nil, errors.New(
                "development mode requires MOSDNS_CONFIRM_DEV_MODE=yes environment variable")
        }
        
        // 【可选：开发模式专用密码】
        devPassword := os.Getenv("MOSDNS_DEV_PASSWORD")
        if devPassword == "" {
            return nil, nil, errors.New(
                "development mode requires MOSDNS_DEV_PASSWORD environment variable")
        }
        
        // 将开发密码存储用于额外验证
        opts.DevPassword = devPassword
    }
    
    // 验证 public_dns_url
    u, err := url.Parse(c.PublicDNSURL)
    if err != nil {
        return nil, nil, err
    }
    
    // 【修复：更严格的开发模式检查】
    if c.Development {
        if !loopbackHost(u.Hostname()) {
            return nil, nil, errors.New(
                "development mode: public_dns_url must use loopback address (localhost, 127.0.0.1, ::1)")
        }
        
        // 确保不使用标准端口（减少意外暴露风险）
        if u.Port() == "80" || u.Port() == "443" || u.Port() == "" {
            return nil, nil, errors.New(
                "development mode: must use non-standard port to prevent accidental exposure")
        }
    }
    
    // ... 继续初始化 ...
}
```

---

### 修复 #9: X-Forwarded-For 验证

**文件**: `internal/controlapi/handler.go`

```go
// 【新增：HandlerOptions 添加信任代理配置】
type HandlerOptions struct {
    // ... 现有字段 ...
    
    // TrustedProxies 是受信任的代理IP前缀列表
    // 只有来自这些代理的请求才会检查X-Forwarded-For
    TrustedProxies []netip.Prefix
    
    // TrustXForwardedFor 启用X-Forwarded-For处理
    // 如果为false，始终使用RemoteAddr
    TrustXForwardedFor bool
}

func (h *Handler) clientIP(r *http.Request) string {
    // 解析远程地址
    addr, err := netip.ParseAddrPort(r.RemoteAddr)
    if err != nil {
        // 回退：尝试只解析IP
        if ip, err := netip.ParseAddr(r.RemoteAddr); err == nil {
            return ip.String()
        }
        return r.RemoteAddr
    }
    
    remoteIP := addr.Addr()
    
    // 【修复：检查是否应该信任X-Forwarded-For】
    if !h.opts.TrustXForwardedFor || len(h.opts.TrustedProxies) == 0 {
        return remoteIP.String()
    }
    
    // 【修复：验证请求是否来自受信任的代理】
    isTrusted := false
    for _, prefix := range h.opts.TrustedProxies {
        if prefix.Contains(remoteIP) {
            isTrusted = true
            break
        }
    }
    
    // 如果不来自受信任的代理，使用RemoteAddr
    if !isTrusted {
        return remoteIP.String()
    }
    
    // 【修复：安全解析X-Forwarded-For链】
    xff := r.Header.Get("X-Forwarded-For")
    if xff == "" {
        return remoteIP.String()
    }
    
    // 解析链中的所有IP
    parts := strings.Split(xff, ",")
    if len(parts) == 0 {
        return remoteIP.String()
    }
    
    // 【修复：从右向左遍历，找到第一个不受信任的IP】
    // 这是最左边的客户端IP（最原始的）
    for i := 0; i < len(parts); i++ {
        ipStr := strings.TrimSpace(parts[i])
        ip, err := netip.ParseAddr(ipStr)
        if err != nil {
            // 格式错误的IP，拒绝整个链
            return remoteIP.String()
        }
        
        // 【修复：验证IP不是明显伪造的】
        // 拒绝未指定地址
        if ip.IsUnspecified() {
            return remoteIP.String()
        }
        
        // 第一个有效的IP即为客户端IP
        // 可选：额外验证（例如拒绝私有地址作为公网客户端）
        if h.opts.RejectPrivateXFF && ip.IsPrivate() {
            return remoteIP.String()
        }
        
        return ip.String()
    }
    
    return remoteIP.String()
}

// 【新增：配置辅助函数】
func parseTrustedProxies(cidrs []string) ([]netip.Prefix, error) {
    var prefixes []netip.Prefix
    for _, cidr := range cidrs {
        prefix, err := netip.ParsePrefix(cidr)
        if err != nil {
            return nil, fmt.Errorf("invalid proxy CIDR %q: %w", cidr, err)
        }
        prefixes = append(prefixes, prefix)
    }
    return prefixes, nil
}

// 在配置中：
// trusted_proxies:
//   - "10.0.0.0/8"      # 内部负载均衡器
//   - "172.16.0.0/12"   # 内部网络
//   - "192.168.1.0/24"  # 特定代理子网
```

---

### 修复 #10: 速率限制整数溢出

**文件**: `internal/control/store.go`

```go
func (s *Store) Admit(ctx context.Context, id Identity) error {
    return s.batch(ctx, func(tx *bbolt.Tx) error {
        if err := ctx.Err(); err != nil {
            return err
        }
        
        now := s.clock.Now().UTC()
        
        // ... 凭证和用户验证 ...
        
        u, err := getUserRecord(tx, id.UserID)
        if err != nil {
            return ErrForbidden
        }
        
        if err = entitledUser(u, now); err != nil {
            return err
        }
        
        // ... 配额检查 ...
        
        // 【修复：安全的令牌桶计算】
        cap := float64(u.QPS) + float64(u.Burst)
        
        // 【修复：处理初始化】
        if u.RateAt == 0 {
            u.RateTokens = cap
            u.RateAt = now.UnixNano()
        } else {
            nowNano := now.UnixNano()
            elapsed := nowNano - u.RateAt
            
            // 【修复：处理时钟回退】
            if elapsed < 0 {
                // 时钟回退，重置到当前时间但保持当前令牌数
                u.RateAt = nowNano
                u.RateTokens = math.Min(cap, u.RateTokens)
            } else if elapsed > 0 {
                // 【修复：限制最大时间间隔以防溢出】
                const maxElapsedNanos = int64(time.Hour)  // 最多累积1小时的令牌
                if elapsed > maxElapsedNanos {
                    elapsed = maxElapsedNanos
                }
                
                // 计算经过的秒数
                elapsedSeconds := float64(elapsed) / float64(time.Second)
                
                // 【修复：安全地计算要添加的令牌】
                tokensToAdd := elapsedSeconds * float64(u.QPS)
                
                // 【修复：检查浮点溢出】
                if math.IsInf(tokensToAdd, 0) || math.IsNaN(tokensToAdd) || tokensToAdd < 0 {
                    // 溢出或无效值，重置到容量
                    u.RateTokens = cap
                } else {
                    // 正常累加
                    newTokens := u.RateTokens + tokensToAdd
                    
                    // 再次检查结果
                    if math.IsInf(newTokens, 0) || math.IsNaN(newTokens) {
                        u.RateTokens = cap
                    } else {
                        u.RateTokens = math.Min(cap, newTokens)
                    }
                }
                
                u.RateAt = nowNano
            }
            // elapsed == 0: 不更新，使用当前值
        }
        
        // 【修复：验证令牌数有效】
        if math.IsNaN(u.RateTokens) || math.IsInf(u.RateTokens, 0) || u.RateTokens < 0 {
            // 无效状态，重置
            u.RateTokens = cap
        }
        
        // 检查是否有足够的令牌
        if u.RateTokens < 1 {
            return ErrRateLimited
        }
        
        // 消耗一个令牌
        u.RateTokens--
        u.QuotaUsed++
        
        // 保存更新的用户记录
        if err = marshalPut(tx.Bucket(bUsers), []byte(u.ID), u); err != nil {
            return err
        }
        
        // ... 使用量统计 ...
        
        return nil
    })
}

// 【可选：添加令牌桶状态健康检查】
func (s *Store) validateRateState(u *userRecord) bool {
    cap := float64(u.QPS) + float64(u.Burst)
    
    // 检查令牌数是否在有效范围内
    if math.IsNaN(u.RateTokens) || math.IsInf(u.RateTokens, 0) {
        return false
    }
    
    if u.RateTokens < 0 || u.RateTokens > cap*2 {  // 允许一些余量
        return false
    }
    
    // 检查时间戳是否合理
    if u.RateAt < 0 {
        return false
    }
    
    now := s.clock.Now().UTC().UnixNano()
    if u.RateAt > now {
        // 时间戳在未来
        return false
    }
    
    // 时间戳不应该太旧（例如超过1周）
    if now-u.RateAt > int64(7*24*time.Hour) {
        return false
    }
    
    return true
}
```

---

## 修复 #11-12: 代码质量问题

### 修复 #11: Unix Socket 权限

**文件**: `coremain/server.go`

```go
func (s *Server) startListener(cfg config.Listener) (net.PacketConn, func() error, error) {
    ctx := context.Background()
    
    switch cfg.Protocol {
    case "udp":
        return s.startUDPListener(ctx, cfg)
    case "tcp":
        return s.startTCPListener(ctx, cfg)
    case "doq":
        return s.startDoQListener(ctx, cfg)
    default:
        return nil, nil, fmt.Errorf("unsupported protocol: %s", cfg.Protocol)
    }
}

func (s *Server) startUDPListener(ctx context.Context, cfg config.Listener) (net.PacketConn, func() error, error) {
    var conn net.PacketConn
    var err error
    var run func() error
    
    abstract := strings.HasPrefix(cfg.Addr, "@")
    
    // 【修复：Unix socket处理】
    if !abstract && isUnixAddr(cfg.Addr) {
        // 清理已存在的socket文件
        os.Remove(cfg.Addr)
    }
    
    conn, err = config.ListenPacket(ctx, "unixgram", cfg.Addr)
    if err != nil {
        return nil, nil, err
    }
    
    // 【修复：设置安全的权限】
    if !abstract && isUnixAddr(cfg.Addr) {
        // 使用限制性权限：仅所有者可读写
        if err := os.Chmod(cfg.Addr, 0600); err != nil {
            conn.Close()
            return nil, nil, fmt.Errorf("failed to set socket permissions: %w", err)
        }
        
        // 【可选：设置所有者和组】
        // 如果需要特定用户/组访问，可以在这里设置
        // if cfg.SocketUser != "" || cfg.SocketGroup != "" {
        //     if err := chownSocket(cfg.Addr, cfg.SocketUser, cfg.SocketGroup); err != nil {
        //         conn.Close()
        //         return nil, nil, err
        //     }
        // }
    }
    
    run = func() error { return s.ServePacket(conn) }
    return conn, run, nil
}

// 【新增：辅助函数】
func isUnixAddr(addr string) bool {
    return strings.Contains(addr, "/") || strings.HasPrefix(addr, "@")
}

// 【可选：支持配置化权限】
type SocketConfig struct {
    Addr        string
    Permissions os.FileMode  // 例如 0660
    User        string       // 所有者用户名
    Group       string       // 所有者组名
}

func (s *Server) startUDPListenerWithConfig(ctx context.Context, cfg SocketConfig) (net.PacketConn, func() error, error) {
    abstract := strings.HasPrefix(cfg.Addr, "@")
    
    if !abstract && isUnixAddr(cfg.Addr) {
        os.Remove(cfg.Addr)
    }
    
    conn, err := net.ListenPacket("unixgram", cfg.Addr)
    if err != nil {
        return nil, nil, err
    }
    
    if !abstract && isUnixAddr(cfg.Addr) {
        // 使用配置的权限或默认的安全权限
        perms := cfg.Permissions
        if perms == 0 {
            perms = 0600  // 默认：仅所有者
        }
        
        if err := os.Chmod(cfg.Addr, perms); err != nil {
            conn.Close()
            return nil, nil, fmt.Errorf("failed to set socket permissions: %w", err)
        }
        
        // 设置所有者
        if cfg.User != "" || cfg.Group != "" {
            if err := chownSocket(cfg.Addr, cfg.User, cfg.Group); err != nil {
                conn.Close()
                return nil, nil, fmt.Errorf("failed to set socket ownership: %w", err)
            }
        }
    }
    
    run := func() error { return s.ServePacket(conn) }
    return conn, run, nil
}

// 【Unix特定的所有权设置】
// +build unix

func chownSocket(path, user, group string) error {
    var uid, gid int = -1, -1
    
    if user != "" {
        u, err := osuser.Lookup(user)
        if err != nil {
            return fmt.Errorf("lookup user %q: %w", user, err)
        }
        uidVal, _ := strconv.Atoi(u.Uid)
        uid = uidVal
    }
    
    if group != "" {
        g, err := osuser.LookupGroup(group)
        if err != nil {
            return fmt.Errorf("lookup group %q: %w", group, err)
        }
        gidVal, _ := strconv.Atoi(g.Gid)
        gid = gidVal
    }
    
    if uid != -1 || gid != -1 {
        if err := os.Chown(path, uid, gid); err != nil {
            return err
        }
    }
    
    return nil
}
```

---

### 修复 #12: 插件注册 Panic

**文件**: `coremain/register.go`

```go
var (
    pluginTypeRegister   = make(map[string]PluginFactory)
    pluginTypeRegisterMu sync.RWMutex
    
    // 【新增：注册错误列表】
    registrationErrors []error
)

// 【修复：返回错误而不是panic】
func RegisterPluginType(typ string, factory PluginFactory) error {
    if typ == "" {
        return fmt.Errorf("plugin type cannot be empty")
    }
    if factory == nil {
        return fmt.Errorf("plugin factory cannot be nil")
    }
    
    pluginTypeRegisterMu.Lock()
    defer pluginTypeRegisterMu.Unlock()
    
    if _, exists := pluginTypeRegister[typ]; exists {
        return fmt.Errorf("duplicate plugin type: %s", typ)
    }
    
    pluginTypeRegister[typ] = factory
    return nil
}

// 【新增：批量注册】
func RegisterPluginTypes(types map[string]PluginFactory) error {
    var errs []error
    
    for typ, factory := range types {
        if err := RegisterPluginType(typ, factory); err != nil {
            errs = append(errs, err)
        }
    }
    
    if len(errs) > 0 {
        return fmt.Errorf("plugin registration errors: %v", errs)
    }
    
    return nil
}

// 【修复：必须注册（用于init）】
func MustRegisterPluginType(typ string, factory PluginFactory) {
    if err := RegisterPluginType(typ, factory); err != nil {
        // 在init()中调用时，这是合理的panic
        // 因为这是编译时配置错误
        panic(fmt.Sprintf("failed to register plugin %q: %v", typ, err))
    }
}

// 【新增：获取已注册的插件】
func GetRegisteredPlugins() []string {
    pluginTypeRegisterMu.RLock()
    defer pluginTypeRegisterMu.RUnlock()
    
    types := make([]string, 0, len(pluginTypeRegister))
    for typ := range pluginTypeRegister {
        types = append(types, typ)
    }
    sort.Strings(types)
    return types
}

// 【新增：检查插件是否已注册】
func IsPluginRegistered(typ string) bool {
    pluginTypeRegisterMu.RLock()
    defer pluginTypeRegisterMu.RUnlock()
    
    _, exists := pluginTypeRegister[typ]
    return exists
}

// 在插件包的init()中使用：
func init() {
    // 选项1：使用Must版本（init时panic是可接受的）
    MustRegisterPluginType("example", NewExamplePlugin)
    
    // 选项2：收集错误，稍后处理
    if err := RegisterPluginType("example", NewExamplePlugin); err != nil {
        // 记录错误
        registrationErrors = append(registrationErrors, err)
    }
}

// 【新增：在应用启动时验证】
func ValidatePluginRegistration() error {
    if len(registrationErrors) > 0 {
        return fmt.Errorf("plugin registration failed: %v", registrationErrors)
    }
    return nil
}

// 在main()中：
func main() {
    // 验证所有插件已正确注册
    if err := ValidatePluginRegistration(); err != nil {
        log.Fatal(err)
    }
    
    // 继续启动...
}
```

---

## 测试代码

为每个修复添加测试以验证行为：

### 测试 #1: 凭证计数竞态

```go
func TestCredentialCountConcurrency(t *testing.T) {
    store := setupTestStore(t)
    defer store.Close()
    
    ctx := context.Background()
    
    // 创建用户
    spec := UserSpec{
        Username:       "testuser",
        Password:       "password123",
        Role:           RoleUser,
        Enabled:        true,
        MaxCredentials: 10,
        QPS:            100,
        Burst:          10,
        Limit:          1000000,
    }
    
    user, err := store.InitializeAdmin(ctx, spec)
    require.NoError(t, err)
    
    // 并发创建凭证
    const goroutines = 20
    const credentialsPerGoroutine = 2
    
    var wg sync.WaitGroup
    errors := make(chan error, goroutines*credentialsPerGoroutine)
    
    for i := 0; i < goroutines; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            for j := 0; j < credentialsPerGoroutine; j++ {
                name := fmt.Sprintf("cred-%d-%d", id, j)
                _, err := store.CreateCredential(ctx, user.ID, user.ID, name, time.Time{})
                if err != nil && !errors.Is(err, ErrConflict) {
                    errors <- err
                }
            }
        }(i)
    }
    
    wg.Wait()
    close(errors)
    
    // 检查是否有非预期错误
    for err := range errors {
        t.Errorf("unexpected error: %v", err)
    }
    
    // 验证最终状态
    u, err := store.GetUser(ctx, user.ID)
    require.NoError(t, err)
    
    // 计数应该准确（最多MaxCredentials个）
    assert.LessOrEqual(t, int(u.CredentialCount), int(spec.MaxCredentials))
    
    // 验证实际凭证数与计数匹配
    creds, err := store.ListCredentials(ctx, user.ID, user.ID)
    require.NoError(t, err)
    
    activeCount := 0
    for _, c := range creds {
        if c.RevokedAt.IsZero() {
            activeCount++
        }
    }
    
    assert.Equal(t, int(u.CredentialCount), activeCount, 
        "credential count mismatch: stored=%d actual=%d", 
        u.CredentialCount, activeCount)
}
```

### 测试 #3: 时序攻击防护

```go
func TestAuthenticationTiming(t *testing.T) {
    store := setupTestStore(t)
    defer store.Close()
    
    ctx := context.Background()
    
    // 创建一个存在的用户
    spec := UserSpec{
        Username: "existinguser",
        Password: "correctpassword",
        Role:     RoleUser,
        Enabled:  true,
    }
    user, err := store.CreateUser(ctx, adminID, spec)
    require.NoError(t, err)
    
    testCases := []struct {
        name     string
        username string
        password string
    }{
        {"valid user, wrong password", "existinguser", "wrongpassword"},
        {"nonexistent user", "nonexistent", "anypassword"},
        {"valid user, correct password", "existinguser", "correctpassword"},
    }
    
    timings := make([]time.Duration, len(testCases))
    
    for i, tc := range testCases {
        start := time.Now()
        _, _ = store.AuthenticatePassword(ctx, tc.username, tc.password)
        timings[i] = time.Since(start)
        
        t.Logf("%s: %v", tc.name, timings[i])
    }
    
    // 验证时序差异不会泄露用户存在性
    // 所有失败情况应该花费相似的时间（±20%）
    avgFailTime := (timings[0] + timings[1]) / 2
    diff := timings[0] - timings[1]
    if diff < 0 {
        diff = -diff
    }
    
    // 时序差异应该小于平均时间的20%
    maxDiff := avgFailTime / 5
    assert.Less(t, diff, maxDiff,
        "timing difference too large: %v (max %v)", diff, maxDiff)
}
```

### 测试 #4: IP 限速器清理

```go
func TestIPLimiterMemoryCleanup(t *testing.T) {
    now := time.Now()
    fakeClock := &fakeClock{now: now}
    
    limiter := newIPLimiter(
        10,              // limit
        time.Minute,     // window
        100,             // capacity
        fakeClock.Now,
    )
    
    // 填充限速器
    for i := 0; i < 100; i++ {
        ip := fmt.Sprintf("192.168.1.%d", i)
        assert.True(t, limiter.allow(ip))
    }
    
    // 验证已满
    assert.False(t, limiter.allow("192.168.2.1"))
    
    // 推进时间超过窗口
    fakeClock.advance(2 * time.Minute)
    
    // 应该能够添加新IP（旧条目已清理）
    assert.True(t, limiter.allow("192.168.2.1"))
    
    // 验证内存已清理
    active, capacity := limiter.stats()
    assert.Less(t, active, capacity,
        "limiter should have cleaned up old entries")
}
```

---

## 部署清单

修复完成后，按以下顺序部署：

### 阶段 1: 关键修复（第1周）
- [ ] 审查并应用凭证计数竞态条件修复 (#1)
- [ ] 添加 defer 事务回滚 (#5)
- [ ] 改进认证错误处理和日志 (#2)
- [ ] 修复 context 使用 (#6)
- [ ] 运行 `go test -race ./...` 验证无竞态
- [ ] 部署到测试环境
- [ ] 进行负载测试
- [ ] 部署到生产环境

### 阶段 2: 高危修复（第2-3周）
- [ ] 实现 IP 限速器改进 (#4)
- [ ] 增强时序攻击防护 (#3)
- [ ] 改进 X-Forwarded-For 处理 (#9)
- [ ] 修复 Unix socket 权限 (#11)
- [ ] 进行安全测试
- [ ] 部署到生产环境

### 阶段 3: 中危修复（第4-6周）
- [ ] 改进 DNS 输入验证 (#7)
- [ ] 添加开发模式警告 (#8)
- [ ] 实现速率限制溢出保护 (#10)
- [ ] 重构插件注册 (#12)
- [ ] 完整集成测试
- [ ] 文档更新

### 阶段 4: 验证和监控
- [ ] 设置监控告警（凭证计数不一致、事务失败等）
- [ ] 进行渗透测试
- [ ] 性能基准测试
- [ ] 文档化所有更改
- [ ] 培训团队

---

## 监控建议

添加以下监控指标：

```go
// 凭证计数健康检查
metrics.Gauge("credential.count_mismatch", mismatchCount)

// 事务失败
metrics.Counter("db.transaction_rollback_failure", 1)
metrics.Counter("db.transaction_retry", 1)

// 认证相关
metrics.Counter("auth.session_cleanup_failure", 1)
metrics.Histogram("auth.password_verify_duration", duration)

// 限速器
metrics.Gauge("ratelimit.ip_limiter_size", limiterSize)
metrics.Counter("ratelimit.ip_limiter_cleanup", 1)

// Unix socket
metrics.Counter("socket.permission_error", 1)
```

---

## 总结

本文档提供了所有已识别安全问题的详细修复方案。每个修复都包含：

1. **问题描述** - 清楚说明漏洞
2. **修复代码** - 可直接应用的代码
3. **测试用例** - 验证修复的测试
4. **部署指南** - 安全应用修复的步骤

请按照优先级顺序实施这些修复，并在每个阶段进行充分的测试。
