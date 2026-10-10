import { useCallback, useState } from "react";
import { request } from "./api";
import { Alert, Card, Empty, Modal, Spinner, fmt, useLoad } from "./components";
import { t } from "./i18n";
import type { TopDomains } from "./types";

// The card lists the first shownDomains names and keeps the rest of the
// ranking, up to the API's maximum, for the full list.
const shownDomains = 5;
const rankedDomains = 100;

// TopDomainsCard ranks the most queried names of a stats window. path is
// /me/top-domains or /admin/top-domains, optionally with ?user_id=.
export function TopDomainsCard({
  path,
  from,
  to,
}: {
  path: string;
  from: string;
  to: string;
}) {
  const load = useCallback(
    (signal: AbortSignal) =>
      request<TopDomains>(
        `${path}${path.includes("?") ? "&" : "?"}${new URLSearchParams({
          from,
          to,
          limit: String(rankedDomains),
        })}`,
        { signal },
      ),
    [path, from, to],
  );
  const { data, error, loading } = useLoad(load, [load]);
  const [all, setAll] = useState(false);
  return (
    <Card
      title={t("查询最多的域名")}
      className="top-domains"
      action={
        data?.query_log_enabled && data.domains.length > shownDomains ? (
          <button type="button" className="link" onClick={() => setAll(true)}>
            {t("查看更多")}
          </button>
        ) : null
      }
    >
      {loading ? <Spinner /> : <Alert error={error} />}
      {data ? <Ranking data={data} limit={shownDomains} /> : null}
      {all && data ? (
        <Modal
          title={t("查询最多的域名")}
          onClose={() => setAll(false)}
          className="top-domains-modal"
        >
          <p className="caption">
            {t(
              "查询量最多的 {n} 个域名，统计 {from} 至 {to} 的 {count} 次查询",
              {
                n: data.domains.length,
                from: fmt.date(data.from),
                to: fmt.date(data.to),
                count: fmt.num(data.queries),
              },
            )}
          </p>
          <Ranking data={data} />
        </Modal>
      ) : null}
    </Card>
  );
}

function Ranking({ data, limit }: { data: TopDomains; limit?: number }) {
  if (!data.query_log_enabled)
    return <Empty>{t("查询明细已关闭，无法统计查询最多的域名。")}</Empty>;
  if (!data.domains.length) return <Empty />;
  return (
    <div className="rows">
      {data.domains.slice(0, limit).map((d, i) => (
        <div key={d.name}>
          <span>
            <span className="top-domains-rank">{i + 1}</span>
            <span className="top-domains-name">
              {d.name.replace(/\.$/, "") || "."}
            </span>
            <small>
              {t("占 {share} · 缓存命中 {hits}", {
                share: fmt.pct(d.queries, data.queries),
                hits: fmt.pct(d.cache_hits, d.queries),
              })}
              {d.extended_ttl
                ? ` · ${t("TTL 已自动延长至 {minutes} 分钟", {
                    minutes: Math.max(1, Math.round(d.extended_ttl / 60)),
                  })}`
                : null}
            </small>
          </span>
          <strong>{fmt.num(d.queries)}</strong>
        </div>
      ))}
    </div>
  );
}
