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
  return error instanceof Error ? error.message : "请求失败，请稍后重试。";
}
