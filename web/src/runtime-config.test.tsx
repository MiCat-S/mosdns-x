import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RuntimeConfigPage } from "./runtime-config";

const config = {
  version: 1,
  query_log: true,
  telemetry: {
    aggregate_retention_days: 7,
    query_retention_hours: 24,
    max_query_records: 100000,
  },
  plugins: [
    {
      tag: "forward",
      type: "fast_forward",
      editable: true,
      fast_forward: {
        upstreams: [{ addr: "https://dns.example/dns-query", max_conns: 4 }],
      },
    },
    {
      tag: "cache",
      type: "cache",
      editable: true,
      cache: {
        size: 1024,
        lazy_cache_ttl: 60,
        lazy_cache_reply_ttl: 5,
        compress_resp: true,
      },
    },
    {
      tag: "private_forward",
      type: "fast_forward",
      editable: false,
      read_only_reason: "sensitive_parameters",
    },
  ],
};

function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function renderRuntime(initialEntries = ["/admin/runtime"]) {
  const router = createMemoryRouter(
    [{ path: "*", element: <RuntimeConfigPage /> }],
    { initialEntries },
  );
  return { ...render(<RouterProvider router={router} />), router };
}

describe("管理端运行配置", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("验证后使用一次性令牌应用安全结构化配置", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (url.endsWith("/admin/runtime/history"))
          return Promise.resolve(
            response({
              items: [
                {
                  revision: "old-revision",
                  created_at: "2026-09-12T00:00:00Z",
                },
              ],
            }),
          );
        if (url.endsWith("/admin/runtime/config/validate"))
          return Promise.resolve(
            response({
              token: "single-use-token",
              expires_at: "2026-09-12T00:05:00Z",
              revision: "current-revision",
              will_clear_caches: true,
            }),
          );
        if (url.endsWith("/admin/runtime/config/apply"))
          return Promise.resolve(
            response({
              revision: "next-revision",
              config: { ...config, query_log: false },
              caches_cleared: true,
            }),
          );
        if (url.endsWith("/admin/runtime/config"))
          return Promise.resolve(
            response({ revision: "current-revision", config }),
          );
        return Promise.reject(
          new Error(`unexpected request: ${url} ${init?.method}`),
        );
      });
    vi.stubGlobal("fetch", fetcher);

    renderRuntime();
    expect(await screen.findByText("private_forward")).toBeInTheDocument();
    expect(
      screen.getByText("此插件含敏感连接参数，不能在面板中读取或修改。"),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("checkbox", { name: "记录查询明细" }));
    fireEvent.click(screen.getByRole("button", { name: "验证修改" }));

    await waitFor(() =>
      expect(
        fetcher.mock.calls.some(([url]) =>
          String(url).endsWith("/admin/runtime/config/validate"),
        ),
      ).toBe(true),
    );
    const validate = fetcher.mock.calls.find(([url]) =>
      String(url).endsWith("/admin/runtime/config/validate"),
    );
    const validateBody = JSON.parse(
      String((validate?.[1] as RequestInit).body),
    );
    expect(validateBody.revision).toBe("current-revision");
    expect(validateBody.config.query_log).toBe(false);
    expect(JSON.stringify(validateBody.config)).not.toMatch(
      /socks5|password|redis/i,
    );
    expect(
      await screen.findByText(/应用后会清空 DNS 缓存/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "应用已验证的修改" }));
    await waitFor(() =>
      expect(
        fetcher.mock.calls.some(([url]) =>
          String(url).endsWith("/admin/runtime/config/apply"),
        ),
      ).toBe(true),
    );
    const apply = fetcher.mock.calls.find(([url]) =>
      String(url).endsWith("/admin/runtime/config/apply"),
    );
    expect(JSON.parse(String((apply?.[1] as RequestInit).body))).toEqual({
      token: "single-use-token",
    });
    expect(
      await screen.findByText("运行配置已应用，DNS 缓存已清空。"),
    ).toBeInTheDocument();
  });

  it("重读主配置、探测可编辑上游且不展示探测原始错误", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (url.endsWith("/admin/runtime/history"))
          return Promise.resolve(response({ items: [] }));
        if (url.endsWith("/admin/runtime/config/reload"))
          return Promise.resolve(
            response({
              revision: "current-revision",
              config,
              restart_required: ["api"],
              caches_cleared: false,
            }),
          );
        if (url.endsWith("/admin/runtime/upstreams/forward/probe"))
          return Promise.resolve(
            response({
              items: [
                {
                  upstream_id: "forward/0",
                  duration_ms: 3.4,
                  rcode: 0,
                  success: true,
                },
                {
                  upstream_id: "forward/1",
                  duration_ms: 5.6,
                  rcode: -1,
                  success: false,
                  error: "private transport failure",
                },
              ],
            }),
          );
        if (url.endsWith("/admin/runtime/config"))
          return Promise.resolve(
            response({ revision: "current-revision", config }),
          );
        return Promise.reject(
          new Error(`unexpected request: ${url} ${init?.method}`),
        );
      });
    vi.stubGlobal("fetch", fetcher);

    renderRuntime();
    await screen.findByText("上游 · forward");

    fireEvent.click(screen.getByRole("button", { name: "重读主配置" }));
    await waitFor(() =>
      expect(
        fetcher.mock.calls.some(
          ([url, init]) =>
            String(url).endsWith("/admin/runtime/config/reload") &&
            (init as RequestInit).method === "POST",
        ),
      ).toBe(true),
    );
    expect(
      await screen.findByText(/主配置包含需要重启的项：api/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "探测上游" }));
    expect(await screen.findByText("forward/0")).toBeInTheDocument();
    expect(screen.getByText("forward/1")).toBeInTheDocument();
    expect(
      screen.queryByText("private transport failure"),
    ).not.toBeInTheDocument();
  });

  it("回滚使用独立端点，并在重读前保护未保存修改", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (url.endsWith("/admin/runtime/history"))
          return Promise.resolve(
            response({
              items: [
                {
                  revision: "old-revision",
                  created_at: "2026-09-12T00:00:00Z",
                },
              ],
            }),
          );
        if (url.endsWith("/admin/runtime/rollback"))
          return Promise.resolve(
            response({ revision: "rollback-revision", config }),
          );
        if (url.endsWith("/admin/runtime/config/reload"))
          return Promise.resolve(
            response({
              revision: "reload-revision",
              config,
              restart_required: [],
              caches_cleared: false,
            }),
          );
        if (url.endsWith("/admin/runtime/config"))
          return Promise.resolve(
            response({ revision: "current-revision", config }),
          );
        return Promise.reject(
          new Error(`unexpected request: ${url} ${init?.method}`),
        );
      });
    vi.stubGlobal("fetch", fetcher);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);

    renderRuntime();
    await screen.findByText("old-revision");
    fireEvent.click(screen.getByRole("button", { name: "回滚" }));
    await waitFor(() =>
      expect(
        fetcher.mock.calls.some(
          ([url, init]) =>
            String(url).endsWith("/admin/runtime/rollback") &&
            (init as RequestInit).method === "POST",
        ),
      ).toBe(true),
    );
    await screen.findByText("已回滚运行配置。");

    fireEvent.click(screen.getByRole("checkbox", { name: "记录查询明细" }));
    const configReadsBefore = fetcher.mock.calls.filter(([url]) =>
      String(url).endsWith("/admin/runtime/config"),
    ).length;
    fireEvent.click(screen.getByRole("button", { name: "重读主配置" }));
    fireEvent.click(screen.getByRole("button", { name: "重新读取摘要" }));
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(
      fetcher.mock.calls.some(([url]) =>
        String(url).endsWith("/admin/runtime/config/reload"),
      ),
    ).toBe(false);
    expect(
      fetcher.mock.calls.filter(([url]) =>
        String(url).endsWith("/admin/runtime/config"),
      ),
    ).toHaveLength(configReadsBefore);
  });

  it("未启用托管配置时仍显示只读摘要，并独立报告历史不可用", async () => {
    const readOnlyState = {
      revision: "",
      config,
      mode: "read_only",
      setup: {
        managed_config_configured: false,
        config_source_available: true,
        reason: "managed_config_not_configured",
      },
      capabilities: {
        view: true,
        edit: false,
        validate: false,
        apply: false,
        reload: false,
        history: false,
        rollback: false,
        probe: false,
      },
      sources: {
        running: {
          kind: "running",
          status: "available",
          config,
          data_providers: [
            {
              tag: "gfwlist",
              file: "/etc/mosdns/rule/gfw.txt",
              auto_reload: true,
              runtime_state: {
                status: "unsupported",
                entry_count: null,
                loaded_at: null,
              },
            },
          ],
          plugins: [],
        },
        base: { kind: "base", status: "available", config },
        candidate: { kind: "candidate", status: "unavailable" },
      },
    };
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation((url: string) =>
          url.endsWith("/admin/runtime/history")
            ? Promise.resolve(
                response({ error: { code: "managed_config_disabled" } }, 503),
              )
            : Promise.resolve(response(readOnlyState)),
        ),
    );
    renderRuntime();
    expect(await screen.findByText("当前为只读模式。")).toBeInTheDocument();
    expect(screen.getByText("gfwlist")).toBeInTheDocument();
    expect(screen.getByText(/条目：暂不可用/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "重读主配置" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "验证修改" }),
    ).not.toBeInTheDocument();
    expect(
      await screen.findByText(/当前未启用托管运行配置/),
    ).toBeInTheDocument();
  });

  it("旧节点返回内部错误码时显示可操作说明", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          response({ error: { code: "managed_config_disabled" } }, 503),
        ),
    );
    renderRuntime();
    expect(
      await screen.findByText(/当前未启用托管运行配置/),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("managed_config_disabled"),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "重试摘要" }),
    ).toBeInTheDocument();
  });

  it("应用失败后清除一次性令牌，修订冲突时保留草稿并读取新基线", async () => {
    let configReads = 0;
    const fetcher = vi.fn().mockImplementation((url: string) => {
      if (url.endsWith("/admin/runtime/history"))
        return Promise.resolve(response({ items: [] }));
      if (url.endsWith("/admin/runtime/config/validate"))
        return Promise.resolve(
          response({
            token: "one-shot",
            revision: "old-revision",
            expires_at: "2026-09-12T00:05:00Z",
          }),
        );
      if (url.endsWith("/admin/runtime/config/apply"))
        return Promise.resolve(
          response({ error: { code: "revision_conflict" } }, 409),
        );
      if (url.endsWith("/admin/runtime/config")) {
        configReads++;
        return Promise.resolve(
          response({
            revision: configReads === 1 ? "old-revision" : "new-revision",
            config,
          }),
        );
      }
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });
    vi.stubGlobal("fetch", fetcher);
    renderRuntime();
    const queryLog = await screen.findByRole("checkbox", {
      name: "记录查询明细",
    });
    fireEvent.click(queryLog);
    fireEvent.click(screen.getByRole("button", { name: "验证修改" }));
    await screen.findByText(/验证令牌有效至/);
    fireEvent.click(screen.getByRole("button", { name: "应用已验证的修改" }));
    expect(
      await screen.findByText(/已载入最新运行基线，并保留你的草稿/),
    ).toBeInTheDocument();
    expect(queryLog).not.toBeChecked();
    expect(
      screen.getByRole("button", { name: "应用已验证的修改" }),
    ).toBeDisabled();
  });

  it("配置摘要失败时仍独立展示已经加载的修订历史", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) =>
        url.endsWith("/admin/runtime/history")
          ? Promise.resolve(
              response({
                items: [
                  {
                    revision: "history-only-revision",
                    created_at: "2026-09-12T00:00:00Z",
                  },
                ],
              }),
            )
          : Promise.reject(new Error("config unavailable")),
      ),
    );
    renderRuntime();
    expect(await screen.findByText("history-only")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "回滚" })).toBeDisabled();
  });

  it("浏览器后退会提示并保留未保存草稿", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation((url: string) =>
          url.endsWith("/admin/runtime/history")
            ? Promise.resolve(response({ items: [] }))
            : Promise.resolve(
                response({ revision: "current-revision", config }),
              ),
        ),
    );
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { router } = renderRuntime(["/previous", "/admin/runtime"]);
    fireEvent.click(
      await screen.findByRole("checkbox", { name: "记录查询明细" }),
    );
    await router.navigate(-1);
    await waitFor(() => expect(confirm).toHaveBeenCalled());
    expect(router.state.location.pathname).toBe("/admin/runtime");
  });
});
