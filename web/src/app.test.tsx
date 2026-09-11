import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
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
    expect(screen.getByText("192.0.2.1, 2001:db8::1")).toBeInTheDocument();
    expect(screen.getByText("未记录客户端 IP")).toBeInTheDocument();
    expect(screen.getByText("无地址记录")).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "查看 example.org. 详情" }),
    );
    const first = screen.getByRole("dialog");
    expect(within(first).getByText("192.0.2.1")).toBeInTheDocument();
    expect(within(first).getByText("2001:db8::1")).toBeInTheDocument();
    expect(
      within(first).getByText("v0 · UDP 1232 bytes · DNSSEC OK"),
    ).toBeInTheDocument();
    expect(within(first).getByText("8 (ECS), 10 (COOKIE)")).toBeInTheDocument();
    expect(
      within(first).getByText("192.0.2.0/24 · family 1 · scope 0"),
    ).toBeInTheDocument();
    fireEvent.click(within(first).getByRole("button", { name: "关闭" }));

    fireEvent.click(
      screen.getByRole("button", { name: "查看 empty.example. 详情" }),
    );
    const legacy = screen.getByRole("dialog");
    expect(within(legacy).getByText("未携带")).toBeInTheDocument();
    expect(within(legacy).getByText("无地址记录")).toBeInTheDocument();
  });
  it("查询日志将筛选条件发送到后端", async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ items: [] }));
    vi.stubGlobal("fetch", fetcher);
    render(<QueryDetails path="/me/queries" enabled />);
    await screen.findByText("当前条件下暂无查询记录");

    fireEvent.change(screen.getByLabelText("域名"), {
      target: { value: "example.com" },
    });
    fireEvent.change(screen.getByLabelText("查询类型"), {
      target: { value: "AAAA" },
    });
    fireEvent.change(screen.getByLabelText("响应码"), {
      target: { value: "NXDOMAIN" },
    });
    fireEvent.change(screen.getByLabelText("协议"), {
      target: { value: "h3" },
    });
    fireEvent.change(screen.getByLabelText("客户端或 Answer IP"), {
      target: { value: "192.0.2.1" },
    });
    fireEvent.change(screen.getByLabelText("缓存"), {
      target: { value: "miss" },
    });
    fireEvent.click(screen.getByRole("button", { name: "查询" }));

    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    const url = new URL(
      String(fetcher.mock.calls[1][0]),
      "https://example.test",
    );
    expect(url.searchParams.get("name")).toBe("example.com");
    expect(url.searchParams.get("qtype")).toBe("AAAA");
    expect(url.searchParams.get("rcode")).toBe("NXDOMAIN");
    expect(url.searchParams.get("protocol")).toBe("h3");
    expect(url.searchParams.get("address")).toBe("192.0.2.1");
    expect(url.searchParams.get("cache")).toBe("miss");
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
  it("本地时间往返保持同一时刻", () => {
    const value = "2026-07-04T16:30:00.000Z";
    const local = toLocalDateTime(value);
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
    expect(fromLocalDateTime(local)).toBe(value);
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
