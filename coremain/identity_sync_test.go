package coremain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
	"go.uber.org/zap"
)

func TestIdentitySyncConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ControlConfig)
		valid  bool
	}{
		{"master", func(c *ControlConfig) { c.IdentitySync.Mode = "master"; c.IdentitySync.MasterURL = "" }, true},
		{"follower", func(*ControlConfig) {}, true},
		{"development loopback", func(c *ControlConfig) { c.Development = true; c.IdentitySync.MasterURL = "http://127.0.0.1:8080" }, true},
		{"bbolt", func(c *ControlConfig) { c.Storage.Driver = "bbolt" }, false},
		{"bad mode", func(c *ControlConfig) { c.IdentitySync.Mode = "standalone" }, false},
		{"short token", func(c *ControlConfig) { c.IdentitySync.Token = "secret" }, false},
		{"header injection", func(c *ControlConfig) { c.IdentitySync.Token = strings.Repeat("x", 32) + "\n" }, false},
		{"plain HTTP", func(c *ControlConfig) { c.IdentitySync.MasterURL = "http://master.example" }, false},
		{"embedded credentials", func(c *ControlConfig) { c.IdentitySync.MasterURL = "https://a:b@master.example" }, false},
		{"query", func(c *ControlConfig) { c.IdentitySync.MasterURL = "https://master.example?" }, false},
		{"path", func(c *ControlConfig) { c.IdentitySync.MasterURL = "https://master.example/path" }, false},
		{"negative interval", func(c *ControlConfig) { c.IdentitySync.IntervalSeconds = -1 }, false},
		{"excessive interval", func(c *ControlConfig) { c.IdentitySync.IntervalSeconds = 3601 }, false},
		{"master follower options", func(c *ControlConfig) { c.IdentitySync.Mode = "master" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ControlConfig{Storage: StorageConfig{Driver: "mysql"}, IdentitySync: &IdentitySyncConfig{Mode: "follower", Token: strings.Repeat("x", 32), MasterURL: "https://master.example"}}
			tc.change(c)
			if err := validateIdentitySyncConfig(c); (err == nil) != tc.valid {
				t.Fatalf("error=%v valid=%v", err, tc.valid)
			}
			if identitySyncInterval(c.IdentitySync) != 30*time.Second && c.IdentitySync.IntervalSeconds == 0 {
				t.Fatal("wrong default interval")
			}
		})
	}
}

func TestOfflineFollowerIdentityCommandsRefusedBeforeOpeningStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	config := "control:\n  storage:\n    driver: mysql\n    mysql:\n      dsn: unused\n  identity_sync:\n    mode: follower\n"
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"init-admin", "reset-password"} {
		_, err := executeControl(t, context.Background(), "password-not-to-log\n", command, "--config", path, "--username", "root")
		if !errors.Is(err, control.ErrIdentityManagedByMaster) {
			t.Fatalf("%s error=%v", command, err)
		}
	}
}

func TestIdentityFetchUsesExportOnlyAuthorizationAndSafeErrors(t *testing.T) {
	token := strings.Repeat("node-secret-", 4)
	snapshot := control.IdentitySnapshot{Version: 1, Users: []control.IdentityUser{{User: control.User{ID: "admin", Username: "admin", Role: control.RoleAdmin, Enabled: true, Period: control.PeriodDaily, Timezone: "UTC", Limit: 100, QPS: 10, MaxCredentials: 2, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, PasswordSalt: make([]byte, 16), PasswordHash: make([]byte, 32), PasswordVersion: 1}}, Credentials: []control.IdentityCredential{}}
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/identity/snapshot" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong export request")
		}
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(token + " sensitive backend error"))
			return
		}
		_ = json.NewEncoder(w).Encode(snapshot)
	}))
	defer server.Close()
	cfg := &IdentitySyncConfig{MasterURL: server.URL, Token: token}
	if v, code, _ := fetchIdentitySnapshot(context.Background(), server.Client(), cfg); code != "" || len(v.Users) != 1 {
		t.Fatalf("success: users=%d code=%s", len(v.Users), code)
	}
	status = 403
	if _, code, _ := fetchIdentitySnapshot(context.Background(), server.Client(), cfg); code != "http_403" {
		t.Fatalf("unsafe/wrong error: %s", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, code, _ := fetchIdentitySnapshot(ctx, server.Client(), cfg); code != "fetch_failed" {
		t.Fatalf("canceled: %s", code)
	}
	status = 200
	snapshot.Users = nil
	if _, code, _ := fetchIdentitySnapshot(context.Background(), server.Client(), cfg); code != "invalid_snapshot" {
		t.Fatalf("invalid: %s", code)
	}
}

func TestInvalidOriginsRejectedBeforeFollowerBootstrap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ControlConfig)
	}{
		{"panel HTTP", func(c *ControlConfig) { c.PanelOrigin = "http://panel.example.test" }},
		{"panel credentials", func(c *ControlConfig) { c.PanelOrigin = "https://secret@panel.example.test" }},
		{"panel path", func(c *ControlConfig) { c.PanelOrigin = "https://panel.example.test/path" }},
		{"DNS HTTP", func(c *ControlConfig) { c.PublicDNSURL = "http://dns.example.test/dns-query" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validControlConfig()
			cfg.Control.Storage = StorageConfig{Driver: "mysql", MySQL: MySQLConfig{DSN: "unreachable"}}
			cfg.Control.IdentitySync = &IdentitySyncConfig{Mode: "follower", Token: strings.Repeat("x", 32), MasterURL: "https://master.example"}
			tc.change(cfg.Control)
			if _, _, err := validateControlConfig(cfg); err == nil {
				t.Fatal("invalid origin accepted before import")
			}
		})
	}
}

// The managed runtime validates a YAML clone of the configuration; a follower
// without nodes must survive that round trip.
func TestFollowerConfigurationSurvivesClone(t *testing.T) {
	cfg := &Config{Control: &ControlConfig{Storage: StorageConfig{Driver: "mysql"}, IdentitySync: &IdentitySyncConfig{Mode: "follower", Token: strings.Repeat("x", 32), MasterURL: "https://master.example"}}}
	clone, err := cloneConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIdentitySyncConfig(clone.Control); err != nil {
		t.Fatal(err)
	}
	clone.Control.IdentitySync.Nodes = []control.DNSNode{}
	if err := validateIdentitySyncConfig(clone.Control); err != nil {
		t.Fatalf("empty node list rejected: %v", err)
	}
}

func TestIdentitySyncDNSNodesConfiguration(t *testing.T) {
	nodes := []control.DNSNode{{Name: "广州", PublicDNSURL: "https://gz.example/dns-query"}}
	c := &ControlConfig{Storage: StorageConfig{Driver: "mysql"}, IdentitySync: &IdentitySyncConfig{Mode: "master", Token: strings.Repeat("x", 32), Nodes: nodes}}
	if err := validateIdentitySyncConfig(c); err != nil {
		t.Fatal(err)
	}
	c.IdentitySync.Mode = "follower"
	c.IdentitySync.MasterURL = "https://master.example"
	if err := validateIdentitySyncConfig(c); err == nil {
		t.Fatal("follower-local nodes accepted")
	}
}

func TestFollowerKeepsDNSNodesWhenSourceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	nodes := []control.DNSNode{{Name: "广州", PublicDNSURL: "https://gz.example/dns-query"}, {Name: "成都", PublicDNSURL: "https://cd.example/dns-query"}}
	follower := &identityFollower{nodes: nodes, cfg: &IdentitySyncConfig{MasterURL: server.URL, Token: strings.Repeat("x", 32)}, client: server.Client(), logger: zap.NewNop()}
	if err := follower.pull(context.Background()); err == nil {
		t.Fatal("source error swallowed")
	}
	if got := follower.dnsNodes(); len(got) != 2 || got[1] != nodes[1] {
		t.Fatal("offline source cleared cached list")
	}
}

func TestIdentitySyncNodesSurviveManagedConfiguration(t *testing.T) {
	nodes := []control.DNSNode{{Name: "广州", PublicDNSURL: "https://gz.example/dns-query"}, {Name: "成都", PublicDNSURL: "https://cd.example/dns-query"}}
	for _, tc := range []struct {
		name, mode string
		nodes      []control.DNSNode
	}{
		{"follower absent", "follower", nil},
		{"follower empty", "follower", []control.DNSNode{}},
		{"master empty", "master", []control.DNSNode{}},
		{"master nodes", "master", nodes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := managedRuntimeTestConfig(t)
			cfg.Control.Storage = StorageConfig{Driver: "mysql", MySQL: MySQLConfig{DSN: "unused-managed-roundtrip-database"}}
			cfg.Control.IdentitySync = &IdentitySyncConfig{Mode: tc.mode, Token: strings.Repeat("x", 32), Nodes: tc.nodes}
			if tc.mode == "follower" {
				cfg.Control.IdentitySync.MasterURL = "https://master.example"
			}
			effective, base, _, view, _, err := prepareManagedRuntimeConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			// Startup and each managed apply clone the control config through YAML.
			// Exercise that real path, including a second apply of the persisted view.
			reapplied, err := applyManagedConfig(base, view)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range []*Config{base, effective, reapplied} {
				if _, _, err := validateControlConfig(candidate); err != nil {
					t.Fatal(err)
				}
				got := candidate.Control.IdentitySync.Nodes
				if len(got) != len(tc.nodes) {
					t.Fatalf("node count changed: got=%d want=%d", len(got), len(tc.nodes))
				}
				for i, node := range tc.nodes {
					if got[i] != node {
						t.Fatalf("node %d changed after managed roundtrip", i)
					}
				}
			}
		})
	}
}
