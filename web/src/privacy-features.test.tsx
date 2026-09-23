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
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
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
  });
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
        screen.getByRole("button", { name: "HTTPS" }).getAttribute("aria-pressed"),
      ).toBe("true"),
    );
  });

  it("已拦截的类型再次点击会移出列表", async () => {
    const calls = mockApi({ ...baseSettings, blocked_qtypes: ["HTTPS", "SVCB"] });
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
