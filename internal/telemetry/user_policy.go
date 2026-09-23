package telemetry

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
)

// UserLogPolicy reports each user's own query log choices.
//
// QueryLogFor returns whether detailed records are kept for userID and how
// long. A zero retention means the server default. An unknown user, such as
// one that was deleted, must report the defaults with a nil error so their
// records still age out. A non-nil error means the choice is unknown right now.
type UserLogPolicy interface {
	QueryLogFor(ctx context.Context, userID string) (enabled bool, retention time.Duration, err error)
}

// UserLogPolicySetter is implemented by stores that honor per-user choices.
// It is separate from Service so other implementations need not support it.
type UserLogPolicySetter interface {
	SetUserLogPolicy(UserLogPolicy)
}

const policyLookupTimeout = 2 * time.Second

var (
	bucketUserQueryCounts = []byte("user_query_counts")
	keyUserCountsBuilt    = []byte("user_query_counts_built")
)

// SetUserLogPolicy installs the per-user policy. Until one is set, every user
// is logged under the server default, which is the behavior before per-user
// choices existed.
func (s *Store) SetUserLogPolicy(policy UserLogPolicy) {
	s.logPolicy.Store(&policy)
}

func (s *Store) userLogPolicy() UserLogPolicy {
	if p := s.logPolicy.Load(); p != nil {
		return *p
	}
	return nil
}

// detailedLogging decides, for each user with a result in events, whether a
// detailed record may be written. It runs before the write transaction opens,
// so no lookup holds the telemetry write lock.
//
// It fails closed: if a user's choice cannot be read, no record is written.
// Missing one record is recoverable; recording a user who turned logging off,
// because a lookup briefly failed, is not.
func (s *Store) detailedLogging(events []event) map[string]bool {
	allowed := make(map[string]bool)
	// Nothing detailed is written with query_log off, the default, so asking
	// each user would only cost control store lookups on every batch.
	if !s.Settings().QueryLogEnabled {
		return allowed
	}
	policy := s.userLogPolicy()
	for _, e := range events {
		if e.result == nil {
			continue
		}
		userID := e.result.Principal.UserID
		if _, seen := allowed[userID]; seen {
			continue
		}
		if policy == nil || userID == "" {
			allowed[userID] = true
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), policyLookupTimeout)
		enabled, _, err := policy.QueryLogFor(ctx, userID)
		cancel()
		allowed[userID] = err == nil && enabled
	}
	return allowed
}

// retentionCutoffs resolves the age cutoff for each user. A user whose choice
// cannot be read right now is left out, so their records are kept and the
// next minute retries, rather than being cut to a default they did not pick.
func (s *Store) retentionCutoffs(users []string, now time.Time) map[string]time.Time {
	policy := s.userLogPolicy()
	fallback := s.Settings().QueryRetention
	cutoffs := make(map[string]time.Time, len(users))
	for _, userID := range users {
		retention := fallback
		if policy != nil && userID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), policyLookupTimeout)
			_, chosen, err := policy.QueryLogFor(ctx, userID)
			cancel()
			if err != nil {
				continue
			}
			if chosen > 0 {
				retention = chosen
			}
		}
		if retention > maxQueryRetention {
			retention = maxQueryRetention
		}
		cutoffs[userID] = now.Add(-retention)
	}
	return cutoffs
}

func userQueryPrefix(userID string) []byte {
	return append([]byte(userID), 0)
}

// queryUsers lists the users holding records, one seek per user.
func queryUsers(tx *bolt.Tx) []string {
	var users []string
	c := tx.Bucket(bucketUserQ).Cursor()
	for k, _ := c.First(); k != nil; {
		i := bytes.IndexByte(k, 0)
		if i < 0 {
			k, _ = c.Next()
			continue
		}
		userID := string(k[:i])
		users = append(users, userID)
		// Every key of this user sorts below userID+"\x01".
		k, _ = c.Seek(append([]byte(userID), 1))
	}
	return users
}

// countKey is the counts bucket key for userID. bbolt rejects a zero-length
// key, and a record without a user would otherwise fail its whole batch, so
// the empty user is stored under a single NUL, which no real id contains.
func countKey(userID string) []byte {
	if userID == "" {
		return []byte{0}
	}
	return []byte(userID)
}

func countKeyUser(key []byte) string {
	if len(key) == 1 && key[0] == 0 {
		return ""
	}
	return string(key)
}

func userQueryCount(tx *bolt.Tx, userID string) uint64 {
	if v := tx.Bucket(bucketUserQueryCounts).Get(countKey(userID)); len(v) == 8 {
		return binary.BigEndian.Uint64(v)
	}
	return 0
}

func putUserQueryCount(tx *bolt.Tx, userID string, count uint64) error {
	b := tx.Bucket(bucketUserQueryCounts)
	if count == 0 {
		return b.Delete(countKey(userID))
	}
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], count)
	return b.Put(countKey(userID), v[:])
}

// rebuildUserQueryCounts derives the per-user counts from the records. It runs
// once on a database written before counts were kept.
func rebuildUserQueryCounts(tx *bolt.Tx) error {
	if tx.Bucket(bucketMeta).Get(keyUserCountsBuilt) != nil {
		return nil
	}
	if _, err := recountUserQueries(tx); err != nil {
		return err
	}
	return tx.Bucket(bucketMeta).Put(keyUserCountsBuilt, []byte{1})
}

// recountUserQueries replaces the per-user counts and the total with values
// derived from the record index, and returns the counts.
func recountUserQueries(tx *bolt.Tx) (map[string]uint64, error) {
	if err := tx.DeleteBucket(bucketUserQueryCounts); err != nil && !errors.Is(err, bolterrors.ErrBucketNotFound) {
		return nil, err
	}
	if _, err := tx.CreateBucket(bucketUserQueryCounts); err != nil {
		return nil, err
	}
	counts := make(map[string]uint64)
	err := tx.Bucket(bucketUserQ).ForEach(func(k, _ []byte) error {
		if i := bytes.IndexByte(k, 0); i >= 0 {
			counts[string(k[:i])]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var total uint64
	for userID, count := range counts {
		if err := putUserQueryCount(tx, userID, count); err != nil {
			return nil, err
		}
		total += count
	}
	return counts, putMetaUint64(tx, keyQueryCount, total)
}

// deleteUserRecords removes up to limit of a user's oldest records that fall
// before cutoff (nil for no cutoff), keeping both indexes and both counts in
// step. It returns how many were removed.
func deleteUserRecords(tx *bolt.Tx, userID string, cutoff *time.Time, limit uint64) (uint64, error) {
	prefix := userQueryPrefix(userID)
	var bound string
	if cutoff != nil {
		bound = fmt.Sprintf("%020d", cutoff.UnixNano())
	}
	queries := tx.Bucket(bucketQueries)
	c := tx.Bucket(bucketUserQ).Cursor()
	var removed uint64
	// Keys sort by record time within a user, so this walks oldest first and
	// can stop at the first record inside the cutoff. bbolt keeps the cursor
	// valid across Delete, so Next reaches the following key.
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix) && removed < limit; k, _ = c.Next() {
		id := k[len(prefix):]
		if cutoff != nil && (len(id) < 20 || string(id[:20]) >= bound) {
			break
		}
		if err := queries.Delete(id); err != nil {
			return removed, err
		}
		if err := c.Delete(); err != nil {
			return removed, err
		}
		removed++
	}
	if removed == 0 {
		return 0, nil
	}
	count := userQueryCount(tx, userID)
	if removed > count {
		count = removed
	}
	if err := putUserQueryCount(tx, userID, count-removed); err != nil {
		return removed, err
	}
	total := metaUint64(tx, keyQueryCount)
	if removed > total {
		total = removed
	}
	return removed, putMetaUint64(tx, keyQueryCount, total-removed)
}

// pruneByUserRetention applies each user's own retention.
func pruneByUserRetention(tx *bolt.Tx, cutoffs map[string]time.Time) error {
	for userID, cutoff := range cutoffs {
		cutoff := cutoff
		if _, err := deleteUserRecords(tx, userID, &cutoff, ^uint64(0)); err != nil {
			return err
		}
	}
	return nil
}

// fairShareEviction plans how many records to remove from each user so the
// total drops by exactly over. It takes from the largest holders first,
// leveling them down toward one another, so a user who logs little keeps
// their history while another user's volume is cut back. Only a user whose
// count reaches the leveled share loses records.
//
// With counts sorted descending, it finds the fewest top users whose excess
// over the next user covers over, then lowers those to one common level. Any
// overshoot from rounding is handed back one record at a time, in user order,
// so the removal is exact and deterministic.
func fairShareEviction(counts map[string]uint64, over uint64) map[string]uint64 {
	plan := make(map[string]uint64)
	if over == 0 || len(counts) == 0 {
		return plan
	}
	users := make([]string, 0, len(counts))
	var total uint64
	for userID, n := range counts {
		if n > 0 {
			users = append(users, userID)
			total += n
		}
	}
	if len(users) == 0 {
		return plan
	}
	if over >= total {
		for _, userID := range users {
			plan[userID] = counts[userID]
		}
		return plan
	}
	sort.Slice(users, func(i, j int) bool {
		if counts[users[i]] != counts[users[j]] {
			return counts[users[i]] > counts[users[j]]
		}
		return users[i] < users[j]
	})
	var sum uint64
	k := 0
	for k < len(users) {
		sum += counts[users[k]]
		k++
		var next uint64
		if k < len(users) {
			next = counts[users[k]]
		}
		if sum-uint64(k)*next >= over {
			break
		}
	}
	level := (sum - over) / uint64(k)
	extra := sum - uint64(k)*level - over
	for i := 0; i < k; i++ {
		take := counts[users[i]] - level
		if uint64(i) < extra {
			take--
		}
		if take > 0 {
			plan[users[i]] = take
		}
	}
	return plan
}

// evictOverCap brings the total within limit following fairShareEviction.
func evictOverCap(tx *bolt.Tx, limit int) error {
	total := metaUint64(tx, keyQueryCount)
	if total <= uint64(limit) {
		return nil
	}
	counts := make(map[string]uint64)
	var sum uint64
	if err := tx.Bucket(bucketUserQueryCounts).ForEach(func(k, v []byte) error {
		if len(v) == 8 {
			n := binary.BigEndian.Uint64(v)
			counts[countKeyUser(k)] = n
			sum += n
		}
		return nil
	}); err != nil {
		return err
	}
	// The counts drift if an older release wrote or pruned records, for
	// example after a rollback and a return to this one; it keeps the total
	// but not these. A plan built from counts that fall short would remove
	// too little and leave the cap unenforced from then on, so recount from
	// the index. This costs one pass, only when they disagree.
	if sum != total {
		recounted, err := recountUserQueries(tx)
		if err != nil {
			return err
		}
		counts, total = recounted, 0
		for _, n := range counts {
			total += n
		}
		if total <= uint64(limit) {
			return nil
		}
	}
	for userID, take := range fairShareEviction(counts, total-uint64(limit)) {
		if _, err := deleteUserRecords(tx, userID, nil, take); err != nil {
			return err
		}
	}
	return nil
}

// visibleRetention is how far back a query may reach. Records past it can
// linger until the next sweep, so the query hides them rather than show what
// is about to be deleted.
//
// It must follow the owner's own retention. Clamping every query to the
// server default hid the older records of a user who chose to keep more,
// though they were stored, which made a longer retention invisible.
//
// Without a policy every user has the server default, as before. An
// administrator viewing all users sees up to the ceiling, since each user's
// records only exist within that user's own retention.
func (s *Store) visibleRetention(ctx context.Context, userID string, fallback time.Duration) time.Duration {
	policy := s.userLogPolicy()
	if policy == nil {
		return fallback
	}
	if userID == "" {
		return maxQueryRetention
	}
	lookupCtx, cancel := context.WithTimeout(ctx, policyLookupTimeout)
	defer cancel()
	_, chosen, err := policy.QueryLogFor(lookupCtx, userID)
	if err != nil || chosen <= 0 {
		return fallback
	}
	return min(chosen, maxQueryRetention)
}
