package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func TestQueryRecordsKeepUpstreamLabelAndTrace(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	clock := now
	s := openClocked(t, &clock, 0)

	matched := true
	answered := result("u1", "c1", dns.RcodeSuccess)
	answered.UpstreamLabel = "223.5.5.5 (UDP)"
	answered.Trace = &query_context.QueryTrace{
		Steps:    []query_context.RouteStep{{Kind: query_context.RouteStepCondition, Detail: "query_is_cn_domain", Hits: []string{"query_is_cn_domain"}, Matched: &matched, Then: query_context.ConditionThenExec}},
		Attempts: []query_context.UpstreamTry{{Seq: 1, Plugin: "forward_local", Upstream: "223.5.5.5 (UDP)", Done: true, Rcode: "NOERROR", Selected: true}},
	}
	cached := result("u1", "c1", dns.RcodeSuccess)
	cached.CacheHit = true
	cached.ResponseSource, cached.UpstreamID = query_context.ResponseSourceCache, ""
	cached.UpstreamLabel = "forward_easymosdns #1 (DoH)"
	if err := s.writeBatch([]event{{time: now, result: &answered}, {time: now.Add(time.Second), result: &cached}}); err != nil {
		t.Fatal(err)
	}

	page, err := s.Queries(context.Background(), "u1", now.Add(-time.Minute), now.Add(time.Minute), QueryFilter{}, Page{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	byLabel := map[string]QueryRecord{}
	for _, item := range page.Items {
		byLabel[item.UpstreamLabel] = item
	}
	got := byLabel["223.5.5.5 (UDP)"]
	if got.Trace == nil || len(got.Trace.Attempts) != 1 || !got.Trace.Attempts[0].Selected || got.Trace.Steps[0].Detail != "query_is_cn_domain" {
		t.Fatalf("answered record=%+v", got)
	}
	if hit := byLabel["forward_easymosdns #1 (DoH)"]; !hit.CacheHit {
		t.Fatalf("cache hit record=%+v", hit)
	}

	filtered, err := s.Queries(context.Background(), "u1", now.Add(-time.Minute), now.Add(time.Minute), QueryFilter{UpstreamLabel: "forward_easymosdns #1 (DoH)"}, Page{Limit: 10})
	if err != nil || len(filtered.Items) != 1 || !filtered.Items[0].CacheHit {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}
}
