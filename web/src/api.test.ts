import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  allPages,
  APIError,
  clearSecrets,
  decodePage,
  decodeRuntimeState,
  decodeSession,
  json,
  maxPages,
  onUnauthorized,
  request,
  requestVoid,
  ResponseError,
  setCSRF,
} from "./api";

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

const reply = (body: unknown, status = 200) =>
  new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

describe("API 边界类型", () => {
  it("204 只由 requestVoid 接受，request 不把空响应伪装成数据", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => Promise.resolve(reply(null, 204))),
    );
    await expect(
      requestVoid("/session", { method: "DELETE" }),
    ).resolves.toBeUndefined();
    await expect(request("/session")).rejects.toBeInstanceOf(ResponseError);
  });

  it.each([
    ["null", "null"],
    ["字符串", JSON.stringify("oops")],
    ["error 不是对象", JSON.stringify({ error: "boom" })],
    ["字段类型错误", JSON.stringify({ error: { code: 7, message: [] } })],
    ["不是 JSON", "<html>bad gateway</html>"],
  ])("错误响应体为%s时仍返回 APIError", async (_, body) => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(body, { status: 502 })),
    );
    const error = await request("/me").catch((e: unknown) => e);
    expect(error).toBeInstanceOf(APIError);
    expect(error).toMatchObject({
      status: 502,
      code: "unavailable",
      message: "请求失败（502）",
    });
  });

  it("成功响应不是 JSON 时报告格式错误", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("not json", { status: 200 })),
    );
    await expect(request("/me")).rejects.toBeInstanceOf(ResponseError);
  });

  it("校验会话、分页和运行配置的顶层结构", async () => {
    const user = { id: "u1", username: "alice", role: "admin" };
    expect(decodeSession({ user, csrf_token: "c" }).user.id).toBe("u1");
    for (const bad of [
      null,
      { user, csrf_token: 1 },
      { user: { ...user, role: "root" }, csrf_token: "c" },
      { csrf_token: "c" },
    ])
      expect(() => decodeSession(bad)).toThrow(ResponseError);

    expect(decodePage({ items: [1], next_cursor: "n" }).items).toEqual([1]);
    expect(decodePage({ items: [] }).next_cursor).toBeUndefined();
    for (const bad of [
      { items: {} },
      { items: [], next_cursor: 5 },
      [],
      "items",
    ])
      expect(() => decodePage(bad)).toThrow(ResponseError);

    const config = {
      version: 1,
      query_log: true,
      telemetry: {},
      plugins: [{ tag: "cache", type: "cache" }],
    };
    expect(decodeRuntimeState({ revision: "r", config }).revision).toBe("r");
    expect(
      decodeRuntimeState({
        revision: "r",
        config,
        sources: { running: { config } },
      }).revision,
    ).toBe("r");
    for (const bad of [
      { config },
      { revision: "r" },
      { revision: "r", config: { ...config, plugins: {} } },
      { revision: "r", config: { ...config, plugins: [null] } },
      { revision: "r", config, sources: { running: { config: {} } } },
    ])
      expect(() => decodeRuntimeState(bad)).toThrow(ResponseError);

    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(reply({ user: null, csrf_token: "c" })),
    );
    await expect(request("/session", {}, decodeSession)).rejects.toBeInstanceOf(
      ResponseError,
    );
  });

  it("allPages 按游标取完全部页面", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(reply({ items: [1, 2], next_cursor: "b" }))
      .mockResolvedValueOnce(reply({ items: [3] }));
    vi.stubGlobal("fetch", fetcher);
    await expect(allPages<number>("/me/credentials")).resolves.toEqual([
      1, 2, 3,
    ]);
    expect(String(fetcher.mock.calls[1][0])).toContain("cursor=b");
  });

  it("allPages 遇到重复游标时停止并报错", async () => {
    let page = 0;
    const fetcher = vi
      .fn()
      .mockImplementation(() =>
        Promise.resolve(
          reply({ items: [page], next_cursor: page++ % 2 ? "a" : "b" }),
        ),
      );
    vi.stubGlobal("fetch", fetcher);
    await expect(allPages("/admin/usage")).rejects.toThrow(
      "服务返回了重复的分页游标，已停止加载。",
    );
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it("allPages 超过页数上限时停止并报错", async () => {
    let page = 0;
    const fetcher = vi
      .fn()
      .mockImplementation(() =>
        Promise.resolve(reply({ items: [], next_cursor: `c${page++}` })),
      );
    vi.stubGlobal("fetch", fetcher);
    await expect(allPages("/admin/usage")).rejects.toThrow(
      `分页数量超过上限（${maxPages} 页），已停止加载。`,
    );
    expect(fetcher).toHaveBeenCalledTimes(maxPages);
  });
});
