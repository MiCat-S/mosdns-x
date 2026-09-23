package dnspolicy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

const cacheTTL = 5 * time.Second

var sharedAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
}

type compiledRule struct {
	rule control.DNSPolicyRule
	re   *regexp.Regexp
}

type policy struct {
	settings control.DNSPolicySettings
	rules    []compiledRule
	expires  time.Time
}

// Engine loads per-user policy from the control store and keeps a short-lived
// compiled cache off the DNS request path. Invalidate makes panel writes take
// effect on the next query.
type Engine struct {
	store       control.Service
	publicLists PublicListMatcher
	now         func() time.Time

	mu       sync.RWMutex
	cache    map[string]policy
	versions map[string]uint64
}

type PublicListMatcher interface {
	MatchID(context.Context, string, string) (string, error)
}

type Decision struct {
	Action       control.DNSPolicyAction `json:"action,omitempty"`
	RuleID       string                  `json:"rule_id,omitempty"`
	PublicListID string                  `json:"public_list_id,omitempty"`
}

func New(store control.Service) *Engine {
	return &Engine{store: store, now: time.Now, cache: make(map[string]policy), versions: make(map[string]uint64)}
}

func NewWithPublicLists(store control.Service, publicLists PublicListMatcher) *Engine {
	engine := New(store)
	engine.publicLists = publicLists
	return engine
}

func (e *Engine) Invalidate(userID string) {
	e.mu.Lock()
	delete(e.cache, userID)
	e.versions[userID]++
	e.mu.Unlock()
}

func (e *Engine) Before(ctx context.Context, principal query_context.Principal, request *dns.Msg) (*dns.Msg, error) {
	response, _, err := e.BeforeWithDecision(ctx, principal, request)
	return response, err
}

func (e *Engine) BeforeWithDecision(ctx context.Context, principal query_context.Principal, request *dns.Msg) (*dns.Msg, Decision, error) {
	if principal.UserID == "" {
		return nil, Decision{}, nil
	}
	p, err := e.load(ctx, principal.UserID)
	if err != nil {
		return nil, Decision{}, err
	}
	if policyPaused(p.settings, e.now()) {
		return nil, Decision{}, nil
	}
	if p.settings.StripECS {
		stripECS(request)
	}
	question := request.Question[0]
	qtype := dns.TypeToString[question.Qtype]
	for _, blocked := range p.settings.BlockedQTypes {
		if strings.EqualFold(blocked, qtype) {
			return noDataResponse(request), Decision{Action: control.DNSPolicyBlock}, nil
		}
	}
	name := strings.ToLower(strings.TrimSuffix(question.Name, "."))
	for _, candidate := range p.rules {
		if !candidate.rule.Enabled || !ruleActionEnabled(p.settings, candidate.rule.Action) || !matches(candidate, name) {
			continue
		}
		switch candidate.rule.Action {
		case control.DNSPolicyAllow:
			return nil, Decision{Action: control.DNSPolicyAllow, RuleID: candidate.rule.ID}, nil
		case control.DNSPolicyBlock:
			return blockedResponse(request), Decision{Action: control.DNSPolicyBlock, RuleID: candidate.rule.ID}, nil
		case control.DNSPolicyRewrite:
			response, ok := rewriteResponse(request, candidate.rule)
			if ok {
				return response, Decision{Action: control.DNSPolicyRewrite, RuleID: candidate.rule.ID}, nil
			}
		}
	}
	if e.publicLists != nil {
		listID, err := e.publicLists.MatchID(ctx, principal.UserID, name)
		if err != nil {
			return nil, Decision{}, err
		}
		if listID != "" {
			return blockedResponse(request), Decision{Action: control.DNSPolicyBlock, PublicListID: listID}, nil
		}
	}
	return nil, Decision{}, nil
}

func (e *Engine) After(ctx context.Context, principal query_context.Principal, request, response *dns.Msg) (*dns.Msg, error) {
	result, _, err := e.AfterWithDecision(ctx, principal, request, response)
	return result, err
}

func (e *Engine) AfterWithDecision(ctx context.Context, principal query_context.Principal, request, response *dns.Msg) (*dns.Msg, Decision, error) {
	if principal.UserID == "" {
		return response, Decision{}, nil
	}
	p, err := e.load(ctx, principal.UserID)
	if err != nil {
		return nil, Decision{}, err
	}
	if policyPaused(p.settings, e.now()) {
		return response, Decision{}, nil
	}
	if !p.settings.BlockPrivateAnswers {
		return response, Decision{}, nil
	}
	for _, answer := range response.Answer {
		if answerHasPrivateAddress(answer) {
			return blockedResponse(request), Decision{Action: control.DNSPolicyBlock}, nil
		}
	}
	return response, Decision{}, nil
}

func policyPaused(settings control.DNSPolicySettings, now time.Time) bool {
	return settings.PolicyPausedUntil != nil && now.Before(*settings.PolicyPausedUntil)
}

func ruleActionEnabled(settings control.DNSPolicySettings, action control.DNSPolicyAction) bool {
	switch action {
	case control.DNSPolicyAllow:
		return settings.CustomAllowEnabled
	case control.DNSPolicyBlock:
		return settings.CustomBlockEnabled
	case control.DNSPolicyRewrite:
		return settings.CustomRewriteEnabled
	default:
		return false
	}
}

func (e *Engine) load(ctx context.Context, userID string) (policy, error) {
	for {
		now := e.now()
		e.mu.RLock()
		cached, ok := e.cache[userID]
		version := e.versions[userID]
		e.mu.RUnlock()
		if ok && now.Before(cached.expires) {
			return cached, nil
		}
		settings, err := e.store.GetDNSPolicySettings(ctx, userID)
		if err != nil {
			return policy{}, err
		}
		rules := make([]control.DNSPolicyRule, 0)
		cursor := ""
		for {
			page, err := e.store.ListDNSPolicyRules(ctx, userID, control.Page{Limit: 1000, Cursor: cursor})
			if err != nil {
				return policy{}, err
			}
			rules = append(rules, page.Items...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		sort.SliceStable(rules, func(i, j int) bool {
			if rules[i].Priority == rules[j].Priority {
				return rules[i].ID < rules[j].ID
			}
			return rules[i].Priority < rules[j].Priority
		})
		compiled := make([]compiledRule, 0, len(rules))
		for _, rule := range rules {
			item := compiledRule{rule: rule}
			if rule.Match == control.DNSPolicyMatchRegexp {
				item.re, err = regexp.Compile(rule.Pattern)
				if err != nil {
					return policy{}, fmt.Errorf("invalid stored DNS policy regexp %q: %w", rule.ID, err)
				}
			}
			compiled = append(compiled, item)
		}
		loaded := policy{settings: settings, rules: compiled, expires: now.Add(cacheTTL)}
		e.mu.Lock()
		if e.versions[userID] != version {
			e.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return policy{}, err
			}
			continue
		}
		e.cache[userID] = loaded
		e.mu.Unlock()
		return loaded, nil
	}
}

func matches(candidate compiledRule, name string) bool {
	pattern := strings.ToLower(strings.TrimSuffix(candidate.rule.Pattern, "."))
	switch candidate.rule.Match {
	case control.DNSPolicyMatchExact:
		return name == pattern
	case control.DNSPolicyMatchSuffix:
		return name == pattern || strings.HasSuffix(name, "."+pattern)
	case control.DNSPolicyMatchKeyword:
		return strings.Contains(name, pattern)
	case control.DNSPolicyMatchRegexp:
		return candidate.re != nil && candidate.re.MatchString(name)
	default:
		return false
	}
}

func stripECS(request *dns.Msg) {
	opt := request.IsEdns0()
	if opt == nil {
		return
	}
	filtered := opt.Option[:0]
	for _, option := range opt.Option {
		if option != nil && option.Option() == dns.EDNS0SUBNET {
			continue
		}
		filtered = append(filtered, option)
	}
	opt.Option = filtered
}

// blockedResponse answers NXDOMAIN, which asserts the name does not exist. It
// is for name-level blocks: custom rules, public lists and private answers.
func blockedResponse(request *dns.Msg) *dns.Msg {
	response := new(dns.Msg)
	response.SetReply(request)
	response.Rcode = dns.RcodeNameError
	return response
}

// noDataResponse answers NOERROR with an empty answer section: the name exists
// but has no record of the requested type. Query type blocking must use this
// rather than NXDOMAIN. Under RFC 8020 an NXDOMAIN means nothing exists at or
// below the name for any type, so a resolver may cache it and fail the A
// lookup as well. Browsers query HTTPS alongside A and AAAA, so an NXDOMAIN
// for a blocked HTTPS record could take the whole site down.
func noDataResponse(request *dns.Msg) *dns.Msg {
	response := new(dns.Msg)
	response.SetReply(request)
	response.Rcode = dns.RcodeSuccess
	return response
}

func rewriteResponse(request *dns.Msg, rule control.DNSPolicyRule) (*dns.Msg, bool) {
	question := request.Question[0]
	header := dns.RR_Header{Name: question.Name, Class: dns.ClassINET, Ttl: 60}
	var answer dns.RR
	switch rule.RecordType {
	case control.DNSPolicyRewriteA:
		if question.Qtype != dns.TypeA && question.Qtype != dns.TypeANY {
			return nil, false
		}
		header.Rrtype = dns.TypeA
		answer = &dns.A{Hdr: header, A: net.ParseIP(rule.Value).To4()}
	case control.DNSPolicyRewriteAAAA:
		if question.Qtype != dns.TypeAAAA && question.Qtype != dns.TypeANY {
			return nil, false
		}
		header.Rrtype = dns.TypeAAAA
		answer = &dns.AAAA{Hdr: header, AAAA: net.ParseIP(rule.Value).To16()}
	case control.DNSPolicyRewriteCNAME:
		if question.Qtype != dns.TypeA && question.Qtype != dns.TypeAAAA && question.Qtype != dns.TypeCNAME && question.Qtype != dns.TypeANY {
			return nil, false
		}
		header.Rrtype = dns.TypeCNAME
		answer = &dns.CNAME{Hdr: header, Target: dns.Fqdn(rule.Value)}
	default:
		return nil, false
	}
	response := new(dns.Msg)
	response.SetReply(request)
	response.Answer = []dns.RR{answer}
	return response, true
}

func answerHasPrivateAddress(answer dns.RR) bool {
	var ip net.IP
	switch record := answer.(type) {
	case *dns.A:
		ip = record.A
	case *dns.AAAA:
		ip = record.AAAA
	default:
		return false
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() || address.IsMulticast() {
		return true
	}
	for _, prefix := range sharedAddressPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
