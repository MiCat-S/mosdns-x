package coremain_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/internal/control"
	_ "github.com/pmkol/mosdns-x/plugin"
)

func lifecycleUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return p
}

func lifecycleTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func lifecyclePlugins() []coremain.PluginConfig {
	return []coremain.PluginConfig{{Tag: "answer", Type: "blackhole", Args: map[string]any{"rcode": 0}}}
}

func TestRunContextStopsUDPAndReleasesPort(t *testing.T) {
	port := lifecycleUDPPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cfg := &coremain.Config{Plugins: lifecyclePlugins(), Servers: []coremain.ServerConfig{{Exec: "answer", Listeners: []*coremain.ServerListenerConfig{{Protocol: "udp", Addr: addr}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coremain.RunMosdnsContext(ctx, cfg) }()
	client := &dns.Client{Timeout: 50 * time.Millisecond}
	msg := new(dns.Msg)
	msg.SetQuestion("example.test.", dns.TypeA)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, err := client.Exchange(msg, addr); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("UDP server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
	rebound, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("port was not released: %v", err)
	}
	rebound.Close()
}

func TestFailedSecondListenerReleasesFirstAndDatabases(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "control.db")
	statsPath := filepath.Join(dir, "stats.db")
	store, err := control.Open(dbPath, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.InitializeAdmin(context.Background(), control.UserSpec{Username: "admin", Password: "lifecycle-password", Role: control.RoleAdmin, Enabled: true, Period: control.PeriodDaily, Timezone: "UTC", Limit: 100, QPS: 10, MaxCredentials: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	first := fmt.Sprintf("127.0.0.1:%d", lifecycleTCPPort(t))
	blocked, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	api := fmt.Sprintf("127.0.0.1:%d", lifecycleTCPPort(t))
	cfg := &coremain.Config{Control: &coremain.ControlConfig{Database: dbPath, StatsDatabase: statsPath, PublicDNSURL: "http://" + first + "/dns-query", PanelOrigin: "http://127.0.0.1", Development: true}, API: coremain.APIConfig{HTTP: api}, Plugins: lifecyclePlugins(), Servers: []coremain.ServerConfig{{Exec: "answer", Listeners: []*coremain.ServerListenerConfig{{Protocol: "http", Addr: first, URLPath: "/dns-query"}, {Protocol: "http", Addr: blocked.Addr().String(), URLPath: "/dns-query"}}}}}
	if err := coremain.RunMosdnsContext(context.Background(), cfg); err == nil {
		t.Fatal("expected second listener bind failure")
	}
	l, err := net.Listen("tcp", first)
	if err != nil {
		t.Fatalf("first listener leaked: %v", err)
	}
	l.Close()
	reopened, err := control.Open(dbPath, control.Options{})
	if err != nil {
		t.Fatalf("control database remained locked: %v", err)
	}
	reopened.Close()
}
