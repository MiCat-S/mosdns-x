import { useState, type FormEvent } from "react";
import { json, message, request } from "./api";
import { Alert, Card, Field } from "./components";
import { t } from "./i18n";
import type { CachePurgeResult } from "./types";

// cacheDomain turns what an administrator typed or pasted (a name, a URL)
// into the name to purge. The server validates it again.
export function cacheDomain(text: string) {
  let name = text.trim();
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(name)) {
    try {
      name = new URL(name).hostname;
    } catch {
      return { name: "", error: t("请输入有效的域名，例如 example.com。") };
    }
  }
  if (name.startsWith("*."))
    return { name: "", error: t("不需要输入 *，勾选“同时清除子域名”即可。") };
  if (!name || name === ".")
    return { name: "", error: t("请输入有效的域名，例如 example.com。") };
  return { name, error: "" };
}

export function cachePurgeSummary(result: CachePurgeResult) {
  if (!result.caches) return t("当前运行配置中没有缓存插件。");
  const domain = result.domain.replace(/\.$/, "");
  if (!result.removed)
    return result.subdomains
      ? t("缓存中没有 {domain} 及其子域名的记录。", { domain })
      : t("缓存中没有 {domain} 的记录。", { domain });
  return result.subdomains
    ? t("已清除 {domain} 及其子域名的 {count} 条缓存记录。", {
        domain,
        count: result.removed,
      })
    : t("已清除 {domain} 的 {count} 条缓存记录。", {
        domain,
        count: result.removed,
      });
}

export function purgeDomainCache(domain: string, subdomains: boolean) {
  return request<CachePurgeResult>(
    "/admin/runtime/cache/purge",
    json("POST", { domain, subdomains }),
  );
}

export function CachePurgeCard() {
  const [domain, setDomain] = useState("");
  const [subdomains, setSubdomains] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const input = cacheDomain(domain);
    setResult("");
    setError(input.error);
    if (input.error) return;
    setBusy(true);
    try {
      setResult(
        cachePurgeSummary(await purgeDomainCache(input.name, subdomains)),
      );
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card title={t("清除域名缓存")} className="cache-purge">
      <p className="caption">
        {t(
          "删除该域名在所有缓存插件中的记录（全部查询类型），下一次查询会重新向上游请求。",
        )}
      </p>
      <form onSubmit={submit}>
        <Alert error={error} />
        <div className="cache-purge-row">
          <Field label={t("域名")}>
            <input
              name="domain"
              value={domain}
              onChange={(event) => setDomain(event.target.value)}
              placeholder="example.com"
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              maxLength={1024}
              required
            />
          </Field>
          <button className="primary" disabled={busy}>
            {busy ? t("正在清除…") : t("清除缓存")}
          </button>
        </div>
        <label className="check">
          <input
            type="checkbox"
            checked={subdomains}
            onChange={(event) => setSubdomains(event.target.checked)}
          />
          {t("同时清除子域名")}
        </label>
        {result ? (
          <p className="caption" role="status">
            {result}
          </p>
        ) : null}
      </form>
    </Card>
  );
}

// PurgeDomainButton clears one query name from the caches, for the query
// log detail.
export function PurgeDomainButton({ domain }: { domain: string }) {
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  async function purge() {
    setBusy(true);
    setStatus("");
    try {
      setStatus(cachePurgeSummary(await purgeDomainCache(domain, false)));
    } catch (e) {
      setStatus(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <span className="cache-purge-inline">
      <button type="button" className="btn-sm" onClick={purge} disabled={busy}>
        {busy ? t("正在清除…") : t("清除此域名缓存")}
      </button>
      {status ? <small role="status">{status}</small> : null}
    </span>
  );
}
