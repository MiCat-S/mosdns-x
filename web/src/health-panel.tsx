import { useEffect, useState } from "react";
import { message, request } from "./api";
import { Alert, Card, Metric, Spinner, fmt } from "./components";

type HealthStatus =
  | "healthy"
  | "warning"
  | "critical"
  | "unknown"
  | "not_applicable"
  | "no_samples"
  | "info";
export interface HealthReport {
  timestamp: string;
  started_at: string;
  storage_checked_at: string | null;
  storage_status: string;
  storage_error?: string;
  storage_age_seconds?: number | null;
  runtime_status?: string;
  overall_status: HealthStatus;
  overall_score: number | null;
  metrics: {
    key: string;
    label: string;
    value: number | null;
    unit: string;
    status: HealthStatus;
    reason?: string;
  }[];
  cleanup: { attempts: number; errors: number; duration_seconds_total: number };
  rate_limiters: {
    name: string;
    entries: number;
    active_entries?: number;
    capacity: number;
    rejected_total: number;
    evicted_total: number;
  }[];
  storage: {
    driver: string;
    active_sessions: number | null;
    credential_count_mismatches: number | null;
    mysql?: {
      max_open: number;
      open: number;
      in_use: number;
      idle: number;
      wait_count: number;
      wait_seconds: number;
      rollback_errors_total: number;
    };
    bbolt?: { open_read_transactions: number; pending_pages: number };
  };
}

const labels: Record<HealthStatus, string> = {
  healthy: "正常",
  warning: "警告",
  critical: "异常",
  unknown: "数据不足",
  not_applicable: "不适用",
  no_samples: "暂无样本",
  info: "历史参考",
};
const reasons: Record<string, string> = {
  no_samples: "尚无会话清理样本，不计算失败率，也不影响当前评分",
  cumulative_only: "进程累计值，仅作历史参考；近期告警使用窗口增量",
  bbolt_backend: "bbolt 不使用 MySQL 事务或连接池",
  no_materialized_count: "MySQL 按事务查询计数，没有独立计数缓存",
  unlimited_pool: "连接池未设置上限，无法计算占用比例",
  not_collected: "等待首次数据库采集",
  collection_failed: "数据库采集失败，请检查服务日志",
  scan_limit:
    "数据量超过扫描预算；可调整 control.health_scan_limit，运行统计仍独立提供",
  stale: "数据库快照已过期，等待重新采集",
  unsupported: "当前后端未提供健康检查",
  invalid_value: "采集值无效",
  invalid_capacity: "无法读取有效容量",
  runtime_unavailable: "运行统计不可用",
};
function Status({ status }: { status: HealthStatus }) {
  const color =
    status === "healthy"
      ? "ok"
      : status === "critical"
        ? "error"
        : status === "warning"
          ? "warning"
          : "off";
  return (
    <span className={`badge ${color}`}>{labels[status] ?? "数据不足"}</span>
  );
}
function value(number: number | null | undefined) {
  return number != null && Number.isFinite(number) ? fmt.num(number) : "—";
}

export function HealthPanel() {
  const [data, setData] = useState<HealthReport>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [automatic, setAutomatic] = useState(true);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let active: AbortController | undefined;
    const isVisible = () => document.visibilityState !== "hidden";
    async function load() {
      if (disposed || active || !isVisible()) return;
      clearTimeout(timer);
      const controller = new AbortController();
      active = controller;
      setLoading(true);
      try {
        const report = await request<HealthReport>("/admin/health", {
          signal: controller.signal,
        });
        if (!disposed && !controller.signal.aborted) {
          setData(report);
          setError("");
        }
      } catch (reason) {
        if (!disposed && !controller.signal.aborted) {
          setData(undefined);
          setError(message(reason) || "监控数据读取失败");
        }
      } finally {
        active = undefined;
        if (!disposed) {
          setLoading(false);
          if (isVisible()) {
            // A foreground event can arrive before an aborted request settles.
            // Resume once it releases the in-flight slot, even in manual mode.
            if (controller.signal.aborted) void load();
            else if (automatic) timer = setTimeout(() => void load(), 5000);
          }
        }
      }
    }
    const visible = () => {
      if (!isVisible()) {
        clearTimeout(timer);
        active?.abort();
      } else {
        void load();
      }
    };
    void load();
    document.addEventListener("visibilitychange", visible);
    return () => {
      disposed = true;
      clearTimeout(timer);
      active?.abort();
      document.removeEventListener("visibilitychange", visible);
    };
  }, [automatic, revision]);
  const storageFresh = data?.storage_status === "healthy";
  const runtimeFresh =
    (data?.runtime_status ?? data?.storage_status) === "healthy";
  return (
    <Card title="安全与运行监控">
      <div className="health-toolbar">
        <p className="caption">
          页面每 5 秒刷新；数据库每分钟扫描。连接池与限速器读取当前内存统计。
        </p>
        <div className="table-actions">
          <button
            type="button"
            disabled={loading}
            onClick={() => setRevision((n) => n + 1)}
          >
            {loading ? "读取中…" : "立即刷新"}
          </button>
          <button
            type="button"
            aria-pressed={automatic}
            onClick={() => setAutomatic((enabled) => !enabled)}
          >
            自动刷新：{automatic ? "开" : "关"}
          </button>
        </div>
      </div>
      <Alert error={error} />
      {loading && !data ? <Spinner /> : null}
      {data ? (
        <>
          <p>
            <Status status={data.overall_status} /> 已观测项评分：
            {value(data.overall_score)}
            {data.overall_score != null ? " / 100" : "（指标不足）"}
          </p>
          <p className="caption">
            更新：{fmt.date(data.timestamp)} · 数据库采集：
            {data.storage_checked_at
              ? fmt.date(data.storage_checked_at)
              : "尚未采集"}
            {" · "}运行{" "}
            {value(
              Math.max(
                0,
                Math.floor(
                  (Date.parse(data.timestamp) - Date.parse(data.started_at)) /
                    60000,
                ),
              ),
            )}{" "}
            分钟
          </p>
          {!storageFresh ? (
            <Alert
              error={reasons[data.storage_error ?? ""] ?? "数据库状态不可用"}
            />
          ) : null}
          <div className="table-wrap">
            <table className="health-metrics-table" aria-label="安全监控指标">
              <thead>
                <tr>
                  <th>指标</th>
                  <th>观测值</th>
                  <th>状态与说明</th>
                </tr>
              </thead>
              <tbody>
                {data.metrics.map((metric) => (
                  <tr key={metric.key}>
                    <td>{metric.label}</td>
                    <td>
                      {metric.value != null && Number.isFinite(metric.value)
                        ? `${metric.value.toLocaleString("zh-CN", { maximumFractionDigits: 2 })}${metric.unit}`
                        : "—"}
                    </td>
                    <td>
                      <Status status={metric.status} />
                      {metric.reason ? (
                        <small>{reasons[metric.reason] ?? "指标不可用"}</small>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="caption">
            清理失败率、回滚失败与拒绝计数从进程启动累计，仅作历史参考，不影响当前评分。
            暂无样本不等于采集失败；未知的适用指标仍会使评分不可用。评分不代表安全审计结论或服务可用性承诺。
          </p>
          <div className="metrics health-summary">
            <Metric
              label="有效面板会话"
              value={storageFresh ? value(data.storage.active_sessions) : "—"}
            />
            <Metric
              label="会话清理尝试 / 失败"
              value={`${value(data.cleanup.attempts)} / ${value(data.cleanup.errors)}`}
            />
            {data.storage.mysql ? (
              <Metric
                label="控制库使用中 / 连接上限"
                value={
                  runtimeFresh
                    ? `${value(data.storage.mysql.in_use)} / ${data.storage.mysql.max_open === 0 ? "无限制" : value(data.storage.mysql.max_open)}`
                    : "—"
                }
                hint={
                  runtimeFresh
                    ? `累计等待 ${value(data.storage.mysql.wait_count)} 次`
                    : "运行统计不可用"
                }
              />
            ) : null}
            {data.storage.bbolt ? (
              <Metric
                label="bbolt 只读事务 / 待复用页"
                value={
                  runtimeFresh
                    ? `${value(data.storage.bbolt.open_read_transactions)} / ${value(data.storage.bbolt.pending_pages)}`
                    : "—"
                }
              />
            ) : null}
          </div>
          <div
            className="table-wrap"
            role="region"
            aria-label="IP 限速器表格（可横向滚动）"
            tabIndex={0}
          >
            <table aria-label="IP 限速器">
              <thead>
                <tr>
                  <th>限速器</th>
                  <th>有效条目 / 容量</th>
                  <th>内存保留条目</th>
                  <th>累计拒绝</th>
                  <th>累计清理</th>
                </tr>
              </thead>
              <tbody>
                {data.rate_limiters.map((limiter) => (
                  <tr key={limiter.name}>
                    <td>
                      {limiter.name === "login" ? "面板登录" : "面板 DNS 查询"}
                    </td>
                    <td>
                      {value(limiter.active_entries)} /{" "}
                      {value(limiter.capacity)}
                    </td>
                    <td>{value(limiter.entries)}</td>
                    <td>{value(limiter.rejected_total)}</td>
                    <td>{value(limiter.evicted_total)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      ) : !loading && !error ? (
        <p className="caption">暂无监控数据。</p>
      ) : null}
    </Card>
  );
}
