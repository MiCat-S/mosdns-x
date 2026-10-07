import { useEffect, useRef, useState } from "react";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import {
  GridComponent,
  LegendComponent,
  TooltipComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { Empty } from "./components";

echarts.use([
  LineChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  CanvasRenderer,
]);

type Point = {
  time: string;
  completed: number;
  failed: number;
  cache_hits: number;
};
type TooltipParam = {
  marker: string;
  seriesName: string;
  value: [number, number];
};

export function toTimedData(
  series: Point[],
  field: keyof Pick<Point, "completed" | "failed" | "cache_hits">,
) {
  return series.map(
    (point) =>
      [new Date(point.time).getTime(), point[field]] as [number, number],
  );
}

export function showPointSymbols(series: Point[]) {
  return series.length === 1;
}

export function formatChartMinute(timestamp: number) {
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(timestamp));
}

export function formatAxisMinute(timestamp: number) {
  const parts = new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).formatToParts(new Date(timestamp));
  const value = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((part) => part.type === type)?.value ?? "";
  return `${value("month")}/${value("day")}\n${value("hour")}:${value("minute")}`;
}

export function timeAxisBounds(from: string, to: string) {
  return { min: new Date(from).getTime(), max: new Date(to).getTime() };
}

// Light-mode values, used when the stylesheet is not loaded (tests).
const fallbackTheme = {
  accent: "rgb(0, 102, 204)",
  danger: "rgb(200, 16, 46)",
  success: "rgb(30, 123, 55)",
  muted: "rgb(110, 110, 115)",
  line: "rgba(0, 0, 0, 0.08)",
  lineStrong: "rgba(0, 0, 0, 0.16)",
  surface: "rgb(255, 255, 255)",
};
type ChartTheme = typeof fallbackTheme;

// Resolves a color token through the browser so the chart gets plain
// rgb()/rgba() that ECharts can blend, in light and dark mode alike.
function tokenColor(name: string, fallback: string) {
  const probe = document.createElement("span");
  probe.style.color = `var(${name})`;
  probe.hidden = true;
  document.body.append(probe);
  const value = getComputedStyle(probe).color;
  probe.remove();
  return /^rgba?\(/.test(value) ? value : fallback;
}

export function chartTheme(): ChartTheme {
  return {
    accent: tokenColor("--accent", fallbackTheme.accent),
    danger: tokenColor("--danger", fallbackTheme.danger),
    success: tokenColor("--success", fallbackTheme.success),
    muted: tokenColor("--text-3", fallbackTheme.muted),
    line: tokenColor("--line", fallbackTheme.line),
    lineStrong: tokenColor("--line-strong", fallbackTheme.lineStrong),
    surface: tokenColor("--surface", fallbackTheme.surface),
  };
}

export function withAlpha(color: string, alpha: number) {
  const m = color.match(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/);
  return m ? `rgba(${m[1]}, ${m[2]}, ${m[3]}, ${alpha})` : color;
}

function useColorScheme() {
  const query = "(prefers-color-scheme: dark)";
  const [dark, setDark] = useState(
    () => window.matchMedia?.(query).matches ?? false,
  );
  useEffect(() => {
    const media = window.matchMedia?.(query);
    if (!media) return;
    const change = () => setDark(media.matches);
    media.addEventListener?.("change", change);
    return () => media.removeEventListener?.("change", change);
  }, []);
  return dark;
}

function tooltip(params: TooltipParam[]) {
  if (!params.length) return "";
  return [
    formatChartMinute(params[0].value[0]),
    ...params.map(
      (item) =>
        `${item.marker}${item.seriesName}：${new Intl.NumberFormat("zh-CN").format(item.value[1])} 次`,
    ),
  ].join("<br/>");
}

function line(
  name: string,
  color: string,
  surface: string,
  series: Point[],
  field: keyof Pick<Point, "completed" | "failed" | "cache_hits">,
) {
  return {
    name,
    type: "line",
    smooth: false,
    connectNulls: false,
    showSymbol: showPointSymbols(series),
    symbol: "circle",
    symbolSize: 8,
    data: toTimedData(series, field),
    color,
    lineStyle: { width: 2, color },
    itemStyle: { color, borderColor: surface, borderWidth: 2 },
    areaStyle: {
      opacity: 1,
      color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
        { offset: 0, color: withAlpha(color, 0.14) },
        { offset: 1, color: withAlpha(color, 0) },
      ]),
    },
    emphasis: { focus: "series" },
  };
}

export default function UsageChart({
  series,
  from,
  to,
  kind,
}: {
  series: Point[];
  from: string;
  to: string;
  kind: "results" | "usage";
}) {
  const ref = useRef<HTMLDivElement>(null);
  const dark = useColorScheme();
  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current);
    const theme = chartTheme();
    const resultSeries = [
      line("已完成", theme.accent, theme.surface, series, "completed"),
      line("失败", theme.danger, theme.surface, series, "failed"),
      line("缓存命中", theme.success, theme.surface, series, "cache_hits"),
    ];
    const bounds = timeAxisBounds(from, to);
    const reduceMotion =
      window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
    chart.setOption({
      animation: !reduceMotion,
      animationDuration: reduceMotion ? 0 : 460,
      animationEasing: "cubicOut",
      tooltip: {
        trigger: "axis",
        formatter: tooltip,
        confine: true,
        backgroundColor: "rgba(29, 29, 31, 0.94)",
        borderColor: "rgba(255, 255, 255, 0.16)",
        borderWidth: 1,
        padding: [10, 12],
        textStyle: { color: "#f5f5f7", fontSize: 12, lineHeight: 20 },
        extraCssText: "border-radius:8px;box-shadow:0 8px 24px rgba(0,0,0,.18)",
        axisPointer: {
          type: "line",
          lineStyle: { color: theme.lineStrong, type: "dashed", width: 1 },
        },
      },
      legend: {
        top: 0,
        left: 0,
        icon: "circle",
        itemWidth: 8,
        itemHeight: 8,
        itemGap: 20,
        textStyle: { color: theme.muted, fontSize: 12 },
      },
      grid: { left: 48, right: 16, top: 36, bottom: 40 },
      xAxis: {
        type: "time",
        min: bounds.min,
        max: bounds.max,
        axisLine: { lineStyle: { color: theme.lineStrong } },
        axisTick: { show: false },
        axisLabel: {
          formatter: (value: number) => formatAxisMinute(value),
          hideOverlap: true,
          margin: 12,
          color: theme.muted,
          fontSize: 11,
          lineHeight: 16,
        },
        splitLine: { show: false },
        silent: true,
      },
      yAxis: {
        type: "value",
        minInterval: 1,
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: { color: theme.muted, fontSize: 11 },
        splitLine: { lineStyle: { color: theme.line, width: 1 } },
      },
      series:
        kind === "usage"
          ? [line("已受理", theme.accent, theme.surface, series, "completed")]
          : resultSeries,
    });
    const resize = () => chart.resize();
    window.addEventListener("resize", resize);
    return () => {
      window.removeEventListener("resize", resize);
      chart.dispose();
    };
  }, [series, from, to, kind, dark]);
  return series.length ? (
    <div
      className="chart"
      ref={ref}
      role="img"
      aria-label={
        kind === "usage" ? "DNS 已受理请求趋势图" : "DNS 处理结果趋势图"
      }
    />
  ) : (
    <Empty>当前窗口暂无统计数据</Empty>
  );
}
