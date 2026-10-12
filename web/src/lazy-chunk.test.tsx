import { afterEach, describe, expect, it, vi } from "vitest";
import { loadChunk, reloadForNewBuild } from "./lazy-chunk";

afterEach(() => sessionStorage.clear());

describe("reloadForNewBuild", () => {
  it("reloads once, then not again within the window", () => {
    const reload = vi.fn();
    expect(reloadForNewBuild(reload)).toBe(true);
    expect(reloadForNewBuild(reload)).toBe(false);
    expect(reload).toHaveBeenCalledTimes(1);
  });
});

describe("loadChunk", () => {
  it("passes a loaded module through", async () => {
    await expect(loadChunk(async () => ({ x: 1 }))).resolves.toEqual({ x: 1 });
  });
  it("rethrows when a reload already happened", async () => {
    sessionStorage.setItem("mosdns.chunk-reload", String(Date.now()));
    const failure = new TypeError(
      "Failed to fetch dynamically imported module",
    );
    await expect(loadChunk(() => Promise.reject(failure))).rejects.toBe(
      failure,
    );
  });
});
