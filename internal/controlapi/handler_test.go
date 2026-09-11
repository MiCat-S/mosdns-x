package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/miekg/dns"
	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/telemetry"
)

type apiClock struct {
	mu sync.RWMutex
	t  time.Time
}

func (c *apiClock) Now() time.Time  { c.mu.RLock(); defer c.mu.RUnlock(); return c.t }
func (c *apiClock) Set(v time.Time) { c.mu.Lock(); c.t = v; c.mu.Unlock() }

type fakeTelemetry struct {
	mu      sync.Mutex
	userIDs []string
	filters []telemetry.QueryFilter
}

func (f *fakeTelemetry) Snapshot(_ context.Context, user string, from, to time.Time) (telemetry.StatsSnapshot, error) {
	f.mu.Lock()
	f.userIDs = append(f.userIDs, user)
	f.mu.Unlock()
	return telemetry.StatsSnapshot{From: from, To: to, RcodeCounts: map[string]uint64{}, Series: []telemetry.SeriesPoint{}, Upstreams: []telemetry.UpstreamStats{}}, nil
}
func (f *fakeTelemetry) Queries(_ context.Context, user string, _, _ time.Time, filter telemetry.QueryFilter, p telemetry.Page) (telemetry.QueryPage, error) {
	f.mu.Lock()
	f.userIDs = append(f.userIDs, user)
	f.filters = append(f.filters, filter)
	f.mu.Unlock()
	return telemetry.QueryPage{Items: []telemetry.QueryRecord{}}, nil
}

func TestQueryFiltersAreValidatedAndScoped(t *testing.T) {
	f := newFixture(t)
	alice, _ := login(t, f.handler, "alice", "password-for-alice")
	values := url.Values{
		"user_id":       {f.user2.ID},
		"name":          {"Example.COM"},
		"qtype":         {"aaaa"},
		"rcode":         {"nxdomain"},
		"credential_id": {"device-1"},
		"protocol":      {"H3"},
		"address":       {"2001:db8::1"},
		"cache":         {"hit"},
	}
	w := req(f.handler, http.MethodGet, "/api/v1/me/queries?"+values.Encode(), "", alice, "")
	if w.Code != http.StatusOK {
		t.Fatalf("queries=%d %s", w.Code, w.Body.String())
	}
	f.telemetry.mu.Lock()
	gotUser := f.telemetry.userIDs[len(f.telemetry.userIDs)-1]
	gotFilter := f.telemetry.filters[len(f.telemetry.filters)-1]
	f.telemetry.mu.Unlock()
	if gotUser != f.user1.ID || gotFilter.Name != "Example.COM" || gotFilter.QType != "AAAA" || gotFilter.Rcode != "NXDOMAIN" || gotFilter.CredentialID != "device-1" || gotFilter.Protocol != "h3" || gotFilter.Address != "2001:db8::1" || gotFilter.CacheHit == nil || !*gotFilter.CacheHit {
		t.Fatalf("user=%q filter=%+v", gotUser, gotFilter)
	}
	for _, path := range []string{"/api/v1/me/queries?address=not-an-ip", "/api/v1/me/queries?cache=maybe"} {
		if invalid := req(f.handler, http.MethodGet, path, "", alice, ""); invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid filter %q=%d", path, invalid.Code)
		}
	}
}

type fixture struct {
	t         *testing.T
	store     *control.Store
	handler   *Handler
	clock     *apiClock
	admin     control.User
	user1     control.User
	user2     control.User
	telemetry *fakeTelemetry
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	clock := &apiClock{t: now}
	s, err := control.Open(filepath.Join(t.TempDir(), "control.db"), control.Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	admin, err := s.InitializeAdmin(context.Background(), spec("admin", control.RoleAdmin))
	if err != nil {
		t.Fatal(err)
	}
	u1, err := s.CreateUser(context.Background(), admin.ID, spec("alice", control.RoleUser))
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.CreateUser(context.Background(), admin.ID, spec("bob", control.RoleUser))
	if err != nil {
		t.Fatal(err)
	}
	ft := new(fakeTelemetry)
	h, err := New(Options{Control: s, Telemetry: ft, PublicDNSURL: "http://localhost/dns-query", PanelOrigin: "http://localhost:3000", Development: true, SessionTTL: time.Hour, Now: clock.Now, Legacy: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(218) }), Assets: fstest.MapFS{"index.html": {Data: []byte("app")}, "assets/app.js": {Data: []byte("js")}}})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t, s, h, clock, admin, u1, u2, ft}
}
func spec(name string, role control.Role) control.UserSpec {
	return control.UserSpec{Username: name, Password: "password-for-" + name, Role: role, Enabled: true, Period: control.PeriodDaily, Timezone: "UTC", Limit: 100, QPS: 100, Burst: 10, MaxCredentials: 3}
}

func req(h http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:1234"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Origin", "http://localhost:3000")
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, h http.Handler, user, password string) (*http.Cookie, string) {
	t.Helper()
	w := req(h, http.MethodPost, "/api/v1/session", `{"username":"`+user+`","password":"`+password+`"}`, nil, "")
	if w.Code != 200 {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	var v sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	res := w.Result()
	cookies := res.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%v", cookies)
	}
	return cookies[0], v.CSRFToken
}

func TestSessionOriginCSRFCookieAndPasswordReset(t *testing.T) {
	f := newFixture(t)
	bad := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"username":"alice","password":"password-for-alice"}`))
	bad.Header.Set("Origin", "http://evil.test")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, bad)
	if w.Code != 403 {
		t.Fatalf("origin=%d", w.Code)
	}
	cookie, csrf := login(t, f.handler, "alice", "password-for-alice")
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("cookie=%+v", cookie)
	}
	if got := req(f.handler, http.MethodPost, "/api/v1/me/credentials", `{"name":"phone"}`, cookie, ""); got.Code != 403 {
		t.Fatalf("missing csrf=%d", got.Code)
	}
	got := req(f.handler, http.MethodPost, "/api/v1/me/password", `{"current_password":"password-for-alice","new_password":"new-password-alice"}`, cookie, csrf)
	if got.Code != 204 {
		t.Fatalf("password=%d %s", got.Code, got.Body.String())
	}
	if len(got.Result().Cookies()) != 1 || !got.Result().Cookies()[0].HttpOnly || got.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("clear cookie=%v", got.Result().Cookies())
	}
	if got = req(f.handler, http.MethodGet, "/api/v1/session", "", cookie, ""); got.Code != 401 {
		t.Fatalf("old session=%d", got.Code)
	}
	login(t, f.handler, "alice", "new-password-alice")
}

func TestCredentialOneTimeURLAndIsolation(t *testing.T) {
	f := newFixture(t)
	alice, csrf := login(t, f.handler, "alice", "password-for-alice")
	w := req(f.handler, http.MethodPost, "/api/v1/me/credentials", `{"name":"phone"}`, alice, csrf)
	if w.Code != 201 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var issued issuedResponse
	if json.Unmarshal(w.Body.Bytes(), &issued) != nil || issued.Token == "" || !strings.Contains(issued.DNSURL, issued.Token) {
		t.Fatalf("issued=%s", w.Body.String())
	}
	w = req(f.handler, http.MethodGet, "/api/v1/me/credentials", "", alice, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), issued.Token) || strings.Contains(w.Body.String(), "doh_url") {
		t.Fatalf("list leaked=%s", w.Body.String())
	}
	bob, bobCSRF := login(t, f.handler, "bob", "password-for-bob")
	w = req(f.handler, http.MethodDelete, "/api/v1/me/credentials/"+issued.Credential.ID, "", bob, bobCSRF)
	if w.Code != 403 {
		t.Fatalf("cross revoke=%d %s", w.Code, w.Body.String())
	}
	w = req(f.handler, http.MethodPost, "/api/v1/me/credentials/"+issued.Credential.ID+"/rotate", "{}", alice, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "doh_url") {
		t.Fatalf("rotate=%d %s", w.Code, w.Body.String())
	}
	dnsCookie := &http.Cookie{Name: "mosdns_session", Value: issued.Token}
	if w = req(f.handler, http.MethodGet, "/api/v1/session", "", dnsCookie, ""); w.Code != 401 {
		t.Fatalf("dns token session=%d", w.Code)
	}
}

func TestLookupIsAuthenticatedValidatedAndScoped(t *testing.T) {
	f := newFixture(t)
	alice, csrf := login(t, f.handler, "alice", "password-for-alice")
	var gotUser, gotName string
	var gotType uint16
	f.handler.opts.Lookup = func(_ context.Context, userID, name string, qtype uint16) (*dns.Msg, error) {
		gotUser, gotName, gotType = userID, name, qtype
		request := new(dns.Msg).SetQuestion(name, qtype)
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("192.0.2.10")}}
		response.SetEdns0(1232, false)
		return response, nil
	}
	if got := req(f.handler, http.MethodPost, "/api/v1/me/lookup", `{"name":"example.org","qtype":"A"}`, nil, csrf); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous lookup=%d", got.Code)
	}
	if got := req(f.handler, http.MethodPost, "/api/v1/me/lookup", `{"name":"example.org","qtype":"A"}`, alice, ""); got.Code != http.StatusForbidden {
		t.Fatalf("lookup without csrf=%d", got.Code)
	}
	if got := req(f.handler, http.MethodPost, "/api/v1/me/lookup", `{"name":"bad name","qtype":"A"}`, alice, csrf); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid lookup=%d", got.Code)
	}
	w := req(f.handler, http.MethodPost, "/api/v1/me/lookup", `{"name":"Example.ORG.","qtype":"a"}`, alice, csrf)
	if w.Code != http.StatusOK || gotUser != f.user1.ID || gotName != "example.org." || gotType != dns.TypeA {
		t.Fatalf("lookup=%d user=%q name=%q type=%d body=%s", w.Code, gotUser, gotName, gotType, w.Body.String())
	}
	var response lookupResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Question.Name != "example.org" || response.Question.QType != "A" || response.Rcode != "NOERROR" || len(response.Answers) != 1 || response.Answers[0].Value != "192.0.2.10" || !response.EDNS.Present || response.EDNS.UDPSize != 1232 {
		t.Fatalf("lookup response=%s err=%v", w.Body.String(), err)
	}
}

func TestUserPolicySettingsAndRules(t *testing.T) {
	f := newFixture(t)
	alice, csrf := login(t, f.handler, "alice", "password-for-alice")
	invalidations := 0
	f.handler.opts.InvalidatePolicy = func(userID string) {
		if userID != f.user1.ID {
			t.Fatalf("invalidated user=%q", userID)
		}
		invalidations++
	}
	if w := req(f.handler, http.MethodGet, "/api/v1/me/settings", "", alice, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"blocked_qtypes":[]`) {
		t.Fatalf("settings=%d %s", w.Code, w.Body.String())
	}
	if w := req(f.handler, http.MethodPatch, "/api/v1/me/settings", `{"strip_ecs":true,"block_private_answers":true,"blocked_qtypes":["AAAA","TXT"]}`, alice, csrf); w.Code != http.StatusOK {
		t.Fatalf("update settings=%d %s", w.Code, w.Body.String())
	}
	w := req(f.handler, http.MethodPost, "/api/v1/me/rules", `{"enabled":true,"action":"rewrite","match":"exact","pattern":"example.org","record_type":"A","value":"192.0.2.1"}`, alice, csrf)
	if w.Code != http.StatusCreated {
		t.Fatalf("create rule=%d %s", w.Code, w.Body.String())
	}
	var rule control.DNSPolicyRule
	if err := json.Unmarshal(w.Body.Bytes(), &rule); err != nil || rule.UserID != f.user1.ID || rule.ID == "" {
		t.Fatalf("rule=%+v err=%v", rule, err)
	}
	if w = req(f.handler, http.MethodGet, "/api/v1/me/rules", "", alice, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), rule.ID) {
		t.Fatalf("list rules=%d %s", w.Code, w.Body.String())
	}
	if w = req(f.handler, http.MethodPatch, "/api/v1/me/rules/"+rule.ID, `{"enabled":false}`, alice, csrf); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("patch rule=%d %s", w.Code, w.Body.String())
	}
	if w = req(f.handler, http.MethodDelete, "/api/v1/me/rules/"+rule.ID, "", alice, csrf); w.Code != http.StatusNoContent {
		t.Fatalf("delete rule=%d %s", w.Code, w.Body.String())
	}
	if invalidations != 4 {
		t.Fatalf("invalidations=%d", invalidations)
	}
}

func TestAdminValidationPaginationAndRoleField(t *testing.T) {
	f := newFixture(t)
	admin, csrf := login(t, f.handler, "admin", "password-for-admin")
	w := req(f.handler, http.MethodPost, "/api/v1/admin/users", `{"username":"x","password":"long-enough-password","role":"user","enabled":true,"period":"daily","timezone":"UTC","limit":9007199254740992,"qps":1,"burst":0,"max_credentials":1}`, admin, csrf)
	if w.Code != 400 {
		t.Fatalf("unsafe integer=%d", w.Code)
	}
	w = req(f.handler, http.MethodPatch, "/api/v1/admin/users/"+f.user1.ID, `{"role":"admin"}`, admin, csrf)
	if w.Code != 400 {
		t.Fatalf("role patch=%d %s", w.Code, w.Body.String())
	}
	w = req(f.handler, http.MethodPatch, "/api/v1/admin/users/"+f.user1.ID, `{"enabled":true,"unknown":1}`, admin, csrf)
	if w.Code != 400 {
		t.Fatalf("unknown=%d", w.Code)
	}
	w = req(f.handler, http.MethodGet, "/api/v1/admin/users?limit=1", "", admin, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "next_cursor") {
		t.Fatalf("page=%d %s", w.Code, w.Body.String())
	}
}

func TestStatsIdentityAndLegacyProtection(t *testing.T) {
	f := newFixture(t)
	alice, _ := login(t, f.handler, "alice", "password-for-alice")
	admin, csrf := login(t, f.handler, "admin", "password-for-admin")
	w := req(f.handler, http.MethodGet, "/api/v1/me/stats?user_id="+f.user2.ID, "", alice, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	f.telemetry.mu.Lock()
	got := f.telemetry.userIDs[len(f.telemetry.userIDs)-1]
	f.telemetry.mu.Unlock()
	if got != f.user1.ID {
		t.Fatalf("stats user=%q", got)
	}
	if w = req(f.handler, http.MethodGet, "/metrics", "", nil, ""); w.Code != 401 {
		t.Fatalf("metrics public=%d", w.Code)
	}
	if w = req(f.handler, http.MethodGet, "/metrics", "", alice, ""); w.Code != 403 {
		t.Fatalf("metrics user=%d", w.Code)
	}
	if w = req(f.handler, http.MethodGet, "/metrics", "", admin, ""); w.Code != 218 {
		t.Fatalf("metrics admin=%d", w.Code)
	}
	if w = req(f.handler, http.MethodPost, "/plugins/cache/flush", "{}", admin, ""); w.Code != 403 {
		t.Fatalf("plugin csrf=%d", w.Code)
	}
	if w = req(f.handler, http.MethodPost, "/plugins/cache/flush", "{}", admin, csrf); w.Code != 218 {
		t.Fatalf("plugin admin=%d", w.Code)
	}
	if w = req(f.handler, http.MethodGet, "/debug/pprof/", "", admin, ""); w.Code != 404 {
		t.Fatalf("pprof disabled=%d", w.Code)
	}
	pprof, err := New(Options{Control: f.store, PublicDNSURL: "http://localhost/dns-query", PanelOrigin: "http://localhost:3000", Development: true, Legacy: f.handler.opts.Legacy, EnablePprof: true})
	if err != nil {
		t.Fatal(err)
	}
	if w = req(pprof, http.MethodGet, "/debug/pprof/", "", nil, ""); w.Code != 401 {
		t.Fatalf("pprof public=%d", w.Code)
	}
	if w = req(pprof, http.MethodGet, "/debug/pprof/", "", admin, ""); w.Code != 218 {
		t.Fatalf("pprof admin=%d", w.Code)
	}
	if w = req(f.handler, http.MethodGet, "//debug/pprof/", "", nil, ""); w.Code != 404 {
		t.Fatalf("pprof variant=%d", w.Code)
	}
}

type failingUserService struct{ control.Service }

func (f failingUserService) CurrentQuota(context.Context, string) (control.QuotaStatus, error) {
	return control.QuotaStatus{}, errors.New("db password token SECRET")
}
func TestErrorsDoNotLeakAndProductionValidation(t *testing.T) {
	f := newFixture(t)
	cookie, _ := login(t, f.handler, "alice", "password-for-alice")
	h, err := New(Options{Control: failingUserService{f.store}, PublicDNSURL: "http://localhost/dns-query", PanelOrigin: "http://localhost:3000", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	w := req(h, http.MethodGet, "/api/v1/me", "", cookie, "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "password") {
		t.Fatalf("leak=%d %s", w.Code, w.Body.String())
	}
	if _, err = New(Options{Control: f.store, PublicDNSURL: "https://dns.test/dns-query", PanelOrigin: "https://panel.test"}); err == nil {
		t.Fatal("production accepted insecure cookie")
	}
	if _, err = New(Options{Control: f.store, PublicDNSURL: "https://user:pass@dns.test/dns-query", PanelOrigin: "https://panel.test", SecureCookies: true}); err == nil {
		t.Fatal("accepted URL userinfo")
	}
}

func TestExpiredUserCanLoginAndEmptyQueries(t *testing.T) {
	f := newFixture(t)
	expiry := f.clock.Now().Add(time.Minute)
	_, err := f.store.UpdateUser(context.Background(), f.admin.ID, f.user1.ID, control.UserPatch{ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := f.store.CreateCredential(context.Background(), f.user1.ID, f.user1.ID, "phone", expiry)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Set(expiry)
	cookie, _ := login(t, f.handler, "alice", "password-for-alice")
	if _, err = f.store.AuthenticateCredential(context.Background(), cred.Token); !errors.Is(err, control.ErrForbidden) {
		t.Fatalf("DNS auth=%v", err)
	}
	w := req(f.handler, http.MethodGet, "/api/v1/me/queries", "", cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("queries=%d %s", w.Code, w.Body.String())
	}
}

type blockingLogin struct {
	control.Service
	started chan struct{}
	release chan struct{}
}

func (b *blockingLogin) Login(ctx context.Context, u, p string, ttl time.Duration) (control.Session, string, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return control.Session{}, "", control.ErrInvalidCredential
	case <-ctx.Done():
		return control.Session{}, "", ctx.Err()
	}
}
func TestLoginConcurrencyAndIPRateLimit(t *testing.T) {
	f := newFixture(t)
	b := &blockingLogin{Service: f.store, started: make(chan struct{}, 1), release: make(chan struct{})}
	h, err := New(Options{Control: b, PublicDNSURL: "http://localhost/dns-query", PanelOrigin: "http://localhost:3000", Development: true, LoginConcurrency: 1, LoginRateLimit: 100, LoginRateWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- req(h, http.MethodPost, "/api/v1/session", `{"username":"alice","password":"password-for-alice"}`, nil, "")
	}()
	<-b.started
	adminCookie, adminCSRF := login(t, f.handler, "admin", "password-for-admin")
	w := req(h, http.MethodPost, "/api/v1/session", `{"username":"bob","password":"password-for-bob"}`, nil, "")
	if w.Code != 429 {
		t.Fatalf("concurrency=%d", w.Code)
	}
	if reset := req(h, http.MethodPost, "/api/v1/admin/users/"+f.user1.ID+"/password", `{"new_password":"some-new-password"}`, adminCookie, adminCSRF); reset.Code != 429 {
		t.Fatalf("reset bypassed KDF slot=%d", reset.Code)
	}
	close(b.release)
	<-done
	rate, err := New(Options{Control: f.store, PublicDNSURL: "http://localhost/dns-query", PanelOrigin: "http://localhost:3000", Development: true, LoginRateLimit: 1, LoginRateWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	first := req(rate, http.MethodPost, "/api/v1/session", `{"username":"alice","password":"definitely-wrong"}`, nil, "")
	if first.Code != 401 {
		t.Fatalf("first=%d", first.Code)
	}
	if second := req(rate, http.MethodPost, "/api/v1/session", `{"username":"random-name","password":"definitely-wrong"}`, nil, ""); second.Code != 429 {
		t.Fatalf("rate=%d", second.Code)
	}
}

func TestProxyChainOriginRangeAndRoot(t *testing.T) {
	f := newFixture(t)
	trusted, _ := netip.ParsePrefix("10.0.0.0/8")
	h, err := New(Options{Control: f.store, PublicDNSURL: "http://localhost/dns-query", PanelOrigin: "http://127.0.0.1:8080", Development: true, TrustedProxyCIDRs: []netip.Prefix{trusted}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.8")
	if got := h.clientIP(r); got != "198.51.100.8" {
		t.Fatalf("spoofed XFF selected %s", got)
	}
	if _, err = New(Options{Control: f.store, PublicDNSURL: "http://localhost/dns-query", PanelOrigin: "http://panel.example", Development: true}); err == nil {
		t.Fatal("development accepted non-loopback http origin")
	}
	admin, _ := login(t, f.handler, "admin", "password-for-admin")
	at := url.QueryEscape(f.clock.Now().Format(time.RFC3339))
	w := req(f.handler, http.MethodGet, "/api/v1/admin/usage?from="+at+"&to="+at, "", admin, "")
	if w.Code != 400 {
		t.Fatalf("equal range=%d", w.Code)
	}
	w = req(f.handler, http.MethodGet, "/", "", nil, "")
	if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "/login" {
		t.Fatalf("root=%d %s", w.Code, w.Header().Get("Location"))
	}
	nilUI, err := New(Options{Control: f.store, PublicDNSURL: "http://localhost/dns-query", PanelOrigin: "http://localhost", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	w = req(nilUI, http.MethodGet, "/login", "", nil, "")
	if w.Code != 404 || !strings.Contains(w.Body.String(), "-tags ui") {
		t.Fatalf("nil ui=%d %s", w.Code, w.Body.String())
	}
}

func TestStaticAndAPINotFound(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/login", "/app/x", "/admin/users"} {
		if w := req(f.handler, http.MethodGet, path, "", nil, ""); w.Code != 200 {
			t.Fatalf("static %s=%d", path, w.Code)
		}
	}
	if w := req(f.handler, http.MethodGet, "/api/unknown", "", nil, ""); w.Code != 404 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("api404=%d %s", w.Code, w.Body.String())
	}
	if w := req(f.handler, http.MethodGet, "/unknown", "", nil, ""); w.Code != 404 {
		t.Fatalf("unknown=%d", w.Code)
	}
}
