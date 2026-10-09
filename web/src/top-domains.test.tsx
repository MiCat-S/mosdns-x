import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TopDomainsCard } from "./top-domains";
import type { TopDomains } from "./types";

const from = "2026-10-09T00:00:00Z";
const to = "2026-10-10T00:00:00Z";

function mockRanking(body: TopDomains) {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      urls.push(String(input));
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return urls;
}

afterEach(() => vi.unstubAllGlobals());

describe("TopDomainsCard", () => {
  it("ranks the names with their share, cache hits and longer TTL", async () => {
    const urls = mockRanking({
      from,
      to,
      queries: 200,
      query_log_enabled: true,
      domains: [
        {
          name: "www.example.com.",
          queries: 120,
          cache_hits: 117,
          extended_ttl: 1800,
        },
        { name: "api.example.net.", queries: 30, cache_hits: 0 },
      ],
    });
    render(
      <TopDomainsCard
        path="/admin/top-domains?user_id=u%201"
        from={from}
        to={to}
      />,
    );
    expect(await screen.findByText("www.example.com")).toBeTruthy();
    expect(
      screen.getByText("占 60.0% · 缓存命中 97.5% · TTL 已自动延长至 30 分钟"),
    ).toBeTruthy();
    expect(screen.getByText("占 15.0% · 缓存命中 0.0%")).toBeTruthy();
    expect(screen.getByText("120")).toBeTruthy();
    const url = new URL(urls[0], "http://panel");
    expect(url.pathname).toBe("/api/v1/admin/top-domains");
    expect(url.searchParams.get("user_id")).toBe("u 1");
    expect(url.searchParams.get("from")).toBe(from);
    expect(url.searchParams.get("to")).toBe(to);
  });

  it("explains a ranking without query logging", async () => {
    mockRanking({
      from,
      to,
      queries: 0,
      query_log_enabled: false,
      domains: [],
    });
    render(<TopDomainsCard path="/me/top-domains" from={from} to={to} />);
    expect(
      await screen.findByText("查询明细已关闭，无法统计查询最多的域名。"),
    ).toBeTruthy();
  });
});
