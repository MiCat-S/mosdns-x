import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { SessionProvider } from "./session";

const account = (id: string, username: string, role = "user") => ({
  id,
  username,
  role,
  enabled: true,
  expires_at: "0001-01-01T00:00:00Z",
  period: "monthly",
  timezone: "UTC",
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
  timezone: "UTC",
  period_start: "2026-09-01T00:00:00Z",
  period_end: "2026-10-01T00:00:00Z",
  limit: 100,
  used: 1,
  remaining: 99,
};
const stats = {
  from: "2026-09-24T00:00:00Z",
  to: "2026-09-25T00:00:00Z",
  completed: 0,
  failed: 0,
  cache_hits: 0,
  avg_latency_ms: 0,
  p95_latency_ms: 0,
  rcode_counts: {},
  series: [],
  upstreams: [],
  dropped: 0,
  updated_at: "2026-09-25T00:00:00Z",
  query_log_enabled: false,
};
const response = (body: unknown, status = 200) =>
  new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

type Call = { url: string; method: string };

function mockApi(deleteStatus = 204) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      calls.push({ url, method });
      const path = new URL(url, "http://localhost").pathname;
      if (path.endsWith("/session"))
        return response({
          user: admin,
          csrf_token: "c",
          expires_at: "2027-01-01T00:00:00Z",
        });
      if (/\/admin\/users\/(u1|a1)$/.test(path)) {
        if (method === "DELETE")
          return deleteStatus === 204
            ? response(null, 204)
            : response(
                { error: { code: "conflict", message: "conflict" } },
                deleteStatus,
              );
        return response({
          user: path.endsWith("a1") ? admin : alice,
          quota,
        });
      }
      if (path.endsWith("/admin/users"))
        return response({ items: [admin], next_cursor: "" });
      if (path.endsWith("/admin/stats")) return response(stats);
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
  return router;
}

beforeEach(() => vi.restoreAllMocks());
afterEach(() => vi.unstubAllGlobals());

describe("删除用户", () => {
  it("确认后删除并返回用户列表", async () => {
    const calls = mockApi();
    const router = mount("/admin/users/u1");
    fireEvent.click(await screen.findByRole("button", { name: "删除用户" }));
    expect(screen.getByText(/此操作无法撤销/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "永久删除" }));
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/admin/users"),
    );
    expect(
      calls.filter((c) => c.method === "DELETE").map((c) => c.url),
    ).toEqual(["/api/v1/admin/users/u1"]);
  });

  it("取消不会发出删除请求", async () => {
    const calls = mockApi();
    mount("/admin/users/u1");
    fireEvent.click(await screen.findByRole("button", { name: "删除用户" }));
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByText(/此操作无法撤销/)).not.toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  });

  it("冲突时说明原因并留在详情页", async () => {
    mockApi(409);
    const router = mount("/admin/users/u1");
    fireEvent.click(await screen.findByRole("button", { name: "删除用户" }));
    fireEvent.click(screen.getByRole("button", { name: "永久删除" }));
    expect(
      await screen.findByText("不能删除自己，也不能删除最后一个启用的管理员。"),
    ).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/users/u1");
  });

  it("不能删除当前登录的自己", async () => {
    mockApi();
    mount("/admin/users/a1");
    expect(
      await screen.findByRole("button", { name: "编辑账户" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "删除用户" }),
    ).not.toBeInTheDocument();
  });
});
