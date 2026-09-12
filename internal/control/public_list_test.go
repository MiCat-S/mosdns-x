package control

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestPublicListCRUDOverridesAndPersistence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	s, _, path := newTestStore(t, now)
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, admin.ID, userSpec("list-user", 100, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePublicList(ctx, user.ID, PublicListSpec{Name: "bad", URL: "https://example.com/list", Format: PublicListFormatMosDNS}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin create=%v", err)
	}
	if _, err := s.CreatePublicList(ctx, admin.ID, PublicListSpec{Name: "bad", URL: "http://example.com/list", Format: PublicListFormatMosDNS}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("http create=%v", err)
	}
	first, err := s.CreatePublicList(ctx, admin.ID, PublicListSpec{Name: "Primary", Category: "ads", URL: "https://example.com/list.txt", Format: PublicListFormatMosDNS, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreatePublicList(ctx, admin.ID, PublicListSpec{Name: "Hosts", URL: "https://example.com/hosts", Format: PublicListFormatHosts, Enabled: false, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", RefreshSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	if first.RefreshSeconds != defaultPublicListRefreshSeconds {
		t.Fatalf("refresh=%d", first.RefreshSeconds)
	}
	if first.Category != "ads" || first.LastRefreshStatus != PublicListRefreshNever || first.LastRefreshedAt != nil {
		t.Fatalf("new list=%+v", first)
	}
	refreshedAt := now.Add(time.Minute)
	if err := s.RecordPublicListRefresh(ctx, first.ID, PublicListRefreshResult{Status: PublicListRefreshSuccess, EntryCount: 42, RefreshedAt: refreshedAt}); err != nil {
		t.Fatal(err)
	}
	refreshError := "upstream failed\n" + strings.Repeat("界", 300)
	if err := s.RecordPublicListRefresh(ctx, first.ID, PublicListRefreshResult{Status: PublicListRefreshError, RefreshedAt: refreshedAt.Add(time.Minute), Error: refreshError}); err != nil {
		t.Fatal(err)
	}
	first, err = s.GetPublicList(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.EntryCount != 42 || first.LastRefreshStatus != PublicListRefreshError || first.LastRefreshedAt == nil || len(first.LastRefreshError) > maxPublicListRefreshErrorBytes || strings.Contains(first.LastRefreshError, "\n") {
		t.Fatalf("refreshed list=%+v", first)
	}
	if _, err := s.CreatePublicList(ctx, admin.ID, PublicListSpec{Name: "primary", URL: "https://example.com/other", Format: PublicListFormatMosDNS}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate=%v", err)
	}
	page1, err := s.ListPublicLists(ctx, Page{Limit: 1})
	if err != nil || len(page1.Items) != 1 || page1.NextCursor == "" {
		t.Fatalf("page1=%+v err=%v", page1, err)
	}
	page2, err := s.ListPublicLists(ctx, Page{Limit: 1, Cursor: page1.NextCursor})
	if err != nil || len(page2.Items) != 1 {
		t.Fatalf("page2=%+v err=%v", page2, err)
	}
	disabled := false
	if err := s.SetUserPublicList(ctx, user.ID, user.ID, first.ID, &disabled); err != nil {
		t.Fatal(err)
	}
	states, err := s.ListUserPublicLists(ctx, user.ID, Page{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, state := range states.Items {
		if state.List.ID == first.ID {
			found = true
			if state.Enabled || !state.Overridden {
				t.Fatalf("state=%+v", state)
			}
		}
	}
	if !found {
		t.Fatal("missing override")
	}
	if err := s.SetUserPublicList(ctx, user.ID, user.ID, first.ID, nil); err != nil {
		t.Fatal(err)
	}
	states, err = s.ListUserPublicLists(ctx, user.ID, Page{})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states.Items {
		if state.List.ID == first.ID && (state.Overridden || !state.Enabled) {
			t.Fatalf("cleared=%+v", state)
		}
	}
	name := "Renamed"
	updated, err := s.UpdatePublicList(ctx, admin.ID, second.ID, PublicListPatch{Name: &name})
	if err != nil || updated.Name != name {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.GetPublicList(ctx, second.ID); err != nil || got.Name != name {
		t.Fatalf("reopened=%+v err=%v", got, err)
	}
	if got, err := reopened.GetPublicList(ctx, first.ID); err != nil || got.EntryCount != 42 || got.LastRefreshStatus != PublicListRefreshError || got.LastRefreshedAt == nil {
		t.Fatalf("reopened refresh state=%+v err=%v", got, err)
	}
	if err := reopened.DeletePublicList(ctx, admin.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.GetPublicList(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted=%v", err)
	}
}

func TestPublicListSchemaV4Upgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v4.db")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := s.UpdateDNSPolicySettings(ctx, admin.ID, admin.ID, DNSPolicySettingsPatch{CustomBlockEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{bPublicLists, bPublicListNames, bUserPublicLists} {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}
		var v [8]byte
		binary.BigEndian.PutUint64(v[:], policySwitchSchemaVersion)
		return tx.Bucket(bMeta).Put(kSchema, v[:])
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ListPublicLists(ctx, Page{}); err != nil {
		t.Fatal(err)
	}
	settings, err := reopened.GetDNSPolicySettings(ctx, admin.ID)
	if err != nil || settings.CustomBlockEnabled {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}
