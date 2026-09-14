import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { HealthPanel, type HealthReport } from "./health-panel";

function report(): HealthReport {
  return {
    timestamp: "2026-09-14T12:00:00Z",
    started_at: "2026-09-14T11:00:00Z",
    storage_checked_at: "2026-09-14T12:00:00Z",
    storage_status: "healthy",
    runtime_status: "healthy",
    overall_status: "healthy",
    overall_score: 100,
    metrics: [
      {
        key: "session_cleanup",
        label: "会话清理失败率",
        value: null,
        unit: "%",
        status: "no_samples",
        reason: "no_samples",
      },
      {
        key: "db_connections",
        label: "控制库连接池占用",
        value: null,
        unit: "%",
        status: "not_applicable",
        reason: "bbolt_backend",
      },
      {
        key: "rate_limiter",
        label: "IP 限速器最高占用",
        value: 0,
        unit: "%",
        status: "healthy",
      },
      {
        key: "credential_count",
        label: "凭证计数不一致用户",
        value: 0,
        unit: "个",
        status: "healthy",
      },
    ],
    cleanup: { attempts: 0, errors: 0, duration_seconds_total: 0 },
    rate_limiters: [
      {
        name: "login",
        entries: 0,
        active_entries: 0,
        capacity: 4096,
        evicted_total: 0,
        rejected_total: 0,
      },
    ],
    storage: {
      driver: "bbolt",
      active_sessions: 2,
      credential_count_mismatches: 0,
      bbolt: { open_read_transactions: 0, pending_pages: 0 },
    },
  };
}
const response = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status });
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("管理员健康监控", () => {
  it("明确区分零、无样本与不适用，无样本不会阻断正常评分", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(report())));
    render(<HealthPanel />);
    expect(
      await screen.findByText(
        "尚无会话清理样本，不计算失败率，也不影响当前评分",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("0%")).toBeInTheDocument();
    expect(screen.getByText("不适用")).toBeInTheDocument();
    expect(screen.getByText("暂无样本")).toBeInTheDocument();
    expect(screen.getByText(/已观测项评分：100/)).toBeInTheDocument();
    expect(
      screen.getByRole("table", { name: "安全监控指标" }),
    ).toBeInTheDocument();
  });

  it("刷新失败时不保留之前的正常显示", async () => {
    const data = report();
    data.overall_status = "healthy";
    data.overall_score = 100;
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(response(data))
      .mockResolvedValueOnce(
        response({ error: { message: "监控服务暂不可用" } }, 503),
      );
    vi.stubGlobal("fetch", fetcher);
    render(<HealthPanel />);
    await screen.findByText(/已观测项评分：100/);
    fireEvent.click(screen.getByRole("button", { name: "立即刷新" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "监控服务暂不可用",
    );
    expect(screen.queryByText(/已观测项评分：100/)).not.toBeInTheDocument();
  });

  it("运行统计失效时不展示旧的连接池计数", async () => {
    const data = report();
    data.storage_status = "unknown";
    data.runtime_status = "unknown";
    data.overall_score = null;
    data.storage_error = "stale";
    data.storage = {
      driver: "mysql",
      active_sessions: 2,
      credential_count_mismatches: null,
      mysql: {
        max_open: 20,
        open: 5,
        in_use: 3,
        idle: 2,
        wait_count: 123,
        wait_seconds: 0,
        rollback_errors_total: 0,
      },
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(data)));
    render(<HealthPanel />);
    expect(await screen.findByText("运行统计不可用")).toBeInTheDocument();
    expect(screen.queryByText("累计等待 123 次")).not.toBeInTheDocument();
    expect(screen.queryByText("3 / 20")).not.toBeInTheDocument();
  });

  it("扫描失败仍显示独立连接池统计，累计错误只标为历史参考", async () => {
    const data = report();
    data.storage_status = "unknown";
    data.storage_error = "scan_limit";
    data.overall_status = "unknown";
    data.overall_score = null;
    data.storage = {
      driver: "mysql",
      active_sessions: null,
      credential_count_mismatches: null,
      mysql: {
        max_open: 20,
        open: 5,
        in_use: 3,
        idle: 2,
        wait_count: 123,
        wait_seconds: 2,
        rollback_errors_total: 1,
      },
    };
    data.metrics[0] = {
      key: "session_cleanup",
      label: "累计会话清理失败率",
      value: 100,
      unit: "%",
      status: "info",
      reason: "cumulative_only",
    };
    data.rate_limiters[0].entries = 3900;
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(data)));
    render(<HealthPanel />);
    expect(await screen.findByText("3 / 20")).toBeInTheDocument();
    expect(screen.getByText("累计等待 123 次")).toBeInTheDocument();
    expect(screen.getByText("历史参考")).toBeInTheDocument();
    expect(screen.getByText(/已观测项评分：—（指标不足）/)).toBeInTheDocument();
    expect(screen.getByText("0 / 4,096")).toBeInTheDocument();
    expect(screen.getByText("3,900")).toBeInTheDocument();
  });

  it("不会重叠轮询，卸载时取消正在进行的请求", async () => {
    vi.useFakeTimers();
    let finish!: (value: Response) => void;
    const fetcher = vi.fn().mockImplementation(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetcher);
    const view = render(<HealthPanel />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000);
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    await act(async () => {
      finish(response(report()));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(fetcher).toHaveBeenCalledTimes(2);
    const signal = fetcher.mock.calls[1][1].signal as AbortSignal;
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("后台标签页不发起请求，回到前台恢复读取", async () => {
    const visibility = vi
      .spyOn(document, "visibilityState", "get")
      .mockReturnValue("hidden");
    const fetcher = vi.fn().mockResolvedValue(response(report()));
    vi.stubGlobal("fetch", fetcher);
    render(<HealthPanel />);
    expect(fetcher).not.toHaveBeenCalled();
    visibility.mockReturnValue("visible");
    fireEvent(document, new Event("visibilitychange"));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  });

  it("快速切回前台时等待取消完成后继续读取，手动模式也不丢失刷新", async () => {
    const visibility = vi
      .spyOn(document, "visibilityState", "get")
      .mockReturnValue("visible");
    let rejectPending!: (error: DOMException) => void;
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(response(report()))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((_, reject) => {
            rejectPending = reject;
          }),
      )
      .mockResolvedValueOnce(response(report()));
    vi.stubGlobal("fetch", fetcher);
    render(<HealthPanel />);
    await screen.findByText(/已观测项评分/);
    fireEvent.click(screen.getByRole("button", { name: "自动刷新：开" }));
    expect(fetcher).toHaveBeenCalledTimes(2);
    visibility.mockReturnValue("hidden");
    fireEvent(document, new Event("visibilitychange"));
    expect(fetcher.mock.calls[1][1].signal.aborted).toBe(true);
    visibility.mockReturnValue("visible");
    fireEvent(document, new Event("visibilitychange"));
    expect(fetcher).toHaveBeenCalledTimes(2);
    await act(async () => {
      rejectPending(new DOMException("Canceled", "AbortError"));
    });
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3));
    expect(screen.getByRole("button", { name: "立即刷新" })).toBeEnabled();
  });
});
