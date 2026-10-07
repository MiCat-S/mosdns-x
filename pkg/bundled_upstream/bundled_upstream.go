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

package bundled_upstream

import (
	"context"
	"errors"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	upstreamtrace "github.com/pmkol/mosdns-x/pkg/upstream/trace"
)

type Upstream interface {
	// Exchange sends q to the upstream and waits for response.
	// If any error occurs. Implements must return a nil msg with a non nil error.
	// Otherwise, Implements must a msg with nil error.
	Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error)

	// Trusted indicates whether this Upstream is trusted/reliable.
	// If true, responses from this Upstream will be accepted without checking its rcode.
	Trusted() bool

	Address() string
}

type parallelResult struct {
	r            *dns.Msg
	err          error
	from         Upstream
	seq          int
	requestEDNS  *dnsutils.EDNSSnapshot
	responseEDNS *dnsutils.EDNSSnapshot
}

type ExchangeResult struct {
	Response         *dns.Msg
	UpstreamID       string
	UpstreamLabel    string
	AttemptSeq       int
	RequestEDNS      *dnsutils.EDNSSnapshot
	ResponseEDNS     *dnsutils.EDNSSnapshot
	Attempted        bool
	DetailsAvailable bool
}

var nopLogger = zap.NewNop()

var ErrAllFailed = errors.New("all upstreams failed")

type observerUpstream interface {
	ObserverID() string
}

func observerID(u Upstream) string {
	if identified, ok := u.(observerUpstream); ok {
		return identified.ObserverID()
	}
	return "upstream"
}

// labeledUpstream names an upstream for query logs without exposing a private
// server's address.
type labeledUpstream interface {
	DisplayName() string
	PluginTag() string
}

func displayName(u Upstream) (label, plugin string) {
	if l, ok := u.(labeledUpstream); ok {
		return l.DisplayName(), l.PluginTag()
	}
	return observerID(u), ""
}

// attemptRecorder writes upstream attempts to the query's journal.
type attemptRecorder struct {
	journal *query_context.Journal
	branch  string
}

func newAttemptRecorder(qCtx *query_context.Context) attemptRecorder {
	return attemptRecorder{journal: qCtx.Journal(), branch: qCtx.Branch()}
}

func (a attemptRecorder) begin(u Upstream) int {
	if a.journal == nil {
		return 0
	}
	label, plugin := displayName(u)
	return a.journal.BeginAttempt(a.branch, plugin, label)
}

func (a attemptRecorder) end(seq int, r *dns.Msg, err error) {
	if a.journal == nil {
		return
	}
	rcode := ""
	errKind := upstreamtrace.ErrorKind(err)
	if r != nil {
		rcode = dns.RcodeToString[r.Rcode]
	} else if err == nil {
		errKind = upstreamtrace.ErrorEmptyResponse
	}
	a.journal.EndAttempt(seq, rcode, errKind)
}

func exchange(ctx context.Context, q *dns.Msg, u Upstream, observer query_context.UpstreamObserver, principal query_context.Principal, rec attemptRecorder) (*dns.Msg, int, error) {
	started := time.Now()
	seq := rec.begin(u)
	r, err := u.Exchange(ctx, q)
	rec.end(seq, r, err)
	if observer != nil {
		attempt := query_context.UpstreamAttempt{
			Principal:  principal,
			UpstreamID: observerID(u),
			Duration:   time.Since(started),
			Rcode:      -1,
			Failed:     err != nil || r == nil,
		}
		if r != nil {
			attempt.Rcode = r.Rcode
			attempt.Failed = attempt.Failed || r.Rcode == dns.RcodeServerFailure || r.Rcode == dns.RcodeRefused
		}
		observer(attempt)
	}
	return r, seq, err
}

type detailedUpstream interface {
	ExchangeDetailed(context.Context, *dns.Msg) (upstreamtrace.Result, error)
}

func exchangeDetailed(ctx context.Context, q *dns.Msg, u Upstream, observer query_context.UpstreamObserver, principal query_context.Principal, rec attemptRecorder) (upstreamtrace.Result, int, error) {
	started := time.Now()
	seq := rec.begin(u)
	var (
		result upstreamtrace.Result
		err    error
	)
	if detailed, ok := u.(detailedUpstream); ok {
		result, err = detailed.ExchangeDetailed(ctx, q)
	} else {
		result.Response, err = u.Exchange(ctx, q)
	}
	rec.end(seq, result.Response, err)
	if observer != nil {
		attempt := query_context.UpstreamAttempt{
			Principal: principal, UpstreamID: observerID(u), Duration: time.Since(started),
			Rcode: -1, Failed: err != nil || result.Response == nil,
		}
		if result.Response != nil {
			attempt.Rcode = result.Response.Rcode
			attempt.Failed = attempt.Failed || result.Response.Rcode == dns.RcodeServerFailure || result.Response.Rcode == dns.RcodeRefused
		}
		observer(attempt)
	}
	return result, seq, err
}

func ExchangeParallel(ctx context.Context, qCtx *query_context.Context, upstreams []Upstream, logger *zap.Logger) (ExchangeResult, error) {
	return exchangeParallel(ctx, qCtx, upstreams, logger, false)
}

func ExchangeParallelDetailed(ctx context.Context, qCtx *query_context.Context, upstreams []Upstream, logger *zap.Logger) (ExchangeResult, error) {
	return exchangeParallel(ctx, qCtx, upstreams, logger, true)
}

func exchangeParallel(ctx context.Context, qCtx *query_context.Context, upstreams []Upstream, logger *zap.Logger, capture bool) (ExchangeResult, error) {
	if logger == nil {
		logger = nopLogger
	}

	q := qCtx.Q()
	meta := qCtx.ReqMeta()
	observer := meta.GetUpstreamObserver()
	principal := meta.GetPrincipal()
	rec := newAttemptRecorder(qCtx)
	t := len(upstreams)
	if t == 1 {
		label, _ := displayName(upstreams[0])
		if capture {
			detailed, seq, err := exchangeDetailed(ctx, q.Copy(), upstreams[0], observer, principal, rec)
			return ExchangeResult{Response: detailed.Response, UpstreamID: observerID(upstreams[0]), UpstreamLabel: label, AttemptSeq: seq, RequestEDNS: detailed.RequestEDNS, ResponseEDNS: detailed.ResponseEDNS, Attempted: true, DetailsAvailable: detailed.DetailsAvailable}, err
		}
		r, seq, err := exchange(ctx, q.Copy(), upstreams[0], observer, principal, rec)
		return ExchangeResult{Response: r, UpstreamID: observerID(upstreams[0]), UpstreamLabel: label, AttemptSeq: seq, Attempted: true}, err
	}

	c := make(chan *parallelResult, t) // use buf chan to avoid blocking.
	for i, u := range upstreams {
		u := u
		qCopy := q.Copy() // Every upstream may mutate its query.
		release, ok := meta.AcquireBackgroundWork()
		if !ok {
			return ExchangeResult{Attempted: i > 0}, context.Canceled
		}
		go func() {
			defer release()
			if capture {
				detailed, seq, err := exchangeDetailed(ctx, qCopy, u, observer, principal, rec)
				c <- &parallelResult{r: detailed.Response, err: err, from: u, seq: seq, requestEDNS: detailed.RequestEDNS, responseEDNS: detailed.ResponseEDNS}
				return
			}
			r, seq, err := exchange(ctx, qCopy, u, observer, principal, rec)
			c <- &parallelResult{r: r, err: err, from: u, seq: seq}
		}()
	}

	for i := 0; i < t; i++ {
		select {
		case res := <-c:
			if res.err != nil {
				logger.Warn("upstream err", qCtx.InfoField(), zap.String("addr", res.from.Address()))
				continue
			}

			if res.r == nil {
				continue
			}

			if res.from.Trusted() || res.r.Rcode == dns.RcodeSuccess {
				label, _ := displayName(res.from)
				return ExchangeResult{Response: res.r, UpstreamID: observerID(res.from), UpstreamLabel: label, AttemptSeq: res.seq, RequestEDNS: res.requestEDNS, ResponseEDNS: res.responseEDNS, Attempted: true, DetailsAvailable: res.requestEDNS != nil && res.responseEDNS != nil}, nil
			}
			continue

		case <-ctx.Done():
			return ExchangeResult{Attempted: true}, ctx.Err()
		}
	}
	return ExchangeResult{Attempted: t > 0}, ErrAllFailed
}
