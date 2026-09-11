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

	"github.com/pmkol/mosdns-x/pkg/query_context"
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
	r    *dns.Msg
	err  error
	from Upstream
}

var nopLogger = zap.NewNop()

var ErrAllFailed = errors.New("all upstreams failed")

type observerUpstream interface {
	ObserverID() string
}

func exchange(ctx context.Context, q *dns.Msg, u Upstream, observer query_context.UpstreamObserver, principal query_context.Principal) (*dns.Msg, error) {
	started := time.Now()
	r, err := u.Exchange(ctx, q)
	if observer != nil {
		id := "upstream"
		if identified, ok := u.(observerUpstream); ok {
			id = identified.ObserverID()
		}
		attempt := query_context.UpstreamAttempt{
			Principal:  principal,
			UpstreamID: id,
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
	return r, err
}

func ExchangeParallel(ctx context.Context, qCtx *query_context.Context, upstreams []Upstream, logger *zap.Logger) (*dns.Msg, error) {
	if logger == nil {
		logger = nopLogger
	}

	q := qCtx.Q()
	meta := qCtx.ReqMeta()
	observer := meta.GetUpstreamObserver()
	principal := meta.GetPrincipal()
	t := len(upstreams)
	if t == 1 {
		return exchange(ctx, q.Copy(), upstreams[0], observer, principal)
	}

	c := make(chan *parallelResult, t) // use buf chan to avoid blocking.
	for _, u := range upstreams {
		u := u
		qCopy := q.Copy() // Every upstream may mutate its query.
		go func() {
			r, err := exchange(ctx, qCopy, u, observer, principal)
			c <- &parallelResult{
				r:    r,
				err:  err,
				from: u,
			}
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
				return res.r, nil
			}
			continue

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, ErrAllFailed
}
