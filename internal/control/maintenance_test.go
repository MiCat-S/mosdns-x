package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestMaintainRetentionIndexesAndQuota(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, clock, _ := newTestStore(t, start)
	ctx := context.Background()
	u, oldCredential := setupUser(t, s, userSpec("maintained", 100, 100, 0))
	oldIdentity, err := s.AuthenticateCredential(ctx, oldCredential.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, oldIdentity); err != nil {
		t.Fatal(err)
	}
	oldSession, _, err := s.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeCredential(ctx, u.ID, u.ID, oldCredential.Credential.ID); err != nil {
		t.Fatal(err)
	}
	expiredCredential, err := s.CreateCredential(ctx, u.ID, u.ID, "expired but retained", start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	activeCredential, err := s.CreateCredential(ctx, u.ID, u.ID, "active", time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	clock.Set(start.Add(100 * 24 * time.Hour))
	activeIdentity, err := s.AuthenticateCredential(ctx, activeCredential.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, activeIdentity); err != nil {
		t.Fatal(err)
	}
	activeSession, activeSessionToken, err := s.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.update(ctx, func(tx *bbolt.Tx) error {
		return s.audit(tx, u.ID, "fresh_event", "user", u.ID, nil, clock.Now())
	}); err != nil {
		t.Fatal(err)
	}
	quotaBefore, err := s.CurrentQuota(ctx, u.ID)
	if err != nil || quotaBefore.Used != 1 {
		t.Fatalf("quota before maintenance=%+v err=%v", quotaBefore, err)
	}

	if err = s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	quotaAfter, err := s.CurrentQuota(ctx, u.ID)
	if err != nil || quotaAfter != quotaBefore {
		t.Fatalf("quota changed: before=%+v after=%+v err=%v", quotaBefore, quotaAfter, err)
	}
	if _, _, err = s.AuthenticateSession(ctx, activeSessionToken); err != nil {
		t.Fatalf("active session removed: %v", err)
	}
	if _, err = s.AuthenticateCredential(ctx, activeCredential.Token); err != nil {
		t.Fatalf("active credential removed: %v", err)
	}

	if err = s.view(ctx, func(tx *bbolt.Tx) error {
		if tx.Bucket(bSessions).Get([]byte(oldSession.ID)) != nil || tx.Bucket(bUserSessions).Get(userSessionKey(u.ID, oldSession.ID)) != nil {
			t.Error("expired session or its user index remains")
		}
		if tx.Bucket(bSessions).Get([]byte(activeSession.ID)) == nil || tx.Bucket(bUserSessions).Get(userSessionKey(u.ID, activeSession.ID)) == nil {
			t.Error("active session or its user index was removed")
		}
		if tx.Bucket(bCredentials).Get([]byte(oldCredential.Credential.ID)) != nil || tx.Bucket(bUserCredentials).Get(userCredentialKey(u.ID, oldCredential.Credential.ID)) != nil {
			t.Error("old revoked credential or its index remains")
		}
		if tx.Bucket(bCredentials).Get([]byte(expiredCredential.Credential.ID)) != nil || tx.Bucket(bUserCredentials).Get(userCredentialKey(u.ID, expiredCredential.Credential.ID)) != nil || tx.Bucket(bActiveCredentials).Get(userCredentialKey(u.ID, expiredCredential.Credential.ID)) != nil {
			t.Error("old expired credential or its indexes remain")
		}
		if tx.Bucket(bCredentials).Get([]byte(activeCredential.Credential.ID)) == nil || tx.Bucket(bActiveCredentials).Get(userCredentialKey(u.ID, activeCredential.Credential.ID)) == nil {
			t.Error("active credential or index was removed")
		}
		user, err := getUserRecord(tx, u.ID)
		if err != nil || user.CredentialCount != 1 {
			t.Errorf("credential count=%d err=%v", user.CredentialCount, err)
		}
		oldUsage := usageKey("u:"+u.ID, start.Unix())
		newUsage := usageKey("u:"+u.ID, clock.Now().Truncate(time.Minute).Unix())
		if tx.Bucket(bUsage).Get(oldUsage) != nil || tx.Bucket(bUsage).Get(newUsage) == nil {
			t.Error("usage retention boundary is incorrect")
		}
		if got := tx.Bucket(bAudit).Stats().KeyN; got != 1 {
			t.Errorf("audit records=%d want 1 fresh record", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceCancellationAndRunnerValidation(t *testing.T) {
	s, _, _ := newTestStore(t, time.Now().UTC())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Maintain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Maintain error=%v", err)
	}
	if err := s.RunMaintenance(context.Background(), 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RunMaintenance error=%v", err)
	}
}

func TestMaintainContinuesAcrossBoundedBatches(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s, _, _ := newTestStore(t, now)
	ctx := context.Background()
	u, _ := setupUser(t, s, userSpec("batches", 100, 100, 0))
	if err := s.update(ctx, func(tx *bbolt.Tx) error {
		for i := 0; i < maintenanceBatchSize*2+17; i++ {
			id := fmt.Sprintf("expired-%04d", i)
			r := sessionRecord{Session: Session{ID: id, UserID: u.ID, ExpiresAt: now.Add(-time.Hour)}}
			if err := marshalPut(tx.Bucket(bSessions), []byte(id), r); err != nil {
				return err
			}
			if err := tx.Bucket(bUserSessions).Put(userSessionKey(u.ID, id), []byte(id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.view(ctx, func(tx *bbolt.Tx) error {
		if got := tx.Bucket(bSessions).Stats().KeyN; got != 0 {
			t.Errorf("sessions remaining=%d", got)
		}
		if got := tx.Bucket(bUserSessions).Stats().KeyN; got != 0 {
			t.Errorf("session indexes remaining=%d", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestorePreservesQuotaAndCredentials(t *testing.T) {
	s, clock, livePath := newTestStore(t, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	u, issued := setupUser(t, s, userSpec("backup", 100, 100, 0))
	identity, err := s.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Admit(ctx, identity); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	backup := filepath.Join(dir, "backup.db")
	if err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode=%v", info.Mode().Perm())
	}
	if err = ValidateBackup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "restored.db")
	if err = Restore(ctx, backup, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination, Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	quota, err := restored.CurrentQuota(ctx, u.ID)
	if err != nil || quota.Used != 1 {
		t.Fatalf("restored quota=%+v err=%v", quota, err)
	}
	if _, err = restored.AuthenticateCredential(ctx, issued.Token); err != nil {
		t.Fatalf("restored credential: %v", err)
	}

	existing := filepath.Join(dir, "existing.db")
	if err = os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = s.Backup(ctx, existing); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination error=%v", err)
	}
	content, _ := os.ReadFile(existing)
	if string(content) != "keep" {
		t.Fatal("existing destination was overwritten")
	}
	if err = Restore(ctx, backup, existing); !errors.Is(err, os.ErrExist) {
		t.Fatalf("restore existing destination error=%v", err)
	}

	if err = ValidateBackup(ctx, livePath); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("live database lock conflict error=%v", err)
	}
}

func TestBackupCancellationDoesNotLeaveDestination(t *testing.T) {
	s, _, _ := newTestStore(t, time.Now().UTC())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "cancelled.db")
	if err := s.Backup(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("Backup error=%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled backup remains: %v", err)
	}
}
