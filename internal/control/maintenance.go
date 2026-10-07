package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.etcd.io/bbolt"
)

const (
	maintenanceBatchSize = 256
	usageRetention       = 35 * 24 * time.Hour
	auditRetention       = 90 * 24 * time.Hour
	credentialRetention  = 35 * 24 * time.Hour
)

// Maintain removes expired operational records in bounded write transactions.
func (s *Store) Maintain(ctx context.Context) error {
	now := s.clock.Now().UTC()
	jobs := []struct {
		bucket []byte
		remove func(*bbolt.Tx, []byte, []byte) (bool, error)
	}{
		{bSessions, sessionRemover(now)},
		{bUsage, usageRemover(now.Add(-usageRetention))},
		{bAudit, auditRemover(now.Add(-auditRetention))},
		{bCredentials, credentialRemover(now, now.Add(-credentialRetention))},
	}
	for _, job := range jobs {
		if err := s.maintainBucket(ctx, job.bucket, job.remove); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) maintainBucket(ctx context.Context, bucket []byte, remove func(*bbolt.Tx, []byte, []byte) (bool, error)) error {
	var cursor []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done := false
		err := s.update(ctx, func(tx *bbolt.Tx) error {
			b := tx.Bucket(bucket)
			c := b.Cursor()
			k, v := c.First()
			if cursor != nil {
				k, v = c.Seek(cursor)
				if k != nil && string(k) == string(cursor) {
					k, v = c.Next()
				}
			}
			scanned := 0
			for ; k != nil && scanned < maintenanceBatchSize; k, v = c.Next() {
				if err := ctx.Err(); err != nil {
					return err
				}
				key := append([]byte(nil), k...)
				value := append([]byte(nil), v...)
				cursor = key
				scanned++
				deleteRecord, err := remove(tx, key, value)
				if err != nil {
					return err
				}
				if deleteRecord {
					if err := b.Delete(key); err != nil {
						return err
					}
				}
			}
			done = k == nil
			return nil
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		if done {
			return nil
		}
	}
}

func sessionRemover(now time.Time) func(*bbolt.Tx, []byte, []byte) (bool, error) {
	return func(tx *bbolt.Tx, key, value []byte) (bool, error) {
		var r sessionRecord
		if err := json.Unmarshal(value, &r); err != nil {
			return false, err
		}
		if r.RevokedAt.IsZero() && now.Before(r.ExpiresAt) {
			return false, nil
		}
		if err := tx.Bucket(bUserSessions).Delete(userSessionKey(r.UserID, string(key))); err != nil {
			return false, err
		}
		return true, nil
	}
}

func usageRemover(cutoff time.Time) func(*bbolt.Tx, []byte, []byte) (bool, error) {
	return func(_ *bbolt.Tx, key, _ []byte) (bool, error) {
		parts := strings.Split(string(key), "\x00")
		if len(parts) < 2 {
			return false, fmt.Errorf("invalid usage key")
		}
		minute, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return false, err
		}
		return time.Unix(minute, 0).Before(cutoff), nil
	}
}

func auditRemover(cutoff time.Time) func(*bbolt.Tx, []byte, []byte) (bool, error) {
	return func(_ *bbolt.Tx, _ []byte, value []byte) (bool, error) {
		var r AuditRecord
		if err := json.Unmarshal(value, &r); err != nil {
			return false, err
		}
		return r.CreatedAt.Before(cutoff), nil
	}
}

func credentialRemover(now, cutoff time.Time) func(*bbolt.Tx, []byte, []byte) (bool, error) {
	return func(tx *bbolt.Tx, key, value []byte) (bool, error) {
		var r credentialRecord
		if err := json.Unmarshal(value, &r); err != nil {
			return false, err
		}
		revoked := !r.RevokedAt.IsZero() && !now.Before(r.RevokedAt)
		expired := !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
		if !revoked && !expired {
			return false, nil
		}
		indexKey := userCredentialKey(r.UserID, string(key))
		active := tx.Bucket(bActiveCredentials)
		if active.Get(indexKey) != nil {
			u, err := getUserRecord(tx, r.UserID)
			if err != nil {
				return false, err
			}
			if u.CredentialCount == 0 {
				return false, fmt.Errorf("credential count underflow")
			}
			u.CredentialCount--
			if err := active.Delete(indexKey); err != nil {
				return false, err
			}
			if err := marshalPut(tx.Bucket(bUsers), []byte(r.UserID), u); err != nil {
				return false, err
			}
		}
		revokedOld := !r.RevokedAt.IsZero() && r.RevokedAt.Before(cutoff)
		expiredOld := !r.ExpiresAt.IsZero() && r.ExpiresAt.Before(cutoff)
		if !revokedOld && !expiredOld {
			return false, nil
		}
		if err := deleteCredentialTokenIndex(tx, r); err != nil {
			return false, err
		}
		if err := tx.Bucket(bUserCredentials).Delete(indexKey); err != nil {
			return false, err
		}
		return true, nil
	}
}

// RunMaintenance runs maintenance immediately and then at interval until ctx ends.
func (s *Store) RunMaintenance(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return ErrInvalidInput
	}
	if err := s.Maintain(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closeCh:
			return ErrUnavailable
		case <-ticker.C:
			if err := s.Maintain(ctx); err != nil {
				return err
			}
		}
	}
}
