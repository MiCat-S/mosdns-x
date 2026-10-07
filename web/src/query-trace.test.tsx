import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import {
  OutboundSection,
  expressionTokens,
  routeReason,
  routeSummary,
} from "./query-trace";
import type { QueryRecord } from "./types";

function record(patch: Partial<QueryRecord>): QueryRecord {
  return {
    id: "q",
    time: "2026-10-07T12:00:00Z",
    user_id: "u",
    credential_id: "c",
    client_ip: "192.0.2.1",
    name: "example.cn.",
    qtype: "A",
    rcode: "NOERROR",
    duration_ms: 67.2,
    cache_hit: false,
    protocol: "h2",
    answer_ips: ["192.0.2.10"],
    ...patch,
  };
}

describe("查询路径", () => {
  it("把条件表达式拆成 matcher 与运算符", () => {
    expect(
      expressionTokens(
        "(ecs_is_local) && (response_has_local_ip) || [response_server_failed]",
      ),
    ).toEqual([
      { kind: "matcher", name: "ecs_is_local" },
      { kind: "operator", text: "且" },
      { kind: "matcher", name: "response_has_local_ip" },
      { kind: "operator", text: "或" },
      { kind: "matcher", name: "response_server_failed" },
    ]);
    expect(expressionTokens("!query_is_ad_domain")).toEqual([
      { kind: "operator", text: "非" },
      { kind: "matcher", name: "query_is_ad_domain" },
    ]);
  });

  it("以最后进入的分支说明分流原因，并兼容旧记录", () => {
    const current = record({
      upstream_label: "223.5.5.5 (UDP)",
      trace: {
        steps: [
          {
            at_ms: 5.5,
            kind: "condition",
            detail: "ecs_is_lan",
            matched: false,
            misses: ["ecs_is_lan"],
            then: "else",
          },
          {
            at_ms: 5.6,
            kind: "condition",
            detail: "(query_is_local_domain) || (query_is_cdn_cn_domain)",
            matched: true,
            hits: ["query_is_local_domain"],
            then: "exec",
          },
        ],
        attempts: [
          {
            seq: 1,
            plugin: "forward_local",
            upstream: "223.5.5.5 (UDP)",
            start_ms: 5.6,
            duration_ms: 61.4,
            done: true,
            rcode: "NOERROR",
            selected: true,
          },
        ],
      },
    });
    expect(routeReason(current)).toBe("命中 query_is_local_domain");
    expect(routeSummary(current)).toBe(
      "命中 query_is_local_domain → 223.5.5.5 (UDP)，61 ms 取得结果",
    );

    const legacy = record({
      trace: {
        steps: [
          {
            at_ms: 1,
            kind: "if",
            detail: "query_is_cn_domain",
            hits: ["query_is_cn_domain"],
          },
        ],
        attempts: [],
      },
    });
    expect(routeReason(legacy)).toBe("命中 query_is_cn_domain");
    expect(routeReason(record({ trace: { steps: [], attempts: [] } }))).toBe(
      "未命中分流规则",
    );
  });

  it("没有可用结果时说明上游全部失败", () => {
    const failed = record({
      rcode: "SERVFAIL",
      response_source: "servfail",
      trace: {
        steps: [],
        attempts: [
          {
            seq: 1,
            upstream: "forward_remote #1 (DoH)",
            start_ms: 1,
            duration_ms: 2000,
            done: true,
            error: "timeout",
          },
        ],
      },
    });
    expect(routeSummary(failed)).toBe(
      "未命中分流规则，1 次上游请求均未取得可用结果",
    );
  });

  it("把连续未命中的规则折叠成一行，展开后可查看", () => {
    const miss = (detail: string, at: number) => ({
      at_ms: at,
      kind: "condition",
      detail,
      matched: false,
      misses: [detail],
      then: "continue" as const,
    });
    render(
      <OutboundSection
        record={record({
          upstream_label: "223.5.5.5 (UDP)",
          trace: {
            steps: [
              miss("query_is_cn_domain", 1),
              miss("query_is_noncn_domain", 1.1),
              miss("query_is_ad_domain", 1.2),
              {
                at_ms: 1.3,
                kind: "condition",
                detail: "ecs_is_lan",
                matched: false,
                misses: ["ecs_is_lan"],
                then: "else",
              },
              miss("response_has_gfw_ip", 9),
            ],
            attempts: [
              {
                seq: 1,
                plugin: "forward_local",
                upstream: "223.5.5.5 (UDP)",
                start_ms: 1.4,
                duration_ms: 7,
                done: true,
                rcode: "NOERROR",
                selected: true,
              },
            ],
          },
        })}
      />,
    );
    const flow = screen.getByRole("list", { name: "查询路径" });
    const nodes = within(flow).getAllByRole("listitem", { hidden: false });
    const top = nodes.filter((node) => node.parentElement === flow);
    // Received, three misses folded, the else branch, the request, the
    // single later miss, returned.
    expect(top).toHaveLength(6);
    expect(top[1]).toHaveTextContent("未命中 3 条规则");
    expect(top[2]).toHaveTextContent("ecs_is_lan否 · 进入 else 分支");
    expect(top[4]).toHaveTextContent("response_has_gfw_ip否 · 继续往下");
    fireEvent.click(within(top[1]).getByText("未命中 3 条规则"));
    expect(
      within(top[1]).getByLabelText("query_is_noncn_domain：未命中"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("figure", { name: "上游请求时间轴" }),
    ).not.toBeInTheDocument();
  });
});
