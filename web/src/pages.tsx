import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  Link,
  Navigate,
  useLocation,
  useNavigate,
  useParams,
} from "react-router-dom";
import { allPages, APIError, json, message, request } from "./api";
import {
  Alert,
  Card,
  Chart,
  Empty,
  Field,
  Metric,
  Modal,
  PageTitle,
  Spinner,
  fmt,
  useLoad,
} from "./components";
import { useSession } from "./session";
import { HealthPanel } from "./health-panel";
import { OutboundSection, outboundName, routeReason } from "./query-trace";
export { RuntimeConfigPage } from "./runtime-config";
import type {
  Audit,
  Credential,
  DeviceUsagePoint,
  IssuedCredential,
  Me,
  Page,
  EDNSInfo,
  QueryRecord,
  Stats,
  SystemInfo,
  UsagePoint,
  User,
} from "./types";

const range = () => {
  const to = new Date(),
    from = new Date(to.getTime() - 24 * 60 * 60 * 1000);
  return { from: from.toISOString(), to: to.toISOString() };
};
const query = (path: string, params: Record<string, string>) =>
  `${path}${path.includes("?") ? "&" : "?"}${new URLSearchParams(params)}`;
export function toLocalDateTime(value: string) {
  if (!value || value.startsWith("0001-")) return "";
  const d = new Date(value);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
export function fromLocalDateTime(value: string) {
  return value ? new Date(value).toISOString() : "0001-01-01T00:00:00Z";
}
export function usageSummary(points: UsagePoint[]) {
  const byMinute = new Map<number, number>();
  let total = 0;
  for (const p of points) {
    total += p.count;
    const minute = Math.floor(new Date(p.minute).getTime() / 60000) * 60000;
    byMinute.set(minute, (byMinute.get(minute) ?? 0) + p.count);
  }
  const series = [...byMinute.entries()]
    .sort(([a], [b]) => a - b)
    .map(([time, count]) => ({
      time: new Date(time).toISOString(),
      completed: count,
      failed: 0,
      cache_hits: 0,
    }));
  const previousMinute = Math.floor(Date.now() / 60000) * 60000 - 60000;
  const recent = byMinute.get(previousMinute) ?? 0;
  return { total, series, recentPerSecond: recent / 60 };
}
export function successRate(stats: Pick<Stats, "completed" | "failed">) {
  return stats.completed
    ? Math.max(0, stats.completed - stats.failed) / stats.completed
    : 0;
}

function usePaged<T>(path: string, version = 0, enabled = true) {
  const [items, setItems] = useState<T[]>([]);
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const load = useCallback(
    async (next = "") => {
      const current = ++generation.current;
      controller.current?.abort();
      controller.current = new AbortController();
      setLoading(true);
      setError("");
      try {
        const params = new URLSearchParams({ limit: "100" });
        if (next) params.set("cursor", next);
        const page = await request<Page<T>>(
          `${path}${path.includes("?") ? "&" : "?"}${params}`,
          { signal: controller.current.signal },
        );
        if (generation.current === current) {
          setItems((old) => (next ? [...old, ...page.items] : page.items));
          setCursor(page.next_cursor ?? "");
        }
      } catch (e) {
        if (generation.current === current) setError(message(e));
      } finally {
        if (generation.current === current) setLoading(false);
      }
    },
    [path],
  );
  useEffect(() => {
    setItems([]);
    if (enabled) void load();
    else setLoading(false);
    return () => {
      controller.current?.abort();
      generation.current++;
    };
  }, [load, version, enabled]);
  return { items, cursor, loading, error, more: () => load(cursor) };
}

export function Login() {
  const { session, loading, error: sessionError, retry, login } = useSession();
  const navigate = useNavigate();
  const location = useLocation();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  if (loading) return <Spinner />;
  if (sessionError)
    return (
      <div className="login">
        <section>
          <h1>无法连接服务</h1>
          <Alert error={sessionError} />
          <button className="primary" onClick={retry}>
            重试
          </button>
        </section>
      </div>
    );
  if (session)
    return (
      <Navigate
        to={session.user.role === "admin" ? "/admin" : "/app"}
        replace
      />
    );
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const f = new FormData(e.currentTarget);
    try {
      const s = await login(
        String(f.get("username")),
        String(f.get("password")),
      );
      const requested = (
        location.state as { from?: { pathname?: string } } | null
      )?.from?.pathname;
      navigate(
        requested &&
          requested.startsWith(s.user.role === "admin" ? "/admin" : "/app")
          ? requested
          : s.user.role === "admin"
            ? "/admin"
            : "/app",
        { replace: true },
      );
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="login">
      <div className="login-shell">
        <section>
          <div className="login-app-icon" aria-hidden>
            M
          </div>
          <p className="login-kicker" translate="no">
            MOSDNS X
          </p>
          <h1>登录网络控制台</h1>
          <p>管理 DNS 服务、访问策略与运行状态。</p>
          <Alert error={error} />
          <form onSubmit={submit}>
            <Field label="用户名">
              <input
                name="username"
                autoComplete="username"
                required
                placeholder="请输入用户名…"
              />
            </Field>
            <Field label="密码">
              <input
                name="password"
                type="password"
                autoComplete="current-password"
                required
                placeholder="请输入密码…"
              />
            </Field>
            <button className="primary wide login-submit" disabled={busy}>
              {busy ? "正在登录…" : "登录"}
            </button>
          </form>
          <p className="login-footnote">凭证仅用于当前控制台会话</p>
        </section>
      </div>
    </div>
  );
}

function StatsBlocks({ stats, usage }: { stats: Stats; usage?: UsagePoint[] }) {
  const summary = usage ? usageSummary(usage) : undefined;
  return (
    <>
      {summary ? (
        <>
          <div className="metrics">
            <Metric
              label="已受理请求"
              value={fmt.num(summary.total)}
              hint="以持久化计量为准"
            />
            <Metric
              label="最近完整分钟平均"
              value={`${summary.recentPerSecond.toFixed(2)} 次/秒`}
              hint="一分钟平均值"
            />
          </div>
          <Card title="已受理用量趋势">
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
        <Metric label="处理完成" value={fmt.num(stats.completed)} />
        <Metric label="成功率" value={fmt.pct(successRate(stats), 1)} />
        <Metric
          label="缓存命中"
          value={fmt.pct(stats.cache_hits, stats.completed)}
        />
        <Metric
          label="平均延迟"
          value={`${stats.avg_latency_ms.toFixed(1)} ms`}
          hint={`P95 ${stats.p95_latency_ms.toFixed(1)} ms`}
        />
        <Metric
          label="统计缺失"
          value={fmt.num(stats.dropped)}
          hint="异步采集未记录数量"
        />
      </div>
      <Card title="处理结果趋势">
        <Chart series={stats.series} from={stats.from} to={stats.to} />
        <p className="caption">
          统计窗口 {fmt.date(stats.from)} 至 {fmt.date(stats.to)} · 更新于{" "}
          {fmt.date(stats.updated_at)}
        </p>
      </Card>
      <div className="grid2">
        <Card title="响应结果">
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
        <Card title="上游服务">
          {stats.upstreams.length ? (
            <div className="rows">
              {stats.upstreams.map((u) => (
                <div key={u.id}>
                  <span>
                    {u.id}
                    <small>
                      {fmt.num(u.attempts)} 次尝试 · {fmt.num(u.failures)}{" "}
                      次失败
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
      </div>
    </>
  );
}
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
  const { data, error, loading } = useLoad(load, [load]);
  return (
    <>
      <PageTitle
        title="服务总览"
        description="全局 DNS 请求、响应与上游运行状态"
        action={<button onClick={() => setVersion((x) => x + 1)}>刷新</button>}
      />
      {loading ? <Spinner /> : <Alert error={error} />}{" "}
      {data ? <StatsBlocks stats={data[0]} usage={data[1]} /> : null}
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
  const { data, error, loading } = useLoad(loader, [loader]);
  return (
    <>
      <PageTitle
        title="查询日志"
        description="筛选全站 DNS 查询，并查看客户端、响应地址和 EDNS 详情"
        action={
          <button onClick={() => setVersion((value) => value + 1)}>
            刷新统计
          </button>
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data ? (
        <>
          <div className="metrics log-metrics">
            <Metric label="处理完成" value={fmt.num(data.completed)} />
            <Metric label="失败" value={fmt.num(data.failed)} />
            <Metric
              label="缓存命中"
              value={fmt.pct(data.cache_hits, data.completed)}
            />
            <Metric
              label="平均延迟"
              value={`${data.avg_latency_ms.toFixed(1)} ms`}
              hint={`P95 ${data.p95_latency_ms.toFixed(1)} ms`}
            />
          </div>
          <QueryDetails
            path="/admin/queries"
            enabled={data.query_log_enabled}
            showPrincipal
          />
        </>
      ) : null}
    </>
  );
}

const blank = {
  username: "",
  password: "",
  role: "user",
  enabled: true,
  expires_at: "",
  period: "monthly",
  timezone: "UTC",
  limit: 100000,
  qps: 10,
  burst: 0,
  max_credentials: 5,
};
function UserForm({
  initial,
  onDone,
  onCancel,
}: {
  initial?: User;
  onDone: (u: User) => void;
  onCancel: () => void;
}) {
  const edit = Boolean(initial);
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const f = new FormData(e.currentTarget);
    const values = {
      enabled: f.get("enabled") === "on",
      expires_at: fromLocalDateTime(String(f.get("expires_at"))),
      period: String(f.get("period")),
      timezone: String(f.get("timezone")),
      limit: Number(f.get("limit")),
      qps: Number(f.get("qps")),
      burst: Number(f.get("burst")),
      max_credentials: Number(f.get("max_credentials")),
    };
    try {
      let body: Record<string, unknown> = {
        ...values,
        username: String(f.get("username")),
        password: String(f.get("password")),
        role: String(f.get("role")),
      };
      if (edit) {
        body = {};
        for (const key of [
          "enabled",
          "period",
          "timezone",
          "limit",
          "qps",
          "burst",
          "max_credentials",
        ] as const) {
          if (values[key] !== initial![key]) body[key] = values[key];
        }
        if (
          String(f.get("expires_at")) !== toLocalDateTime(initial!.expires_at)
        )
          body.expires_at = values.expires_at;
      }
      const user = edit
        ? await request<User>(
            `/admin/users/${initial!.id}`,
            json("PATCH", body),
          )
        : await request<User>("/admin/users", json("POST", body));
      onDone(user);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  const v = initial ?? blank;
  return (
    <form onSubmit={submit}>
      <Alert error={error} />
      <div className="form-grid">
        {!edit ? (
          <>
            <Field label="用户名">
              <input name="username" required autoComplete="off" />
            </Field>
            <Field label="初始密码">
              <input
                name="password"
                type="password"
                required
                minLength={12}
                maxLength={1024}
                autoComplete="new-password"
              />
            </Field>
            <Field label="角色">
              <select name="role" defaultValue={v.role}>
                <option value="user">普通用户</option>
                <option value="admin">管理员</option>
              </select>
            </Field>
          </>
        ) : null}
        <Field label="周期">
          <select name="period" defaultValue={v.period}>
            <option value="daily">每日</option>
            <option value="monthly">每月</option>
          </select>
        </Field>
        <Field label="时区">
          <input name="timezone" defaultValue={v.timezone} required />
        </Field>
        <Field label="到期时间" hint="留空表示长期有效">
          <input
            name="expires_at"
            type="datetime-local"
            defaultValue={toLocalDateTime(v.expires_at)}
          />
        </Field>
        <Field label="请求额度">
          <input
            name="limit"
            type="number"
            min="1"
            max={Number.MAX_SAFE_INTEGER}
            step="1"
            defaultValue={v.limit}
            required
          />
        </Field>
        <Field label="QPS">
          <input
            name="qps"
            type="number"
            min="1"
            step="1"
            defaultValue={v.qps}
            required
          />
        </Field>
        <Field
          label="额外突发量"
          hint="允许在 QPS 之外额外突发，0 表示不额外放行"
        >
          <input
            name="burst"
            type="number"
            min="0"
            step="1"
            defaultValue={v.burst}
            required
          />
        </Field>
        <Field label="最多凭证">
          <input
            name="max_credentials"
            type="number"
            min="1"
            step="1"
            defaultValue={v.max_credentials}
            required
          />
        </Field>
        <label className="check">
          <input name="enabled" type="checkbox" defaultChecked={v.enabled} />
          启用账户
        </label>
      </div>
      <div className="actions">
        <button type="button" onClick={onCancel}>
          取消
        </button>
        <button className="primary" disabled={busy}>
          {busy ? "正在保存…" : "保存"}
        </button>
      </div>
    </form>
  );
}
export function Users() {
  const [version, setVersion] = useState(0),
    [modal, setModal] = useState(false);
  const {
    items: data,
    error,
    loading,
    cursor,
    more,
  } = usePaged<User>("/admin/users", version);
  const nav = useNavigate();
  return (
    <>
      <PageTitle
        title="用户管理"
        description="设置账户状态、服务期限、额度与速率"
        action={
          <button className="primary" onClick={() => setModal(true)}>
            ＋ 开设账户
          </button>
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />}{" "}
      {data?.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>用户</th>
                <th>状态</th>
                <th>周期额度</th>
                <th>QPS / 突发</th>
                <th>到期</th>
                <th>
                  <span className="sr-only">操作</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {data.map((u) => (
                <tr key={u.id}>
                  <td>
                    <strong>{u.username}</strong>
                    <small>{u.role === "admin" ? "管理员" : "普通用户"}</small>
                  </td>
                  <td>
                    <span className={`badge ${u.enabled ? "ok" : "off"}`}>
                      {u.enabled ? "启用" : "停用"}
                    </span>
                  </td>
                  <td>
                    {fmt.num(u.limit)} / {u.period === "daily" ? "日" : "月"}
                  </td>
                  <td>
                    {u.qps} / {u.burst}
                  </td>
                  <td>{fmt.date(u.expires_at)}</td>
                  <td>
                    <button
                      className="link"
                      onClick={() => nav(`/admin/users/${u.id}`)}
                    >
                      查看
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : !loading && !error ? (
        <Empty>尚未开设账户</Empty>
      ) : null}
      {cursor ? (
        <button onClick={more} disabled={loading}>
          {loading ? "正在加载…" : "加载更多"}
        </button>
      ) : null}
      {modal ? (
        <Modal title="开设账户" onClose={() => setModal(false)}>
          <UserForm
            onCancel={() => setModal(false)}
            onDone={() => {
              setModal(false);
              setVersion((x) => x + 1);
            }}
          />
        </Modal>
      ) : null}
    </>
  );
}

function Secret({
  issued,
  onClose,
}: {
  issued: IssuedCredential;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState("");
  async function copy(label: string, value: string) {
    await navigator.clipboard.writeText(value);
    setCopied(label);
  }
  return (
    <Modal title="凭证已签发" onClose={onClose}>
      <div className="notice">请现在保存。关闭后将无法再次查看令牌。</div>
      <Field label="DNS 地址">
        <div className="copy">
          <code>{issued.doh_url}</code>
          <button type="button" onClick={() => copy("地址", issued.doh_url)}>
            复制
          </button>
        </div>
      </Field>
      <Field label="Bearer 令牌">
        <div className="copy">
          <code>{issued.token}</code>
          <button type="button" onClick={() => copy("令牌", issued.token)}>
            复制
          </button>
        </div>
      </Field>
      {copied ? <p role="status">已复制{copied}</p> : null}
      <Card title="接入说明">
        <p>可直接使用上方专属地址，或在固定 DNS 地址的请求中添加：</p>
        <code>Authorization: Bearer &lt;令牌&gt;</code>
      </Card>
      <div className="actions">
        <button className="primary" onClick={onClose}>
          我已保存，关闭
        </button>
      </div>
    </Modal>
  );
}
export function credentialStatus(c: Credential) {
  if (c.revoked_at && !c.revoked_at.startsWith("0001-")) return "已撤销";
  if (
    c.expires_at &&
    !c.expires_at.startsWith("0001-") &&
    new Date(c.expires_at).getTime() <= Date.now()
  )
    return "已到期";
  return "有效";
}
export function credentialCanRevoke(c: Credential) {
  return credentialStatus(c) !== "已撤销";
}
function CredentialManager({ base, max }: { base: string; max?: number }) {
  const [version, setVersion] = useState(0),
    [issued, setIssued] = useState<IssuedCredential | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState("");
  const load = useCallback(
    (s: AbortSignal) => allPages<Credential>(`${base}/credentials`, s),
    [base, version],
  );
  const { data, loading, error: loadError } = useLoad(load, [load]);
  async function create(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    setBusy("create");
    setError("");
    try {
      const expires = String(f.get("expires_at"));
      const result = await request<IssuedCredential>(
        `${base}/credentials`,
        json("POST", {
          name: String(f.get("name")),
          ...(expires ? { expires_at: new Date(expires).toISOString() } : {}),
        }),
      );
      setIssued(result);
      form.reset();
      setVersion((x) => x + 1);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  }
  async function rotate(c: Credential) {
    if (
      !confirm(
        `轮换凭证“${c.name}”？旧令牌会立即失效，使用它的设备将停止解析。`,
      )
    )
      return;
    setBusy(c.id);
    try {
      setIssued(
        await request<IssuedCredential>(`${base}/credentials/${c.id}/rotate`, {
          method: "POST",
        }),
      );
      setVersion((x) => x + 1);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  }
  async function revoke(c: Credential) {
    if (!confirm(`撤销凭证“${c.name}”？此操作会立即中断使用它的设备。`)) return;
    setBusy(c.id);
    try {
      await request(`${base}/credentials/${c.id}`, { method: "DELETE" });
      setVersion((x) => x + 1);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  }
  const activeCount =
    data?.filter((c) => credentialStatus(c) === "有效").length ?? 0;
  return (
    <>
      <Card title="创建凭证">
        <form className="inline-form" onSubmit={create}>
          <Field label="名称">
            <input
              name="name"
              required
              maxLength={80}
              autoComplete="off"
              placeholder="例如：家中路由器…"
            />
          </Field>
          <Field label="单独到期" hint="留空则跟随账户">
            <input name="expires_at" type="datetime-local" />
          </Field>
          <button
            className="primary"
            disabled={
              busy === "create" || (max !== undefined && activeCount >= max)
            }
          >
            {busy === "create" ? "正在创建…" : "创建凭证"}
          </button>
        </form>
        {max !== undefined ? (
          <p className="caption">
            已使用 {activeCount} / {max} 个凭证名额
          </p>
        ) : null}
      </Card>
      <Alert error={error || loadError} />
      {loading ? (
        <Spinner />
      ) : data?.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>名称</th>
                <th>创建</th>
                <th>单独到期</th>
                <th>状态</th>
                <th>
                  <span className="sr-only">操作</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {data.map((c) => {
                const status = credentialStatus(c);
                const active = status === "有效";
                return (
                  <tr key={c.id}>
                    <td>
                      <strong>{c.name}</strong>
                    </td>
                    <td>{fmt.date(c.created_at)}</td>
                    <td>{fmt.date(c.expires_at)}</td>
                    <td>
                      <span className={`badge ${active ? "ok" : "off"}`}>
                        {status}
                      </span>
                    </td>
                    <td className="table-actions">
                      <button
                        disabled={busy === c.id || !active}
                        onClick={() => rotate(c)}
                      >
                        轮换
                      </button>
                      <button
                        className="danger"
                        disabled={busy === c.id || !credentialCanRevoke(c)}
                        onClick={() => revoke(c)}
                      >
                        撤销
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty>暂无凭证</Empty>
      )}
      {issued ? (
        <Secret issued={issued} onClose={() => setIssued(null)} />
      ) : null}
    </>
  );
}

export function UserDetail() {
  const { id = "" } = useParams();
  return <UserDetailContent key={id} id={id} />;
}

function UserDetailContent({ id }: { id: string }) {
  const [version, setVersion] = useState(0),
    [edit, setEdit] = useState(false),
    [resetPassword, setResetPassword] = useState(false),
    [removing, setRemoving] = useState(false);
  const { session } = useSession();
  const nav = useNavigate();
  const params = useMemo(range, [id]);
  const base = `/admin/users/${encodeURIComponent(id)}`;
  const load = useCallback(
    (s: AbortSignal) =>
      Promise.all([
        request<Me>(base, { signal: s }),
        request<Stats>(query("/admin/stats", { ...params, user_id: id }), {
          signal: s,
        }),
        allPages<UsagePoint>(
          query("/admin/usage", { ...params, user_id: id }),
          s,
        ),
      ]),
    [base, id, params, version],
  );
  const { data, error, loading } = useLoad(load, [load]);
  const account = data?.[0];
  return (
    <>
      <PageTitle
        title={account?.user.username ?? "用户详情"}
        description="账户状态、当前额度与代管设备凭证"
        action={
          account ? (
            <div className="actions">
              <button onClick={() => setResetPassword(true)}>重置密码</button>
              <button onClick={() => setEdit(true)}>编辑账户</button>
              {session?.user.id !== id ? (
                <button className="danger" onClick={() => setRemoving(true)}>
                  删除用户
                </button>
              ) : null}
            </div>
          ) : null
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />}{" "}
      {account ? (
        <>
          <div className="metrics">
            <Metric
              label="剩余额度"
              value={fmt.num(account.quota.remaining)}
              hint={`已用 ${fmt.num(account.quota.used)} / ${fmt.num(account.quota.limit)}`}
            />
            <Metric
              label="重置时间"
              value={fmt.date(account.quota.period_end)}
            />
            <Metric
              label="账户到期"
              value={fmt.date(account.user.expires_at)}
            />
            <Metric
              label="QPS / 突发"
              value={`${account.user.qps} / ${account.user.burst}`}
            />
          </div>
          <CredentialManager base={base} max={account.user.max_credentials} />
          <StatsBlocks stats={data[1]} usage={data[2]} />
          <DeviceUsage
            path={`${base}/device-usage`}
            credentialsPath={`${base}/credentials`}
          />
          <QueryDetails
            path={`/admin/queries?user_id=${encodeURIComponent(id)}`}
            enabled={data[1].query_log_enabled}
            credentialsPath={`${base}/credentials`}
          />
          {edit ? (
            <Modal title="编辑账户" onClose={() => setEdit(false)}>
              <UserForm
                initial={account.user}
                onCancel={() => setEdit(false)}
                onDone={() => {
                  setEdit(false);
                  setVersion((x) => x + 1);
                }}
              />
            </Modal>
          ) : null}
          {resetPassword ? (
            <Modal title="重置用户密码" onClose={() => setResetPassword(false)}>
              <AdminPasswordReset
                base={base}
                username={account.user.username}
                onDone={() => setResetPassword(false)}
              />
            </Modal>
          ) : null}
          {removing ? (
            <Modal title="删除用户" onClose={() => setRemoving(false)}>
              <DeleteUser
                base={base}
                username={account.user.username}
                onCancel={() => setRemoving(false)}
                onDone={() => nav("/admin/users", { replace: true })}
              />
            </Modal>
          ) : null}
        </>
      ) : null}
    </>
  );
}

export function deleteUserError(e: unknown) {
  if (e instanceof APIError && e.status === 409)
    return "不能删除自己，也不能删除最后一个启用的管理员。";
  if (e instanceof APIError && e.status === 404) return "该用户已不存在。";
  if (e instanceof APIError && e.status === 503)
    return "账户可能已删除，但查询日志未能清除。请稍后再次删除以完成清理。";
  return message(e);
}

function DeleteUser({
  base,
  username,
  onCancel,
  onDone,
}: {
  base: string;
  username: string;
  onCancel: () => void;
  onDone: () => void;
}) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function remove() {
    setBusy(true);
    setError("");
    try {
      await request(base, { method: "DELETE" });
      onDone();
    } catch (e) {
      setError(deleteUserError(e));
      setBusy(false);
    }
  }
  return (
    <div>
      <p>
        永久删除 <strong>{username}</strong>
        及其全部数据：设备凭证、登录会话、用量记录、查询日志，以及与该用户相关的审计记录。
      </p>
      <p>使用该用户凭证的设备会立即停止解析。此操作无法撤销。</p>
      <Alert error={error} />
      <div className="actions">
        <button onClick={onCancel} disabled={busy}>
          取消
        </button>
        <button className="danger" onClick={remove} disabled={busy}>
          {busy ? "正在删除…" : "永久删除"}
        </button>
      </div>
    </div>
  );
}

function AdminPasswordReset({
  base,
  username,
  onDone,
}: {
  base: string;
  username: string;
  onDone: () => void;
}) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    if (f.get("new_password") !== f.get("confirm")) {
      setError("两次输入的新密码不一致。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await request(
        `${base}/password`,
        json("POST", { new_password: f.get("new_password") }),
      );
      onDone();
    } catch (e) {
      setError(message(e));
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit}>
      <p>重置 {username} 的密码会撤销该用户的全部面板会话。</p>
      <Alert error={error} />
      <Field label="新密码">
        <input
          name="new_password"
          type="password"
          minLength={12}
          maxLength={1024}
          autoComplete="new-password"
          required
        />
      </Field>
      <Field label="确认新密码">
        <input
          name="confirm"
          type="password"
          minLength={12}
          maxLength={1024}
          autoComplete="new-password"
          required
        />
      </Field>
      <div className="actions">
        <button className="primary" disabled={busy}>
          {busy ? "正在重置…" : "重置密码"}
        </button>
      </div>
    </form>
  );
}

function DeviceUsage({
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
  const grouped = useMemo(() => {
    const m = new Map<string, number>();
    const names = new Map<string, string>(
      (data?.[1] ?? []).map((c) => [c.id, c.name]),
    );
    for (const x of data?.[0] ?? []) {
      const k = x.credential_id
        ? (names.get(x.credential_id) ?? x.credential_id)
        : "未标识凭证";
      m.set(k, (m.get(k) ?? 0) + x.count);
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [data]);
  return (
    <Card title="设备使用（过去 24 小时）">
      <Alert error={error} />
      {loading ? (
        <Spinner />
      ) : grouped.length ? (
        <div className="rows">
          {grouped.map(([n, c]) => (
            <div key={n}>
              <span>{n}</span>
              <strong>{fmt.num(c)} 次</strong>
            </div>
          ))}
        </div>
      ) : (
        <Empty>当前窗口暂无设备用量</Empty>
      )}
    </Card>
  );
}

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
  multiple_opt: "检测到多个 OPT 记录",
  multiple_ecs: "检测到多个 ECS 选项",
  invalid_ecs_family: "ECS 地址族无效",
  invalid_ecs_prefix: "ECS 前缀长度无效",
  invalid_ecs_address: "ECS 地址无效",
};

const responseSourceNames: Record<string, string> = {
  cache: "缓存",
  upstream: "上游",
  hosts: "Hosts",
  sequence: "执行链",
  servfail: "SERVFAIL",
};

function responseSourceName(source?: string) {
  return source ? (responseSourceNames[source] ?? source) : "未记录";
}

function logDate(value: string) {
  return new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "medium",
  }).format(new Date(value));
}

type EDNSStage =
  | "client_request"
  | "upstream_request"
  | "upstream_response"
  | "client_response";

function missingEDNSMessage(record: QueryRecord, stage: EDNSStage) {
  if (stage === "client_request") {
    return record.edns_trace_version
      ? "该阶段快照暂不可用"
      : "历史记录未采集客户端请求快照";
  }
  if (stage === "client_response") {
    return record.edns_trace_version
      ? "该阶段快照暂不可用"
      : "历史记录未采集客户端响应快照";
  }
  if (!record.edns_trace_version) {
    return "历史记录未采集该上游阶段";
  }
  switch (record.upstream_stage_status) {
    case "selected":
      return "已记录最终上游，但该阶段快照暂不可用";
    case "attempted_no_selection":
      return "发生过上游尝试，但没有最终采用的上游结果";
    case "discarded":
      return "上游结果已被后续本地响应完整替换，无对应最终上游快照";
    case "not_linked":
      if (record.response_source === "cache") {
        return "本次响应来自缓存，无对应上游快照";
      }
      if (record.response_source === "hosts") {
        return "本次由本地 Hosts 生成响应，无对应上游快照";
      }
      return "本次最终响应没有可关联的上游快照";
    default:
      return "该阶段快照暂不可用";
  }
}

function ecsDescription(edns: EDNSInfo) {
  if (!edns.ecs) return "无";
  const family =
    edns.ecs.family === 1
      ? "IPv4"
      : edns.ecs.family === 2
        ? "IPv6"
        : `地址族 ${edns.ecs.family}`;
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
            {snapshot.present ? "有 EDNS" : "无 EDNS"}
          </span>
        ) : null}
      </header>
      {!snapshot ? (
        <p className="edns-stage-missing">{missing}</p>
      ) : !snapshot.present ? (
        <p className="edns-stage-empty">已观察报文：没有 EDNS</p>
      ) : (
        <dl>
          <div>
            <dt>版本</dt>
            <dd>EDNS v{snapshot.version}</dd>
          </div>
          <div>
            <dt>通告 UDP Size</dt>
            <dd>{snapshot.udp_size} bytes</dd>
          </div>
          <div>
            <dt>DO 位</dt>
            <dd>
              {snapshot.dnssec_ok ? "已设置（请求 DNSSEC 数据）" : "未设置"}
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
                : "无已记录选项"}
              {snapshot.option_codes_truncated ? (
                <small>仅显示前 64 项，不能据此判断其他 Option 不存在</small>
              ) : null}
            </dd>
          </div>
          <div className="edns-stage-wide">
            <dt>ECS</dt>
            <dd>{ecsDescription(snapshot)}</dd>
          </div>
          {snapshot.anomalies?.length ? (
            <div className="edns-stage-wide">
              <dt>采集异常</dt>
              <dd className="edns-anomalies">
                {snapshot.anomalies
                  .map((item) => ednsAnomalyNames[item] ?? item)
                  .join("；")}
                <small>异常信息仅用于解释快照，不影响 DNS 请求处理。</small>
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
    return ["阶段信息未知、异常或不完整，无法确定完整的 EDNS/ECS 变化"];
  }
  const [client, upstreamRequest, upstreamResponse, response] = stages as [
    EDNSInfo,
    EDNSInfo,
    EDNSInfo,
    EDNSInfo,
  ];
  const differences: string[] = [];
  if (!client.present && upstreamRequest.present) {
    differences.push("客户端请求没有 EDNS，上游请求中出现 EDNS");
  } else if (client.present && !upstreamRequest.present) {
    differences.push("客户端请求携带 EDNS，上游请求中已不存在");
  }
  if (!client.ecs && upstreamRequest.ecs) {
    differences.push("客户端请求中没有 ECS，上游请求中出现 ECS");
  } else if (client.ecs && !upstreamRequest.ecs) {
    differences.push("客户端请求携带 ECS，上游请求中已不存在");
  } else if (!sameECS(client.ecs, upstreamRequest.ecs)) {
    differences.push("上游请求中的 ECS 与客户端请求不同");
  }
  if (upstreamResponse.present && !response.present) {
    differences.push("上游响应携带 EDNS，客户端逻辑响应中已不存在");
  }
  if (upstreamResponse.ecs && !response.ecs) {
    differences.push("上游响应携带 ECS，客户端逻辑响应中已不存在");
  } else if (!sameECS(upstreamResponse.ecs, response.ecs)) {
    differences.push("客户端逻辑响应中的 ECS 与上游原始响应不同");
  }
  return differences;
}

function QueryLogDetail({
  record,
  deviceName,
  showPrincipal,
}: {
  record: QueryRecord;
  deviceName: string;
  showPrincipal: boolean;
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
        <span className="badge off">{record.qtype || "未知类型"}</span>
        {record.cache_hit ? (
          <span className="badge cache">缓存命中</span>
        ) : null}
        <time>{logDate(record.time)}</time>
      </div>
      <section>
        <h3>请求</h3>
        <dl>
          <div>
            <dt>查询名称</dt>
            <dd>{record.name}</dd>
          </div>
          <div>
            <dt>设备</dt>
            <dd>
              {deviceName}
              {record.credential_id ? (
                <small>{record.credential_id}</small>
              ) : null}
            </dd>
          </div>
          {showPrincipal ? (
            <div>
              <dt>用户 ID</dt>
              <dd>{record.user_id || "未识别"}</dd>
            </div>
          ) : null}
          <div>
            <dt>客户端 IP</dt>
            <dd>{record.client_ip || "未记录"}</dd>
          </div>
        </dl>
      </section>
      <section>
        <h3>响应</h3>
        <dl>
          <div>
            <dt>响应码</dt>
            <dd>{record.rcode || "UNKNOWN"}</dd>
          </div>
          <div>
            <dt>Answer IP</dt>
            <dd className="answer-list">
              {record.answer_ips?.length
                ? record.answer_ips.map((address) => (
                    <code key={address}>{address}</code>
                  ))
                : "无地址记录"}
            </dd>
          </div>
          <div>
            <dt>处理路径</dt>
            <dd>{responseSourceName(record.response_source)}</dd>
          </div>
          <div>
            <dt>处理组件</dt>
            <dd>{record.response_source_id || "未记录"}</dd>
          </div>
          <div>
            <dt>出站 DNS</dt>
            <dd>{outboundName(record)}</dd>
          </div>
          <div>
            <dt>处理耗时</dt>
            <dd>{record.duration_ms.toFixed(2)} ms</dd>
          </div>
        </dl>
      </section>
      <OutboundSection record={record} deviceName={deviceName} />
      <section>
        <h3>传输与 EDNS</h3>
        <dl className="transport-summary">
          <div>
            <dt>协议</dt>
            <dd>{record.protocol?.toUpperCase() || "未记录"}</dd>
          </div>
          <div>
            <dt>采集版本</dt>
            <dd>
              {record.edns_trace_version
                ? `四阶段 v${record.edns_trace_version}`
                : "历史记录"}
            </dd>
          </div>
        </dl>
        <div className="edns-stage-grid">
          <EDNSStageCard
            title="客户端请求"
            note="进入控制策略与执行链之前"
            snapshot={record.edns}
            missing={missingEDNSMessage(record, "client_request")}
          />
          <EDNSStageCard
            title="最终上游请求"
            note="最终采用交换的协议发送边界"
            snapshot={record.upstream_request_edns}
            missing={missingEDNSMessage(record, "upstream_request")}
          />
          <EDNSStageCard
            title="上游原始响应"
            note="同一次交换解码后、响应后处理之前"
            snapshot={record.upstream_response_edns}
            missing={missingEDNSMessage(record, "upstream_response")}
          />
          <EDNSStageCard
            title="客户端逻辑响应"
            note="全部响应处理后、交给协议写出层之前"
            snapshot={record.response_edns}
            missing={missingEDNSMessage(record, "client_response")}
          />
        </div>
        {differences.length ? (
          <div className="edns-differences" role="note">
            <strong>阶段变化</strong>
            <ul>
              {differences.map((difference) => (
                <li key={difference}>{difference}</li>
              ))}
            </ul>
            <small>变化仅来自报文快照，不能据此确定由哪个插件造成。</small>
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
  title = "查询日志",
}: {
  path: string;
  enabled: boolean;
  credentialsPath?: string;
  showPrincipal?: boolean;
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
      ? (credentialNames.get(record.credential_id) ?? "未知设备")
      : "未标识设备";
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
        <Empty>查询日志未启用。管理员可在服务配置中开启 query_log。</Empty>
      ) : (
        <>
          <form className="query-filters" onSubmit={applyFilters}>
            <Field label="时间范围">
              <select
                value={draft.hours}
                onChange={(event) => updateFilter("hours", event.target.value)}
              >
                <option value="1">最近 1 小时</option>
                <option value="6">最近 6 小时</option>
                <option value="24">最近 24 小时</option>
                <option value="168">最近 7 天</option>
                <option value="720">最近 30 天</option>
              </select>
            </Field>
            <Field label="域名">
              <input
                value={draft.name}
                onChange={(event) => updateFilter("name", event.target.value)}
                placeholder="例如：包含 example.com…"
                maxLength={255}
              />
            </Field>
            <Field label="查询类型">
              <select
                value={draft.qtype}
                onChange={(event) => updateFilter("qtype", event.target.value)}
              >
                <option value="">全部类型</option>
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
            <Field label="响应码">
              <select
                value={draft.rcode}
                onChange={(event) => updateFilter("rcode", event.target.value)}
              >
                <option value="">全部响应</option>
                {["NOERROR", "NXDOMAIN", "SERVFAIL", "REFUSED", "FORMERR"].map(
                  (value) => (
                    <option key={value}>{value}</option>
                  ),
                )}
              </select>
            </Field>
            <Field label="设备">
              {credentialsPath ? (
                <select
                  value={draft.credentialId}
                  onChange={(event) =>
                    updateFilter("credentialId", event.target.value)
                  }
                >
                  <option value="">全部设备</option>
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
                  placeholder="凭证 ID…"
                  maxLength={64}
                />
              )}
            </Field>
            <Field label="协议">
              <select
                value={draft.protocol}
                onChange={(event) =>
                  updateFilter("protocol", event.target.value)
                }
              >
                <option value="">全部协议</option>
                {["udp", "tcp", "tls", "quic", "http", "https", "h2", "h3"].map(
                  (value) => (
                    <option key={value} value={value}>
                      {value.toUpperCase()}
                    </option>
                  ),
                )}
              </select>
            </Field>
            <Field label="客户端或 Answer IP">
              <input
                value={draft.address}
                onChange={(event) =>
                  updateFilter("address", event.target.value)
                }
                placeholder="例如：192.0.2.1…"
              />
            </Field>
            <Field label="缓存">
              <select
                value={draft.cache}
                onChange={(event) => updateFilter("cache", event.target.value)}
              >
                <option value="all">全部</option>
                <option value="hit">命中缓存</option>
                <option value="miss">未命中缓存</option>
              </select>
            </Field>
            <Field label="处理来源">
              <select
                value={draft.source}
                onChange={(event) => updateFilter("source", event.target.value)}
              >
                <option value="all">全部来源</option>
                {Object.entries(responseSourceNames).map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="出站 DNS">
              <input
                value={draft.upstream}
                onChange={(event) =>
                  updateFilter("upstream", event.target.value)
                }
                placeholder="例如：223.5.5.5 (UDP)…"
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
                重置
              </button>
              <button className="primary" type="submit">
                查询
              </button>
            </div>
          </form>
          <div className="log-list-heading">
            <div>
              <strong>最新记录</strong>
              <span>已加载 {fmt.num(data.length)} 条，按时间从新到旧</span>
            </div>
            <button
              onClick={() => setRefreshVersion((value) => value + 1)}
              disabled={loading}
            >
              刷新
            </button>
          </div>
          <Alert error={error} />
          {loading && !data.length ? (
            <Spinner />
          ) : data.length ? (
            <div className="table-wrap query-table">
              <table>
                <thead>
                  <tr>
                    <th>查询</th>
                    <th>结果</th>
                    <th>出站 DNS</th>
                    <th>设备 / 客户端</th>
                    <th>Answer IP</th>
                    <th>协议 / 耗时</th>
                    <th>时间</th>
                  </tr>
                </thead>
                <tbody>
                  {data.map((record) => (
                    <tr key={record.id}>
                      <td className="query-detail">
                        <button
                          className="query-name"
                          aria-label={`查看 ${record.name} 详情`}
                          onClick={() => setSelected(record)}
                        >
                          {record.name}
                        </button>
                        <small>{record.qtype}</small>
                      </td>
                      <td>
                        <span
                          className={`badge ${record.rcode === "NOERROR" ? "ok" : "error"}`}
                        >
                          {record.rcode || "UNKNOWN"}
                        </span>
                        {record.cache_hit ? (
                          <small className="cache-text">缓存命中</small>
                        ) : null}
                        <small>
                          {responseSourceName(record.response_source)}
                        </small>
                      </td>
                      <td className="query-detail">
                        {outboundName(record)}
                        {record.trace && !record.cache_hit ? (
                          <small>{routeReason(record)}</small>
                        ) : null}
                        {record.trace && record.trace.attempts.length > 1 ? (
                          <small>
                            共 {record.trace.attempts.length} 次上游请求
                          </small>
                        ) : null}
                      </td>
                      <td className="query-detail">
                        {deviceName(record)}
                        <small>{record.client_ip || "未记录客户端 IP"}</small>
                        {showPrincipal ? (
                          <small>用户 {record.user_id || "未识别"}</small>
                        ) : null}
                      </td>
                      <td className="query-detail answer-preview">
                        {record.answer_ips?.length
                          ? record.answer_ips.join(", ")
                          : "无地址记录"}
                      </td>
                      <td>
                        {record.protocol?.toUpperCase() || "未知"}
                        <small>{record.duration_ms.toFixed(2)} ms</small>
                      </td>
                      <td>{logDate(record.time)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty>当前条件下暂无查询记录</Empty>
          )}
          {cursor ? (
            <div className="query-more">
              <button onClick={more} disabled={loading}>
                {loading ? "正在加载…" : "加载更多"}
              </button>
            </div>
          ) : null}
          {selected ? (
            <Modal
              title={selected.name || "查询详情"}
              onClose={() => setSelected(null)}
              className="query-log-modal"
            >
              <QueryLogDetail
                record={selected}
                deviceName={deviceName(selected)}
                showPrincipal={showPrincipal}
              />
            </Modal>
          ) : null}
        </>
      )}
    </Card>
  );
}

export function ServicePage() {
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
        <PageTitle title="主页" />
        <Spinner />
      </>
    );
  return (
    <>
      <PageTitle title="主页" description="连接信息、服务额度与设备接入。" />
      <Alert error={error} />
      {data ? (
        <>
          <Card title="连接信息" className="service-connection">
            <div className="connection-row">
              <div>
                <span>公共 DoH 地址</span>
                <code className="block">{data[0].public_dns_url}</code>
              </div>
              <Link className="primary action-link" to="/app/account">
                管理设备凭证
              </Link>
            </div>
            <div className="credential-summary">
              <span>设备凭证</span>
              {data[2].length ? (
                <div>
                  {data[2].map((credential) => (
                    <code key={credential.id}>{credential.name}</code>
                  ))}
                </div>
              ) : (
                <p>尚未创建设备凭证。每台设备请使用独立凭证接入。</p>
              )}
            </div>
          </Card>
          <div className="metrics">
            <Metric
              label="剩余额度"
              value={fmt.num(data[0].quota.remaining)}
              hint={`已用 ${fmt.num(data[0].quota.used)} / ${fmt.num(data[0].quota.limit)}`}
            />
            <Metric
              label="额度重置"
              value={fmt.date(data[0].quota.period_end)}
              hint={`${data[0].quota.period === "daily" ? "每日" : "每月"} · ${data[0].quota.timezone}`}
            />
            <Metric
              label="服务到期"
              value={fmt.date(data[0].user.expires_at)}
            />
            <Metric
              label="QPS / 突发"
              value={`${data[0].user.qps} / ${data[0].user.burst}`}
            />
          </div>
          <Card title="额度使用">
            <div
              className="progress"
              aria-label={`额度已使用 ${fmt.pct(data[0].quota.used, data[0].quota.limit)}`}
            >
              <span
                style={{
                  width: fmt.pct(data[0].quota.used, data[0].quota.limit),
                }}
              />
            </div>
          </Card>
          <Card title="订阅">
            <div className="rows">
              <div>
                <span>服务周期</span>
                <strong>
                  {data[0].quota.period === "daily" ? "每日" : "每月"} 额度
                </strong>
              </div>
            </div>
          </Card>
          <Card title="设备接入" className="connection-guide">
            <ol>
              <li>为每台设备创建独立凭证，专属 DoH 地址和令牌只显示一次。</li>
              <li>在客户端填写专属地址，或使用对应的 Bearer Token。</li>
              <li>设备遗失或不再使用时，请在账户中心轮换或撤销其凭证。</li>
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
  const { data, error, loading } = useLoad(load, [load]);
  return (
    <>
      <PageTitle
        title="统计与日志"
        description="过去 24 小时的用量、设备和 DNS 查询记录"
        action={<button onClick={() => setVersion((x) => x + 1)}>刷新</button>}
      />
      {loading ? <Spinner /> : <Alert error={error} />}{" "}
      {data ? (
        <>
          <StatsBlocks stats={data[0]} usage={data[1]} />
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
        title="接入凭证"
        description="为每台设备签发独立地址或 Bearer 令牌"
      />
      {loading ? <Spinner /> : <Alert error={error} />}{" "}
      {data ? (
        <CredentialManager base="/me" max={data.user.max_credentials} />
      ) : null}
    </>
  );
}

function PasswordForm() {
  const { clearLocal } = useSession();
  const nav = useNavigate();
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    if (f.get("new_password") !== f.get("confirm")) {
      setError("两次输入的新密码不一致。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await request(
        "/me/password",
        json("POST", {
          current_password: f.get("current_password"),
          new_password: f.get("new_password"),
        }),
      );
      clearLocal();
      nav("/login", {
        replace: true,
        state: { notice: "密码已修改，请重新登录" },
      });
    } catch (e) {
      setError(message(e));
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit}>
      <Alert error={error} />
      <Field label="当前密码">
        <input
          type="password"
          name="current_password"
          autoComplete="current-password"
          required
        />
      </Field>
      <Field label="新密码" hint="至少 12 个字符">
        <input
          type="password"
          name="new_password"
          autoComplete="new-password"
          required
          minLength={12}
          maxLength={1024}
        />
      </Field>
      <Field label="确认新密码">
        <input
          type="password"
          name="confirm"
          autoComplete="new-password"
          required
          minLength={12}
          maxLength={1024}
        />
      </Field>
      <button className="primary" disabled={busy}>
        {busy ? "正在修改…" : "修改密码"}
      </button>
    </form>
  );
}

export function PasswordPage() {
  return (
    <>
      <PageTitle title="修改密码" description="修改后，当前会话会立即退出" />
      <Card className="narrow">
        <PasswordForm />
      </Card>
    </>
  );
}

export function AccountPage() {
  const load = useCallback(
    (signal: AbortSignal) => request<Me>("/me", { signal }),
    [],
  );
  const { data, error, loading } = useLoad(load, [load]);
  return (
    <>
      <PageTitle
        title="账户中心"
        description="管理账户状态、设备凭证与登录密码。"
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data ? (
        <>
          <div className="metrics">
            <Metric label="账户" value={data.user.username} />
            <Metric label="服务到期" value={fmt.date(data.user.expires_at)} />
            <Metric label="凭证名额" value={data.user.max_credentials} />
          </div>
          <Card title="公共 DNS 地址">
            <code className="block">{data.public_dns_url}</code>
            <p className="caption">
              请使用设备凭证生成的专属地址接入；不要将凭证令牌分享给他人。
            </p>
          </Card>
          <CredentialManager base="/me" max={data.user.max_credentials} />
          <Card className="narrow" title="修改密码">
            <p className="caption">修改成功后，当前会话会立即退出。</p>
            <PasswordForm />
          </Card>
        </>
      ) : null}
    </>
  );
}

export function HelpPage() {
  return (
    <>
      <PageTitle title="帮助与支持" description="设备接入与用量查看说明。" />
      <div className="grid2 help-grid">
        <Card title="设备接入">
          <p>
            先在账户中心创建一个设备凭证。专属 DoH 地址和 Bearer Token
            仅会显示一次，请在设备端安全保存。
          </p>
          <p>凭证遗失或设备不再使用时，可在账户中心轮换或撤销它。</p>
        </Card>
        <Card title="用量与日志">
          <p>
            统计与日志展示账户已处理的 DNS
            请求。查询明细是否可见取决于服务端是否已开启日志记录。
          </p>
        </Card>
      </div>
    </>
  );
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
      <PageTitle title="操作审计" description="过去 24 小时的账户与凭证变更" />
      {loading ? <Spinner /> : <Alert error={error} />}{" "}
      {data?.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>时间</th>
                <th>操作</th>
                <th>对象</th>
                <th>操作者</th>
              </tr>
            </thead>
            <tbody>
              {data.map((a) => (
                <tr key={a.id}>
                  <td>{fmt.date(a.created_at)}</td>
                  <td>{a.action}</td>
                  <td>
                    {a.target_type}
                    {a.target_id ? ` · ${a.target_id}` : ""}
                  </td>
                  <td>{a.actor_id || "系统"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : !loading && !error ? (
        <Empty>当前窗口暂无审计记录</Empty>
      ) : null}
      {cursor ? (
        <button onClick={more} disabled={loading}>
          {loading ? "正在加载…" : "加载更多"}
        </button>
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
        title="系统概览"
        description="版本、运行时间与可公开的配置摘要"
        action={data ? <button onClick={download}>下载配置摘要</button> : null}
      />
      {loading ? <Spinner /> : <Alert error={error} />} <HealthPanel />
      {data ? (
        <>
          <div className="metrics">
            <Metric label="版本" value={data.version} />
            <Metric label="启动时间" value={fmt.date(data.started_at)} />
            <Metric
              label="查询明细"
              value={data.query_log_enabled ? "已启用" : "未启用"}
            />
            <Metric label="控制数据" value={data.config.control_storage} />
            <Metric label="统计数据" value={data.config.telemetry_storage} />
          </div>
          <Card title="公共 DNS 地址">
            <code className="block">{data.public_dns_url}</code>
          </Card>
          <Card title="配置摘要">
            <pre>{JSON.stringify(data.config, null, 2)}</pre>
          </Card>
        </>
      ) : null}
    </>
  );
}
