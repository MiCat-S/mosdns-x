package query_context

import (
	"math"
	"sync"
	"time"
)

// Journal limits keep a looping or fanned-out sequence from growing one
// query's record without bound.
const (
	maxJournalSteps    = 32
	maxJournalAttempts = 32
)

// Route step kinds recorded by the executable sequence.
const (
	RouteStepCondition         = "condition"
	RouteStepCacheHit          = "cache_hit"
	RouteStepLazyRefresh       = "lazy_refresh"
	RouteStepSecondaryStarted  = "secondary_started"
	RouteStepBranchSelected    = "branch_selected"
	RouteStepPrimaryUnhealthy  = "primary_unhealthy"
	RouteStepLoadBalanceChosen = "load_balance"
)

// RouteStep is one routing decision taken while answering a query.
type RouteStep struct {
	AtMS   float64  `json:"at_ms"`
	Branch string   `json:"branch,omitempty"`
	Kind   string   `json:"kind"`
	Detail string   `json:"detail,omitempty"`
	Hits   []string `json:"hits,omitempty"`
	// The fields below describe a condition step: whether it matched, the
	// matchers that evaluated false (others in Detail were not evaluated),
	// and what ran next: "exec", "else" or "continue".
	Matched *bool    `json:"matched,omitempty"`
	Misses  []string `json:"misses,omitempty"`
	Then    string   `json:"then,omitempty"`
}

// Condition outcomes recorded in RouteStep.Then.
const (
	ConditionThenExec     = "exec"
	ConditionThenElse     = "else"
	ConditionThenContinue = "continue"
)

// UpstreamTry is one exchange with an upstream. Upstream is a display name
// that never carries a private server's host or IP.
type UpstreamTry struct {
	Seq        int     `json:"seq"`
	Branch     string  `json:"branch,omitempty"`
	Plugin     string  `json:"plugin,omitempty"`
	Upstream   string  `json:"upstream"`
	StartMS    float64 `json:"start_ms"`
	DurationMS float64 `json:"duration_ms"`
	Done       bool    `json:"done"`
	Rcode      string  `json:"rcode,omitempty"`
	Error      string  `json:"error,omitempty"`
	Selected   bool    `json:"selected,omitempty"`
}

// QueryTrace is the routing path and upstream attempts of one query.
type QueryTrace struct {
	Steps     []RouteStep   `json:"steps"`
	Attempts  []UpstreamTry `json:"attempts"`
	Truncated bool          `json:"truncated,omitempty"`
}

// Journal collects a query's routing decisions and upstream attempts. One
// journal is shared by every branch copy of the query, so it is safe for
// concurrent use. A nil *Journal records nothing.
type Journal struct {
	mu        sync.Mutex
	start     time.Time
	steps     []RouteStep
	attempts  []UpstreamTry
	truncated bool
}

func NewJournal(start time.Time) *Journal {
	return &Journal{start: start}
}

func (j *Journal) since() float64 {
	return float64(time.Since(j.start).Microseconds()) / 1000
}

// roundMS keeps three decimals, the microsecond precision the times have.
func roundMS(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// Step records a routing decision.
func (j *Journal) Step(branch, kind, detail string, hits ...string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.steps) >= maxJournalSteps {
		j.truncated = true
		return
	}
	j.steps = append(j.steps, RouteStep{AtMS: j.since(), Branch: branch, Kind: kind, Detail: detail, Hits: append([]string(nil), hits...)})
}

// Condition records an evaluated sequence condition, matched or not.
func (j *Journal) Condition(branch, expr string, matched bool, hits, misses []string, then string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.steps) >= maxJournalSteps {
		j.truncated = true
		return
	}
	j.steps = append(j.steps, RouteStep{
		AtMS: j.since(), Branch: branch, Kind: RouteStepCondition, Detail: expr,
		Hits: append([]string(nil), hits...), Misses: append([]string(nil), misses...),
		Matched: &matched, Then: then,
	})
}

// BeginAttempt records an exchange that is starting and returns its sequence
// number, or 0 when nothing was recorded.
func (j *Journal) BeginAttempt(branch, plugin, upstream string) int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.attempts) >= maxJournalAttempts {
		j.truncated = true
		return 0
	}
	seq := len(j.attempts) + 1
	j.attempts = append(j.attempts, UpstreamTry{Seq: seq, Branch: branch, Plugin: plugin, Upstream: upstream, StartMS: j.since()})
	return seq
}

// EndAttempt records how attempt seq ended. errKind is an error category,
// never the raw error, which can name the server.
func (j *Journal) EndAttempt(seq int, rcode, errKind string) {
	if j == nil || seq <= 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if seq > len(j.attempts) {
		return
	}
	a := &j.attempts[seq-1]
	a.DurationMS = roundMS(j.since() - a.StartMS)
	a.Done = true
	a.Rcode = rcode
	a.Error = errKind
}

// Snapshot copies the journal, marking attempt selected as the one whose
// response was returned. Attempts still running are reported as unfinished.
func (j *Journal) Snapshot(selected int) *QueryTrace {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := &QueryTrace{
		Steps:     make([]RouteStep, len(j.steps)),
		Attempts:  make([]UpstreamTry, len(j.attempts)),
		Truncated: j.truncated,
	}
	copy(out.Steps, j.steps)
	copy(out.Attempts, j.attempts)
	for i := range out.Attempts {
		if out.Attempts[i].Seq == selected {
			out.Attempts[i].Selected = true
		}
		if !out.Attempts[i].Done {
			out.Attempts[i].DurationMS = roundMS(j.since() - out.Attempts[i].StartMS)
		}
	}
	return out
}
