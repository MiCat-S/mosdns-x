import type { ReactNode } from "react";

import type { QueryRecord, RouteStep, UpstreamTry } from "./types";

const branchNames: Record<string, string> = {
  primary: "主要",
  secondary: "备援",
  lazy_refresh: "后台刷新",
  reference: "参考查询",
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
  canceled: "已取消",
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

type Tone = "ok" | "fail" | "warn" | "pending" | "neutral" | "info";

function attemptTone(attempt: UpstreamTry): Tone {
  if (!attempt.done) return "pending";
  if (attempt.error || attempt.rcode === "SERVFAIL") return "fail";
  if (attempt.rcode && attempt.rcode !== "NOERROR") return "warn";
  return "ok";
}

const fallbackReasons: Record<string, string> = {
  primary_failed: "主要上游失败，启用备援",
  fast_fallback: "主要上游未及时回应，同时启用备援",
  always_standby: "备援始终同时查询",
};

function isCondition(step: RouteStep) {
  return (
    step.kind === "condition" || step.kind === "if" || step.kind === "else"
  );
}

// Records written before v26.10.07.1 keep only matched conditions ("if") and
// conditions that took their else branch ("else").
function conditionOutcome(step: RouteStep): {
  matched: boolean;
  then: string;
} {
  if (step.kind === "if") return { matched: true, then: "exec" };
  if (step.kind === "else") return { matched: false, then: "else" };
  return { matched: step.matched === true, then: step.then ?? "continue" };
}

export function describeStep(step: RouteStep) {
  switch (step.kind) {
    case "cache_hit":
      return `命中缓存 ${step.detail}`;
    case "lazy_refresh":
      return `缓存已过期，先返回旧结果，并在后台刷新 ${step.detail}`;
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

// routeReason names the rule that chose the path: the matchers that were true
// in the last condition whose branch ran.
export function routeReason(record: QueryRecord) {
  const steps = record.trace?.steps ?? [];
  for (let i = steps.length - 1; i >= 0; i--) {
    const step = steps[i];
    if (!isCondition(step) || conditionOutcome(step).then !== "exec") continue;
    return `命中 ${step.hits?.length ? step.hits.join("、") : step.detail}`;
  }
  return record.trace ? "未命中分流规则" : "";
}

function ms(value: number) {
  return `${value.toFixed(value < 10 ? 1 : 0)} ms`;
}

export function routeSummary(record: QueryRecord) {
  const trace = record.trace;
  if (!trace) return "";
  if (record.cache_hit) {
    return record.upstream_label
      ? `直接命中缓存，原始来源 ${record.upstream_label}，${ms(record.duration_ms)} 返回`
      : `直接命中缓存，${ms(record.duration_ms)} 返回`;
  }
  const reason = routeReason(record);
  const selected = trace.attempts.find((attempt) => attempt.selected);
  if (!selected) {
    return trace.attempts.length
      ? `${reason}，${trace.attempts.length} 次上游请求均未取得可用结果`
      : `${reason}，没有发出上游请求`;
  }
  const fellBack = trace.steps.some(
    (step) =>
      step.kind === "branch_selected" &&
      step.detail?.split("/").pop() === "secondary",
  );
  const why = trace.steps.find(
    (step) => step.kind === "secondary_started",
  )?.detail;
  const note = !fellBack
    ? ""
    : why === "primary_failed"
      ? "（主要上游失败，改用备援）"
      : why === "fast_fallback"
        ? "（主要上游未及时回应，改用备援）"
        : "（采用备援的结果）";
  return `${reason} → ${selected.upstream}，${ms(selected.duration_ms)} 取得结果${note}`;
}

type Token =
  { kind: "matcher"; name: string } | { kind: "operator"; text: string };

const operatorNames: Record<string, string> = {
  "||": "或",
  "&&": "且",
  "!": "非",
};

// expressionTokens splits a condition such as
// `(query_is_local_domain) || [qtype65]` into matchers and operators.
// Parentheses are dropped; the panel shows a flat sequence.
export function expressionTokens(expression: string): Token[] {
  const tokens: Token[] = [];
  const pattern = /\[([^\]]+)\]|([A-Za-z0-9_.-]+)|(\|\||&&|!)/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(expression))) {
    const name = match[1] ?? match[2];
    if (name) {
      tokens.push({ kind: "matcher", name });
    } else if (match[3]) {
      tokens.push({ kind: "operator", text: operatorNames[match[3]] });
    }
  }
  return tokens;
}

const matcherStateNames = {
  hit: "命中",
  miss: "未命中",
  skipped: "未检查（前面的条件已决定结果）",
  unknown: "",
};

function ConditionExpression({ step }: { step: RouteStep }) {
  const hits = new Set(step.hits ?? []);
  const misses = new Set(step.misses ?? []);
  const detailed = step.kind === "condition";
  return (
    <span className="route-expression">
      {expressionTokens(step.detail ?? "").map((token, index) => {
        if (token.kind === "operator") {
          return (
            <span key={index} className="route-operator">
              {token.text}
            </span>
          );
        }
        const state: keyof typeof matcherStateNames = hits.has(token.name)
          ? "hit"
          : misses.has(token.name)
            ? "miss"
            : detailed
              ? "skipped"
              : "unknown";
        const name = matcherStateNames[state];
        return (
          <span
            key={index}
            className={`route-matcher ${state}`}
            title={name || undefined}
            aria-label={name ? `${token.name}：${name}` : token.name}
          >
            {state === "hit" ? (
              <span className="route-check" aria-hidden="true">
                ✓
              </span>
            ) : null}
            {token.name}
          </span>
        );
      })}
    </span>
  );
}

const thenNames: Record<string, string> = {
  exec: "进入此分支",
  else: "进入 else 分支",
  continue: "继续往下",
};

type FlowItem =
  | { kind: "step"; at: number; step: RouteStep }
  | { kind: "attempt"; at: number; attempt: UpstreamTry }
  // A run of conditions that did not match and changed nothing.
  | { kind: "misses"; at: number; steps: RouteStep[] };

function isPlainMiss(step: RouteStep) {
  return (
    step.kind === "condition" &&
    step.matched !== true &&
    (step.then ?? "continue") === "continue"
  );
}

function flowItems(record: QueryRecord): FlowItem[] {
  const trace = record.trace;
  if (!trace) return [];
  const sorted: FlowItem[] = [
    ...trace.steps.map((step) => ({
      kind: "step" as const,
      at: step.at_ms,
      step,
    })),
    ...trace.attempts.map((attempt) => ({
      kind: "attempt" as const,
      at: attempt.start_ms,
      attempt,
    })),
  ].sort((a, b) => a.at - b.at);
  // Collapse runs of two or more plain misses in the same branch: they are
  // usually most of the steps, and only the conditions that changed the path
  // need to be read.
  const items: FlowItem[] = [];
  for (const item of sorted) {
    const last = items[items.length - 1];
    if (item.kind === "step" && isPlainMiss(item.step)) {
      if (
        last?.kind === "misses" &&
        last.steps[0].branch === item.step.branch
      ) {
        last.steps.push(item.step);
        continue;
      }
      if (
        last?.kind === "step" &&
        isPlainMiss(last.step) &&
        last.step.branch === item.step.branch
      ) {
        items[items.length - 1] = {
          kind: "misses",
          at: last.at,
          steps: [last.step, item.step],
        };
        continue;
      }
    }
    items.push(item);
  }
  return items;
}

function FlowNode({
  shape,
  tone,
  aside,
  asideTone,
  branch,
  children,
}: {
  shape: "point" | "decision" | "upstream" | "event";
  tone: Tone;
  aside?: ReactNode;
  asideTone?: Tone;
  branch?: string;
  children: ReactNode;
}) {
  return (
    <li className={`route-node ${shape} ${tone}`}>
      <span className="route-marker" aria-hidden="true" />
      <div className="route-content">
        {branch ? (
          <span className="route-branch">{branchName(branch)}</span>
        ) : null}
        {children}
      </div>
      {aside ? (
        <span className={`route-aside ${asideTone ?? ""}`}>{aside}</span>
      ) : null}
    </li>
  );
}

function ConditionNode({ step }: { step: RouteStep }) {
  const outcome = conditionOutcome(step);
  return (
    <FlowNode
      shape="decision"
      tone={outcome.matched ? "ok" : "neutral"}
      branch={step.branch}
      aside={`${outcome.matched ? "是" : "否"} · ${thenNames[outcome.then]}`}
      asideTone={outcome.matched ? "ok" : "neutral"}
    >
      <ConditionExpression step={step} />
    </FlowNode>
  );
}

function MissesNode({ steps }: { steps: RouteStep[] }) {
  return (
    <li className="route-node decision neutral">
      <span className="route-marker" aria-hidden="true" />
      <details className="route-misses">
        <summary>
          {steps[0].branch ? (
            <span className="route-branch">{branchName(steps[0].branch)}</span>
          ) : null}
          未命中 {steps.length} 条规则
        </summary>
        <ul>
          {steps.map((step, index) => (
            <li key={index}>
              <ConditionExpression step={step} />
            </li>
          ))}
        </ul>
      </details>
      <span className="route-aside neutral">否 · 继续往下</span>
    </li>
  );
}

function StepNode({ record, step }: { record: QueryRecord; step: RouteStep }) {
  if (isCondition(step)) return <ConditionNode step={step} />;
  if (step.kind === "cache_hit") {
    return (
      <FlowNode
        shape="upstream"
        tone="ok"
        branch={step.branch}
        aside={`+${ms(step.at_ms)}`}
      >
        <span className="route-label">命中缓存</span>
        <span>
          {record.cache_hit && record.upstream_label
            ? `原始来源 ${record.upstream_label}`
            : "原始来源未记录"}
        </span>
        <span className="route-muted">{step.detail}</span>
      </FlowNode>
    );
  }
  const warn =
    step.kind === "secondary_started" || step.kind === "primary_unhealthy";
  return (
    <FlowNode
      shape="event"
      tone={warn ? "warn" : "neutral"}
      branch={step.branch}
      aside={`+${ms(step.at_ms)}`}
    >
      <span>{describeStep(step)}</span>
    </FlowNode>
  );
}

function AttemptNode({ attempt }: { attempt: UpstreamTry }) {
  const tone = attemptTone(attempt);
  return (
    <FlowNode
      shape="upstream"
      tone={tone}
      branch={attempt.branch}
      asideTone={tone}
      aside={
        <>
          {ms(attempt.duration_ms)} · {attemptResult(attempt)}
          {attempt.selected ? " · 采用" : ""}
        </>
      }
    >
      <span className="route-label">出站 DNS</span>
      <span>{attempt.upstream}</span>
      {attempt.plugin ? (
        <span className="route-muted">{attempt.plugin}</span>
      ) : null}
    </FlowNode>
  );
}

function RouteFlow({
  record,
  deviceName,
}: {
  record: QueryRecord;
  deviceName?: string;
}) {
  const answers = record.answer_ips?.length ?? 0;
  const ok = record.rcode === "NOERROR";
  return (
    <ol className="route-flow" aria-label="查询路径">
      <FlowNode shape="point" tone="info">
        <span className="route-label">收到查询</span>
        <span>{record.name}</span>
        <span className="route-muted">
          {record.qtype} · {record.protocol?.toUpperCase() || "未知协议"}
          {deviceName ? ` · ${deviceName}` : ""}
        </span>
      </FlowNode>
      {flowItems(record).map((item, index) => {
        switch (item.kind) {
          case "attempt":
            return (
              <AttemptNode
                key={`a${item.attempt.seq}`}
                attempt={item.attempt}
              />
            );
          case "misses":
            return <MissesNode key={`m${index}`} steps={item.steps} />;
          default:
            return (
              <StepNode key={`s${index}`} record={record} step={item.step} />
            );
        }
      })}
      <FlowNode
        shape="point"
        tone={ok ? "ok" : "fail"}
        aside={`共 ${ms(record.duration_ms)}`}
        asideTone={ok ? "ok" : "fail"}
      >
        <span className="route-label">返回</span>
        <span className={ok ? "route-ok" : "route-fail"}>
          {record.rcode || "UNKNOWN"}
        </span>
        <span className="route-muted">
          {answers ? `${answers} 个地址` : "无地址记录"}
        </span>
      </FlowNode>
    </ol>
  );
}

// axisMs labels the waterfall axis with enough precision that its ticks
// differ even when the requests span a millisecond or two.
function axisMs(value: number, scale: number) {
  const digits = scale < 2 ? 2 : scale < 20 ? 1 : 0;
  return `${value.toFixed(digits)} ms`;
}

function AttemptWaterfall({ record }: { record: QueryRecord }) {
  const trace = record.trace;
  if (!trace || trace.attempts.length < 2) return null;
  // The axis starts at the first request; time spent before it, such as
  // admission and rule matching, would only push every bar to the right.
  const origin = Math.min(...trace.attempts.map((attempt) => attempt.start_ms));
  const end = Math.max(
    ...trace.attempts.map((attempt) => attempt.start_ms + attempt.duration_ms),
  );
  const scale = end - origin > 0 ? end - origin : 1;
  const pct = (value: number) =>
    `${Math.max(0, Math.min(100, (value / scale) * 100))}%`;
  const markers = trace.steps.filter(
    (step) => step.kind === "secondary_started",
  );
  return (
    <figure className="waterfall" aria-label="上游请求时间轴">
      <figcaption>
        上游请求时间轴 <span className="route-muted">从第一次请求起计时</span>
      </figcaption>
      <div className="waterfall-grid">
        <span />
        <div className="waterfall-axis">
          <span>0</span>
          <span>{axisMs(scale / 2, scale)}</span>
          <span>{axisMs(scale, scale)}</span>
        </div>
        <span />
        {trace.attempts.map((attempt) => {
          const tone = attemptTone(attempt);
          return [
            <div className="waterfall-label" key={`l${attempt.seq}`}>
              {attempt.branch ? (
                <span className="route-branch">
                  {branchName(attempt.branch)}
                </span>
              ) : null}
              <span>{attempt.upstream}</span>
            </div>,
            <div className="waterfall-track" key={`t${attempt.seq}`}>
              {markers.map((marker, index) => (
                <span
                  key={index}
                  className="waterfall-marker"
                  style={{ left: pct(marker.at_ms - origin) }}
                />
              ))}
              <span
                className={`waterfall-bar ${tone}${attempt.selected ? " selected" : ""}`}
                style={{
                  left: pct(attempt.start_ms - origin),
                  width: `max(3px, ${pct(attempt.duration_ms)})`,
                }}
              />
            </div>,
            <div className={`waterfall-result ${tone}`} key={`r${attempt.seq}`}>
              {ms(attempt.duration_ms)} · {attemptResult(attempt)}
              {attempt.selected ? " · 采用" : ""}
            </div>,
          ];
        })}
      </div>
      {markers.map((marker, index) => (
        <p className="waterfall-note" key={index}>
          <span className="waterfall-marker-key" aria-hidden="true" />
          第一次请求后 {axisMs(marker.at_ms - origin, scale)}：
          {describeStep(marker)}
        </p>
      ))}
    </figure>
  );
}

export function OutboundSection({
  record,
  deviceName,
}: {
  record: QueryRecord;
  deviceName?: string;
}) {
  const trace = record.trace;
  return (
    <section className="outbound-section">
      <h3>查询路径</h3>
      {!trace ? (
        <>
          <dl>
            <div>
              <dt>出站 DNS</dt>
              <dd>{outboundName(record)}</dd>
            </div>
          </dl>
          <p className="outbound-empty">历史记录未采集查询路径。</p>
        </>
      ) : (
        <>
          <p className="route-summary">{routeSummary(record)}</p>
          <p className="route-legend" aria-hidden="true">
            <span>图例</span>
            <span className="route-matcher hit">
              <span className="route-check">✓</span>命中
            </span>
            <span className="route-matcher miss">未命中</span>
            <span className="route-matcher skipped">未检查</span>
          </p>
          <RouteFlow record={record} deviceName={deviceName} />
          <AttemptWaterfall record={record} />
          {trace.truncated ? (
            <p className="outbound-empty">记录过多，只保留了前面的部分。</p>
          ) : null}
        </>
      )}
    </section>
  );
}
