import { describe, expect, it } from "vitest";

import { expressionTokens, routeReason, routeSummary } from "./query-trace";
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
});
