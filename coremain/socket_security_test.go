//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package coremain

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pmkol/mosdns-x/pkg/safe_close"
	"go.uber.org/zap"
)

func TestUnixSocketPermissionsAndFilePreservation(t *testing.T) {
	for _, protocol := range []string{"udp", "tcp"} {
		t.Run(protocol, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "mosdns-socket-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			m := &Mosdns{logger: zap.NewNop(), sc: safe_close.NewSafeClose()}
			m.sc.SendCloseSignal(nil)
			m.sc.Done()
			t.Cleanup(func() {
				if err := m.shutdown(); err != nil {
					t.Error(err)
				}
			})
			path := filepath.Join(dir, "dns.sock")
			cfg := &ServerListenerConfig{Protocol: protocol, Addr: path, UnixDomainSocket: true}
			if err := m.startServerListener(cfg, testDNSHandler{}); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
				t.Fatalf("socket info=%v error=%v", info, err)
			}
			regular := filepath.Join(dir, "data")
			if err := os.WriteFile(regular, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.Addr = regular
			if err := m.startServerListener(cfg, testDNSHandler{}); err == nil {
				t.Fatal("listener replaced a regular file")
			}
			link := filepath.Join(dir, "link")
			if err := os.Symlink(regular, link); err != nil {
				t.Fatal(err)
			}
			cfg.Addr = link
			if err := m.startServerListener(cfg, testDNSHandler{}); err == nil {
				t.Fatal("listener followed or replaced a symlink")
			}
			content, err := os.ReadFile(regular)
			if err != nil || string(content) != "preserve" {
				t.Fatalf("existing file changed: %q %v", content, err)
			}
			if _, err := os.Readlink(link); err != nil {
				t.Fatalf("existing symlink changed: %v", err)
			}
		})
	}
}
