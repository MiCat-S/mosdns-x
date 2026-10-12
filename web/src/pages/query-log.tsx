import {
  lazy,
  Suspense,
  useCallback,
  useMemo,
  useState,
  type FormEvent,
} from "react";
import { allPages } from "../api";
import {
  Alert,
  Card,
  Empty,
  Field,
  Modal,
  Spinner,
  fmt,
  useLoad,
} from "../components";
import { dateFormat, msg, t } from "../i18n";
import { OutboundSection, outboundName, routeReason } from "../query-trace";
import { loadChunk } from "../lazy-chunk";
import { query, usePaged } from "./shared";
import type { Credential, EDNSInfo, QueryRecord } from "../types";
// Only admin views purge caches; users never load the purge module.
const PurgeDomainButton = lazy(() =>
  loadChunk(() => import("../cache-purge")).then((m) => ({
    default: m.PurgeDomainButton,
  })),
);

const emptyQueryFilters = {
  hours: "24",
  name: "",
  qtype: "",
  rcode: "",
  credentialId: "",
  protocol: "",
  address: "",
  cache: "all",
  source: "all",
  upstream: "",
};

type QueryFilters = typeof emptyQueryFilters;

const ednsOptionNames: Record<number, string> = {
  3: "NSID",
  8: "ECS",
  10: "COOKIE",
  12: "Padding",
  15: "EDE",
};

const ednsAnomalyNames: Record<string, string> = {
  multiple_opt: msg("检测到多个 OPT 记录"),
  multiple_ecs: msg("检测到多个 ECS 选项"),
  invalid_ecs_family: msg("ECS 地址族无效"),
  invalid_ecs_prefix: msg("ECS 前缀长度无效"),
  invalid_ecs_address: msg("ECS 地址无效"),
};

const responseSourceNames: Record<string, string> = {
  cache: msg("缓存"),
  upstream: msg("上游"),
  hosts: "Hosts",
  sequence: msg("执行链"),
  servfail: "SERVFAIL",
};

function responseSourceName(source?: string) {
  return source ? t(responseSourceNames[source] ?? source) : t("未记录");
}

// principalName shows a record's account by name, or by ID once the account
// has been deleted.
function principalName(record: QueryRecord) {
  return record.username || record.user_id || t("未识别");
}

const logDayFormat: Intl.DateTimeFormatOptions = { dateStyle: "medium" };
const logClockFormat: Intl.DateTimeFormatOptions = { timeStyle: "medium" };
const logDateFormat: Intl.DateTimeFormatOptions = {
  dateStyle: "medium",
  timeStyle: "medium",
};
function logDay(value: string) {
  return dateFormat(logDayFormat).format(new Date(value));
}
function logClock(value: string) {
  return dateFormat(logClockFormat).format(new Date(value));
}
function logDate(value: string) {
  return dateFormat(logDateFormat).format(new Date(value));
}

type EDNSStage =
  | "client_request"
  | "upstream_request"
  | "upstream_response"
  | "client_response";

function missingEDNSMessage(record: QueryRecord, stage: EDNSStage) {
  if (stage === "client_request") {
    return record.edns_trace_version
      ? t("该阶段快照暂不可用")
      : t("历史记录未采集客户端请求快照");
  }
  if (stage === "client_response") {
    return record.edns_trace_version
      ? t("该阶段快照暂不可用")
      : t("历史记录未采集客户端响应快照");
  }
  if (!record.edns_trace_version) {
    return t("历史记录未采集该上游阶段");
  }
  switch (record.upstream_stage_status) {
    case "selected":
      return t("已记录最终上游，但该阶段快照暂不可用");
    case "attempted_no_selection":
      return t("发生过上游尝试，但没有最终采用的上游结果");
    case "discarded":
      return t("上游结果已被后续本地响应完整替换，无对应最终上游快照");
    case "not_linked":
      if (record.response_source === "cache") {
        return t("本次响应来自缓存，无对应上游快照");
      }
      if (record.response_source === "hosts") {
        return t("本次由本地 Hosts 生成响应，无对应上游快照");
      }
      return t("本次最终响应没有可关联的上游快照");
    default:
      return t("该阶段快照暂不可用");
  }
}

function ecsDescription(edns: EDNSInfo) {
  if (!edns.ecs) return t("无");
  const family =
    edns.ecs.family === 1
      ? "IPv4"
      : edns.ecs.family === 2
        ? "IPv6"
        : t("地址族 {family}", { family: edns.ecs.family });
  return `${edns.ecs.address}/${edns.ecs.source_prefix} · ${family} · Scope Prefix ${edns.ecs.scope_prefix}`;
}

function EDNSStageCard({
  title,
  note,
  snapshot,
  missing,
}: {
  title: string;
  note: string;
  snapshot?: EDNSInfo | null;
  missing: string;
}) {
  return (
    <article className="edns-stage">
      <header>
        <div>
          <h4>{title}</h4>
          <small>{note}</small>
        </div>
        {snapshot ? (
          <span className={`badge ${snapshot.present ? "ok" : "off"}`}>
            {snapshot.present ? t("有 EDNS") : t("无 EDNS")}
          </span>
        ) : null}
      </header>
      {!snapshot ? (
        <p className="edns-stage-missing">{missing}</p>
      ) : !snapshot.present ? (
        <p className="edns-stage-empty">{t("已观察报文：没有 EDNS")}</p>
      ) : (
        <dl>
          <div>
            <dt>{t("版本")}</dt>
            <dd>EDNS v{snapshot.version}</dd>
          </div>
          <div>
            <dt>{t("通告 UDP Size")}</dt>
            <dd>{snapshot.udp_size} bytes</dd>
          </div>
          <div>
            <dt>{t("DO 位")}</dt>
            <dd>
              {snapshot.dnssec_ok
                ? t("已设置（请求 DNSSEC 数据）")
                : t("未设置")}
            </dd>
          </div>
          <div>
            <dt>Option Code</dt>
            <dd>
              {snapshot.option_codes?.length
                ? snapshot.option_codes
                    .map(
                      (code) =>
                        `${code}${ednsOptionNames[code] ? ` (${ednsOptionNames[code]})` : ""}`,
                    )
                    .join(", ")
                : t("无已记录选项")}
              {snapshot.option_codes_truncated ? (
                <small>
                  {t("仅显示前 64 项，不能据此判断其他 Option 不存在")}
                </small>
              ) : null}
            </dd>
          </div>
          <div className="edns-stage-wide">
            <dt>ECS</dt>
            <dd>{ecsDescription(snapshot)}</dd>
          </div>
          {snapshot.anomalies?.length ? (
            <div className="edns-stage-wide">
              <dt>{t("采集异常")}</dt>
              <dd className="edns-anomalies">
                {snapshot.anomalies
                  .map((item) => t(ednsAnomalyNames[item] ?? item))
                  .join(t("；"))}
                <small>
                  {t("异常信息仅用于解释快照，不影响 DNS 请求处理。")}
                </small>
              </dd>
            </div>
          ) : null}
        </dl>
      )}
    </article>
  );
}

function snapshotComparable(snapshot?: EDNSInfo | null) {
  return Boolean(
    snapshot && !snapshot.option_codes_truncated && !snapshot.anomalies?.length,
  );
}

function sameECS(left?: EDNSInfo["ecs"], right?: EDNSInfo["ecs"]) {
  if (!left || !right) return left === right;
  return (
    left.address === right.address &&
    left.family === right.family &&
    left.source_prefix === right.source_prefix &&
    left.scope_prefix === right.scope_prefix
  );
}

function ednsDifferences(record: QueryRecord) {
  if (record.upstream_stage_status !== "selected") return [];
  const stages = [
    record.edns,
    record.upstream_request_edns,
    record.upstream_response_edns,
    record.response_edns,
  ];
  if (!stages.every(snapshotComparable)) {
    return [t("阶段信息未知、异常或不完整，无法确定完整的 EDNS/ECS 变化")];
  }
  const [client, upstreamRequest, upstreamResponse, response] = stages as [
    EDNSInfo,
    EDNSInfo,
    EDNSInfo,
    EDNSInfo,
  ];
  const differences: string[] = [];
  if (!client.present && upstreamRequest.present) {
    differences.push(t("客户端请求没有 EDNS，上游请求中出现 EDNS"));
  } else if (client.present && !upstreamRequest.present) {
    differences.push(t("客户端请求携带 EDNS，上游请求中已不存在"));
  }
  if (!client.ecs && upstreamRequest.ecs) {
    differences.push(t("客户端请求中没有 ECS，上游请求中出现 ECS"));
  } else if (client.ecs && !upstreamRequest.ecs) {
    differences.push(t("客户端请求携带 ECS，上游请求中已不存在"));
  } else if (!sameECS(client.ecs, upstreamRequest.ecs)) {
    differences.push(t("上游请求中的 ECS 与客户端请求不同"));
  }
  if (upstreamResponse.present && !response.present) {
    differences.push(t("上游响应携带 EDNS，客户端逻辑响应中已不存在"));
  }
  if (upstreamResponse.ecs && !response.ecs) {
    differences.push(t("上游响应携带 ECS，客户端逻辑响应中已不存在"));
  } else if (!sameECS(upstreamResponse.ecs, response.ecs)) {
    differences.push(t("客户端逻辑响应中的 ECS 与上游原始响应不同"));
  }
  return differences;
}

function QueryLogDetail({
  record,
  deviceName,
  showPrincipal,
  cachePurge,
}: {
  record: QueryRecord;
  deviceName: string;
  showPrincipal: boolean;
  cachePurge: boolean;
}) {
  const differences = ednsDifferences(record);
  return (
    <div className="log-detail">
      <div className="log-detail-summary">
        <span
          className={`badge ${record.rcode === "NOERROR" ? "ok" : "error"}`}
        >
          {record.rcode || "UNKNOWN"}
        </span>
        <span className="badge off">{record.qtype || t("未知类型")}</span>
        {record.cache_hit ? (
          <span className="badge cache">{t("缓存命中")}</span>
        ) : null}
        <time>{logDate(record.time)}</time>
      </div>
      <section>
        <h3>{t("请求")}</h3>
        <dl>
          <div>
            <dt>{t("查询名称")}</dt>
            <dd>
              {record.name}
              {cachePurge && record.name ? (
                <Suspense fallback={null}>
                  <PurgeDomainButton domain={record.name} />
                </Suspense>
              ) : null}
            </dd>
          </div>
          <div>
            <dt>{t("设备")}</dt>
            <dd>
              {deviceName}
              {record.credential_id ? (
                <small>{record.credential_id}</small>
              ) : null}
            </dd>
          </div>
          {showPrincipal ? (
            <div>
              <dt>{t("用户")}</dt>
              <dd>
                {principalName(record)}
                {record.username && record.user_id ? (
                  <small>{record.user_id}</small>
                ) : null}
              </dd>
            </div>
          ) : null}
          <div>
            <dt>{t("客户端 IP")}</dt>
            <dd>{record.client_ip || t("未记录")}</dd>
          </div>
        </dl>
      </section>
      <section>
        <h3>{t("响应")}</h3>
        <dl>
          <div>
            <dt>{t("响应码")}</dt>
            <dd>{record.rcode || "UNKNOWN"}</dd>
          </div>
          <div>
            <dt>Answer IP</dt>
            <dd className="answer-list">
              {record.answer_ips?.length
                ? record.answer_ips.map((address) => (
                    <code key={address}>{address}</code>
                  ))
                : t("无地址记录")}
            </dd>
          </div>
          <div>
            <dt>{t("处理路径")}</dt>
            <dd>{responseSourceName(record.response_source)}</dd>
          </div>
          <div>
            <dt>{t("处理组件")}</dt>
            <dd>{record.response_source_id || t("未记录")}</dd>
          </div>
          <div>
            <dt>{t("出站 DNS")}</dt>
            <dd>{outboundName(record)}</dd>
          </div>
          <div>
            <dt>{t("处理耗时")}</dt>
            <dd>{record.duration_ms.toFixed(2)} ms</dd>
          </div>
        </dl>
      </section>
      <OutboundSection record={record} deviceName={deviceName} />
      <section>
        <h3>{t("传输与 EDNS")}</h3>
        <dl className="transport-summary">
          <div>
            <dt>{t("协议")}</dt>
            <dd>{record.protocol?.toUpperCase() || t("未记录")}</dd>
          </div>
          <div>
            <dt>{t("采集版本")}</dt>
            <dd>
              {record.edns_trace_version
                ? t("四阶段 v{version}", { version: record.edns_trace_version })
                : t("历史记录")}
            </dd>
          </div>
        </dl>
        <div className="edns-stage-grid">
          <EDNSStageCard
            title={t("客户端请求")}
            note={t("进入控制策略与执行链之前")}
            snapshot={record.edns}
            missing={missingEDNSMessage(record, "client_request")}
          />
          <EDNSStageCard
            title={t("最终上游请求")}
            note={t("最终采用交换的协议发送边界")}
            snapshot={record.upstream_request_edns}
            missing={missingEDNSMessage(record, "upstream_request")}
          />
          <EDNSStageCard
            title={t("上游原始响应")}
            note={t("同一次交换解码后、响应后处理之前")}
            snapshot={record.upstream_response_edns}
            missing={missingEDNSMessage(record, "upstream_response")}
          />
          <EDNSStageCard
            title={t("客户端逻辑响应")}
            note={t("全部响应处理后、交给协议写出层之前")}
            snapshot={record.response_edns}
            missing={missingEDNSMessage(record, "client_response")}
          />
        </div>
        {differences.length ? (
          <div className="edns-differences" role="note">
            <strong>{t("阶段变化")}</strong>
            <ul>
              {differences.map((difference) => (
                <li key={difference}>{difference}</li>
              ))}
            </ul>
            <small>
              {t("变化仅来自报文快照，不能据此确定由哪个插件造成。")}
            </small>
          </div>
        ) : null}
      </section>
    </div>
  );
}

export function QueryDetails({
  path,
  enabled,
  credentialsPath,
  showPrincipal = false,
  cachePurge = false,
  title = t("查询日志"),
}: {
  path: string;
  enabled: boolean;
  credentialsPath?: string;
  showPrincipal?: boolean;
  // Admin views offer to purge the selected name from the caches.
  cachePurge?: boolean;
  title?: string;
}) {
  const [draft, setDraft] = useState<QueryFilters>({ ...emptyQueryFilters });
  const [applied, setApplied] = useState<QueryFilters>({
    ...emptyQueryFilters,
  });
  const [selected, setSelected] = useState<QueryRecord | null>(null);
  const [refreshVersion, setRefreshVersion] = useState(0);
  const queryPath = useMemo(() => {
    const to = new Date();
    const from = new Date(
      to.getTime() - Number(applied.hours) * 60 * 60 * 1000,
    );
    const params: Record<string, string> = {
      from: from.toISOString(),
      to: to.toISOString(),
    };
    if (applied.name) params.name = applied.name;
    if (applied.qtype) params.qtype = applied.qtype;
    if (applied.rcode) params.rcode = applied.rcode;
    if (applied.credentialId) params.credential_id = applied.credentialId;
    if (applied.protocol) params.protocol = applied.protocol;
    if (applied.address) params.address = applied.address;
    if (applied.cache !== "all") params.cache = applied.cache;
    if (applied.source !== "all") params.source = applied.source;
    if (applied.upstream) params.upstream = applied.upstream;
    return query(path, params);
  }, [path, applied, refreshVersion]);
  const {
    items: data,
    error,
    loading,
    cursor,
    more,
  } = usePaged<QueryRecord>(queryPath, 0, enabled);
  const credentialLoader = useCallback(
    (signal: AbortSignal) =>
      enabled && credentialsPath
        ? allPages<Credential>(credentialsPath, signal)
        : Promise.resolve([] as Credential[]),
    [credentialsPath, enabled],
  );
  const { data: credentials } = useLoad(credentialLoader, [credentialLoader]);
  const credentialNames = useMemo(
    () => new Map((credentials ?? []).map((item) => [item.id, item.name])),
    [credentials],
  );
  const upstreamLabels = useMemo(
    () =>
      [
        ...new Set(data.map((record) => record.upstream_label).filter(Boolean)),
      ].sort() as string[],
    [data],
  );
  const deviceName = (record: QueryRecord) =>
    record.credential_id
      ? (credentialNames.get(record.credential_id) ??
        record.device_name ??
        t("未知设备"))
      : t("未标识设备");
  function updateFilter(name: keyof QueryFilters, value: string) {
    setDraft((current) => ({ ...current, [name]: value }));
  }
  function applyFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSelected(null);
    setApplied({
      ...draft,
      name: draft.name.trim(),
      address: draft.address.trim(),
      upstream: draft.upstream.trim(),
    });
  }
  function resetFilters() {
    const filters = { ...emptyQueryFilters };
    setDraft(filters);
    setApplied(filters);
    setSelected(null);
    setRefreshVersion((value) => value + 1);
  }

  return (
    <Card title={title} className="query-console">
      {!enabled ? (
        <Empty>
          {t("查询日志未启用。管理员可在服务配置中开启 query_log。")}
        </Empty>
      ) : (
        <>
          <form className="query-filters" onSubmit={applyFilters}>
            <Field label={t("时间范围")}>
              <select
                value={draft.hours}
                onChange={(event) => updateFilter("hours", event.target.value)}
              >
                <option value="1">{t("最近 1 小时")}</option>
                <option value="6">{t("最近 6 小时")}</option>
                <option value="24">{t("最近 24 小时")}</option>
                <option value="168">{t("最近 7 天")}</option>
                <option value="720">{t("最近 30 天")}</option>
              </select>
            </Field>
            <Field label={t("域名")}>
              <input
                value={draft.name}
                onChange={(event) => updateFilter("name", event.target.value)}
                placeholder={t("例如：包含 example.com…")}
                maxLength={255}
              />
            </Field>
            <Field label={t("查询类型")}>
              <select
                value={draft.qtype}
                onChange={(event) => updateFilter("qtype", event.target.value)}
              >
                <option value="">{t("全部类型")}</option>
                {[
                  "A",
                  "AAAA",
                  "HTTPS",
                  "CNAME",
                  "MX",
                  "TXT",
                  "NS",
                  "PTR",
                  "ANY",
                ].map((value) => (
                  <option key={value}>{value}</option>
                ))}
              </select>
            </Field>
            <Field label={t("响应码")}>
              <select
                value={draft.rcode}
                onChange={(event) => updateFilter("rcode", event.target.value)}
              >
                <option value="">{t("全部响应")}</option>
                {["NOERROR", "NXDOMAIN", "SERVFAIL", "REFUSED", "FORMERR"].map(
                  (value) => (
                    <option key={value}>{value}</option>
                  ),
                )}
              </select>
            </Field>
            <Field label={t("设备")}>
              {credentialsPath ? (
                <select
                  value={draft.credentialId}
                  onChange={(event) =>
                    updateFilter("credentialId", event.target.value)
                  }
                >
                  <option value="">{t("全部设备")}</option>
                  {(credentials ?? []).map((credential) => (
                    <option key={credential.id} value={credential.id}>
                      {credential.name}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  value={draft.credentialId}
                  onChange={(event) =>
                    updateFilter("credentialId", event.target.value)
                  }
                  placeholder={t("凭证 ID…")}
                  maxLength={64}
                />
              )}
            </Field>
            <Field label={t("协议")}>
              <select
                value={draft.protocol}
                onChange={(event) =>
                  updateFilter("protocol", event.target.value)
                }
              >
                <option value="">{t("全部协议")}</option>
                {["udp", "tcp", "tls", "quic", "http", "https", "h2", "h3"].map(
                  (value) => (
                    <option key={value} value={value}>
                      {value.toUpperCase()}
                    </option>
                  ),
                )}
              </select>
            </Field>
            <Field label={t("客户端或 Answer IP")}>
              <input
                value={draft.address}
                onChange={(event) =>
                  updateFilter("address", event.target.value)
                }
                placeholder={t("例如：192.0.2.1…")}
              />
            </Field>
            <Field label={t("缓存")}>
              <select
                value={draft.cache}
                onChange={(event) => updateFilter("cache", event.target.value)}
              >
                <option value="all">{t("全部")}</option>
                <option value="hit">{t("命中缓存")}</option>
                <option value="miss">{t("未命中缓存")}</option>
              </select>
            </Field>
            <Field label={t("处理来源")}>
              <select
                value={draft.source}
                onChange={(event) => updateFilter("source", event.target.value)}
              >
                <option value="all">{t("全部来源")}</option>
                {Object.entries(responseSourceNames).map(([value, label]) => (
                  <option key={value} value={value}>
                    {t(label)}
                  </option>
                ))}
              </select>
            </Field>
            <Field label={t("出站 DNS")}>
              <input
                value={draft.upstream}
                onChange={(event) =>
                  updateFilter("upstream", event.target.value)
                }
                placeholder={t("例如：223.5.5.5 (UDP)…")}
                list="query-upstream-labels"
                maxLength={128}
              />
              <datalist id="query-upstream-labels">
                {upstreamLabels.map((label) => (
                  <option key={label} value={label} />
                ))}
              </datalist>
            </Field>
            <div className="query-filter-actions">
              <button type="button" onClick={resetFilters}>
                {t("重置")}
              </button>
              <button className="primary" type="submit">
                {t("查询")}
              </button>
            </div>
          </form>
          <div className="log-list-heading">
            <div>
              <strong>{t("最新记录")}</strong>
              <span>
                {t("已加载 {count} 条，按时间从新到旧", {
                  count: fmt.num(data.length),
                })}
              </span>
            </div>
            <button
              onClick={() => setRefreshVersion((value) => value + 1)}
              disabled={loading}
            >
              {t("刷新")}
            </button>
          </div>
          <Alert error={error} />
          {loading && !data.length ? (
            <Spinner />
          ) : data.length ? (
            <div
              className="table-wrap query-table"
              role="region"
              aria-label={t("查询记录")}
              tabIndex={0}
            >
              <table>
                <thead>
                  <tr>
                    <th>{t("查询")}</th>
                    <th>{t("结果")}</th>
                    <th>{t("出站 DNS")}</th>
                    <th>{t("设备 / 客户端")}</th>
                    <th>Answer IP</th>
                    <th>{t("协议 / 耗时")}</th>
                    <th>{t("时间")}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.map((record) => (
                    <tr key={record.id}>
                      <td className="query-detail" data-label={t("查询")}>
                        <button
                          className="query-name"
                          aria-label={t("查看 {name} 详情", {
                            name: record.name,
                          })}
                          onClick={() => setSelected(record)}
                        >
                          {record.name}
                        </button>
                        <small>{record.qtype}</small>
                      </td>
                      <td data-label={t("结果")}>
                        <span
                          className={`badge ${record.rcode === "NOERROR" ? "ok" : "error"}`}
                        >
                          {record.rcode || "UNKNOWN"}
                        </span>
                        {record.cache_hit ? (
                          <small className="cache-text">{t("缓存命中")}</small>
                        ) : null}
                        <small>
                          {responseSourceName(record.response_source)}
                        </small>
                      </td>
                      <td className="query-detail" data-label={t("出站 DNS")}>
                        {outboundName(record)}
                        {record.trace && !record.cache_hit ? (
                          <small>{routeReason(record)}</small>
                        ) : null}
                        {record.trace && record.trace.attempts.length > 1 ? (
                          <small>
                            {t("共 {count} 次上游请求", {
                              count: record.trace.attempts.length,
                            })}
                          </small>
                        ) : null}
                      </td>
                      <td
                        className="query-detail"
                        data-label={t("设备 / 客户端")}
                      >
                        {deviceName(record)}
                        <small>
                          {record.client_ip || t("未记录客户端 IP")}
                        </small>
                        {showPrincipal ? (
                          <small>
                            {t("用户 {name}", { name: principalName(record) })}
                          </small>
                        ) : null}
                      </td>
                      <td
                        className="query-detail answer-preview"
                        data-label="Answer IP"
                      >
                        {record.answer_ips?.length
                          ? record.answer_ips.join(", ")
                          : t("无地址记录")}
                      </td>
                      <td data-label={t("协议 / 耗时")}>
                        {record.protocol?.toUpperCase() || t("未知")}
                        <small>{record.duration_ms.toFixed(2)} ms</small>
                      </td>
                      <td data-label={t("时间")}>
                        {logDay(record.time)}
                        <small>{logClock(record.time)}</small>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty>{t("当前条件下暂无查询记录")}</Empty>
          )}
          {cursor ? (
            <div className="query-more">
              <button onClick={more} disabled={loading}>
                {loading ? t("正在加载…") : t("加载更多")}
              </button>
            </div>
          ) : null}
          {selected ? (
            <Modal
              title={selected.name || t("查询详情")}
              onClose={() => setSelected(null)}
              className="query-log-modal"
            >
              <QueryLogDetail
                record={selected}
                deviceName={deviceName(selected)}
                showPrincipal={showPrincipal}
                cachePurge={cachePurge}
              />
            </Modal>
          ) : null}
        </>
      )}
    </Card>
  );
}
