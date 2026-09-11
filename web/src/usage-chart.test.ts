import { describe, expect, it } from "vitest";
import {
  formatAxisMinute,
  formatChartMinute,
  showPointSymbols,
  timeAxisBounds,
  toTimedData,
} from "./usage-chart";

const point = (time: string, completed: number) => ({
  time,
  completed,
  failed: 0,
  cache_hits: 0,
});

describe("用量图时间数据", () => {
  it("单点数据启用可见标记", () => {
    expect(showPointSymbols([point("2026-09-11T08:00:00Z", 3)])).toBe(true);
    expect(
      showPointSymbols([
        point("2026-09-11T08:00:00Z", 3),
        point("2026-09-11T08:01:00Z", 1),
      ]),
    ).toBe(false);
  });

  it("稀疏数据保留真实时间间距且格式包含日期和分钟", () => {
    const data = toTimedData(
      [point("2026-09-10T23:58:00Z", 3), point("2026-09-11T08:02:00Z", 5)],
      "completed",
    );
    expect(data[1][0] - data[0][0]).toBe(8 * 60 * 60 * 1000 + 4 * 60 * 1000);
    expect(data.map((item) => item[1])).toEqual([3, 5]);
    expect(formatChartMinute(data[0][0])).toMatch(/2026.*09.*(?:10|11).*58/);
  });

  it("时间轴严格使用查询窗口而不按单点自动扩展", () => {
    const from = "2026-09-10T08:39:00Z";
    const to = "2026-09-11T08:39:00Z";
    expect(timeAxisBounds(from, to)).toEqual({
      min: Date.parse(from),
      max: Date.parse(to),
    });
    expect(formatAxisMinute(Date.parse(from))).toMatch(
      /\d{2}\/\d{2}\n\d{2}:\d{2}/,
    );
  });
});
