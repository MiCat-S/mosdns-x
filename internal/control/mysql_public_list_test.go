package control

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

var mysqlPublicListTestColumns = []string{"id", "name", "category", "url", "format", "enabled", "default_enabled", "published", "sha256", "refresh_seconds", "entry_count", "last_refresh_status", "last_refreshed_at_ns", "last_refresh_error", "snapshot_status", "snapshot_sha256", "last_successful_at_ns", "created_at_ns", "updated_at_ns"}

func mysqlAdminRow(now time.Time) *sqlmock.Rows {
	start := now.Truncate(24 * time.Hour).Unix()
	return sqlmock.NewRows(mysqlUserTestColumns).AddRow("admin-1", "admin", "admin", true, nil, "daily", "UTC", 100, 10, 2, 3, now.Add(-time.Hour).UnixNano(), now.Add(-time.Hour).UnixNano(), make([]byte, 16), make([]byte, 32), 1, start, start, 0, 12, now.UnixNano())
}

func mysqlPublicListRow(list PublicList) *sqlmock.Rows {
	return sqlmock.NewRows(mysqlPublicListTestColumns).AddRow(list.ID, list.Name, list.Category, list.URL, string(list.Format), list.Enabled, list.DefaultEnabled, list.Published, list.SHA256, list.RefreshSeconds, list.EntryCount, string(list.LastRefreshStatus), mysqlPublicListTimeValue(list.LastRefreshedAt), list.LastRefreshError, string(list.SnapshotStatus), list.SnapshotSHA256, mysqlPublicListTimeValue(list.LastSuccessfulAt), list.CreatedAt.UnixNano(), list.UpdatedAt.UnixNano())
}

func TestMySQLPublicListCRUDAndUserOverride(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{db: db, clock: &fakeClock{t: now}, operationTimeout: time.Second, closeCh: make(chan struct{})}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("admin-1").WillReturnRows(mysqlAdminRow(now))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_public_lists`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	list, err := store.CreatePublicList(ctx, "admin-1", PublicListSpec{Name: "Primary", Category: "ads", URL: "https://example.com/list", Format: PublicListFormatMosDNS, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=?`)).WithArgs(list.ID).WillReturnRows(mysqlPublicListRow(list))
	if got, err := store.GetPublicList(ctx, list.ID); err != nil || got.Name != "Primary" {
		t.Fatalf("got=%+v err=%v", got, err)
	}

	renamed := "Renamed"
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("admin-1").WillReturnRows(mysqlAdminRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=? FOR UPDATE`)).WithArgs(list.ID).WillReturnRows(mysqlPublicListRow(list))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_public_lists SET name=?, name_normalized=?, category=?, url=?, format=?, enabled=?, default_enabled=?, published=?, sha256=?, refresh_seconds=?, updated_at_ns=? WHERE id=?`)).
		WithArgs(renamed, "renamed", list.Category, list.URL, string(list.Format), list.DefaultEnabled, list.DefaultEnabled, list.Published, list.SHA256, list.RefreshSeconds, now.Add(time.Nanosecond).UnixNano(), list.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	updated, err := store.UpdatePublicList(ctx, "admin-1", list.ID, PublicListPatch{Name: &renamed})
	if err != nil || updated.Name != renamed {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if !updated.UpdatedAt.After(list.UpdatedAt) {
		t.Fatalf("updated_at did not advance: before=%s after=%s", list.UpdatedAt, updated.UpdatedAt)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("admin-1").WillReturnRows(mysqlAdminRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=? FOR UPDATE`)).WithArgs(list.ID).WillReturnRows(mysqlPublicListRow(updated))
	mock.ExpectRollback()
	defaultEnabled := true
	if _, err := store.CommitPublicListSnapshot(ctx, "admin-1", list.ID, list.UpdatedAt, PublicListSpec{
		Name: "Stale", URL: list.URL, Format: list.Format, DefaultEnabled: &defaultEnabled, RefreshSeconds: 300,
	}, PublicListRefreshResult{Status: PublicListRefreshSuccess, EntryCount: 1, SHA256: strings.Repeat("b", 64), RefreshedAt: now}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale snapshot commit error=%v", err)
	}
	list = updated

	refreshedAt := now.Add(time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=? FOR UPDATE`)).WithArgs(list.ID).WillReturnRows(mysqlPublicListRow(list))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_public_lists SET entry_count=?, last_refresh_status=?, last_refreshed_at_ns=?, last_refresh_error='', snapshot_status='current', snapshot_sha256=?, last_successful_at_ns=? WHERE id=?`)).
		WithArgs(uint64(123), string(PublicListRefreshSuccess), refreshedAt.UnixNano(), "digest", refreshedAt.UnixNano(), list.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.RecordPublicListRefresh(ctx, list.ID, PublicListRefreshResult{Status: PublicListRefreshSuccess, EntryCount: 123, SHA256: "digest", RefreshedAt: refreshedAt}); err != nil {
		t.Fatal(err)
	}
	list.EntryCount = 123
	list.LastRefreshStatus = PublicListRefreshSuccess
	list.LastRefreshedAt = &refreshedAt
	list.SnapshotStatus = PublicListSnapshotCurrent
	list.SnapshotSHA256 = "digest"
	list.LastSuccessfulAt = &refreshedAt

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=? FOR UPDATE`)).WithArgs(list.ID).WillReturnRows(mysqlPublicListRow(list))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_public_lists SET last_refresh_status=?, last_refreshed_at_ns=?, last_refresh_error=?, snapshot_status=IF(snapshot_status IN ('current','stale'),'stale','missing') WHERE id=?`)).
		WithArgs(string(PublicListRefreshError), refreshedAt.UnixNano(), safePublicListRefreshError("upstream failed\n"+strings.Repeat("x", 600)), list.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.RecordPublicListRefresh(ctx, list.ID, PublicListRefreshResult{Status: PublicListRefreshError, RefreshedAt: refreshedAt, Error: "upstream failed\n" + strings.Repeat("x", 600)}); err != nil {
		t.Fatal(err)
	}
	list.LastRefreshStatus = PublicListRefreshError
	list.LastRefreshError = safePublicListRefreshError("upstream failed\n" + strings.Repeat("x", 600))
	list.SnapshotStatus = PublicListSnapshotStale

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("user-1").WillReturnRows(mysqlTestUserRow(now, 0, 12, now.UnixNano()))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=? FOR UPDATE`)).WithArgs(list.ID).WillReturnRows(mysqlPublicListRow(list))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_user_public_lists`)).WithArgs("user-1", list.ID, false).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	disabled := false
	if err := store.SetUserPublicList(ctx, "user-1", "user-1", list.ID, &disabled); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ?`)).WithArgs("user-1").WillReturnRows(mysqlTestUserRow(now, 0, 12, now.UnixNano()))
	rows := sqlmock.NewRows(append(mysqlPublicListTestColumns, "override_enabled")).AddRow(list.ID, list.Name, list.Category, list.URL, string(list.Format), list.Enabled, list.DefaultEnabled, list.Published, list.SHA256, list.RefreshSeconds, list.EntryCount, string(list.LastRefreshStatus), mysqlPublicListTimeValue(list.LastRefreshedAt), list.LastRefreshError, string(list.SnapshotStatus), list.SnapshotSHA256, mysqlPublicListTimeValue(list.LastSuccessfulAt), list.CreatedAt.UnixNano(), list.UpdatedAt.UnixNano(), false)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+mysqlPublicListQualifiedColumns+`, o.enabled FROM mosdns_public_lists l`)).WithArgs("user-1", "", 101).WillReturnRows(rows)
	states, err := store.ListUserPublicLists(ctx, "user-1", Page{})
	if err != nil || len(states.Items) != 1 || states.Items[0].Enabled || !states.Items[0].Overridden {
		t.Fatalf("states=%+v err=%v", states, err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("admin-1").WillReturnRows(mysqlAdminRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=? FOR UPDATE`)).WithArgs(list.ID).WillReturnRows(mysqlPublicListRow(list))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_user_public_lists WHERE list_id=?`)).WithArgs(list.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM mosdns_public_lists WHERE id=?`)).WithArgs(list.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := store.DeletePublicList(ctx, "admin-1", list.ID); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLCommitPublicListSnapshotCreatesPublishedRecord(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{db: db, clock: &fakeClock{t: now}, operationTimeout: time.Second, closeCh: make(chan struct{})}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("admin-1").WillReturnRows(mysqlAdminRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=? FOR UPDATE`)).WithArgs("list-1").WillReturnRows(sqlmock.NewRows(mysqlPublicListTestColumns))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_public_lists`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_audit_logs`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	defaultEnabled := true
	list, err := store.CommitPublicListSnapshot(ctx, "admin-1", "list-1", time.Time{}, PublicListSpec{
		Name: "Ads", URL: "https://example.com/ads.txt", Format: PublicListFormatMosDNS,
		DefaultEnabled: &defaultEnabled, RefreshSeconds: 300,
	}, PublicListRefreshResult{
		Status: PublicListRefreshSuccess, EntryCount: 12, SHA256: strings.Repeat("a", 64), RefreshedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !list.Published || !list.DefaultEnabled || list.SnapshotStatus != PublicListSnapshotCurrent || list.EntryCount != 12 {
		t.Fatalf("list=%+v", list)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
