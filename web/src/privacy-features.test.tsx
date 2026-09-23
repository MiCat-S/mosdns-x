import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PrivacyPage } from "./pages";

type Call = { url: string; method: string; body: unknown };

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const baseSettings = {
  user_id: "u1",
  strip_ecs: false,
  block_private_answers: false,
  blocked_qtypes: [] as string[],
  custom_block_enabled: true,
  custom_allow_enabled: true,
  custom_rewrite_enabled: true,
  policy_paused_until: null,
  updated_at: "2026-09-22T00:00:00Z",
};

// Routes by URL and method rather than call order, so a page that loads an
// extra resource does not shift every later assertion.
function mockApi(settings = baseSettings, lists: unknown[] = []) {
  const calls: Call[] = [];
  let current = { ...settings };
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ url, method, body });
      if (url.includes("/me/settings")) {
        if (method === "PATCH") current = { ...current, ...(body as object) };
        return json(current);
      }
      if (url.includes("/me/public-lists")) {
        return json({ items: lists, next_cursor: "" });
      }
      return json({ error: { code: "not_found", message: "" } }, 404);
    },
  );
  vi.stubGlobal("fetch", fetcher);
  return calls;
}

beforeEach(() => vi.restoreAllMocks());
afterEach(() => vi.unstubAllGlobals());

describe("查询类型拦截", () => {
  it("提供 HTTPS 与 SVCB，点击后加入拦截列表", async () => {
    const calls = mockApi();
    render(<PrivacyPage />);
    const https = await screen.findByRole("button", { name: "HTTPS" });
    expect(screen.getByRole("button", { name: "SVCB" })).toBeTruthy();
    expect(https.getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(https);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.body).toEqual({ blocked_qtypes: ["HTTPS"] });
    await waitFor(() =>
      expect(
        screen
          .getByRole("button", { name: "HTTPS" })
          .getAttribute("aria-pressed"),
      ).toBe("true"),
    );
  });

  it("已拦截的类型再次点击会移出列表", async () => {
    const calls = mockApi({
      ...baseSettings,
      blocked_qtypes: ["HTTPS", "SVCB"],
    });
    render(<PrivacyPage />);
    const svcb = await screen.findByRole("button", { name: "SVCB" });
    expect(svcb.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(svcb);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      blocked_qtypes: ["HTTPS"],
    });
  });
});

function list(
  id: string,
  category: string,
  opts: { enabled?: boolean; published?: boolean } = {},
) {
  return {
    list: {
      id,
      name: id,
      category,
      format: "domains",
      enabled: true,
      default_enabled: false,
      published: opts.published ?? true,
      refresh_seconds: 3600,
      entry_count: 10,
      last_refresh_status: "success",
      snapshot_status: "current",
      created_at: "2026-09-22T00:00:00Z",
      updated_at: "2026-09-22T00:00:00Z",
    },
    enabled: opts.enabled ?? false,
    overridden: false,
  };
}

describe("恶意域名情报", () => {
  it("没有该分类的列表时保持禁用并说明原因", async () => {
    mockApi(baseSettings, [list("ads", "广告与追踪")]);
    render(<PrivacyPage />);
    const row = await screen.findByRole("checkbox", { name: "恶意域名情报" });
    await waitFor(() => expect((row as HTMLInputElement).disabled).toBe(true));
    expect(
      screen.getByText(/管理员发布「恶意域名」分类的公共列表后可用/),
    ).toBeTruthy();
  });

  it("开启时只启用已发布的恶意域名列表", async () => {
    const calls = mockApi(baseSettings, [
      list("malware", "恶意域名"),
      list("phishing", " 恶意域名 "),
      list("draft", "恶意域名", { published: false }),
      list("ads", "广告与追踪"),
    ]);
    render(<PrivacyPage />);
    const row = (await screen.findByRole("checkbox", {
      name: "恶意域名情报",
    })) as HTMLInputElement;
    await waitFor(() => expect(row.disabled).toBe(false));
    expect(row.checked).toBe(false);
    expect(screen.getByText(/2 个情报源/)).toBeTruthy();

    fireEvent.click(row);
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2),
    );
    const patched = calls
      .filter((c) => c.method === "PATCH")
      .map((c) => [c.url.split("/me/public-lists/")[1], c.body]);
    expect(patched).toEqual([
      ["malware", { enabled: true }],
      ["phishing", { enabled: true }],
    ]);
    await waitFor(() => expect(row.checked).toBe(true));
  });

  it("全部已启用时显示开启，关闭会逐个停用", async () => {
    const calls = mockApi(baseSettings, [
      list("malware", "恶意域名", { enabled: true }),
      list("phishing", "恶意域名", { enabled: true }),
    ]);
    render(<PrivacyPage />);
    const row = (await screen.findByRole("checkbox", {
      name: "恶意域名情报",
    })) as HTMLInputElement;
    await waitFor(() => expect(row.checked).toBe(true));
    fireEvent.click(row);
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2),
    );
    expect(
      calls
        .filter((c) => c.method === "PATCH")
        .every((c) => (c.body as { enabled: boolean }).enabled === false),
    ).toBe(true);
  });

  it("部分启用时视为关闭，开启只补上未启用的", async () => {
    const calls = mockApi(baseSettings, [
      list("malware", "恶意域名", { enabled: true }),
      list("phishing", "恶意域名", { enabled: false }),
    ]);
    render(<PrivacyPage />);
    const row = (await screen.findByRole("checkbox", {
      name: "恶意域名情报",
    })) as HTMLInputElement;
    await waitFor(() => expect(row.disabled).toBe(false));
    expect(row.checked).toBe(false);
    fireEvent.click(row);
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1),
    );
    expect(calls.find((c) => c.method === "PATCH")?.url).toContain(
      "/me/public-lists/phishing",
    );
  });
});
