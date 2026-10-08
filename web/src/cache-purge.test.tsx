import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CachePurgeCard,
  cacheDomain,
  cachePurgeSummary,
  PurgeDomainButton,
} from "./cache-purge";

const response = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

function mockPurge(reply: () => Response) {
  const bodies: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(String(input)).toBe("/api/v1/admin/runtime/cache/purge");
      expect(init?.method).toBe("POST");
      bodies.push(JSON.parse(String(init?.body)));
      return reply();
    }),
  );
  return bodies;
}

afterEach(() => vi.unstubAllGlobals());

describe("cacheDomain", () => {
  it("takes the host from a pasted URL", () => {
    expect(cacheDomain(" https://WWW.Example.com:8443/a?b ").name).toBe(
      "www.example.com",
    );
    expect(cacheDomain("example.com.").name).toBe("example.com.");
  });
  it("refuses empty input and wildcards", () => {
    expect(cacheDomain("  ").error).not.toBe("");
    expect(cacheDomain(".").error).not.toBe("");
    expect(cacheDomain("*.example.com").error).toContain("同时清除子域名");
  });
});

describe("cachePurgeSummary", () => {
  const base = { domain: "example.com.", subdomains: false, caches: 1 };
  it("explains every outcome", () => {
    expect(cachePurgeSummary({ ...base, caches: 0, removed: 0 })).toBe(
      "当前运行配置中没有缓存插件。",
    );
    expect(cachePurgeSummary({ ...base, removed: 0 })).toBe(
      "缓存中没有 example.com 的记录。",
    );
    expect(cachePurgeSummary({ ...base, subdomains: true, removed: 4 })).toBe(
      "已清除 example.com 及其子域名的 4 条缓存记录。",
    );
  });
});

describe("CachePurgeCard", () => {
  it("purges a domain with its subdomains", async () => {
    const bodies = mockPurge(() =>
      response({
        domain: "example.com.",
        subdomains: true,
        caches: 2,
        removed: 3,
      }),
    );
    render(<CachePurgeCard />);
    fireEvent.change(screen.getByLabelText("域名"), {
      target: { value: "https://example.com/" },
    });
    fireEvent.click(screen.getByLabelText("同时清除子域名"));
    fireEvent.click(screen.getByRole("button", { name: "清除缓存" }));
    expect(
      await screen.findByText("已清除 example.com 及其子域名的 3 条缓存记录。"),
    ).toBeTruthy();
    expect(bodies).toEqual([{ domain: "example.com", subdomains: true }]);
  });

  it("checks the input before calling the server", async () => {
    const bodies = mockPurge(() => response({}));
    render(<CachePurgeCard />);
    fireEvent.change(screen.getByLabelText("域名"), {
      target: { value: "*.example.com" },
    });
    fireEvent.click(screen.getByRole("button", { name: "清除缓存" }));
    expect((await screen.findByRole("alert")).textContent).toContain(
      "同时清除子域名",
    );
    expect(bodies).toEqual([]);
  });

  it("shows the server's verdict on a bad domain", async () => {
    mockPurge(() =>
      response({ error: { code: "invalid_domain", message: "x" } }, 400),
    );
    render(<CachePurgeCard />);
    fireEvent.change(screen.getByLabelText("域名"), {
      target: { value: "bad_domain/x" },
    });
    fireEvent.click(screen.getByRole("button", { name: "清除缓存" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "请输入有效的域名，例如 example.com。",
    );
  });
});

describe("PurgeDomainButton", () => {
  it("purges exactly the query name", async () => {
    const bodies = mockPurge(() =>
      response({
        domain: "example.org.",
        subdomains: false,
        caches: 1,
        removed: 0,
      }),
    );
    render(<PurgeDomainButton domain="example.org." />);
    fireEvent.click(screen.getByRole("button", { name: "清除此域名缓存" }));
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toBe(
        "缓存中没有 example.org 的记录。",
      ),
    );
    expect(bodies).toEqual([{ domain: "example.org.", subdomains: false }]);
  });
});
