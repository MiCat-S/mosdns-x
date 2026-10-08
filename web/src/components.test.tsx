import { render, screen } from "@testing-library/react";
import { useCallback } from "react";
import { describe, expect, it } from "vitest";
import { Metric, useLoad } from "./components";

function DeferredView({
  id,
  loaders,
}: {
  id: string;
  loaders: Record<string, Promise<string>>;
}) {
  const load = useCallback(() => loaders[id], [id, loaders]);
  const { data, loading } = useLoad(load, [load]);
  return <div>{loading ? "加载中" : data}</div>;
}

describe("异步页面数据", () => {
  it("切换目标时清除旧数据且忽略旧请求结果", async () => {
    let finishA!: (value: string) => void;
    let finishB!: (value: string) => void;
    const loaders = {
      a: new Promise<string>((resolve) => (finishA = resolve)),
      b: new Promise<string>((resolve) => (finishB = resolve)),
    };
    const view = render(<DeferredView id="a" loaders={loaders} />);
    view.rerender(<DeferredView id="b" loaders={loaders} />);
    expect(screen.getByText("加载中")).toBeInTheDocument();
    finishA("旧用户");
    await Promise.resolve();
    expect(screen.queryByText("旧用户")).not.toBeInTheDocument();
    finishB("新用户");
    expect(await screen.findByText("新用户")).toBeInTheDocument();
  });
});

describe("指标字号", () => {
  it("数字与短单位用大号数字，日期和文字降为正文字号", () => {
    const size = (value: string, size?: "num" | "text") => {
      const view = render(<Metric label="指标" value={value} size={size} />);
      const text = view.container
        .querySelector(".metric")
        ?.classList.contains("metric-text");
      view.unmount();
      return text ? "text" : "num";
    };
    expect(size("1,234")).toBe("num");
    expect(size("99.5%")).toBe("num");
    expect(size("12.3 ms")).toBe("num");
    expect(size("0.25 次/秒")).toBe("num");
    expect(size("5 / 10")).toBe("num");
    expect(size("2026年10月8日 12:00")).toBe("text");
    expect(size("已启用")).toBe("text");
    expect(size("未设置")).toBe("text");
    expect(size("2027/01/01 8:00")).toBe("text");
    expect(size("Oct 8, 2026, 1:30 PM")).toBe("text");
    expect(size("Enabled")).toBe("text");
    expect(size("mysql", "text")).toBe("text");
  });
});
