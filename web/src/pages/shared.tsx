import { useCallback, useEffect, useRef, useState } from "react";
import { decodePage, message, request } from "../api";
import type { Stats, UsagePoint } from "../types";

// Helpers shared by the admin and user pages. Each page module imports
// what it needs from here, so a route's chunk carries only its own pages.

// range is the default stats window: the past 24 hours.
export const range = () => {
  const to = new Date(),
    from = new Date(to.getTime() - 24 * 60 * 60 * 1000);
  return { from: from.toISOString(), to: to.toISOString() };
};
export const query = (path: string, params: Record<string, string>) =>
  `${path}${path.includes("?") ? "&" : "?"}${new URLSearchParams(params)}`;
export function toLocalDateTime(value: string) {
  if (!value || value.startsWith("0001-")) return "";
  const d = new Date(value);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
export function fromLocalDateTime(value: string) {
  return value ? new Date(value).toISOString() : "0001-01-01T00:00:00Z";
}
export function usageSummary(points: UsagePoint[]) {
  const byMinute = new Map<number, number>();
  let total = 0;
  for (const p of points) {
    total += p.count;
    const minute = Math.floor(new Date(p.minute).getTime() / 60000) * 60000;
    byMinute.set(minute, (byMinute.get(minute) ?? 0) + p.count);
  }
  const series = [...byMinute.entries()]
    .sort(([a], [b]) => a - b)
    .map(([time, count]) => ({
      time: new Date(time).toISOString(),
      completed: count,
      failed: 0,
      cache_hits: 0,
    }));
  const previousMinute = Math.floor(Date.now() / 60000) * 60000 - 60000;
  const recent = byMinute.get(previousMinute) ?? 0;
  return { total, series, recentPerSecond: recent / 60 };
}
export function successRate(stats: Pick<Stats, "completed" | "failed">) {
  return stats.completed
    ? Math.max(0, stats.completed - stats.failed) / stats.completed
    : 0;
}

export function usePaged<T>(path: string, version = 0, enabled = true) {
  const [items, setItems] = useState<T[]>([]);
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const load = useCallback(
    async (next = "") => {
      const current = ++generation.current;
      controller.current?.abort();
      controller.current = new AbortController();
      setLoading(true);
      setError("");
      try {
        const params = new URLSearchParams({ limit: "100" });
        if (next) params.set("cursor", next);
        const page = await request(
          `${path}${path.includes("?") ? "&" : "?"}${params}`,
          { signal: controller.current.signal },
          decodePage<T>,
        );
        if (generation.current === current) {
          setItems((old) => (next ? [...old, ...page.items] : page.items));
          setCursor(page.next_cursor ?? "");
        }
      } catch (e) {
        if (generation.current === current) setError(message(e));
      } finally {
        if (generation.current === current) setLoading(false);
      }
    },
    [path],
  );
  useEffect(() => {
    setItems([]);
    if (enabled) void load();
    else setLoading(false);
    return () => {
      controller.current?.abort();
      generation.current++;
    };
  }, [load, version, enabled]);
  return { items, cursor, loading, error, more: () => load(cursor) };
}
