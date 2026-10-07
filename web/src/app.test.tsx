import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
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
  return render(<RouterProvider router={router} />);
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
              edns_trace_version: 1,
              upstream_stage_status: "selected",
              response_source: "upstream",
              response_source_id: "forward_remote",
              upstream_id: "forward_remote/0",
              upstream_label: "forward_remote #1 (DoH)",
              trace: {
                steps: [
                  {
                    at_ms: 0.1,
                    kind: "condition",
                    detail: "query_is_cn_domain",
                    misses: ["query_is_cn_domain"],
                    matched: false,
                    then: "continue",
                  },
                  {
                    at_ms: 0.2,
                    kind: "condition",
                    detail: "(query_is_gfw_domain) || [qtype65]",
                    hits: ["query_is_gfw_domain"],
                    matched: true,
                    then: "exec",
                  },
                  {
                    at_ms: 2500.4,
                    kind: "secondary_started",
                    detail: "fast_fallback",
                  },
                  {
                    at_ms: 2540.1,
                    kind: "branch_selected",
                    detail: "secondary",
                  },
                ],
                attempts: [
                  {
                    seq: 1,
                    branch: "primary",
                    plugin: "forward_easymosdns",
                    upstream: "forward_easymosdns #1 (DoH)",
                    start_ms: 0.3,
                    duration_ms: 2540,
                    done: false,
                  },
                  {
                    seq: 2,
                    branch: "secondary",
                    plugin: "forward_remote",
                    upstream: "forward_remote #1 (DoH)",
                    start_ms: 2500.5,
                    duration_ms: 39.6,
                    done: true,
                    rcode: "NOERROR",
                    selected: true,
                  },
                ],
              },
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
              upstream_request_edns: {
                present: true,
                version: 0,
                udp_size: 1232,
                dnssec_ok: true,
                option_codes: [8],
                ecs: {
                  address: "198.51.100.0",
                  family: 1,
                  source_prefix: 24,
                  scope_prefix: 0,
                },
              },
              upstream_response_edns: {
                present: true,
                version: 0,
                udp_size: 1232,
                dnssec_ok: false,
                option_codes: [8],
                ecs: {
                  address: "198.51.100.0",
                  family: 1,
                  source_prefix: 24,
                  scope_prefix: 0,
                },
              },
              response_edns: {
                present: false,
                version: 0,
                udp_size: 0,
                dnssec_ok: false,
                option_codes: [],
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
    expect(within(first).getByText("四阶段 v1")).toBeInTheDocument();
    expect(within(first).getAllByText("EDNS v0")).toHaveLength(3);
    expect(
      within(first).getAllByText("已设置（请求 DNSSEC 数据）"),
    ).toHaveLength(2);
    expect(within(first).getByText("8 (ECS), 10 (COOKIE)")).toBeInTheDocument();
    expect(
      within(first).getByText("192.0.2.0/24 · IPv4 · Scope Prefix 0"),
    ).toBeInTheDocument();
    expect(
      within(first).getByText("上游请求中的 ECS 与客户端请求不同"),
    ).toBeInTheDocument();
    expect(
      within(first).getByText("上游响应携带 ECS，客户端逻辑响应中已不存在"),
    ).toBeInTheDocument();
    expect(
      within(first).getByText("已观察报文：没有 EDNS"),
    ).toBeInTheDocument();
    expect(within(first).getByText("上游")).toBeInTheDocument();
    expect(
      within(first).getAllByText("forward_remote #1 (DoH)").length,
    ).toBeGreaterThanOrEqual(3);
    expect(
      within(first).getByText(
        "命中 query_is_gfw_domain → forward_remote #1 (DoH)，40 ms 取得结果（主要上游未及时回应，改用备援）",
      ),
    ).toBeInTheDocument();
    const flow = within(first).getByRole("list", { name: "查询路径" });
    const nodes = within(flow).getAllByRole("listitem");
    // Received, two conditions, primary attempt, fallback, secondary
    // attempt, branch chosen, returned.
    expect(nodes).toHaveLength(8);
    expect(nodes[1]).toHaveTextContent("query_is_cn_domain否 · 继续往下");
    expect(
      within(nodes[1]).getByLabelText("query_is_cn_domain：未命中"),
    ).toBeInTheDocument();
    expect(
      within(nodes[2]).getByLabelText("query_is_gfw_domain：命中"),
    ).toBeInTheDocument();
    expect(
      within(nodes[2]).getByLabelText(
        "qtype65：未检查（前面的条件已决定结果）",
      ),
    ).toBeInTheDocument();
    expect(nodes[2]).toHaveTextContent("是 · 进入此分支");
    expect(nodes[3]).toHaveTextContent("forward_easymosdns #1 (DoH)");
    expect(nodes[4]).toHaveTextContent("主要上游未及时回应，同时启用备援");
    expect(nodes[5]).toHaveTextContent("40 ms · NOERROR · 采用");
    expect(nodes[6]).toHaveTextContent("采用备援的结果");
    expect(nodes[7]).toHaveTextContent("返回NOERROR");
    expect(nodes[7]).toHaveTextContent("共 1.3 ms");
    expect(
      within(first).getByRole("figure", { name: "上游请求时间轴" }),
    ).toBeInTheDocument();
    expect(nodes[3]).toHaveTextContent("未完成（已先返回其他结果）");
    expect(
      within(first).getAllByText("forward_easymosdns #1 (DoH)").length,
    ).toBeGreaterThanOrEqual(1);
    expect(
      within(first).queryByText(/forward_remote\/0/),
    ).not.toBeInTheDocument();
    expect(within(first).queryByText("命中规则")).not.toBeInTheDocument();
    expect(within(first).queryByText("命中公共列表")).not.toBeInTheDocument();
    fireEvent.click(within(first).getByRole("button", { name: "关闭" }));

    fireEvent.click(
      screen.getByRole("button", { name: "查看 empty.example. 详情" }),
    );
    const legacy = screen.getByRole("dialog");
    expect(within(legacy).getByText("历史记录")).toBeInTheDocument();
    expect(
      within(legacy).getByText("历史记录未采集客户端请求快照"),
    ).toBeInTheDocument();
    expect(
      within(legacy).getAllByText("历史记录未采集该上游阶段"),
    ).toHaveLength(2);
    expect(
      within(legacy).getByText("历史记录未采集客户端响应快照"),
    ).toBeInTheDocument();
    expect(within(legacy).getByText("无地址记录")).toBeInTheDocument();
    expect(
      within(legacy).getByText("历史记录未采集查询路径。"),
    ).toBeInTheDocument();
  });
  it("查询明细不会把缓存命中的空上游快照解释为未调用上游", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        response({
          items: [
            {
              id: "cached-query",
              time: "2026-09-11T12:00:00Z",
              user_id: "u1",
              credential_id: "c1",
              client_ip: "192.0.2.44",
              name: "cached.example.",
              qtype: "A",
              rcode: "NOERROR",
              duration_ms: 0.8,
              cache_hit: true,
              protocol: "h2",
              answer_ips: ["192.0.2.1"],
              response_source: "cache",
              response_source_id: "cache_wan",
              upstream_label: "223.5.5.5 (UDP)",
              trace: {
                steps: [{ at_ms: 0.1, kind: "cache_hit", detail: "cache_wan" }],
                attempts: [],
              },
              edns_trace_version: 1,
              upstream_stage_status: "not_linked",
              edns: {
                present: false,
                version: 0,
                udp_size: 0,
                dnssec_ok: false,
                option_codes: [],
              },
              upstream_request_edns: null,
              upstream_response_edns: null,
              response_edns: {
                present: false,
                version: 0,
                udp_size: 0,
                dnssec_ok: false,
                option_codes: [],
              },
            },
          ],
        }),
      ),
    );
    render(<QueryDetails path="/me/queries" enabled />);
    fireEvent.click(
      await screen.findByRole("button", {
        name: "查看 cached.example. 详情",
      }),
    );
    const detail = screen.getByRole("dialog");
    expect(
      within(detail).getAllByText("本次响应来自缓存，无对应上游快照"),
    ).toHaveLength(2);
    expect(within(detail).getAllByText("已观察报文：没有 EDNS")).toHaveLength(
      2,
    );
    expect(
      within(detail).getAllByText("缓存 · 原始 223.5.5.5 (UDP)").length,
    ).toBeGreaterThanOrEqual(1);
    expect(
      within(detail).getByText(
        "直接命中缓存，原始来源 223.5.5.5 (UDP)，0.8 ms 返回",
      ),
    ).toBeInTheDocument();
    expect(within(detail).getByText("命中缓存")).toBeInTheDocument();
    expect(
      within(detail).getByText("原始来源 223.5.5.5 (UDP)"),
    ).toBeInTheDocument();
    expect(
      within(detail).queryByRole("figure", { name: "上游请求时间轴" }),
    ).not.toBeInTheDocument();
    expect(within(detail).queryByText("未调用上游")).not.toBeInTheDocument();
  });
  it("管理端日志以名称显示账户和设备，账户删除后显示 ID", async () => {
    const base = {
      time: "2026-10-07T12:00:00Z",
      client_ip: "192.0.2.44",
      qtype: "A",
      rcode: "NOERROR",
      duration_ms: 1,
      cache_hit: false,
      protocol: "h2",
      answer_ips: [],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        response({
          items: [
            {
              ...base,
              id: "named",
              name: "named.example.",
              user_id: "4Hz38ohEGyng9jxKEwQUbA",
              credential_id: "c1",
              username: "66",
              device_name: "Mac",
            },
            {
              ...base,
              id: "gone",
              name: "gone.example.",
              user_id: "deletedUserId123",
              credential_id: "c2",
            },
          ],
        }),
      ),
    );
    render(<QueryDetails path="/admin/queries" enabled showPrincipal />);
    expect(await screen.findByText("用户 66")).toBeInTheDocument();
    expect(screen.getByText("用户 deletedUserId123")).toBeInTheDocument();
    expect(
      screen.queryByText("用户 4Hz38ohEGyng9jxKEwQUbA"),
    ).not.toBeInTheDocument();
    const rows = screen.getAllByRole("row");
    expect(
      within(rows[1])
        .getAllByRole("cell")
        .map((cell) => cell.dataset.label),
    ).toEqual([
      "查询",
      "结果",
      "出站 DNS",
      "设备 / 客户端",
      "Answer IP",
      "协议 / 耗时",
      "时间",
    ]);
    expect(rows[1]).toHaveTextContent("Mac");
    expect(rows[2]).toHaveTextContent("未知设备");
    fireEvent.click(
      screen.getByRole("button", { name: "查看 named.example. 详情" }),
    );
    const detail = screen.getByRole("dialog");
    expect(
      within(detail).getByText("4Hz38ohEGyng9jxKEwQUbA"),
    ).toBeInTheDocument();
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
    fireEvent.change(screen.getByLabelText("处理来源"), {
      target: { value: "upstream" },
    });
    fireEvent.change(screen.getByLabelText("出站 DNS"), {
      target: { value: "223.5.5.5 (UDP)" },
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
    expect(url.searchParams.get("source")).toBe("upstream");
    expect(url.searchParams.get("upstream")).toBe("223.5.5.5 (UDP)");
    expect(url.searchParams.get("upstream_id")).toBeNull();
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
  it("管理员布局保留退出登录入口", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) =>
        url.endsWith("/session")
          ? Promise.resolve(
              response({
                user: { ...user, role: "admin" },
                csrf_token: "c",
                expires_at: "2027-01-01T00:00:00Z",
              }),
            )
          : Promise.resolve(response({ items: [] })),
      ),
    );
    mount("/admin/users");
    expect(
      await screen.findByRole("button", { name: "退出登录" }),
    ).toBeInTheDocument();
  });
  it("管理员的更多页列出审计、系统与修改密码", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) =>
        url.endsWith("/session")
          ? Promise.resolve(
              response({
                user: { ...user, role: "admin" },
                csrf_token: "c",
                expires_at: "2027-01-01T00:00:00Z",
              }),
            )
          : Promise.resolve(response({ items: [] })),
      ),
    );
    const view = mount("/admin/more");
    expect(
      await screen.findByRole("heading", { name: "更多" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "更多" })).toHaveAttribute(
      "href",
      "/admin/more",
    );
    const list = view.container.querySelector<HTMLElement>(".more-links")!;
    expect(
      within(list)
        .getAllByRole("link")
        .map((link) => [link.textContent, link.getAttribute("href")]),
    ).toEqual([
      ["审计", "/admin/audit"],
      ["系统", "/admin/system"],
      ["修改密码", "/admin/password"],
    ]);
    for (const link of screen.getAllByRole("link", { name: "修改密码" }))
      expect(link).toHaveAttribute("href", "/admin/password");
  });
  it("管理员可以从导航进入运行配置", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url.endsWith("/session"))
          return Promise.resolve(
            response({
              user: { ...user, role: "admin" },
              csrf_token: "c",
              expires_at: "2027-01-01T00:00:00Z",
            }),
          );
        if (url.endsWith("/admin/runtime/history"))
          return Promise.resolve(response({ items: [] }));
        if (url.endsWith("/admin/runtime/config"))
          return Promise.resolve(
            response({
              revision: "runtime-revision",
              config: {
                version: 1,
                query_log: false,
                telemetry: {
                  aggregate_retention_days: 7,
                  query_retention_hours: 24,
                  max_query_records: 100000,
                },
                plugins: [],
              },
            }),
          );
        return Promise.resolve(response({ items: [] }));
      }),
    );
    mount("/admin/runtime");
    expect(
      await screen.findByRole("heading", { name: "运行配置" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "运行配置" })).toHaveAttribute(
      "href",
      "/admin/runtime",
    );
  });
  it("运行配置有草稿时取消退出不会注销会话", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (url.endsWith("/session")) {
          if (init?.method === "DELETE")
            return Promise.resolve(response({}, 204));
          return Promise.resolve(
            response({
              user: { ...user, role: "admin" },
              csrf_token: "c",
              expires_at: "2027-01-01T00:00:00Z",
            }),
          );
        }
        if (url.endsWith("/admin/runtime/history"))
          return Promise.resolve(response({ items: [] }));
        if (url.endsWith("/admin/runtime/config"))
          return Promise.resolve(
            response({
              revision: "runtime-revision",
              config: {
                version: 1,
                query_log: false,
                telemetry: {
                  aggregate_retention_days: 7,
                  query_retention_hours: 24,
                  max_query_records: 100000,
                },
                plugins: [],
              },
            }),
          );
        return Promise.resolve(response({ items: [] }));
      });
    vi.stubGlobal("fetch", fetcher);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    mount("/admin/runtime");
    fireEvent.click(
      await screen.findByRole("checkbox", { name: "记录查询明细" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
    expect(confirm).toHaveBeenCalledWith(
      "运行配置还有未保存的修改，确定退出登录吗？",
    );
    expect(
      fetcher.mock.calls.some(
        ([url, init]) =>
          String(url).endsWith("/session") && init?.method === "DELETE",
      ),
    ).toBe(false);
    expect(
      screen.getByRole("heading", { name: "运行配置" }),
    ).toBeInTheDocument();
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
      await screen.findByRole("heading", { name: "主页" }),
    ).toBeInTheDocument();
  });
  it.each([
    "/app/privacy",
    "/app/lists",
    "/app/rules",
    "/app/labs",
    "/app/advanced",
    "/app/lookup",
  ])("已移除的用户页面 %s 重定向到主页", async (path) => {
    const fetcher = vi.fn().mockResolvedValue(
      response({
        user,
        csrf_token: "c",
        expires_at: "2027-01-01T00:00:00Z",
      }),
    );
    vi.stubGlobal("fetch", fetcher);
    mount(path);
    expect(
      await screen.findByRole("heading", { name: "主页" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: /规则|公共列表|隐私|Lookup/ }),
    ).toBeNull();
    for (const [url] of fetcher.mock.calls)
      expect(String(url)).not.toMatch(
        /\/me\/(settings|rules|public-lists|lookup)/,
      );
  });
  it("已移除的管理员公共列表页重定向到总览", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) =>
        url.endsWith("/session")
          ? Promise.resolve(
              response({
                user: { ...user, role: "admin" },
                csrf_token: "c",
                expires_at: "2027-01-01T00:00:00Z",
              }),
            )
          : Promise.resolve(response({}, 503)),
      ),
    );
    mount("/admin/lists");
    expect(
      await screen.findByRole("heading", { name: "服务总览" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "公共列表" })).toBeNull();
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
  it("轮换和撤销凭证需在对话框中确认后才发送请求", async () => {
    const credential = {
      id: "k",
      user_id: "u1",
      name: "手机",
      expires_at: "0001-01-01T00:00:00Z",
      revoked_at: "0001-01-01T00:00:00Z",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
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
        if (url.endsWith("/me")) return Promise.resolve(response({ user }));
        if (url.endsWith("/rotate"))
          return Promise.resolve(
            response({
              credential,
              token: "rotated-secret",
              doh_url: "https://dns.test/dns-query/k",
            }),
          );
        if (url.includes("/credentials"))
          return Promise.resolve(response({ items: [credential] }));
        return Promise.resolve(response({}));
      });
    vi.stubGlobal("fetch", fetcher);
    const writes = () =>
      fetcher.mock.calls.filter(
        ([, init]) => init?.method === "POST" || init?.method === "DELETE",
      );
    mount("/app/account");
    fireEvent.click(await screen.findByRole("button", { name: "撤销 手机" }));
    let dialog = screen.getByRole("dialog", { name: "撤销凭证" });
    expect(dialog).toHaveTextContent("撤销“手机”会立即中断使用它的设备");
    fireEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(writes()).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "轮换 手机" }));
    dialog = screen.getByRole("dialog", { name: "轮换凭证" });
    expect(writes()).toHaveLength(0);
    fireEvent.click(within(dialog).getByRole("button", { name: "轮换" }));
    expect(await screen.findByText("rotated-secret")).toBeInTheDocument();
    expect(writes().map(([url]) => String(url))).toEqual([
      expect.stringMatching(/\/me\/credentials\/k\/rotate$/),
    ]);
  });
});
