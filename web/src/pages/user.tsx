import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { allPages, request } from "../api";
import {
  Alert,
  Card,
  LanguageSelect,
  MasterManaged,
  Metric,
  PageTitle,
  Spinner,
  fmt,
  useLoad,
} from "../components";
import { t } from "../i18n";
import { useFollower, useSession } from "../session";
import { useTopDomains } from "../top-domains";
import { Addresses, CredentialManager, credentialActive } from "./credentials";
import { PasswordForm } from "./password";
import { QueryDetails } from "./query-log";
import { query, range } from "./shared";
import { DeviceUsage, StatsBlocks } from "./stats";
import type { Credential, Me, Stats, UsagePoint } from "../types";

export function ServicePage() {
  // With multi-node sync each node counts the quota on its own.
  const synced = Boolean(useSession().session?.identity_sync);
  const follower = useFollower();
  const load = useCallback(
    (s: AbortSignal) =>
      Promise.all([
        request<Me>("/me", { signal: s }),
        request<Stats>(query("/me/stats", range()), { signal: s }),
        allPages<Credential>("/me/credentials", s),
      ]),
    [],
  );
  const { data, error, loading } = useLoad(load, [load]);
  if (loading)
    return (
      <>
        <PageTitle title={t("主页")} />
        <Spinner />
      </>
    );
  return (
    <>
      <PageTitle
        title={t("主页")}
        description={t("连接信息、服务额度与设备接入。")}
      />
      <Alert error={error} />
      {data ? (
        <>
          <Card
            title={t("设备凭证")}
            action={
              <Link className="link" to="/app/account">
                {follower ? t("查看设备凭证") : t("管理设备凭证")}
              </Link>
            }
          >
            {data[2].some(credentialActive) ? (
              <dl className="credential-addresses">
                {data[2].filter(credentialActive).map((c) => (
                  <div key={c.id}>
                    <dt>{c.name}</dt>
                    <dd>
                      {c.token_available ? (
                        <Addresses urls={c.doh_urls} single={c.doh_url} />
                      ) : (
                        <p className="caption">
                          {t(
                            "该凭证签发于保存完整地址之前，可在账户中心录入。",
                          )}
                        </p>
                      )}
                    </dd>
                  </div>
                ))}
              </dl>
            ) : (
              <p className="caption">
                {t("尚未创建设备凭证。每台设备请使用独立凭证接入。")}
              </p>
            )}
          </Card>
          <div className="metrics">
            <Metric
              label={synced ? t("本节点剩余额度") : t("剩余额度")}
              value={fmt.num(data[0].quota.remaining)}
              hint={t("已用 {used} / {limit}", {
                used: fmt.num(data[0].quota.used),
                limit: fmt.num(data[0].quota.limit),
              })}
            />
            <Metric
              label={t("额度重置")}
              value={fmt.date(data[0].quota.period_end)}
              hint={`${data[0].quota.period === "daily" ? t("每日") : t("每月")} · ${data[0].quota.timezone}`}
            />
            <Metric
              label={t("服务到期")}
              value={fmt.date(data[0].user.expires_at)}
            />
            <Metric
              label={t("QPS / 突发")}
              value={`${data[0].user.qps} / ${data[0].user.burst}`}
            />
          </div>
          <Card title={t("额度使用")}>
            <div
              className="progress"
              aria-label={t("额度已使用 {percent}", {
                percent: fmt.pct(data[0].quota.used, data[0].quota.limit),
              })}
            >
              <span
                style={{
                  width: fmt.pct(data[0].quota.used, data[0].quota.limit),
                }}
              />
            </div>
          </Card>
          <Card title={t("订阅")}>
            <div className="rows">
              <div>
                <span>{t("服务周期")}</span>
                <strong>
                  {data[0].quota.period === "daily"
                    ? t("每日 额度")
                    : t("每月 额度")}
                </strong>
              </div>
            </div>
          </Card>
          <Card title={t("设备接入")} className="connection-guide">
            <ol>
              <li>
                {t("为每台设备创建独立凭证，完整地址随时可在账户中心查看。")}
              </li>
              <li>{t("在客户端填写该凭证的完整地址，任选一个节点即可。")}</li>
              <li>
                {t("设备遗失或不再使用时，请在账户中心轮换或撤销其凭证。")}
              </li>
            </ol>
          </Card>
          <StatsBlocks stats={data[1]} />
        </>
      ) : null}
    </>
  );
}
export function UsagePage() {
  const [version, setVersion] = useState(0);
  const params = useMemo(range, [version]);
  const load = useCallback(
    (s: AbortSignal) =>
      Promise.all([
        request<Stats>(query("/me/stats", params), { signal: s }),
        allPages<UsagePoint>(query("/me/usage", params), s),
      ]),
    [params],
  );
  const { data, error, loading } = useLoad(load, [load], "me");
  const topDomains = useTopDomains("/me/top-domains", params.from, params.to);
  return (
    <>
      <PageTitle
        title={t("统计与日志")}
        description={t("过去 24 小时的用量、设备和 DNS 查询记录")}
        action={
          <button onClick={() => setVersion((x) => x + 1)}>{t("刷新")}</button>
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data ? (
        <>
          <StatsBlocks
            stats={data[0]}
            usage={data[1]}
            topDomains={topDomains}
          />
          <DeviceUsage
            path="/me/device-usage"
            credentialsPath="/me/credentials"
          />
          <QueryDetails
            path="/me/queries"
            enabled={data[0].query_log_enabled}
            credentialsPath="/me/credentials"
          />
        </>
      ) : null}
    </>
  );
}
export function CredentialsPage() {
  const load = useCallback(
    (s: AbortSignal) => request<Me>("/me", { signal: s }),
    [],
  );
  const { data, error, loading } = useLoad(load, [load]);
  return (
    <>
      <PageTitle
        title={t("接入凭证")}
        description={t("为每台设备签发独立的专属地址")}
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data ? (
        <CredentialManager base="/me" max={data.user.max_credentials} />
      ) : null}
    </>
  );
}

export function AccountPage() {
  const follower = useFollower();
  const load = useCallback(
    (signal: AbortSignal) => request<Me>("/me", { signal }),
    [],
  );
  const { data, error, loading } = useLoad(load, [load]);
  return (
    <>
      <PageTitle
        title={t("账户中心")}
        description={
          follower
            ? t("查看账户状态与设备凭证。")
            : t("管理账户状态、设备凭证与登录密码。")
        }
      />
      <MasterManaged />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data ? (
        <>
          <div className="metrics">
            <Metric label={t("账户")} value={data.user.username} />
            <Metric
              label={t("服务到期")}
              value={fmt.date(data.user.expires_at)}
            />
            <Metric label={t("凭证名额")} value={data.user.max_credentials} />
          </div>
          <CredentialManager base="/me" max={data.user.max_credentials} />
          {follower ? null : (
            <Card className="narrow" title={t("修改密码")}>
              <p className="caption">{t("修改成功后，当前会话会立即退出。")}</p>
              <PasswordForm />
            </Card>
          )}
        </>
      ) : null}
      <Card className="language-card" title={t("界面语言")}>
        <LanguageSelect />
      </Card>
    </>
  );
}

export function HelpPage() {
  return (
    <>
      <PageTitle
        title={t("帮助与支持")}
        description={t("设备接入与用量查看说明。")}
      />
      <div className="grid2 help-grid">
        <Card title={t("设备接入")}>
          <p>
            {t(
              "先在账户中心创建一个设备凭证，再将它的完整地址填到设备上。地址随时可以在账户中心查看和复制。",
            )}
          </p>
          <p>{t("凭证遗失或设备不再使用时，可在账户中心轮换或撤销它。")}</p>
        </Card>
        <Card title={t("用量与日志")}>
          <p>
            {t(
              "统计与日志展示账户已处理的 DNS 请求。查询明细是否可见取决于服务端是否已开启日志记录。",
            )}
          </p>
        </Card>
      </div>
    </>
  );
}
