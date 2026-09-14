#!/bin/bash

# mosdns-x 安全监控脚本
# 用途: 实时监控已修复的安全问题
# 使用: ./monitoring-script.sh [log_file] [watch]

set -e

# 配置
LOG_FILE="${1:-/var/log/mosdns/mosdns.log}"
WATCH_MODE="${2:-}"
WINDOW_MINUTES=5
CRITICAL_THRESHOLD=10
WARNING_THRESHOLD=5

# 颜色定义
RED='\033[0;31m'
YELLOW='\033[1;33m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 辅助函数
print_header() {
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

print_ok() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_warn() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_info() {
    echo -e "ℹ️  $1"
}

# 检查日志文件存在
check_log_file() {
    if [ ! -f "$LOG_FILE" ]; then
        print_error "日志文件不存在: $LOG_FILE"
        echo ""
        echo "提示:"
        echo "  1. 检查 mosdns 是否正在运行: systemctl status mosdns"
        echo "  2. 检查日志配置: cat /etc/mosdns/config.yaml | grep log"
        echo "  3. 使用自定义路径: $0 /path/to/mosdns.log"
        exit 1
    fi
}

# 生成报告
generate_report() {
    clear
    print_header "mosdns-x 安全监控报告"
    echo "📅 生成时间: $(date '+%Y-%m-%d %H:%M:%S')"
    echo "📁 日志文件: $LOG_FILE"
    echo "⏱️  分析窗口: 最近 ${WINDOW_MINUTES} 分钟"
    echo ""

    # 计算时间戳（5分钟前）
    if [[ "$OSTYPE" == "darwin"* ]]; then
        # macOS
        TIME_THRESHOLD=$(date -u -v-${WINDOW_MINUTES}M '+%Y-%m-%dT%H:%M:%S')
    else
        # Linux
        TIME_THRESHOLD=$(date -u -d "${WINDOW_MINUTES} minutes ago" '+%Y-%m-%dT%H:%M:%S')
    fi

    # 1. 会话清理监控（修复 #2）
    print_header "1. 会话清理状态（修复 #2: 错误处理）"

    cleanup_failures=$(grep "session_cleanup_failed" "$LOG_FILE" 2>/dev/null | \
        awk -v threshold="$TIME_THRESHOLD" '$0 > threshold' | wc -l | tr -d ' ')

    cleanup_success=$(grep "session_cleanup_success" "$LOG_FILE" 2>/dev/null | \
        awk -v threshold="$TIME_THRESHOLD" '$0 > threshold' | wc -l | tr -d ' ')

    total_cleanup=$((cleanup_failures + cleanup_success))

    if [ "$total_cleanup" -eq 0 ]; then
        print_info "无会话清理操作（可能无活动）"
    else
        if [ "$cleanup_failures" -eq 0 ]; then
            print_ok "成功: $cleanup_success/$total_cleanup (100%)"
        elif [ "$cleanup_failures" -lt "$WARNING_THRESHOLD" ]; then
            print_warn "失败: $cleanup_failures/$total_cleanup ($(awk "BEGIN {printf \"%.1f\", $cleanup_failures*100/$total_cleanup}")%)"
            echo "   → 存在会话泄漏风险，建议检查日志详情"
        else
            print_error "失败: $cleanup_failures/$total_cleanup ($(awk "BEGIN {printf \"%.1f\", $cleanup_failures*100/$total_cleanup}")%)"
            echo "   → 严重：大量会话清理失败！"
            # 显示最近的失败
            echo ""
            echo "   最近失败详情:"
            grep "session_cleanup_failed" "$LOG_FILE" | tail -3 | while read -r line; do
                echo "   $(echo "$line" | grep -oP '(?<=session_id":").*?(?=")' || echo 'N/A')"
            done
        fi
    fi
    echo ""

    # 2. 数据库连接池（修复 #5）
    print_header "2. 数据库连接池（修复 #5: 事务回滚）"

    # MySQL 连接池统计
    latest_mysql=$(grep "mysql_store_health" "$LOG_FILE" | tail -1)
    if [ -n "$latest_mysql" ]; then
        open_conns=$(echo "$latest_mysql" | grep -oP 'open_connections":\K[0-9]+' || echo "0")
        in_use=$(echo "$latest_mysql" | grep -oP 'in_use":\K[0-9]+' || echo "0")
        idle=$(echo "$latest_mysql" | grep -oP 'idle":\K[0-9]+' || echo "0")
        utilization=$(echo "$latest_mysql" | grep -oP 'utilization_pct":\K[0-9.]+' || echo "0")
        wait_count=$(echo "$latest_mysql" | grep -oP 'wait_count":\K[0-9]+' || echo "0")

        echo "MySQL 连接池:"
        if (( $(echo "$utilization < 60" | bc -l) )); then
            print_ok "使用率: ${utilization}% (健康)"
        elif (( $(echo "$utilization < 80" | bc -l) )); then
            print_warn "使用率: ${utilization}% (中等)"
        else
            print_error "使用率: ${utilization}% (高负载)"
        fi

        echo "   • 打开连接: $open_conns"
        echo "   • 使用中: $in_use"
        echo "   • 空闲: $idle"

        if [ "$wait_count" -gt 100 ]; then
            print_warn "等待次数: $wait_count (连接不足)"
        else
            echo "   • 等待次数: $wait_count"
        fi
    else
        print_info "未找到 MySQL 统计（可能使用 BoltDB）"
    fi

    # 事务回滚失败
    rollback_failures=$(grep "transaction_rollback_failed" "$LOG_FILE" 2>/dev/null | \
        awk -v threshold="$TIME_THRESHOLD" '$0 > threshold' | wc -l | tr -d ' ')

    echo ""
    if [ "$rollback_failures" -eq 0 ]; then
        print_ok "事务回滚: 无失败"
    else
        print_error "事务回滚失败: $rollback_failures 次（连接泄漏风险）"
    fi

    # BoltDB 统计
    latest_boltdb=$(grep "control_store_health" "$LOG_FILE" | tail -1)
    if [ -n "$latest_boltdb" ]; then
        echo ""
        echo "BoltDB 统计:"
        active_sessions=$(echo "$latest_boltdb" | grep -oP 'active_sessions":\K[0-9]+' || echo "0")
        open_tx=$(echo "$latest_boltdb" | grep -oP 'boltdb_open_tx":\K[0-9]+' || echo "0")

        echo "   • 活跃会话: $active_sessions"
        if [ "$open_tx" -eq 0 ]; then
            print_ok "打开事务: $open_tx (正常)"
        else
            print_warn "打开事务: $open_tx (可能存在长事务)"
        fi
    fi
    echo ""

    # 3. 限速器内存（修复 #4）
    print_header "3. 限速器内存使用（修复 #4: 内存增长）"

    latest_limiter=$(grep "rate_limiter" "$LOG_FILE" | tail -1)
    if [ -n "$latest_limiter" ]; then
        entries=$(echo "$latest_limiter" | grep -oP 'remaining":\K[0-9]+' || echo "0")
        capacity=$(echo "$latest_limiter" | grep -oP 'capacity":\K[0-9]+' || echo "4096")
        utilization_pct=$(awk "BEGIN {printf \"%.1f\", $entries*100/$capacity}")

        if (( $(echo "$utilization_pct < 50" | bc -l) )); then
            print_ok "当前条目: $entries/$capacity (${utilization_pct}%)"
        elif (( $(echo "$utilization_pct < 80" | bc -l) )); then
            print_warn "当前条目: $entries/$capacity (${utilization_pct}%)"
        else
            print_error "当前条目: $entries/$capacity (${utilization_pct}% - 接近容量上限)"
        fi

        # 清理统计
        cleanup_count=$(grep "rate_limiter_cleanup" "$LOG_FILE" 2>/dev/null | \
            awk -v threshold="$TIME_THRESHOLD" '$0 > threshold' | wc -l | tr -d ' ')

        if [ "$cleanup_count" -gt 0 ]; then
            evicted=$(grep "rate_limiter_cleanup" "$LOG_FILE" | tail -1 | \
                grep -oP 'evicted":\K[0-9]+' || echo "0")
            print_info "最近清理: 驱逐 $evicted 个过期条目"
        fi
    else
        print_info "暂无限速器数据（可能尚未触发）"
    fi
    echo ""

    # 4. 凭证管理（修复 #1）
    print_header "4. 凭证计数一致性（修复 #1: 竞态条件）"

    mismatch_count=$(grep "credential_count_mismatch" "$LOG_FILE" 2>/dev/null | \
        awk -v threshold="$TIME_THRESHOLD" '$0 > threshold' | wc -l | tr -d ' ')

    if [ "$mismatch_count" -eq 0 ]; then
        print_ok "无凭证计数不匹配"
    else
        print_error "发现 $mismatch_count 次凭证计数不匹配"
        echo "   → 数据一致性问题，需立即检查"
    fi
    echo ""

    # 5. 整体健康评分
    print_header "5. 整体健康评分"

    health_score=100
    issues=()

    # 扣分逻辑
    if [ "$cleanup_failures" -gt "$CRITICAL_THRESHOLD" ]; then
        health_score=$((health_score - 30))
        issues+=("会话清理大量失败")
    elif [ "$cleanup_failures" -gt "$WARNING_THRESHOLD" ]; then
        health_score=$((health_score - 15))
        issues+=("会话清理部分失败")
    fi

    if [ "$rollback_failures" -gt 0 ]; then
        health_score=$((health_score - 20))
        issues+=("事务回滚失败")
    fi

    if [ -n "$utilization" ] && (( $(echo "$utilization > 80" | bc -l) )); then
        health_score=$((health_score - 15))
        issues+=("数据库连接池高使用率")
    fi

    if [ "$mismatch_count" -gt 0 ]; then
        health_score=$((health_score - 25))
        issues+=("凭证计数不一致")
    fi

    # 显示评分
    if [ "$health_score" -ge 90 ]; then
        print_ok "健康评分: ${health_score}/100 (优秀)"
    elif [ "$health_score" -ge 70 ]; then
        print_warn "健康评分: ${health_score}/100 (良好)"
    elif [ "$health_score" -ge 50 ]; then
        print_warn "健康评分: ${health_score}/100 (需要关注)"
    else
        print_error "健康评分: ${health_score}/100 (需要立即处理)"
    fi

    if [ ${#issues[@]} -gt 0 ]; then
        echo ""
        echo "发现的问题:"
        for issue in "${issues[@]}"; do
            echo "   • $issue"
        done
    fi
    echo ""

    # 6. 快速诊断建议
    if [ "$health_score" -lt 90 ]; then
        print_header "6. 诊断建议"

        if [ "$cleanup_failures" -gt 0 ]; then
            echo "🔍 会话清理失败:"
            echo "   查看详情: grep 'session_cleanup_failed' $LOG_FILE | tail -10"
            echo "   检查上下文: grep -B5 -A5 'session_cleanup_failed' $LOG_FILE | tail -20"
            echo ""
        fi

        if [ "$rollback_failures" -gt 0 ]; then
            echo "🔍 事务回滚失败:"
            echo "   查看详情: grep 'transaction_rollback_failed' $LOG_FILE | tail -10"
            echo "   检查连接: SELECT * FROM INFORMATION_SCHEMA.PROCESSLIST;"
            echo ""
        fi

        if [ -n "$utilization" ] && (( $(echo "$utilization > 80" | bc -l) )); then
            echo "🔍 数据库连接池高负载:"
            echo "   当前配置: grep -A5 'mysql' /etc/mosdns/config.yaml"
            echo "   建议增加: max_open_connections: 64"
            echo ""
        fi

        if [ "$mismatch_count" -gt 0 ]; then
            echo "🔍 凭证计数不匹配:"
            echo "   这是严重问题，请立即："
            echo "   1. 检查并发测试: go test -race ./internal/control/"
            echo "   2. 验证数据一致性"
            echo "   3. 考虑数据库修复"
            echo ""
        fi
    fi

    print_header "报告结束"
    echo "💡 提示: 运行 '$0 $LOG_FILE watch' 进入监控模式"
    echo ""
}

# 主函数
main() {
    check_log_file

    if [ "$WATCH_MODE" = "watch" ]; then
        echo "进入实时监控模式（Ctrl+C 退出）"
        echo "刷新间隔: 60 秒"
        echo ""
        while true; do
            generate_report
            sleep 60
        done
    else
        generate_report
    fi
}

# 运行
main
