package control

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.etcd.io/bbolt"
)

const defaultPublicListRefreshSeconds = 3600
const maxPublicListRefreshErrorBytes = 512

func normalizePublicListSpec(spec PublicListSpec) (PublicListSpec, error) {
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

func applyPublicListPatch(current PublicList, patch PublicListPatch) (PublicList, error) {
	if patch.Name == nil && patch.Category == nil && patch.URL == nil && patch.Format == nil && patch.Enabled == nil && patch.SHA256 == nil && patch.RefreshSeconds == nil {
		return current, ErrInvalidInput
	}
	spec := PublicListSpec{Name: current.Name, Category: current.Category, URL: current.URL, Format: current.Format, Enabled: current.Enabled, SHA256: current.SHA256, RefreshSeconds: current.RefreshSeconds}
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
	current.Name, current.Category, current.URL, current.Format, current.Enabled = normalized.Name, normalized.Category, normalized.URL, normalized.Format, normalized.Enabled
	current.SHA256, current.RefreshSeconds = normalized.SHA256, normalized.RefreshSeconds
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
	if list.LastRefreshStatus == "" {
		list.LastRefreshStatus = PublicListRefreshNever
	}
	if list.LastRefreshedAt != nil {
		refreshedAt := list.LastRefreshedAt.UTC()
		list.LastRefreshedAt = &refreshedAt
	}
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
	out := PublicList{ID: id, Name: spec.Name, Category: spec.Category, URL: spec.URL, Format: spec.Format, Enabled: spec.Enabled, SHA256: spec.SHA256, RefreshSeconds: spec.RefreshSeconds, LastRefreshStatus: PublicListRefreshNever, CreatedAt: now, UpdatedAt: now}
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
		out.UpdatedAt = now
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
		refreshedAt := result.RefreshedAt.UTC()
		list.LastRefreshStatus = result.Status
		list.LastRefreshedAt = &refreshedAt
		if result.Status == PublicListRefreshSuccess {
			list.EntryCount = result.EntryCount
			list.LastRefreshError = ""
		} else {
			list.LastRefreshError = safePublicListRefreshError(result.Error)
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
		if _, err := getPublicList(tx, listID); err != nil {
			return err
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
