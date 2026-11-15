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
