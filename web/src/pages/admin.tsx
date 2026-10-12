import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { allPages, request } from "../api";
import {
  Alert,
  Card,
  Empty,
  LanguageSelect,
  Metric,
  PageTitle,
  Spinner,
  adminMoreLinks,
  fmt,
  useLoad,
} from "../components";
import { msg, t } from "../i18n";
import { CachePurgeCard } from "../cache-purge";
import { HealthPanel } from "../health-panel";
import { useTopDomains } from "../top-domains";
import { PasswordForm } from "./password";
import { QueryDetails } from "./query-log";
import { query, range, usePaged } from "./shared";
import { StatsBlocks } from "./stats";
import type { Audit, Stats, SystemInfo, UsagePoint } from "../types";

export function AdminOverview() {
  const [version, setVersion] = useState(0);
  const params = useMemo(range, [version]);
  const load = useCallback(
    (s: AbortSignal) =>
      Promise.all([
        request<Stats>(query("/admin/stats", params), { signal: s }),
        allPages<UsagePoint>(query("/admin/usage", params), s),
      ]),
    [params],
  );
  const { data, error, loading } = useLoad(load, [load], "admin");
  const topDomains = useTopDomains(
    "/admin/top-domains",
    params.from,
    params.to,
  );
  return (
    <>
      <PageTitle
        title={t("服务总览")}
        description={t("全局 DNS 请求、响应与上游运行状态")}
        action={
          <button onClick={() => setVersion((x) => x + 1)}>{t("刷新")}</button>
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data ? (
        <StatsBlocks stats={data[0]} usage={data[1]} topDomains={topDomains} />
      ) : null}
    </>
  );
}

export function AdminLogs() {
  const [version, setVersion] = useState(0);
  const params = useMemo(range, [version]);
  const loader = useCallback(
    (signal: AbortSignal) =>
      request<Stats>(query("/admin/stats", params), { signal }),
    [params],
  );
  const { data, error, loading } = useLoad(loader, [loader], "admin");
  return (
    <>
      <PageTitle
        title={t("查询日志")}
        description={t("筛选全站 DNS 查询，并查看客户端、响应地址和 EDNS 详情")}
        action={
          <button onClick={() => setVersion((value) => value + 1)}>
            {t("刷新统计")}
          </button>
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data ? (
        <>
          <div className="metrics log-metrics">
            <Metric label={t("处理完成")} value={fmt.num(data.completed)} />
            <Metric label={t("失败")} value={fmt.num(data.failed)} />
            <Metric
              label={t("缓存命中")}
              value={fmt.pct(data.cache_hits, data.completed)}
            />
            <Metric
              label={t("平均延迟")}
              value={`${data.avg_latency_ms.toFixed(1)} ms`}
              hint={`P95 ${data.p95_latency_ms.toFixed(1)} ms`}
            />
          </div>
          <QueryDetails
            path="/admin/queries"
            enabled={data.query_log_enabled}
            showPrincipal
            cachePurge
          />
        </>
      ) : null}
    </>
  );
}

export function PasswordPage() {
  return (
    <>
      <PageTitle
        title={t("修改密码")}
        description={t("修改后，当前会话会立即退出")}
      />
      <Card className="narrow">
        <PasswordForm />
      </Card>
    </>
  );
}

export function MorePage() {
  return (
    <>
      <PageTitle
        title={t("更多")}
        description={t("审计、系统状态与账户安全")}
      />
      <Card className="more-card">
        <ul className="more-links">
          {adminMoreLinks.map(({ to, label }) => (
            <li key={to}>
              <Link to={to}>{t(label)}</Link>
            </li>
          ))}
        </ul>
      </Card>
      <Card className="language-card" title={t("界面语言")}>
        <LanguageSelect />
      </Card>
    </>
  );
}

// Audit actions the server records, as shown in the audit table; an action
// this build does not know is shown as its raw name.
const auditActionNames: Record<string, string> = {
  initialize_admin: msg("初始化管理员"),
  create_user: msg("创建用户"),
  update_user: msg("修改用户"),
  delete_user: msg("删除用户"),
  set_password: msg("重置密码"),
  change_password: msg("修改密码"),
  revoke_session: msg("撤销会话"),
  create_credential: msg("创建凭证"),
  rotate_credential: msg("轮换凭证"),
  revoke_credential: msg("撤销凭证"),
  runtime_apply: msg("应用运行配置"),
  runtime_rollback: msg("回滚运行配置"),
  runtime_reload: msg("重载主配置"),
  cache_purge: msg("清除域名缓存"),
};
const auditTargetNames: Record<string, string> = {
  user: msg("账户"),
  session: msg("会话"),
  credential: msg("凭证"),
  revision: msg("修订"),
  domain: msg("域名"),
};
export function auditActionName(action: string) {
  return action in auditActionNames ? t(auditActionNames[action]) : action;
}
export function auditTargetName(type: string) {
  return type in auditTargetNames ? t(auditTargetNames[type]) : type;
}

export function AuditPage() {
  const params = useMemo(range, []);
  const {
    items: data,
    error,
    loading,
    cursor,
    more,
  } = usePaged<Audit>(query("/admin/audit", params));
  return (
    <>
      <PageTitle
        title={t("操作审计")}
        description={t("过去 24 小时的账户、凭证与运行配置变更")}
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data?.length ? (
        <div
          className="table-wrap"
          role="region"
          aria-label={t("审计记录")}
          tabIndex={0}
        >
          <table>
            <thead>
              <tr>
                <th>{t("时间")}</th>
                <th>{t("操作")}</th>
                <th>{t("对象")}</th>
                <th>{t("操作者")}</th>
              </tr>
            </thead>
            <tbody>
              {data.map((a) => (
                <tr key={a.id}>
                  <td>{fmt.date(a.created_at)}</td>
                  <td>{auditActionName(a.action)}</td>
                  <td>
                    {auditTargetName(a.target_type)}
                    {a.target_id ? ` · ${a.target_id}` : ""}
                  </td>
                  <td>{a.actor_id || t("系统")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : !loading && !error ? (
        <Empty>{t("当前窗口暂无审计记录")}</Empty>
      ) : null}
      {cursor ? (
        <div className="table-more">
          <button onClick={more} disabled={loading}>
            {loading ? t("正在加载…") : t("加载更多")}
          </button>
        </div>
      ) : null}
    </>
  );
}
export function SystemPage() {
  const load = useCallback(
    (s: AbortSignal) => request<SystemInfo>("/admin/system", { signal: s }),
    [],
  );
  const { data, error, loading } = useLoad(load, [load]);
  function download() {
    if (!data) return;
    const blob = new Blob([JSON.stringify(data.config, null, 2)], {
      type: "application/json",
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "mosdns-public-config.json";
    a.click();
    URL.revokeObjectURL(url);
  }
  return (
    <>
      <PageTitle
        title={t("系统概览")}
        description={t("版本、运行时间与可公开的配置摘要")}
        action={
          data ? <button onClick={download}>{t("下载配置摘要")}</button> : null
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />} <HealthPanel />
      {data ? (
        <>
          <div className="metrics">
            <Metric label={t("版本")} value={data.version} size="text" />
            <Metric label={t("启动时间")} value={fmt.date(data.started_at)} />
            <Metric
              label={t("查询明细")}
              value={data.query_log_enabled ? t("已启用") : t("未启用")}
            />
            <Metric
              label={t("控制数据")}
              value={data.config.control_storage}
              size="text"
            />
            <Metric
              label={t("统计数据")}
              value={data.config.telemetry_storage}
              size="text"
            />
          </div>
          <CachePurgeCard />
          <Card title={t("公共 DNS 地址")}>
            <code className="block">{data.public_dns_url}</code>
          </Card>
          <Card title={t("配置摘要")}>
            <pre>{JSON.stringify(data.config, null, 2)}</pre>
          </Card>
        </>
      ) : null}
    </>
  );
}
