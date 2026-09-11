import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import {
  credentialStatus,
  credentialCanRevoke,
  fromLocalDateTime,
  QueryDetails,
  successRate,
  toLocalDateTime,
  usageSummary,
} from "./pages";
import { SessionProvider } from "./session";
const user = {
  id: "u1",
  username: "alice",
  role: "user",
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
};
const response = (body: unknown, status = 200) =>
  new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
function mount(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <SessionProvider>
        <App />
      </SessionProvider>
    </MemoryRouter>,
  );
}
beforeEach(() => vi.restoreAllMocks());
describe("前端访问与秘密处理", () => {
  it("查询明细显示客户端、Answer IP、EDNS 和 ECS，并兼容空字段", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        response({
          items: [
            {
              id: "q1",
              time: "2026-09-11T12:00:00Z",
              user_id: "u1",
              credential_id: "c1",
              client_ip: "2001:db8::44",
              name: "example.org.",
              qtype: "AAAA",
              rcode: "NOERROR",
              duration_ms: 1.25,
              cache_hit: false,
              protocol: "h3",
              answer_ips: ["192.0.2.1", "2001:db8::1"],
              edns: {
                present: true,
                version: 0,
                udp_size: 1232,
                dnssec_ok: true,
                option_codes: [8, 10],
                ecs: {
                  address: "192.0.2.0",
                  family: 1,
                  source_prefix: 24,
                  scope_prefix: 0,
                },
              },
            },
            {
              id: "q2",
              time: "2026-09-11T12:00:01Z",
              user_id: "u1",
              credential_id: "old",
              client_ip: "",
              name: "empty.example.",
              qtype: "A",
              rcode: "SERVFAIL",
              duration_ms: 2,
              cache_hit: false,
              protocol: "https",
              answer_ips: [],
            },
          ],
        }),
      ),
    );
    render(<QueryDetails path="/me/queries" enabled />);
    expect(await screen.findByText("2001:db8::44")).toBeInTheDocument();
    expect(
      screen.getByText(/Answer: 192\.0\.2\.1, 2001:db8::1/),
    ).toBeInTheDocument();
    expect(screen.getByText("EDNS v0 · UDP 1232 · DO")).toBeInTheDocument();
    expect(screen.getByText("选项: 8, 10")).toBeInTheDocument();
    expect(screen.getByText(/ECS: 192\.0\.2\.0\/24/)).toBeInTheDocument();
    expect(screen.getByText("未知客户端")).toBeInTheDocument();
    expect(screen.getByText("无 Answer IP")).toBeInTheDocument();
    expect(screen.getByText("无 EDNS")).toBeInTheDocument();
  });
  it("初始会话服务失败时显示错误和重试入口", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          response(
            { error: { code: "unavailable", message: "暂时无法连接" } },
            503,
          ),
        ),
    );
    mount("/login");
    expect(await screen.findByRole("alert")).toHaveTextContent("暂时无法连接");
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });
  it("退出网络失败时保留当前会话并展示错误", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (url.endsWith("/session") && init?.method === "DELETE")
          return Promise.reject(new Error("网络中断"));
        if (url.endsWith("/session"))
          return Promise.resolve(
            response({
              user,
              csrf_token: "c",
              expires_at: "2027-01-01T00:00:00Z",
            }),
          );
        return Promise.resolve(
          response(
            { error: { code: "unavailable", message: "暂不可用" } },
            503,
          ),
        );
      });
    vi.stubGlobal("fetch", fetcher);
    mount("/app");
    const exit = await screen.findByRole("button", { name: "退出登录" });
    fireEvent.click(exit);
    expect(await screen.findByText("网络中断")).toBeInTheDocument();
    expect(screen.getByText("alice")).toBeInTheDocument();
  });
  it("非 UTC 时区的本地时间往返保持同一时刻", () => {
    const value = "2026-07-04T16:30:00.000Z";
    expect(toLocalDateTime(value)).toBe("2026-07-05T00:30");
    expect(fromLocalDateTime(toLocalDateTime(value))).toBe(value);
  });
  it("已撤销和已到期凭证都不是有效凭证", () => {
    const base = {
      id: "c",
      user_id: "u1",
      name: "设备",
      expires_at: "0001-01-01T00:00:00Z",
      revoked_at: "0001-01-01T00:00:00Z",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    expect(credentialStatus(base)).toBe("有效");
    expect(
      credentialStatus({ ...base, expires_at: "2020-01-01T00:00:00Z" }),
    ).toBe("已到期");
    expect(
      credentialStatus({ ...base, revoked_at: "2026-01-02T00:00:00Z" }),
    ).toBe("已撤销");
    expect(
      credentialCanRevoke({ ...base, expires_at: "2020-01-01T00:00:00Z" }),
    ).toBe(true);
    expect(
      credentialCanRevoke({ ...base, revoked_at: "2026-01-02T00:00:00Z" }),
    ).toBe(false);
  });
  it("处理成功率将 failed 视为 completed 的子集，并聚合持久用量", () => {
    expect(successRate({ completed: 100, failed: 8 })).toBe(0.92);
    const summary = usageSummary([
      { minute: "2026-01-01T00:00:00Z", user_id: "u", count: 3 },
      {
        minute: "2026-01-01T00:00:00Z",
        user_id: "u",
        credential_id: "c",
        count: 4,
      },
    ]);
    expect(summary.total).toBe(7);
    expect(summary.series[0].completed).toBe(7);
  });
  it("最近完整分钟没有流量时平均值为零", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-11T08:20:30Z"));
    const summary = usageSummary([
      { minute: "2026-09-10T22:20:00Z", user_id: "u", count: 600 },
    ]);
    expect(summary.recentPerSecond).toBe(0);
    vi.useRealTimers();
  });
  it("用户会话不能进入管理员路由", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        response({
          user,
          csrf_token: "c",
          expires_at: "2027-01-01T00:00:00Z",
        }),
      ),
    );
    mount("/admin/users");
    expect(
      await screen.findByRole("heading", { name: "我的服务" }),
    ).toBeInTheDocument();
  });
  it("API 错误呈现给用户", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) =>
        url.endsWith("/session")
          ? Promise.resolve(
              response({
                user,
                csrf_token: "c",
                expires_at: "2027-01-01T00:00:00Z",
              }),
            )
          : Promise.resolve(
              response(
                { error: { code: "unavailable", message: "服务暂时不可用" } },
                503,
              ),
            ),
      ),
    );
    mount("/app/credentials");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "服务暂时不可用",
    );
  });
  it("凭证令牌只在签发弹窗中展示，关闭即销毁", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (url.endsWith("/session"))
          return Promise.resolve(
            response({
              user,
              csrf_token: "c",
              expires_at: "2027-01-01T00:00:00Z",
            }),
          );
        if (url.endsWith("/me"))
          return Promise.resolve(
            response({
              user,
              quota: {
                period: "monthly",
                timezone: "UTC",
                period_start: "2026-09-01T00:00:00Z",
                period_end: "2026-10-01T00:00:00Z",
                limit: 100,
                used: 0,
                remaining: 100,
              },
            }),
          );
        if (url.includes("/credentials") && init?.method === "POST")
          return Promise.resolve(
            response(
              {
                credential: {
                  id: "k",
                  user_id: "u1",
                  name: "手机",
                  expires_at: "0001-01-01T00:00:00Z",
                  revoked_at: "0001-01-01T00:00:00Z",
                  created_at: "2026-01-01T00:00:00Z",
                  updated_at: "2026-01-01T00:00:00Z",
                },
                token: "one-time-secret",
                doh_url: "https://dns.test/dns-query/k",
              },
              201,
            ),
          );
        if (url.includes("/credentials"))
          return Promise.resolve(response({ items: [] }));
        return Promise.resolve(response({}));
      });
    vi.stubGlobal("fetch", fetcher);
    mount("/app/credentials");
    const name = await screen.findByLabelText("名称");
    fireEvent.change(name, { target: { value: "手机" } });
    fireEvent.click(screen.getByRole("button", { name: "创建凭证" }));
    expect(await screen.findByText("one-time-secret")).toBeInTheDocument();
    expect(name).toHaveValue("");
    expect(
      fetcher.mock.calls.filter(([url]) => String(url).includes("/credentials"))
        .length,
    ).toBeGreaterThanOrEqual(2);
    fireEvent.click(screen.getByRole("button", { name: "我已保存，关闭" }));
    await waitFor(() =>
      expect(screen.queryByText("one-time-secret")).not.toBeInTheDocument(),
    );
  });
});
