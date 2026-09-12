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

package doh

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/miekg/dns"
	"gitlab.com/go-extension/http"

	C "github.com/pmkol/mosdns-x/constant"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/pool"
	upstreamtrace "github.com/pmkol/mosdns-x/pkg/upstream/trace"
)

const dnsContentType = "application/dns-message"

var bufPool = pool.NewBytesBufPool(65535)

type Upstream struct {
	url       *url.URL
	transport *http.Transport
}

func NewUpstream(url *url.URL, transport *http.Transport) *Upstream {
	return &Upstream{url, transport}
}

func (u *Upstream) ExchangeContext(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	result, err := u.exchangeContext(ctx, q, false)
	return result.Response, err
}

func (u *Upstream) ExchangeContextDetailed(ctx context.Context, q *dns.Msg) (upstreamtrace.Result, error) {
	return u.exchangeContext(ctx, q, true)
}

func (u *Upstream) exchangeContext(ctx context.Context, q *dns.Msg, capture bool) (upstreamtrace.Result, error) {
	q.Id = 0
	var requestSnapshot dnsutils.EDNSSnapshot
	if capture {
		requestSnapshot = dnsutils.SnapshotEDNS(q)
	}
	wire, buf, err := pool.PackBuffer(q)
	if err != nil {
		return upstreamtrace.Result{}, err
	}
	defer buf.Release()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url.String(), bytes.NewReader(wire))
	if err != nil {
		return upstreamtrace.Result{}, err
	}
	req.Header.Set("Content-Type", dnsContentType)
	req.Header.Set("Accept", dnsContentType)
	req.Header.Set("User-Agent", fmt.Sprintf("mosdns-x/%s", C.Version))
	res, err := u.transport.RoundTrip(req)
	if err != nil {
		return upstreamtrace.Result{}, err
	}
	if res.StatusCode != 200 {
		return upstreamtrace.Result{}, fmt.Errorf("unexpected status %v: %s", res.StatusCode, res.Status)
	}
	if contentType := res.Header.Get("Content-Type"); contentType != dnsContentType {
		return upstreamtrace.Result{}, fmt.Errorf("unexpected content type: %s", contentType)
	}
	if contentLength := res.Header.Get("Content-Length"); contentLength != "" {
		if length, err := strconv.Atoi(contentLength); err == nil && length == 0 {
			return upstreamtrace.Result{}, fmt.Errorf("empty response")
		}
	}
	defer res.Body.Close()
	bb := bufPool.Get()
	defer bufPool.Release(bb)
	_, err = bb.ReadFrom(res.Body)
	if err != nil {
		return upstreamtrace.Result{}, err
	}
	r := new(dns.Msg)
	err = r.Unpack(bb.Bytes())
	if err != nil {
		return upstreamtrace.Result{}, err
	}
	if capture {
		return upstreamtrace.NewResult(r, requestSnapshot), nil
	}
	return upstreamtrace.Result{Response: r}, nil
}

func (u *Upstream) Close() error {
	u.transport.CloseIdleConnections()
	return nil
}
