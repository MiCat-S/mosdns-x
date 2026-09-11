package controlapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/telemetry"
)

const (
	maxBody        = 32 << 10
	maxCursor      = 2048
	maxSafeInteger = uint64(1<<53 - 1)
)

type Telemetry interface {
	Snapshot(context.Context, string, time.Time, time.Time) (telemetry.StatsSnapshot, error)
	Queries(context.Context, string, time.Time, time.Time, telemetry.Page) (telemetry.QueryPage, error)
}

type SystemInfo struct {
	Version         string       `json:"version"`
	StartedAt       time.Time    `json:"started_at"`
	PublicDNSURL    string       `json:"public_dns_url"`
	QueryLogEnabled bool         `json:"query_log_enabled"`
	Config          SystemConfig `json:"config"`
}

type SystemConfig struct {
	DNSProtocols      []string `json:"dns_protocols"`
	ManagementEnabled bool     `json:"management_enabled"`
	PprofEnabled      bool     `json:"pprof_enabled"`
}

type Options struct {
	Control           control.Service
	Telemetry         Telemetry
	PublicDNSURL      string
	PanelOrigin       string
	SecureCookies     bool
	Development       bool
	SessionTTL        time.Duration
	CookieName        string
	SystemInfo        func(context.Context) (SystemInfo, error)
	Assets            fs.FS
	Legacy            http.Handler
	EnablePprof       bool
	TrustedProxyCIDRs []netip.Prefix
	ClientIPHeader    string
	LoginConcurrency  int
	LoginRateLimit    int
	LoginRateWindow   time.Duration
	LoginIPCapacity   int
	Now               func() time.Time
}

type Handler struct {
	opts      Options
	publicURL *url.URL
	kdfSlots  chan struct{}
	limiter   *ipLimiter
}

func New(opts Options) (*Handler, error) {
	if opts.Control == nil {
		return nil, errors.New("control service is required")
	}
	u, err := validatePublicURL(opts.PublicDNSURL, opts.Development)
	if err != nil {
		return nil, err
	}
	if opts.PanelOrigin == "" {
		return nil, errors.New("panel origin is required")
	}
	origin, err := url.Parse(opts.PanelOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return nil, errors.New("invalid panel origin")
	}
	if origin.Scheme != "https" {
		host := origin.Hostname()
		ip, _ := netip.ParseAddr(host)
		if !opts.Development || origin.Scheme != "http" || (host != "localhost" && !ip.IsLoopback()) {
			return nil, errors.New("panel origin must use https or development loopback http")
		}
	}
	if !opts.Development && !opts.SecureCookies {
		return nil, errors.New("secure cookies are required in production")
	}
	if opts.SessionTTL == 0 {
		opts.SessionTTL = 24 * time.Hour
	}
	if opts.SessionTTL < time.Minute || opts.SessionTTL > 31*24*time.Hour {
		return nil, errors.New("invalid session ttl")
	}
	if opts.CookieName == "" {
		opts.CookieName = "mosdns_session"
	}
	if !validCookieName(opts.CookieName) {
		return nil, errors.New("invalid cookie name")
	}
	if opts.ClientIPHeader == "" {
		opts.ClientIPHeader = "X-Forwarded-For"
	}
	if opts.LoginConcurrency == 0 {
		opts.LoginConcurrency = 4
	}
	if opts.LoginConcurrency < 1 || opts.LoginConcurrency > 64 {
		return nil, errors.New("invalid login concurrency")
	}
	if opts.LoginRateLimit == 0 {
		opts.LoginRateLimit = 10
	}
	if opts.LoginRateWindow == 0 {
		opts.LoginRateWindow = time.Minute
	}
	if opts.LoginIPCapacity == 0 {
		opts.LoginIPCapacity = 4096
	}
	if opts.LoginRateLimit < 1 || opts.LoginIPCapacity < 1 || opts.LoginRateWindow <= 0 {
		return nil, errors.New("invalid login rate options")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Handler{opts: opts, publicURL: u, kdfSlots: make(chan struct{}, opts.LoginConcurrency), limiter: newIPLimiter(opts.LoginRateLimit, opts.LoginRateWindow, opts.LoginIPCapacity, opts.Now)}, nil
}

func validatePublicURL(raw string, dev bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid public DNS URL")
	}
	if u.Scheme != "https" {
		host := u.Hostname()
		if !dev || u.Scheme != "http" || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
			return nil, errors.New("public DNS URL must use https")
		}
	}
	return u, nil
}
func validCookieName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		h.serveAPI(w, r)
		return
	}
	if isPprofPath(r.URL.Path) {
		if !h.opts.EnablePprof || h.opts.Legacy == nil {
			http.NotFound(w, r)
			return
		}
		h.serveLegacy(w, r)
		return
	}
	if r.URL.Path == "/metrics" || strings.HasPrefix(r.URL.Path, "/plugins/") {
		if h.opts.Legacy == nil {
			http.NotFound(w, r)
			return
		}
		h.serveLegacy(w, r)
		return
	}
	if r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
		return
	}
	if h.opts.Assets == nil && isPanelPath(r.URL.Path) {
		http.Error(w, "UI assets unavailable; rebuild with -tags ui", http.StatusNotFound)
		return
	}
	if h.serveStatic(w, r) {
		return
	}
	http.NotFound(w, r)
}

func isPprofPath(p string) bool { return p == "/debug/pprof/" || strings.HasPrefix(p, "/debug/pprof/") }
func (h *Handler) serveLegacy(w http.ResponseWriter, r *http.Request) {
	ss, u, ok := h.auth(w, r)
	if !ok {
		return
	}
	if u.Role != control.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if isWrite(r.Method) && !h.writeAuthorized(r, &ss) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	h.opts.Legacy.ServeHTTP(w, r)
}

func (h *Handler) serveAPI(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") && r.URL.Path != "/api/v1" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if p == "" {
		p = "/"
	}
	if p == "/session" {
		h.session(w, r)
		return
	}
	ss, u, ok := h.auth(w, r)
	if !ok {
		return
	}
	if isWrite(r.Method) && !h.writeAuthorized(r, &ss) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if p == "/me" && r.Method == http.MethodGet {
		q, err := h.opts.Control.CurrentQuota(r.Context(), u.ID)
		if err != nil {
			h.serviceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": u, "quota": q})
		return
	}
	if strings.HasPrefix(p, "/me/") {
		h.me(w, r, ss, u, strings.TrimPrefix(p, "/me"))
		return
	}
	if strings.HasPrefix(p, "/admin/") || p == "/admin" {
		if u.Role != control.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		h.admin(w, r, u, strings.TrimPrefix(p, "/admin"))
		return
	}
	writeError(w, http.StatusNotFound, "not_found")
}

type sessionResponse struct {
	User      control.User `json:"user"`
	CSRFToken string       `json:"csrf_token"`
	ExpiresAt time.Time    `json:"expires_at"`
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		if !h.originOK(r) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		if len(in.Username) > 128 || len(in.Password) < 12 || len(in.Password) > 1024 {
			writeError(w, http.StatusBadRequest, "invalid_input")
			return
		}
		if !h.limiter.allow(h.clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		if !h.acquireKDF(w) {
			return
		}
		defer h.releaseKDF()
		ss, token, err := h.opts.Control.Login(r.Context(), in.Username, in.Password, h.opts.SessionTTL)
		if err != nil {
			h.serviceError(w, err)
			return
		}
		h.setCookie(w, token, ss.ExpiresAt)
		u, err := h.opts.Control.GetUser(r.Context(), ss.UserID)
		if err != nil {
			_ = h.opts.Control.RevokeSession(context.Background(), ss.UserID, ss.ID)
			h.clearCookie(w)
			h.serviceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sessionResponse{u, ss.CSRFToken, ss.ExpiresAt})
	case http.MethodGet:
		ss, u, ok := h.auth(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, sessionResponse{u, ss.CSRFToken, ss.ExpiresAt})
	case http.MethodDelete:
		ss, _, ok := h.auth(w, r)
		if !ok {
			return
		}
		if !h.writeAuthorized(r, &ss) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if err := h.opts.Control.RevokeSession(r.Context(), ss.UserID, ss.ID); err != nil {
			h.serviceError(w, err)
			return
		}
		h.clearCookie(w)
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) auth(w http.ResponseWriter, r *http.Request) (control.Session, control.User, bool) {
	totalCookieBytes := 0
	for _, v := range r.Header.Values("Cookie") {
		totalCookieBytes += len(v)
	}
	if totalCookieBytes > 4096 {
		writeError(w, http.StatusUnauthorized, "invalid_credential")
		return control.Session{}, control.User{}, false
	}
	c, err := r.Cookie(h.opts.CookieName)
	if err != nil || len(c.Value) > 512 {
		writeError(w, http.StatusUnauthorized, "invalid_credential")
		return control.Session{}, control.User{}, false
	}
	ss, u, err := h.opts.Control.AuthenticateSession(r.Context(), c.Value)
	if err != nil {
		if errors.Is(err, control.ErrInvalidCredential) || errors.Is(err, control.ErrForbidden) {
			writeError(w, http.StatusUnauthorized, "invalid_credential")
		} else {
			h.serviceError(w, err)
		}
		return control.Session{}, control.User{}, false
	}
	return ss, u, true
}
func (h *Handler) originOK(r *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Origin")), []byte(h.opts.PanelOrigin)) == 1
}
func (h *Handler) writeAuthorized(r *http.Request, ss *control.Session) bool {
	if !h.originOK(r) || ss == nil {
		return false
	}
	v := r.Header.Get("X-CSRF-Token")
	return len(v) > 0 && len(v) <= 256 && subtle.ConstantTimeCompare([]byte(v), []byte(ss.CSRFToken)) == 1
}
func isWrite(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}
func (h *Handler) acquireKDF(w http.ResponseWriter) bool {
	select {
	case h.kdfSlots <- struct{}{}:
		return true
	default:
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return false
	}
}
func (h *Handler) releaseKDF() { <-h.kdfSlots }

func (h *Handler) setCookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: h.opts.CookieName, Value: value, Path: "/", Expires: expires, MaxAge: int(expires.Sub(h.opts.Now()).Seconds()), HttpOnly: true, Secure: h.opts.SecureCookies, SameSite: http.SameSiteLaxMode})
}
func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: h.opts.CookieName, Value: "", Path: "/", Expires: time.Unix(1, 0), MaxAge: -1, HttpOnly: true, Secure: h.opts.SecureCookies, SameSite: http.SameSiteLaxMode})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request, ss control.Session, u control.User, p string) {
	switch p {
	case "/password":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var in struct {
			Current string `json:"current_password"`
			New     string `json:"new_password"`
		}
		if decodeJSON(w, r, &in) != nil {
			return
		}
		if len(in.Current) < 12 || len(in.Current) > 1024 || len(in.New) < 12 || len(in.New) > 1024 {
			writeError(w, http.StatusBadRequest, "invalid_input")
			return
		}
		if !h.acquireKDF(w) {
			return
		}
		defer h.releaseKDF()
		if err := h.opts.Control.ChangePassword(r.Context(), u.ID, in.Current, in.New); err != nil {
			h.serviceError(w, err)
			return
		}
		h.clearCookie(w)
		w.WriteHeader(http.StatusNoContent)
		return
	case "/credentials":
		h.credentials(w, r, u.ID, u.ID, "")
		return
	case "/usage":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		h.usage(w, r, u.ID, false)
		return
	case "/device-usage":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		h.deviceUsage(w, r, u.ID)
		return
	case "/stats":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		h.stats(w, r, u.ID)
		return
	case "/queries":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		h.queries(w, r, u.ID)
		return
	}
	if strings.HasPrefix(p, "/credentials/") {
		h.credentials(w, r, u.ID, u.ID, strings.TrimPrefix(p, "/credentials/"))
		return
	}
	writeError(w, http.StatusNotFound, "not_found")
}

func (h *Handler) admin(w http.ResponseWriter, r *http.Request, admin control.User, p string) {
	if p == "/users" {
		if r.Method == http.MethodGet {
			pg, ok := parsePage(w, r)
			if !ok {
				return
			}
			v, err := h.opts.Control.ListUsers(r.Context(), pg)
			if err != nil {
				h.serviceError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, v)
			return
		}
		if r.Method == http.MethodPost {
			var in control.UserSpec
			if decodeJSON(w, r, &in) != nil {
				return
			}
			if !validSpec(in) {
				writeError(w, http.StatusBadRequest, "invalid_input")
				return
			}
			if !h.acquireKDF(w) {
				return
			}
			defer h.releaseKDF()
			v, err := h.opts.Control.CreateUser(r.Context(), admin.ID, in)
			if err != nil {
				h.serviceError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, v)
			return
		}
		methodNotAllowed(w)
		return
	}
	if p == "/usage" && r.Method == http.MethodGet {
		h.usage(w, r, r.URL.Query().Get("user_id"), true)
		return
	}
	if p == "/stats" && r.Method == http.MethodGet {
		h.stats(w, r, r.URL.Query().Get("user_id"))
		return
	}
	if p == "/queries" && r.Method == http.MethodGet {
		h.queries(w, r, r.URL.Query().Get("user_id"))
		return
	}
	if p == "/audit" && r.Method == http.MethodGet {
		from, to, ok := parseRange(w, r, h.opts.Now())
		if !ok {
			return
		}
		pg, ok := parsePage(w, r)
		if !ok {
			return
		}
		v, err := h.opts.Control.ListAudit(r.Context(), from, to, pg)
		if err != nil {
			h.serviceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
		return
	}
	if p == "/system" && r.Method == http.MethodGet {
		if h.opts.SystemInfo == nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		v, err := h.opts.SystemInfo(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		v.PublicDNSURL = h.opts.PublicDNSURL
		if v.Config.DNSProtocols == nil {
			v.Config.DNSProtocols = []string{}
		}
		writeJSON(w, http.StatusOK, v)
		return
	}
	if !strings.HasPrefix(p, "/users/") {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	parts := strings.Split(strings.TrimPrefix(p, "/users/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	userID := parts[0]
	if len(parts) == 1 {
		if r.Method == http.MethodGet {
			u, err := h.opts.Control.GetUser(r.Context(), userID)
			if err != nil {
				h.serviceError(w, err)
				return
			}
			q, err := h.opts.Control.CurrentQuota(r.Context(), userID)
			if err != nil {
				h.serviceError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"user": u, "quota": q})
			return
		}
		if r.Method == http.MethodPatch {
			var in control.UserPatch
			if decodeJSON(w, r, &in) != nil {
				return
			}
			if !validPatch(in) {
				writeError(w, http.StatusBadRequest, "invalid_input")
				return
			}
			v, err := h.opts.Control.UpdateUser(r.Context(), admin.ID, userID, in)
			if err != nil {
				h.serviceError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, v)
			return
		}
		methodNotAllowed(w)
		return
	}
	if len(parts) == 2 && parts[1] == "password" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var in struct {
			New string `json:"new_password"`
		}
		if decodeJSON(w, r, &in) != nil {
			return
		}
		if len(in.New) < 12 || len(in.New) > 1024 {
			writeError(w, http.StatusBadRequest, "invalid_input")
			return
		}
		if !h.acquireKDF(w) {
			return
		}
		defer h.releaseKDF()
		if err := h.opts.Control.SetPassword(r.Context(), admin.ID, userID, in.New); err != nil {
			h.serviceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[1] == "device-usage" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		h.deviceUsage(w, r, userID)
		return
	}
	if len(parts) >= 2 && parts[1] == "credentials" {
		rest := ""
		if len(parts) > 2 {
			rest = strings.Join(parts[2:], "/")
		}
		h.credentials(w, r, admin.ID, userID, rest)
		return
	}
	writeError(w, http.StatusNotFound, "not_found")
}

type issuedResponse struct {
	control.IssuedCredential
	DNSURL string `json:"doh_url"`
}

func (h *Handler) credentials(w http.ResponseWriter, r *http.Request, actor, userID, rest string) {
	if rest == "" {
		if r.Method == http.MethodGet {
			pg, ok := parsePage(w, r)
			if !ok {
				return
			}
			v, err := h.opts.Control.ListCredentials(r.Context(), userID, pg)
			if err != nil {
				h.serviceError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, v)
			return
		}
		if r.Method == http.MethodPost {
			var in struct {
				Name      string     `json:"name"`
				ExpiresAt *time.Time `json:"expires_at,omitempty"`
			}
			if decodeJSON(w, r, &in) != nil {
				return
			}
			expires := time.Time{}
			if in.ExpiresAt != nil {
				expires = *in.ExpiresAt
			}
			v, err := h.opts.Control.CreateCredential(r.Context(), actor, userID, in.Name, expires)
			if err != nil {
				h.serviceError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, issuedResponse{v, h.dohURL(v.Token)})
			return
		}
		methodNotAllowed(w)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := h.opts.Control.RevokeCredential(r.Context(), actor, userID, parts[0]); err != nil {
			h.serviceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[1] == "rotate" && r.Method == http.MethodPost {
		v, err := h.opts.Control.RotateCredential(r.Context(), actor, userID, parts[0])
		if err != nil {
			h.serviceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, issuedResponse{v, h.dohURL(v.Token)})
		return
	}
	writeError(w, http.StatusNotFound, "not_found")
}
func (h *Handler) dohURL(token string) string {
	u := *h.publicURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + url.PathEscape(token)
	return u.String()
}

func (h *Handler) usage(w http.ResponseWriter, r *http.Request, userID string, admin bool) {
	from, to, ok := parseRange(w, r, h.opts.Now())
	if !ok {
		return
	}
	pg, ok := parsePage(w, r)
	if !ok {
		return
	}
	v, err := h.opts.Control.Usage(r.Context(), userID, from, to, pg)
	if err != nil {
		h.serviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h *Handler) deviceUsage(w http.ResponseWriter, r *http.Request, userID string) {
	from, to, ok := parseRange(w, r, h.opts.Now())
	if !ok {
		return
	}
	pg, ok := parsePage(w, r)
	if !ok {
		return
	}
	credentialID := r.URL.Query().Get("credential_id")
	v, err := h.opts.Control.CredentialUsage(r.Context(), userID, credentialID, from, to, pg)
	if err != nil {
		h.serviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h *Handler) stats(w http.ResponseWriter, r *http.Request, userID string) {
	if h.opts.Telemetry == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	from, to, ok := parseRange(w, r, h.opts.Now())
	if !ok {
		return
	}
	v, err := h.opts.Telemetry.Snapshot(r.Context(), userID, from, to)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if v.RcodeCounts == nil {
		v.RcodeCounts = map[string]uint64{}
	}
	if v.Series == nil {
		v.Series = []telemetry.SeriesPoint{}
	}
	if v.Upstreams == nil {
		v.Upstreams = []telemetry.UpstreamStats{}
	}
	writeJSON(w, http.StatusOK, v)
}
func (h *Handler) queries(w http.ResponseWriter, r *http.Request, userID string) {
	if h.opts.Telemetry == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	from, to, ok := parseRange(w, r, h.opts.Now())
	if !ok {
		return
	}
	pg, ok := parsePage(w, r)
	if !ok {
		return
	}
	v, err := h.opts.Telemetry.Queries(r.Context(), userID, from, to, telemetry.Page{Limit: pg.Limit, Cursor: pg.Cursor})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if v.Items == nil {
		v.Items = []telemetry.QueryRecord{}
	}
	writeJSON(w, http.StatusOK, v)
}

func validSpec(v control.UserSpec) bool {
	return len(v.Password) >= 12 && len(v.Password) <= 1024 && v.Limit > 0 && v.Limit <= maxSafeInteger && v.QPS > 0 && v.QPS <= 1_000_000 && v.Burst <= 1_000_000 && v.MaxCredentials > 0 && v.MaxCredentials <= 10_000 && (v.Role == "" || v.Role == control.RoleAdmin || v.Role == control.RoleUser)
}
func validPatch(v control.UserPatch) bool {
	return (v.Limit == nil || *v.Limit > 0 && *v.Limit <= maxSafeInteger) && (v.QPS == nil || *v.QPS > 0 && *v.QPS <= 1_000_000) && (v.Burst == nil || *v.Burst <= 1_000_000) && (v.MaxCredentials == nil || *v.MaxCredentials > 0 && *v.MaxCredentials <= 10_000)
}
func parsePage(w http.ResponseWriter, r *http.Request) (control.Page, bool) {
	q := r.URL.Query()
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 1000 {
			writeError(w, http.StatusBadRequest, "invalid_input")
			return control.Page{}, false
		}
		limit = v
	}
	cursor := q.Get("cursor")
	if len(cursor) > maxCursor {
		writeError(w, http.StatusBadRequest, "invalid_input")
		return control.Page{}, false
	}
	return control.Page{Limit: limit, Cursor: cursor}, true
}
func parseRange(w http.ResponseWriter, r *http.Request, now time.Time) (time.Time, time.Time, bool) {
	to := now.UTC()
	from := to.Add(-24 * time.Hour)
	var err error
	if raw := r.URL.Query().Get("from"); raw != "" {
		from, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input")
			return time.Time{}, time.Time{}, false
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		to, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input")
			return time.Time{}, time.Time{}, false
		}
	}
	if !from.Before(to) || to.Sub(from) > 31*24*time.Hour {
		writeError(w, http.StatusBadRequest, "invalid_input")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input")
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_input")
		return errors.New("trailing JSON")
	}
	return nil
}
func (h *Handler) serviceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, control.ErrInvalidInput):
		writeError(w, 400, "invalid_input")
	case errors.Is(err, control.ErrInvalidCredential):
		writeError(w, 401, "invalid_credential")
	case errors.Is(err, control.ErrForbidden):
		writeError(w, 403, "forbidden")
	case errors.Is(err, control.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, control.ErrConflict):
		writeError(w, 409, "conflict")
	case errors.Is(err, control.ErrRateLimited):
		writeError(w, 429, "rate_limited")
	case errors.Is(err, control.ErrQuotaExceeded):
		writeError(w, 429, "quota_exceeded")
	default:
		writeError(w, 503, "unavailable")
	}
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST, PATCH, DELETE")
	writeError(w, http.StatusMethodNotAllowed, "invalid_input")
}

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request) bool {
	if h.opts.Assets == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		h.staticFile(w, r, strings.TrimPrefix(r.URL.Path, "/"))
		return true
	}
	if r.URL.Path == "/login" || r.URL.Path == "/app" || strings.HasPrefix(r.URL.Path, "/app/") || r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
		h.staticFile(w, r, "index.html")
		return true
	}
	return false
}
func isPanelPath(p string) bool {
	return p == "/login" || p == "/app" || strings.HasPrefix(p, "/app/") || p == "/admin" || strings.HasPrefix(p, "/admin/")
}
func (h *Handler) staticFile(w http.ResponseWriter, r *http.Request, name string) {
	if strings.Contains(name, "..") || strings.Contains(name, "\\") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(h.opts.Assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func (h *Handler) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, e := netip.ParseAddr(host)
	if e == nil && h.trusted(addr) {
		raw := r.Header.Get(h.opts.ClientIPHeader)
		if !strings.EqualFold(h.opts.ClientIPHeader, "X-Forwarded-For") {
			if strings.Contains(raw, ",") {
				return addr.String()
			}
			if parsed, e := netip.ParseAddr(strings.TrimSpace(raw)); e == nil {
				return parsed.String()
			}
			return addr.String()
		}
		parts := strings.Split(raw, ",")
		chain := make([]netip.Addr, 0, len(parts)+1)
		for _, part := range parts {
			parsed, e := netip.ParseAddr(strings.TrimSpace(part))
			if e != nil {
				return addr.String()
			}
			chain = append(chain, parsed)
		}
		chain = append(chain, addr)
		for i := len(chain) - 1; i > 0; i-- {
			if !h.trusted(chain[i]) {
				return chain[i].String()
			}
		}
		if len(chain) > 0 {
			return chain[0].String()
		}
	}
	if e == nil {
		return addr.String()
	}
	return "invalid"
}
func (h *Handler) trusted(a netip.Addr) bool {
	for _, p := range h.opts.TrustedProxyCIDRs {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

type ipEntry struct {
	start time.Time
	count int
}
type ipLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	capacity int
	now      func() time.Time
	entries  map[string]ipEntry
}

func newIPLimiter(limit int, window time.Duration, capacity int, now func() time.Time) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, capacity: capacity, now: now, entries: make(map[string]ipEntry)}
}
func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if e, ok := l.entries[ip]; ok {
		if now.Sub(e.start) >= l.window {
			l.entries[ip] = ipEntry{now, 1}
			return true
		}
		if e.count >= l.limit {
			return false
		}
		e.count++
		l.entries[ip] = e
		return true
	}
	if len(l.entries) >= l.capacity {
		for k, e := range l.entries {
			if now.Sub(e.start) >= l.window {
				delete(l.entries, k)
			}
		}
		if len(l.entries) >= l.capacity {
			return false
		}
	}
	l.entries[ip] = ipEntry{now, 1}
	return true
}

var _ http.Handler = (*Handler)(nil)
