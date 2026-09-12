package control

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const mysqlPublicListColumns = `id, name, category, url, format, enabled, sha256, refresh_seconds, entry_count, last_refresh_status, last_refreshed_at_ns, last_refresh_error, created_at_ns, updated_at_ns`
const mysqlPublicListQualifiedColumns = `l.id, l.name, l.category, l.url, l.format, l.enabled, l.sha256, l.refresh_seconds, l.entry_count, l.last_refresh_status, l.last_refreshed_at_ns, l.last_refresh_error, l.created_at_ns, l.updated_at_ns`

func scanMySQLPublicList(row sqlScanner) (PublicList, error) {
	var out PublicList
	var format, refreshStatus string
	var created, updated int64
	var refreshedAt sql.NullInt64
	if err := row.Scan(&out.ID, &out.Name, &out.Category, &out.URL, &format, &out.Enabled, &out.SHA256, &out.RefreshSeconds, &out.EntryCount, &refreshStatus, &refreshedAt, &out.LastRefreshError, &created, &updated); err != nil {
		return out, err
	}
	out.Format = PublicListFormat(format)
	out.LastRefreshStatus = PublicListRefreshStatus(refreshStatus)
	if refreshedAt.Valid {
		value := time.Unix(0, refreshedAt.Int64).UTC()
		out.LastRefreshedAt = &value
	}
	out.CreatedAt, out.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	normalizeStoredPublicList(&out)
	return out, nil
}

func mysqlPublicList(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string, lock bool) (PublicList, error) {
	query := `SELECT ` + mysqlPublicListColumns + ` FROM mosdns_public_lists WHERE id=?`
	if lock {
		query += ` FOR UPDATE`
	}
	out, err := scanMySQLPublicList(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

func insertMySQLPublicList(ctx context.Context, tx *sql.Tx, list PublicList) error {
	normalizeStoredPublicList(&list)
	_, err := tx.ExecContext(ctx, `INSERT INTO mosdns_public_lists (id, name, name_normalized, category, url, format, enabled, sha256, refresh_seconds, entry_count, last_refresh_status, last_refreshed_at_ns, last_refresh_error, created_at_ns, updated_at_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, list.ID, list.Name, strings.ToLower(list.Name), list.Category, list.URL, string(list.Format), list.Enabled, list.SHA256, list.RefreshSeconds, list.EntryCount, string(list.LastRefreshStatus), mysqlPublicListTimeValue(list.LastRefreshedAt), list.LastRefreshError, list.CreatedAt.UnixNano(), list.UpdatedAt.UnixNano())
	return err
}

func mysqlPublicListTimeValue(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().UnixNano()
}

func (s *MySQLStore) CreatePublicList(ctx context.Context, actor string, spec PublicListSpec) (PublicList, error) {
	spec, err := normalizePublicListSpec(spec)
	if err != nil {
		return PublicList{}, err
	}
	id, err := randomText(16)
	if err != nil {
		return PublicList{}, err
	}
	now := s.clock.Now().UTC()
	out := PublicList{ID: id, Name: spec.Name, Category: spec.Category, URL: spec.URL, Format: spec.Format, Enabled: spec.Enabled, SHA256: spec.SHA256, RefreshSeconds: spec.RefreshSeconds, LastRefreshStatus: PublicListRefreshNever, CreatedAt: now, UpdatedAt: now}
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
			return err
		}
		if err := insertMySQLPublicList(ctx, tx, out); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "create_public_list", "public_list", id, map[string]any{"list": out}, now)
	})
	return out, err
}

func (s *MySQLStore) UpdatePublicList(ctx context.Context, actor, id string, patch PublicListPatch) (PublicList, error) {
	if id == "" {
		return PublicList{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	var out PublicList
	err := s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
			return err
		}
		current, err := mysqlPublicList(ctx, tx, id, true)
		if err != nil {
			return err
		}
		out, err = applyPublicListPatch(current, patch)
		if err != nil {
			return err
		}
		out.UpdatedAt = now
		_, err = tx.ExecContext(ctx, `UPDATE mosdns_public_lists SET name=?, name_normalized=?, category=?, url=?, format=?, enabled=?, sha256=?, refresh_seconds=?, updated_at_ns=? WHERE id=?`, out.Name, strings.ToLower(out.Name), out.Category, out.URL, string(out.Format), out.Enabled, out.SHA256, out.RefreshSeconds, now.UnixNano(), id)
		if err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "update_public_list", "public_list", id, map[string]any{"before": current, "after": out}, now)
	})
	return out, err
}

func (s *MySQLStore) RecordPublicListRefresh(ctx context.Context, listID string, result PublicListRefreshResult) error {
	if listID == "" || (result.Status != PublicListRefreshSuccess && result.Status != PublicListRefreshError) || result.RefreshedAt.IsZero() {
		return ErrInvalidInput
	}
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := mysqlPublicList(ctx, tx, listID, true); err != nil {
			return err
		}
		if result.Status == PublicListRefreshSuccess {
			_, err := tx.ExecContext(ctx, `UPDATE mosdns_public_lists SET entry_count=?, last_refresh_status=?, last_refreshed_at_ns=?, last_refresh_error='' WHERE id=?`, result.EntryCount, string(result.Status), result.RefreshedAt.UTC().UnixNano(), listID)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE mosdns_public_lists SET last_refresh_status=?, last_refreshed_at_ns=?, last_refresh_error=? WHERE id=?`, string(result.Status), result.RefreshedAt.UTC().UnixNano(), safePublicListRefreshError(result.Error), listID)
		return err
	})
}

func (s *MySQLStore) DeletePublicList(ctx context.Context, actor, id string) error {
	if id == "" {
		return ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
			return err
		}
		list, err := mysqlPublicList(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mosdns_user_public_lists WHERE list_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mosdns_public_lists WHERE id=?`, id); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "delete_public_list", "public_list", id, map[string]any{"list": list}, now)
	})
}

func (s *MySQLStore) GetPublicList(ctx context.Context, id string) (PublicList, error) {
	if s.closed.Load() {
		return PublicList{}, ErrUnavailable
	}
	if id == "" {
		return PublicList{}, ErrInvalidInput
	}
	op, cancel := s.operationContext(ctx)
	defer cancel()
	out, err := mysqlPublicList(op, s.db, id, false)
	return out, mysqlStoreError(err)
}

func (s *MySQLStore) ListPublicLists(ctx context.Context, page Page) (PageResult[PublicList], error) {
	out := PageResult[PublicList]{Items: []PublicList{}}
	if s.closed.Load() {
		return out, ErrUnavailable
	}
	limit, err := pageLimit(page)
	if err != nil {
		return out, err
	}
	op, cancel := s.operationContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(op, `SELECT `+mysqlPublicListColumns+` FROM mosdns_public_lists WHERE id>? ORDER BY id LIMIT ?`, page.Cursor, limit+1)
	if err != nil {
		return out, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		list, err := scanMySQLPublicList(rows)
		if err != nil {
			return out, mysqlStoreError(err)
		}
		out.Items = append(out.Items, list)
	}
	if err := rows.Err(); err != nil {
		return out, mysqlStoreError(err)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextCursor = out.Items[limit-1].ID
	}
	return out, nil
}

func (s *MySQLStore) SetUserPublicList(ctx context.Context, actor, userID, listID string, enabled *bool) error {
	if userID == "" || listID == "" {
		return ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		if _, err := mysqlPublicList(ctx, tx, listID, true); err != nil {
			return err
		}
		if enabled == nil {
			_, err := tx.ExecContext(ctx, `DELETE FROM mosdns_user_public_lists WHERE user_id=? AND list_id=?`, userID, listID)
			if err != nil {
				return err
			}
		} else {
			_, err := tx.ExecContext(ctx, `INSERT INTO mosdns_user_public_lists (user_id,list_id,enabled) VALUES (?,?,?) ON DUPLICATE KEY UPDATE enabled=VALUES(enabled)`, userID, listID, *enabled)
			if err != nil {
				return err
			}
		}
		return mysqlAudit(ctx, tx, actor, "set_user_public_list", "public_list", listID, map[string]any{"user_id": userID, "enabled": enabled}, now)
	})
}

func (s *MySQLStore) ListUserPublicLists(ctx context.Context, userID string, page Page) (PageResult[UserPublicList], error) {
	out := PageResult[UserPublicList]{Items: []UserPublicList{}}
	if s.closed.Load() {
		return out, ErrUnavailable
	}
	if userID == "" {
		return out, ErrInvalidInput
	}
	limit, err := pageLimit(page)
	if err != nil {
		return out, err
	}
	op, cancel := s.operationContext(ctx)
	defer cancel()
	if _, err := mysqlUser(op, s.db, userID, false); err != nil {
		return out, mysqlStoreError(err)
	}
	rows, err := s.db.QueryContext(op, `SELECT `+mysqlPublicListQualifiedColumns+`, o.enabled FROM mosdns_public_lists l LEFT JOIN mosdns_user_public_lists o ON o.list_id=l.id AND o.user_id=? WHERE l.id>? ORDER BY l.id LIMIT ?`, userID, page.Cursor, limit+1)
	if err != nil {
		return out, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var list PublicList
		var format, refreshStatus string
		var created, updated int64
		var refreshedAt sql.NullInt64
		var override sql.NullBool
		if err := rows.Scan(&list.ID, &list.Name, &list.Category, &list.URL, &format, &list.Enabled, &list.SHA256, &list.RefreshSeconds, &list.EntryCount, &refreshStatus, &refreshedAt, &list.LastRefreshError, &created, &updated, &override); err != nil {
			return out, mysqlStoreError(err)
		}
		list.Format = PublicListFormat(format)
		list.LastRefreshStatus = PublicListRefreshStatus(refreshStatus)
		if refreshedAt.Valid {
			value := time.Unix(0, refreshedAt.Int64).UTC()
			list.LastRefreshedAt = &value
		}
		list.CreatedAt, list.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
		normalizeStoredPublicList(&list)
		item := UserPublicList{List: list, Enabled: list.Enabled}
		if override.Valid {
			item.Enabled, item.Overridden = override.Bool, true
		}
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		return out, mysqlStoreError(err)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextCursor = out.Items[limit-1].List.ID
	}
	return out, nil
}
