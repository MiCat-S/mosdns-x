package control

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
	if first.SnapshotStatus != PublicListSnapshotStale || first.LastSuccessfulAt == nil {
		t.Fatalf("last-good snapshot state=%+v", first)
	}
	if err := s.SetUserPublicList(ctx, user.ID, user.ID, first.ID, &disabled); err != nil {
		t.Fatal(err)
	}
	unpublished := false
	if _, err := s.UpdatePublicList(ctx, admin.ID, first.ID, PublicListPatch{Published: &unpublished}); err != nil {
		t.Fatal(err)
	}
	states, err = s.ListUserPublicLists(ctx, user.ID, Page{})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states.Items {
		if state.List.ID == first.ID {
			t.Fatalf("unpublished list visible=%+v", state)
		}
	}
	if err := s.SetUserPublicList(ctx, user.ID, user.ID, first.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("changed unpublished override=%v", err)
	}
	published := true
	if _, err := s.UpdatePublicList(ctx, admin.ID, first.ID, PublicListPatch{Published: &published}); err != nil {
		t.Fatal(err)
	}
	states, err = s.ListUserPublicLists(ctx, user.ID, Page{})
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, state := range states.Items {
		if state.List.ID == first.ID {
			found = true
			if state.Enabled || !state.Overridden {
				t.Fatalf("republished override=%+v", state)
			}
		}
	}
	if !found {
		t.Fatal("republished list missing")
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

func TestCommitPublicListSnapshotPublishesMetadataAtomically(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	store, err := Open(filepath.Join(t.TempDir(), "control.db"), Options{Clock: &fakeClock{t: now}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, err := store.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	defaultEnabled := true
	refreshedAt := now
	list, err := store.CommitPublicListSnapshot(ctx, admin.ID, "list-1", time.Time{}, PublicListSpec{
		Name: "Ads", URL: "https://example.com/ads.txt", Format: PublicListFormatMosDNS,
		DefaultEnabled: &defaultEnabled, RefreshSeconds: 300,
	}, PublicListRefreshResult{
		Status: PublicListRefreshSuccess, EntryCount: 42, SHA256: strings.Repeat("a", 64), RefreshedAt: refreshedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !list.Published || !list.DefaultEnabled || list.EntryCount != 42 || list.SnapshotStatus != PublicListSnapshotCurrent || list.SnapshotSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("published list=%+v", list)
	}

	defaultEnabled = false
	updated, err := store.CommitPublicListSnapshot(ctx, admin.ID, list.ID, list.UpdatedAt, PublicListSpec{
		Name: "Ads v2", URL: "https://example.com/ads-v2.txt", Format: PublicListFormatMosDNS,
		DefaultEnabled: &defaultEnabled, RefreshSeconds: 600,
	}, PublicListRefreshResult{
		Status: PublicListRefreshSuccess, EntryCount: 7, SHA256: strings.Repeat("b", 64), RefreshedAt: refreshedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Published || updated.DefaultEnabled || updated.Name != "Ads v2" || updated.EntryCount != 7 || updated.SnapshotSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("updated list=%+v", updated)
	}
	if !updated.UpdatedAt.After(list.UpdatedAt) {
		t.Fatalf("updated_at did not advance: before=%s after=%s", list.UpdatedAt, updated.UpdatedAt)
	}
}

func TestPublicListSnapshotCASAdvancesWithFixedClock(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	store, err := Open(filepath.Join(t.TempDir(), "control.db"), Options{Clock: &fakeClock{t: now}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, err := store.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	defaultEnabled := true
	list, err := store.CommitPublicListSnapshot(ctx, admin.ID, "list-1", time.Time{}, PublicListSpec{
		Name: "Ads", URL: "https://example.com/ads.txt", Format: PublicListFormatMosDNS,
		DefaultEnabled: &defaultEnabled, RefreshSeconds: 300,
	}, PublicListRefreshResult{Status: PublicListRefreshSuccess, EntryCount: 1, SHA256: strings.Repeat("a", 64), RefreshedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	category := "privacy"
	updated, err := store.UpdatePublicList(ctx, admin.ID, list.ID, PublicListPatch{Category: &category})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.UpdatedAt.After(list.UpdatedAt) {
		t.Fatalf("updated_at did not advance: before=%s after=%s", list.UpdatedAt, updated.UpdatedAt)
	}
	if _, err := store.CommitPublicListSnapshot(ctx, admin.ID, list.ID, list.UpdatedAt, PublicListSpec{
		Name: "Stale", URL: list.URL, Format: list.Format,
		DefaultEnabled: &defaultEnabled, RefreshSeconds: 300,
	}, PublicListRefreshResult{Status: PublicListRefreshSuccess, EntryCount: 1, SHA256: strings.Repeat("b", 64), RefreshedAt: now}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale snapshot commit error=%v", err)
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

func TestPublicListSchemaV5UpgradePreservesPublicationAndDefault(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v5.db")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.CreatePublicList(ctx, admin.ID, PublicListSpec{Name: "Legacy", URL: "https://example.com/list", Format: PublicListFormatMosDNS, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	refreshedAt := time.Now().UTC()
	if err := s.RecordPublicListRefresh(ctx, list.ID, PublicListRefreshResult{Status: PublicListRefreshSuccess, EntryCount: 10, RefreshedAt: refreshedAt}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPublicListRefresh(ctx, list.ID, PublicListRefreshResult{Status: PublicListRefreshError, RefreshedAt: refreshedAt.Add(time.Minute), Error: "temporary failure"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bPublicLists)
		var legacy map[string]any
		if err := json.Unmarshal(bucket.Get([]byte(list.ID)), &legacy); err != nil {
			return err
		}
		for _, field := range []string{"default_enabled", "published", "snapshot_status", "snapshot_sha256", "last_successful_at"} {
			delete(legacy, field)
		}
		data, err := json.Marshal(legacy)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(list.ID), data); err != nil {
			return err
		}
		var version [8]byte
		binary.BigEndian.PutUint64(version[:], publicListSchemaVersion)
		return tx.Bucket(bMeta).Put(kSchema, version[:])
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reopened, err := Open(path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := reopened.GetPublicList(ctx, list.ID)
		if err != nil {
			reopened.Close()
			t.Fatal(err)
		}
		if !got.Published || got.DefaultEnabled || got.Enabled || got.SnapshotStatus != PublicListSnapshotStale || got.EntryCount != 10 {
			reopened.Close()
			t.Fatalf("migrated=%+v", got)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
