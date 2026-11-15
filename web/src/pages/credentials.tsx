import { Fragment, useCallback, useState, type FormEvent } from "react";
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
  Card,
  Empty,
  Field,
  Modal,
  Spinner,
  fmt,
  useLoad,
} from "../components";
import { msg, t } from "../i18n";
import { useFollower } from "../session";
import type { Credential, IssuedCredential } from "../types";

// Addresses lists a credential's full DoH address on each node, each with
// its own copy button.
export function Addresses({
  urls,
  single,
}: {
  urls?: { name: string; url: string }[];
  single?: string;
}) {
  const [copied, setCopied] = useState("");
  const items = urls?.length ? urls : single ? [{ name: "", url: single }] : [];
  if (!items.length) return null;
  async function copy(url: string) {
    await navigator.clipboard.writeText(url);
    setCopied(url);
  }
  return (
    <dl className="node-urls">
      {items.map(({ name, url }) => (
        <div key={url}>
          {name ? <dt>{name}</dt> : null}
          <dd className="copy">
            <code>{url}</code>
            <button
              type="button"
              className="btn-sm"
              aria-label={
                name ? t("复制 {name} 的地址", { name }) : t("复制地址")
              }
              onClick={() => copy(url)}
            >
              {copied === url ? t("已复制") : t("复制")}
            </button>
          </dd>
        </div>
      ))}
    </dl>
  );
}

function Issued({
  issued,
  onClose,
}: {
  issued: IssuedCredential;
  onClose: () => void;
}) {
  return (
    <Modal title={t("凭证已签发")} onClose={onClose}>
      <p className="caption">
        {(issued.doh_urls?.length ?? 0) > 1
          ? t(
              "以下是该凭证在各节点的完整地址，其他节点在下次同步后（30 秒内）生效。之后也可以在凭证列表中查看。",
            )
          : t("以下是该凭证的完整地址，之后也可以在凭证列表中查看。")}
      </p>
      <Addresses urls={issued.doh_urls} single={issued.doh_url} />
      <div className="actions">
        <button className="primary" onClick={onClose}>
          {t("完成")}
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
export function credentialActive(c: Credential) {
  return credentialStatus(c) === credentialStates.active;
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
  const [restoring, setRestoring] = useState<Credential | null>(null);
  // A follower lists the credentials it mirrors; they change on the master,
  // which the page says once.
  const readOnly = useFollower();
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
      {readOnly ? null : (
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
      )}
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
                {readOnly ? null : (
                  <th>
                    <span className="sr-only">{t("操作")}</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {data.map((c) => {
                const status = credentialStatus(c);
                const active = status === credentialStates.active;
                return (
                  <Fragment key={c.id}>
                    <tr>
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
                      {readOnly ? null : (
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
                      )}
                    </tr>
                    {active ? (
                      <tr className="credential-urls">
                        <td colSpan={readOnly ? 4 : 5}>
                          {c.token_available ? (
                            <Addresses urls={c.doh_urls} single={c.doh_url} />
                          ) : (
                            <p className="caption">
                              {t("该凭证签发于保存完整地址之前。")}{" "}
                              {readOnly ? (
                                t("请到主控面板录入地址。")
                              ) : (
                                <button
                                  type="button"
                                  className="link"
                                  aria-label={t("录入 {name} 的地址", {
                                    name: c.name,
                                  })}
                                  onClick={() => setRestoring(c)}
                                >
                                  {t("录入地址")}
                                </button>
                              )}
                            </p>
                          )}
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
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
        <Issued issued={issued} onClose={() => setIssued(null)} />
      ) : null}
      {restoring ? (
        <RestoreAddress
          base={base}
          credential={restoring}
          onClose={() => setRestoring(null)}
          onDone={() => {
            setRestoring(null);
            setVersion((x) => x + 1);
          }}
        />
      ) : null}
    </>
  );
}

// RestoreAddress records the token a device already uses for a credential
// issued before full addresses were kept. The server accepts it only if it
// matches the stored hash, so the device keeps working unchanged.
function RestoreAddress({
  base,
  credential,
  onClose,
  onDone,
}: {
  base: string;
  credential: Credential;
  onClose: () => void;
  onDone: () => void;
}) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const value = String(new FormData(e.currentTarget).get("token")).trim();
    setBusy(true);
    setError("");
    try {
      await request<IssuedCredential>(
        `${base}/credentials/${credential.id}/restore-token`,
        json("POST", { token: value }),
      );
      onDone();
    } catch (e) {
      setError(restoreError(e));
      setBusy(false);
    }
  }
  return (
    <Modal title={t("录入完整地址")} onClose={onClose}>
      <form onSubmit={submit}>
        <p className="confirm-text">
          {t(
            "输入设备上正在使用的“{name}”专属地址或令牌。核对与该凭证一致后保存，设备无需改动。",
            { name: credential.name },
          )}
        </p>
        <Alert error={error} />
        <Field label={t("专属地址或令牌")}>
          <input name="token" required autoComplete="off" spellCheck={false} />
        </Field>
        <div className="actions">
          <button type="button" onClick={onClose}>
            {t("取消")}
          </button>
          <button className="primary" disabled={busy}>
            {busy ? t("正在保存…") : t("保存")}
          </button>
        </div>
      </form>
    </Modal>
  );
}
function restoreError(e: unknown) {
  if (e instanceof APIError && e.code === "token_mismatch")
    return t("与该凭证不一致，请确认输入的是这台设备正在使用的地址。");
  return message(e);
}
