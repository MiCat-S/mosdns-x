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
import { dateFormat, numberFormat, t, useLang } from "./i18n";

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

const chartMinute: Intl.DateTimeFormatOptions = {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
};
const axisMinute: Intl.DateTimeFormatOptions = {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
};

export function formatChartMinute(timestamp: number) {
  return dateFormat(chartMinute).format(new Date(timestamp));
}

export function formatAxisMinute(timestamp: number) {
  const parts = dateFormat(axisMinute, "zh-CN").formatToParts(
    new Date(timestamp),
  );
  const value = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((part) => part.type === type)?.value ?? "";
  return `${value("month")}/${value("day")}\n${value("hour")}:${value("minute")}`;
}

export function timeAxisBounds(from: string, to: string) {
  return { min: new Date(from).getTime(), max: new Date(to).getTime() };
}

// Light-mode values, used when the stylesheet is not loaded (tests).
const fallbackTheme = {
  accent: "rgb(173, 78, 45)",
  danger: "rgb(179, 32, 58)",
  success: "rgb(53, 114, 38)",
  muted: "rgb(107, 106, 101)",
  line: "rgba(20, 20, 19, 0.1)",
  lineStrong: "rgba(20, 20, 19, 0.18)",
  surface: "rgb(255, 255, 255)",
};
type ChartTheme = typeof fallbackTheme;

const themeTokens: Record<keyof ChartTheme, string> = {
  accent: "--accent",
  danger: "--danger",
  success: "--success",
  muted: "--text-3",
  line: "--line",
  lineStrong: "--line-strong",
  surface: "--surface",
};
// The tokens only change with the color scheme, so a fully resolved theme
// is kept per scheme.
const resolvedThemes = new Map<boolean, ChartTheme>();

// Resolves the color tokens through the browser so the chart gets plain
// rgb()/rgba() that ECharts can blend, in light and dark mode alike. All
// probes are added before any style is read, so the browser computes
// styles once instead of once per token.
export function chartTheme(dark = false): ChartTheme {
  const known = resolvedThemes.get(dark);
  if (known) return known;
  const keys = Object.keys(themeTokens) as (keyof ChartTheme)[];
  const probes = keys.map((key) => {
    const probe = document.createElement("span");
    probe.style.color = `var(${themeTokens[key]})`;
    probe.hidden = true;
    return probe;
  });
  document.body.append(...probes);
  const values = probes.map((probe) => getComputedStyle(probe).color);
  probes.forEach((probe) => probe.remove());
  const theme = { ...fallbackTheme };
  let resolved = true;
  keys.forEach((key, i) => {
    if (/^rgba?\(/.test(values[i])) theme[key] = values[i];
    else resolved = false;
  });
  // Without the stylesheet (tests, a slow load) the fallbacks are used but
  // not kept, so the real colors are picked up once they resolve.
  if (resolved) resolvedThemes.set(dark, theme);
  return theme;
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
        `${item.marker}${t("{series}：{count} 次", {
          series: item.seriesName,
          count: numberFormat().format(item.value[1]),
        })}`,
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

type ChartProps = {
  series: Point[];
  from: string;
  to: string;
  kind: "results" | "usage";
};

function chartOption(
  { series, from, to, kind }: ChartProps,
  dark: boolean,
): echarts.EChartsCoreOption {
  const theme = chartTheme(dark);
  const bounds = timeAxisBounds(from, to);
  const reduceMotion =
    window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  return {
    animation: !reduceMotion,
    animationDuration: reduceMotion ? 0 : 460,
    animationEasing: "cubicOut",
    tooltip: {
      trigger: "axis",
      formatter: tooltip,
      confine: true,
      backgroundColor: "rgba(20, 20, 19, 0.94)",
      borderColor: "rgba(255, 255, 255, 0.16)",
      borderWidth: 1,
      padding: [10, 12],
      textStyle: { color: "#faf9f5", fontSize: 12, lineHeight: 20 },
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
        ? [line(t("已受理"), theme.accent, theme.surface, series, "completed")]
        : [
            line(t("已完成"), theme.accent, theme.surface, series, "completed"),
            line(t("失败"), theme.danger, theme.surface, series, "failed"),
            line(
              t("缓存命中"),
              theme.success,
              theme.surface,
              series,
              "cache_hits",
            ),
          ],
  };
}

export default function UsageChart({ series, from, to, kind }: ChartProps) {
  const ref = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  const dark = useColorScheme();
  const lang = useLang();
  const hasData = series.length > 0;
  // One ECharts instance lives as long as its container: the container is
  // rendered only with data, so the instance is created when data arrives
  // and kept across refreshes, theme and language changes.
  useEffect(() => {
    const node = ref.current;
    if (!node) return;
    const instance = echarts.init(node);
    chart.current = instance;
    let frame = 0;
    // Resizes are coalesced to one per animation frame.
    const resize = () => {
      if (frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        instance.resize();
      });
    };
    // ResizeObserver also follows layout changes that don't resize the
    // window (sidebar, grid columns); jsdom and old browsers lack it.
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(resize);
    if (observer) observer.observe(node);
    else window.addEventListener("resize", resize);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", resize);
      cancelAnimationFrame(frame);
      instance.dispose();
      chart.current = null;
    };
  }, [hasData]);
  useEffect(() => {
    // replaceMerge drops series of a previous kind instead of merging them.
    chart.current?.setOption(chartOption({ series, from, to, kind }, dark), {
      replaceMerge: ["series"],
    });
  }, [hasData, series, from, to, kind, dark, lang]);
  return hasData ? (
    <div
      className="chart"
      ref={ref}
      role="img"
      aria-label={
        kind === "usage" ? t("DNS 已受理请求趋势图") : t("DNS 处理结果趋势图")
      }
    />
  ) : (
    <Empty>{t("当前窗口暂无统计数据")}</Empty>
  );
}
