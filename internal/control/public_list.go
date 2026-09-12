package control

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.etcd.io/bbolt"
)

const defaultPublicListRefreshSeconds = 3600
const maxPublicListRefreshErrorBytes = 512

func migratePublicListPublicationState(tx *bbolt.Tx, previousVersion uint64) error {
	if previousVersion == 0 || previousVersion > publicListSchemaVersion {
		return nil
	}
	bucket := tx.Bucket(bPublicLists)
	if bucket == nil {
		return nil
	}
	return bucket.ForEach(func(key, value []byte) error {
		var list PublicList
		if err := decode(value, &list); err != nil {
			return err
		}
		migrateLegacyPublicListState(&list)
		return marshalPut(bucket, key, list)
	})
}

func migrateLegacyPublicListState(list *PublicList) {
	list.DefaultEnabled = list.Enabled
	list.Published = true
	if list.LastRefreshStatus == PublicListRefreshSuccess && list.LastRefreshedAt != nil {
		list.SnapshotStatus = PublicListSnapshotCurrent
		list.LastSuccessfulAt = list.LastRefreshedAt
	} else if list.EntryCount > 0 {
		list.SnapshotStatus = PublicListSnapshotStale
	} else {
		list.SnapshotStatus = PublicListSnapshotMissing
	}
}

func normalizePublicListSpec(spec PublicListSpec) (PublicListSpec, error) {
	if spec.DefaultEnabled != nil {
		if spec.Enabled && !*spec.DefaultEnabled {
			return spec, fmt.Errorf("%w: enabled and default_enabled conflict", ErrInvalidInput)
		}
		spec.Enabled = *spec.DefaultEnabled
	}
	defaultEnabled := spec.Enabled
	spec.DefaultEnabled = &defaultEnabled
	if spec.Published == nil {
		published := true
		spec.Published = &published
	}
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" || len(spec.Name) > 128 || strings.IndexFunc(spec.Name, unicode.IsControl) >= 0 {
		return spec, fmt.Errorf("%w: invalid public list name", ErrInvalidInput)
	}
	spec.Category = strings.TrimSpace(spec.Category)
	if len(spec.Category) > 64 || strings.IndexFunc(spec.Category, unicode.IsControl) >= 0 {
		return spec, fmt.Errorf("%w: invalid public list category", ErrInvalidInput)
	}
	u, err := url.Parse(spec.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || len(spec.URL) > 2048 {
		return spec, fmt.Errorf("%w: public list URL must be HTTPS", ErrInvalidInput)
	}
	spec.URL = u.String()
	if spec.Format != PublicListFormatMosDNS && spec.Format != PublicListFormatHosts {
		return spec, fmt.Errorf("%w: invalid public list format", ErrInvalidInput)
	}
	spec.SHA256 = strings.ToLower(strings.TrimSpace(spec.SHA256))
	if spec.SHA256 != "" {
		decoded, err := hex.DecodeString(spec.SHA256)
		if err != nil || len(decoded) != 32 {
			return spec, fmt.Errorf("%w: sha256 must contain 64 hex characters", ErrInvalidInput)
		}
	}
	if spec.RefreshSeconds == 0 {
		spec.RefreshSeconds = defaultPublicListRefreshSeconds
	}
	if spec.RefreshSeconds < 300 || spec.RefreshSeconds > 86400 {
		return spec, fmt.Errorf("%w: refresh_seconds must be between 300 and 86400", ErrInvalidInput)
	}
	return spec, nil
}

// NormalizePublicListSpec applies the storage contract before a downloaded
// candidate is bound to its metadata.
func NormalizePublicListSpec(spec PublicListSpec) (PublicListSpec, error) {
	return normalizePublicListSpec(spec)
}

func applyPublicListPatch(current PublicList, patch PublicListPatch) (PublicList, error) {
	if patch.Name == nil && patch.Category == nil && patch.URL == nil && patch.Format == nil && patch.Enabled == nil && patch.DefaultEnabled == nil && patch.Published == nil && patch.SHA256 == nil && patch.RefreshSeconds == nil {
		return current, ErrInvalidInput
	}
	defaultEnabled := current.DefaultEnabled
	published := current.Published
	spec := PublicListSpec{Name: current.Name, Category: current.Category, URL: current.URL, Format: current.Format, Enabled: current.DefaultEnabled, DefaultEnabled: &defaultEnabled, Published: &published, SHA256: current.SHA256, RefreshSeconds: current.RefreshSeconds}
	if patch.Name != nil {
		spec.Name = *patch.Name
	}
	if patch.Category != nil {
		spec.Category = *patch.Category
	}
	if patch.URL != nil {
		spec.URL = *patch.URL
	}
	if patch.Format != nil {
		spec.Format = *patch.Format
	}
	if patch.Enabled != nil {
		spec.Enabled = *patch.Enabled
		spec.DefaultEnabled = patch.Enabled
	}
	if patch.DefaultEnabled != nil {
		spec.Enabled = *patch.DefaultEnabled
		spec.DefaultEnabled = patch.DefaultEnabled
	}
	if patch.Published != nil {
		spec.Published = patch.Published
	}
	if patch.SHA256 != nil {
		spec.SHA256 = *patch.SHA256
	}
	if patch.RefreshSeconds != nil {
		spec.RefreshSeconds = *patch.RefreshSeconds
	}
	normalized, err := normalizePublicListSpec(spec)
	if err != nil {
		return current, err
	}
	sourceChanged := current.URL != normalized.URL || current.Format != normalized.Format || current.SHA256 != normalized.SHA256
	current.Name, current.Category, current.URL, current.Format = normalized.Name, normalized.Category, normalized.URL, normalized.Format
	current.Enabled, current.DefaultEnabled, current.Published = normalized.Enabled, *normalized.DefaultEnabled, *normalized.Published
	current.SHA256, current.RefreshSeconds = normalized.SHA256, normalized.RefreshSeconds
	if sourceChanged {
		current.Published = false
	}
	return current, nil
}

func getPublicList(tx *bbolt.Tx, id string) (PublicList, error) {
	var out PublicList
	if err := decode(tx.Bucket(bPublicLists).Get([]byte(id)), &out); err != nil {
		return out, err
	}
	normalizeStoredPublicList(&out)
	return out, nil
}

func normalizeStoredPublicList(list *PublicList) {
	list.Enabled = list.DefaultEnabled
	if list.LastRefreshStatus == "" {
		list.LastRefreshStatus = PublicListRefreshNever
	}
	if list.SnapshotStatus == "" {
		list.SnapshotStatus = PublicListSnapshotMissing
	}
	if list.LastRefreshedAt != nil {
		refreshedAt := list.LastRefreshedAt.UTC()
		list.LastRefreshedAt = &refreshedAt
	}
	if list.LastSuccessfulAt != nil {
		successfulAt := list.LastSuccessfulAt.UTC()
		list.LastSuccessfulAt = &successfulAt
	}
}

func nextPublicListUpdatedAt(current, now time.Time) time.Time {
	now = now.UTC()
	if !now.After(current) {
		return current.Add(time.Nanosecond)
	}
	return now
}

func applyPublicListRefresh(list *PublicList, result PublicListRefreshResult) error {
	if result.Status != PublicListRefreshSuccess && result.Status != PublicListRefreshError || result.RefreshedAt.IsZero() {
		return ErrInvalidInput
	}
	refreshedAt := result.RefreshedAt.UTC()
	list.LastRefreshStatus = result.Status
	list.LastRefreshedAt = &refreshedAt
	if result.Status == PublicListRefreshSuccess {
		list.EntryCount = result.EntryCount
		list.LastRefreshError = ""
		list.SnapshotStatus = PublicListSnapshotCurrent
		list.SnapshotSHA256 = strings.ToLower(result.SHA256)
		list.LastSuccessfulAt = &refreshedAt
	} else {
		list.LastRefreshError = safePublicListRefreshError(result.Error)
		if result.SnapshotMissing {
			list.EntryCount = 0
			list.SnapshotStatus = PublicListSnapshotMissing
			list.SnapshotSHA256 = ""
		} else if list.SnapshotStatus == PublicListSnapshotCurrent || list.SnapshotStatus == PublicListSnapshotStale {
			list.SnapshotStatus = PublicListSnapshotStale
		} else {
			list.SnapshotStatus = PublicListSnapshotMissing
		}
	}
	return nil
}

func (s *Store) CreatePublicList(ctx context.Context, actor string, spec PublicListSpec) (PublicList, error) {
	spec, err := normalizePublicListSpec(spec)
	if err != nil {
		return PublicList{}, err
	}
	id, err := randomText(16)
	if err != nil {
		return PublicList{}, err
	}
	now := s.clock.Now().UTC()
	out := PublicList{ID: id, Name: spec.Name, Category: spec.Category, URL: spec.URL, Format: spec.Format, Enabled: spec.Enabled, DefaultEnabled: *spec.DefaultEnabled, Published: *spec.Published, SHA256: spec.SHA256, RefreshSeconds: spec.RefreshSeconds, LastRefreshStatus: PublicListRefreshNever, SnapshotStatus: PublicListSnapshotMissing, CreatedAt: now, UpdatedAt: now}
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		if err := requireAdmin(tx, actor, now); err != nil {
			return err
		}
		name := strings.ToLower(out.Name)
		if tx.Bucket(bPublicListNames).Get([]byte(name)) != nil {
			return ErrConflict
		}
		if err := marshalPut(tx.Bucket(bPublicLists), []byte(id), out); err != nil {
			return err
		}
		if err := tx.Bucket(bPublicListNames).Put([]byte(name), []byte(id)); err != nil {
			return err
		}
		return s.audit(tx, actor, "create_public_list", "public_list", id, map[string]any{"list": out}, now)
	})
	return out, err
}

func (s *Store) UpdatePublicList(ctx context.Context, actor, id string, patch PublicListPatch) (PublicList, error) {
	if id == "" {
		return PublicList{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	var out PublicList
	err := s.update(ctx, func(tx *bbolt.Tx) error {
		if err := requireAdmin(tx, actor, now); err != nil {
			return err
		}
		current, err := getPublicList(tx, id)
		if err != nil {
			return err
		}
		out, err = applyPublicListPatch(current, patch)
		if err != nil {
			return err
		}
		out.UpdatedAt = nextPublicListUpdatedAt(current.UpdatedAt, now)
		oldName, newName := strings.ToLower(current.Name), strings.ToLower(out.Name)
		if oldName != newName {
			if tx.Bucket(bPublicListNames).Get([]byte(newName)) != nil {
				return ErrConflict
			}
			if err := tx.Bucket(bPublicListNames).Delete([]byte(oldName)); err != nil {
				return err
			}
			if err := tx.Bucket(bPublicListNames).Put([]byte(newName), []byte(id)); err != nil {
				return err
			}
		}
		if err := marshalPut(tx.Bucket(bPublicLists), []byte(id), out); err != nil {
			return err
		}
		return s.audit(tx, actor, "update_public_list", "public_list", id, map[string]any{"before": current, "after": out}, now)
	})
	return out, err
}

// CommitPublicListSnapshot atomically publishes metadata and the pointer to an
// already-written immutable snapshot. The caller removes the staged snapshot
// if this transaction fails.
func (s *Store) CommitPublicListSnapshot(ctx context.Context, actor, id string, expectedUpdatedAt time.Time, spec PublicListSpec, refresh PublicListRefreshResult) (PublicList, error) {
	spec, err := normalizePublicListSpec(spec)
	if err != nil {
		return PublicList{}, err
	}
	if refresh.Status != PublicListRefreshSuccess || refresh.SHA256 == "" {
		return PublicList{}, ErrInvalidInput
	}
	if id == "" {
		id, err = randomText(16)
		if err != nil {
			return PublicList{}, err
		}
	}
	now := s.clock.Now().UTC()
	var out PublicList
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		if err := requireAdmin(tx, actor, now); err != nil {
			return err
		}
		bucket := tx.Bucket(bPublicLists)
		current, currentErr := getPublicList(tx, id)
		if currentErr != nil && currentErr != ErrNotFound {
			return currentErr
		}
		published := true
		if currentErr == nil {
			if expectedUpdatedAt.IsZero() || !current.UpdatedAt.Equal(expectedUpdatedAt) {
				return ErrConflict
			}
			defaultEnabled := *spec.DefaultEnabled
			out, err = applyPublicListPatch(current, PublicListPatch{
				Name: &spec.Name, Category: &spec.Category, URL: &spec.URL, Format: &spec.Format,
				DefaultEnabled: &defaultEnabled, Published: &published, SHA256: &spec.SHA256,
				RefreshSeconds: &spec.RefreshSeconds,
			})
			if err != nil {
				return err
			}
			out.Published = true
			out.UpdatedAt = nextPublicListUpdatedAt(current.UpdatedAt, now)
		} else {
			if !expectedUpdatedAt.IsZero() {
				return ErrConflict
			}
			out = PublicList{ID: id, Name: spec.Name, Category: spec.Category, URL: spec.URL, Format: spec.Format, Enabled: *spec.DefaultEnabled, DefaultEnabled: *spec.DefaultEnabled, Published: true, SHA256: spec.SHA256, RefreshSeconds: spec.RefreshSeconds, CreatedAt: now, UpdatedAt: now}
		}
		if err := applyPublicListRefresh(&out, refresh); err != nil {
			return err
		}
		oldName, newName := "", strings.ToLower(out.Name)
		if currentErr == nil {
			oldName = strings.ToLower(current.Name)
		}
		if oldName != newName {
			if existing := tx.Bucket(bPublicListNames).Get([]byte(newName)); existing != nil && string(existing) != id {
				return ErrConflict
			}
			if oldName != "" {
				if err := tx.Bucket(bPublicListNames).Delete([]byte(oldName)); err != nil {
					return err
				}
			}
			if err := tx.Bucket(bPublicListNames).Put([]byte(newName), []byte(id)); err != nil {
				return err
			}
		}
		if err := marshalPut(bucket, []byte(id), out); err != nil {
			return err
		}
		action := "publish_public_list"
		return s.audit(tx, actor, action, "public_list", id, map[string]any{"before": current, "after": out}, now)
	})
	return out, err
}

func (s *Store) DeletePublicList(ctx context.Context, actor, id string) error {
	if id == "" {
		return ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		if err := requireAdmin(tx, actor, now); err != nil {
			return err
		}
		list, err := getPublicList(tx, id)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bPublicLists).Delete([]byte(id)); err != nil {
			return err
		}
		if err := tx.Bucket(bPublicListNames).Delete([]byte(strings.ToLower(list.Name))); err != nil {
			return err
		}
		cursor := tx.Bucket(bUserPublicLists).Cursor()
		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			if strings.HasSuffix(string(k), "\x00"+id) {
				if err := cursor.Delete(); err != nil {
					return err
				}
			}
		}
		return s.audit(tx, actor, "delete_public_list", "public_list", id, map[string]any{"list": list}, now)
	})
}

func (s *Store) GetPublicList(ctx context.Context, id string) (PublicList, error) {
	if id == "" {
		return PublicList{}, ErrInvalidInput
	}
	var out PublicList
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		var err error
		out, err = getPublicList(tx, id)
		return err
	})
	return out, err
}

func (s *Store) ListPublicLists(ctx context.Context, page Page) (PageResult[PublicList], error) {
	out := PageResult[PublicList]{Items: []PublicList{}}
	limit, err := pageLimit(page)
	if err != nil {
		return out, err
	}
	err = s.view(ctx, func(tx *bbolt.Tx) error {
		c := tx.Bucket(bPublicLists).Cursor()
		k, v := c.First()
		if page.Cursor != "" {
			k, v = c.Seek([]byte(page.Cursor))
			if string(k) == page.Cursor {
				k, v = c.Next()
			}
		}
		for ; k != nil && len(out.Items) <= limit; k, v = c.Next() {
			var list PublicList
			if err := decode(v, &list); err != nil {
				return err
			}
			normalizeStoredPublicList(&list)
			out.Items = append(out.Items, list)
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			out.NextCursor = out.Items[limit-1].ID
		}
		return nil
	})
	return out, err
}

func userPublicListKey(userID, listID string) []byte { return []byte(userID + "\x00" + listID) }

func (s *Store) RecordPublicListRefresh(ctx context.Context, listID string, result PublicListRefreshResult) error {
	if listID == "" || (result.Status != PublicListRefreshSuccess && result.Status != PublicListRefreshError) || result.RefreshedAt.IsZero() {
		return ErrInvalidInput
	}
	return s.update(ctx, func(tx *bbolt.Tx) error {
		list, err := getPublicList(tx, listID)
		if err != nil {
			return err
		}
		if err := applyPublicListRefresh(&list, result); err != nil {
			return err
		}
		return marshalPut(tx.Bucket(bPublicLists), []byte(listID), list)
	})
}

func safePublicListRefreshError(message string) string {
	message = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message))
	if len(message) <= maxPublicListRefreshErrorBytes {
		return message
	}
	for len(message) > maxPublicListRefreshErrorBytes {
		_, size := utf8.DecodeLastRuneInString(message)
		message = message[:len(message)-size]
	}
	return message
}

func (s *Store) SetUserPublicList(ctx context.Context, actor, userID, listID string, enabled *bool) error {
	if userID == "" || listID == "" {
		return ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		if err := authorizeCredentialOwner(tx, actor, userID, now); err != nil {
			return err
		}
		list, err := getPublicList(tx, listID)
		if err != nil {
			return err
		}
		if !list.Published {
			return ErrNotFound
		}
		key := userPublicListKey(userID, listID)
		if enabled == nil {
			if err := tx.Bucket(bUserPublicLists).Delete(key); err != nil {
				return err
			}
		} else {
			value := byte(0)
			if *enabled {
				value = 1
			}
			if err := tx.Bucket(bUserPublicLists).Put(key, []byte{value}); err != nil {
				return err
			}
		}
		return s.audit(tx, actor, "set_user_public_list", "public_list", listID, map[string]any{"user_id": userID, "enabled": enabled}, now)
	})
}

func (s *Store) ListUserPublicLists(ctx context.Context, userID string, page Page) (PageResult[UserPublicList], error) {
	out := PageResult[UserPublicList]{Items: []UserPublicList{}}
	if userID == "" {
		return out, ErrInvalidInput
	}
	limit, err := pageLimit(page)
	if err != nil {
		return out, err
	}
	err = s.view(ctx, func(tx *bbolt.Tx) error {
		if _, err := getUserRecord(tx, userID); err != nil {
			return err
		}
		c := tx.Bucket(bPublicLists).Cursor()
		k, v := c.First()
		if page.Cursor != "" {
			k, v = c.Seek([]byte(page.Cursor))
			if string(k) == page.Cursor {
				k, v = c.Next()
			}
		}
		for ; k != nil && len(out.Items) <= limit; k, v = c.Next() {
			var list PublicList
			if err := decode(v, &list); err != nil {
				return err
			}
			normalizeStoredPublicList(&list)
			if !list.Published {
				continue
			}
			item := UserPublicList{List: list, Enabled: list.Enabled}
			if override := tx.Bucket(bUserPublicLists).Get(userPublicListKey(userID, list.ID)); len(override) == 1 {
				item.Enabled, item.Overridden = override[0] == 1, true
			}
			out.Items = append(out.Items, item)
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			out.NextCursor = out.Items[limit-1].List.ID
		}
		return nil
	})
	return out, err
}
