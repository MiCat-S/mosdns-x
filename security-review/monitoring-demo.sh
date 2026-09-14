#!/bin/bash

# mosdns-x 监控系统演示脚本
# 用途: 生成模拟日志数据，演示监控脚本功能
# 使用: ./monitoring-demo.sh

set -e

DEMO_LOG="/tmp/mosdns-demo.log"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 颜色
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}mosdns-x 监控系统演示${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# 清理旧日志
rm -f "$DEMO_LOG"

echo -e "${GREEN}1. 生成模拟日志数据...${NC}"

# 获取当前时间戳
get_timestamp() {
    date -u '+%Y-%m-%dT%H:%M:%S.000Z'
}

# 生成健康日志
generate_health_logs() {
    local sessions=$1
    local db_usage=$2
    local limiter_size=$3
    
    cat >> "$DEMO_LOG" << LOGS
{"level":"info","ts":"$(get_timestamp)","msg":"control_store_health","active_sessions":${sessions},"boltdb_open_tx":0,"boltdb_pending_pages":0,"checked_at":"$(get_timestamp)"}
{"level":"info","ts":"$(get_timestamp)","msg":"mysql_store_health","open_connections":32,"in_use":${db_usage},"idle":$((32-db_usage)),"utilization_pct":$(awk "BEGIN {printf \"%.1f\", ${db_usage}*100/32}"),"wait_count":0,"wait_duration":0,"checked_at":"$(get_timestamp)"}
{"level":"info","ts":"$(get_timestamp)","msg":"rate_limiter_cleanup","evicted":5,"remaining":${limiter_size},"capacity":4096,"utilization_pct":$(awk "BEGIN {printf \"%.1f\", ${limiter_size}*100/4096}"),"metric":"rate_limiter_memory"}
LOGS
}

echo "   • 正常运行状态（健康评分: 100）"
generate_health_logs 15 10 234
sleep 0.5

echo "   • 中等负载（健康评分: 85-90）"
generate_health_logs 45 25 1200
cat >> "$DEMO_LOG" << 'LOGS'
{"level":"info","ts":"2026-09-14T14:32:00.000Z","msg":"session_cleanup_success","session_id":"sess_abc123","cleanup_duration":0.002}
LOGS
sleep 0.5

echo "   • 出现问题（健康评分: 60-70）"
generate_health_logs 78 28 3500
cat >> "$DEMO_LOG" << 'LOGS'
{"level":"error","ts":"2026-09-14T14:33:00.000Z","msg":"session_cleanup_failed","user_id":"usr_001","session_id":"sess_xyz789","error":"context deadline exceeded","cleanup_duration":5.002,"trigger":"auth_failure","metric":"session_leak_risk"}
{"level":"warn","ts":"2026-09-14T14:33:05.000Z","msg":"mysql_connection_pool_high_utilization","utilization_pct":87.5,"recommendation":"consider increasing max_open_connections"}
LOGS
sleep 0.5

echo "   • 严重问题（健康评分: < 50）"
generate_health_logs 120 30 3900
cat >> "$DEMO_LOG" << 'LOGS'
{"level":"error","ts":"2026-09-14T14:34:00.000Z","msg":"session_cleanup_failed","user_id":"usr_002","session_id":"sess_aaa111","error":"database locked","cleanup_duration":10.001,"trigger":"auth_failure","metric":"session_leak_risk"}
{"level":"error","ts":"2026-09-14T14:34:02.000Z","msg":"session_cleanup_failed","user_id":"usr_003","session_id":"sess_bbb222","error":"connection refused","cleanup_duration":5.123,"trigger":"timeout","metric":"session_leak_risk"}
{"level":"warn","ts":"2026-09-14T14:34:05.000Z","msg":"transaction_rollback_failed","error":"connection reset","metric":"db_connection_leak_risk"}
{"level":"error","ts":"2026-09-14T14:34:10.000Z","msg":"credential_count_mismatch_detected","user_id":"usr_004","expected":5,"actual":4,"metric":"data_integrity_issue"}
{"level":"warn","ts":"2026-09-14T14:34:15.000Z","msg":"rate_limiter_near_capacity","entries":3900,"capacity":4096,"metric":"rate_limiter_saturation"}
LOGS

echo ""
echo -e "${GREEN}2. 运行监控脚本分析...${NC}"
echo ""

# 运行监控脚本
if [ -f "$SCRIPT_DIR/monitoring-script.sh" ]; then
    bash "$SCRIPT_DIR/monitoring-script.sh" "$DEMO_LOG"
else
    echo "错误: 找不到 monitoring-script.sh"
    echo "请确保在 security-review 目录中运行此脚本"
    exit 1
fi

echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}演示完成！${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "📝 演示日志已保存到: $DEMO_LOG"
echo "🔍 你可以手动检查: cat $DEMO_LOG | jq"
echo ""
echo "💡 下一步:"
echo "   1. 查看实际日志: tail -f /var/log/mosdns/mosdns.log"
echo "   2. 实施监控: 阅读 monitoring/MONITORING_QUICKSTART.md"
echo "   3. 运行实际监控: ./monitoring-script.sh /var/log/mosdns/mosdns.log"
echo ""
