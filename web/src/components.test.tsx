import { render, screen } from "@testing-library/react";
import { useCallback } from "react";
import { describe, expect, it } from "vitest";
import { useLoad } from "./components";

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
