package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.etcd.io/bbolt"
	"golang.org/x/crypto/argon2"
)

const (
	legacySchemaVersion             = 1
	credentialSchemaVersion         = 2
	policySchemaVersion             = 3
	schemaVersion                   = 4
	defaultPage                     = 100
	maxPage                         = 1000
	maxUsageRange                   = 31 * 24 * time.Hour
	maxCredentialGenerationAttempts = 16
)

var (
	bMeta               = []byte("meta")
	bUsers              = []byte("users")
	bUsernames          = []byte("usernames")
	bSessions           = []byte("sessions")
	bUserSessions       = []byte("user_sessions")
	bCredentials        = []byte("credentials")
	bCredentialTokens   = []byte("credential_tokens")
	bUserCredentials    = []byte("user_credentials")
	bActiveCredentials  = []byte("active_credentials")
	bUsage              = []byte("usage")
	bAudit              = []byte("audit")
	bDNSPolicySettings  = []byte("dns_policy_settings")
	bDNSPolicyRules     = []byte("dns_policy_rules")
	bUserDNSPolicyRules = []byte("user_dns_policy_rules")
	kSchema             = []byte("schema_version")
)

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Options struct {
	Clock          Clock
	AdmitQueueSize int
}

type Store struct {
	db         *bbolt.DB
	clock      Clock
	mu         sync.RWMutex
	closed     bool
	closeCh    chan struct{}
	admitSlots chan struct{}
}

type userRecord struct {
	User
	PasswordSalt    []byte  `json:"password_salt"`
	PasswordHash    []byte  `json:"password_hash"`
	PasswordVersion uint64  `json:"password_version"`
	QuotaStart      int64   `json:"quota_start"`
	QuotaHighStart  int64   `json:"quota_high_start"`
	QuotaUsed       uint64  `json:"quota_used"`
	RateTokens      float64 `json:"rate_tokens"`
	RateAt          int64   `json:"rate_at"`
	CredentialCount uint32  `json:"credential_count"`
}

type sessionRecord struct {
	Session
	SecretHash [32]byte  `json:"secret_hash"`
	RevokedAt  time.Time `json:"revoked_at,omitempty"`
}

type credentialRecord struct {
	Credential
	// SecretHash is retained only for credentials issued in the legacy
	// id.secret format. New credentials hash the complete UUID token.
	SecretHash [32]byte `json:"secret_hash,omitempty"`
	TokenHash  []byte   `json:"token_hash,omitempty"`
	Version    uint64   `json:"version"`
}

func Open(path string, opts Options) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalidInput)
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.AdmitQueueSize == 0 {
		opts.AdmitQueueSize = 1024
	}
	if opts.AdmitQueueSize < 0 {
		return nil, fmt.Errorf("%w: negative admit queue size", ErrInvalidInput)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("%w: open database: %v", ErrUnavailable, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: chmod database: %v", ErrUnavailable, err)
	}
	db.MaxBatchDelay = time.Millisecond
	db.MaxBatchSize = 128
	s := &Store{db: db, clock: opts.Clock, closeCh: make(chan struct{}), admitSlots: make(chan struct{}, opts.AdmitQueueSize)}
	if err := db.Update(func(tx *bbolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(bMeta)
		if err != nil {
			return err
		}
		previousVersion := uint64(0)
		v := meta.Get(kSchema)
		if v != nil {
			if len(v) != 8 {
				return fmt.Errorf("unsupported schema version")
			}
			previousVersion = binary.BigEndian.Uint64(v)
			if previousVersion != legacySchemaVersion && previousVersion != credentialSchemaVersion && previousVersion != policySchemaVersion && previousVersion != schemaVersion {
				return fmt.Errorf("unsupported schema version")
			}
		}
		for _, name := range [][]byte{bUsers, bUsernames, bSessions, bUserSessions, bCredentials, bCredentialTokens, bUserCredentials, bActiveCredentials, bUsage, bAudit, bDNSPolicySettings, bDNSPolicyRules, bUserDNSPolicyRules} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		if err := rebuildCredentialTokenIndex(tx); err != nil {
			return err
		}
		if err := initializeDNSPolicyData(tx, previousVersion); err != nil {
			return err
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], schemaVersion)
		return meta.Put(kSchema, buf[:])
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: initialize database: %v", ErrUnavailable, err)
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.closeCh)
	return s.db.Close()
}

func (s *Store) view(ctx context.Context, fn func(*bbolt.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrUnavailable
	}
	if err := s.db.View(fn); err != nil {
		if isDomainError(err) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (s *Store) update(ctx context.Context, fn func(*bbolt.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrUnavailable
	}
	if err := s.db.Update(fn); err != nil {
		if isDomainError(err) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (s *Store) batch(ctx context.Context, fn func(*bbolt.Tx) error) error {
	select {
	case s.admitSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closeCh:
		return ErrUnavailable
	default:
		return ErrUnavailable
	}
	defer func() { <-s.admitSlots }()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrUnavailable
	}
	if err := s.db.Batch(fn); err != nil {
		if isDomainError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func isDomainError(err error) bool {
	return errors.Is(err, ErrInvalidCredential) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrRateLimited) || errors.Is(err, ErrQuotaExceeded) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidInput)
}

func marshalPut(b *bbolt.Bucket, key []byte, v any) error {
	p, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put(key, p)
}
func decode[T any](p []byte, dst *T) error {
	if p == nil {
		return ErrNotFound
	}
	return json.Unmarshal(p, dst)
}

func randomText(n int) (string, error) {
	p := make([]byte, n)
	if _, err := rand.Read(p); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(p), nil
}
func hashSecret(v string) [32]byte { return sha256.Sum256([]byte(v)) }

func randomUUIDv4() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	var encoded [36]byte
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded[:]), nil
}

func validUUIDv4(token string) bool {
	if len(token) != 36 || token[8] != '-' || token[13] != '-' || token[18] != '-' || token[23] != '-' || token[14] != '4' {
		return false
	}
	if token[19] != '8' && token[19] != '9' && token[19] != 'a' && token[19] != 'b' {
		return false
	}
	for i := range token {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((token[i] >= '0' && token[i] <= '9') || (token[i] >= 'a' && token[i] <= 'f')) {
			return false
		}
	}
	return true
}

func rebuildCredentialTokenIndex(tx *bbolt.Tx) error {
	index := tx.Bucket(bCredentialTokens)
	cursor := index.Cursor()
	for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
		if err := cursor.Delete(); err != nil {
			return err
		}
	}
	return tx.Bucket(bCredentials).ForEach(func(id, value []byte) error {
		var r credentialRecord
		if err := json.Unmarshal(value, &r); err != nil {
			return err
		}
		if len(r.TokenHash) == 0 {
			return nil
		}
		if len(r.TokenHash) != sha256.Size {
			return fmt.Errorf("invalid token hash for credential %q", id)
		}
		if existing := index.Get(r.TokenHash); existing != nil && string(existing) != string(id) {
			return fmt.Errorf("duplicate credential token hash")
		}
		return index.Put(r.TokenHash, id)
	})
}

func deleteCredentialTokenIndex(tx *bbolt.Tx, r credentialRecord) error {
	if len(r.TokenHash) != sha256.Size {
		return nil
	}
	return tx.Bucket(bCredentialTokens).Delete(r.TokenHash)
}

func uniqueCredentialID(credentials *bbolt.Bucket) (string, error) {
	for range maxCredentialGenerationAttempts {
		id, err := randomText(16)
		if err != nil {
			return "", err
		}
		if credentials.Get([]byte(id)) == nil {
			return id, nil
		}
	}
	return "", errors.New("credential id collision limit reached")
}

func uniqueCredentialToken(index *bbolt.Bucket) (string, [sha256.Size]byte, error) {
	for range maxCredentialGenerationAttempts {
		token, err := randomUUIDv4()
		if err != nil {
			return "", [sha256.Size]byte{}, err
		}
		hash := hashSecret(token)
		if index.Get(hash[:]) == nil {
			return token, hash, nil
		}
	}
	return "", [sha256.Size]byte{}, errors.New("credential token collision limit reached")
}

func hashPassword(password string) ([]byte, []byte, error) {
	if len(password) < 12 || len(password) > 1024 {
		return nil, nil, fmt.Errorf("%w: password length must be 12..1024", ErrInvalidInput)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	return salt, argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32), nil
}
func verifyPassword(password string, salt, expected []byte) bool {
	if len(salt) != 16 || len(expected) != 32 || len(password) > 1024 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, expected) == 1
}

func normalizeSpec(spec UserSpec) (UserSpec, error) {
	spec.Username = strings.TrimSpace(spec.Username)
	if spec.Username == "" || len(spec.Username) > 128 {
		return spec, fmt.Errorf("%w: invalid username", ErrInvalidInput)
	}
	if spec.Role != "" && spec.Role != RoleAdmin && spec.Role != RoleUser {
		return spec, fmt.Errorf("%w: invalid role", ErrInvalidInput)
	}
	if spec.Role == "" {
		spec.Role = RoleUser
	}
	if spec.Period == "" {
		spec.Period = PeriodMonthly
	}
	if spec.Timezone == "" {
		spec.Timezone = "UTC"
	}
	if spec.Period != PeriodDaily && spec.Period != PeriodMonthly {
		return spec, fmt.Errorf("%w: invalid period", ErrInvalidInput)
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return spec, fmt.Errorf("%w: invalid timezone", ErrInvalidInput)
	}
	if spec.Limit == 0 || spec.QPS == 0 || spec.MaxCredentials == 0 {
		return spec, fmt.Errorf("%w: limit, qps and max_credentials must be positive", ErrInvalidInput)
	}
	if spec.Burst > 1_000_000 || spec.QPS > 1_000_000 || spec.MaxCredentials > 10_000 {
		return spec, fmt.Errorf("%w: limits too large", ErrInvalidInput)
	}
	return spec, nil
}

func periodBounds(now time.Time, p Period, tz string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	n := now.In(loc)
	var start time.Time
	if p == PeriodDaily {
		start = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	} else if p == PeriodMonthly {
		start = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
	} else {
		return time.Time{}, time.Time{}, ErrInvalidInput
	}
	end := start.AddDate(0, 0, 1)
	if p == PeriodMonthly {
		end = start.AddDate(0, 1, 0)
	}
	return start.UTC(), end.UTC(), nil
}

func publicUser(r userRecord) User { return r.User }
func getUserRecord(tx *bbolt.Tx, id string) (userRecord, error) {
	var r userRecord
	err := decode(tx.Bucket(bUsers).Get([]byte(id)), &r)
	return r, err
}
func enabledUser(r userRecord) error {
	if !r.Enabled {
		return ErrForbidden
	}
	return nil
}
func entitledUser(r userRecord, now time.Time) error {
	if err := enabledUser(r); err != nil {
		return err
	}
	if !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
		return ErrForbidden
	}
	return nil
}
func requireAdmin(tx *bbolt.Tx, actor string, now time.Time) error {
	r, e := getUserRecord(tx, actor)
	if e != nil {
		return ErrForbidden
	}
	if e = enabledUser(r); e != nil {
		return e
	}
	if r.Role != RoleAdmin {
		return ErrForbidden
	}
	return nil
}

func (s *Store) InitializeAdmin(ctx context.Context, spec UserSpec) (User, error) {
	spec.Role = RoleAdmin
	spec.Enabled = true
	spec, err := normalizeSpec(spec)
	if err != nil {
		return User{}, err
	}
	salt, ph, err := hashPassword(spec.Password)
	if err != nil {
		return User{}, err
	}
	now := s.clock.Now().UTC()
	id, err := randomText(16)
	if err != nil {
		return User{}, err
	}
	u := User{ID: id, Username: spec.Username, Role: RoleAdmin, Enabled: true, ExpiresAt: spec.ExpiresAt, Period: spec.Period, Timezone: spec.Timezone, Limit: spec.Limit, QPS: spec.QPS, Burst: spec.Burst, MaxCredentials: spec.MaxCredentials, CreatedAt: now, UpdatedAt: now}
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		if tx.Bucket(bUsers).Stats().KeyN != 0 {
			return ErrConflict
		}
		r := userRecord{User: u, PasswordSalt: salt, PasswordHash: ph, PasswordVersion: 1, RateTokens: float64(u.QPS) + float64(u.Burst), RateAt: now.UnixNano()}
		if err := marshalPut(tx.Bucket(bUsers), []byte(id), r); err != nil {
			return err
		}
		if err := putDefaultDNSPolicySettings(tx, id, now); err != nil {
			return err
		}
		if err := tx.Bucket(bUsernames).Put([]byte(strings.ToLower(u.Username)), []byte(id)); err != nil {
			return err
		}
		return s.audit(tx, "", "initialize_admin", "user", id, nil, now)
	})
	return u, err
}

func (s *Store) CreateUser(ctx context.Context, actor string, spec UserSpec) (User, error) {
	spec, err := normalizeSpec(spec)
	if err != nil {
		return User{}, err
	}
	salt, ph, err := hashPassword(spec.Password)
	if err != nil {
		return User{}, err
	}
	now := s.clock.Now().UTC()
	id, err := randomText(16)
	if err != nil {
		return User{}, err
	}
	u := User{ID: id, Username: spec.Username, Role: spec.Role, Enabled: spec.Enabled, ExpiresAt: spec.ExpiresAt, Period: spec.Period, Timezone: spec.Timezone, Limit: spec.Limit, QPS: spec.QPS, Burst: spec.Burst, MaxCredentials: spec.MaxCredentials, CreatedAt: now, UpdatedAt: now}
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		if err := requireAdmin(tx, actor, now); err != nil {
			return err
		}
		names := tx.Bucket(bUsernames)
		if names.Get([]byte(strings.ToLower(u.Username))) != nil {
			return ErrConflict
		}
		r := userRecord{User: u, PasswordSalt: salt, PasswordHash: ph, PasswordVersion: 1, RateTokens: float64(u.QPS) + float64(u.Burst), RateAt: now.UnixNano()}
		if err := marshalPut(tx.Bucket(bUsers), []byte(id), r); err != nil {
			return err
		}
		if err := putDefaultDNSPolicySettings(tx, id, now); err != nil {
			return err
		}
		if err := names.Put([]byte(strings.ToLower(u.Username)), []byte(id)); err != nil {
			return err
		}
		return s.audit(tx, actor, "create_user", "user", id, nil, now)
	})
	return u, err
}

func (s *Store) AuthenticatePassword(ctx context.Context, username, password string) (User, error) {
	r, err := s.passwordSnapshot(ctx, username)
	if err != nil {
		if !errors.Is(err, ErrInvalidCredential) {
			return User{}, err
		}
		verifyPassword(password, make([]byte, 16), make([]byte, 32))
		return User{}, ErrInvalidCredential
	}
	if !verifyPassword(password, r.PasswordSalt, r.PasswordHash) || enabledUser(r) != nil {
		return User{}, ErrInvalidCredential
	}
	return r.User, nil
}

func (s *Store) passwordSnapshot(ctx context.Context, username string) (userRecord, error) {
	var out userRecord
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		id := tx.Bucket(bUsernames).Get([]byte(strings.ToLower(strings.TrimSpace(username))))
		if id == nil {
			return ErrInvalidCredential
		}
		r, e := getUserRecord(tx, string(id))
		out = r
		return e
	})
	return out, err
}

func (s *Store) Login(ctx context.Context, username, password string, ttl time.Duration) (Session, string, error) {
	if ttl <= 0 {
		return Session{}, "", ErrInvalidInput
	}
	snapshot, err := s.passwordSnapshot(ctx, username)
	if err != nil {
		if !errors.Is(err, ErrInvalidCredential) {
			return Session{}, "", err
		}
		verifyPassword(password, make([]byte, 16), make([]byte, 32))
		return Session{}, "", ErrInvalidCredential
	}
	if !verifyPassword(password, snapshot.PasswordSalt, snapshot.PasswordHash) {
		return Session{}, "", ErrInvalidCredential
	}
	id, err := randomText(16)
	if err != nil {
		return Session{}, "", err
	}
	secret, err := randomText(32)
	if err != nil {
		return Session{}, "", err
	}
	csrf, err := randomText(32)
	if err != nil {
		return Session{}, "", err
	}
	var session Session
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		current, e := getUserRecord(tx, snapshot.ID)
		if e != nil || enabledUser(current) != nil {
			return ErrInvalidCredential
		}
		if current.PasswordVersion != snapshot.PasswordVersion || subtle.ConstantTimeCompare(current.PasswordHash, snapshot.PasswordHash) != 1 {
			return ErrInvalidCredential
		}
		now := s.clock.Now().UTC()
		session = Session{ID: id, UserID: current.ID, CSRFToken: csrf, ExpiresAt: now.Add(ttl), CreatedAt: now}
		if e := marshalPut(tx.Bucket(bSessions), []byte(id), sessionRecord{Session: session, SecretHash: hashSecret(secret)}); e != nil {
			return e
		}
		return tx.Bucket(bUserSessions).Put(userSessionKey(current.ID, id), []byte(id))
	})
	return session, id + "." + secret, err
}

func (s *Store) SetPassword(ctx context.Context, actor, userID, password string) error {
	salt, ph, err := hashPassword(password)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		if actor != userID {
			if err := requireAdmin(tx, actor, now); err != nil {
				return err
			}
		}
		r, err := getUserRecord(tx, userID)
		if err != nil {
			return err
		}
		r.PasswordSalt = salt
		r.PasswordHash = ph
		r.PasswordVersion++
		r.UpdatedAt = now
		if err := marshalPut(tx.Bucket(bUsers), []byte(userID), r); err != nil {
			return err
		}
		if err := revokeUserSessions(tx, userID, now); err != nil {
			return err
		}
		return s.audit(tx, actor, "set_password", "user", userID, nil, now)
	})
}

func (s *Store) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	var snapshot userRecord
	if err := s.view(ctx, func(tx *bbolt.Tx) error { r, e := getUserRecord(tx, userID); snapshot = r; return e }); err != nil {
		return err
	}
	if !verifyPassword(currentPassword, snapshot.PasswordSalt, snapshot.PasswordHash) {
		return ErrInvalidCredential
	}
	salt, hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		r, e := getUserRecord(tx, userID)
		if e != nil {
			return e
		}
		if enabledUser(r) != nil {
			return ErrForbidden
		}
		if r.PasswordVersion != snapshot.PasswordVersion || subtle.ConstantTimeCompare(r.PasswordHash, snapshot.PasswordHash) != 1 {
			return ErrInvalidCredential
		}
		r.PasswordSalt = salt
		r.PasswordHash = hash
		r.PasswordVersion++
		r.UpdatedAt = now
		if e = marshalPut(tx.Bucket(bUsers), []byte(userID), r); e != nil {
			return e
		}
		if e = revokeUserSessions(tx, userID, now); e != nil {
			return e
		}
		return s.audit(tx, userID, "change_password", "user", userID, nil, now)
	})
}

func revokeUserSessions(tx *bbolt.Tx, userID string, now time.Time) error {
	index := tx.Bucket(bUserSessions)
	prefix := []byte(userID + "\x00")
	c := index.Cursor()
	for k, sessionID := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, sessionID = c.Next() {
		v := tx.Bucket(bSessions).Get(sessionID)
		var r sessionRecord
		if err := json.Unmarshal(v, &r); err != nil {
			return err
		}
		if r.UserID == userID && r.RevokedAt.IsZero() {
			r.RevokedAt = now
			if err := marshalPut(tx.Bucket(bSessions), sessionID, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) UpdateUser(ctx context.Context, actor, userID string, p UserPatch) (User, error) {
	var out User
	now := s.clock.Now().UTC()
	err := s.update(ctx, func(tx *bbolt.Tx) error {
		if err := requireAdmin(tx, actor, now); err != nil {
			return err
		}
		r, err := getUserRecord(tx, userID)
		if err != nil {
			return err
		}
		oldEnabled := r.Enabled
		before := r.User
		if p.Enabled != nil {
			r.Enabled = *p.Enabled
		}
		if p.ExpiresAt != nil {
			r.ExpiresAt = *p.ExpiresAt
		}
		if p.Limit != nil {
			if *p.Limit == 0 {
				return ErrInvalidInput
			}
			r.Limit = *p.Limit
		}
		if p.QPS != nil {
			if *p.QPS == 0 {
				return ErrInvalidInput
			}
			r.QPS = *p.QPS
		}
		if p.Burst != nil {
			r.Burst = *p.Burst
		}
		if p.MaxCredentials != nil {
			if *p.MaxCredentials == 0 {
				return ErrInvalidInput
			}
			r.MaxCredentials = *p.MaxCredentials
		}
		periodChanges := p.Period != nil && *p.Period != r.Period
		timezoneChanges := p.Timezone != nil && *p.Timezone != r.Timezone
		if periodChanges || timezoneChanges {
			start, _, e := periodBounds(now, r.Period, r.Timezone)
			if e != nil {
				return ErrInvalidInput
			}
			if r.QuotaStart == start.Unix() && r.QuotaUsed > 0 {
				return fmt.Errorf("%w: period/timezone cannot change after use in current period", ErrConflict)
			}
			if periodChanges {
				r.Period = *p.Period
			}
			if timezoneChanges {
				r.Timezone = *p.Timezone
			}
			newStart, _, e := periodBounds(now, r.Period, r.Timezone)
			if e != nil {
				return ErrInvalidInput
			}
			r.QuotaStart = 0
			r.QuotaHighStart = newStart.Unix()
		}
		if r.Role == RoleAdmin && enabledUser(r) != nil {
			activeAdmins := 0
			err = tx.Bucket(bUsers).ForEach(func(k, v []byte) error {
				if string(k) == userID {
					return nil
				}
				var other userRecord
				if e := json.Unmarshal(v, &other); e != nil {
					return e
				}
				if other.Role == RoleAdmin && enabledUser(other) == nil {
					activeAdmins++
				}
				return nil
			})
			if err != nil {
				return err
			}
			if activeAdmins == 0 {
				return fmt.Errorf("%w: cannot disable the last administrator", ErrConflict)
			}
		}
		r.UpdatedAt = now
		cap := float64(r.QPS) + float64(r.Burst)
		if r.RateTokens > cap {
			r.RateTokens = cap
		}
		if err := marshalPut(tx.Bucket(bUsers), []byte(userID), r); err != nil {
			return err
		}
		if oldEnabled && !r.Enabled {
			if err := revokeUserSessions(tx, userID, now); err != nil {
				return err
			}
		}
		out = r.User
		return s.audit(tx, actor, "update_user", "user", userID, map[string]any{"before": before, "after": r.User}, now)
	})
	return out, err
}

func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (Session, string, error) {
	if ttl <= 0 {
		return Session{}, "", ErrInvalidInput
	}
	id, err := randomText(16)
	if err != nil {
		return Session{}, "", err
	}
	secret, err := randomText(32)
	if err != nil {
		return Session{}, "", err
	}
	csrf, err := randomText(32)
	if err != nil {
		return Session{}, "", err
	}
	now := s.clock.Now().UTC()
	ss := Session{ID: id, UserID: userID, CSRFToken: csrf, ExpiresAt: now.Add(ttl), CreatedAt: now}
	r := sessionRecord{Session: ss, SecretHash: hashSecret(secret)}
	err = s.update(ctx, func(tx *bbolt.Tx) error {
		u, e := getUserRecord(tx, userID)
		if e != nil {
			return e
		}
		if e = enabledUser(u); e != nil {
			return e
		}
		if e := marshalPut(tx.Bucket(bSessions), []byte(id), r); e != nil {
			return e
		}
		return tx.Bucket(bUserSessions).Put(userSessionKey(userID, id), []byte(id))
	})
	return ss, id + "." + secret, err
}

func splitToken(token string) (string, string, bool) {
	id, sec, ok := strings.Cut(token, ".")
	return id, sec, ok && id != "" && sec != ""
}
func (s *Store) AuthenticateSession(ctx context.Context, token string) (Session, User, error) {
	id, sec, ok := splitToken(token)
	if !ok {
		return Session{}, User{}, ErrInvalidCredential
	}
	var ss Session
	var u User
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		var r sessionRecord
		if decode(tx.Bucket(bSessions).Get([]byte(id)), &r) != nil {
			return ErrInvalidCredential
		}
		h := hashSecret(sec)
		if subtle.ConstantTimeCompare(h[:], r.SecretHash[:]) != 1 || !r.RevokedAt.IsZero() || !s.clock.Now().UTC().Before(r.ExpiresAt) {
			return ErrInvalidCredential
		}
		ur, e := getUserRecord(tx, r.UserID)
		if e != nil || enabledUser(ur) != nil {
			return ErrInvalidCredential
		}
		ss = r.Session
		u = ur.User
		return nil
	})
	return ss, u, err
}

func (s *Store) RevokeSession(ctx context.Context, actor, id string) error {
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		var r sessionRecord
		if err := decode(tx.Bucket(bSessions).Get([]byte(id)), &r); err != nil {
			return err
		}
		if actor != r.UserID {
			if err := requireAdmin(tx, actor, now); err != nil {
				return err
			}
		}
		r.RevokedAt = now
		if err := marshalPut(tx.Bucket(bSessions), []byte(id), r); err != nil {
			return err
		}
		return s.audit(tx, actor, "revoke_session", "session", id, nil, now)
	})
}

func authorizeCredentialOwner(tx *bbolt.Tx, actor, userID string, now time.Time) error {
	if actor == userID {
		r, e := getUserRecord(tx, userID)
		if e != nil {
			return e
		}
		return enabledUser(r)
	}
	return requireAdmin(tx, actor, now)
}
func (s *Store) CreateCredential(ctx context.Context, actor, userID, name string, expires time.Time) (IssuedCredential, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return IssuedCredential{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	if !expires.IsZero() && !now.Before(expires) {
		return IssuedCredential{}, fmt.Errorf("%w: credential expiry must be in the future", ErrInvalidInput)
	}
	var issued IssuedCredential
	err := s.update(ctx, func(tx *bbolt.Tx) error {
		if e := authorizeCredentialOwner(tx, actor, userID, now); e != nil {
			return e
		}
		u, e := getUserRecord(tx, userID)
		if e != nil {
			return e
		}
		if !expires.IsZero() && !u.ExpiresAt.IsZero() && expires.After(u.ExpiresAt) {
			return ErrInvalidInput
		}
		index := tx.Bucket(bUserCredentials)
		if e = cleanupActiveCredentials(tx, &u, now); e != nil {
			return e
		}
		if u.CredentialCount >= u.MaxCredentials {
			return ErrConflict
		}
		id, e := uniqueCredentialID(tx.Bucket(bCredentials))
		if e != nil {
			return e
		}
		token, tokenHash, e := uniqueCredentialToken(tx.Bucket(bCredentialTokens))
		if e != nil {
			return e
		}
		c := Credential{ID: id, UserID: userID, Name: name, ExpiresAt: expires, CreatedAt: now, UpdatedAt: now}
		r := credentialRecord{Credential: c, TokenHash: append([]byte(nil), tokenHash[:]...), Version: 1}
		if e = marshalPut(tx.Bucket(bCredentials), []byte(id), r); e != nil {
			return e
		}
		if e = tx.Bucket(bCredentialTokens).Put(tokenHash[:], []byte(id)); e != nil {
			return e
		}
		if e = index.Put(userCredentialKey(userID, id), []byte(id)); e != nil {
			return e
		}
		if e = tx.Bucket(bActiveCredentials).Put(userCredentialKey(userID, id), []byte(id)); e != nil {
			return e
		}
		u.CredentialCount++
		if e = marshalPut(tx.Bucket(bUsers), []byte(userID), u); e != nil {
			return e
		}
		if e = s.audit(tx, actor, "create_credential", "credential", id, map[string]any{"name": name}, now); e != nil {
			return e
		}
		issued = IssuedCredential{Credential: c, Token: token}
		return nil
	})
	return issued, err
}

func cleanupActiveCredentials(tx *bbolt.Tx, u *userRecord, now time.Time) error {
	b := tx.Bucket(bActiveCredentials)
	prefix := []byte(u.ID + "\x00")
	c := b.Cursor()
	for k, id := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, id = c.Next() {
		var r credentialRecord
		if err := decode(tx.Bucket(bCredentials).Get(id), &r); err != nil {
			return err
		}
		if !r.RevokedAt.IsZero() || !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
			if err := b.Delete(k); err != nil {
				return err
			}
			if u.CredentialCount == 0 {
				return fmt.Errorf("credential count underflow")
			}
			u.CredentialCount--
		}
	}
	return nil
}

func (s *Store) RotateCredential(ctx context.Context, actor, userID, id string) (IssuedCredential, error) {
	now := s.clock.Now().UTC()
	var issued IssuedCredential
	err := s.update(ctx, func(tx *bbolt.Tx) error {
		if e := authorizeCredentialOwner(tx, actor, userID, now); e != nil {
			return e
		}
		var r credentialRecord
		if e := decode(tx.Bucket(bCredentials).Get([]byte(id)), &r); e != nil {
			return e
		}
		if r.UserID != userID {
			return ErrForbidden
		}
		if !r.RevokedAt.IsZero() {
			return ErrConflict
		}
		if !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
			return ErrForbidden
		}
		token, tokenHash, e := uniqueCredentialToken(tx.Bucket(bCredentialTokens))
		if e != nil {
			return e
		}
		if e := deleteCredentialTokenIndex(tx, r); e != nil {
			return e
		}
		r.SecretHash = [sha256.Size]byte{}
		r.TokenHash = append([]byte(nil), tokenHash[:]...)
		r.Version++
		r.UpdatedAt = now
		if e := marshalPut(tx.Bucket(bCredentials), []byte(id), r); e != nil {
			return e
		}
		if e := tx.Bucket(bCredentialTokens).Put(tokenHash[:], []byte(id)); e != nil {
			return e
		}
		if e := s.audit(tx, actor, "rotate_credential", "credential", id, nil, now); e != nil {
			return e
		}
		issued = IssuedCredential{Credential: r.Credential, Token: token}
		return nil
	})
	return issued, err
}

func (s *Store) RevokeCredential(ctx context.Context, actor, userID, id string) error {
	now := s.clock.Now().UTC()
	return s.update(ctx, func(tx *bbolt.Tx) error {
		if e := authorizeCredentialOwner(tx, actor, userID, now); e != nil {
			return e
		}
		var r credentialRecord
		if e := decode(tx.Bucket(bCredentials).Get([]byte(id)), &r); e != nil {
			return e
		}
		if r.UserID != userID {
			return ErrForbidden
		}
		if r.RevokedAt.IsZero() {
			r.RevokedAt = now
			r.UpdatedAt = now
			if e := marshalPut(tx.Bucket(bCredentials), []byte(id), r); e != nil {
				return e
			}
			activeKey := userCredentialKey(userID, id)
			active := tx.Bucket(bActiveCredentials)
			if active.Get(activeKey) != nil {
				u, e := getUserRecord(tx, userID)
				if e != nil {
					return e
				}
				if u.CredentialCount == 0 {
					return fmt.Errorf("credential count underflow")
				}
				u.CredentialCount--
				if e = active.Delete(activeKey); e != nil {
					return e
				}
				if e = marshalPut(tx.Bucket(bUsers), []byte(userID), u); e != nil {
					return e
				}
			}
		}
		return s.audit(tx, actor, "revoke_credential", "credential", id, nil, now)
	})
}

func (s *Store) AuthenticateCredential(ctx context.Context, token string) (Identity, error) {
	var id, legacySecret string
	legacy := false
	var tokenHash [sha256.Size]byte
	if validUUIDv4(token) {
		tokenHash = hashSecret(token)
	} else {
		var ok bool
		id, legacySecret, ok = splitToken(token)
		if !ok {
			return Identity{}, ErrInvalidCredential
		}
		legacy = true
	}
	var out Identity
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		if !legacy {
			indexedID := tx.Bucket(bCredentialTokens).Get(tokenHash[:])
			if indexedID == nil {
				return ErrInvalidCredential
			}
			id = string(indexedID)
		}
		var r credentialRecord
		if decode(tx.Bucket(bCredentials).Get([]byte(id)), &r) != nil {
			return ErrInvalidCredential
		}
		if legacy {
			h := hashSecret(legacySecret)
			if subtle.ConstantTimeCompare(h[:], r.SecretHash[:]) != 1 {
				return ErrInvalidCredential
			}
		} else if subtle.ConstantTimeCompare(tokenHash[:], r.TokenHash) != 1 {
			return ErrInvalidCredential
		}
		now := s.clock.Now().UTC()
		if !r.RevokedAt.IsZero() || (!r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)) {
			return ErrForbidden
		}
		u, e := getUserRecord(tx, r.UserID)
		if e != nil {
			return ErrForbidden
		}
		if e = entitledUser(u, now); e != nil {
			return ErrForbidden
		}
		out = Identity{UserID: r.UserID, CredentialID: id, CredentialVersion: r.Version}
		return nil
	})
	return out, err
}

func (s *Store) Admit(ctx context.Context, id Identity) error {
	return s.batch(ctx, func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		var c credentialRecord
		if err := decode(tx.Bucket(bCredentials).Get([]byte(id.CredentialID)), &c); err != nil {
			return ErrInvalidCredential
		}
		if c.UserID != id.UserID || c.Version != id.CredentialVersion || !c.RevokedAt.IsZero() || (!c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)) {
			return ErrForbidden
		}
		u, err := getUserRecord(tx, id.UserID)
		if err != nil {
			return ErrForbidden
		}
		if err = entitledUser(u, now); err != nil {
			return err
		}
		start, _, err := periodBounds(now, u.Period, u.Timezone)
		if err != nil {
			return err
		}
		if u.QuotaHighStart != 0 && start.Unix() < u.QuotaHighStart {
			return ErrUnavailable
		}
		if u.QuotaStart != start.Unix() {
			u.QuotaStart = start.Unix()
			u.QuotaUsed = 0
		}
		if start.Unix() > u.QuotaHighStart {
			u.QuotaHighStart = start.Unix()
		}
		if u.QuotaUsed >= u.Limit {
			return ErrQuotaExceeded
		}
		cap := float64(u.QPS) + float64(u.Burst)
		if u.RateAt == 0 {
			u.RateTokens = cap
		} else {
			elapsed := float64(now.UnixNano()-u.RateAt) / float64(time.Second)
			if elapsed > 0 {
				u.RateTokens = math.Min(cap, u.RateTokens+elapsed*float64(u.QPS))
				u.RateAt = now.UnixNano()
			}
		}
		if u.RateAt == 0 {
			u.RateAt = now.UnixNano()
		}
		if u.RateTokens < 1 {
			return ErrRateLimited
		}
		u.RateTokens--
		u.QuotaUsed++
		if err = marshalPut(tx.Bucket(bUsers), []byte(u.ID), u); err != nil {
			return err
		}
		minute := now.Truncate(time.Minute).Unix()
		for _, scope := range []string{"g", "u:" + u.ID} {
			key := usageKey(scope, minute)
			v := tx.Bucket(bUsage).Get(key)
			var n uint64
			if len(v) == 8 {
				n = binary.BigEndian.Uint64(v)
			}
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], n+1)
			if err = tx.Bucket(bUsage).Put(key, buf[:]); err != nil {
				return err
			}
		}
		key := deviceUsageKey(u.ID, minute, id.CredentialID)
		v := tx.Bucket(bUsage).Get(key)
		var n uint64
		if len(v) == 8 {
			n = binary.BigEndian.Uint64(v)
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], n+1)
		if err = tx.Bucket(bUsage).Put(key, buf[:]); err != nil {
			return err
		}
		return nil
	})
}

func usageKey(scope string, minute int64) []byte {
	return []byte(fmt.Sprintf("%s\x00%020d", scope, minute))
}
func deviceUsageKey(userID string, minute int64, credentialID string) []byte {
	return []byte(fmt.Sprintf("d:%s\x00%020d\x00%s", userID, minute, credentialID))
}
func userCredentialKey(userID, credentialID string) []byte {
	return []byte(userID + "\x00" + credentialID)
}
func userSessionKey(userID, sessionID string) []byte { return []byte(userID + "\x00" + sessionID) }
func (s *Store) audit(tx *bbolt.Tx, actor, action, targetType, target string, metadata map[string]any, now time.Time) error {
	id, err := randomText(12)
	if err != nil {
		return err
	}
	r := AuditRecord{ID: id, ActorID: actor, Action: action, TargetType: targetType, TargetID: target, Metadata: metadata, CreatedAt: now}
	return marshalPut(tx.Bucket(bAudit), []byte(fmt.Sprintf("%020d.%s", now.UnixNano(), id)), r)
}

func pageLimit(p Page) (int, error) {
	if p.Limit == 0 {
		return defaultPage, nil
	}
	if p.Limit < 0 || p.Limit > maxPage {
		return 0, ErrInvalidInput
	}
	return p.Limit, nil
}

func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	var out User
	err := s.view(ctx, func(tx *bbolt.Tx) error { r, e := getUserRecord(tx, id); out = r.User; return e })
	return out, err
}
func (s *Store) ListUsers(ctx context.Context, p Page) (PageResult[User], error) {
	limit, e := pageLimit(p)
	if e != nil {
		return PageResult[User]{Items: []User{}}, e
	}
	out := PageResult[User]{Items: []User{}}
	e = s.view(ctx, func(tx *bbolt.Tx) error {
		c := tx.Bucket(bUsers).Cursor()
		k, v := c.First()
		if p.Cursor != "" {
			k, v = c.Seek([]byte(p.Cursor))
			if string(k) == p.Cursor {
				k, v = c.Next()
			}
		}
		for ; k != nil && len(out.Items) < limit+1; k, v = c.Next() {
			var r userRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			out.Items = append(out.Items, r.User)
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			out.NextCursor = out.Items[limit-1].ID
		}
		return nil
	})
	return out, e
}
func (s *Store) ListCredentials(ctx context.Context, userID string, p Page) (PageResult[Credential], error) {
	limit, e := pageLimit(p)
	if e != nil {
		return PageResult[Credential]{Items: []Credential{}}, e
	}
	out := PageResult[Credential]{Items: []Credential{}}
	e = s.view(ctx, func(tx *bbolt.Tx) error {
		index := tx.Bucket(bUserCredentials)
		prefix := []byte(userID + "\x00")
		c := index.Cursor()
		k, v := c.Seek(prefix)
		if p.Cursor != "" {
			cursorKey := userCredentialKey(userID, p.Cursor)
			k, v = c.Seek(cursorKey)
			if string(k) == string(cursorKey) {
				k, v = c.Next()
			}
		}
		for ; k != nil && strings.HasPrefix(string(k), string(prefix)); k, v = c.Next() {
			var r credentialRecord
			if err := json.Unmarshal(tx.Bucket(bCredentials).Get(v), &r); err != nil {
				return err
			}
			out.Items = append(out.Items, r.Credential)
			if len(out.Items) == limit+1 {
				break
			}
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			out.NextCursor = out.Items[limit-1].ID
		}
		return nil
	})
	return out, e
}
func (s *Store) CurrentQuota(ctx context.Context, userID string) (QuotaStatus, error) {
	var out QuotaStatus
	err := s.view(ctx, func(tx *bbolt.Tx) error {
		r, e := getUserRecord(tx, userID)
		if e != nil {
			return e
		}
		start, end, e := periodBounds(s.clock.Now().UTC(), r.Period, r.Timezone)
		if e != nil {
			return e
		}
		used := r.QuotaUsed
		if r.QuotaHighStart != 0 && start.Unix() < r.QuotaHighStart {
			return ErrUnavailable
		}
		if r.QuotaStart != start.Unix() {
			used = 0
		}
		remaining := uint64(0)
		if used < r.Limit {
			remaining = r.Limit - used
		}
		out = QuotaStatus{Period: r.Period, Timezone: r.Timezone, PeriodStart: start, PeriodEnd: end, Limit: r.Limit, Used: used, Remaining: remaining}
		return nil
	})
	return out, err
}

func (s *Store) Usage(ctx context.Context, userID string, from, to time.Time, p Page) (PageResult[UsagePoint], error) {
	scope := "g"
	if userID != "" {
		scope = "u:" + userID
	}
	return s.usage(ctx, scope, userID, "", from, to, p)
}
func (s *Store) CredentialUsage(ctx context.Context, userID, credentialID string, from, to time.Time, p Page) (PageResult[UsagePoint], error) {
	if userID == "" {
		return PageResult[UsagePoint]{Items: []UsagePoint{}}, ErrInvalidInput
	}
	return s.deviceUsage(ctx, userID, credentialID, from, to, p)
}
func (s *Store) usage(ctx context.Context, scope, userID, credentialID string, from, to time.Time, p Page) (PageResult[UsagePoint], error) {
	limit, e := pageLimit(p)
	if e != nil {
		return PageResult[UsagePoint]{Items: []UsagePoint{}}, e
	}
	if to.Before(from) || to.Sub(from) > maxUsageRange {
		return PageResult[UsagePoint]{Items: []UsagePoint{}}, ErrInvalidInput
	}
	out := PageResult[UsagePoint]{Items: []UsagePoint{}}
	prefix := scope + "\x00"
	e = s.view(ctx, func(tx *bbolt.Tx) error {
		c := tx.Bucket(bUsage).Cursor()
		startKey := usageKey(scope, from.Truncate(time.Minute).Unix())
		endKey := usageKey(scope, to.Unix())
		k, v := c.Seek(startKey)
		if p.Cursor != "" {
			if !strings.HasPrefix(p.Cursor, prefix) || p.Cursor < string(startKey) || p.Cursor >= string(endKey) {
				return ErrInvalidInput
			}
			k, v = c.Seek([]byte(p.Cursor))
			if string(k) == p.Cursor {
				k, v = c.Next()
			}
		}
		for ; k != nil && strings.HasPrefix(string(k), prefix); k, v = c.Next() {
			parts := strings.Split(string(k), "\x00")
			if len(parts) != 2 {
				continue
			}
			minute, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return err
			}
			t := time.Unix(minute, 0).UTC()
			if !t.Before(to) {
				break
			}
			if t.Before(from) {
				continue
			}
			out.Items = append(out.Items, UsagePoint{Minute: t, UserID: userID, CredentialID: credentialID, Count: binary.BigEndian.Uint64(v)})
			if len(out.Items) == limit+1 {
				break
			}
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			out.NextCursor = string(usageKey(scope, last.Minute.Unix()))
		}
		return nil
	})
	return out, e
}

func (s *Store) deviceUsage(ctx context.Context, userID, credentialID string, from, to time.Time, p Page) (PageResult[UsagePoint], error) {
	limit, e := pageLimit(p)
	if e != nil {
		return PageResult[UsagePoint]{Items: []UsagePoint{}}, e
	}
	if to.Before(from) || to.Sub(from) > maxUsageRange {
		return PageResult[UsagePoint]{Items: []UsagePoint{}}, ErrInvalidInput
	}
	out := PageResult[UsagePoint]{Items: []UsagePoint{}}
	prefix := "d:" + userID + "\x00"
	start := deviceUsageKey(userID, from.Truncate(time.Minute).Unix(), "")
	end := deviceUsageKey(userID, to.Unix(), "")
	e = s.view(ctx, func(tx *bbolt.Tx) error {
		c := tx.Bucket(bUsage).Cursor()
		k, v := c.Seek(start)
		if p.Cursor != "" {
			if !strings.HasPrefix(p.Cursor, prefix) || p.Cursor < string(start) || p.Cursor >= string(end) {
				return ErrInvalidInput
			}
			k, v = c.Seek([]byte(p.Cursor))
			if string(k) == p.Cursor {
				k, v = c.Next()
			}
		}
		for ; k != nil && strings.HasPrefix(string(k), prefix); k, v = c.Next() {
			parts := strings.Split(string(k), "\x00")
			if len(parts) != 3 {
				continue
			}
			minute, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return err
			}
			at := time.Unix(minute, 0).UTC()
			if !at.Before(to) {
				break
			}
			if at.Before(from) || credentialID != "" && parts[2] != credentialID {
				continue
			}
			out.Items = append(out.Items, UsagePoint{Minute: at, UserID: userID, CredentialID: parts[2], Count: binary.BigEndian.Uint64(v)})
			if len(out.Items) == limit+1 {
				break
			}
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			out.NextCursor = string(deviceUsageKey(userID, last.Minute.Unix(), last.CredentialID))
		}
		return nil
	})
	return out, e
}

func (s *Store) ListAudit(ctx context.Context, from, to time.Time, p Page) (PageResult[AuditRecord], error) {
	limit, e := pageLimit(p)
	if e != nil {
		return PageResult[AuditRecord]{Items: []AuditRecord{}}, e
	}
	if to.Before(from) || to.Sub(from) > maxUsageRange {
		return PageResult[AuditRecord]{Items: []AuditRecord{}}, ErrInvalidInput
	}
	out := PageResult[AuditRecord]{Items: []AuditRecord{}}
	e = s.view(ctx, func(tx *bbolt.Tx) error {
		c := tx.Bucket(bAudit).Cursor()
		startKey := fmt.Sprintf("%020d", from.UnixNano())
		k, v := c.Seek([]byte(startKey))
		if p.Cursor != "" {
			if p.Cursor < startKey {
				return ErrInvalidInput
			}
			k, v = c.Seek([]byte(p.Cursor))
			if string(k) == p.Cursor {
				k, v = c.Next()
			}
		}
		for ; k != nil; k, v = c.Next() {
			var r AuditRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if !r.CreatedAt.Before(to) {
				break
			}
			if r.CreatedAt.Before(from) {
				continue
			}
			out.Items = append(out.Items, r)
			if len(out.Items) == limit+1 {
				break
			}
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			out.NextCursor = fmt.Sprintf("%020d.%s", last.CreatedAt.UnixNano(), last.ID)
		}
		return nil
	})
	return out, e
}

var _ Service = (*Store)(nil)
