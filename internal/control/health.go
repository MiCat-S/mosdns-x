package control

import (
	"context"
	"errors"
	"fmt"

	"go.etcd.io/bbolt"
)

const healthScanLimit = 50_000

var ErrHealthScanLimit = errors.New("health scan limit exceeded")

// HealthProvider is optional so third-party Service implementations remain compatible.
// Collection is read-only. Callers must bound its frequency and provide a deadline.
type HealthProvider interface {
	CollectHealth(context.Context) (StorageHealth, error)
}

type StorageHealth struct {
	Driver                    string      `json:"driver"`
	ActiveSessions            *uint64     `json:"active_sessions"`
	CredentialCountMismatches *uint64     `json:"credential_count_mismatches"`
	MySQL                     *PoolHealth `json:"mysql,omitempty"`
	Bolt                      *BoltHealth `json:"bbolt,omitempty"`
}

type PoolHealth struct {
	MaxOpen        int     `json:"max_open"`
	Open           int     `json:"open"`
	InUse          int     `json:"in_use"`
	Idle           int     `json:"idle"`
	WaitCount      int64   `json:"wait_count"`
	WaitSeconds    float64 `json:"wait_seconds"`
	RollbackErrors uint64  `json:"rollback_errors_total"`
}

type BoltHealth struct {
	OpenReadTransactions int `json:"open_read_transactions"`
	PendingPages         int `json:"pending_pages"`
}

func (s *Store) CollectHealth(ctx context.Context) (StorageHealth, error) {
	return s.collectHealth(ctx, healthScanLimit)
}

func (s *Store) collectHealth(ctx context.Context, limit int) (StorageHealth, error) {
	result := StorageHealth{Driver: "bbolt"}
	var sessions, mismatches uint64
	limited := false
	now := s.clock.Now().UTC()
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		remaining := limit
		check := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			remaining--
			if remaining < 0 {
				limited = true
				return ErrHealthScanLimit
			}
			return nil
		}
		type userHealth struct {
			enabled bool
			stored  uint32
			indexed uint64
			invalid bool
		}
		users := make(map[string]*userHealth)
		if err := tx.Bucket(bUsers).ForEach(func(k, v []byte) error {
			if err := check(); err != nil {
				return err
			}
			var u userRecord
			if err := decode(v, &u); err != nil || u.ID != string(k) {
				return fmt.Errorf("invalid user record in health scan")
			}
			users[u.ID] = &userHealth{enabled: u.Enabled, stored: u.CredentialCount}
			return nil
		}); err != nil {
			return err
		}
		// Counts include expired entries pending lazy cleanup. Comparing only
		// unexpired credentials would incorrectly report normal expiry as corruption.
		if err := tx.Bucket(bActiveCredentials).ForEach(func(k, id []byte) error {
			if err := check(); err != nil {
				return err
			}
			var c credentialRecord
			if err := decode(tx.Bucket(bCredentials).Get(id), &c); err != nil {
				return err
			}
			u := users[c.UserID]
			if u == nil || string(k) != string(userCredentialKey(c.UserID, c.ID)) || string(id) != c.ID {
				return fmt.Errorf("invalid active credential index")
			}
			u.indexed++
			u.invalid = u.invalid || !c.RevokedAt.IsZero()
			return nil
		}); err != nil {
			return err
		}
		for _, u := range users {
			if u.invalid || uint64(u.stored) != u.indexed {
				mismatches++
			}
		}
		if err := tx.Bucket(bSessions).ForEach(func(_, v []byte) error {
			if err := check(); err != nil {
				return err
			}
			var ss sessionRecord
			if err := decode(v, &ss); err != nil {
				return err
			}
			u := users[ss.UserID]
			if u != nil && u.enabled && ss.RevokedAt.IsZero() && now.Before(ss.ExpiresAt) {
				sessions++
			}
			return nil
		}); err != nil {
			return err
		}
		return ctx.Err()
	})
	if err != nil {
		if limited {
			return result, ErrHealthScanLimit
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return result, ErrUnavailable
	}
	stats := s.db.Stats()
	result.ActiveSessions = &sessions
	result.CredentialCountMismatches = &mismatches
	result.Bolt = &BoltHealth{OpenReadTransactions: stats.OpenTxN, PendingPages: stats.PendingPageN}
	return result, nil
}

func (s *MySQLStore) CollectHealth(ctx context.Context) (StorageHealth, error) {
	result := StorageHealth{Driver: "mysql"}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if s.closed.Load() {
		return result, ErrUnavailable
	}
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	var active uint64
	err := s.db.QueryRowContext(opCtx, `SELECT COUNT(*) FROM mosdns_sessions s
		JOIN mosdns_users u ON u.id=s.user_id
		WHERE s.revoked_at_ns IS NULL AND s.expires_at_ns>? AND u.enabled=TRUE`,
		s.clock.Now().UTC().UnixNano()).Scan(&active)
	if err != nil {
		return result, mysqlStoreError(err)
	}
	stats := s.db.Stats()
	result.ActiveSessions = &active
	// MySQL checks COUNT(*) under a user row lock; there is no cached credential
	// count to compare. Leave the metric nil, not a fabricated zero.
	result.MySQL = &PoolHealth{
		MaxOpen: stats.MaxOpenConnections, Open: stats.OpenConnections, InUse: stats.InUse,
		Idle: stats.Idle, WaitCount: stats.WaitCount, WaitSeconds: stats.WaitDuration.Seconds(),
		RollbackErrors: s.rollbackErrors.Load(),
	}
	return result, nil
}

var _ HealthProvider = (*Store)(nil)
var _ HealthProvider = (*MySQLStore)(nil)
