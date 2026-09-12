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
import { allPages, json, message, request } from "./api";
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
export { RuntimeConfigPage } from "./runtime-config";
import type {
  Audit,
  Credential,
  DeviceUsagePoint,
  IssuedCredential,
  Me,
  LookupRecord,
  LookupResult,
  Page,
  PublicList,
  PublicListFormat,
  QueryRecord,
  Stats,
  SystemInfo,
  Rule,
  RuleAction,
  RuleMatch,
  RuleRecordType,
  UserSettings,
  UserPublicList,
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
        <div className="login-intro" aria-hidden>
          <div className="login-orbit">
            <span />
            <span />
            <span />
            <strong>M</strong>
          </div>
          <p className="login-kicker">PRIVATE DNS PLATFORM</p>
          <h2>
            让每一次查询
            <br />
            都清晰可控
          </h2>
          <p>统一管理用户、凭证、配额与 DNS 运行数据。</p>
        </div>
        <section>
          <div className="brand login-brand">
            <span className="brandmark">M</span>
            <span className="brand-copy">
              <strong>MosDNS</strong>
              <small>Control Center</small>
            </span>
          </div>
          <h1>欢迎回来</h1>
          <p>登录后管理你的 DNS 服务</p>
          <Alert error={error} />
          <form onSubmit={submit}>
            <Field label="用户名">
              <input
                name="username"
                autoComplete="username"
                required
                autoFocus
                placeholder="请输入用户名"
              />
            </Field>
            <Field label="密码">
              <input
                name="password"
                type="password"
                autoComplete="current-password"
                required
                placeholder="请输入密码"
              />
            </Field>
            <button className="primary wide login-submit" disabled={busy}>
              {busy ? "正在登录…" : "登录"}
            </button>
          </form>
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
              placeholder="例如：家中路由器"
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
    [resetPassword, setResetPassword] = useState(false);
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
        </>
      ) : null}
    </>
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
  upstreamId: "",
};

type QueryFilters = typeof emptyQueryFilters;

const ednsOptionNames: Record<number, string> = {
  3: "NSID",
  8: "ECS",
  10: "COOKIE",
  12: "Padding",
  15: "EDE",
};

const responseSourceNames: Record<string, string> = {
  cache: "缓存",
  upstream: "上游",
  custom_block: "自定义拦截",
  custom_rewrite: "自定义重写",
  public_list: "公共列表",
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

function QueryLogDetail({
  record,
  deviceName,
  showPrincipal,
  ruleLabel,
  publicListName,
}: {
  record: QueryRecord;
  deviceName: string;
  showPrincipal: boolean;
  ruleLabel?: string;
  publicListName?: string;
}) {
  const edns = record.edns;
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
            <dd>
              {record.response_source === "custom_block" ||
              record.response_source === "custom_rewrite"
                ? ruleLabel || record.response_source_id || "未记录"
                : record.response_source === "public_list"
                  ? publicListName || record.response_source_id || "未记录"
                  : record.response_source_id || "未记录"}
            </dd>
          </div>
          <div>
            <dt>最终上游</dt>
            <dd>{record.upstream_id || "未使用上游"}</dd>
          </div>
          <div>
            <dt>命中规则</dt>
            <dd>
              {ruleLabel || record.matched_rule_id || "未命中"}
              {ruleLabel && record.matched_rule_id ? (
                <small>{record.matched_rule_id}</small>
              ) : null}
            </dd>
          </div>
          <div>
            <dt>命中公共列表</dt>
            <dd>
              {publicListName || record.matched_public_list_id || "未命中"}
              {publicListName && record.matched_public_list_id ? (
                <small>{record.matched_public_list_id}</small>
              ) : null}
            </dd>
          </div>
          <div>
            <dt>处理耗时</dt>
            <dd>{record.duration_ms.toFixed(2)} ms</dd>
          </div>
        </dl>
      </section>
      <section>
        <h3>传输与 EDNS</h3>
        <dl>
          <div>
            <dt>协议</dt>
            <dd>{record.protocol?.toUpperCase() || "未记录"}</dd>
          </div>
          <div>
            <dt>EDNS</dt>
            <dd>
              {edns?.present
                ? `v${edns.version} · UDP ${edns.udp_size} bytes${edns.dnssec_ok ? " · DNSSEC OK" : ""}`
                : "未携带"}
            </dd>
          </div>
          <div>
            <dt>EDNS 选项</dt>
            <dd>
              {edns?.option_codes?.length
                ? edns.option_codes
                    .map(
                      (code) =>
                        `${code}${ednsOptionNames[code] ? ` (${ednsOptionNames[code]})` : ""}`,
                    )
                    .join(", ")
                : "无"}
            </dd>
          </div>
          <div>
            <dt>ECS</dt>
            <dd>
              {edns?.ecs
                ? `${edns.ecs.address || "地址无效"}/${edns.ecs.source_prefix} · family ${edns.ecs.family} · scope ${edns.ecs.scope_prefix}`
                : "无"}
            </dd>
          </div>
        </dl>
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
    if (applied.upstreamId) params.upstream_id = applied.upstreamId;
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
  const deviceName = (record: QueryRecord) =>
    record.credential_id
      ? (credentialNames.get(record.credential_id) ?? "未知设备")
      : "未标识设备";
  const selectedRulePath =
    selected?.matched_rule_id && selected.user_id
      ? path.startsWith("/admin/")
        ? `/admin/users/${encodeURIComponent(selected.user_id)}/rules`
        : "/me/rules"
      : "";
  const selectedListPath = selected?.matched_public_list_id
    ? path.startsWith("/admin/")
      ? "/admin/public-lists"
      : "/me/public-lists"
    : "";
  const ruleLoader = useCallback(
    (signal: AbortSignal) =>
      selectedRulePath
        ? allPages<Rule>(selectedRulePath, signal)
        : Promise.resolve([] as Rule[]),
    [selectedRulePath],
  );
  const listLoader = useCallback(
    async (signal: AbortSignal) => {
      if (!selectedListPath) return [] as PublicList[];
      if (selectedListPath.startsWith("/admin/"))
        return allPages<PublicList>(selectedListPath, signal);
      const items = await allPages<UserPublicList>(selectedListPath, signal);
      return items.map((item) => item.list);
    },
    [selectedListPath],
  );
  const { data: selectedRules } = useLoad(ruleLoader, [ruleLoader]);
  const { data: selectedLists } = useLoad(listLoader, [listLoader]);
  const selectedRule = selectedRules?.find(
    (rule) => rule.id === selected?.matched_rule_id,
  );
  const selectedList = selectedLists?.find(
    (list) => list.id === selected?.matched_public_list_id,
  );
  const selectedRuleLabel = selectedRule
    ? `${selectedRule.action} · ${selectedRule.pattern}`
    : undefined;

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
      upstreamId: draft.upstreamId.trim(),
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
                placeholder="包含 example.com"
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
                  placeholder="凭证 ID"
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
                placeholder="192.0.2.1"
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
            <Field label="最终上游">
              <input
                value={draft.upstreamId}
                onChange={(event) =>
                  updateFilter("upstreamId", event.target.value)
                }
                placeholder="forward_remote/0"
                maxLength={255}
              />
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
            >
              <QueryLogDetail
                record={selected}
                deviceName={deviceName(selected)}
                showPrincipal={showPrincipal}
                ruleLabel={selectedRuleLabel}
                publicListName={selectedList?.name}
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
        request<UserSettings>("/me/settings", { signal: s }),
      ]),
    [],
  );
  const { data, error, loading, setData } = useLoad(load, [load]);
  const [pauseBusy, setPauseBusy] = useState(false);
  const [pauseError, setPauseError] = useState("");
  const settings = data ? normalizeSettings(data[3]) : undefined;
  async function updatePause(value: string) {
    if (!data || !settings) return;
    setPauseBusy(true);
    setPauseError("");
    try {
      const settings = await request<UserSettings>(
        "/me/settings",
        json("PATCH", { policy_paused_until: pauseUntil(value) }),
      );
      setData([data[0], data[1], data[2], normalizeSettings(settings)]);
    } catch (reason) {
      setPauseError(message(reason));
    } finally {
      setPauseBusy(false);
    }
  }
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
      <Alert error={error || pauseError} />
      {data && settings ? (
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
          <Card title="订阅与 DNS 策略">
            <div className="rows">
              <div>
                <span>服务周期</span>
                <strong>
                  {data[0].quota.period === "daily" ? "每日" : "每月"} 额度
                </strong>
              </div>
              <div>
                <span>暂停客制化 DNS</span>
                <select
                  aria-label="暂停客制化 DNS"
                  value={pauseSelection(settings.policy_paused_until)}
                  onChange={(event) => updatePause(event.target.value)}
                  disabled={pauseBusy}
                >
                  {policyPaused(settings.policy_paused_until) ? (
                    <option value="paused" disabled>
                      已暂停至 {fmt.date(settings.policy_paused_until)}
                    </option>
                  ) : null}
                  <option value="0">不暂停</option>
                  <option value="900">暂停 15 分钟</option>
                  <option value="1800">暂停 30 分钟</option>
                  <option value="3600">暂停 1 小时</option>
                  <option value="10800">暂停 3 小时</option>
                  <option value="21600">暂停 6 小时</option>
                  <option value="43200">暂停 12 小时</option>
                  <option value="86400">暂停 1 天</option>
                </select>
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

const lookupTypes = ["A", "AAAA", "CNAME", "NS", "MX", "TXT"] as const;
const ruleRecordTypes = ["A", "AAAA", "CNAME"] as const;

function normalizeRuleList(value: Rule[] | Page<Rule>) {
  return Array.isArray(value) ? value : value.items;
}

function splitQTypes(value: string) {
  return [
    ...new Set(
      value
        .split(/[，,\s]+/)
        .map((item) => item.trim().toUpperCase())
        .filter(Boolean),
    ),
  ];
}

const zeroTime = "0001-01-01T00:00:00Z";

type NormalizedUserSettings = Omit<
  UserSettings,
  | "blocked_qtypes"
  | "custom_block_enabled"
  | "custom_allow_enabled"
  | "custom_rewrite_enabled"
  | "policy_paused_until"
> & {
  blocked_qtypes: string[];
  custom_block_enabled: boolean;
  custom_allow_enabled: boolean;
  custom_rewrite_enabled: boolean;
  policy_paused_until: string;
};

function normalizeSettings(settings: UserSettings): NormalizedUserSettings {
  return {
    ...settings,
    blocked_qtypes: settings.blocked_qtypes ?? [],
    custom_block_enabled: settings.custom_block_enabled ?? true,
    custom_allow_enabled: settings.custom_allow_enabled ?? true,
    custom_rewrite_enabled: settings.custom_rewrite_enabled ?? true,
    policy_paused_until: settings.policy_paused_until || zeroTime,
  };
}

function policyPaused(until: string) {
  return !until.startsWith("0001-") && new Date(until).getTime() > Date.now();
}

function pauseUntil(seconds: string) {
  const duration = Number(seconds);
  return duration > 0
    ? new Date(Date.now() + duration * 1000).toISOString()
    : zeroTime;
}

function pauseSelection(until: string) {
  return policyPaused(until) ? "paused" : "0";
}

export function PrivacyPage() {
  const load = useCallback(
    (signal: AbortSignal) => request<UserSettings>("/me/settings", { signal }),
    [],
  );
  const { data, error: loadError, loading, setData } = useLoad(load, [load]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const settings = data ? normalizeSettings(data) : undefined;
  async function update(patch: Partial<UserSettings>) {
    if (!settings) return;
    setBusy(true);
    setError("");
    try {
      const updated = await request<UserSettings>(
        "/me/settings",
        json("PATCH", patch),
      );
      setData(normalizeSettings(updated));
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageTitle
        title="安全与隐私保护"
        description="每项切换会立即保存并应用到你的 DNS 请求。"
      />
      {loading ? <Spinner /> : <Alert error={loadError || error} />}
      {settings ? (
        <Card title="可用保护" className="settings-list">
          <SettingsRow
            title="DNS 重绑定防护"
            description="拦截指向私有或本地网络地址的 DNS 应答。"
            checked={settings.block_private_answers}
            disabled={busy}
            onChange={(value) => update({ block_private_answers: value })}
          />
          <SettingsRow
            title="停用 ECS"
            description="不向上游携带 EDNS Client Subnet 客户端网段信息。"
            checked={settings.strip_ecs}
            disabled={busy}
            onChange={(value) => update({ strip_ecs: value })}
          />
          <div className="settings-row qtype-row">
            <div>
              <strong>查询类型拦截</strong>
              <small>点击类型即可立即加入或移出拒绝列表。</small>
            </div>
            <div className="qtype-switches" aria-label="查询类型拦截">
              {["AAAA", "TXT", "MX", "NS"].map((type) => {
                const checked = settings.blocked_qtypes.includes(type);
                return (
                  <button
                    key={type}
                    type="button"
                    className={checked ? "switch-chip active" : "switch-chip"}
                    aria-pressed={checked}
                    disabled={busy}
                    onClick={() =>
                      update({
                        blocked_qtypes: checked
                          ? settings.blocked_qtypes.filter(
                              (item) => item !== type,
                            )
                          : [...settings.blocked_qtypes, type],
                      })
                    }
                  >
                    {type}
                  </button>
                );
              })}
            </div>
          </div>
          <SettingsRow
            title="DNSSEC 强制验证"
            description="节点未配置此能力。"
            checked={false}
            disabled
          />
          <SettingsRow
            title="恶意域名情报"
            description="节点未配置此能力。"
            checked={false}
            disabled
          />
        </Card>
      ) : null}
    </>
  );
}

function SettingsRow({
  title,
  description,
  checked,
  disabled = false,
  onChange,
}: {
  title: string;
  description: string;
  checked: boolean;
  disabled?: boolean;
  onChange?: (value: boolean) => void;
}) {
  return (
    <div className="settings-row">
      <div>
        <strong>{title}</strong>
        <small>{description}</small>
      </div>
      <label className="switch">
        <input
          type="checkbox"
          checked={checked}
          disabled={disabled}
          aria-label={title}
          onChange={(event) => onChange?.(event.target.checked)}
        />
        <span aria-hidden />
      </label>
    </div>
  );
}

function rulePayload(form: HTMLFormElement) {
  const fields = new FormData(form);
  const action = String(fields.get("action")) as RuleAction;
  const recordType = String(fields.get("record_type")) as RuleRecordType;
  const value = String(fields.get("value")).trim();
  return {
    action,
    match: String(fields.get("match")) as RuleMatch,
    pattern: String(fields.get("pattern")).trim(),
    priority: Number(fields.get("priority")),
    ...(action === "rewrite" && recordType ? { record_type: recordType } : {}),
    ...(action === "rewrite" && value ? { value } : {}),
    enabled: fields.get("enabled") === "on",
  };
}

function RuleForm({
  rule,
  onDone,
  onCancel,
}: {
  rule?: Rule;
  onDone: () => void;
  onCancel?: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const editing = Boolean(rule);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const payload = rulePayload(form);
    if (!payload.pattern) {
      setError("请填写匹配内容。");
      return;
    }
    if (
      payload.action === "rewrite" &&
      (!payload.record_type || !payload.value)
    ) {
      setError("重写规则需要选择记录类型并填写目标值。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await request<Rule>(
        rule ? `/me/rules/${rule.id}` : "/me/rules",
        json(rule ? "PATCH" : "POST", payload),
      );
      if (!editing) form.reset();
      onDone();
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="rule-form" onSubmit={submit}>
      <Alert error={error} />
      <div className="form-grid">
        <Field label="动作">
          <select name="action" defaultValue={rule?.action ?? "block"}>
            <option value="allow">允许</option>
            <option value="block">拦截</option>
            <option value="rewrite">重写</option>
          </select>
        </Field>
        <Field label="匹配方式">
          <select name="match" defaultValue={rule?.match ?? "suffix"}>
            <option value="exact">完全匹配</option>
            <option value="suffix">后缀匹配</option>
            <option value="keyword">关键词匹配</option>
            <option value="regexp">正则匹配</option>
          </select>
        </Field>
      </div>
      <Field label="匹配内容" hint="例如 example.com 或 ads.example.com">
        <input
          name="pattern"
          defaultValue={rule?.pattern}
          maxLength={1024}
          required
        />
      </Field>
      <Field label="优先级" hint="数值越小越先匹配；同一优先级按规则 ID 排序。">
        <input
          name="priority"
          type="number"
          min="0"
          max="4294967295"
          defaultValue={rule?.priority ?? 100}
          required
        />
      </Field>
      <div className="form-grid">
        <Field label="记录类型" hint="仅重写规则使用，重写时必选。">
          <select name="record_type" defaultValue={rule?.record_type ?? ""}>
            <option value="">选择类型</option>
            {ruleRecordTypes.map((type) => (
              <option key={type} value={type}>
                {type}
              </option>
            ))}
          </select>
        </Field>
        <Field label="重写目标" hint="仅 rewrite 动作使用。">
          <input name="value" defaultValue={rule?.value} maxLength={1024} />
        </Field>
      </div>
      <label className="check">
        <input
          name="enabled"
          type="checkbox"
          defaultChecked={rule?.enabled ?? true}
        />
        启用此规则
      </label>
      <div className="actions">
        {onCancel ? (
          <button type="button" onClick={onCancel}>
            取消
          </button>
        ) : null}
        <button className="primary" disabled={busy}>
          {busy ? "正在保存…" : editing ? "保存规则" : "添加规则"}
        </button>
      </div>
    </form>
  );
}

function CompactRuleForm({
  action,
  onDone,
}: {
  action: RuleAction | "decision";
  onDone: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const fields = new FormData(form);
    const selectedAction =
      action === "decision"
        ? (String(fields.get("action")) as RuleAction)
        : action;
    const rewrite = selectedAction === "rewrite";
    const pattern = String(fields.get("pattern")).trim();
    const value = String(fields.get("value")).trim();
    const recordType = String(fields.get("record_type")) as RuleRecordType;
    if (!pattern || (rewrite && (!recordType || !value))) {
      setError(rewrite ? "请填写域名、记录类型和重写值。" : "请填写域名。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await request<Rule>(
        "/me/rules",
        json("POST", {
          action: selectedAction,
          match: String(fields.get("match")) as RuleMatch,
          pattern,
          priority: 100,
          ...(rewrite ? { record_type: recordType, value } : {}),
          enabled: true,
        }),
      );
      form.reset();
      onDone();
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="compact-rule-form" onSubmit={submit}>
      <Alert error={error} />
      {action === "rewrite" ? (
        <>
          <input type="hidden" name="match" value="exact" />
          <select name="record_type" aria-label="重写记录类型" defaultValue="A">
            {ruleRecordTypes.map((type) => (
              <option key={type}>{type}</option>
            ))}
          </select>
          <input
            name="pattern"
            aria-label="重写域名"
            placeholder="域名"
            maxLength={1024}
            required
          />
          <input
            name="value"
            aria-label="重写值"
            placeholder="IP 地址或 CNAME"
            maxLength={1024}
            required
          />
        </>
      ) : (
        <>
          <select name="match" aria-label="匹配方式" defaultValue="suffix">
            <option value="exact">@ 完全匹配</option>
            <option value="suffix">suffix 后缀</option>
            <option value="keyword">contains 包含</option>
            <option value="regexp">regexp 正则</option>
          </select>
          <input
            name="pattern"
            aria-label="规则域名"
            placeholder="域名或匹配内容"
            maxLength={1024}
            required
          />
          {action === "decision" ? (
            <select name="action" aria-label="规则动作" defaultValue="block">
              <option value="block">拦截</option>
              <option value="allow">放行</option>
            </select>
          ) : null}
        </>
      )}
      <button className="primary" disabled={busy}>
        {busy
          ? "正在添加…"
          : action === "rewrite"
            ? "添加重写"
            : action === "block"
              ? "添加拦截"
              : action === "allow"
                ? "添加放行"
                : "添加规则"}
      </button>
    </form>
  );
}

function RuleGroup({
  title,
  description,
  rules,
  onEdit,
  onDelete,
}: {
  title: string;
  description: string;
  rules: Rule[];
  onEdit: (rule: Rule) => void;
  onDelete: (rule: Rule) => void;
}) {
  return (
    <Card className="rule-group">
      <div className="rule-group-header">
        <div>
          <h2>{title}</h2>
          <p className="caption">{description}</p>
        </div>
      </div>
      {rules.length ? (
        <div className="flat-rule-list">
          {rules.map((rule) => (
            <div key={rule.id}>
              <code>{rule.pattern}</code>
              <span>{rule.match}</span>
              {rule.value ? (
                <span>
                  {rule.record_type} → {rule.value}
                </span>
              ) : null}
              <span>优先级 {rule.priority}</span>
              <span className={`badge ${rule.enabled ? "ok" : "off"}`}>
                {rule.enabled ? "启用" : "停用"}
              </span>
              <button onClick={() => onEdit(rule)}>编辑</button>
              <button className="danger" onClick={() => onDelete(rule)}>
                删除
              </button>
            </div>
          ))}
        </div>
      ) : (
        <p className="caption rule-empty">尚未添加{title}。</p>
      )}
    </Card>
  );
}

export function RulesPage() {
  const [version, setVersion] = useState(0);
  const [editing, setEditing] = useState<Rule | null>(null);
  const [error, setError] = useState("");
  const [updating, setUpdating] = useState(false);
  const load = useCallback(
    (signal: AbortSignal) =>
      Promise.all([
        allPages<Rule>("/me/rules", signal),
        request<UserSettings>("/me/settings", { signal }),
      ]),
    [],
  );
  const {
    data,
    error: loadError,
    loading,
    setData,
  } = useLoad(load, [load, version]);
  const rules = data ? normalizeRuleList(data[0]) : [];
  const settings = data ? normalizeSettings(data[1]) : undefined;
  async function updateSettings(patch: Partial<UserSettings>) {
    if (!data) return;
    setUpdating(true);
    setError("");
    try {
      const updated = await request<UserSettings>(
        "/me/settings",
        json("PATCH", patch),
      );
      setData([data[0], normalizeSettings(updated)]);
    } catch (reason) {
      setError(message(reason));
    } finally {
      setUpdating(false);
    }
  }
  async function remove(rule: Rule) {
    if (!confirm(`删除规则“${rule.pattern}”？`)) return;
    setError("");
    try {
      await request(`/me/rules/${rule.id}`, { method: "DELETE" });
      setVersion((current) => current + 1);
    } catch (reason) {
      setError(message(reason));
    }
  }
  return (
    <>
      <PageTitle
        title="自定义规则"
        description="为你的 DNS 请求添加拦截、放行或重写规则。"
      />
      <Alert error={loadError || error} />
      {loading || !settings ? (
        <Spinner />
      ) : (
        <>
          <Card title="规则开关" className="settings-list">
            <SettingsRow
              title="自定义拦截"
              description="启用自定义域名拦截规则。"
              checked={settings.custom_block_enabled}
              disabled={updating}
              onChange={(value) =>
                updateSettings({ custom_block_enabled: value })
              }
            />
            <SettingsRow
              title="自定义放行"
              description="启用自定义域名放行规则。"
              checked={settings.custom_allow_enabled}
              disabled={updating}
              onChange={(value) =>
                updateSettings({ custom_allow_enabled: value })
              }
            />
            <SettingsRow
              title="自定义重写"
              description="启用自定义 A、AAAA 和 CNAME 重写规则。"
              checked={settings.custom_rewrite_enabled}
              disabled={updating}
              onChange={(value) =>
                updateSettings({ custom_rewrite_enabled: value })
              }
            />
          </Card>
          <Card title="添加拦截或放行规则">
            <CompactRuleForm
              action="decision"
              onDone={() => setVersion((current) => current + 1)}
            />
          </Card>
          <Card title="添加重写规则">
            <CompactRuleForm
              action="rewrite"
              onDone={() => setVersion((current) => current + 1)}
            />
          </Card>
          <RuleGroup
            title="拦截规则"
            description="匹配后阻止该 DNS 查询。"
            rules={rules.filter((rule) => rule.action === "block")}
            onEdit={setEditing}
            onDelete={remove}
          />
          <RuleGroup
            title="放行规则"
            description="匹配后允许查询继续执行。"
            rules={rules.filter((rule) => rule.action === "allow")}
            onEdit={setEditing}
            onDelete={remove}
          />
          <RuleGroup
            title="重写规则"
            description="以指定 A、AAAA 或 CNAME 应答替代查询结果。"
            rules={rules.filter((rule) => rule.action === "rewrite")}
            onEdit={setEditing}
            onDelete={remove}
          />
        </>
      )}
      {editing ? (
        <Modal title="编辑规则" onClose={() => setEditing(null)}>
          <RuleForm
            rule={editing}
            onDone={() => {
              setEditing(null);
              setVersion((current) => current + 1);
            }}
            onCancel={() => setEditing(null)}
          />
        </Modal>
      ) : null}
    </>
  );
}

function lookupRecordText(record: LookupRecord) {
  const value = record.value ?? record.data ?? "";
  return (
    [
      record.name,
      record.ttl === undefined ? "" : String(record.ttl),
      record.type,
      value,
    ]
      .filter(Boolean)
      .join(" ") || "空记录"
  );
}

function LookupRecords({
  title,
  records,
}: {
  title: string;
  records: LookupRecord[];
}) {
  return (
    <Card title={title}>
      {records.length ? (
        <ul className="dns-records">
          {records.map((record, index) => (
            <li key={`${record.name ?? "record"}-${index}`}>
              <code>{lookupRecordText(record)}</code>
            </li>
          ))}
        </ul>
      ) : (
        <Empty>无记录</Empty>
      )}
    </Card>
  );
}

export function LookupPage() {
  const [name, setName] = useState("");
  const [qtype, setQType] = useState<(typeof lookupTypes)[number]>("A");
  const [data, setData] = useState<LookupResult | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const question = name.trim();
    if (!question) return;
    setBusy(true);
    setError("");
    try {
      setData(
        await request<LookupResult>(
          "/me/lookup",
          json("POST", { name: question, qtype }),
        ),
      );
    } catch (reason) {
      setError(message(reason));
      setData(null);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <PageTitle
        title="Lookup"
        description="使用当前账户策略执行一次 DNS 查询。"
      />
      <Card title="查询">
        <form className="lookup-form" onSubmit={submit}>
          <Field label="记录类型">
            <select
              value={qtype}
              onChange={(event) =>
                setQType(event.target.value as (typeof lookupTypes)[number])
              }
            >
              {lookupTypes.map((type) => (
                <option key={type}>{type}</option>
              ))}
            </select>
          </Field>
          <Field label="域名">
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="example.com"
              maxLength={255}
              required
            />
          </Field>
          <Field label="ECS（CIDR）" hint="此节点未支持 Lookup ECS。">
            <input disabled placeholder="例如 203.0.113.0/24" />
          </Field>
          <button className="primary" disabled={busy}>
            {busy ? "正在查询…" : "开始查询"}
          </button>
        </form>
        <Alert error={error} />
      </Card>
      {data ? (
        <>
          <div className="metrics">
            <Metric label="响应码" value={data.rcode || "UNKNOWN"} />
            <Metric
              label="查询耗时"
              value={`${data.duration_ms.toFixed(2)} ms`}
            />
            <Metric
              label="问题"
              value={`${data.question.name} · ${data.question.qtype}`}
            />
          </div>
          <LookupRecords title="Answer" records={data.answers ?? []} />
          <div className="grid2">
            <LookupRecords title="Authority" records={data.authority ?? []} />
            <LookupRecords title="Additional" records={data.additional ?? []} />
          </div>
          <Card title="EDNS">
            <div className="rows">
              <div>
                <span>携带 EDNS</span>
                <strong>
                  {data.edns?.present
                    ? `v${data.edns.version} · UDP ${data.edns.udp_size} bytes`
                    : "否"}
                </strong>
              </div>
              <div>
                <span>DNSSEC OK</span>
                <strong>{data.edns?.dnssec_ok ? "是" : "否"}</strong>
              </div>
              <div>
                <span>ECS</span>
                <strong>
                  {data.edns?.ecs
                    ? `${data.edns.ecs.address}/${data.edns.ecs.source_prefix}`
                    : "无"}
                </strong>
              </div>
            </div>
          </Card>
        </>
      ) : null}
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
      <PageTitle title="帮助与支持" description="面板内的常用功能说明。" />
      <div className="grid2 help-grid">
        <Card title="设备接入">
          <p>
            先在账户中心创建一个设备凭证。专属 DoH 地址和 Bearer Token
            仅会显示一次，请在设备端安全保存。
          </p>
          <p>凭证遗失或设备不再使用时，可在账户中心轮换或撤销它。</p>
        </Card>
        <Card title="安全与隐私">
          <p>
            移除 ECS
            可减少向上游暴露的网络信息；拦截私有地址应答可降低被引导到本地网络地址的风险。
          </p>
        </Card>
        <Card title="自定义规则">
          <p>
            规则按匹配方式处理你的查询。使用正则匹配前请先在 DNS Lookup
            验证域名和记录类型，避免意外影响常用服务。
          </p>
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

export function PublicListsPage() {
  const load = useCallback(
    (signal: AbortSignal) =>
      allPages<UserPublicList>("/me/public-lists", signal),
    [],
  );
  const { data, error: loadError, loading, setData } = useLoad(load, [load]);
  const [error, setError] = useState("");
  const [updating, setUpdating] = useState<Set<string>>(new Set());
  const groups = useMemo(() => {
    const grouped = new Map<string, UserPublicList[]>();
    for (const item of data ?? []) {
      const category = item.list.category?.trim() || "未分类";
      const items = grouped.get(category) ?? [];
      items.push(item);
      grouped.set(category, items);
    }
    return [...grouped.entries()].sort(([left], [right]) =>
      left.localeCompare(right, "zh-CN"),
    );
  }, [data]);
  async function update(item: UserPublicList, enabled: boolean) {
    const id = item.list.id;
    setUpdating((current) => new Set(current).add(id));
    setError("");
    setData((current) =>
      current?.map((entry) =>
        entry.list.id === id ? { ...entry, enabled, overridden: true } : entry,
      ),
    );
    try {
      await request(
        `/me/public-lists/${encodeURIComponent(id)}`,
        json("PATCH", { enabled }),
      );
    } catch (reason) {
      setData((current) =>
        current?.map((entry) => (entry.list.id === id ? item : entry)),
      );
      setError(message(reason));
    } finally {
      setUpdating((current) => {
        const next = new Set(current);
        next.delete(id);
        return next;
      });
    }
  }
  return (
    <>
      <PageTitle
        title="订阅的公共列表"
        description="选择适用于当前账户的公共 DNS 规则源。"
      />
      <Alert error={loadError || error} />
      {loading ? <Spinner /> : null}
      {!loading && data?.length === 0 ? (
        <Empty>管理员尚未发布公共列表。</Empty>
      ) : null}
      {groups.map(([category, items]) => (
        <Card
          key={category}
          title={category}
          className="settings-list public-list-group"
        >
          {items.map((item) => (
            <div className="settings-row public-list-row" key={item.list.id}>
              <div>
                <strong>{item.list.name}</strong>
                <small>
                  {item.overridden
                    ? "已覆盖管理员默认设置"
                    : "继承管理员默认设置"}{" "}
                  · {item.list.format} · 每 {item.list.refresh_seconds} 秒刷新
                </small>
                <small className="public-list-state">
                  {publicListRuntimeSummary(item.list)}
                </small>
              </div>
              <label className="switch">
                <input
                  type="checkbox"
                  checked={item.enabled}
                  disabled={updating.has(item.list.id)}
                  aria-label={`启用 ${item.list.name}`}
                  onChange={(event) => update(item, event.target.checked)}
                />
                <span aria-hidden />
              </label>
            </div>
          ))}
        </Card>
      ))}
    </>
  );
}

function publicListRuntimeSummary(list: PublicList) {
  const entries =
    list.entry_count === undefined
      ? "条目数待刷新"
      : `${fmt.num(list.entry_count)} 条目`;
  const refreshed = list.last_refreshed_at
    ? `上次刷新 ${fmt.date(list.last_refreshed_at)}`
    : "尚未刷新";
  const status =
    list.last_refresh_status === "success"
      ? "刷新成功"
      : list.last_refresh_status === "error"
        ? "刷新失败"
        : list.last_refresh_status === "never"
          ? "从未刷新"
          : list.last_refresh_status;
  return list.last_refresh_error
    ? `${entries} · 刷新失败：${list.last_refresh_error} · ${refreshed}`
    : `${entries} · ${status ? status + " · " : ""}${refreshed}`;
}

function publicListPayload(form: HTMLFormElement) {
  const fields = new FormData(form);
  return {
    name: String(fields.get("name")).trim(),
    category: String(fields.get("category")).trim(),
    url: String(fields.get("url")).trim(),
    format: String(fields.get("format")) as PublicListFormat,
    enabled: fields.get("enabled") === "on",
    sha256: String(fields.get("sha256")).trim(),
    refresh_seconds: Number(fields.get("refresh_seconds")),
  };
}

function validPublicListURL(value: string) {
  try {
    return new URL(value).protocol === "https:";
  } catch {
    return false;
  }
}

function PublicListForm({
  list,
  onDone,
  onCancel,
}: {
  list?: PublicList;
  onDone: () => void;
  onCancel?: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const payload = publicListPayload(form);
    if (
      !payload.name ||
      !payload.category ||
      !validPublicListURL(payload.url)
    ) {
      setError("请填写名称、分类和 HTTPS 地址。");
      return;
    }
    if (
      !Number.isInteger(payload.refresh_seconds) ||
      payload.refresh_seconds < 300 ||
      payload.refresh_seconds > 86_400
    ) {
      setError("刷新秒数必须是 300 到 86400 的整数。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await request<PublicList>(
        list
          ? `/admin/public-lists/${encodeURIComponent(list.id)}`
          : "/admin/public-lists",
        json(list ? "PATCH" : "POST", payload),
      );
      if (!list) form.reset();
      onDone();
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="public-list-form" onSubmit={submit}>
      <Alert error={error} />
      <div className="form-grid">
        <Field label="名称">
          <input
            name="name"
            defaultValue={list?.name}
            maxLength={128}
            required
          />
        </Field>
        <Field label="分类">
          <input
            name="category"
            defaultValue={list?.category}
            placeholder="例如 广告与追踪"
            maxLength={64}
            required
          />
        </Field>
      </div>
      <Field label="HTTPS URL">
        <input
          name="url"
          type="url"
          defaultValue={list?.url}
          placeholder="https://example.com/list.txt"
          maxLength={2048}
          required
        />
      </Field>
      <div className="form-grid">
        <Field label="格式">
          <select name="format" defaultValue={list?.format ?? "mosdns"}>
            <option value="mosdns">mosdns</option>
            <option value="hosts">hosts</option>
          </select>
        </Field>
        <Field label="刷新秒数">
          <input
            name="refresh_seconds"
            type="number"
            min="300"
            max="86400"
            defaultValue={list?.refresh_seconds ?? 3600}
            required
          />
        </Field>
      </div>
      <Field
        label="SHA-256（可选）"
        hint="填写 Release 或规则源公布的 64 位十六进制校验值。"
      >
        <input name="sha256" defaultValue={list?.sha256} maxLength={64} />
      </Field>
      <label className="check">
        <input
          name="enabled"
          type="checkbox"
          defaultChecked={list?.enabled ?? true}
        />
        默认启用此列表
      </label>
      <div className="actions">
        {onCancel ? (
          <button type="button" onClick={onCancel}>
            取消
          </button>
        ) : null}
        <button className="primary" disabled={busy}>
          {busy ? "正在保存…" : list ? "保存列表" : "添加公共列表"}
        </button>
      </div>
    </form>
  );
}

export function AdminPublicListsPage() {
  const [version, setVersion] = useState(0);
  const [editing, setEditing] = useState<PublicList | null>(null);
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState<Set<string>>(new Set());
  const {
    items,
    cursor,
    loading,
    error: loadError,
    more,
  } = usePaged<PublicList>("/admin/public-lists", version);
  const reload = () => setVersion((current) => current + 1);
  async function refresh(list: PublicList) {
    setRefreshing((current) => new Set(current).add(list.id));
    setError("");
    try {
      await request(
        `/admin/public-lists/${encodeURIComponent(list.id)}/refresh`,
        {
          method: "POST",
        },
      );
      reload();
    } catch (reason) {
      setError(message(reason));
    } finally {
      setRefreshing((current) => {
        const next = new Set(current);
        next.delete(list.id);
        return next;
      });
    }
  }
  async function remove(list: PublicList) {
    if (!confirm(`删除公共列表“${list.name}”？`)) return;
    setError("");
    try {
      await request(`/admin/public-lists/${encodeURIComponent(list.id)}`, {
        method: "DELETE",
      });
      reload();
    } catch (reason) {
      setError(message(reason));
    }
  }
  return (
    <>
      <PageTitle
        title="公共列表"
        description="发布可由用户订阅的远程 DNS 规则列表。"
      />
      <Alert error={loadError || error} />
      <Card title="添加公共列表">
        <PublicListForm onDone={reload} />
      </Card>
      <Card title="已发布列表">
        {loading ? <Spinner /> : null}
        {!loading && items.length === 0 ? (
          <Empty>尚未发布公共列表。</Empty>
        ) : null}
        {items.length ? (
          <div className="table-wrap">
            <table className="public-list-table">
              <thead>
                <tr>
                  <th>名称与分类</th>
                  <th>来源</th>
                  <th>刷新状态</th>
                  <th>默认</th>
                  <th aria-label="操作" />
                </tr>
              </thead>
              <tbody>
                {items.map((list) => (
                  <tr key={list.id}>
                    <td>
                      <strong>{list.name}</strong>
                      <small>{list.category?.trim() || "未分类"}</small>
                    </td>
                    <td>
                      <code>{list.format}</code>
                      <small>{list.url}</small>
                      <small>
                        每 {list.refresh_seconds} 秒 ·{" "}
                        {list.sha256 ? "已校验 SHA-256" : "未设 SHA-256"}
                      </small>
                    </td>
                    <td>
                      <span
                        className={`badge ${list.last_refresh_error ? "off" : "ok"}`}
                      >
                        {list.last_refresh_status ||
                          (list.last_refresh_error ? "失败" : "未刷新")}
                      </span>
                      <small>{publicListRuntimeSummary(list)}</small>
                    </td>
                    <td>
                      <span className={`badge ${list.enabled ? "ok" : "off"}`}>
                        {list.enabled ? "启用" : "停用"}
                      </span>
                    </td>
                    <td className="table-actions">
                      <button
                        disabled={refreshing.has(list.id)}
                        onClick={() => refresh(list)}
                      >
                        {refreshing.has(list.id) ? "刷新中…" : "刷新"}
                      </button>
                      <button onClick={() => setEditing(list)}>编辑</button>
                      <button className="danger" onClick={() => remove(list)}>
                        删除
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
        {cursor ? (
          <button onClick={more} disabled={loading}>
            加载更多
          </button>
        ) : null}
      </Card>
      {editing ? (
        <Modal title="编辑公共列表" onClose={() => setEditing(null)}>
          <PublicListForm
            list={editing}
            onDone={() => {
              setEditing(null);
              reload();
            }}
            onCancel={() => setEditing(null)}
          />
        </Modal>
      ) : null}
    </>
  );
}

export function LabsPage() {
  return (
    <>
      <PageTitle
        title="实验性功能"
        description="以下功能需要节点提供额外实现，当前服务未启用。"
      />
      <UnavailableGroup
        title="Web3 与替代根"
        items={["ENS / Web3 域名解析", "替代 DNS 根"]}
      />
      <UnavailableGroup
        title="快捷功能与自定义上游"
        items={["一键安全模式", "自定义实验上游"]}
      />
      <UnavailableGroup
        title="网络与响应优化"
        items={["ECS 实验模式", "IPv4 / IPv6 响应偏好", "响应记录优化"]}
      />
      <UnavailableGroup
        title="实验查询类型"
        items={["HTTPS / SVCB 处理", "新兴 qtype 分流"]}
      />
    </>
  );
}

function UnavailableGroup({
  title,
  items,
}: {
  title: string;
  items: string[];
}) {
  return (
    <Card title={title}>
      <div className="capability-list">
        {items.map((item) => (
          <div key={item}>
            <div>
              <strong>{item}</strong>
              <small>节点未配置此能力。</small>
            </div>
            <label className="switch">
              <input type="checkbox" disabled aria-label={item} />
              <span aria-hidden />
            </label>
          </div>
        ))}
      </div>
    </Card>
  );
}

export function AdvancedPage() {
  const load = useCallback(
    (signal: AbortSignal) => request<UserSettings>("/me/settings", { signal }),
    [],
  );
  const { data, error: loadError, loading, setData } = useLoad(load, [load]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const settings = data ? normalizeSettings(data) : undefined;
  async function update(patch: Partial<UserSettings>) {
    if (!settings) return;
    setBusy(true);
    setError("");
    try {
      const updated = await request<UserSettings>(
        "/me/settings",
        json("PATCH", patch),
      );
      setData(normalizeSettings(updated));
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <PageTitle
        title="高级设置"
        description="集中调整当前节点实际支持的 DNS 策略。"
      />
      {loading ? <Spinner /> : <Alert error={loadError || error} />}
      {settings ? (
        <Card title="安全与规则" className="settings-list">
          <SettingsRow
            title="DNS 重绑定防护"
            description="拦截私有地址应答。"
            checked={settings.block_private_answers}
            disabled={busy}
            onChange={(value) => update({ block_private_answers: value })}
          />
          <SettingsRow
            title="停用 ECS"
            description="不向上游携带客户端网段。"
            checked={settings.strip_ecs}
            disabled={busy}
            onChange={(value) => update({ strip_ecs: value })}
          />
          <SettingsRow
            title="启用拦截规则"
            description="应用自定义拦截规则组。"
            checked={settings.custom_block_enabled}
            disabled={busy}
            onChange={(value) => update({ custom_block_enabled: value })}
          />
          <SettingsRow
            title="启用放行规则"
            description="应用自定义放行规则组。"
            checked={settings.custom_allow_enabled}
            disabled={busy}
            onChange={(value) => update({ custom_allow_enabled: value })}
          />
          <SettingsRow
            title="启用重写规则"
            description="应用自定义重写规则组。"
            checked={settings.custom_rewrite_enabled}
            disabled={busy}
            onChange={(value) => update({ custom_rewrite_enabled: value })}
          />
          <div className="settings-row">
            <div>
              <strong>暂停客制化 DNS</strong>
              <small>暂停期间临时绕过本账户的安全设置和自定义规则。</small>
            </div>
            <select
              aria-label="暂停客制化 DNS"
              value={pauseSelection(settings.policy_paused_until)}
              disabled={busy}
              onChange={(event) =>
                update({ policy_paused_until: pauseUntil(event.target.value) })
              }
            >
              {policyPaused(settings.policy_paused_until) ? (
                <option value="paused" disabled>
                  当前暂停至 {fmt.date(settings.policy_paused_until)}
                </option>
              ) : null}
              <option value="0">不暂停</option>
              <option value="900">暂停 15 分钟</option>
              <option value="1800">暂停 30 分钟</option>
              <option value="3600">暂停 1 小时</option>
              <option value="10800">暂停 3 小时</option>
              <option value="21600">暂停 6 小时</option>
              <option value="43200">暂停 12 小时</option>
              <option value="86400">暂停 1 天</option>
            </select>
          </div>
        </Card>
      ) : null}
      <UnavailableGroup
        title="缓存 / ECS"
        items={["自定义缓存策略", "ECS 地址覆写"]}
      />
      <UnavailableGroup title="日志" items={["扩展查询日志", "长期日志保留"]} />
      <UnavailableGroup
        title="兼容"
        items={["客户端兼容模式", "传统 DNS 协议接入"]}
      />
      <UnavailableGroup
        title="功能管理"
        items={["按设备功能配置", "服务端插件管理"]}
      />
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
      {loading ? <Spinner /> : <Alert error={error} />}{" "}
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
