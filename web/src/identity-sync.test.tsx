import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { APIError, message } from "./api";
import { syncError } from "./health-panel";
import { SessionProvider } from "./session";
import type { IdentitySync } from "./types";

const account = (id: string, username: string, role = "user") => ({
  id,
  username,
  role,
  enabled: true,
  expires_at: "0001-01-01T00:00:00Z",
  period: "monthly",
  timezone: "Asia/Shanghai",
  limit: 100,
  qps: 5,
  burst: 10,
  max_credentials: 2,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
});
const admin = account("a1", "root", "admin");
const alice = account("u1", "alice");
const quota = {
  period: "monthly",
  timezone: "Asia/Shanghai",
  period_start: "2026-11-01T00:00:00Z",
  period_end: "2026-12-01T00:00:00Z",
  limit: 100,
  used: 1,
  remaining: 99,
};
const stats = {
  from: "2026-11-14T00:00:00Z",
  to: "2026-11-15T00:00:00Z",
  completed: 0,
  failed: 0,
  cache_hits: 0,
  avg_latency_ms: 0,
  p95_latency_ms: 0,
  rcode_counts: {},
  series: [],
  upstreams: [],
  dropped: 0,
  updated_at: "2026-11-15T00:00:00Z",
  query_log_enabled: false,
};
const credential = {
  id: "c1",
  user_id: "u1",
  name: "phone",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  expires_at: "0001-01-01T00:00:00Z",
  revoked_at: "0001-01-01T00:00:00Z",
};
const response = (body: unknown, status = 200) =>
  new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const follower: IdentitySync = {
  mode: "follower",
  master_panel_url: "https://gz.example",
};

type Call = { path: string; method: string; body?: unknown };

function mockApi(sync: IdentitySync | undefined, user = admin) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname;
      const method = init?.method ?? "GET";
      calls.push({
        path,
        method,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      });
      if (path.endsWith("/session"))
        return response({
          user,
          csrf_token: "c",
          expires_at: "2027-01-01T00:00:00Z",
          ...(sync ? { identity_sync: sync } : {}),
        });
      if (/\/(credentials)$/.test(path))
        return response({ items: [credential], next_cursor: "" });
      if (path.endsWith("/admin/users/u1")) {
        if (method === "PATCH")
          return response({
            ...alice,
            ...(init?.body ? JSON.parse(String(init.body)) : {}),
          });
        return response({ user: alice, quota });
      }
      if (path.endsWith("/me"))
        return response({
          user,
          quota,
          public_dns_url: "https://cd.example/dns-query",
        });
      if (path.endsWith("/admin/users"))
        return response({ items: [admin, alice], next_cursor: "" });
      if (path.endsWith("/stats")) return response(stats);
      return response({ items: [], next_cursor: "" });
    }),
  );
  return calls;
}

function mount(path: string) {
  const router = createMemoryRouter(
    [
      {
        path: "*",
        element: (
          <SessionProvider>
            <App />
          </SessionProvider>
        ),
      },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
}

const notice = /本节点的账户由主控统一管理/;

beforeEach(() => vi.restoreAllMocks());
afterEach(() => vi.unstubAllGlobals());

describe("从节点面板", () => {
  it("用户列表不能开设账户，并指向主控面板", async () => {
    mockApi(follower);
    mount("/admin/users");
    expect(await screen.findByText("alice")).toBeInTheDocument();
    expect(screen.getByText(notice)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "前往主控面板" })).toHaveAttribute(
      "href",
      "https://gz.example",
    );
    expect(
      screen.queryByRole("button", { name: /开设账户/ }),
    ).not.toBeInTheDocument();
  });

  it("用户详情只读：没有编辑、重置、删除与凭证操作", async () => {
    mockApi(follower);
    mount("/admin/users/u1");
    expect(await screen.findByText("phone")).toBeInTheDocument();
    for (const name of ["编辑账户", "重置密码", "删除用户", "创建凭证"])
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    const table = screen.getByRole("region", { name: "凭证列表" });
    expect(within(table).queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText("本节点剩余额度")).toBeInTheDocument();
  });

  it("管理员修改密码页改为提示", async () => {
    mockApi(follower);
    mount("/admin/password");
    expect(await screen.findByText(notice)).toBeInTheDocument();
    expect(screen.queryByLabelText("当前密码")).not.toBeInTheDocument();
  });

  it("普通用户的账户中心不能改密码或签发凭证", async () => {
    mockApi(follower, alice);
    mount("/app/account");
    expect(await screen.findByText("phone")).toBeInTheDocument();
    expect(screen.getByText(notice)).toBeInTheDocument();
    expect(screen.getByText(/请到主控面板录入地址/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /录入/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByLabelText("当前密码")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "创建凭证" }),
    ).not.toBeInTheDocument();
  });

  it("不安全的主控地址不显示为链接", async () => {
    mockApi({ mode: "follower", master_panel_url: "javascript:alert(1)" });
    mount("/admin/users");
    expect(await screen.findByText(notice)).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "前往主控面板" }),
    ).not.toBeInTheDocument();
  });
});

describe("主控面板", () => {
  it("编辑账户时周期和时区锁定，保存不提交它们", async () => {
    const calls = mockApi({ mode: "master" });
    mount("/admin/users/u1");
    fireEvent.click(await screen.findByRole("button", { name: "编辑账户" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByLabelText(/周期/)).toBeDisabled();
    expect(within(dialog).getByLabelText(/时区/)).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText(/请求额度/), {
      target: { value: "200" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      limit: 200,
    });
  });

  it("单机模式不锁定周期", async () => {
    mockApi(undefined);
    mount("/admin/users/u1");
    fireEvent.click(await screen.findByRole("button", { name: "编辑账户" }));
    expect(
      within(screen.getByRole("dialog")).getByLabelText(/周期/),
    ).toBeEnabled();
    expect(screen.getByText("剩余额度")).toBeInTheDocument();
  });
});

describe("凭证的完整地址", () => {
  const urls = [
    { name: "广州", url: "https://gz.example/dns-query/tok" },
    { name: "成都", url: "https://cd.example/dns-query/tok" },
  ];
  function mockCredentials(stored: boolean, restoreStatus = 204) {
    const calls: Call[] = [];
    let restored = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const path = new URL(String(input), "http://localhost").pathname;
        const method = init?.method ?? "GET";
        calls.push({
          path,
          method,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        });
        if (path.endsWith("/session"))
          return response({
            user: alice,
            csrf_token: "c",
            expires_at: "2027-01-01T00:00:00Z",
            identity_sync: { mode: "master" },
          });
        if (path.endsWith("/me/credentials/c1/restore-token")) {
          if (restoreStatus !== 204)
            return response(
              { error: { code: "token_mismatch", message: "x" } },
              restoreStatus,
            );
          restored = true;
          return response({
            credential,
            token: "tok",
            doh_url: urls[0].url,
            doh_urls: urls,
          });
        }
        if (path.endsWith("/me/credentials") && method === "POST")
          return response(
            {
              credential: { ...credential, id: "c2", name: "laptop" },
              token: "tok",
              doh_url: urls[0].url,
              doh_urls: urls,
            },
            201,
          );
        if (path.endsWith("/me/credentials"))
          return response({
            items: [
              stored || restored
                ? {
                    ...credential,
                    token_available: true,
                    doh_url: urls[0].url,
                    doh_urls: urls,
                  }
                : { ...credential, token_available: false },
            ],
            next_cursor: "",
          });
        if (path.endsWith("/me"))
          return response({
            user: alice,
            quota,
            public_dns_url: "https://gz.example/dns-query",
          });
        if (path.endsWith("/stats")) return response(stats);
        return response({ items: [], next_cursor: "" });
      }),
    );
    return calls;
  }

  it("账户中心为每条凭证列出各节点的完整地址", async () => {
    mockCredentials(true);
    mount("/app/account");
    expect(
      await screen.findByText("https://cd.example/dns-query/tok"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("https://gz.example/dns-query/tok"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "复制 成都 的地址" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("https://gz.example/dns-query")).toBeNull();
  });

  it("主页直接给出各凭证的地址", async () => {
    mockCredentials(true);
    mount("/app");
    expect(
      await screen.findByText("https://cd.example/dns-query/tok"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/公共/)).toBeNull();
  });

  it("签发凭证时给出每个节点的完整地址", async () => {
    mockCredentials(true);
    mount("/app/account");
    fireEvent.change(await screen.findByLabelText("名称"), {
      target: { value: "laptop" },
    });
    fireEvent.click(screen.getByRole("button", { name: "创建凭证" }));
    const dialog = await screen.findByRole("dialog", { name: "凭证已签发" });
    expect(
      within(dialog).getByText("https://cd.example/dns-query/tok"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/其他节点在下次同步后/),
    ).toBeInTheDocument();
  });

  it("旧凭证可粘贴设备上的地址补全", async () => {
    const calls = mockCredentials(false);
    mount("/app/account");
    fireEvent.click(
      await screen.findByRole("button", { name: "录入 phone 的地址" }),
    );
    const dialog = screen.getByRole("dialog", { name: "录入完整地址" });
    fireEvent.change(within(dialog).getByLabelText("专属地址或令牌"), {
      target: { value: "  https://gz.example/dns-query/tok  " },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    expect(
      await screen.findByText("https://cd.example/dns-query/tok"),
    ).toBeInTheDocument();
    expect(calls.find((c) => c.path.endsWith("/restore-token"))?.body).toEqual({
      token: "https://gz.example/dns-query/tok",
    });
  });

  it("补全时地址不匹配会说明原因", async () => {
    mockCredentials(false, 400);
    mount("/app/account");
    fireEvent.click(
      await screen.findByRole("button", { name: "录入 phone 的地址" }),
    );
    const dialog = screen.getByRole("dialog", { name: "录入完整地址" });
    fireEvent.change(within(dialog).getByLabelText("专属地址或令牌"), {
      target: { value: "wrong" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    expect(
      await within(dialog).findByText(/与该凭证不一致/),
    ).toBeInTheDocument();
  });
});

describe("同步错误文案", () => {
  it("把错误码译成说明，不展示原始错误", () => {
    expect(syncError("http_401")).toMatch(/同步令牌/);
    expect(syncError("http_502")).toMatch(/HTTP 502/);
    expect(syncError("fetch_failed")).toMatch(/无法连接主控/);
    expect(syncError("period_mismatch")).toMatch(/保留原周期和已用额度/);
    expect(syncError("something_new")).toMatch(/something_new/);
    expect(
      message(new APIError(409, "identity_managed_by_master", "x")),
    ).toMatch(/主控面板/);
    expect(message(new APIError(409, "identity_period_locked", "x"))).toMatch(
      /不能修改/,
    );
  });
});
