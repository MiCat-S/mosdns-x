import type { Page } from "./types";

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

export async function request<T>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
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
    let detail: { error?: { code?: string; message?: string } } = {};
    try {
      detail = await response.json();
    } catch {
      /* malformed error response */
    }
    throw new APIError(
      response.status,
      detail.error?.code ?? "unavailable",
      detail.error?.message ?? `请求失败（${response.status}）`,
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}
export async function allPages<T>(
  path: string,
  signal?: AbortSignal,
): Promise<T[]> {
  const items: T[] = [];
  let cursor = "";
  do {
    const u = new URLSearchParams({ limit: "1000" });
    if (cursor) u.set("cursor", cursor);
    const p = await request<Page<T>>(
      `${path}${path.includes("?") ? "&" : "?"}${u}`,
      { signal },
    );
    items.push(...p.items);
    cursor = p.next_cursor ?? "";
  } while (cursor);
  return items;
}
export function json(method: string, value: unknown): RequestInit {
  return { method, body: JSON.stringify(value) };
}
export function message(error: unknown) {
  if (error instanceof DOMException && error.name === "AbortError") return "";
  if (error instanceof APIError && error.status === 403)
    return "你没有执行此操作的权限。";
  if (error instanceof APIError) {
    const messages: Record<string, string> = {
      managed_config_disabled:
        "当前未启用托管运行配置。你仍可查看只读摘要；如需验证、应用和回滚，请按当前版本说明配置托管文件。",
      revision_conflict:
        "配置已被其他操作更新。请基于最新运行基线核对草稿并重新验证。",
      invalid_config: "候选配置校验失败，请检查对应字段或查看技术详情。",
      validation_token_invalid: "验证结果已失效，请重新验证当前内容后继续。",
      validation_token_expired: "验证结果已过期，请重新验证当前内容后继续。",
      config_source_unavailable:
        "无法读取主配置来源，请检查托管文件是否存在及服务账号是否有读取权限。",
      runtime_inspector_unavailable:
        "当前节点无法提供运行配置摘要，请检查节点版本与运行状态。",
      validation_required: "此列表必须重新验证并预览后才能发布。",
      public_list_source_invalid:
        "列表来源不符合安全要求，请使用不含凭据且可公开访问的 HTTPS 地址。",
      public_list_too_large: "列表文件超过节点允许的大小，请缩小内容后重试。",
      public_list_content_invalid:
        "列表内容或 SHA-256 校验未通过，请查看格式和摘要后重试。",
      public_list_not_published: "该列表尚未发布，不能执行刷新。",
      public_list_validation_busy:
        "待确认的列表验证过多，请先完成发布或等待旧验证过期后重试。",
      public_list_conflict:
        "列表已被其他操作更新。请重新加载列表、再次验证，然后发布当前内容。",
      public_list_cleanup_pending:
        "列表记录已删除，但快照文件仍待清理；页面将重新读取实际状态，服务重启时会再次清理。",
    };
    if (messages[error.code]) return messages[error.code];
  }
  return error instanceof Error ? error.message : "请求失败，请稍后重试。";
}
