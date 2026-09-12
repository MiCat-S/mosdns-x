import { useEffect, useRef } from "react";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import {
  GridComponent,
  LegendComponent,
  TooltipComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

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
    lineStyle: { width: 2.25, color },
    itemStyle: {
      color,
      borderColor: "#ffffff",
      borderWidth: 2,
      shadowBlur: 8,
      shadowColor: `${color}4d`,
    },
    areaStyle: {
      opacity: 1,
      color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
        { offset: 0, color: `${color}22` },
        { offset: 1, color: `${color}00` },
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
  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current);
    const resultSeries = [
      line("已完成", "#0071e3", series, "completed"),
      line("失败", "#ff3b30", series, "failed"),
      line("缓存命中", "#34c759", series, "cache_hits"),
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
        extraCssText:
          "border-radius:12px;box-shadow:0 18px 48px rgba(0,0,0,.2);backdrop-filter:blur(14px)",
        axisPointer: {
          type: "line",
          lineStyle: { color: "#8e8e93", type: "dashed", width: 1 },
        },
      },
      legend: {
        bottom: 0,
        icon: "circle",
        itemWidth: 8,
        itemHeight: 8,
        itemGap: 22,
        textStyle: { color: "#6e6e73", fontSize: 12 },
      },
      grid: { left: 52, right: 20, top: 22, bottom: 58 },
      xAxis: {
        type: "time",
        min: bounds.min,
        max: bounds.max,
        axisLine: { lineStyle: { color: "#d1d1d6" } },
        axisTick: { show: false },
        axisLabel: {
          formatter: (value: number) => formatAxisMinute(value),
          hideOverlap: true,
          margin: 12,
          color: "#6e6e73",
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
        axisLabel: { color: "#6e6e73", fontSize: 11 },
        splitLine: { lineStyle: { color: "#e5e5ea", width: 1 } },
      },
      series:
        kind === "usage"
          ? [line("已受理", "#0071e3", series, "completed")]
          : resultSeries,
    });
    const resize = () => chart.resize();
    window.addEventListener("resize", resize);
    return () => {
      window.removeEventListener("resize", resize);
      chart.dispose();
    };
  }, [series, from, to, kind]);
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
    <div className="empty">当前窗口暂无统计数据</div>
  );
}
