import { beforeEach, describe, expect, it, vi } from "vitest";
import { clearSecrets, json, onUnauthorized, request, setCSRF } from "./api";

beforeEach(() => {
  clearSecrets();
  vi.restoreAllMocks();
});
describe("API 会话保护", () => {
  it("写请求携带内存中的 CSRF，读请求不携带", async () => {
    const fetcher = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetcher);
    setCSRF("csrf-value");
    await request("/me/password", json("POST", {}));
    let init = fetcher.mock.calls[0][1] as RequestInit;
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf-value");
    await request("/session");
    init = fetcher.mock.calls[1][1] as RequestInit;
    expect(new Headers(init.headers).has("X-CSRF-Token")).toBe(false);
  });
  it("401 通知会话失效并清除 CSRF", async () => {
    const lost = vi.fn();
    onUnauthorized(lost);
    setCSRF("secret");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: { code: "invalid_credential", message: "登录失效" },
          }),
          { status: 401, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );
    await expect(request("/me")).rejects.toThrow("登录失效");
    expect(lost).toHaveBeenCalledOnce();
  });
});
