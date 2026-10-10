// Package adaptive_ttl lengthens the TTL that clients see for names they
// query over and over while the answer stays the same.
//
// It learns per query key (name, type and ECS subnet, as the cache keys
// them) how often the key is queried and since when its answer has been
// unchanged. A key queried at least min_queries times in an hour is hot; a
// hot key whose answer has not changed for stable_for gets a TTL of half
// that stable time, up to max_ttl. A changed answer resets the stable time,
// so names that rotate addresses (CDNs, load balancers, failover) are never
// lengthened. When domains is set, only names it matches are learned, so the
// longer TTLs can be kept to large providers whose addresses rarely move.
// Such providers often rotate answers within a pool of addresses that all
// keep working; with trust_answers their hot keys are lengthened by how long
// they have been hot instead, whatever the answer.
// Only the TTL sent to the client changes: caches after this plugin in the
// chain keep the upstream TTL and refresh on it.
package adaptive_ttl

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/matcher/domain"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

const PluginType = "adaptive_ttl"

// RouteStepTTLExtended is the journal step recorded when a response's TTL
// was lengthened; its detail is the new TTL in seconds.
const RouteStepTTLExtended = "ttl_extended"

const (
	window            = time.Hour
	defaultMinQueries = 12
	defaultStableFor  = time.Hour
	defaultMaxTTL     = 3600
	defaultSize       = 65536
	sweepInterval     = time.Minute
)

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() interface{} { return new(Args) })
}

var _ coremain.ExecutablePlugin = (*adaptiveTTL)(nil)

type Args struct {
	// Queries in the last hour that make a key hot. Default 12, about one
	// every five minutes.
	MinQueries int64 `yaml:"min_queries"`
	// Seconds the answer must stay the same before its TTL is lengthened.
	// Default 3600.
	StableFor int64 `yaml:"stable_for"`
	// Upper bound of a lengthened TTL in seconds. Default 3600.
	MaxTTL int64 `yaml:"max_ttl"`
	// Keys tracked at most. Default 65536.
	Size int64 `yaml:"size"`
	// Only names matching these are learned and lengthened, in the domain
	// matcher syntax (provider: included). Empty means every name.
	Domains []string `yaml:"domains"`
	// Names never lengthened, in the domain matcher syntax.
	Exclude []string `yaml:"exclude"`
	// Lengthen hot keys without requiring the answer to stay the same; the
	// TTL grows with how long the key has been hot. Requires domains.
	TrustAnswers bool `yaml:"trust_answers"`
	// Also lengthen NOERROR responses without answers, including the
	// negative caching time of their SOA.
	ExtendEmpty bool `yaml:"extend_empty"`
}

type key struct {
	name   string
	qtype  uint16
	subnet netip.Prefix
}

type entry struct {
	windowStart       time.Time
	current, previous uint32 // queries in this and the previous window
	hot               bool
	hotSince          time.Time
	lastSeen          time.Time
	answer            uint64 // fingerprint of the last fresh response
	stableSince       time.Time
	extendable        bool // the last fresh response may be lengthened
}

type adaptiveTTL struct {
	*coremain.BP
	minQueries float64
	stableFor  time.Duration
	maxTTL     uint32
	size       int
	trust      bool
	empty      bool
	domains    *domain.MatcherGroup[struct{}]
	exclude    *domain.MatcherGroup[struct{}]
	now        func() time.Time

	mu        sync.Mutex
	keys      map[key]*entry
	lastSweep time.Time

	extendedTotal prometheus.Counter
	tracked       prometheus.GaugeFunc
}

func Init(bp *coremain.BP, args interface{}) (coremain.Plugin, error) {
	a := args.(*Args)
	p, err := newAdaptiveTTL(bp, a)
	if err != nil {
		return nil, err
	}
	if len(a.Domains) > 0 {
		mg, err := domain.BatchLoadDomainProvider(a.Domains, bp.M().GetDataManager())
		if err != nil {
			return nil, fmt.Errorf("domains: %w", err)
		}
		p.domains = mg
	}
	if len(a.Exclude) > 0 {
		mg, err := domain.BatchLoadDomainProvider(a.Exclude, bp.M().GetDataManager())
		if err != nil {
			p.Shutdown()
			return nil, fmt.Errorf("exclude: %w", err)
		}
		p.exclude = mg
	}
	bp.GetMetricsReg().MustRegister(p.extendedTotal, p.tracked)
	return p, nil
}

// newAdaptiveTTL validates args and applies defaults. Init loads the domain
// lists and registers the metrics.
func newAdaptiveTTL(bp *coremain.BP, args *Args) (*adaptiveTTL, error) {
	for name, v := range map[string]int64{"min_queries": args.MinQueries, "stable_for": args.StableFor, "max_ttl": args.MaxTTL, "size": args.Size} {
		if v < 0 {
			return nil, fmt.Errorf("%s must not be negative", name)
		}
	}
	if args.MaxTTL > int64(^uint32(0)>>1) {
		return nil, fmt.Errorf("max_ttl must be at most %d", ^uint32(0)>>1)
	}
	if args.TrustAnswers && len(args.Domains) == 0 {
		return nil, errors.New("trust_answers requires domains")
	}
	p := &adaptiveTTL{
		BP:         bp,
		minQueries: defaultMinQueries,
		stableFor:  defaultStableFor,
		maxTTL:     defaultMaxTTL,
		size:       defaultSize,
		trust:      args.TrustAnswers,
		empty:      args.ExtendEmpty,
		now:        time.Now,
		keys:       make(map[key]*entry),
	}
	if args.MinQueries > 0 {
		p.minQueries = float64(args.MinQueries)
	}
	if args.StableFor > 0 {
		p.stableFor = time.Duration(args.StableFor) * time.Second
	}
	if args.MaxTTL > 0 {
		p.maxTTL = uint32(args.MaxTTL)
	}
	if args.Size > 0 {
		p.size = int(args.Size)
	}
	p.extendedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ttl_extended_total",
		Help: "Responses whose TTL was lengthened",
	})
	p.tracked = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "tracked_keys",
		Help: "Query keys whose frequency and answer are being learned",
	}, func() float64 {
		p.mu.Lock()
		defer p.mu.Unlock()
		return float64(len(p.keys))
	})
	return p, nil
}

func (p *adaptiveTTL) Exec(ctx context.Context, qCtx *query_context.Context, next executable_seq.ExecutableChainNode) error {
	k, ok := p.keyOf(qCtx.Q())
	if err := executable_seq.ExecChainNode(ctx, qCtx, next); err != nil || !ok {
		return err
	}
	r := qCtx.R()
	ttl := p.observe(k, r, qCtx.StaleHit())
	if ttl == 0 || dnsutils.GetMinimalTTL(r) >= ttl {
		return nil
	}
	dnsutils.ApplyMinimalTTL(r, ttl)
	if len(r.Answer) == 0 {
		// Clients cache a negative answer for the smaller of the SOA's TTL
		// and its MINIMUM field.
		for _, rr := range r.Ns {
			if soa, ok := rr.(*dns.SOA); ok && soa.Minttl < ttl {
				soa.Minttl = ttl
			}
		}
	}
	p.extendedTotal.Inc()
	qCtx.Journal().Step(qCtx.Branch(), RouteStepTTLExtended, strconv.FormatUint(uint64(ttl), 10))
	return nil
}

// keyOf returns the key a query is learned under, or false for queries this
// plugin leaves alone.
func (p *adaptiveTTL) keyOf(q *dns.Msg) (key, bool) {
	if len(q.Question) != 1 || q.Question[0].Qclass != dns.ClassINET {
		return key{}, false
	}
	question := q.Question[0]
	if p.domains != nil {
		if _, listed := p.domains.Match(question.Name); !listed {
			return key{}, false
		}
	}
	if p.exclude != nil {
		if _, excluded := p.exclude.Match(question.Name); excluded {
			return key{}, false
		}
	}
	k := key{name: strings.ToLower(question.Name), qtype: question.Qtype}
	if ecs := dnsutils.GetMsgECS(q); ecs != nil {
		if addr, ok := netip.AddrFromSlice(ecs.Address); ok {
			if prefix, err := addr.Unmap().Prefix(int(ecs.SourceNetmask)); err == nil {
				k.subnet = prefix
			}
		}
	}
	return k, true
}

// observe counts a query for k and learns from its response. It returns the
// TTL to give the response, or 0 to leave it unchanged.
func (p *adaptiveTTL) observe(k key, r *dns.Msg, stale bool) uint32 {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.keys[k]
	if e == nil {
		if len(p.keys) >= p.size && !p.sweep(now) {
			return 0
		}
		e = &entry{windowStart: now, lastSeen: now}
		p.keys[k] = e
	}
	if now.Sub(e.lastSeen) > window+time.Duration(p.maxTTL)*time.Second {
		// Idle longer than a lengthened TTL lasts: no client is relying on
		// it, so hotness is learned again.
		e.hot = false
	}
	e.lastSeen = now
	if elapsed := now.Sub(e.windowStart); elapsed >= window {
		n := elapsed / window
		if n == 1 {
			e.previous = e.current
		} else {
			e.previous = 0
		}
		e.current = 0
		e.windowStart = e.windowStart.Add(n * window)
	}
	e.current++
	if !e.hot && p.rate(e, now) >= p.minQueries {
		e.hot = true
		e.hotSince = now
	}
	// An expired entry served while the cache refreshes it repeats an answer
	// already seen, and its short TTL is what makes the client come back for
	// the refreshed one.
	if r == nil || stale || (r.Rcode != dns.RcodeSuccess && r.Rcode != dns.RcodeNameError) {
		return 0
	}
	if fp := fingerprint(r); fp != e.answer || e.stableSince.IsZero() {
		e.answer = fp
		e.stableSince = now
	}
	e.extendable = r.Rcode == dns.RcodeSuccess && (len(r.Answer) > 0 || p.empty && hasSOA(r))
	return p.ttl(e, now)
}

// rate estimates the queries in the hour before now from the current and
// previous windows.
func (p *adaptiveTTL) rate(e *entry, now time.Time) float64 {
	weight := 1 - float64(now.Sub(e.windowStart))/float64(window)
	return float64(e.previous)*max(weight, 0) + float64(e.current)
}

// ttl is the lengthened TTL for e at now, or 0 when e does not qualify:
// half of how long the answer has stayed the same, or with trust_answers
// how long the key has been hot, once that reaches stable_for.
func (p *adaptiveTTL) ttl(e *entry, now time.Time) uint32 {
	if !e.hot || !e.extendable {
		return 0
	}
	since := e.stableSince
	if p.trust {
		since = e.hotSince
	}
	d := now.Sub(since)
	if d < p.stableFor {
		return 0
	}
	return uint32(min(uint64(d/2/time.Second), uint64(p.maxTTL)))
}

// hasSOA reports whether r carries an SOA, without which clients cannot
// cache a negative answer at all.
func hasSOA(r *dns.Msg) bool {
	for _, rr := range r.Ns {
		if rr.Header().Rrtype == dns.TypeSOA {
			return true
		}
	}
	return false
}

// sweep drops keys idle for longer than a lengthened TTL can last, at most
// once per sweepInterval, and reports whether there is room for a new key.
func (p *adaptiveTTL) sweep(now time.Time) bool {
	if now.Sub(p.lastSweep) >= sweepInterval {
		p.lastSweep = now
		idle := window + time.Duration(p.maxTTL)*time.Second
		for k, e := range p.keys {
			if now.Sub(e.lastSeen) > idle {
				delete(p.keys, k)
			}
		}
	}
	return len(p.keys) < p.size
}

// fingerprint identifies a response's rcode and answer records regardless
// of their order and TTLs.
func fingerprint(r *dns.Msg) uint64 {
	sum := uint64(r.Rcode)
	for _, rr := range r.Answer {
		h := fnv.New64a()
		hdr := rr.Header()
		h.Write([]byte(strings.ToLower(hdr.Name)))
		var t [2]byte
		binary.BigEndian.PutUint16(t[:], hdr.Rrtype)
		h.Write(t[:])
		switch v := rr.(type) {
		case *dns.A:
			h.Write(v.A)
		case *dns.AAAA:
			h.Write(v.AAAA)
		case *dns.CNAME:
			h.Write([]byte(strings.ToLower(v.Target)))
		default:
			c := dns.Copy(rr)
			c.Header().Ttl = 0
			h.Write([]byte(c.String()))
		}
		// Summing keeps the fingerprint independent of record order.
		sum += h.Sum64()
	}
	return sum
}

// ExtendedTTLs reports, for every name with a lengthened TTL right now, the
// longest TTL any of its keys gets.
func (p *adaptiveTTL) ExtendedTTLs() map[string]uint32 {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]uint32)
	idle := window + time.Duration(p.maxTTL)*time.Second
	for k, e := range p.keys {
		if now.Sub(e.lastSeen) > idle {
			continue
		}
		if ttl := p.ttl(e, now); ttl > result[k.name] {
			result[k.name] = ttl
		}
	}
	return result
}

func (p *adaptiveTTL) Shutdown() error {
	var errs []error
	for _, mg := range []*domain.MatcherGroup[struct{}]{p.domains, p.exclude} {
		if mg != nil {
			errs = append(errs, mg.Close())
		}
	}
	return errors.Join(errs...)
}
