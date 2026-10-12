import { useCallback, useMemo } from "react";
import { allPages } from "../api";
import {
  Alert,
  Card,
  Chart,
  Empty,
  Metric,
  Spinner,
  fmt,
  useLoad,
} from "../components";
import { t, useLang } from "../i18n";
import { TopDomainsView, type TopDomainsState } from "../top-domains";
import { query, range, successRate, usageSummary } from "./shared";
import type { Credential, DeviceUsagePoint, Stats, UsagePoint } from "../types";

export function StatsBlocks({
  stats,
  usage,
  topDomains,
}: {
  stats: Stats;
  usage?: UsagePoint[];
  // The page loads the ranking next to its stats; see useTopDomains.
  topDomains?: TopDomainsState;
}) {
  const summary = usage ? usageSummary(usage) : undefined;
  return (
    <>
      {summary ? (
        <>
          <div className="metrics">
            <Metric
              label={t("已受理请求")}
              value={fmt.num(summary.total)}
              hint={t("以持久化计量为准")}
            />
            <Metric
              label={t("最近完整分钟平均")}
              value={t("{n} 次/秒", { n: summary.recentPerSecond.toFixed(2) })}
              hint={t("一分钟平均值")}
            />
          </div>
          <Card title={t("已受理用量趋势")}>
            <Chart
              series={summary.series}
              from={stats.from}
              to={stats.to}
              kind="usage"
            />
          </Card>
        </>
      ) : null}
      <div className="metrics">
        <Metric label={t("处理完成")} value={fmt.num(stats.completed)} />
        <Metric label={t("成功率")} value={fmt.pct(successRate(stats), 1)} />
        <Metric
          label={t("缓存命中")}
          value={fmt.pct(stats.cache_hits, stats.completed)}
        />
        <Metric
          label={t("平均延迟")}
          value={`${stats.avg_latency_ms.toFixed(1)} ms`}
          hint={`P95 ${stats.p95_latency_ms.toFixed(1)} ms`}
        />
        <Metric
          label={t("统计缺失")}
          value={fmt.num(stats.dropped)}
          hint={t("异步采集未记录数量")}
        />
      </div>
      <Card title={t("处理结果趋势")}>
        <Chart series={stats.series} from={stats.from} to={stats.to} />
        <p className="caption">
          {t("统计窗口 {from} 至 {to} · 更新于 {updated}", {
            from: fmt.date(stats.from),
            to: fmt.date(stats.to),
            updated: fmt.date(stats.updated_at),
          })}
        </p>
      </Card>
      <div className={topDomains ? "grid3" : "grid2"}>
        <Card title={t("响应结果")}>
          {Object.keys(stats.rcode_counts).length ? (
            <div className="rows">
              {Object.entries(stats.rcode_counts).map(([k, v]) => (
                <div key={k}>
                  <span>{k}</span>
                  <strong>{fmt.num(v)}</strong>
                </div>
              ))}
            </div>
          ) : (
            <Empty />
          )}
        </Card>
        <Card title={t("上游服务")}>
          {stats.upstreams.length ? (
            <div className="rows">
              {stats.upstreams.map((u) => (
                <div key={u.id}>
                  <span>
                    {u.id}
                    <small>
                      {t("{attempts} 次尝试 · {failures} 次失败", {
                        attempts: fmt.num(u.attempts),
                        failures: fmt.num(u.failures),
                      })}
                    </small>
                  </span>
                  <strong>{u.avg_latency_ms.toFixed(1)} ms</strong>
                </div>
              ))}
            </div>
          ) : (
            <Empty />
          )}
        </Card>
        {topDomains ? (
          <TopDomainsView
            data={topDomains.data}
            error={topDomains.error}
            loading={topDomains.loading}
          />
        ) : null}
      </div>
    </>
  );
}

export function DeviceUsage({
  path,
  credentialsPath,
}: {
  path: string;
  credentialsPath: string;
}) {
  const params = useMemo(range, []);
  const load = useCallback(
    (s: AbortSignal) =>
      Promise.all([
        allPages<DeviceUsagePoint>(query(path, params), s),
        allPages<Credential>(credentialsPath, s),
      ]),
    [path, credentialsPath, params],
  );
  const { data, error, loading } = useLoad(load, [load]);
  const lang = useLang();
  const grouped = useMemo(() => {
    const m = new Map<string, number>();
    const names = new Map<string, string>(
      (data?.[1] ?? []).map((c) => [c.id, c.name]),
    );
    for (const x of data?.[0] ?? []) {
      const k = x.credential_id
        ? (names.get(x.credential_id) ?? x.credential_id)
        : t("未标识凭证");
      m.set(k, (m.get(k) ?? 0) + x.count);
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [data, lang]);
  return (
    <Card title={t("设备使用（过去 24 小时）")}>
      <Alert error={error} />
      {loading ? (
        <Spinner />
      ) : grouped.length ? (
        <div className="rows">
          {grouped.map(([n, c]) => (
            <div key={n}>
              <span>{n}</span>
              <strong>{t("{count} 次", { count: fmt.num(c) })}</strong>
            </div>
          ))}
        </div>
      ) : (
        <Empty>{t("当前窗口暂无设备用量")}</Empty>
      )}
    </Card>
  );
}
