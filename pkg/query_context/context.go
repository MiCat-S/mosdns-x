/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package query_context

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
)

const (
	ProtocolUDP   = "udp"
	ProtocolTCP   = "tcp"
	ProtocolTLS   = "tls"
	ProtocolQUIC  = "quic"
	ProtocolHTTP  = "http"
	ProtocolHTTPS = "https"
	ProtocolH2    = "h2"
	ProtocolH3    = "h3"
)

// RequestMeta represents some metadata about the request.
type RequestMeta struct {
	// ClientAddr contains the client ip address.
	// It might be zero/invalid.
	clientAddr netip.Addr

	serverName string

	protocol string

	principal Principal

	upstreamObserver      UpstreamObserver
	backgroundWorkTracker BackgroundWorkTracker
}

// Principal identifies the authenticated account and credential for a query.
// It is a value type so request metadata users cannot mutate shared state.
type Principal struct {
	UserID            string
	CredentialID      string
	CredentialVersion uint64
}

// UpstreamAttempt is a self-contained observation of one actual upstream call.
type UpstreamAttempt struct {
	Principal  Principal
	UpstreamID string
	Duration   time.Duration
	Rcode      int
	Failed     bool
}

type UpstreamObserver func(UpstreamAttempt)

// BackgroundWorkTracker keeps runtime-owned resources alive for work that can
// continue after an entry handler has selected and returned a response.
type BackgroundWorkTracker func() (release func(), ok bool)

const (
	ResponseSourceCache         = "cache"
	ResponseSourceUpstream      = "upstream"
	ResponseSourceCustomBlock   = "custom_block"
	ResponseSourceCustomRewrite = "custom_rewrite"
	ResponseSourcePublicList    = "public_list"
	ResponseSourceHosts         = "hosts"
	ResponseSourceSequence      = "sequence"
	ResponseSourceServfail      = "servfail"
)

const (
	UpstreamStageSelected             = "selected"
	UpstreamStageAttemptedNoSelection = "attempted_no_selection"
	UpstreamStageDiscarded            = "discarded"
	UpstreamStageNotLinked            = "not_linked"
	UpstreamStageUnavailable          = "unavailable"
)

// ResponseTrace identifies the component that produced the response selected
// for this branch. Context setters and copy operations clone its snapshots so
// parallel branches never share mutable trace data.
type ResponseTrace struct {
	Source               string
	SourceID             string
	UpstreamID           string
	MatchedRuleID        string
	MatchedPublicListID  string
	UpstreamStageStatus  string
	UpstreamRequestEDNS  *dnsutils.EDNSSnapshot
	UpstreamResponseEDNS *dnsutils.EDNSSnapshot
}

func (t ResponseTrace) Clone() ResponseTrace {
	t.UpstreamRequestEDNS = dnsutils.CloneEDNSSnapshot(t.UpstreamRequestEDNS)
	t.UpstreamResponseEDNS = dnsutils.CloneEDNSSnapshot(t.UpstreamResponseEDNS)
	return t
}

func (t ResponseTrace) IsZero() bool {
	return t.Source == "" && t.SourceID == "" && t.UpstreamID == "" &&
		t.MatchedRuleID == "" && t.MatchedPublicListID == "" &&
		t.UpstreamStageStatus == "" && t.UpstreamRequestEDNS == nil &&
		t.UpstreamResponseEDNS == nil
}

func NewRequestMeta(addr netip.Addr) *RequestMeta {
	meta := new(RequestMeta)
	meta.SetClientAddr(addr)
	return meta
}

// OriginalQuery returns the copied original query msg a that created the Context.
// It always returns a non-nil msg.
// The returned msg SHOULD NOT be modified.
func (m *RequestMeta) SetClientAddr(addr netip.Addr) {
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	m.clientAddr = addr
}

func (m *RequestMeta) SetProtocol(protocol string) {
	m.protocol = protocol
}

func (m *RequestMeta) SetServerName(serverName string) {
	m.serverName = serverName
}

func (m *RequestMeta) GetClientAddr() netip.Addr {
	return m.clientAddr
}

func (m *RequestMeta) GetProtocol() string {
	return m.protocol
}

func (m *RequestMeta) GetServerName() string {
	return m.serverName
}

// SetPrincipal sets the authenticated principal before the request enters the
// executable chain. Callers must treat RequestMeta as read-only afterwards.
func (m *RequestMeta) SetPrincipal(principal Principal) {
	m.principal = principal
}

func (m *RequestMeta) GetPrincipal() Principal {
	if m == nil {
		return Principal{}
	}
	return m.principal
}

func (m *RequestMeta) SetUpstreamObserver(observer UpstreamObserver) {
	m.upstreamObserver = observer
}

func (m *RequestMeta) GetUpstreamObserver() UpstreamObserver {
	if m == nil {
		return nil
	}
	return m.upstreamObserver
}

// Copy returns an independent metadata container. Callbacks and the principal
// are immutable after admission and are safe to copy by value.
func (m *RequestMeta) Copy() *RequestMeta {
	if m == nil {
		return new(RequestMeta)
	}
	copy := *m
	return &copy
}

func (m *RequestMeta) SetBackgroundWorkTracker(tracker BackgroundWorkTracker) {
	m.backgroundWorkTracker = tracker
}

func (m *RequestMeta) AcquireBackgroundWork() (func(), bool) {
	if m == nil || m.backgroundWorkTracker == nil {
		return func() {}, true
	}
	return m.backgroundWorkTracker()
}

// Context is a query context that pass through plugins
// A Context will always have a non-nil Q.
// Context MUST be created using NewContext.
// All Context funcs are not safe for concurrent use.
type Context struct {
	// init at beginning
	startTime     time.Time // when this Context was created
	q             *dns.Msg
	originalQuery *dns.Msg
	id            uint32 // additional uint to distinguish duplicated msg
	reqMeta       *RequestMeta

	r                   *dns.Msg
	cacheHit            bool
	trace               ResponseTrace
	captureQueryDetails bool
	marks               map[uint]struct{}
}

var (
	contextUid      uint32
	zeroRequestMeta = &RequestMeta{}
)

// NewContext creates a new query Context.
// q is the query dns msg. It cannot be nil, or NewContext will panic.
// meta can be nil.
func NewContext(q *dns.Msg, meta *RequestMeta) *Context {
	if q == nil {
		panic("handler: query msg is nil")
	}

	if meta == nil {
		meta = zeroRequestMeta
	}

	ctx := &Context{
		q:             q,
		originalQuery: q.Copy(),
		reqMeta:       meta,
		id:            atomic.AddUint32(&contextUid, 1),
		startTime:     time.Now(),
	}

	return ctx
}

// String returns a short summery of its query.
func (ctx *Context) String() string {
	var question string
	var clientAddr string

	if len(ctx.q.Question) >= 1 {
		q := ctx.q.Question[0]
		question = fmt.Sprintf("%s %s %s", q.Name, dnsutils.QclassToString(q.Qclass), dnsutils.QtypeToString(q.Qtype))
	} else {
		question = "empty question"
	}
	if ctx.reqMeta.clientAddr.IsValid() {
		clientAddr = ctx.reqMeta.clientAddr.String()
	} else {
		clientAddr = "unknown client"
	}

	return fmt.Sprintf("%s %d %d %s", question, ctx.q.Id, ctx.id, clientAddr)
}

// Q returns the query msg. It always returns a non-nil msg.
func (ctx *Context) Q() *dns.Msg {
	return ctx.q
}

// OriginalQuery returns the copied original query msg a that created the Context.
// It always returns a non-nil msg.
// The returned msg SHOULD NOT be modified.
func (ctx *Context) OriginalQuery() *dns.Msg {
	return ctx.originalQuery
}

// ReqMeta returns the request metadata. It always returns a non-nil RequestMeta.
// The returned *RequestMeta is a reference shared by all ReqMeta.
// Caller must not modify it.
func (ctx *Context) ReqMeta() *RequestMeta {
	return ctx.reqMeta
}

// R returns the response. It might be nil.
func (ctx *Context) R() *dns.Msg {
	return ctx.r
}

// SetResponse stores the response r to the context.
// Note: It just stores the pointer of r. So the caller
// shouldn't modify or read r after the call.
func (ctx *Context) SetResponse(r *dns.Msg) {
	discardedUpstream := ctx.captureQueryDetails &&
		(ctx.trace.UpstreamStageStatus == UpstreamStageSelected || ctx.trace.UpstreamID != "")
	ctx.r = r
	ctx.cacheHit = false
	ctx.trace = ResponseTrace{}
	if discardedUpstream {
		ctx.trace.UpstreamStageStatus = UpstreamStageDiscarded
	}
}

// SetResponseWithTrace stores a response together with its origin.
func (ctx *Context) SetResponseWithTrace(r *dns.Msg, trace ResponseTrace) {
	ctx.SetResponse(r)
	if trace.UpstreamStageStatus == "" && ctx.trace.UpstreamStageStatus == UpstreamStageDiscarded {
		trace.UpstreamStageStatus = UpstreamStageDiscarded
	}
	ctx.trace = trace.Clone()
}

// SetResponseTrace updates the origin of the current response.
func (ctx *Context) SetResponseTrace(trace ResponseTrace) {
	ctx.trace = trace.Clone()
}

func (ctx *Context) ResponseTrace() ResponseTrace {
	return ctx.trace.Clone()
}

// AdoptResponse copies response state from a completed isolated branch.
func (ctx *Context) AdoptResponse(src *Context) {
	ctx.SetResponse(src.R())
	ctx.cacheHit = src.cacheHit
	ctx.trace = src.trace.Clone()
}

// AdoptFailureTrace retains only reliable branch-level evidence that upstream
// work produced no selectable response. It never adopts a failed branch's
// response, upstream ID, or EDNS snapshots.
func (ctx *Context) AdoptFailureTrace(src *Context) {
	if !ctx.captureQueryDetails || src == nil {
		return
	}
	incoming := src.trace.UpstreamStageStatus
	if incoming != UpstreamStageAttemptedNoSelection && incoming != UpstreamStageUnavailable {
		return
	}
	if ctx.trace.UpstreamStageStatus == UpstreamStageSelected || ctx.trace.UpstreamStageStatus == UpstreamStageDiscarded {
		return
	}
	if incoming == UpstreamStageAttemptedNoSelection || ctx.trace.UpstreamStageStatus == "" {
		ctx.trace.UpstreamStageStatus = incoming
	}
	ctx.trace.UpstreamID = ""
	ctx.trace.UpstreamRequestEDNS = nil
	ctx.trace.UpstreamResponseEDNS = nil
}

// SetCaptureQueryDetails fixes the detailed-observation choice for this query.
// It must be called before the context is shared with executable branches.
func (ctx *Context) SetCaptureQueryDetails(enabled bool) {
	ctx.captureQueryDetails = enabled
}

func (ctx *Context) CaptureQueryDetails() bool {
	return ctx.captureQueryDetails
}

func (ctx *Context) SetCacheHit(hit bool) {
	ctx.cacheHit = hit
}

func (ctx *Context) CacheHit() bool {
	return ctx.cacheHit
}

// Id returns the Context id.
// Note: This id is not the dns msg id.
// It's a unique uint32 growing with the number of query.
func (ctx *Context) Id() uint32 {
	return ctx.id
}

// StartTime returns the time when the Context was created.
func (ctx *Context) StartTime() time.Time {
	return ctx.startTime
}

// InfoField returns a zap.Field.
// Just for convenience.
func (ctx *Context) InfoField() zap.Field {
	return zap.Stringer("query", ctx)
}

// Copy deep copies this Context.
func (ctx *Context) Copy() *Context {
	newCtx := new(Context)
	ctx.CopyTo(newCtx)
	return newCtx
}

// CopyTo deep copies this Context to d.
func (ctx *Context) CopyTo(d *Context) *Context {
	d.startTime = ctx.startTime
	d.q = ctx.q.Copy()
	d.originalQuery = ctx.originalQuery
	d.reqMeta = ctx.reqMeta
	d.id = ctx.id

	if r := ctx.r; r != nil {
		d.r = r.Copy()
	}
	d.cacheHit = ctx.cacheHit
	d.trace = ctx.trace.Clone()
	d.captureQueryDetails = ctx.captureQueryDetails
	for m := range ctx.marks {
		d.AddMark(m)
	}
	return d
}

// AddMark adds mark m to this Context.
func (ctx *Context) AddMark(m uint) {
	if ctx.marks == nil {
		ctx.marks = make(map[uint]struct{})
	}
	ctx.marks[m] = struct{}{}
}

// HasMark reports whether this Context has mark m.
func (ctx *Context) HasMark(m uint) bool {
	_, ok := ctx.marks[m]
	return ok
}

var allocatedMark struct {
	sync.Mutex
	u uint
}

var errMarkOverflowed = errors.New("too many allocated marks")

func AllocateMark() (uint, error) {
	allocatedMark.Lock()
	defer allocatedMark.Unlock()
	m := allocatedMark.u + 1
	if m == 0 {
		return 0, errMarkOverflowed
	}
	allocatedMark.u++
	return m, nil
}
