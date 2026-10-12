import type { Page, RuntimeConfig, RuntimeState, Session } from "./types";
import { t } from "./i18n";

const BASE = "/api/v1";
let csrfToken = "";
let unauthorizedHandler: (() => void) | undefined;
export class APIError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}
// ResponseError reports a successful response whose body is not what the
// panel expects, such as a missing field or an endless page cursor.
export class ResponseError extends Error {}
export function setCSRF(token: string) {
  csrfToken = token;
}
export function clearSecrets() {
  csrfToken = "";
}
export function onUnauthorized(handler: () => void) {
  unauthorizedHandler = handler;
  return () => {
    if (unauthorizedHandler === handler) unauthorizedHandler = undefined;
  };
}

// A decoder checks the shape of a parsed JSON body at the API boundary and
// throws ResponseError when it does not match.
export type Decoder<T> = (value: unknown) => T;

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function invalid(): never {
  throw new ResponseError(t("服务返回的数据格式无效。"));
}

// The error body is {"error":{"code":"…","message":"…"}}; anything else
// falls back to the status line.
function errorDetail(body: unknown) {
  const error = isObject(body) && isObject(body.error) ? body.error : {};
  return {
    code: typeof error.code === "string" ? error.code : undefined,
    message: typeof error.message === "string" ? error.message : undefined,
  };
}

async function send(path: string, init: RequestInit) {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (init.body) headers.set("Content-Type", "application/json");
  if (!["GET", "HEAD", "OPTIONS"].includes(method) && csrfToken)
    headers.set("X-CSRF-Token", csrfToken);
  const response = await fetch(`${BASE}${path}`, {
    ...init,
    method,
    headers,
    credentials: "same-origin",
  });
  if (response.status === 401) {
    clearSecrets();
    unauthorizedHandler?.();
  }
  if (!response.ok) {
    let body: unknown;
    try {
      body = await response.json();
    } catch {
      /* malformed error response */
    }
    const detail = errorDetail(body);
    throw new APIError(
      response.status,
      detail.code ?? "unavailable",
      detail.message ?? t("请求失败（{status}）", { status: response.status }),
    );
  }
  return response;
}

// request returns the JSON body of a successful response, checked by decode
// when one is given. Endpoints that answer 204 No Content use requestVoid.
export async function request<T>(
  path: string,
  init: RequestInit = {},
  decode?: Decoder<T>,
): Promise<T> {
  const response = await send(path, init);
  if (response.status === 204) invalid();
  let body: unknown;
  try {
    body = await response.json();
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    invalid();
  }
  return decode ? decode(body) : (body as T);
}
export async function requestVoid(
  path: string,
  init: RequestInit = {},
): Promise<void> {
  await send(path, init);
}

export function decodeSession(value: unknown): Session {
  if (
    !isObject(value) ||
    typeof value.csrf_token !== "string" ||
    !isObject(value.user) ||
    typeof value.user.id !== "string" ||
    typeof value.user.username !== "string" ||
    (value.user.role !== "admin" && value.user.role !== "user")
  )
    invalid();
  return value as unknown as Session;
}
// decodePage checks the envelope only; the items keep their declared type.
export function decodePage<T>(value: unknown): Page<T> {
  if (
    !isObject(value) ||
    !Array.isArray(value.items) ||
    (value.next_cursor !== undefined && typeof value.next_cursor !== "string")
  )
    invalid();
  return value as unknown as Page<T>;
}
export function decodeRuntimeConfig(value: unknown): RuntimeConfig {
  if (
    !isObject(value) ||
    typeof value.version !== "number" ||
    typeof value.query_log !== "boolean" ||
    !isObject(value.telemetry) ||
    !Array.isArray(value.plugins) ||
    !value.plugins.every(
      (plugin) =>
        isObject(plugin) &&
        typeof plugin.tag === "string" &&
        typeof plugin.type === "string",
    )
  )
    invalid();
  return value as unknown as RuntimeConfig;
}
// decodeRuntimeState checks the revision and every config the page edits or
// compares: the summary and, when present, the running source.
export function decodeRuntimeState<T extends RuntimeState = RuntimeState>(
  value: unknown,
): T {
  if (!isObject(value) || typeof value.revision !== "string") invalid();
  decodeRuntimeConfig(value.config);
  if (value.sources !== undefined) {
    if (!isObject(value.sources)) invalid();
    const running = value.sources.running;
    if (running !== undefined) {
      if (!isObject(running)) invalid();
      if (running.config !== undefined) decodeRuntimeConfig(running.config);
    }
  }
  return value as unknown as T;
}

// allPages follows next_cursor to the end. A backend that repeats a cursor
// or never stops would keep the panel fetching forever, so both end the
// walk with an error.
export const maxPages = 1000;
export async function allPages<T>(
  path: string,
  signal?: AbortSignal,
): Promise<T[]> {
  const items: T[] = [];
  const seen = new Set<string>();
  let cursor = "";
  let pages = 0;
  do {
    if (++pages > maxPages)
      throw new ResponseError(
        t("分页数量超过上限（{max} 页），已停止加载。", { max: maxPages }),
      );
    const u = new URLSearchParams({ limit: "1000" });
    if (cursor) u.set("cursor", cursor);
    const p = await request(
      `${path}${path.includes("?") ? "&" : "?"}${u}`,
      { signal },
      decodePage<T>,
    );
    items.push(...p.items);
    cursor = p.next_cursor ?? "";
    if (cursor && seen.has(cursor))
      throw new ResponseError(t("服务返回了重复的分页游标，已停止加载。"));
    seen.add(cursor);
  } while (cursor);
  return items;
}
export function json(method: string, value: unknown): RequestInit {
  return { method, body: JSON.stringify(value) };
}
export function message(error: unknown) {
  if (error instanceof DOMException && error.name === "AbortError") return "";
  if (error instanceof APIError && error.status === 403)
    return t("你没有执行此操作的权限。");
  if (error instanceof APIError) {
    const messages: Record<string, string> = {
      managed_config_disabled: t(
        "当前未启用托管运行配置。你仍可查看只读摘要；如需验证、应用和回滚，请按当前版本说明配置托管文件。",
      ),
      revision_conflict: t(
        "配置已被其他操作更新。请基于最新运行基线核对草稿并重新验证。",
      ),
      invalid_config: t("候选配置校验失败，请检查对应字段或查看技术详情。"),
      validation_token_invalid: t("验证结果已失效，请重新验证当前内容后继续。"),
      validation_token_expired: t("验证结果已过期，请重新验证当前内容后继续。"),
      config_source_unavailable: t(
        "无法读取主配置来源，请检查托管文件是否存在及服务账号是否有读取权限。",
      ),
      runtime_inspector_unavailable: t(
        "当前节点无法提供运行配置摘要，请检查节点版本与运行状态。",
      ),
      invalid_domain: t("请输入有效的域名，例如 example.com。"),
      cache_purge_failed: t("部分缓存未能清除，请查看服务日志后重试。"),
      cache_purge_busy: t("另一次清除正在进行，请稍后再试。"),
    };
    if (messages[error.code]) return messages[error.code];
  }
  return error instanceof Error ? error.message : t("请求失败，请稍后重试。");
}
