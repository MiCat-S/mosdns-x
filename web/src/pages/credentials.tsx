import { useCallback, useState, type FormEvent } from "react";
import { allPages, json, message, request, requestVoid } from "../api";
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
import { msg, t } from "../i18n";
import type { Credential, IssuedCredential } from "../types";

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
    <Modal title={t("凭证已签发")} onClose={onClose}>
      <div className="notice">
        {t("请现在保存。关闭后将无法再次查看令牌。")}
      </div>
      <Field label={t("DNS 地址")}>
        <div className="copy">
          <code>{issued.doh_url}</code>
          <button
            type="button"
            onClick={() => copy(msg("已复制地址"), issued.doh_url)}
          >
            {t("复制")}
          </button>
        </div>
      </Field>
      <Field label={t("Bearer 令牌")}>
        <div className="copy">
          <code>{issued.token}</code>
          <button
            type="button"
            onClick={() => copy(msg("已复制令牌"), issued.token)}
          >
            {t("复制")}
          </button>
        </div>
      </Field>
      {copied ? <p role="status">{t(copied)}</p> : null}
      <Card title={t("接入说明")}>
        <p>{t("可直接使用上方专属地址，或在固定 DNS 地址的请求中添加：")}</p>
        <code>{t("Authorization: Bearer <令牌>")}</code>
      </Card>
      <div className="actions">
        <button className="primary" onClick={onClose}>
          {t("我已保存，关闭")}
        </button>
      </div>
    </Modal>
  );
}
// credentialStatus returns the Chinese source text (compared in code and
// tests); translate it with t() where it is displayed.
const credentialStates = {
  active: msg("有效"),
  revoked: msg("已撤销"),
  expired: msg("已到期"),
};
export function credentialStatus(c: Credential) {
  if (c.revoked_at && !c.revoked_at.startsWith("0001-"))
    return credentialStates.revoked;
  if (
    c.expires_at &&
    !c.expires_at.startsWith("0001-") &&
    new Date(c.expires_at).getTime() <= Date.now()
  )
    return credentialStates.expired;
  return credentialStates.active;
}
export function credentialCanRevoke(c: Credential) {
  return credentialStatus(c) !== credentialStates.revoked;
}
export function CredentialManager({
  base,
  max,
}: {
  base: string;
  max?: number;
}) {
  const [version, setVersion] = useState(0),
    [issued, setIssued] = useState<IssuedCredential | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(""),
    [pending, setPending] = useState<{
      kind: "rotate" | "revoke";
      c: Credential;
    } | null>(null);
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
    setPending(null);
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
    setPending(null);
    setBusy(c.id);
    try {
      await requestVoid(`${base}/credentials/${c.id}`, { method: "DELETE" });
      setVersion((x) => x + 1);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  }
  const activeCount =
    data?.filter((c) => credentialStatus(c) === credentialStates.active)
      .length ?? 0;
  return (
    <>
      <Card title={t("创建凭证")}>
        <form className="inline-form" onSubmit={create}>
          <Field label={t("名称")}>
            <input
              name="name"
              required
              maxLength={80}
              autoComplete="off"
              placeholder={t("例如：家中路由器…")}
            />
          </Field>
          <Field label={t("单独到期")} hint={t("留空则跟随账户")}>
            <input name="expires_at" type="datetime-local" />
          </Field>
          <button
            className="primary"
            disabled={
              busy === "create" || (max !== undefined && activeCount >= max)
            }
          >
            {busy === "create" ? t("正在创建…") : t("创建凭证")}
          </button>
        </form>
        {max !== undefined ? (
          <p className="caption">
            {t("已使用 {used} / {max} 个凭证名额", {
              used: activeCount,
              max,
            })}
          </p>
        ) : null}
      </Card>
      <Alert error={error || loadError} />
      {loading ? (
        <Spinner />
      ) : data?.length ? (
        <div
          className="table-wrap"
          role="region"
          aria-label={t("凭证列表")}
          tabIndex={0}
        >
          <table>
            <thead>
              <tr>
                <th>{t("名称")}</th>
                <th>{t("创建")}</th>
                <th>{t("单独到期")}</th>
                <th>{t("状态")}</th>
                <th>
                  <span className="sr-only">{t("操作")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {data.map((c) => {
                const status = credentialStatus(c);
                const active = status === credentialStates.active;
                return (
                  <tr key={c.id}>
                    <td>
                      <strong>{c.name}</strong>
                    </td>
                    <td>{fmt.date(c.created_at)}</td>
                    <td>{fmt.date(c.expires_at)}</td>
                    <td>
                      <span className={`badge ${active ? "ok" : "off"}`}>
                        {t(status)}
                      </span>
                    </td>
                    <td className="table-actions">
                      <button
                        className="btn-sm"
                        aria-label={t("轮换 {name}", { name: c.name })}
                        disabled={busy === c.id || !active}
                        onClick={() => setPending({ kind: "rotate", c })}
                      >
                        {t("轮换")}
                      </button>
                      <button
                        className="danger btn-sm"
                        aria-label={t("撤销 {name}", { name: c.name })}
                        disabled={busy === c.id || !credentialCanRevoke(c)}
                        onClick={() => setPending({ kind: "revoke", c })}
                      >
                        {t("撤销")}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty>{t("暂无凭证")}</Empty>
      )}
      {pending ? (
        <Modal
          title={pending.kind === "rotate" ? t("轮换凭证") : t("撤销凭证")}
          onClose={() => setPending(null)}
        >
          <p className="confirm-text">
            {pending.kind === "rotate"
              ? t(
                  "轮换“{name}”后，旧令牌会立即失效，使用它的设备将停止解析，需要换用新令牌。",
                  { name: pending.c.name },
                )
              : t("撤销“{name}”会立即中断使用它的设备，此操作无法撤销。", {
                  name: pending.c.name,
                })}
          </p>
          <div className="actions">
            <button type="button" onClick={() => setPending(null)}>
              {t("取消")}
            </button>
            <button
              type="button"
              className={pending.kind === "rotate" ? "primary" : "danger"}
              onClick={() =>
                pending.kind === "rotate"
                  ? rotate(pending.c)
                  : revoke(pending.c)
              }
            >
              {pending.kind === "rotate" ? t("轮换") : t("撤销")}
            </button>
          </div>
        </Modal>
      ) : null}
      {issued ? (
        <Secret issued={issued} onClose={() => setIssued(null)} />
      ) : null}
    </>
  );
}
