package adaptive_ttl

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/matcher/domain"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

type execFunc func(context.Context, *query_context.Context, executable_seq.ExecutableChainNode) error

func (f execFunc) Exec(ctx context.Context, q *query_context.Context, next executable_seq.ExecutableChainNode) error {
	return f(ctx, q, next)
}

type harness struct {
	t   *testing.T
	p   *adaptiveTTL
	now time.Time
}

func newHarness(t *testing.T, args Args) *harness {
	t.Helper()
	p, err := newAdaptiveTTL(coremain.NewBP("adaptive", PluginType, nil, nil), &args)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, p: p, now: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
	p.now = func() time.Time { return h.now }
	return h
}

// reply describes the response the rest of the chain produces.
type reply struct {
	rcode int
	ips   []string
	stale bool
}

func answer(ips ...string) reply { return reply{rcode: dns.RcodeSuccess, ips: ips} }

// ask sends a query for name from subnet (empty for none) and returns the
// response TTL and the TTL recorded in the journal ("" when not lengthened).
func (h *harness) ask(name, subnet string, rep reply) (uint32, string) {
	h.t.Helper()
	q := new(dns.Msg).SetQuestion(name, dns.TypeA)
	if subnet != "" {
		dnsutils.AddECS(dnsutils.UpgradeEDNS0(q), dnsutils.NewEDNS0Subnet(net.ParseIP(subnet).To4(), 24, false), true)
	}
	journal := query_context.NewJournal(h.now)
	meta := query_context.NewRequestMeta(netip.Addr{})
	meta.SetJournal(journal)
	qCtx := query_context.NewContext(q, meta)
	next := executable_seq.WrapExecutable(execFunc(func(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
		r := new(dns.Msg)
		r.SetRcode(qCtx.Q(), rep.rcode)
		ttl := uint32(300)
		if rep.stale {
			ttl = 5
		}
		for _, ip := range rep.ips {
			rr, _ := dns.NewRR(name + " " + strconv.Itoa(int(ttl)) + " IN A " + ip)
			r.Answer = append(r.Answer, rr)
		}
		qCtx.SetResponse(r)
		qCtx.SetCacheHit(rep.stale)
		qCtx.SetStaleHit(rep.stale)
		return nil
	}))
	if err := h.p.Exec(context.Background(), qCtx, next); err != nil {
		h.t.Fatal(err)
	}
	detail := ""
	for _, step := range journal.Snapshot(0).Steps {
		if step.Kind == RouteStepTTLExtended {
			detail = step.Detail
		}
	}
	return dnsutils.GetMinimalTTL(qCtx.R()), detail
}

// every queries name each gap until d has passed and returns the last TTL.
func (h *harness) every(gap, d time.Duration, name, subnet string, rep func(i int) reply) uint32 {
	h.t.Helper()
	var ttl uint32
	for i, end := 0, h.now.Add(d); h.now.Before(end); i++ {
		ttl, _ = h.ask(name, subnet, rep(i))
		h.now = h.now.Add(gap)
	}
	return ttl
}

func same(rep reply) func(int) reply { return func(int) reply { return rep } }

func TestHotStableAnswersGetLongerTTLs(t *testing.T) {
	h := newHarness(t, Args{})
	ip := answer("192.0.2.1")
	if ttl := h.every(4*time.Minute, time.Hour, "hot.example.", "", same(ip)); ttl != 300 {
		t.Fatalf("TTL before the answer was stable for an hour = %d", ttl)
	}
	ttl, step := h.ask("hot.example.", "", ip)
	if ttl != 1800 || step != "1800" {
		t.Fatalf("after an hour: ttl=%d step=%q, want half the stable time", ttl, step)
	}
	h.every(4*time.Minute, time.Hour, "hot.example.", "", same(ip))
	if ttl, _ := h.ask("hot.example.", "", ip); ttl != 3600 {
		t.Fatalf("after two hours: ttl=%d, want max_ttl", ttl)
	}
	if ttl := h.every(4*time.Minute, 3*time.Hour, "hot.example.", "", same(ip)); ttl != 3600 {
		t.Fatalf("capped ttl=%d", ttl)
	}
	if got := h.p.ExtendedTTLs(); len(got) != 1 || got["hot.example."] != 3600 {
		t.Fatalf("ExtendedTTLs = %v", got)
	}
}

func TestChangingAnswersKeepTheirTTL(t *testing.T) {
	h := newHarness(t, Args{})
	rotating := func(i int) reply {
		if i%2 == 0 {
			return answer("192.0.2.1", "192.0.2.2")
		}
		return answer("192.0.2.3")
	}
	if ttl := h.every(4*time.Minute, 3*time.Hour, "cdn.example.", "", rotating); ttl != 300 {
		t.Fatalf("rotating answer ttl = %d", ttl)
	}
	reordered := func(i int) reply {
		if i%2 == 0 {
			return answer("192.0.2.1", "192.0.2.2")
		}
		return answer("192.0.2.2", "192.0.2.1")
	}
	// The last query comes 116 minutes after the first.
	if ttl := h.every(4*time.Minute, 2*time.Hour, "order.example.", "", reordered); ttl != 58*60 {
		t.Fatalf("reordered answer ttl = %d", ttl)
	}
	// A change after the TTL grew starts the learning over.
	if ttl, _ := h.ask("order.example.", "", answer("198.51.100.7")); ttl != 300 {
		t.Fatalf("ttl after the answer changed = %d", ttl)
	}
}

func TestRarelyQueriedNamesKeepTheirTTL(t *testing.T) {
	h := newHarness(t, Args{})
	if ttl := h.every(10*time.Minute, 3*time.Hour, "rare.example.", "", same(answer("192.0.2.1"))); ttl != 300 {
		t.Fatalf("six queries an hour got ttl %d", ttl)
	}
	if got := h.p.ExtendedTTLs(); len(got) != 0 {
		t.Fatalf("ExtendedTTLs = %v", got)
	}
}

func TestStaleAnswersAreNotLengthened(t *testing.T) {
	h := newHarness(t, Args{})
	ip := answer("192.0.2.1")
	h.every(4*time.Minute, 2*time.Hour, "hot.example.", "", same(ip))
	stale := ip
	stale.stale = true
	if ttl, step := h.ask("hot.example.", "", stale); ttl != 5 || step != "" {
		t.Fatalf("stale answer ttl=%d step=%q", ttl, step)
	}
	if ttl, _ := h.ask("hot.example.", "", ip); ttl != 3600 {
		t.Fatalf("fresh answer after a stale one ttl=%d", ttl)
	}
}

func TestHotNamesStayHotWhileTheLongerTTLKeepsClientsAway(t *testing.T) {
	h := newHarness(t, Args{})
	ip := answer("192.0.2.1")
	h.every(4*time.Minute, 2*time.Hour, "hot.example.", "", same(ip))
	// Clients now come back about once an hour, below min_queries.
	if ttl := h.every(55*time.Minute, 5*time.Hour, "hot.example.", "", same(ip)); ttl != 3600 {
		t.Fatalf("ttl with clients relying on the longer TTL = %d", ttl)
	}
	// Nobody asked for longer than a lengthened TTL lasts.
	h.now = h.now.Add(2*time.Hour + time.Minute)
	if ttl, _ := h.ask("hot.example.", "", ip); ttl != 300 {
		t.Fatalf("ttl after the name went idle = %d", ttl)
	}
}

func TestSubnetsAreLearnedApart(t *testing.T) {
	h := newHarness(t, Args{})
	var ttlA, ttlB uint32
	for end := h.now.Add(2 * time.Hour); h.now.Before(end); h.now = h.now.Add(4 * time.Minute) {
		ttlA, _ = h.ask("cdn.example.", "198.51.100.10", answer("192.0.2.1"))
		ttlB, _ = h.ask("cdn.example.", "203.0.113.20", answer("192.0.2.9"))
	}
	if ttlA <= 300 || ttlB <= 300 {
		t.Fatalf("per-subnet answers differ but each is stable: ttl %d and %d", ttlA, ttlB)
	}
	if len(h.p.keys) != 2 {
		t.Fatalf("keys = %d", len(h.p.keys))
	}
}

func TestOnlyAnswersAreLengthened(t *testing.T) {
	h := newHarness(t, Args{})
	nx := reply{rcode: dns.RcodeNameError}
	h.every(4*time.Minute, 2*time.Hour, "missing.example.", "", same(nx))
	if ttl, step := h.ask("missing.example.", "", nx); step != "" || ttl != 0 {
		t.Fatalf("NXDOMAIN lengthened: ttl=%d step=%q", ttl, step)
	}
	ip := answer("192.0.2.1")
	h.every(4*time.Minute, 2*time.Hour, "flaky.example.", "", func(i int) reply {
		if i%3 == 0 {
			return reply{rcode: dns.RcodeServerFailure}
		}
		return ip
	})
	if ttl, _ := h.ask("flaky.example.", "", ip); ttl <= 300 {
		t.Fatalf("SERVFAIL between identical answers reset the learning: ttl=%d", ttl)
	}
}

func TestExcludedNamesAreLeftAlone(t *testing.T) {
	h := newHarness(t, Args{})
	m := domain.NewDomainMixMatcher()
	if err := m.Add("domain:example.net", struct{}{}); err != nil {
		t.Fatal(err)
	}
	h.p.exclude = new(domain.MatcherGroup[struct{}])
	h.p.exclude.Append(m)
	if ttl := h.every(4*time.Minute, 3*time.Hour, "node.example.net.", "", same(answer("192.0.2.1"))); ttl != 300 {
		t.Fatalf("excluded name ttl = %d", ttl)
	}
	if len(h.p.keys) != 0 {
		t.Fatalf("excluded name tracked: %d keys", len(h.p.keys))
	}
}

func TestTrackedKeysAreBounded(t *testing.T) {
	h := newHarness(t, Args{Size: 2})
	ip := answer("192.0.2.1")
	h.ask("a.example.", "", ip)
	h.ask("b.example.", "", ip)
	h.ask("c.example.", "", ip)
	if len(h.p.keys) != 2 {
		t.Fatalf("keys = %d, want the limit", len(h.p.keys))
	}
	h.now = h.now.Add(2*time.Hour + time.Minute)
	h.ask("c.example.", "", ip)
	if _, ok := h.p.keys[key{name: "c.example.", qtype: dns.TypeA}]; !ok || len(h.p.keys) != 1 {
		t.Fatalf("idle keys not swept: %v", h.p.keys)
	}
}

func TestArgs(t *testing.T) {
	for _, args := range []Args{{MinQueries: -1}, {StableFor: -1}, {MaxTTL: -1}, {Size: -1}, {MaxTTL: 1 << 31}} {
		if _, err := newAdaptiveTTL(coremain.NewBP("adaptive", PluginType, nil, nil), &args); err == nil {
			t.Fatalf("accepted %+v", args)
		}
	}
	p, err := newAdaptiveTTL(coremain.NewBP("adaptive", PluginType, nil, nil), &Args{MinQueries: 3, StableFor: 600, MaxTTL: 900, Size: 10})
	if err != nil || p.minQueries != 3 || p.stableFor != 10*time.Minute || p.maxTTL != 900 || p.size != 10 {
		t.Fatalf("p=%+v err=%v", p, err)
	}
}
