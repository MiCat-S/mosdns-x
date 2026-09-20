package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The verbatim YAML path exists to keep the documented defaults. A round trip
// through viper would drop every comment and reorder the keys.
func TestGenCfgKeepsCommentsAndOrder(t *testing.T) {
	out := filepath.Join(t.TempDir(), "config.yaml")
	if err := genCfg(out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != templateConfig {
		t.Fatal("generated YAML differs from the template")
	}
	if !strings.Contains(string(got), "# Query timeout in seconds. 0 selects 5.") {
		t.Fatal("comments were stripped")
	}
	if strings.Index(string(got), "log:") > strings.Index(string(got), "plugins:") {
		t.Fatal("keys were reordered")
	}
}

// A generated template must never enable the control panel. mosdns refuses to
// start when control is configured without an administrator, so shipping one
// would break a fresh install.
func TestTemplateLeavesControlDisabled(t *testing.T) {
	for _, key := range []string{"control:", "panel_origin", "public_dns_url"} {
		if strings.Contains(templateConfig, key) {
			t.Fatalf("template must not configure %q", key)
		}
	}
}

func TestGenCfgBacksUpExistingFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "config.yaml")
	const original = "servers: [] # do not lose me\n"
	if err := os.WriteFile(out, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := genCfg(out); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.yaml.bak.") {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one", backups)
	}
	saved, err := os.ReadFile(filepath.Join(dir, backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != original {
		t.Fatalf("backup = %q, want the original contents", saved)
	}
}

func TestGenCfgRefusesIrregularTarget(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(filepath.Join(dir, "elsewhere.yaml"), out); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := genCfg(out); err == nil {
		t.Fatal("a symlinked target was replaced")
	}
}

// Non-YAML extensions still go through viper, which performs the conversion.
func TestGenCfgConvertsOtherExtensions(t *testing.T) {
	out := filepath.Join(t.TempDir(), "config.json")
	if err := genCfg(out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "forward_google") {
		t.Fatalf("converted output lost content: %s", got)
	}
}
