# 监控指标文档索引

> 当前入口：[实际监控说明](../../docs/monitoring.md)。以下为原始方案索引；请勿将占位代码或示例分数当作当前服务状态。

本目录包含 mosdns-x 安全监控的完整实施指南。

---

## 📚 文档列表

### 1. [MONITORING_QUICKSTART.md](MONITORING_QUICKSTART.md) ⭐ **从这里开始**
**预计时间**: 1-2 小时  
**难度**: ⭐⭐ (中等)

快速启动指南，使用轻量级日志监控：
- ✅ 无需额外基础设施
- ✅ 最小代码修改
- ✅ 1-2小时完成
- ✅ 立即可用

**适合**: 快速验证修复效果、测试环境、小规模部署

---

### 2. [MONITORING_IMPLEMENTATION.md](MONITORING_IMPLEMENTATION.md) 📊 完整方案
**预计时间**: 2-4 天  
**难度**: ⭐⭐⭐ (中高)

生产级监控完整实施方案：
- 📊 **方案 A**: Prometheus + Grafana（推荐生产环境）
- 📝 **方案 B**: 内部日志监控（快速实施）
- 🏥 **方案 C**: 健康检查端点（容器化环境）

**包含内容**:
- 详细代码示例（`internal/control/metrics.go`）
- Grafana 仪表板配置
- Prometheus 告警规则
- 故障排查指南

**适合**: 生产环境、大规模部署、需要可视化和告警

---

### 3. [monitoring-script.sh](monitoring-script.sh) 🔧 监控脚本
**使用**: `./monitoring-script.sh [log_file] [watch]`

自动化监控脚本，提供：
- ✅ 实时健康检查
- ✅ 5大安全指标监控
- ✅ 健康评分（0-100）
- ✅ 诊断建议
- ✅ 彩色输出

**示例**:
```bash
# 单次报告
./monitoring-script.sh /var/log/mosdns/mosdns.log

# 实时监控模式（每60秒刷新）
./monitoring-script.sh /var/log/mosdns/mosdns.log watch
```

---

## 🎯 监控的5大核心指标

根据已完成的安全修复，重点监控：

| # | 修复问题 | 监控指标 | 关键阈值 |
|---|---------|---------|---------|
| 1 | 凭证计数竞态 | `credential_count_mismatch` | 0 |
| 2 | 会话清理错误 | `session_cleanup_errors` | < 1% |
| 5 | 事务回滚 | `db_connection_pool_usage` | < 80% |
| 4 | 限速器内存 | `rate_limiter_entries` | < 4096 |
| - | 整体健康 | `active_sessions`, `open_tx` | 变化趋势 |

---

## 🚀 快速开始流程

### 第1步：选择方案（5分钟）

**如果你想要**：
- **快速验证修复** → 使用 [MONITORING_QUICKSTART.md](MONITORING_QUICKSTART.md)
- **生产级监控** → 使用 [MONITORING_IMPLEMENTATION.md](MONITORING_IMPLEMENTATION.md)
- **自动化检查** → 直接运行 [monitoring-script.sh](monitoring-script.sh)

### 第2步：实施（1小时 - 4天）

根据选择的方案按步骤实施。

### 第3步：验证（10分钟）

```bash
# 检查日志输出
tail -f /var/log/mosdns/mosdns.log | grep "health"

# 运行监控脚本
./security-review/monitoring-script.sh /var/log/mosdns/mosdns.log

# 检查 Prometheus 指标（如果使用方案 A）
curl http://localhost:8080/metrics | grep mosdns_control
```

---

## 📊 方案对比

| 特性 | 快速启动<br/>(QUICKSTART) | 完整方案 A<br/>(Prometheus) | 监控脚本<br/>(Script) |
|------|--------------------------|---------------------------|---------------------|
| **实施时间** | 1-2 小时 | 2-4 天 | 5 分钟 |
| **代码修改** | 中等 | 较多 | 无 |
| **基础设施** | 无需 | Prometheus + Grafana | 无需 |
| **可视化** | ❌ | ✅ Grafana 仪表板 | ✅ 终端输出 |
| **告警** | ❌ | ✅ Prometheus 告警 | ✅ 彩色标记 |
| **持久化** | 日志文件 | 时间序列数据库 | 无 |
| **实时性** | 60秒 | 15秒 | 60秒 |
| **适用环境** | 测试、小规模 | 生产环境 | 所有环境 |
| **学习曲线** | 低 | 中 | 极低 |

---

## 💡 推荐实施路径

### 路径 1：渐进式（推荐）
1. **今天**: 运行 `monitoring-script.sh` 了解当前状态
2. **第1周**: 实施快速启动方案（1-2小时）
3. **第2-3周**: 观察日志，确认修复效果
4. **第4周**: 升级到完整 Prometheus 方案

### 路径 2：快速验证
1. **立即**: 运行 `monitoring-script.sh`
2. **今天**: 实施快速启动方案
3. **本周**: 观察24小时验证修复
4. **结束**: 根据需要决定是否升级

### 路径 3：直接生产级
1. **第1天**: 阅读完整方案文档
2. **第2-3天**: 实施 Prometheus 集成
3. **第4天**: 配置 Grafana 和告警
4. **持续**: 调优和维护

---

## 🔍 故障排查

### 问题：监控脚本无输出

```bash
# 检查日志文件
ls -lh /var/log/mosdns/mosdns.log

# 检查 mosdns 是否运行
ps aux | grep mosdns

# 使用自定义路径
./monitoring-script.sh /custom/path/mosdns.log
```

### 问题：没有健康检查日志

**原因**: 可能还未实施快速启动方案

**解决**: 
1. 阅读 [MONITORING_QUICKSTART.md](MONITORING_QUICKSTART.md)
2. 添加健康检查代码
3. 重新编译和部署

### 问题：Prometheus 无数据

**原因**: 可能还未实施完整方案

**解决**:
1. 阅读 [MONITORING_IMPLEMENTATION.md](MONITORING_IMPLEMENTATION.md) 方案 A
2. 添加 Prometheus 指标收集器
3. 验证 `/metrics` 端点

---

## 📈 成功指标

监控系统成功运行的标志：

### 技术指标
- ✅ 健康检查日志每60秒出现一次
- ✅ 监控脚本健康评分 > 90
- ✅ 无会话清理失败
- ✅ 数据库连接使用率 < 80%
- ✅ 限速器内存稳定

### 业务指标
- ✅ 服务可用性 > 99.9%
- ✅ 零凭证计数不匹配
- ✅ 无连接泄漏
- ✅ 内存使用稳定

---

## 📞 获取帮助

### 常见问题
1. **日志在哪里?**
   - BoltDB: 检查 mosdns 配置文件
   - MySQL: 同上
   - 默认: `/var/log/mosdns/mosdns.log`

2. **需要重启服务吗?**
   - 快速启动方案: 是（添加代码后）
   - 监控脚本: 否（直接运行）
   - 完整方案: 是（添加指标后）

3. **影响性能吗?**
   - 日志监控: 可忽略（< 0.1% CPU）
   - Prometheus: 极小（< 1% CPU）
   - 健康检查: 每60秒一次，可忽略

### 进一步阅读
- 主文档: [../SECURITY_README.md](../SECURITY_README.md)
- 修复指南: [../SECURITY_FIXES.md](../SECURITY_FIXES.md)
- 审查报告: [../SECURITY_REVIEW_CN.md](../SECURITY_REVIEW_CN.md)

---

## 🎉 开始监控

```bash
# 1. 快速检查当前状态（5分钟）
cd security-review
./monitoring-script.sh /var/log/mosdns/mosdns.log

# 2. 实施基础监控（1-2小时）
# 阅读 MONITORING_QUICKSTART.md 并按步骤操作

# 3. （可选）升级到 Prometheus（2-4天）
# 阅读 MONITORING_IMPLEMENTATION.md 方案 A
```

---

**文档创建**: 2026-09-14  
**维护**: 定期更新

祝监控顺利！ 🚀
