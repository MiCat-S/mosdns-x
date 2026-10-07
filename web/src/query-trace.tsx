import type { QueryRecord, RouteStep, UpstreamTry } from "./types";

const branchNames: Record<string, string> = {
  primary: "主要",
  secondary: "备援",
  lazy_refresh: "后台刷新",
};

export function branchName(branch?: string) {
  if (!branch) return "";
  return branch
    .split("/")
    .map((part) => {
      const parallel = /^parallel#(\d+)$/.exec(part);
      if (parallel) return `并行 ${parallel[1]}`;
      return branchNames[part] ?? part;
    })
    .join(" › ");
}

const attemptErrors: Record<string, string> = {
  timeout: "超时",
  canceled: "已取消（其他上游先回应）",
  tls: "TLS 错误",
  connection_refused: "连接被拒绝",
  connection_reset: "连接中断",
  connect_failed: "无法连接",
  resolve_failed: "无法解析上游地址",
  http_status: "HTTP 错误",
  bad_response: "响应无效",
  empty_response: "空响应",
  error: "其他错误",
};

export function attemptResult(attempt: UpstreamTry) {
  if (!attempt.done) return "未完成（已先返回其他结果）";
  if (attempt.error) return attemptErrors[attempt.error] ?? attempt.error;
  return attempt.rcode || "无响应码";
}

const fallbackReasons: Record<string, string> = {
  primary_failed: "主要上游失败，启用备援",
  fast_fallback: "主要上游未及时回应，同时启用备援",
  always_standby: "备援始终同时查询",
};

export function describeStep(step: RouteStep) {
  switch (step.kind) {
    case "if":
      return step.hits?.length
        ? `命中规则 ${step.detail}（${step.hits.join("、")}）`
        : `命中规则 ${step.detail}`;
    case "else":
      return `未命中规则 ${step.detail}，走 else 分支`;
    case "cache_hit":
      return `命中缓存 ${step.detail}`;
    case "lazy_refresh":
      return `缓存已过期，先返回旧结果并在后台刷新 ${step.detail}`;
    case "secondary_started":
      return fallbackReasons[step.detail ?? ""] ?? "启用备援";
    case "branch_selected":
      return `采用${branchName(step.detail) || "该分支"}的结果`;
    case "primary_unhealthy":
      return "主要上游近期失败较多，主要与备援同时查询";
    case "load_balance":
      return `负载均衡选择第 ${step.detail?.replace("#", "")} 组`;
    default:
      return step.detail ? `${step.kind} ${step.detail}` : step.kind;
  }
}

// outboundName names the DNS a query went out to. A cache hit names the
// upstream that produced the cached answer.
export function outboundName(record: QueryRecord) {
  if (record.cache_hit) {
    return record.upstream_label
      ? `缓存 · 原始 ${record.upstream_label}`
      : "缓存（来源未记录）";
  }
  if (record.upstream_label) return record.upstream_label;
  switch (record.response_source) {
    case "hosts":
      return "本地 Hosts";
    case "sequence":
      return "本地处理";
    case "servfail":
      return record.trace?.attempts.length ? "上游全部失败" : "未发出上游请求";
  }
  return record.upstream_id || "未记录";
}

function ms(value: number) {
  return `${value.toFixed(value < 10 ? 2 : 1)} ms`;
}

export function OutboundSection({ record }: { record: QueryRecord }) {
  const trace = record.trace;
  return (
    <section className="outbound-section">
      <h3>出站路径</h3>
      <dl>
        <div>
          <dt>出站 DNS</dt>
          <dd>{outboundName(record)}</dd>
        </div>
      </dl>
      {!trace ? (
        <p className="outbound-empty">历史记录未采集出站路径。</p>
      ) : (
        <>
          <h4>分流过程</h4>
          {trace.steps.length ? (
            <ol className="route-steps">
              {trace.steps.map((step, index) => (
                <li key={index}>
                  <span className="route-time">+{ms(step.at_ms)}</span>
                  {step.branch ? (
                    <span className="badge off">{branchName(step.branch)}</span>
                  ) : null}
                  <span>{describeStep(step)}</span>
                </li>
              ))}
            </ol>
          ) : (
            <p className="outbound-empty">未经过分流规则，按默认路径处理。</p>
          )}
          <h4>上游请求</h4>
          {trace.attempts.length ? (
            <div className="table-wrap">
              <table className="attempt-table">
                <thead>
                  <tr>
                    <th>#</th>
                    <th>DNS</th>
                    <th>分支</th>
                    <th>开始</th>
                    <th>耗时</th>
                    <th>结果</th>
                  </tr>
                </thead>
                <tbody>
                  {trace.attempts.map((attempt) => (
                    <tr
                      key={attempt.seq}
                      className={attempt.selected ? "selected" : undefined}
                    >
                      <td>{attempt.seq}</td>
                      <td>
                        {attempt.upstream}
                        {attempt.selected ? (
                          <span className="badge ok">采用</span>
                        ) : null}
                        {attempt.plugin ? (
                          <small>{attempt.plugin}</small>
                        ) : null}
                      </td>
                      <td>{branchName(attempt.branch) || "—"}</td>
                      <td>+{ms(attempt.start_ms)}</td>
                      <td>{ms(attempt.duration_ms)}</td>
                      <td
                        className={
                          attempt.error || attempt.rcode === "SERVFAIL"
                            ? "attempt-failed"
                            : undefined
                        }
                      >
                        {attemptResult(attempt)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="outbound-empty">
              {record.cache_hit
                ? "本次由缓存直接回应，没有发出上游请求。"
                : "本次没有发出上游请求。"}
            </p>
          )}
          {trace.truncated ? (
            <p className="outbound-empty">记录过多，只保留了前面的部分。</p>
          ) : null}
        </>
      )}
    </section>
  );
}
