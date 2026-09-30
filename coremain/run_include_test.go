package coremain

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeIncludeTestConfig writes a config at dir/rel that declares one plugin
// tagged tag and includes the given paths verbatim.
func writeIncludeTestConfig(t *testing.T, dir, rel, tag string, includes ...string) {
	t.Helper()
	var b strings.Builder
	if len(includes) > 0 {
		b.WriteString("include:\n")
		for _, include := range includes {
			b.WriteString("  - " + include + "\n")
		}
	}
	b.WriteString("plugins:\n  - tag: " + tag + "\n    type: include_test\n")
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergedConfigIncludes(t *testing.T) {
	tests := []struct {
		name string
		// setup writes the config tree into dir and returns the root config
		// path to load plus the directory to run from.
		setup    func(t *testing.T, dir string) (root, cwd string)
		wantTags []string
		wantErr  []string
	}{
		{
			name: "relative include resolves against the including file",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "main.yaml", "main", "sub/a.yaml")
				writeIncludeTestConfig(t, dir, "sub/a.yaml", "a", "b.yaml")
				writeIncludeTestConfig(t, dir, "sub/b.yaml", "b")
				// Decoys relative to the CWD must not be picked up.
				other := t.TempDir()
				writeIncludeTestConfig(t, other, "sub/a.yaml", "decoy")
				writeIncludeTestConfig(t, other, "b.yaml", "decoy")
				return filepath.Join(dir, "main.yaml"), other
			},
			wantTags: []string{"b", "a", "main"},
		},
		{
			name: "relative root config with relative includes",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "conf/main.yaml", "main", "sub/a.yaml")
				writeIncludeTestConfig(t, dir, "conf/sub/a.yaml", "a")
				return filepath.Join("conf", "main.yaml"), dir
			},
			wantTags: []string{"a", "main"},
		},
		{
			name: "absolute include is used as-is",
			setup: func(t *testing.T, dir string) (string, string) {
				other := t.TempDir()
				writeIncludeTestConfig(t, other, "abs.yaml", "abs")
				writeIncludeTestConfig(t, dir, "main.yaml", "main", filepath.Join(other, "abs.yaml"))
				return filepath.Join(dir, "main.yaml"), dir
			},
			wantTags: []string{"abs", "main"},
		},
		{
			name: "symlink and alternate spellings are loaded once",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "main.yaml", "main", "a.yaml", "link.yaml", "./sub/../a.yaml")
				writeIncludeTestConfig(t, dir, "a.yaml", "a")
				if err := os.Symlink(filepath.Join(dir, "a.yaml"), filepath.Join(dir, "link.yaml")); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
				if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(dir, "main.yaml"), dir
			},
			wantTags: []string{"a", "main"},
		},
		{
			name: "symlinked root is not included again through its target",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "real/main.yaml", "main", "a.yaml")
				writeIncludeTestConfig(t, dir, "real/a.yaml", "a", "main.yaml")
				if err := os.Symlink(filepath.Join(dir, "real", "main.yaml"), filepath.Join(dir, "real", "entry.yaml")); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
				return filepath.Join(dir, "real", "entry.yaml"), dir
			},
			wantErr: []string{"include cycle:", "entry.yaml -> a.yaml -> main.yaml"},
		},
		{
			name: "diamond include loads the shared file once",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "a.yaml", "a", "b.yaml", "c.yaml")
				writeIncludeTestConfig(t, dir, "b.yaml", "b", "d.yaml")
				writeIncludeTestConfig(t, dir, "c.yaml", "c", "d.yaml")
				writeIncludeTestConfig(t, dir, "d.yaml", "d")
				return filepath.Join(dir, "a.yaml"), dir
			},
			wantTags: []string{"d", "b", "c", "a"},
		},
		{
			name: "cycle back to the root reports the chain",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "main.yaml", "main", "sub/a.yaml")
				writeIncludeTestConfig(t, dir, "sub/a.yaml", "a", "../main.yaml")
				return "main.yaml", dir
			},
			wantErr: []string{"include cycle: main.yaml -> sub/a.yaml -> ../main.yaml"},
		},
		{
			name: "cycle between sub configs reports the chain",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "main.yaml", "main", "a.yaml")
				writeIncludeTestConfig(t, dir, "a.yaml", "a", "b.yaml")
				writeIncludeTestConfig(t, dir, "b.yaml", "b", "./a.yaml")
				return "main.yaml", dir
			},
			wantErr: []string{"include cycle: main.yaml -> a.yaml -> b.yaml -> ./a.yaml"},
		},
		{
			name: "self include is a cycle",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "main.yaml", "main", "main.yaml")
				return "main.yaml", dir
			},
			wantErr: []string{"include cycle: main.yaml -> main.yaml"},
		},
		{
			name: "missing include reports the read failure",
			setup: func(t *testing.T, dir string) (string, string) {
				writeIncludeTestConfig(t, dir, "main.yaml", "main", "missing.yaml")
				return filepath.Join(dir, "main.yaml"), dir
			},
			wantErr: []string{"missing.yaml", "failed to read config"},
		},
		{
			name: "depth limit still applies",
			setup: func(t *testing.T, dir string) (string, string) {
				for i := 0; i <= maxIncludeDepth; i++ {
					writeIncludeTestConfig(t, dir, fmt.Sprintf("c%d.yaml", i), fmt.Sprintf("c%d", i), fmt.Sprintf("c%d.yaml", i+1))
				}
				writeIncludeTestConfig(t, dir, fmt.Sprintf("c%d.yaml", maxIncludeDepth+1), "last")
				return filepath.Join(dir, "c0.yaml"), dir
			},
			wantErr: []string{"maximum include depth reached"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			root, cwd := tt.setup(t, dir)
			t.Chdir(cwd)
			cfg, err := loadMergedConfig(root)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("loadMergedConfig succeeded, want error containing %q", tt.wantErr)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error = %q, want it to contain %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var tags []string
			for _, plugin := range cfg.Plugins {
				tags = append(tags, plugin.Tag)
			}
			if !reflect.DeepEqual(tags, tt.wantTags) {
				t.Fatalf("plugin tags = %v, want %v", tags, tt.wantTags)
			}
			if !filepath.IsAbs(cfg.sourcePath) {
				t.Fatalf("sourcePath = %q, want an absolute path", cfg.sourcePath)
			}
		})
	}
}
