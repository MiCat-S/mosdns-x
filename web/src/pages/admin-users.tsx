import { useCallback, useMemo, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  allPages,
  APIError,
  json,
  message,
  request,
  requestVoid,
} from "../api";
import {
  Alert,
  Empty,
  Field,
  Metric,
  Modal,
  PageTitle,
  Spinner,
  fmt,
  useLoad,
} from "../components";
import { useSession } from "../session";
import { t } from "../i18n";
import { useTopDomains } from "../top-domains";
import { CredentialManager } from "./credentials";
import { QueryDetails } from "./query-log";
import {
  fromLocalDateTime,
  query,
  range,
  toLocalDateTime,
  usePaged,
} from "./shared";
import { DeviceUsage, StatsBlocks } from "./stats";
import type { Me, Stats, UsagePoint, User } from "../types";

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
            <Field label={t("用户名")}>
              <input name="username" required autoComplete="off" />
            </Field>
            <Field label={t("初始密码")}>
              <input
                name="password"
                type="password"
                required
                minLength={12}
                maxLength={1024}
                autoComplete="new-password"
              />
            </Field>
            <Field label={t("角色")}>
              <select name="role" defaultValue={v.role}>
                <option value="user">{t("普通用户")}</option>
                <option value="admin">{t("管理员")}</option>
              </select>
            </Field>
          </>
        ) : null}
        <Field label={t("周期")}>
          <select name="period" defaultValue={v.period}>
            <option value="daily">{t("每日")}</option>
            <option value="monthly">{t("每月")}</option>
          </select>
        </Field>
        <Field label={t("时区")}>
          <input name="timezone" defaultValue={v.timezone} required />
        </Field>
        <Field label={t("到期时间")} hint={t("留空表示长期有效")}>
          <input
            name="expires_at"
            type="datetime-local"
            defaultValue={toLocalDateTime(v.expires_at)}
          />
        </Field>
        <Field label={t("请求额度")}>
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
          label={t("额外突发量")}
          hint={t("允许在 QPS 之外额外突发，0 表示不额外放行")}
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
        <Field label={t("最多凭证")}>
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
          {t("启用账户")}
        </label>
      </div>
      <div className="actions">
        <button type="button" onClick={onCancel}>
          {t("取消")}
        </button>
        <button className="primary" disabled={busy}>
          {busy ? t("正在保存…") : t("保存")}
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
        title={t("用户管理")}
        description={t("设置账户状态、服务期限、额度与速率")}
        action={
          <button className="primary" onClick={() => setModal(true)}>
            {t("＋ 开设账户")}
          </button>
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {data?.length ? (
        <div
          className="table-wrap"
          role="region"
          aria-label={t("用户列表")}
          tabIndex={0}
        >
          <table>
            <thead>
              <tr>
                <th>{t("用户")}</th>
                <th>{t("状态")}</th>
                <th>{t("周期额度")}</th>
                <th>{t("QPS / 突发")}</th>
                <th>{t("到期")}</th>
                <th>
                  <span className="sr-only">{t("操作")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {data.map((u) => (
                <tr key={u.id}>
                  <td>
                    <strong>{u.username}</strong>
                    <small>
                      {u.role === "admin" ? t("管理员") : t("普通用户")}
                    </small>
                  </td>
                  <td>
                    <span className={`badge ${u.enabled ? "ok" : "off"}`}>
                      {u.enabled ? t("启用") : t("停用")}
                    </span>
                  </td>
                  <td>
                    {u.period === "daily"
                      ? t("{limit} / 日", { limit: fmt.num(u.limit) })
                      : t("{limit} / 月", { limit: fmt.num(u.limit) })}
                  </td>
                  <td>
                    {u.qps} / {u.burst}
                  </td>
                  <td>{fmt.date(u.expires_at)}</td>
                  <td>
                    <button
                      className="link"
                      aria-label={t("查看 {name}", { name: u.username })}
                      onClick={() => nav(`/admin/users/${u.id}`)}
                    >
                      {t("查看")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : !loading && !error ? (
        <Empty
          action={
            <button className="primary" onClick={() => setModal(true)}>
              {t("开设账户")}
            </button>
          }
        >
          {t("尚未开设账户")}
        </Empty>
      ) : null}
      {cursor ? (
        <div className="table-more">
          <button onClick={more} disabled={loading}>
            {loading ? t("正在加载…") : t("加载更多")}
          </button>
        </div>
      ) : null}
      {modal ? (
        <Modal title={t("开设账户")} onClose={() => setModal(false)}>
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
  const { data, error, loading } = useLoad(load, [load], id);
  const topDomains = useTopDomains(
    `/admin/top-domains?user_id=${encodeURIComponent(id)}`,
    params.from,
    params.to,
  );
  const account = data?.[0];
  return (
    <>
      <PageTitle
        title={account?.user.username ?? t("用户详情")}
        description={t("账户状态、当前额度与代管设备凭证")}
        action={
          account ? (
            <>
              <button onClick={() => setResetPassword(true)}>
                {t("重置密码")}
              </button>
              <button onClick={() => setEdit(true)}>{t("编辑账户")}</button>
              {session?.user.id !== id ? (
                <button className="danger" onClick={() => setRemoving(true)}>
                  {t("删除用户")}
                </button>
              ) : null}
            </>
          ) : null
        }
      />
      {loading ? <Spinner /> : <Alert error={error} />}
      {account ? (
        <>
          <div className="metrics">
            <Metric
              label={t("剩余额度")}
              value={fmt.num(account.quota.remaining)}
              hint={t("已用 {used} / {limit}", {
                used: fmt.num(account.quota.used),
                limit: fmt.num(account.quota.limit),
              })}
            />
            <Metric
              label={t("重置时间")}
              value={fmt.date(account.quota.period_end)}
            />
            <Metric
              label={t("账户到期")}
              value={fmt.date(account.user.expires_at)}
            />
            <Metric
              label={t("QPS / 突发")}
              value={`${account.user.qps} / ${account.user.burst}`}
            />
          </div>
          <CredentialManager base={base} max={account.user.max_credentials} />
          <StatsBlocks
            stats={data[1]}
            usage={data[2]}
            topDomains={topDomains}
          />
          <DeviceUsage
            path={`${base}/device-usage`}
            credentialsPath={`${base}/credentials`}
          />
          <QueryDetails
            path={`/admin/queries?user_id=${encodeURIComponent(id)}`}
            enabled={data[1].query_log_enabled}
            credentialsPath={`${base}/credentials`}
            cachePurge
          />
          {edit ? (
            <Modal title={t("编辑账户")} onClose={() => setEdit(false)}>
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
            <Modal
              title={t("重置用户密码")}
              onClose={() => setResetPassword(false)}
            >
              <AdminPasswordReset
                base={base}
                username={account.user.username}
                onDone={() => setResetPassword(false)}
              />
            </Modal>
          ) : null}
          {removing ? (
            <Modal title={t("删除用户")} onClose={() => setRemoving(false)}>
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
    return t("不能删除自己，也不能删除最后一个启用的管理员。");
  if (e instanceof APIError && e.status === 404) return t("该用户已不存在。");
  if (e instanceof APIError && e.status === 503)
    return t("账户可能已删除，但查询日志未能清除。请稍后再次删除以完成清理。");
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
      await requestVoid(base, { method: "DELETE" });
      onDone();
    } catch (e) {
      setError(deleteUserError(e));
      setBusy(false);
    }
  }
  // The sentence is one message; the name keeps its emphasis by splitting the
  // translation at the {username} placeholder.
  const [before, after = ""] = t(
    "永久删除 {username}及其全部数据：设备凭证、登录会话、用量记录、查询日志，以及与该用户相关的审计记录。",
  ).split("{username}");
  return (
    <div>
      <p>
        {before}
        <strong>{username}</strong>
        {after}
      </p>
      <p>{t("使用该用户凭证的设备会立即停止解析。此操作无法撤销。")}</p>
      <Alert error={error} />
      <div className="actions">
        <button onClick={onCancel} disabled={busy}>
          {t("取消")}
        </button>
        <button className="danger" onClick={remove} disabled={busy}>
          {busy ? t("正在删除…") : t("永久删除")}
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
      setError(t("两次输入的新密码不一致。"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      await requestVoid(
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
      <p>
        {t("重置 {username} 的密码会撤销该用户的全部面板会话。", { username })}
      </p>
      <Alert error={error} />
      <Field label={t("新密码")}>
        <input
          name="new_password"
          type="password"
          minLength={12}
          maxLength={1024}
          autoComplete="new-password"
          required
        />
      </Field>
      <Field label={t("确认新密码")}>
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
          {busy ? t("正在重置…") : t("重置密码")}
        </button>
      </div>
    </form>
  );
}
