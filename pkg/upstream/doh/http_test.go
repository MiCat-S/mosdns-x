package doh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	xhttp "gitlab.com/go-extension/http"
)

func newTestUpstream(t *testing.T, h http.HandlerFunc) *Upstream {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL + "/dns-query")
	if err != nil {
		t.Fatal(err)
	}
	// One connection per host: a response body left open keeps that
	// connection busy, so the next query would wait for it forever.
	tr := &xhttp.Transport{MaxConnsPerHost: 1}
	t.Cleanup(tr.CloseIdleConnections)
	return NewUpstream(u, tr)
}

func TestErrorResponsesReleaseTheConnection(t *testing.T) {
	var n atomic.Int32
	up := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		// Alternate the two early error returns: a bad status and a bad
		// content type.
		if n.Add(1)%2 == 1 {
			w.Header().Set("Content-Type", dnsContentType)
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}
		_, _ = io.WriteString(w, "upstream failed")
	})

	for i := range 4 {
		q := new(dns.Msg)
		q.SetQuestion("www.example.org.", dns.TypeA)
		// Keep every query's context alive until the end: canceling it
		// would also free a connection whose body was left open.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := up.ExchangeContext(ctx, q)
		if ctx.Err() != nil {
			t.Fatalf("query %d waited for a connection held by an earlier error response: %v", i, err)
		}
		if err == nil {
			t.Fatalf("query %d: error response was accepted", i)
		}
	}
}

func TestExchangeKeepsTheCallersQueryID(t *testing.T) {
	up := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		if err := q.Unpack(b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if q.Id != 0 {
			http.Error(w, "DoH query id must be 0", http.StatusBadRequest)
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(q)
		out, _ := resp.Pack()
		w.Header().Set("Content-Type", dnsContentType)
		_, _ = w.Write(out)
	})

	q := new(dns.Msg)
	q.SetQuestion("www.example.org.", dns.TypeA)
	q.Id = 4242
	for _, detailed := range []bool{false, true} {
		var r *dns.Msg
		var err error
		if detailed {
			result, derr := up.ExchangeContextDetailed(context.Background(), q)
			r, err = result.Response, derr
		} else {
			r, err = up.ExchangeContext(context.Background(), q)
		}
		if err != nil {
			t.Fatal(err)
		}
		if q.Id != 4242 {
			t.Fatalf("detailed=%v: caller's query id changed to %d", detailed, q.Id)
		}
		if r.Id != 4242 {
			t.Fatalf("detailed=%v: response id = %d, want the caller's 4242", detailed, r.Id)
		}
	}
}
