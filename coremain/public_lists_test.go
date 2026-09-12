package coremain

import (
	"path/filepath"
	"testing"
)

func TestPublicListDirectory(t *testing.T) {
	if got := publicListDirectory(&Config{Control: &ControlConfig{ManagedConfig: "/var/lib/mosdns/managed.yaml", Database: "/etc/mosdns/control.db"}}); got != "/var/lib/mosdns/public-lists" {
		t.Fatalf("managed directory=%q", got)
	}
	if got := publicListDirectory(&Config{Control: &ControlConfig{Database: "/etc/mosdns/control.db"}}); got != "/etc/mosdns/public-lists" {
		t.Fatalf("database directory=%q", got)
	}
	cfg := &Config{Control: &ControlConfig{Storage: StorageConfig{Driver: "mysql"}}, sourcePath: filepath.Join(t.TempDir(), "config.yaml")}
	if got, want := publicListDirectory(cfg), filepath.Join(filepath.Dir(cfg.sourcePath), "public-lists"); got != want {
		t.Fatalf("source directory=%q, want %q", got, want)
	}
}
