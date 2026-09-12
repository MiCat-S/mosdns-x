import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import {
  createMemoryRouter,
  MemoryRouter,
  RouterProvider,
} from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import {
  credentialStatus,
  credentialCanRevoke,
  fromLocalDateTime,
  QueryDetails,
  LookupPage,
  PrivacyPage,
  PublicListsPage,
  AdminPublicListsPage,
  RulesPage,
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
  it("安全与隐私页读取并保存用户设置", async () => {
    const settings = {
      user_id: "u1",
      strip_ecs: false,
      block_private_answers: false,
      blocked_qtypes: ["TXT"],
      updated_at: "2026-09-12T00:00:00Z",
    };
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(response(settings))
      .mockResolvedValueOnce(response({ ...settings, strip_ecs: true }));
    vi.stubGlobal("fetch", fetcher);
    render(<PrivacyPage />);
    const stripECS = await screen.findByRole("checkbox", {
      name: "停用 ECS",
    });
    fireEvent.click(stripECS);
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(String(fetcher.mock.calls[1][0])).toContain("/me/settings");
    const init = fetcher.mock.calls[1][1] as RequestInit;
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(String(init.body))).toEqual({
      strip_ecs: true,
    });
  });
  it("DNS Lookup 发送限定记录类型并展示应答", async () => {
    const fetcher = vi.fn().mockResolvedValue(
      response({
        question: { name: "example.com", qtype: "AAAA" },
        rcode: "NOERROR",
        duration_ms: 1.5,
        answers: [
          { name: "example.com.", type: "AAAA", ttl: 60, value: "2001:db8::1" },
        ],
        authority: [],
        additional: [],
        edns: {
          present: false,
          version: 0,
          udp_size: 0,
          dnssec_ok: false,
          option_codes: [],
        },
      }),
    );
    vi.stubGlobal("fetch", fetcher);
    render(<LookupPage />);
    fireEvent.change(screen.getByLabelText("域名"), {
      target: { value: "example.com" },
    });
    fireEvent.change(screen.getByLabelText("记录类型"), {
      target: { value: "AAAA" },
    });
    fireEvent.click(screen.getByRole("button", { name: "开始查询" }));
    expect(await screen.findByText(/2001:db8::1/)).toBeInTheDocument();
    expect(String(fetcher.mock.calls[0][0])).toContain("/me/lookup");
    expect(
      JSON.parse(String((fetcher.mock.calls[0][1] as RequestInit).body)),
    ).toEqual({
      name: "example.com",
      qtype: "AAAA",
    });
  });
  it("自定义规则页使用用户规则接口", async () => {
    const settings = {
      user_id: "u1",
      strip_ecs: false,
      block_private_answers: false,
      blocked_qtypes: [],
      custom_block_enabled: true,
      custom_allow_enabled: true,
      custom_rewrite_enabled: true,
      policy_paused_until: "0001-01-01T00:00:00Z",
      updated_at: "2026-09-12T00:00:00Z",
    };
    const writes: Array<Record<string, unknown>> = [];
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        const method = init?.method ?? "GET";
        if (method === "PATCH") {
          const body = JSON.parse(String(init?.body));
          writes.push(body);
          return Promise.resolve(
            response({ ...settings, custom_block_enabled: false }),
          );
        }
        if (method === "POST") {
          const body = JSON.parse(String(init?.body));
          writes.push(body);
          return Promise.resolve(
            response({
              id: `r${writes.length}`,
              user_id: "u1",
              ...body,
              created_at: "2026-09-12T00:00:00Z",
              updated_at: "2026-09-12T00:00:00Z",
            }),
          );
        }
        return Promise.resolve(
          url.includes("/me/settings")
            ? response(settings)
            : response({ items: [] }),
        );
      });
    vi.stubGlobal("fetch", fetcher);
    render(<RulesPage />);
    expect(await screen.findByText("尚未添加拦截规则。")).toBeInTheDocument();
    expect(String(fetcher.mock.calls[0][0])).toContain("/me/rules");
    fireEvent.click(screen.getByRole("checkbox", { name: "自定义拦截" }));
    await waitFor(() =>
      expect(writes).toContainEqual({ custom_block_enabled: false }),
    );
    fireEvent.change(screen.getAllByRole("textbox", { name: "规则域名" })[0], {
      target: { value: "ads.example" },
    });
    fireEvent.click(screen.getByRole("button", { name: "添加规则" }));
    await waitFor(() =>
      expect(writes).toContainEqual({
        action: "block",
        match: "suffix",
        pattern: "ads.example",
        priority: 100,
        enabled: true,
      }),
    );
    fireEvent.change(screen.getByRole("textbox", { name: "重写域名" }), {
      target: { value: "router.example" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "重写值" }), {
      target: { value: "192.0.2.9" },
    });
    fireEvent.click(screen.getByRole("button", { name: "添加重写" }));
    await waitFor(() =>
      expect(writes).toContainEqual({
        action: "rewrite",
        match: "exact",
        pattern: "router.example",
        priority: 100,
        record_type: "A",
        value: "192.0.2.9",
        enabled: true,
      }),
    );
  });
  it("用户公共列表按分类显示，并即时保存个人覆盖", async () => {
    const list = {
      id: "list-1",
      name: "广告拦截",
      category: "广告与追踪",
      url: "https://lists.example/ads.txt",
      format: "hosts",
      enabled: true,
      sha256: "",
      refresh_seconds: 3600,
      entry_count: 128,
      last_refresh_status: "成功",
      last_refreshed_at: "2026-09-12T00:00:00Z",
      created_at: "2026-09-01T00:00:00Z",
      updated_at: "2026-09-01T00:00:00Z",
    };
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) =>
        init?.method === "PATCH"
          ? Promise.resolve(response({}, 204))
          : Promise.resolve(
              response({ items: [{ list, enabled: true, overridden: false }] }),
            ),
      );
    vi.stubGlobal("fetch", fetcher);
    render(<PublicListsPage />);
    expect(await screen.findByText("广告与追踪")).toBeInTheDocument();
    expect(screen.getByText("广告拦截")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("checkbox", { name: "启用 广告拦截" }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(String(fetcher.mock.calls[1][0])).toContain(
      "/me/public-lists/list-1",
    );
    const init = fetcher.mock.calls[1][1] as RequestInit;
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(String(init.body))).toEqual({ enabled: false });
    expect(screen.getByText(/已覆盖管理员默认设置/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "恢复管理员默认值" }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(4));
    expect(
      JSON.parse(String((fetcher.mock.calls[2][1] as RequestInit).body)),
    ).toEqual({ enabled: null });
    expect(screen.getByText(/继承管理员默认设置/)).toBeInTheDocument();
  });
  it("管理员可以预览、发布并刷新公共列表", async () => {
    const list = {
      id: "list-1",
      name: "广告拦截",
      category: "广告与追踪",
      url: "https://lists.example/ads.txt",
      format: "hosts",
      enabled: true,
      sha256: "",
      refresh_seconds: 3600,
      entry_count: 0,
      last_refresh_status: "未刷新",
      created_at: "2026-09-01T00:00:00Z",
      updated_at: "2026-09-01T00:00:00Z",
    };
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (init?.method === "POST" && url.endsWith("/refresh"))
          return Promise.resolve(response({}, 204));
        if (init?.method === "POST" && url.endsWith("/validate"))
          return Promise.resolve(
            response({
              valid: true,
              validation_token: "candidate-token",
              format: "mosdns",
              entry_count: 42,
              invalid_entry_count: 1,
              invalid_entries: [{ line: 3, reason: "不是有效域名" }],
              samples: ["ads.example"],
              sha256: "a".repeat(64),
            }),
          );
        if (init?.method === "POST" && url.endsWith("/publish"))
          return Promise.resolve(response(list, 201));
        return Promise.resolve(response({ items: [list] }));
      });
    vi.stubGlobal("fetch", fetcher);
    render(<AdminPublicListsPage />);
    expect(await screen.findByText("广告拦截")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    await waitFor(() =>
      expect(
        fetcher.mock.calls.some(
          ([url, init]) =>
            String(url).endsWith("/admin/public-lists/list-1/refresh") &&
            (init as RequestInit).method === "POST",
        ),
      ).toBe(true),
    );
    fireEvent.click(screen.getAllByRole("button", { name: "新建订阅" })[0]);
    fireEvent.change(screen.getByLabelText("名称"), {
      target: { value: "隐私规则" },
    });
    fireEvent.change(screen.getByLabelText("分类"), {
      target: { value: "隐私增强" },
    });
    fireEvent.change(screen.getByLabelText("HTTPS URL"), {
      target: { value: "https://lists.example/privacy.txt" },
    });
    fireEvent.click(screen.getByRole("button", { name: "验证并预览" }));
    expect(
      await screen.findByText("验证通过，可以确认发布"),
    ).toBeInTheDocument();
    expect(screen.getByText("42")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "确认发布" }));
    await waitFor(() =>
      expect(
        fetcher.mock.calls.some(
          ([url, init]) =>
            String(url).endsWith("/admin/public-lists/publish") &&
            (init as RequestInit).method === "POST",
        ),
      ).toBe(true),
    );
    const post = fetcher.mock.calls.find(
      ([url, init]) =>
        String(url).endsWith("/admin/public-lists/publish") &&
        (init as RequestInit).method === "POST",
    );
    expect(JSON.parse(String((post?.[1] as RequestInit).body))).toEqual({
      validation_token: "candidate-token",
    });
  });
  it("管理员公共列表区分空目录与加载失败，创建表单只在弹窗出现", async () => {
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new Error("network unavailable"))
      .mockResolvedValue(response({ items: [] }));
    vi.stubGlobal("fetch", fetcher);
    const { unmount } = render(<AdminPublicListsPage />);
    expect(
      await screen.findByText(
        "加载失败不代表列表为空。重试后再进行发布或下架操作。",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("名称")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(
      await screen.findByText("尚未创建用户拦截订阅。"),
    ).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "新建订阅" })[0]);
    const dialog = screen.getByRole("dialog", {
      name: "新建用户拦截订阅",
    });
    expect(dialog).toBeInTheDocument();
    expect(
      within(dialog).getByText(/域名命中后返回 NXDOMAIN/),
    ).toBeInTheDocument();
    unmount();
  });
  it("公共列表验证结果只绑定发起验证时的表单内容", async () => {
    let resolveValidation: ((value: Response) => void) | undefined;
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (init?.method === "POST" && url.endsWith("/validate")) {
          return new Promise<Response>((resolve) => {
            resolveValidation = resolve;
          });
        }
        return Promise.resolve(response({ items: [] }));
      });
    vi.stubGlobal("fetch", fetcher);
    render(<AdminPublicListsPage />);
    await screen.findByText("尚未创建用户拦截订阅。");
    fireEvent.click(screen.getAllByRole("button", { name: "新建订阅" })[0]);
    fireEvent.change(screen.getByLabelText("名称"), {
      target: { value: "广告规则" },
    });
    const url = screen.getByLabelText("HTTPS URL");
    fireEvent.change(url, {
      target: { value: "https://lists.example/old.txt" },
    });
    fireEvent.click(screen.getByRole("button", { name: "验证并预览" }));
    fireEvent.change(url, {
      target: { value: "https://lists.example/new.txt" },
    });
    resolveValidation?.(
      response({
        valid: true,
        validation_token: "old-token",
        format: "mosdns",
        entry_count: 1,
      }),
    );
    expect(
      await screen.findByText("表单内容已变化，请重新验证当前内容。"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "确认发布" })).toBeDisabled();
  });
  it("编辑已发布列表的普通元数据不会由前端主动下架", async () => {
    const list = {
      id: "list-1",
      name: "广告拦截",
      category: "",
      url: "https://lists.example/ads.txt",
      format: "mosdns",
      enabled: true,
      default_enabled: true,
      published: true,
      refresh_seconds: 3600,
      created_at: "2026-09-01T00:00:00Z",
      updated_at: "2026-09-01T00:00:00Z",
    };
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) =>
        init?.method === "PATCH"
          ? Promise.resolve(response({ ...list, name: "新名称" }))
          : Promise.resolve(response({ items: [list] })),
      );
    vi.stubGlobal("fetch", fetcher);
    render(<AdminPublicListsPage />);
    await screen.findByText("广告拦截");
    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    fireEvent.change(screen.getByLabelText("名称"), {
      target: { value: "新名称" },
    });
    fireEvent.click(screen.getByRole("button", { name: "保存修改" }));
    await waitFor(() =>
      expect(
        fetcher.mock.calls.some(([, init]) => init?.method === "PATCH"),
      ).toBe(true),
    );
    const patchCall = fetcher.mock.calls.find(
      ([, init]) => init?.method === "PATCH",
    );
    expect(
      JSON.parse(String((patchCall?.[1] as RequestInit).body)),
    ).not.toHaveProperty("published");
  });
  it("用户页只展示已发布列表并说明显式覆盖和下架行为", async () => {
    const unpublished = {
      id: "draft-1",
      name: "待发布规则",
      category: "草稿",
      url: "https://lists.example/draft.txt",
      format: "mosdns",
      enabled: true,
      published: false,
      default_enabled: true,
      refresh_seconds: 3600,
      created_at: "2026-09-01T00:00:00Z",
      updated_at: "2026-09-01T00:00:00Z",
    };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        response({
          items: [{ list: unpublished, enabled: true, overridden: true }],
        }),
      ),
    );
    render(
      <MemoryRouter>
        <PublicListsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText("暂无可订阅列表")).toBeInTheDocument();
    expect(screen.queryByText("待发布规则")).not.toBeInTheDocument();
    expect(screen.getByText(/管理员下架列表后/)).toBeInTheDocument();
  });
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
              response_source: "upstream",
              response_source_id: "forward_remote",
              upstream_id: "forward_remote/0",
              matched_rule_id: "rule-1",
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
    expect(within(first).getByText("上游")).toBeInTheDocument();
    expect(within(first).getByText("forward_remote/0")).toBeInTheDocument();
    expect(within(first).getByText("rule-1")).toBeInTheDocument();
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
    fireEvent.change(screen.getByLabelText("处理来源"), {
      target: { value: "upstream" },
    });
    fireEvent.change(screen.getByLabelText("最终上游"), {
      target: { value: "forward_remote/0" },
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
    expect(url.searchParams.get("upstream_id")).toBe("forward_remote/0");
  });
  it("查询详情将规则和公共列表 ID 解析为可读名称", async () => {
    const record = {
      id: "q1",
      time: "2026-09-11T12:00:00Z",
      user_id: "u1",
      credential_id: "",
      client_ip: "192.0.2.1",
      name: "ads.example.",
      qtype: "A",
      rcode: "NXDOMAIN",
      duration_ms: 1,
      cache_hit: false,
      protocol: "h3",
      answer_ips: [],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url.includes("/me/rules"))
          return Promise.resolve(
            response({
              items: [
                {
                  id: "rule-1",
                  action: "block",
                  match: "suffix",
                  pattern: "ads.example",
                },
              ],
            }),
          );
        if (url.includes("/me/public-lists"))
          return Promise.resolve(
            response({
              items: [
                { list: { id: "list-1", name: "公共广告列表" }, enabled: true },
              ],
            }),
          );
        return Promise.resolve(
          response({
            items: [
              {
                ...record,
                response_source: "custom_block",
                response_source_id: "rule-1",
                matched_rule_id: "rule-1",
              },
              {
                ...record,
                id: "q2",
                name: "tracker.example.",
                response_source: "public_list",
                response_source_id: "list-1",
                matched_public_list_id: "list-1",
              },
            ],
          }),
        );
      }),
    );
    render(<QueryDetails path="/me/queries" enabled />);
    fireEvent.click(
      await screen.findByRole("button", { name: "查看 ads.example. 详情" }),
    );
    expect(await screen.findAllByText("block · ads.example")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "关闭" }));
    fireEvent.click(
      screen.getByRole("button", { name: "查看 tracker.example. 详情" }),
    );
    expect(await screen.findAllByText("公共广告列表")).toHaveLength(2);
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
