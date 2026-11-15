package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestCredentialTokenRestorePreservesAuthenticationAndOwnership(t *testing.T) {
	s, _, _ := newTestStore(t, time.Now().UTC())
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, admin.ID, userSpec("owner", 100, 100, 10))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.CreateCredential(ctx, user.ID, user.ID, "phone", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an existing hash-only record created by the previous version.
	if err := s.update(ctx, func(tx *bbolt.Tx) error {
		var c credentialRecord
		if err := decode(tx.Bucket(bCredentials).Get([]byte(issued.Credential.ID)), &c); err != nil {
			return err
		}
		c.Token = ""
		return marshalPut(tx.Bucket(bCredentials), []byte(c.ID), c)
	}); err != nil {
		t.Fatal(err)
	}
	ids := []string{issued.Credential.ID}
	tokens, err := s.CredentialTokens(ctx, user.ID, ids)
	if err != nil || len(tokens) != 0 {
		t.Fatal("hash-only credential unexpectedly has a display token")
	}
	if _, err := s.RestoreCredentialToken(ctx, user.ID, user.ID, issued.Credential.ID, "wrong"); !errors.Is(err, ErrTokenMismatch) {
		t.Fatalf("wrong-token error=%v", err)
	}
	restored, err := s.RestoreCredentialToken(ctx, admin.ID, user.ID, issued.Credential.ID, "https://cd.example/dns-query/"+issued.Token+"?dns=ignored")
	if err != nil || restored.Token != issued.Token {
		t.Fatalf("restore failed: %v", err)
	}
	after, err := s.AuthenticateCredential(ctx, issued.Token)
	if err != nil || after != identity {
		t.Fatalf("restore changed authentication/version: %v", err)
	}
	other, err := s.CreateUser(ctx, admin.ID, userSpec("other", 100, 100, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCredentialToken(ctx, other.ID, other.ID, issued.Credential.ID, issued.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other owner error=%v", err)
	}
	tokens, err = s.CredentialTokens(ctx, other.ID, ids)
	if err != nil || len(tokens) != 0 {
		t.Fatal("token visible to another owner")
	}
	rotated, err := s.RotateCredential(ctx, user.ID, user.ID, issued.Credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err = s.CredentialTokens(ctx, user.ID, ids)
	if err != nil || tokens[ids[0]] != rotated.Token {
		t.Fatal("rotation did not replace saved token")
	}
	if _, err = s.RestoreCredentialToken(ctx, user.ID, user.ID, issued.Credential.ID, issued.Token); !errors.Is(err, ErrTokenMismatch) {
		t.Fatalf("old token restored after rotation: %v", err)
	}
	if err = s.RevokeCredential(ctx, user.ID, user.ID, issued.Credential.ID); err != nil {
		t.Fatal(err)
	}
	tokens, err = s.CredentialTokens(ctx, user.ID, ids)
	if err != nil || len(tokens) != 0 {
		t.Fatal("revocation retained saved token")
	}
	if _, err = s.RestoreCredentialToken(ctx, user.ID, user.ID, issued.Credential.ID, rotated.Token); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoked restore=%v", err)
	}
	audit, err := s.ListAudit(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), Page{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(audit)
	if strings.Contains(string(encoded), issued.Token) || strings.Contains(string(encoded), rotated.Token) {
		t.Fatal("audit contains plaintext credential token")
	}
}

func TestRestoreLegacyCredentialToken(t *testing.T) {
	s, _, _ := newTestStore(t, time.Now().UTC())
	user, issued := setupUser(t, s, userSpec("legacy", 100, 100, 10))
	ctx := context.Background()
	secret := "legacy-secret-for-test"
	token := issued.Credential.ID + "." + secret
	if err := s.update(ctx, func(tx *bbolt.Tx) error {
		var c credentialRecord
		if err := decode(tx.Bucket(bCredentials).Get([]byte(issued.Credential.ID)), &c); err != nil {
			return err
		}
		if err := deleteCredentialTokenIndex(tx, c); err != nil {
			return err
		}
		c.Token = ""
		c.TokenHash = nil
		c.SecretHash = hashSecret(secret)
		return marshalPut(tx.Bucket(bCredentials), []byte(c.ID), c)
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := s.RestoreCredentialToken(ctx, user.ID, user.ID, issued.Credential.ID, token)
	if err != nil || restored.Token != token {
		t.Fatalf("legacy restore=%v", err)
	}
	if _, err = s.AuthenticateCredential(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreCredentialToken(ctx, user.ID, user.ID, issued.Credential.ID, "other-id."+secret); !errors.Is(err, ErrTokenMismatch) {
		t.Fatalf("legacy wrong ID=%v", err)
	}
}

func TestFollowerRejectsTokenRestoreBeforeStorage(t *testing.T) {
	s := &MySQLStore{identityMode: "follower"}
	if _, err := s.RestoreCredentialToken(context.Background(), "", "", "", ""); !errors.Is(err, ErrIdentityManagedByMaster) {
		t.Fatalf("error=%v", err)
	}
}
