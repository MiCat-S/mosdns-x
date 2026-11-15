package telemetry

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// UserEraser is implemented by stores that can erase a deleted user's data.
// It is separate from Service so other implementations need not support it.
type UserEraser interface {
	DeleteUser(ctx context.Context, userID string) error
}

var _ UserEraser = (*Store)(nil)

// DeleteUser erases the user's query records and per-user aggregates. Events
// already queued are flushed first so they cannot land after the erase. A
// query that was admitted before the user's credentials were removed, but
// observed only after this returns, can still leave a record; it then ages
// out under the server default retention.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("telemetry: empty user id")
	}
	if err := s.Flush(ctx); err != nil {
		return err
	}
	var err error
	if s.mysql != nil {
		err = s.mysqlDeleteUser(ctx, userID)
	} else {
		err = s.db.Update(func(tx *bolt.Tx) error {
			for {
				removed, err := deleteUserRecords(tx, userID, nil, math.MaxUint64)
				if err != nil {
					return err
				}
				if removed == 0 {
					break
				}
			}
			prefix := []byte("u\x00" + userID + "\x00")
			for _, name := range [][]byte{bucketMinutes, bucketUpstream} {
				b := tx.Bucket(name)
				var keys [][]byte
				c := b.Cursor()
				for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
					keys = append(keys, append([]byte(nil), k...))
				}
				// Their expiry entries are left behind; prune skips targets
				// that no longer exist.
				for _, k := range keys {
					if err := b.Delete(k); err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
	if err != nil {
		return err
	}
	s.clearDeletedUserCaches(userID)
	return nil
}

func (s *Store) clearDeletedUserCaches(userID string) {
	s.invalidateTopDomains()
	s.droppedMu.Lock()
	for key := range s.droppedByWindow {
		if strings.HasPrefix(key, userID+"\x00") {
			delete(s.droppedByWindow, key)
		}
	}
	s.droppedMu.Unlock()
}

func (s *Store) mysqlDeleteUser(ctx context.Context, userID string) error {
	for {
		done, err := s.mysqlDeleteUserBatch(ctx, userID)
		if err != nil || done {
			return err
		}
	}
}

// BatchedUserEraser is used by the durable control cleanup queue. A false
// result leaves the job pending, so large histories never need one long SQL tx.
type BatchedUserEraser interface {
	DeleteUserBatch(context.Context, string) (bool, error)
}

func (s *Store) DeleteUserBatch(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, errors.New("telemetry: empty user id")
	}
	if s.mysql == nil {
		err := s.DeleteUser(ctx, userID)
		return err == nil, err
	}
	if err := s.Flush(ctx); err != nil {
		return false, err
	}
	done, err := s.mysqlDeleteUserBatch(ctx, userID)
	if err == nil && done {
		s.clearDeletedUserCaches(userID)
	}
	return done, err
}

func (s *Store) mysqlDeleteUserBatch(ctx context.Context, userID string) (bool, error) {
	done := true
	for _, q := range []string{
		`DELETE FROM mosdns_query_logs WHERE user_id=? LIMIT 1000`,
		`DELETE FROM mosdns_telemetry_minutes WHERE scope_kind='u' AND scope_id=? LIMIT 1000`,
		`DELETE FROM mosdns_telemetry_rcodes WHERE scope_kind='u' AND scope_id=? LIMIT 1000`,
		`DELETE FROM mosdns_telemetry_latency WHERE scope_kind='u' AND scope_id=? LIMIT 1000`,
		`DELETE FROM mosdns_telemetry_upstreams WHERE scope_kind='u' AND scope_id=? LIMIT 1000`,
	} {
		opCtx, cancel := s.mysqlContext(ctx)
		result, err := s.mysql.ExecContext(opCtx, q, userID)
		cancel()
		if err != nil {
			return false, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		done = done && count < 1000
	}
	return done, nil
}
