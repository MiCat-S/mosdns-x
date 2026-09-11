package coremain

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/safe_close"
)

type testDNSHandler struct{}

func (testDNSHandler) ServeDNS(context.Context, *dns.Msg, *query_context.RequestMeta) (*dns.Msg, error) {
	return new(dns.Msg), nil
}

func validControlConfig() *Config {
	return &Config{
		Control: &ControlConfig{
			Database:      "/tmp/control.db",
			StatsDatabase: "/tmp/stats.db",
			PublicDNSURL:  "https://dns.example.test/dns-query",
			PanelOrigin:   "https://panel.example.test",
		},
		API:     APIConfig{HTTP: "127.0.0.1:0"},
		Servers: []ServerConfig{{Listeners: []*ServerListenerConfig{{Protocol: "doh", Addr: "0.0.0.0:0", URLPath: "/dns-query"}}}},
	}
}

func TestValidateControlConfig(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{name: "valid"},
		{name: "same database", change: func(c *Config) { c.Control.StatsDatabase = c.Control.Database }, want: "must differ"},
		{name: "public api", change: func(c *Config) { c.API.HTTP = "0.0.0.0:8080" }, want: "loopback"},
		{name: "public raw dns", change: func(c *Config) { c.Servers[0].Listeners[0].Protocol = "udp" }, want: "raw DNS"},
		{name: "proxy protocol", change: func(c *Config) { c.Servers[0].Listeners[0].ProxyProtocol = true }, want: "proxy_protocol"},
		{name: "path mismatch", change: func(c *Config) { c.Servers[0].Listeners[0].URLPath = "/other" }, want: "differs"},
		{name: "development public dns", change: func(c *Config) { c.Control.Development = true }, want: "development public_dns_url"},
		{name: "development panel", change: func(c *Config) {
			c.Control.Development = true
			c.Control.PublicDNSURL = "http://127.0.0.1/dns-query"
			c.Servers[0].Listeners[0].Addr = "127.0.0.1:0"
		}, want: "development panel_origin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validControlConfig()
			if tt.change != nil {
				tt.change(cfg)
			}
			_, _, err := validateControlConfig(cfg)
			if tt.want == "" && err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateControlConfigDefaultsPath(t *testing.T) {
	cfg := validControlConfig()
	cfg.Control.PublicDNSURL = "https://dns.example.test"
	cfg.Servers[0].Listeners[0].URLPath = ""
	u, _, err := validateControlConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/dns-query" || cfg.Servers[0].Listeners[0].URLPath != "/dns-query" {
		t.Fatalf("paths = %q, %q", u.Path, cfg.Servers[0].Listeners[0].URLPath)
	}
}

func TestListenerOwnedWhenSafeCloseAlreadyClosed(t *testing.T) {
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.LocalAddr().String()
	probe.Close()
	m := &Mosdns{logger: zap.NewNop(), sc: safe_close.NewSafeClose()}
	m.sc.SendCloseSignal(nil)
	m.sc.Done()
	if err := m.startServerListener(&ServerListenerConfig{Protocol: "udp", Addr: addr}, testDNSHandler{}); err != nil {
		t.Fatal(err)
	}
	if err := m.shutdown(); err != nil {
		t.Fatal(err)
	}
	rebound, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("listener leaked when Attach was skipped: %v", err)
	}
	rebound.Close()
}
