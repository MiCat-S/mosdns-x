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
