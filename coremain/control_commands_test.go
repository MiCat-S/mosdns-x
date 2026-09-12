package coremain

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
)

func executeControl(t *testing.T, ctx context.Context, input string, args ...string) (string, error) {
	t.Helper()
	c := newControlCommand()
	c.SetArgs(args)
	c.SetIn(strings.NewReader(input))
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	if ctx != nil {
		c.SetContext(ctx)
	}
	err := c.Execute()
	return out.String(), err
}

func TestControlInitAdminFromStdin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private", "nested")
	db := filepath.Join(dir, "control.db")
	secret := "a-strong-admin-password"
	out, err := executeControl(t, context.Background(), secret+"\n", "init-admin", "--database", db, "--username", "root")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) {
		t.Fatal("password leaked to output")
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("new directory mode=%o", st.Mode().Perm())
	}
	s, err := control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.AuthenticatePassword(context.Background(), "root", secret)
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != control.RoleAdmin || !u.Enabled || u.Period != control.PeriodDaily || u.Timezone != "UTC" || u.Limit != 1_000_000 || u.QPS != 1_000 || u.Burst != 100 || u.MaxCredentials != 10 {
		t.Fatalf("admin defaults=%+v", u)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	out, err = executeControl(t, context.Background(), secret+"\n", "init-admin", "--database", db, "--username", "other")
	if err == nil {
		t.Fatal("duplicate initialization succeeded")
	}
	if strings.Contains(out+err.Error(), secret) {
		t.Fatal("duplicate error leaked password")
	}
}

func TestControlInitAdminReadsStorageFromConfig(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "control.db")
	config := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(config, []byte("control:\n  database: "+database+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeControl(t, context.Background(), "admin-password-value\n", "init-admin", "--config", config, "--username", "root"); err != nil {
		t.Fatal(err)
	}
	store, err := control.Open(database, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AuthenticatePassword(context.Background(), "root", "admin-password-value"); err != nil {
		t.Fatal(err)
	}
}

func TestControlStatus(t *testing.T) {
	dir := t.TempDir()
	disabledConfig := filepath.Join(dir, "disabled.yaml")
	if err := os.WriteFile(disabledConfig, []byte("log:\n  level: error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertControlStatus(t, disabledConfig, controlStatusDisabled, controlStatusExitDisabled)

	database := filepath.Join(dir, "control.db")
	config := filepath.Join(dir, "control.yaml")
	if err := os.WriteFile(config, []byte("control:\n  database: "+database+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertControlStatus(t, config, controlStatusUninitialized, controlStatusExitUninitialized)
	if _, err := executeControl(t, context.Background(), "admin-password-value\n", "init-admin", "--config", config, "--username", "root"); err != nil {
		t.Fatal(err)
	}
	out, err := executeControl(t, context.Background(), "", "status", "--config", config)
	if err != nil {
		t.Fatal(err)
	}
	if out != controlStatusReady+"\n" {
		t.Fatalf("status output=%q", out)
	}

	invalidConfig := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(invalidConfig, []byte("control:\n  storage:\n    driver: mysql\n    mysql:\n      dsn: secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = executeControl(t, context.Background(), "", "status", "--config", invalidConfig)
	if out != controlStatusStorageError+"\n" {
		t.Fatalf("status output=%q", out)
	}
	if err == nil || !strings.Contains(err.Error(), "non-zero") || strings.Contains(out+err.Error(), "secret-value") {
		t.Fatalf("storage error=%v output=%q", err, out)
	}
	var exitErr interface{ ExitCode() int }
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != controlStatusExitStorageError {
		t.Fatalf("storage exit error=%v", err)
	}
}

func assertControlStatus(t *testing.T, config, expected string, expectedExitCode int) {
	t.Helper()
	out, err := executeControl(t, context.Background(), "", "status", "--config", config)
	if out != expected+"\n" {
		t.Fatalf("status output=%q, expected %q", out, expected+"\n")
	}
	var exitErr interface{ ExitCode() int }
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != expectedExitCode {
		t.Fatalf("status error=%v, expected exit code %d", err, expectedExitCode)
	}
}

func TestControlPasswordInputValidation(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db")
	for name, input := range map[string]string{"multiple": "valid-password\nextra\n", "too_long": strings.Repeat("x", 1025) + "\r\n", "too_short": "short\n"} {
		t.Run(name, func(t *testing.T) {
			out, err := executeControl(t, context.Background(), input, "init-admin", "--database", db, "--username", "root")
			if err == nil {
				t.Fatal("invalid password accepted")
			}
			if strings.Contains(out+err.Error(), input) {
				t.Fatal("password leaked")
			}
		})
	}
	if _, err := executeControl(t, context.Background(), "valid-password\n", "init-admin", "--database", db); err == nil {
		t.Fatal("missing username accepted")
	}
	if _, err := executeControl(t, context.Background(), "valid-password\n", "init-admin", "--database", db, "--mysql-dsn", "user:pass@tcp(localhost:3306)/mosdns", "--username", "root"); err == nil {
		t.Fatal("multiple storage targets accepted")
	}
}

func TestControlMigrateMySQLDryRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	if _, err := executeControl(t, context.Background(), "admin-password-value\n", "init-admin", "--database", path, "--username", "root"); err != nil {
		t.Fatal(err)
	}
	out, err := executeControl(t, context.Background(), "", "migrate-mysql", "--component", "control", "--database", path, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "用户 1") || !strings.Contains(out, "未连接或修改 MySQL") {
		t.Fatalf("output=%q", out)
	}
}

func TestControlPasswordAccepts1024BytesWithCRLF(t *testing.T) {
	password := strings.Repeat("x", 1024)
	db := filepath.Join(t.TempDir(), "control.db")
	out, err := executeControl(t, context.Background(), password+"\r\n", "init-admin", "--database", db, "--username", "root")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, password) {
		t.Fatal("password leaked")
	}
	s, err := control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.AuthenticatePassword(context.Background(), "root", password); err != nil {
		t.Fatal(err)
	}
}

func TestControlInitDoesNotChmodExistingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := executeControl(t, context.Background(), "admin-password-value\n", "init-admin", "--database", filepath.Join(parent, "control.db"), "--username", "root"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("existing parent mode changed to %o", st.Mode().Perm())
	}
}

func TestControlBackupRestorePreservesState(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "live.db")
	secret := "admin-password-value"
	if _, err := executeControl(t, context.Background(), secret+"\n", "init-admin", "--database", db, "--username", "root"); err != nil {
		t.Fatal(err)
	}
	s, err := control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.AuthenticatePassword(context.Background(), "root", secret)
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(context.Background(), admin.ID, control.UserSpec{Username: "user", Password: "user-password-value", Role: control.RoleUser, Enabled: true, Period: control.PeriodDaily, Timezone: "UTC", Limit: 5, QPS: 100, Burst: 0, MaxCredentials: 1})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.CreateCredential(context.Background(), user.ID, user.ID, "device", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.AuthenticateCredential(context.Background(), issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backup.db")
	if _, err = executeControl(t, context.Background(), "", "backup", "--database", db, "--output", backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(dir, "restored.db")
	if _, err = executeControl(t, context.Background(), "", "restore", "--input", backup, "--database", restored); err != nil {
		t.Fatal(err)
	}
	rs, err := control.Open(restored, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	quota, err := rs.CurrentQuota(context.Background(), user.ID)
	if err != nil || quota.Used != 1 {
		t.Fatalf("quota=%+v err=%v", quota, err)
	}
	if _, err = rs.AuthenticateCredential(context.Background(), issued.Token); err != nil {
		t.Fatalf("credential=%v", err)
	}
}

func TestControlBackupRestoreRejectPathsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	out := filepath.Join(dir, "out.db")
	if _, err := executeControl(t, context.Background(), "", "backup", "--database", missing, "--output", out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source=%v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source was created: %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output was created: %v", err)
	}
	if _, err := executeControl(t, context.Background(), "", "restore", "--input", missing, "--database", filepath.Join(dir, "restored-missing.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore missing source=%v", err)
	}
	valid := filepath.Join(dir, "valid.db")
	if _, err := executeControl(t, context.Background(), "admin-password-value\n", "init-admin", "--database", valid, "--username", "root"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backup.db")
	if _, err := executeControl(t, context.Background(), "", "backup", "--database", valid, "--output", backup); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(dest, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeControl(t, context.Background(), "", "restore", "--input", backup, "--database", dest); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination=%v", err)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "keep" {
		t.Fatal("destination overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelOut := filepath.Join(dir, "cancel.db")
	if _, err := executeControl(t, ctx, "", "backup", "--database", valid, "--output", cancelOut); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := os.Stat(cancelOut); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel output remains: %v", err)
	}
}

func TestControlBackupReportsOfflineLockConflict(t *testing.T) {
	db := filepath.Join(t.TempDir(), "live.db")
	if _, err := executeControl(t, context.Background(), "admin-password-value\n", "init-admin", "--database", db, "--username", "root"); err != nil {
		t.Fatal(err)
	}
	live, err := control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	_, err = executeControl(t, context.Background(), "", "backup", "--database", db, "--output", filepath.Join(t.TempDir(), "backup.db"))
	if !errors.Is(err, control.ErrUnavailable) {
		t.Fatalf("lock conflict=%v", err)
	}
}
