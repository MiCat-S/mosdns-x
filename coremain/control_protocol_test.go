package coremain_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/telemetry"
	"github.com/pmkol/mosdns-x/mlog"
	_ "github.com/pmkol/mosdns-x/plugin"
)

func TestControlDoH2DoH3ProtocolIntegration(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, pool := protocolCertificate(t, dir)
	dnsPort := sharedTCPUDPPort(t)
	apiPort := freeTCPPort(t)
	upstream, upstreamAddr, upstreamCount := startProtocolUpstream(t)
	defer upstream.Shutdown()
	dbPath := filepath.Join(dir, "control.db")
	statsPath := filepath.Join(dir, "stats.db")
	store, err := control.Open(dbPath, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.InitializeAdmin(context.Background(), protocolUserSpec("admin", control.RoleAdmin, 1_000))
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(context.Background(), admin.ID, protocolUserSpec("client", control.RoleUser, 4))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateCredential(context.Background(), user.ID, user.ID, "h2", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateCredential(context.Background(), user.ID, user.ID, "h3", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	_, adminSession, err := store.CreateSession(context.Background(), admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	dnsAddr := fmt.Sprintf("127.0.0.1:%d", dnsPort)
	apiAddr := fmt.Sprintf("127.0.0.1:%d", apiPort)
	baseURL := "https://" + dnsAddr + "/dns-query"
	cfg := &coremain.Config{Log: coremainTestLog(), Control: &coremain.ControlConfig{Database: dbPath, StatsDatabase: statsPath, PublicDNSURL: baseURL, PanelOrigin: "https://localhost", QueryLog: true}, API: coremain.APIConfig{HTTP: apiAddr}, Plugins: []coremain.PluginConfig{
		{Tag: "forward", Type: "fast_forward", Args: map[string]any{"upstream": []any{map[string]any{"addr": "udp://" + upstreamAddr}}}},
		{Tag: "cache", Type: "cache", Args: map[string]any{"size": 64}},
		{Tag: "main", Type: "sequence", Args: map[string]any{"exec": []any{"cache", "forward"}}},
	}, Servers: []coremain.ServerConfig{{Exec: "main", Listeners: []*coremain.ServerListenerConfig{{Protocol: "doh", Addr: dnsAddr, Cert: certPath, Key: keyPath, URLPath: "/dns-query"}, {Protocol: "doh3", Addr: dnsAddr, Cert: certPath, Key: keyPath, URLPath: "/dns-query"}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- coremain.RunMosdnsContext(ctx, cfg) }()
	stopped := false
	defer func() {
		if stopped {
			return
		}
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("server shutdown timed out")
		}
	}()
	tlsConfig := &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	h2Transport := &http2.Transport{TLSClientConfig: tlsConfig.Clone()}
	defer h2Transport.CloseIdleConnections()
	h2Client := &http.Client{Transport: h2Transport, Timeout: 5 * time.Second}
	h3Transport := &http3.Transport{TLSClientConfig: tlsConfig.Clone()}
	defer h3Transport.Close()
	h3Client := &http.Client{Transport: h3Transport, Timeout: 5 * time.Second}
	waitProtocolReady(t, h2Client, h3Client, baseURL, runDone)
	queryA := protocolQuery(t, "cached-a.example.")
	queryB := protocolQuery(t, "cached-b.example.")
	if resp := protocolPOST(t, h2Client, baseURL, "", queryA); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing credential status=%d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	malformed := protocolPOST(t, h2Client, baseURL, first.Token, []byte{1, 2, 3})
	if malformed.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed status=%d", malformed.StatusCode)
	}
	malformed.Body.Close()
	protocolDNSResponse(t, protocolPOST(t, h2Client, baseURL, first.Token, queryA), 2)
	protocolDNSResponse(t, protocolGET(t, h3Client, baseURL+"/"+url.PathEscape(second.Token), "", queryA), 3)
	protocolDNSResponse(t, protocolPOST(t, h2Client, baseURL+"/"+url.PathEscape(first.Token), "", queryB), 2)
	protocolDNSResponse(t, protocolGET(t, h3Client, baseURL, second.Token, queryB), 3)
	fifth := protocolPOST(t, h2Client, baseURL, first.Token, queryA)
	if fifth.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("shared quota fifth=%d", fifth.StatusCode)
	}
	fifth.Body.Close()
	if got := upstreamCount.Load(); got != 2 {
		t.Fatalf("upstream queries=%d want 2 (two cache misses)", got)
	}
	apiClient := &http.Client{Timeout: 3 * time.Second}
	stats, queries := waitProtocolTelemetry(t, apiClient, "http://"+apiAddr, adminSession)
	if stats.Completed != 4 || stats.CacheHits != 2 {
		t.Fatalf("stats=%+v", stats)
	}
	if len(queries.Items) != 4 {
		t.Fatalf("query records=%d", len(queries.Items))
	}
	protocols := map[string]bool{}
	cacheHits := 0
	for _, q := range queries.Items {
		protocols[q.Protocol] = true
		if q.CacheHit {
			cacheHits++
		}
	}
	if !protocols["h2"] || !protocols["h3"] || cacheHits != 2 {
		t.Fatalf("query protocols=%v cache_hits=%d", protocols, cacheHits)
	}
	cancel()
	select {
	case err := <-runDone:
		stopped = true
		if err != nil {
			t.Fatalf("run exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop")
	}
	reopened, err := control.Open(dbPath, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	quota, err := reopened.CurrentQuota(context.Background(), user.ID)
	if err != nil || quota.Used != 4 || quota.Remaining != 0 {
		t.Fatalf("persisted quota=%+v err=%v", quota, err)
	}
}

func coremainTestLog() mlog.LogConfig { return mlog.LogConfig{Level: "error"} }

func protocolUserSpec(name string, role control.Role, limit uint64) control.UserSpec {
	return control.UserSpec{Username: name, Password: "protocol-password-" + name, Role: role, Enabled: true, Period: control.PeriodDaily, Timezone: "UTC", Limit: limit, QPS: 100, Burst: 0, MaxCredentials: 4}
}

func protocolCertificate(t *testing.T, dir string) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return certPath, keyPath, pool
}

func sharedTCPUDPPort(t *testing.T) int {
	t.Helper()
	for range 20 {
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := tcp.Addr().(*net.TCPAddr).Port
		udp, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = udp.Close()
			_ = tcp.Close()
			return port
		}
		_ = tcp.Close()
	}
	t.Fatal("cannot reserve shared TCP/UDP port")
	return 0
}
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

func startProtocolUpstream(t *testing.T) (*dns.Server, string, *atomic.Uint64) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count := new(atomic.Uint64)
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		count.Add(1)
		r := new(dns.Msg)
		r.SetReply(q)
		r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: net.IPv4(192, 0, 2, 10)}}
		_ = w.WriteMsg(r)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	return srv, pc.LocalAddr().String(), count
}

func protocolQuery(t *testing.T, name string) []byte {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypeA)
	m.Id = 0
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func protocolPOST(t *testing.T, c *http.Client, target, token string, body []byte) *http.Response {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/dns-message")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func protocolGET(t *testing.T, c *http.Client, target, token string, msg []byte) *http.Response {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("dns", base64.RawURLEncoding.EncodeToString(msg))
	u.RawQuery = q.Encode()
	r, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Accept", "application/dns-message")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func protocolDNSResponse(t *testing.T, resp *http.Response, proto int) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ProtoMajor != proto {
		t.Fatalf("DNS response status=%d proto=%s", resp.StatusCode, resp.Proto)
	}
	wire, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var m dns.Msg
	if err = m.Unpack(wire); err != nil {
		t.Fatal(err)
	}
	if len(m.Answer) != 1 {
		t.Fatalf("answers=%d", len(m.Answer))
	}
}

func waitProtocolReady(t *testing.T, h2, h3 *http.Client, base string, run <-chan error) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	h2ok, h3ok := false, false
	for !h2ok || !h3ok {
		select {
		case err := <-run:
			t.Fatalf("server exited during startup: %v", err)
		case <-deadline.C:
			t.Fatal("protocol listeners not ready")
		case <-ticker.C:
			if !h2ok {
				if r, e := h2.Get(base); e == nil {
					h2ok = r.StatusCode == 401 && r.ProtoMajor == 2
					r.Body.Close()
				}
			}
			if !h3ok {
				if r, e := h3.Get(base); e == nil {
					h3ok = r.StatusCode == 401 && r.ProtoMajor == 3
					r.Body.Close()
				}
			}
		}
	}
}
func waitProtocolTelemetry(t *testing.T, c *http.Client, base, session string) (telemetry.StatsSnapshot, telemetry.QueryPage) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("telemetry did not flush")
		case <-ticker.C:
			req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/admin/stats", nil)
			req.AddCookie(&http.Cookie{Name: "mosdns_session", Value: session})
			resp, err := c.Do(req)
			if err != nil {
				continue
			}
			var stats telemetry.StatsSnapshot
			_ = json.NewDecoder(resp.Body).Decode(&stats)
			resp.Body.Close()
			if stats.Completed < 4 {
				continue
			}
			req, _ = http.NewRequest(http.MethodGet, base+"/api/v1/admin/queries", nil)
			req.AddCookie(&http.Cookie{Name: "mosdns_session", Value: session})
			resp, err = c.Do(req)
			if err != nil {
				continue
			}
			var queries telemetry.QueryPage
			_ = json.NewDecoder(resp.Body).Decode(&queries)
			resp.Body.Close()
			if len(queries.Items) >= 4 {
				return stats, queries
			}
		}
	}
}
