import { useEffect, useState } from "react";
import { message, request } from "./api";
import { Alert, Card, Metric, Spinner, fmt } from "./components";
import { locale, msg, t } from "./i18n";

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
  healthy: msg("正常"),
  warning: msg("警告"),
  critical: msg("异常"),
  unknown: msg("数据不足"),
  not_applicable: msg("不适用"),
  no_samples: msg("暂无样本"),
  info: msg("历史参考"),
};
const reasons: Record<string, string> = {
  no_samples: msg("尚无会话清理样本，不计算失败率，也不影响当前评分"),
  cumulative_only: msg("进程累计值，仅作历史参考；近期告警使用窗口增量"),
  bbolt_backend: msg("bbolt 不使用 MySQL 事务或连接池"),
  no_materialized_count: msg("MySQL 按事务查询计数，没有独立计数缓存"),
  unlimited_pool: msg("连接池未设置上限，无法计算占用比例"),
  not_collected: msg("等待首次数据库采集"),
  collection_failed: msg("数据库采集失败，请检查服务日志"),
  scan_limit: msg(
    "数据量超过扫描预算；可调整 control.health_scan_limit，运行统计仍独立提供",
  ),
  stale: msg("数据库快照已过期，等待重新采集"),
  unsupported: msg("当前后端未提供健康检查"),
  invalid_value: msg("采集值无效"),
  invalid_capacity: msg("无法读取有效容量"),
  runtime_unavailable: msg("运行统计不可用"),
};
// The server sends metric labels and units in Chinese. The labels it uses
// today are listed so they get translations; anything else is shown as sent.
const metricLabels = [
  msg("累计会话清理失败率"),
  msg("IP 限速器最高占用"),
  msg("控制库连接池占用"),
  msg("凭证一致性异常"),
  msg("累计 MySQL 回滚失败"),
];
function metricLabel(label: string) {
  return metricLabels.includes(label) ? t(label) : label;
}
function withUnit(number: string, unit: string) {
  switch (unit) {
    case "处": // i18n-ignore: unit sent by the server
      return t("{value}处", { value: number });
    case "次": // i18n-ignore: unit sent by the server
      return t("{value}次", { value: number });
    default:
      return `${number}${unit}`;
  }
}
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
    <span className={`badge ${color}`}>
      {t(labels[status] ?? msg("数据不足"))}
    </span>
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
          setError(message(reason) || t("监控数据读取失败"));
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
    <Card title={t("安全与运行监控")}>
      <div className="health-toolbar">
        <p className="caption">
          {t(
            "页面每 5 秒刷新；数据库每分钟扫描。连接池与限速器读取当前内存统计。",
          )}
        </p>
        <div className="table-actions">
          <button
            type="button"
            disabled={loading}
            onClick={() => setRevision((n) => n + 1)}
          >
            {loading ? t("读取中…") : t("立即刷新")}
          </button>
          <button
            type="button"
            aria-pressed={automatic}
            onClick={() => setAutomatic((enabled) => !enabled)}
          >
            {automatic ? t("自动刷新：开") : t("自动刷新：关")}
          </button>
        </div>
      </div>
      <Alert error={error} />
      {loading && !data ? <Spinner /> : null}
      {data ? (
        <>
          <p className="health-score">
            <Status status={data.overall_status} />
            <span>
              {data.overall_score != null
                ? t("已观测项评分：{score} / 100", {
                    score: value(data.overall_score),
                  })
                : t("已观测项评分：{score}（指标不足）", {
                    score: value(data.overall_score),
                  })}
            </span>
          </p>
          <p className="caption">
            {t("更新：{time}", { time: fmt.date(data.timestamp) })} ·{" "}
            {data.storage_checked_at
              ? t("数据库采集：{time}", {
                  time: fmt.date(data.storage_checked_at),
                })
              : t("数据库采集：尚未采集")}
            {" · "}
            {t("运行 {minutes} 分钟", {
              minutes: value(
                Math.max(
                  0,
                  Math.floor(
                    (Date.parse(data.timestamp) - Date.parse(data.started_at)) /
                      60000,
                  ),
                ),
              ),
            })}
          </p>
          {!storageFresh ? (
            <Alert
              error={t(
                reasons[data.storage_error ?? ""] ?? msg("数据库状态不可用"),
              )}
            />
          ) : null}
          <div
            className="table-wrap"
            role="region"
            aria-label={t("安全监控指标表格（可横向滚动）")}
            tabIndex={0}
          >
            <table
              className="health-metrics-table"
              aria-label={t("安全监控指标")}
            >
              <thead>
                <tr>
                  <th>{t("指标")}</th>
                  <th>{t("观测值")}</th>
                  <th>{t("状态与说明")}</th>
                </tr>
              </thead>
              <tbody>
                {data.metrics.map((metric) => (
                  <tr key={metric.key}>
                    <td>{metricLabel(metric.label)}</td>
                    <td>
                      {metric.value != null && Number.isFinite(metric.value)
                        ? withUnit(
                            metric.value.toLocaleString(locale(), {
                              maximumFractionDigits: 2,
                            }),
                            metric.unit,
                          )
                        : "—"}
                    </td>
                    <td>
                      <Status status={metric.status} />
                      {metric.reason ? (
                        <small>
                          {t(reasons[metric.reason] ?? msg("指标不可用"))}
                        </small>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="caption">
            {t(
              "清理失败率、回滚失败与拒绝计数从进程启动累计，仅作历史参考，不影响当前评分。",
            )}{" "}
            {t(
              "暂无样本不等于采集失败；未知的适用指标仍会使评分不可用。评分不代表安全审计结论或服务可用性承诺。",
            )}
          </p>
          <div className="metrics health-summary">
            <Metric
              label={t("有效面板会话")}
              value={storageFresh ? value(data.storage.active_sessions) : "—"}
            />
            <Metric
              label={t("会话清理尝试 / 失败")}
              value={`${value(data.cleanup.attempts)} / ${value(data.cleanup.errors)}`}
            />
            {data.storage.mysql ? (
              <Metric
                label={t("控制库使用中 / 连接上限")}
                value={
                  runtimeFresh
                    ? `${value(data.storage.mysql.in_use)} / ${data.storage.mysql.max_open === 0 ? t("无限制") : value(data.storage.mysql.max_open)}`
                    : "—"
                }
                hint={
                  runtimeFresh
                    ? t("累计等待 {count} 次", {
                        count: value(data.storage.mysql.wait_count),
                      })
                    : t("运行统计不可用")
                }
              />
            ) : null}
            {data.storage.bbolt ? (
              <Metric
                label={t("bbolt 只读事务 / 待复用页")}
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
            aria-label={t("IP 限速器表格（可横向滚动）")}
            tabIndex={0}
          >
            <table aria-label={t("IP 限速器")}>
              <thead>
                <tr>
                  <th>{t("限速器")}</th>
                  <th>{t("有效条目 / 容量")}</th>
                  <th>{t("内存保留条目")}</th>
                  <th>{t("累计拒绝")}</th>
                  <th>{t("累计清理")}</th>
                </tr>
              </thead>
              <tbody>
                {data.rate_limiters.map((limiter) => (
                  <tr key={limiter.name}>
                    <td>
                      {limiter.name === "login"
                        ? t("面板登录")
                        : t("面板 DNS 查询")}
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
        <p className="caption">{t("暂无监控数据。")}</p>
      ) : null}
    </Card>
  );
}
