package coremain

import (
	"context"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/dnspolicy"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func policyBeforeWithTrace(engine *dnspolicy.Engine) func(context.Context, query_context.Principal, *dns.Msg) (*dns.Msg, query_context.ResponseTrace, error) {
	return func(ctx context.Context, principal query_context.Principal, request *dns.Msg) (*dns.Msg, query_context.ResponseTrace, error) {
		response, decision, err := engine.BeforeWithDecision(ctx, principal, request)
		return response, policyDecisionTrace(decision), err
	}
}

func policyAfterWithTrace(engine *dnspolicy.Engine) func(context.Context, query_context.Principal, *dns.Msg, *dns.Msg) (*dns.Msg, query_context.ResponseTrace, error) {
	return func(ctx context.Context, principal query_context.Principal, request, response *dns.Msg) (*dns.Msg, query_context.ResponseTrace, error) {
		result, decision, err := engine.AfterWithDecision(ctx, principal, request, response)
		return result, policyDecisionTrace(decision), err
	}
}

func policyDecisionTrace(decision dnspolicy.Decision) query_context.ResponseTrace {
	if decision.PublicListID != "" {
		return query_context.ResponseTrace{
			Source:              query_context.ResponseSourcePublicList,
			SourceID:            decision.PublicListID,
			MatchedPublicListID: decision.PublicListID,
		}
	}
	if decision.RuleID != "" {
		if decision.Action == control.DNSPolicyAllow {
			return query_context.ResponseTrace{MatchedRuleID: decision.RuleID}
		}
		source := query_context.ResponseSourceCustomBlock
		if decision.Action == control.DNSPolicyRewrite {
			source = query_context.ResponseSourceCustomRewrite
		}
		return query_context.ResponseTrace{
			Source:        source,
			SourceID:      decision.RuleID,
			MatchedRuleID: decision.RuleID,
		}
	}
	if decision.Action == control.DNSPolicyBlock {
		return query_context.ResponseTrace{Source: query_context.ResponseSourceCustomBlock}
	}
	return query_context.ResponseTrace{}
}
