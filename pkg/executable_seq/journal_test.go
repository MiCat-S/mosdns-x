package executable_seq

import (
	"context"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func journalContext() (*query_context.Context, *query_context.Journal) {
	journal := query_context.NewJournal(time.Now())
	meta := query_context.NewRequestMeta(netip.Addr{})
	meta.SetJournal(journal)
	return query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), meta), journal
}

// answering records the branch it ran in as a stand-in upstream attempt.
type answering struct{ sleep time.Duration }

func (a answering) Exec(ctx context.Context, qCtx *query_context.Context, next ExecutableChainNode) error {
	if a.sleep > 0 {
		select {
		case <-time.After(a.sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	seq := qCtx.Journal().BeginAttempt(qCtx.Branch(), "forward", "223.5.5.5 (UDP)")
	qCtx.Journal().EndAttempt(seq, "NOERROR", "")
	r := new(dns.Msg)
	r.SetReply(qCtx.Q())
	qCtx.SetResponseWithTrace(r, query_context.ResponseTrace{Source: query_context.ResponseSourceUpstream, AttemptSeq: seq})
	return ExecChainNode(ctx, qCtx, next)
}

func TestJournalRecordsMatchedConditionAndItsMatchers(t *testing.T) {
	matchers := map[string]Matcher{
		"is_cn":    &DummyMatcher{Matched: false},
		"is_local": &DummyMatcher{Matched: true},
		"is_ad":    &DummyMatcher{Matched: false},
	}
	execs := map[string]Executable{"forward_local": answering{}}
	tree, err := BuildExecutableLogicTree([]interface{}{
		map[string]interface{}{"if": "is_ad", "exec": []interface{}{"forward_local"}},
		map[string]interface{}{"if": "is_cn || is_local", "exec": []interface{}{"forward_local"}},
	}, zap.NewNop(), execs, matchers)
	if err != nil {
		t.Fatal(err)
	}
	qCtx, journal := journalContext()
	if err := ExecChainNode(context.Background(), qCtx, tree); err != nil {
		t.Fatal(err)
	}
	trace := journal.Snapshot(qCtx.ResponseTrace().AttemptSeq)
	if len(trace.Steps) != 1 {
		t.Fatalf("steps = %+v, want only the matched condition", trace.Steps)
	}
	step := trace.Steps[0]
	if step.Kind != query_context.RouteStepIf || step.Detail != "is_cn || is_local" || !reflect.DeepEqual(step.Hits, []string{"is_local"}) {
		t.Fatalf("step = %+v", step)
	}
	if len(trace.Attempts) != 1 || !trace.Attempts[0].Selected || trace.Attempts[0].Upstream != "223.5.5.5 (UDP)" {
		t.Fatalf("attempts = %+v", trace.Attempts)
	}
}

func TestJournalRecordsFastFallbackBranches(t *testing.T) {
	fallback := &FallbackNode{
		primary:              WrapExecutable(answering{sleep: 300 * time.Millisecond}),
		secondary:            WrapExecutable(answering{}),
		fastFallbackDuration: 10 * time.Millisecond,
		logger:               zap.NewNop(),
	}
	qCtx, journal := journalContext()
	if err := fallback.exec(context.Background(), qCtx); err != nil {
		t.Fatal(err)
	}
	trace := journal.Snapshot(qCtx.ResponseTrace().AttemptSeq)
	var kinds []string
	for _, step := range trace.Steps {
		kinds = append(kinds, step.Kind+":"+step.Detail)
	}
	want := []string{
		query_context.RouteStepSecondaryStarted + ":fast_fallback",
		query_context.RouteStepBranchSelected + ":secondary",
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("steps = %v, want %v", kinds, want)
	}
	if len(trace.Attempts) != 1 || trace.Attempts[0].Branch != "secondary" || !trace.Attempts[0].Selected {
		t.Fatalf("attempts = %+v", trace.Attempts)
	}
}

func TestJournalNamesInPlacePrimaryBranch(t *testing.T) {
	fallback := &FallbackNode{
		primary:   WrapExecutable(answering{}),
		secondary: WrapExecutable(answering{}),
		logger:    zap.NewNop(),
	}
	qCtx, journal := journalContext()
	if err := fallback.exec(context.Background(), qCtx); err != nil {
		t.Fatal(err)
	}
	trace := journal.Snapshot(0)
	if len(trace.Attempts) != 1 || trace.Attempts[0].Branch != "primary" || qCtx.Branch() != "" {
		t.Fatalf("attempts = %+v branch after = %q", trace.Attempts, qCtx.Branch())
	}
}

func TestNoJournalRecordsNothing(t *testing.T) {
	qCtx := query_context.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), nil)
	node := &ConditionNode{ConditionMatcher: &DummyMatcher{Matched: true}, ExecutableNode: WrapExecutable(answering{}), Expr: "x"}
	if err := ExecChainNode(context.Background(), qCtx, node); err != nil || qCtx.R() == nil {
		t.Fatalf("err=%v response=%v", err, qCtx.R())
	}
}
