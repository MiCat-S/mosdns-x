package controlapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/pmkol/mosdns-x/internal/control"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type cleanupContextKey struct{}

type failedLoginResponse struct {
	control.Service
	cancel          context.CancelFunc
	revokeErr       error
	cleanupCalled   bool
	cleanupErr      error
	cleanupDeadline time.Time
	cleanupValue    any
}

func (s *failedLoginResponse) Login(context.Context, string, string, time.Duration) (control.Session, string, error) {
	return control.Session{ID: "session-id", UserID: "user-id", CSRFToken: "csrf-secret", ExpiresAt: time.Now().Add(time.Hour)}, "session-id.session-secret", nil
}

func (s *failedLoginResponse) GetUser(context.Context, string) (control.User, error) {
	if s.cancel != nil {
		s.cancel()
		return control.User{}, context.Canceled
	}
	return control.User{}, control.ErrUnavailable
}

func (s *failedLoginResponse) RevokeSession(ctx context.Context, _, _ string) error {
	s.cleanupCalled = true
	s.cleanupErr = ctx.Err()
	s.cleanupDeadline, _ = ctx.Deadline()
	s.cleanupValue = ctx.Value(cleanupContextKey{})
	return s.revokeErr
}

func TestFailedLoginResponseCleansUpWithoutIssuingCookie(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, revokeFails := range []bool{false, true} {
			name := "active"
			if canceled {
				name = "canceled"
			}
			if revokeFails {
				name += "/revoke_failure"
			} else {
				name += "/revoke_success"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.WithValue(context.Background(), cleanupContextKey{}, "request-context"))
				defer cancel()
				service := &failedLoginResponse{}
				if canceled {
					service.cancel = cancel
				}
				if revokeFails {
					service.revokeErr = control.ErrUnavailable
				}
				logCore, logs := observer.New(zap.DebugLevel)
				h, err := New(Options{
					Control: service, PublicDNSURL: "http://localhost/dns-query",
					PanelOrigin: "http://localhost:3000", Development: true, Logger: zap.New(logCore),
				})
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(http.MethodPost, "/api/v1/session",
					strings.NewReader(`{"username":"test-user","password":"test-password"}`)).WithContext(ctx)
				r.Header.Set("Origin", "http://localhost:3000")
				r.RemoteAddr = "192.0.2.1:1234"
				w := httptest.NewRecorder()
				started := time.Now()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				cookies := w.Result().Cookies()
				if len(cookies) != 1 || cookies[0].Value != "" || cookies[0].MaxAge >= 0 {
					t.Fatal("failed login must send only a deletion cookie")
				}
				if !service.cleanupCalled || service.cleanupErr != nil || service.cleanupValue != "request-context" {
					t.Fatalf("cleanup called=%v err=%v value=%v", service.cleanupCalled, service.cleanupErr, service.cleanupValue)
				}
				if service.cleanupDeadline.Before(started) || service.cleanupDeadline.After(time.Now().Add(5*time.Second)) {
					t.Fatal("cleanup must have an independent five-second deadline")
				}
				entries := logs.FilterMessage("failed to revoke session after login response failure").All()
				if revokeFails {
					if len(entries) != 1 || entries[0].Level != zap.ErrorLevel {
						t.Fatal("revoke failure must be logged exactly once")
					}
					fields := entries[0].ContextMap()
					if fields["session_id"] != "session-id" || fields["user_id"] != "user-id" {
						t.Fatalf("missing cleanup identifiers: %v", fields)
					}
					for _, secret := range []string{"session-secret", "csrf-secret", "test-password"} {
						for _, value := range fields {
							if text, ok := value.(string); ok && strings.Contains(text, secret) {
								t.Fatal("cleanup log contains a secret")
							}
						}
					}
				} else if len(entries) != 0 {
					t.Fatal("successful cleanup logged an error")
				}
			})
		}
	}
}

func TestDevelopmentModeWarning(t *testing.T) {
	logCore, logs := observer.New(zap.WarnLevel)
	_, err := New(Options{Control: &failedLoginResponse{}, PublicDNSURL: "http://localhost/dns-query",
		PanelOrigin: "http://localhost:3000", Development: true, Logger: zap.New(logCore)})
	if err != nil {
		t.Fatal(err)
	}
	if got := logs.FilterMessageSnippet("control development mode enabled").Len(); got != 1 {
		t.Fatalf("development warnings=%d want=1", got)
	}
}

func TestWriteAuthorizationRejectsInvalidCSRF(t *testing.T) {
	h := &Handler{opts: Options{PanelOrigin: "https://panel.example.test"}}
	ss := &control.Session{CSRFToken: strings.Repeat("a", 43)}
	for _, tc := range []struct {
		name, origin, token string
		session             *control.Session
		want                bool
	}{
		{"valid", h.opts.PanelOrigin, ss.CSRFToken, ss, true},
		{"missing", h.opts.PanelOrigin, "", ss, false},
		{"wrong", h.opts.PanelOrigin, strings.Repeat("b", 43), ss, false},
		{"oversized", h.opts.PanelOrigin, strings.Repeat("a", 257), ss, false},
		{"untrusted origin", "https://other.example.test", ss.CSRFToken, ss, false},
		{"missing session", h.opts.PanelOrigin, ss.CSRFToken, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-CSRF-Token", tc.token)
			if got := h.writeAuthorized(r, tc.session); got != tc.want {
				t.Fatalf("authorized=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestIPLimiterPrunesExpiredEntriesBelowCapacity(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	l := newIPLimiter(2, time.Minute, 10, func() time.Time { return now })
	if !l.allow("old") {
		t.Fatal("initial request rejected")
	}
	now = now.Add(30 * time.Second)
	if !l.allow("fresh") {
		t.Fatal("fresh request rejected")
	}
	now = now.Add(31 * time.Second)
	if !l.allow("fresh") || l.allow("fresh") {
		t.Fatal("cleanup reset a live rate limit")
	}
	if _, ok := l.entries["old"]; ok {
		t.Fatal("expired entry retained below capacity")
	}
	if len(l.entries) != 1 {
		t.Fatalf("retained entries=%d", len(l.entries))
	}
}

func TestIPLimiterConcurrentRequestsAndCapacity(t *testing.T) {
	now := time.Now()
	l := newIPLimiter(3, time.Minute, 2, func() time.Time { return now })
	var wg sync.WaitGroup
	var allowed atomic.Int32
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.allow("shared") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 3 || !l.allow("second") || l.allow("third") || len(l.entries) != 2 {
		t.Fatalf("allowed=%d entries=%d", allowed.Load(), len(l.entries))
	}
}

func TestProxyHeadersCannotOverrideUntrustedPeer(t *testing.T) {
	trusted := netip.MustParsePrefix("10.0.0.0/8")
	for _, tc := range []struct {
		name, peer, header, value, want string
		trusted                         []netip.Prefix
	}{
		{"untrusted peer", "192.0.2.1:123", "X-Forwarded-For", "203.0.113.99", "192.0.2.1", []netip.Prefix{trusted}},
		{"no trusted proxies", "10.0.0.1:123", "X-Forwarded-For", "203.0.113.99", "10.0.0.1", nil},
		{"untrusted hop", "10.0.0.1:123", "X-Forwarded-For", "203.0.113.99, 198.51.100.2", "198.51.100.2", []netip.Prefix{trusted}},
		{"trusted chain", "10.0.0.1:123", "X-Forwarded-For", "192.0.2.8, 10.0.0.2", "192.0.2.8", []netip.Prefix{trusted}},
		{"malformed chain", "10.0.0.1:123", "X-Forwarded-For", "invalid, 192.0.2.8", "10.0.0.1", []netip.Prefix{trusted}},
		{"malformed custom header", "10.0.0.1:123", "X-Real-IP", "192.0.2.8, 192.0.2.9", "10.0.0.1", []netip.Prefix{trusted}},
		{"untrusted custom header", "192.0.2.1:123", "X-Real-IP", "192.0.2.8", "192.0.2.1", []netip.Prefix{trusted}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{opts: Options{TrustedProxyCIDRs: tc.trusted, ClientIPHeader: tc.header}}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set(tc.header, tc.value)
			if got := h.clientIP(r); got != tc.want {
				t.Fatalf("client IP=%s want=%s", got, tc.want)
			}
		})
	}
}

func TestLookupRejectsMalformedNamesBeforeExecution(t *testing.T) {
	h, err := New(Options{Control: &failedLoginResponse{}, PublicDNSURL: "http://localhost/dns-query",
		PanelOrigin: "http://localhost:3000", Development: true,
		Lookup: func(context.Context, string, string, uint16) (*dns.Msg, error) {
			t.Error("malformed lookup reached the executor")
			return nil, errors.New("unexpected lookup")
		}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "a..test", "-bad.test", "bad-.test", "bad name.test", strings.Repeat("a", 64) + ".test", strings.Repeat("a.", 127) + "a", "example.test..", "bad/test"} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"`+name+`","qtype":"A"}`))
		w := httptest.NewRecorder()
		h.lookup(w, r, control.User{ID: "user"})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("name=%q status=%d", name, w.Code)
		}
	}
	h.opts.Lookup = func(context.Context, string, string, uint16) (*dns.Msg, error) { return nil, nil }
	w := httptest.NewRecorder()
	h.lookup(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"example.test","qtype":"A"}`)), control.User{ID: "user"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil lookup response status=%d", w.Code)
	}
}
