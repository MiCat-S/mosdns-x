package telemetry

import (
	"bytes"
	"context"
	"database/sql"
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
	s.droppedMu.Lock()
	for key := range s.droppedByWindow {
		if strings.HasPrefix(key, userID+"\x00") {
			delete(s.droppedByWindow, key)
		}
	}
	s.droppedMu.Unlock()
	return nil
}

func (s *Store) mysqlDeleteUser(parent context.Context, userID string) error {
	ctx, cancel := s.mysqlContext(parent)
	defer cancel()
	tx, err := s.mysql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM mosdns_query_logs WHERE user_id=?`,
		`DELETE FROM mosdns_telemetry_minutes WHERE scope_kind='u' AND scope_id=?`,
		`DELETE FROM mosdns_telemetry_rcodes WHERE scope_kind='u' AND scope_id=?`,
		`DELETE FROM mosdns_telemetry_latency WHERE scope_kind='u' AND scope_id=?`,
		`DELETE FROM mosdns_telemetry_upstreams WHERE scope_kind='u' AND scope_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
