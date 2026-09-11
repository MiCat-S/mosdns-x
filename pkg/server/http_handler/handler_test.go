package http_handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/query_access"
)

type testRequest struct {
	u          *url.URL
	method     string
	header     http.Header
	body       io.ReadCloser
	remoteAddr string
}

func (r *testRequest) URL() *url.URL             { return r.u }
func (r *testRequest) TLS() *TlsInfo             { return nil }
func (r *testRequest) Body() io.ReadCloser       { return r.body }
func (r *testRequest) Header() Header            { return r.header }
func (r *testRequest) Method() string            { return r.method }
func (r *testRequest) Context() context.Context  { return context.Background() }
func (r *testRequest) RequestURI() string        { return r.u.RequestURI() }
func (r *testRequest) GetRemoteAddr() string     { return r.remoteAddr }
func (r *testRequest) SetRemoteAddr(addr string) { r.remoteAddr = addr }

type testWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *testWriter) Header() Header              { return w.header }
func (w *testWriter) Write(b []byte) (int, error) { return w.body.Write(b) }
func (w *testWriter) WriteHeader(status int)      { w.status = status }

type testDNSHandler struct {
	calls int
	meta  *query_context.RequestMeta
	err   error
}

func (h *testDNSHandler) ServeDNS(_ context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error) {
	h.calls++
	h.meta = meta
	if h.err != nil {
		return nil, h.err
	}
	r := new(dns.Msg)
	r.SetReply(req)
	return r, nil
}

func packedQuery(t *testing.T) []byte {
	t.Helper()
	b, err := new(dns.Msg).SetQuestion("example.org.", dns.TypeA).Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newRequest(method, rawURL string, body []byte) *testRequest {
	u, _ := url.Parse(rawURL)
	return &testRequest{u: u, method: method, header: make(http.Header), body: io.NopCloser(bytes.NewReader(body)), remoteAddr: "192.0.2.10:1234"}
}

func runHandler(t *testing.T, h *Handler, req *testRequest) *testWriter {
	t.Helper()
	w := &testWriter{header: make(http.Header)}
	h.ServeHTTP(w, req)
	return w
}

func TestProtectedGETPathAndBearerPOST(t *testing.T) {
	principal := query_context.Principal{UserID: "user", CredentialID: "cred", CredentialVersion: 7}
	uuid := "123e4567-e89b-42d3-a456-426614174000"
	for _, tc := range []struct{ name, method, rawURL, bearer, credential string }{
		{"legacy path GET", http.MethodGet, "/dns-query/id.secret", "", "id.secret"},
		{"legacy bearer GET", http.MethodGet, "/dns-query", "Bearer id.secret", "id.secret"},
		{"UUID path GET", http.MethodGet, "/dns-query/" + uuid, "", uuid},
		{"UUID bearer GET", http.MethodGet, "/dns-query", "Bearer " + uuid, uuid},
		{"path POST", http.MethodPost, "/dns-query/id.secret", "", "id.secret"},
		{"bearer POST", http.MethodPost, "/dns-query", "Bearer id.secret", "id.secret"},
		{"lowercase bearer", http.MethodPost, "/dns-query", "bearer id.secret", "id.secret"},
		{"uppercase bearer", http.MethodPost, "/dns-query", "BEARER id.secret", "id.secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dh := new(testDNSHandler)
			authCalls := 0
			h, err := NewHandler(HandlerOpts{DNSHandler: dh, Authenticate: func(_ context.Context, credential string) (query_context.Principal, error) {
				authCalls++
				if credential != tc.credential {
					t.Fatalf("credential=%q", credential)
				}
				return principal, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			var req *testRequest
			if tc.method == http.MethodGet {
				raw := tc.rawURL + "?dns=" + base64.RawURLEncoding.EncodeToString(packedQuery(t))
				req = newRequest(tc.method, raw, nil)
				req.header.Set("Accept", "*/*")
			} else {
				req = newRequest(tc.method, tc.rawURL, packedQuery(t))
				req.header.Set("Content-Type", "application/dns-message")
			}
			req.header.Set("Authorization", tc.bearer)
			w := runHandler(t, h, req)
			if w.status != http.StatusOK || authCalls != 1 || dh.calls != 1 || dh.meta.GetPrincipal() != principal {
				t.Fatalf("status=%d auth=%d dns=%d principal=%#v body=%q", w.status, authCalls, dh.calls, dh.meta.GetPrincipal(), w.body.String())
			}
			if got := w.header.Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control=%q", got)
			}
		})
	}
}

func TestCredentialSyntax(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"123e4567-e89b-42d3-a456-426614174000", true},
		{"id.secret", true},
		{"123e4567-e89b-12d3-a456-426614174000", false},
		{"123E4567-E89B-42D3-A456-426614174000", false},
		{"123e4567-e89b-42d3-7456-426614174000", false},
		{"id.secret.extra", false},
		{"id secret", false},
		{"id/secret", false},
		{"id/.secret", false},
		{"", false},
	} {
		if got := validCredential(tc.value); got != tc.valid {
			t.Errorf("validCredential(%q)=%v want %v", tc.value, got, tc.valid)
		}
	}
}

type readTracker struct{ read bool }

func (r *readTracker) Read([]byte) (int, error) { r.read = true; return 0, io.EOF }
func (*readTracker) Close() error               { return nil }

func TestAuthenticationFailuresSkipBodyAndDNS(t *testing.T) {
	for _, tc := range []struct {
		name, path, auth string
		authErr          error
		want             int
	}{
		{"missing", "/dns-query", "", nil, 401},
		{"ambiguous", "/dns-query/id.secret", "Bearer id.secret", nil, 401},
		{"expired", "/dns-query", "Bearer id.secret", query_access.New(query_access.InvalidCredential), 401},
		{"denied", "/dns-query", "Bearer id.secret", query_access.New(query_access.Forbidden), 403},
		{"rate", "/dns-query", "Bearer id.secret", query_access.New(query_access.RateLimited), 429},
		{"quota", "/dns-query", "Bearer id.secret", query_access.New(query_access.QuotaExceeded), 429},
		{"unavailable", "/dns-query", "Bearer id.secret", query_access.New(query_access.Unavailable), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dh := new(testDNSHandler)
			tracker := new(readTracker)
			h, _ := NewHandler(HandlerOpts{DNSHandler: dh, Authenticate: func(context.Context, string) (query_context.Principal, error) {
				if tc.authErr != nil {
					return query_context.Principal{}, tc.authErr
				}
				return query_context.Principal{}, nil
			}})
			req := newRequest(http.MethodPost, tc.path, nil)
			req.body = tracker
			req.header.Set("Authorization", tc.auth)
			w := runHandler(t, h, req)
			if w.status != tc.want || tracker.read || dh.calls != 0 {
				t.Fatalf("status=%d read=%v dns=%d", w.status, tracker.read, dh.calls)
			}
			if got := w.header.Get("WWW-Authenticate"); (tc.want == 401) != (got == `Bearer realm="mosdns"`) {
				t.Fatalf("WWW-Authenticate=%q status=%d", got, tc.want)
			}
			if w.header.Get("Cache-Control") != "private, no-store" {
				t.Fatal("protected error is cacheable")
			}
		})
	}
}

func TestGETAcceptCompatibilityAndOversize(t *testing.T) {
	for _, accept := range []string{"", "*/*", "text/plain, application/dns-message; q=1"} {
		dh := new(testDNSHandler)
		h, _ := NewHandler(HandlerOpts{DNSHandler: dh, Path: "/dns-query"})
		req := newRequest(http.MethodGet, "/dns-query?dns="+base64.RawURLEncoding.EncodeToString(packedQuery(t)), nil)
		req.header.Set("Accept", accept)
		if got := runHandler(t, h, req).status; got != 200 {
			t.Fatalf("Accept %q status=%d", accept, got)
		}
	}
	dh := new(testDNSHandler)
	h, _ := NewHandler(HandlerOpts{DNSHandler: dh, Path: "/dns-query"})
	req := newRequest(http.MethodGet, "/dns-query?dns="+strings.Repeat("A", maxEncodedDNSQuery+1), nil)
	if got := runHandler(t, h, req).status; got != http.StatusRequestEntityTooLarge || dh.calls != 0 {
		t.Fatalf("status=%d calls=%d", got, dh.calls)
	}
}

func TestAdmitUnauthorizedChallenge(t *testing.T) {
	dh := &testDNSHandler{err: query_access.New(query_access.InvalidCredential)}
	h, _ := NewHandler(HandlerOpts{DNSHandler: dh, Authenticate: func(context.Context, string) (query_context.Principal, error) { return query_context.Principal{}, nil }})
	req := newRequest(http.MethodPost, "/dns-query", packedQuery(t))
	req.header.Set("Authorization", "Bearer id.secret")
	req.header.Set("Content-Type", "application/dns-message")
	w := runHandler(t, h, req)
	if w.status != http.StatusUnauthorized || w.header.Get("WWW-Authenticate") != `Bearer realm="mosdns"` {
		t.Fatalf("status=%d challenge=%q", w.status, w.header.Get("WWW-Authenticate"))
	}
}

func TestOversizePOST(t *testing.T) {
	dh := new(testDNSHandler)
	h, _ := NewHandler(HandlerOpts{DNSHandler: dh})
	req := newRequest(http.MethodPost, "/anything", bytes.Repeat([]byte{0}, maxDNSMessageSize+1))
	req.header.Set("Content-Type", "application/dns-message")
	w := runHandler(t, h, req)
	if w.status != http.StatusRequestEntityTooLarge || dh.calls != 0 {
		t.Fatalf("status=%d calls=%d", w.status, dh.calls)
	}
}

func TestTrustedProxyResolution(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8:1::/48")}
	tests := []struct{ name, peer, selected, header, value, want string }{
		{"untrusted spoof", "192.0.2.1:53", "", "X-Forwarded-For", "198.51.100.2", "192.0.2.1"},
		{"trusted v4", "10.0.0.1:53", "", "X-Forwarded-For", "198.51.100.2", "198.51.100.2"},
		{"trusted v6", "[2001:db8:1::1]:53", "", "X-Forwarded-For", "2001:db8:2::2", "2001:db8:2::2"},
		{"xff hops", "10.0.0.1:53", "", "X-Forwarded-For", "198.51.100.3, 10.1.1.1, 10.2.2.2", "198.51.100.3"},
		{"xff stops", "10.0.0.1:53", "", "X-Forwarded-For", "203.0.113.9, 198.51.100.3, 10.2.2.2", "198.51.100.3"},
		{"invalid xff", "10.0.0.1:53", "", "X-Forwarded-For", "attacker, 10.2.2.2", "10.0.0.1"},
		{"custom header", "10.0.0.1:53", "True-Client-IP", "True-Client-IP", "198.51.100.4", "198.51.100.4"},
		{"invalid custom", "10.0.0.1:53", "X-Real-IP", "X-Real-IP", "invalid", "10.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(http.MethodGet, "/", nil)
			req.remoteAddr = tc.peer
			req.header.Set(tc.header, tc.value)
			got, _ := getRemoteAddr(req, tc.selected, trusted)
			if got.String() != tc.want || req.remoteAddr != tc.peer {
				t.Fatalf("addr=%s remote=%q", got, req.remoteAddr)
			}
		})
	}
}

func TestSelectedProxyHeaderDoesNotFallBack(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	req := newRequest(http.MethodGet, "/", nil)
	req.remoteAddr = "10.0.0.1:53"
	req.header.Set("X-Forwarded-For", "198.51.100.10, 10.1.1.1")
	req.header.Set("True-Client-IP", "203.0.113.20")
	req.header.Set("X-Real-IP", "203.0.113.21")

	got, _ := getRemoteAddr(req, "", trusted)
	if got.String() != "198.51.100.10" {
		t.Fatalf("default selected alternate header: %s", got)
	}
	got, _ = getRemoteAddr(req, "X-Real-IP", trusted)
	if got.String() != "203.0.113.21" {
		t.Fatalf("explicit header was not selected: %s", got)
	}

	req.header.Set("X-Real-IP", "invalid")
	got, _ = getRemoteAddr(req, "X-Real-IP", trusted)
	if got.String() != "10.0.0.1" {
		t.Fatalf("invalid selected header fell back: %s", got)
	}
}

type logBuffer struct{ bytes.Buffer }

func (b *logBuffer) Sync() error { return nil }

func TestSecretAbsentFromLogs(t *testing.T) {
	logs := new(logBuffer)
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), logs, zap.DebugLevel))
	dh := new(testDNSHandler)
	h, _ := NewHandler(HandlerOpts{DNSHandler: dh, Logger: logger, Authenticate: func(context.Context, string) (query_context.Principal, error) {
		return query_context.Principal{}, query_access.Wrap(query_access.InvalidCredential, errors.New("secret-value"))
	}})
	req := newRequest(http.MethodGet, "/dns-query/id.secret-value", nil)
	runHandler(t, h, req)
	if strings.Contains(logs.String(), "secret-value") || strings.Contains(logs.String(), "id.secret") {
		t.Fatalf("secret leaked: %s", logs.String())
	}
}
