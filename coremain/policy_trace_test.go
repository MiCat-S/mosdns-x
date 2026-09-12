package coremain

import (
	"testing"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/dnspolicy"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func TestPolicyDecisionTrace(t *testing.T) {
	tests := []struct {
		name     string
		decision dnspolicy.Decision
		want     query_context.ResponseTrace
	}{
		{name: "allow", decision: dnspolicy.Decision{Action: control.DNSPolicyAllow, RuleID: "rule-allow"}, want: query_context.ResponseTrace{MatchedRuleID: "rule-allow"}},
		{
			name:     "block rule",
			decision: dnspolicy.Decision{Action: control.DNSPolicyBlock, RuleID: "rule-1"},
			want:     query_context.ResponseTrace{Source: query_context.ResponseSourceCustomBlock, SourceID: "rule-1", MatchedRuleID: "rule-1"},
		},
		{
			name:     "rewrite rule",
			decision: dnspolicy.Decision{Action: control.DNSPolicyRewrite, RuleID: "rule-2"},
			want:     query_context.ResponseTrace{Source: query_context.ResponseSourceCustomRewrite, SourceID: "rule-2", MatchedRuleID: "rule-2"},
		},
		{
			name:     "public list",
			decision: dnspolicy.Decision{Action: control.DNSPolicyBlock, PublicListID: "list-1"},
			want:     query_context.ResponseTrace{Source: query_context.ResponseSourcePublicList, SourceID: "list-1", MatchedPublicListID: "list-1"},
		},
		{
			name:     "generic block",
			decision: dnspolicy.Decision{Action: control.DNSPolicyBlock},
			want:     query_context.ResponseTrace{Source: query_context.ResponseSourceCustomBlock},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policyDecisionTrace(tt.decision); got != tt.want {
				t.Fatalf("trace=%+v, want %+v", got, tt.want)
			}
		})
	}
}
