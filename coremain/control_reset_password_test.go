package coremain

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
)

func runReset(t *testing.T, db, username, password string) (string, error) {
	t.Helper()
	cmd := newControlResetPasswordCommand()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetIn(strings.NewReader(password + "\n"))
	cmd.SetArgs([]string{"--database", db, "--username", username})
	err := cmd.Execute()
	return out.String(), err
}

// The end-to-end path a locked-out operator follows: the old password stops
// working, the new one works, and sessions issued earlier are gone.
func TestResetPasswordRestoresAccess(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "control.db")
	const oldPassword = "original-password"
	const newPassword = "replacement-password"

	s, err := control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.InitializeAdmin(ctx, control.UserSpec{
		Username: "Operator", Password: oldPassword,
		Limit: 100, QPS: 10, MaxCredentials: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, sessionToken, err := s.CreateSession(ctx, admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateSession(ctx, sessionToken); err != nil {
		t.Fatalf("session should be valid before the reset: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The username lookup must fold case: "Operator" is reset via "operator".
	out, err := runReset(t, db, "operator", newPassword)
	if err != nil {
		t.Fatalf("reset failed: %v (%s)", err, out)
	}
	if !strings.Contains(out, "Operator") {
		t.Fatalf("output did not name the user: %q", out)
	}

	s, err = control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AuthenticatePassword(ctx, "Operator", oldPassword); !errors.Is(err, control.ErrInvalidCredential) {
		t.Fatalf("old password still accepted: %v", err)
	}
	if _, err := s.AuthenticatePassword(ctx, "Operator", newPassword); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	if _, _, err := s.AuthenticateSession(ctx, sessionToken); !errors.Is(err, control.ErrInvalidCredential) {
		t.Fatalf("a session from before the reset survived: %v", err)
	}
}

// A device credential must keep resolving: resetting the panel password should
// not take the DNS service down for every device.
func TestResetPasswordKeepsDeviceCredentials(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "control.db")
	s, err := control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.InitializeAdmin(ctx, control.UserSpec{
		Username: "operator", Password: "original-password",
		Limit: 100, QPS: 10, MaxCredentials: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.CreateCredential(ctx, admin.ID, admin.ID, "phone", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := runReset(t, db, "operator", "replacement-password"); err != nil {
		t.Fatal(err)
	}

	s, err = control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AuthenticateCredential(ctx, issued.Token); err != nil {
		t.Fatalf("device credential stopped working after the reset: %v", err)
	}
}

func TestResetPasswordRejectsBadInput(t *testing.T) {
	db := filepath.Join(t.TempDir(), "control.db")
	s, err := control.Open(db, control.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InitializeAdmin(context.Background(), control.UserSpec{
		Username: "operator", Password: "original-password",
		Limit: 100, QPS: 10, MaxCredentials: 5,
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	t.Run("unknown user", func(t *testing.T) {
		if _, err := runReset(t, db, "nobody", "replacement-password"); err == nil {
			t.Fatal("resetting an unknown user succeeded")
		}
	})
	t.Run("short password", func(t *testing.T) {
		if _, err := runReset(t, db, "operator", "short"); err == nil {
			t.Fatal("a password under the minimum length was accepted")
		}
	})
	t.Run("username required", func(t *testing.T) {
		if _, err := runReset(t, db, "", "replacement-password"); err == nil {
			t.Fatal("a missing username was accepted")
		}
	})
}
