import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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

function response(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  });
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

    render(<RuntimeConfigPage />);
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

    render(<RuntimeConfigPage />);
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
});
