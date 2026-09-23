package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

const (
	legacyMySQLControlSchemaVersion = 1
	policyMySQLControlSchemaVersion = 2
	policySwitchMySQLSchemaVersion  = 3
	publicListMySQLSchemaVersion    = 4
	// publicListV5MySQLSchemaVersion is the version whose data migration
	// rewrites public list publication state. It must run only on the way to
	// v5, never again: rerunning it resets what administrators published. It
	// is named apart from mysqlControlSchemaVersion so a later version bump
	// cannot silently make it run again.
	publicListV5MySQLSchemaVersion = 5
	mysqlControlSchemaVersion      = 5
)

type MySQLOptions struct {
	DSN              string
	MaxOpenConns     int
	MaxIdleConns     int
	ConnMaxLifetime  time.Duration
	OperationTimeout time.Duration
	Clock            Clock
}

type MySQLStore struct {
	db               *sql.DB
	clock            Clock
	operationTimeout time.Duration
	closed           atomic.Bool
	closeOnce        sync.Once
	closeCh          chan struct{}
	closeErr         error
	rollbackErrors   atomic.Uint64
}

var mysqlControlMigrations = []string{
	`CREATE TABLE IF NOT EXISTS mosdns_schema_migrations (
		component VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		version INT UNSIGNED NOT NULL
	) ENGINE=InnoDB`,
	`CREATE TABLE IF NOT EXISTS mosdns_control_meta (
		id TINYINT UNSIGNED PRIMARY KEY,
		initialized BOOLEAN NOT NULL DEFAULT FALSE
	) ENGINE=InnoDB`,
	`INSERT INTO mosdns_control_meta (id, initialized) VALUES (1, FALSE)
		ON DUPLICATE KEY UPDATE id = VALUES(id)`,
	`CREATE TABLE IF NOT EXISTS mosdns_users (
		id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		username VARCHAR(128) NOT NULL,
		username_normalized VARCHAR(128) NOT NULL,
		role VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		enabled BOOLEAN NOT NULL,
		expires_at_ns BIGINT NULL,
		period VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		timezone VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		quota_limit BIGINT UNSIGNED NOT NULL,
		qps BIGINT UNSIGNED NOT NULL,
		burst BIGINT UNSIGNED NOT NULL,
		max_credentials BIGINT UNSIGNED NOT NULL,
		created_at_ns BIGINT NOT NULL,
		updated_at_ns BIGINT NOT NULL,
		password_salt VARBINARY(16) NOT NULL,
		password_hash VARBINARY(32) NOT NULL,
		password_version BIGINT UNSIGNED NOT NULL,
		quota_start BIGINT NOT NULL DEFAULT 0,
		quota_high_start BIGINT NOT NULL DEFAULT 0,
		quota_used BIGINT UNSIGNED NOT NULL DEFAULT 0,
		rate_tokens DOUBLE NOT NULL DEFAULT 0,
		rate_at_ns BIGINT NOT NULL DEFAULT 0,
		UNIQUE KEY uq_mosdns_users_username (username_normalized),
		KEY ix_mosdns_users_role_enabled (role, enabled)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS mosdns_sessions (
		id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		user_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		csrf_token VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		secret_hash BINARY(32) NOT NULL,
		expires_at_ns BIGINT NOT NULL,
		revoked_at_ns BIGINT NULL,
		created_at_ns BIGINT NOT NULL,
		KEY ix_mosdns_sessions_user (user_id, id),
		KEY ix_mosdns_sessions_expiry (expires_at_ns),
		CONSTRAINT fk_mosdns_sessions_user FOREIGN KEY (user_id) REFERENCES mosdns_users(id)
	) ENGINE=InnoDB`,
	`CREATE TABLE IF NOT EXISTS mosdns_credentials (
		id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		user_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		name VARCHAR(128) NOT NULL,
		expires_at_ns BIGINT NULL,
		revoked_at_ns BIGINT NULL,
		created_at_ns BIGINT NOT NULL,
		updated_at_ns BIGINT NOT NULL,
		legacy_secret_hash BINARY(32) NULL,
		token_hash BINARY(32) NULL,
		version BIGINT UNSIGNED NOT NULL,
		UNIQUE KEY uq_mosdns_credentials_token (token_hash),
		KEY ix_mosdns_credentials_user (user_id, id),
		KEY ix_mosdns_credentials_retention (revoked_at_ns, expires_at_ns),
		CONSTRAINT fk_mosdns_credentials_user FOREIGN KEY (user_id) REFERENCES mosdns_users(id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS mosdns_usage_minutes (
		scope_kind CHAR(1) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		credential_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		minute_epoch BIGINT NOT NULL,
		query_count BIGINT UNSIGNED NOT NULL,
		PRIMARY KEY (scope_kind, scope_id, credential_id, minute_epoch),
		KEY ix_mosdns_usage_retention (minute_epoch),
		KEY ix_mosdns_usage_device (scope_kind, scope_id, minute_epoch, credential_id)
	) ENGINE=InnoDB`,
	`CREATE TABLE IF NOT EXISTS mosdns_audit_logs (
		id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		actor_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		action VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		target_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		target_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		metadata_json LONGTEXT NULL,
		created_at_ns BIGINT NOT NULL,
		KEY ix_mosdns_audit_time (created_at_ns, id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS mosdns_dns_policy_settings (
		user_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		strip_ecs BOOLEAN NOT NULL DEFAULT FALSE,
		block_private_answers BOOLEAN NOT NULL DEFAULT FALSE,
		blocked_qtypes_json LONGTEXT NOT NULL,
		custom_block_enabled BOOLEAN NOT NULL DEFAULT TRUE,
		custom_allow_enabled BOOLEAN NOT NULL DEFAULT TRUE,
		custom_rewrite_enabled BOOLEAN NOT NULL DEFAULT TRUE,
		policy_paused_until_ns BIGINT NULL,
		answer_family VARCHAR(8) NOT NULL DEFAULT '',
		updated_at_ns BIGINT NOT NULL,
		CONSTRAINT fk_mosdns_dns_policy_settings_user FOREIGN KEY (user_id) REFERENCES mosdns_users(id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS mosdns_dns_policy_rules (
		id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		user_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		enabled BOOLEAN NOT NULL,
		priority BIGINT UNSIGNED NOT NULL,
		action VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		match_kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		pattern VARCHAR(1024) NOT NULL,
		record_type VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
		rewrite_value VARCHAR(1024) NULL,
		created_at_ns BIGINT NOT NULL,
		updated_at_ns BIGINT NOT NULL,
		KEY ix_mosdns_dns_policy_rules_user (user_id, priority, id),
		CONSTRAINT fk_mosdns_dns_policy_rules_user FOREIGN KEY (user_id) REFERENCES mosdns_users(id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
	`INSERT INTO mosdns_dns_policy_settings
		(user_id, strip_ecs, block_private_answers, blocked_qtypes_json, custom_block_enabled,
		 custom_allow_enabled, custom_rewrite_enabled, policy_paused_until_ns, updated_at_ns)
		SELECT id, FALSE, FALSE, '[]', TRUE, TRUE, TRUE, NULL, created_at_ns FROM mosdns_users
		ON DUPLICATE KEY UPDATE user_id = VALUES(user_id)`,
	`CREATE TABLE IF NOT EXISTS mosdns_public_lists (
		id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		name_normalized VARCHAR(128) NOT NULL,
		category VARCHAR(64) NOT NULL,
		url VARCHAR(2048) NOT NULL,
		format VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		enabled BOOLEAN NOT NULL,
		default_enabled BOOLEAN NOT NULL,
		published BOOLEAN NOT NULL,
		sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		refresh_seconds BIGINT UNSIGNED NOT NULL,
		entry_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
		last_refresh_status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'never',
		last_refreshed_at_ns BIGINT NULL,
		last_refresh_error VARCHAR(512) NOT NULL DEFAULT '',
		snapshot_status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'missing',
		snapshot_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
		last_successful_at_ns BIGINT NULL,
		created_at_ns BIGINT NOT NULL,
		updated_at_ns BIGINT NOT NULL,
		UNIQUE KEY uq_mosdns_public_lists_name (name_normalized)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS mosdns_user_public_lists (
		user_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		list_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		enabled BOOLEAN NOT NULL,
		PRIMARY KEY (user_id, list_id),
		KEY ix_mosdns_user_public_lists_list (list_id, user_id),
		CONSTRAINT fk_mosdns_user_public_lists_user FOREIGN KEY (user_id) REFERENCES mosdns_users(id),
		CONSTRAINT fk_mosdns_user_public_lists_list FOREIGN KEY (list_id) REFERENCES mosdns_public_lists(id)
	) ENGINE=InnoDB`,
}

var mysqlControlV3Columns = []struct {
	name       string
	definition string
}{
	{"custom_block_enabled", "custom_block_enabled BOOLEAN NOT NULL DEFAULT TRUE"},
	{"custom_allow_enabled", "custom_allow_enabled BOOLEAN NOT NULL DEFAULT TRUE"},
	{"custom_rewrite_enabled", "custom_rewrite_enabled BOOLEAN NOT NULL DEFAULT TRUE"},
	{"policy_paused_until_ns", "policy_paused_until_ns BIGINT NULL"},
}

var mysqlControlV5PublicListColumns = []struct {
	name       string
	definition string
}{
	{"default_enabled", "default_enabled BOOLEAN NOT NULL DEFAULT FALSE"},
	{"published", "published BOOLEAN NOT NULL DEFAULT TRUE"},
	{"snapshot_status", "snapshot_status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'missing'"},
	{"snapshot_sha256", "snapshot_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT ''"},
	{"last_successful_at_ns", "last_successful_at_ns BIGINT NULL"},
}

const mysqlControlV5PublicListColumnsQuery = `SELECT COLUMN_NAME FROM information_schema.COLUMNS
	WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'mosdns_public_lists'
	AND COLUMN_NAME IN ('default_enabled', 'published', 'snapshot_status', 'snapshot_sha256', 'last_successful_at_ns')`

const mysqlControlV3ColumnsQuery = `SELECT COLUMN_NAME FROM information_schema.COLUMNS
	WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'mosdns_dns_policy_settings'
	AND COLUMN_NAME IN ('custom_block_enabled', 'custom_allow_enabled', 'custom_rewrite_enabled', 'policy_paused_until_ns')`

func OpenMySQL(opts MySQLOptions) (*MySQLStore, error) {
	return OpenMySQLContext(context.Background(), opts)
}

// OpenMySQLContext allows callers to cancel connection setup and schema initialization.
func OpenMySQLContext(parent context.Context, opts MySQLOptions) (*MySQLStore, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.DSN) == "" {
		return nil, fmt.Errorf("%w: empty mysql dsn", ErrInvalidInput)
	}
	parsed, err := mysqlDriver.ParseDSN(opts.DSN)
	if err != nil || parsed.DBName == "" {
		return nil, fmt.Errorf("%w: invalid mysql dsn", ErrInvalidInput)
	}
	db, err := sql.Open("mysql", parsed.FormatDSN())
	if err != nil {
		return nil, mysqlStoreError(err)
	}
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = 32
	}
	if opts.MaxIdleConns < 0 {
		_ = db.Close()
		return nil, fmt.Errorf("%w: negative mysql max idle connections", ErrInvalidInput)
	}
	if opts.MaxIdleConns == 0 {
		opts.MaxIdleConns = 8
	}
	if opts.MaxIdleConns > opts.MaxOpenConns {
		_ = db.Close()
		return nil, fmt.Errorf("%w: mysql max idle connections exceed max open connections", ErrInvalidInput)
	}
	if opts.ConnMaxLifetime <= 0 {
		opts.ConnMaxLifetime = 30 * time.Minute
	}
	if opts.OperationTimeout <= 0 {
		opts.OperationTimeout = 500 * time.Millisecond
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	db.SetMaxOpenConns(opts.MaxOpenConns)
	db.SetMaxIdleConns(opts.MaxIdleConns)
	db.SetConnMaxLifetime(opts.ConnMaxLifetime)
	s := &MySQLStore{db: db, clock: opts.Clock, operationTimeout: opts.OperationTimeout, closeCh: make(chan struct{})}
	startupTimeout := max(opts.OperationTimeout, 15*time.Second)
	ctx, cancel := context.WithTimeout(parent, startupTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, mysqlStoreError(err)
	}
	if err := initializeMySQLControl(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func initializeMySQLControl(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return mysqlStoreError(err)
	}
	defer conn.Close()
	var locked int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('mosdns_x_control_schema', 10)`).Scan(&locked); err != nil || locked != 1 {
		if err == nil {
			err = errors.New("mysql migration lock unavailable")
		}
		return mysqlStoreError(err)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(releaseCtx, `SELECT RELEASE_LOCK('mosdns_x_control_schema')`)
	}()
	if _, err := conn.ExecContext(ctx, mysqlControlMigrations[0]); err != nil {
		return mysqlStoreError(err)
	}
	var version int
	err = conn.QueryRowContext(ctx, `SELECT version FROM mosdns_schema_migrations WHERE component = 'control'`).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return mysqlStoreError(err)
	}
	if err == nil && (version > mysqlControlSchemaVersion || version < legacyMySQLControlSchemaVersion) {
		return fmt.Errorf("%w: unsupported mysql control schema version %d", ErrUnavailable, version)
	}
	if err == nil && version == policyMySQLControlSchemaVersion {
		if migrationErr := ensureMySQLControlV3Columns(ctx, conn); migrationErr != nil {
			return mysqlStoreError(migrationErr)
		}
	}
	for _, statement := range mysqlControlMigrations[1:] {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return mysqlStoreError(err)
		}
	}
	if errors.Is(err, sql.ErrNoRows) || version < publicListV5MySQLSchemaVersion {
		if migrationErr := ensureMySQLControlV5PublicListColumns(ctx, conn); migrationErr != nil {
			return mysqlStoreError(migrationErr)
		}
		if _, migrationErr := conn.ExecContext(ctx, `UPDATE mosdns_public_lists SET default_enabled=enabled, published=TRUE, snapshot_status=IF(last_refresh_status='success','current',IF(entry_count>0,'stale','missing')), snapshot_sha256='', last_successful_at_ns=IF(last_refresh_status='success',last_refreshed_at_ns,NULL)`); migrationErr != nil {
			return mysqlStoreError(migrationErr)
		}
	}
	// answer_family is additive and defaulted, so it is ensured on every start
	// instead of behind a version bump. An older binary names its columns in
	// every statement and never reads it, so it can still open this database,
	// which keeps rolling back the binary possible.
	if migrationErr := ensureMySQLAnswerFamilyColumn(ctx, conn); migrationErr != nil {
		return mysqlStoreError(migrationErr)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = conn.ExecContext(ctx, `INSERT INTO mosdns_schema_migrations (component, version) VALUES ('control', ?)`, mysqlControlSchemaVersion)
		return mysqlStoreError(err)
	case version < mysqlControlSchemaVersion:
		_, err = conn.ExecContext(ctx, `UPDATE mosdns_schema_migrations SET version=? WHERE component='control'`, mysqlControlSchemaVersion)
		return mysqlStoreError(err)
	default:
		return nil
	}
}

func ensureMySQLControlV5PublicListColumns(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, mysqlControlV5PublicListColumnsQuery)
	if err != nil {
		return err
	}
	existing := make(map[string]struct{}, len(mysqlControlV5PublicListColumns))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	migration := mysqlControlV5PublicListAlter(existing)
	if migration == "" {
		return nil
	}
	_, err = conn.ExecContext(ctx, migration)
	return err
}

func mysqlControlV5PublicListAlter(existing map[string]struct{}) string {
	missing := make([]string, 0, len(mysqlControlV5PublicListColumns))
	for _, column := range mysqlControlV5PublicListColumns {
		if _, ok := existing[column.name]; !ok {
			missing = append(missing, "ADD COLUMN "+column.definition)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return `ALTER TABLE mosdns_public_lists ` + strings.Join(missing, ", ")
}

func ensureMySQLControlV3Columns(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, mysqlControlV3ColumnsQuery)
	if err != nil {
		return err
	}
	existing := make(map[string]struct{}, len(mysqlControlV3Columns))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	migration := mysqlControlV3Alter(existing)
	if migration == "" {
		return nil
	}
	_, err = conn.ExecContext(ctx, migration)
	return err
}

const mysqlAnswerFamilyColumnQuery = `SELECT COLUMN_NAME FROM information_schema.COLUMNS
	WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'mosdns_dns_policy_settings'
	AND COLUMN_NAME = 'answer_family'`

// ensureMySQLAnswerFamilyColumn adds answer_family when it is missing. It reads
// the live column list, so a table that already has the column, including one
// created fresh or upgraded earlier, is left untouched. It runs under the
// schema lock, so concurrent starts cannot both issue the ALTER.
func ensureMySQLAnswerFamilyColumn(ctx context.Context, conn *sql.Conn) error {
	var name string
	err := conn.QueryRowContext(ctx, mysqlAnswerFamilyColumnQuery).Scan(&name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = conn.ExecContext(ctx, `ALTER TABLE mosdns_dns_policy_settings ADD COLUMN answer_family VARCHAR(8) NOT NULL DEFAULT ''`)
	return err
}

func mysqlControlV3Alter(existing map[string]struct{}) string {
	missing := make([]string, 0, len(mysqlControlV3Columns))
	for _, column := range mysqlControlV3Columns {
		if _, ok := existing[column.name]; !ok {
			missing = append(missing, "ADD COLUMN "+column.definition)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return `ALTER TABLE mosdns_dns_policy_settings ` + strings.Join(missing, ", ")
}

func (s *MySQLStore) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= s.operationTimeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, s.operationTimeout)
}

func (s *MySQLStore) withTx(parent context.Context, fn func(context.Context, *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrUnavailable
	}
	ctx, cancel := s.operationContext(parent)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return mysqlStoreError(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			s.rollbackErrors.Add(1)
		}
	}()
	if err = fn(ctx, tx); err != nil {
		return mysqlStoreError(err)
	}
	if err = tx.Commit(); err != nil {
		return mysqlStoreError(err)
	}
	return nil
}

func (s *MySQLStore) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.closeCh)
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

func mysqlStoreError(err error) error {
	if err == nil || isDomainError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return ErrConflict
	}
	return fmt.Errorf("%w: mysql: %v", ErrUnavailable, err)
}

func mysqlTimeValue(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v.UTC().UnixNano()
}

func mysqlNullTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(0, v.Int64).UTC()
}

type sqlScanner interface{ Scan(...any) error }

const mysqlUserColumns = `id, username, role, enabled, expires_at_ns, period, timezone,
	quota_limit, qps, burst, max_credentials, created_at_ns, updated_at_ns,
	password_salt, password_hash, password_version, quota_start, quota_high_start,
	quota_used, rate_tokens, rate_at_ns`

func scanMySQLUser(row sqlScanner) (userRecord, error) {
	var r userRecord
	var role, period string
	var expires sql.NullInt64
	var qps, burst, maxCredentials uint64
	var created, updated int64
	err := row.Scan(&r.ID, &r.Username, &role, &r.Enabled, &expires, &period, &r.Timezone,
		&r.Limit, &qps, &burst, &maxCredentials, &created, &updated,
		&r.PasswordSalt, &r.PasswordHash, &r.PasswordVersion, &r.QuotaStart,
		&r.QuotaHighStart, &r.QuotaUsed, &r.RateTokens, &r.RateAt)
	if err != nil {
		return r, err
	}
	if qps > math.MaxUint32 || burst > math.MaxUint32 || maxCredentials > math.MaxUint32 {
		return r, errors.New("mysql user limits exceed application range")
	}
	r.Role = Role(role)
	r.Period = Period(period)
	r.QPS = uint32(qps)
	r.Burst = uint32(burst)
	r.MaxCredentials = uint32(maxCredentials)
	r.ExpiresAt = mysqlNullTime(expires)
	r.CreatedAt = time.Unix(0, created).UTC()
	r.UpdatedAt = time.Unix(0, updated).UTC()
	return r, nil
}

func mysqlUser(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string, lock bool) (userRecord, error) {
	query := `SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ?`
	if lock {
		query += ` FOR UPDATE`
	}
	r, err := scanMySQLUser(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func mysqlUserByName(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, username string, lock bool) (userRecord, error) {
	query := `SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE username_normalized = ?`
	if lock {
		query += ` FOR UPDATE`
	}
	r, err := scanMySQLUser(q.QueryRowContext(ctx, query, strings.ToLower(strings.TrimSpace(username))))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrInvalidCredential
	}
	return r, err
}

func insertMySQLUser(ctx context.Context, tx *sql.Tx, r userRecord) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mosdns_users (
		id, username, username_normalized, role, enabled, expires_at_ns, period, timezone,
		quota_limit, qps, burst, max_credentials, created_at_ns, updated_at_ns,
		password_salt, password_hash, password_version, quota_start, quota_high_start,
		quota_used, rate_tokens, rate_at_ns
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Username, strings.ToLower(r.Username), string(r.Role), r.Enabled, mysqlTimeValue(r.ExpiresAt),
		string(r.Period), r.Timezone, r.Limit, r.QPS, r.Burst, r.MaxCredentials,
		r.CreatedAt.UnixNano(), r.UpdatedAt.UnixNano(), r.PasswordSalt, r.PasswordHash,
		r.PasswordVersion, r.QuotaStart, r.QuotaHighStart, r.QuotaUsed, r.RateTokens, r.RateAt)
	return err
}

func updateMySQLUser(ctx context.Context, tx *sql.Tx, r userRecord) error {
	_, err := tx.ExecContext(ctx, `UPDATE mosdns_users SET enabled=?, expires_at_ns=?, period=?, timezone=?,
		quota_limit=?, qps=?, burst=?, max_credentials=?, updated_at_ns=?, password_salt=?,
		password_hash=?, password_version=?, quota_start=?, quota_high_start=?, quota_used=?,
		rate_tokens=?, rate_at_ns=? WHERE id=?`, r.Enabled, mysqlTimeValue(r.ExpiresAt), string(r.Period),
		r.Timezone, r.Limit, r.QPS, r.Burst, r.MaxCredentials, r.UpdatedAt.UnixNano(), r.PasswordSalt,
		r.PasswordHash, r.PasswordVersion, r.QuotaStart, r.QuotaHighStart, r.QuotaUsed,
		r.RateTokens, r.RateAt, r.ID)
	return err
}

func requireMySQLAdmin(ctx context.Context, tx *sql.Tx, actor string) error {
	r, err := mysqlUser(ctx, tx, actor, true)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrForbidden
		}
		return err
	}
	if enabledUser(r) != nil || r.Role != RoleAdmin {
		return ErrForbidden
	}
	return nil
}

func mysqlAudit(ctx context.Context, tx *sql.Tx, actor, action, targetType, target string, metadata map[string]any, now time.Time) error {
	id, err := randomText(12)
	if err != nil {
		return err
	}
	var encoded any
	if metadata != nil {
		b, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		encoded = b
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_audit_logs
		(id, actor_id, action, target_type, target_id, metadata_json, created_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, actor, action, targetType, target, encoded, now.UnixNano())
	return err
}

func newUserRecord(spec UserSpec, salt, passwordHash []byte, now time.Time, id string) userRecord {
	u := User{ID: id, Username: spec.Username, Role: spec.Role, Enabled: spec.Enabled,
		ExpiresAt: spec.ExpiresAt, Period: spec.Period, Timezone: spec.Timezone, Limit: spec.Limit,
		QPS: spec.QPS, Burst: spec.Burst, MaxCredentials: spec.MaxCredentials, CreatedAt: now, UpdatedAt: now}
	return userRecord{User: u, PasswordSalt: salt, PasswordHash: passwordHash, PasswordVersion: 1,
		RateTokens: float64(u.QPS) + float64(u.Burst), RateAt: now.UnixNano()}
}

func (s *MySQLStore) InitializeAdmin(ctx context.Context, spec UserSpec) (User, error) {
	spec.Role, spec.Enabled = RoleAdmin, true
	spec, err := normalizeSpec(spec)
	if err != nil {
		return User{}, err
	}
	salt, passwordHash, err := hashPassword(spec.Password)
	if err != nil {
		return User{}, err
	}
	id, err := randomText(16)
	if err != nil {
		return User{}, err
	}
	now := s.clock.Now().UTC()
	r := newUserRecord(spec, salt, passwordHash, now, id)
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var initialized bool
		if err := tx.QueryRowContext(ctx, `SELECT initialized FROM mosdns_control_meta WHERE id=1 FOR UPDATE`).Scan(&initialized); err != nil {
			return err
		}
		if initialized {
			return ErrConflict
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_users`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrConflict
		}
		if err := insertMySQLUser(ctx, tx, r); err != nil {
			return err
		}
		if err := insertMySQLDNSPolicySettings(ctx, tx, defaultDNSPolicySettings(id, now)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mosdns_control_meta SET initialized=TRUE WHERE id=1`); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, "", "initialize_admin", "user", id, nil, now)
	})
	return r.User, err
}

func (s *MySQLStore) CreateUser(ctx context.Context, actor string, spec UserSpec) (User, error) {
	spec, err := normalizeSpec(spec)
	if err != nil {
		return User{}, err
	}
	salt, passwordHash, err := hashPassword(spec.Password)
	if err != nil {
		return User{}, err
	}
	id, err := randomText(16)
	if err != nil {
		return User{}, err
	}
	now := s.clock.Now().UTC()
	r := newUserRecord(spec, salt, passwordHash, now, id)
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
			return err
		}
		if err := insertMySQLUser(ctx, tx, r); err != nil {
			return err
		}
		if err := insertMySQLDNSPolicySettings(ctx, tx, defaultDNSPolicySettings(id, now)); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "create_user", "user", id, nil, now)
	})
	return r.User, err
}

func (s *MySQLStore) passwordSnapshot(ctx context.Context, username string) (userRecord, error) {
	if s.closed.Load() {
		return userRecord{}, ErrUnavailable
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	r, err := mysqlUserByName(opCtx, s.db, username, false)
	return r, mysqlStoreError(err)
}

func (s *MySQLStore) AuthenticatePassword(ctx context.Context, username, password string) (User, error) {
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

func (s *MySQLStore) Login(ctx context.Context, username, password string, ttl time.Duration) (Session, string, error) {
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
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		current, err := mysqlUser(ctx, tx, snapshot.ID, true)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrInvalidCredential
			}
			return err
		}
		if enabledUser(current) != nil || current.PasswordVersion != snapshot.PasswordVersion || subtle.ConstantTimeCompare(current.PasswordHash, snapshot.PasswordHash) != 1 {
			return ErrInvalidCredential
		}
		now := s.clock.Now().UTC()
		session = Session{ID: id, UserID: current.ID, CSRFToken: csrf, ExpiresAt: now.Add(ttl), CreatedAt: now}
		hash := hashSecret(secret)
		_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_sessions
			(id, user_id, csrf_token, secret_hash, expires_at_ns, revoked_at_ns, created_at_ns)
			VALUES (?, ?, ?, ?, ?, NULL, ?)`, id, current.ID, csrf, hash[:], session.ExpiresAt.UnixNano(), now.UnixNano())
		return err
	})
	return session, id + "." + secret, err
}

func (s *MySQLStore) CreateSession(ctx context.Context, userID string, ttl time.Duration) (Session, string, error) {
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
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		u, err := mysqlUser(ctx, tx, userID, false)
		if err != nil {
			return err
		}
		if err := enabledUser(u); err != nil {
			return err
		}
		hash := hashSecret(secret)
		_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_sessions
			(id, user_id, csrf_token, secret_hash, expires_at_ns, revoked_at_ns, created_at_ns)
			VALUES (?, ?, ?, ?, ?, NULL, ?)`, id, userID, csrf, hash[:], ss.ExpiresAt.UnixNano(), now.UnixNano())
		return err
	})
	return ss, id + "." + secret, err
}

func (s *MySQLStore) AuthenticateSession(ctx context.Context, token string) (Session, User, error) {
	id, secret, ok := splitToken(token)
	if s.closed.Load() {
		return Session{}, User{}, ErrUnavailable
	}
	if !ok {
		return Session{}, User{}, ErrInvalidCredential
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	var ss Session
	var userID, csrf string
	var secretHash []byte
	var expires, created int64
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(opCtx, `SELECT user_id, csrf_token, secret_hash, expires_at_ns, revoked_at_ns, created_at_ns
		FROM mosdns_sessions WHERE id=?`, id).Scan(&userID, &csrf, &secretHash, &expires, &revoked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrInvalidCredential
	}
	if err != nil {
		return Session{}, User{}, mysqlStoreError(err)
	}
	h := hashSecret(secret)
	if subtle.ConstantTimeCompare(h[:], secretHash) != 1 || revoked.Valid || !s.clock.Now().UTC().Before(time.Unix(0, expires)) {
		return Session{}, User{}, ErrInvalidCredential
	}
	u, err := mysqlUser(opCtx, s.db, userID, false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Session{}, User{}, ErrInvalidCredential
		}
		return Session{}, User{}, mysqlStoreError(err)
	}
	if enabledUser(u) != nil {
		return Session{}, User{}, ErrInvalidCredential
	}
	ss = Session{ID: id, UserID: userID, CSRFToken: csrf, ExpiresAt: time.Unix(0, expires).UTC(), CreatedAt: time.Unix(0, created).UTC()}
	return ss, u.User, nil
}

func revokeMySQLUserSessions(ctx context.Context, tx *sql.Tx, userID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE mosdns_sessions SET revoked_at_ns=? WHERE user_id=? AND revoked_at_ns IS NULL`, now.UnixNano(), userID)
	return err
}

func (s *MySQLStore) SetPassword(ctx context.Context, actor, userID, password string) error {
	salt, passwordHash, err := hashPassword(password)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if actor != userID {
			if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
				return err
			}
		}
		r, err := mysqlUser(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		r.PasswordSalt, r.PasswordHash = salt, passwordHash
		r.PasswordVersion++
		r.UpdatedAt = now
		if err := updateMySQLUser(ctx, tx, r); err != nil {
			return err
		}
		if err := revokeMySQLUserSessions(ctx, tx, userID, now); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "set_password", "user", userID, nil, now)
	})
}

func (s *MySQLStore) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if s.closed.Load() {
		return ErrUnavailable
	}
	opCtx, cancel := s.operationContext(ctx)
	snapshot, err := mysqlUser(opCtx, s.db, userID, false)
	cancel()
	if err != nil {
		return mysqlStoreError(err)
	}
	if !verifyPassword(currentPassword, snapshot.PasswordSalt, snapshot.PasswordHash) {
		return ErrInvalidCredential
	}
	salt, passwordHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		r, err := mysqlUser(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		if enabledUser(r) != nil {
			return ErrForbidden
		}
		if r.PasswordVersion != snapshot.PasswordVersion || subtle.ConstantTimeCompare(r.PasswordHash, snapshot.PasswordHash) != 1 {
			return ErrInvalidCredential
		}
		r.PasswordSalt, r.PasswordHash = salt, passwordHash
		r.PasswordVersion++
		r.UpdatedAt = now
		if err := updateMySQLUser(ctx, tx, r); err != nil {
			return err
		}
		if err := revokeMySQLUserSessions(ctx, tx, userID, now); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, userID, "change_password", "user", userID, nil, now)
	})
}

func (s *MySQLStore) RevokeSession(ctx context.Context, actor, id string) error {
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var userID string
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM mosdns_sessions WHERE id=? FOR UPDATE`, id).Scan(&userID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if actor != userID {
			if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mosdns_sessions SET revoked_at_ns=? WHERE id=?`, now.UnixNano(), id); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "revoke_session", "session", id, nil, now)
	})
}

func (s *MySQLStore) UpdateUser(ctx context.Context, actor, userID string, p UserPatch) (User, error) {
	var out User
	now := s.clock.Now().UTC()
	err := s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireMySQLAdmin(ctx, tx, actor); err != nil {
			return err
		}
		r, err := mysqlUser(ctx, tx, userID, true)
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
			start, _, err := periodBounds(now, r.Period, r.Timezone)
			if err != nil {
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
			newStart, _, err := periodBounds(now, r.Period, r.Timezone)
			if err != nil {
				return ErrInvalidInput
			}
			r.QuotaStart = 0
			r.QuotaHighStart = newStart.Unix()
		}
		if r.Role == RoleAdmin && !r.Enabled {
			var initialized bool
			if err := tx.QueryRowContext(ctx, `SELECT initialized FROM mosdns_control_meta WHERE id=1 FOR UPDATE`).Scan(&initialized); err != nil {
				return err
			}
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_users WHERE role='admin' AND enabled=TRUE AND id<>?`, userID).Scan(&active); err != nil {
				return err
			}
			if active == 0 {
				return fmt.Errorf("%w: cannot disable the last administrator", ErrConflict)
			}
		}
		r.UpdatedAt = now
		cap := float64(r.QPS) + float64(r.Burst)
		if r.RateTokens > cap {
			r.RateTokens = cap
		}
		if err := updateMySQLUser(ctx, tx, r); err != nil {
			return err
		}
		if oldEnabled && !r.Enabled {
			if err := revokeMySQLUserSessions(ctx, tx, userID, now); err != nil {
				return err
			}
		}
		out = r.User
		return mysqlAudit(ctx, tx, actor, "update_user", "user", userID, map[string]any{"before": before, "after": r.User}, now)
	})
	return out, err
}

type mysqlCredentialRecord struct {
	Credential
	LegacySecretHash []byte
	TokenHash        []byte
	Version          uint64
}

func scanMySQLCredential(row sqlScanner) (mysqlCredentialRecord, error) {
	var r mysqlCredentialRecord
	var expires, revoked sql.NullInt64
	var created, updated int64
	err := row.Scan(&r.ID, &r.UserID, &r.Name, &expires, &revoked, &created, &updated, &r.LegacySecretHash, &r.TokenHash, &r.Version)
	if err != nil {
		return r, err
	}
	r.ExpiresAt, r.RevokedAt = mysqlNullTime(expires), mysqlNullTime(revoked)
	r.CreatedAt, r.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return r, nil
}

const mysqlCredentialColumns = `id, user_id, name, expires_at_ns, revoked_at_ns, created_at_ns, updated_at_ns, legacy_secret_hash, token_hash, version`

func mysqlCredential(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string, lock bool) (mysqlCredentialRecord, error) {
	query := `SELECT ` + mysqlCredentialColumns + ` FROM mosdns_credentials WHERE id=?`
	if lock {
		query += ` FOR UPDATE`
	}
	r, err := scanMySQLCredential(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func authorizeMySQLCredentialOwner(ctx context.Context, tx *sql.Tx, actor, userID string, now time.Time) error {
	if actor == userID {
		r, err := mysqlUser(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		return enabledUser(r)
	}
	return requireMySQLAdmin(ctx, tx, actor)
}

func (s *MySQLStore) CreateCredential(ctx context.Context, actor, userID, name string, expires time.Time) (IssuedCredential, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return IssuedCredential{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	if !expires.IsZero() && !now.Before(expires) {
		return IssuedCredential{}, fmt.Errorf("%w: credential expiry must be in the future", ErrInvalidInput)
	}
	id, err := randomText(16)
	if err != nil {
		return IssuedCredential{}, err
	}
	token, err := randomUUIDv4()
	if err != nil {
		return IssuedCredential{}, err
	}
	tokenHash := hashSecret(token)
	c := Credential{ID: id, UserID: userID, Name: name, ExpiresAt: expires, CreatedAt: now, UpdatedAt: now}
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		u, err := mysqlUser(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		if !expires.IsZero() && !u.ExpiresAt.IsZero() && expires.After(u.ExpiresAt) {
			return ErrInvalidInput
		}
		var count uint64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_credentials
			WHERE user_id=? AND revoked_at_ns IS NULL AND (expires_at_ns IS NULL OR expires_at_ns>?)`, userID, now.UnixNano()).Scan(&count); err != nil {
			return err
		}
		if count >= uint64(u.MaxCredentials) {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mosdns_credentials
			(id, user_id, name, expires_at_ns, revoked_at_ns, created_at_ns, updated_at_ns, legacy_secret_hash, token_hash, version)
			VALUES (?, ?, ?, ?, NULL, ?, ?, NULL, ?, 1)`, id, userID, name, mysqlTimeValue(expires), now.UnixNano(), now.UnixNano(), tokenHash[:]); err != nil {
			return err
		}
		return mysqlAudit(ctx, tx, actor, "create_credential", "credential", id, map[string]any{"name": name}, now)
	})
	return IssuedCredential{Credential: c, Token: token}, err
}

func (s *MySQLStore) RotateCredential(ctx context.Context, actor, userID, id string) (IssuedCredential, error) {
	now := s.clock.Now().UTC()
	token, err := randomUUIDv4()
	if err != nil {
		return IssuedCredential{}, err
	}
	tokenHash := hashSecret(token)
	var out Credential
	err = s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		r, err := mysqlCredential(ctx, tx, id, true)
		if err != nil {
			return err
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
		if _, err := tx.ExecContext(ctx, `UPDATE mosdns_credentials SET legacy_secret_hash=NULL, token_hash=?, version=version+1, updated_at_ns=? WHERE id=?`, tokenHash[:], now.UnixNano(), id); err != nil {
			return err
		}
		r.Version++
		r.UpdatedAt = now
		out = r.Credential
		return mysqlAudit(ctx, tx, actor, "rotate_credential", "credential", id, nil, now)
	})
	return IssuedCredential{Credential: out, Token: token}, err
}

func (s *MySQLStore) RevokeCredential(ctx context.Context, actor, userID, id string) error {
	now := s.clock.Now().UTC()
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := authorizeMySQLCredentialOwner(ctx, tx, actor, userID, now); err != nil {
			return err
		}
		r, err := mysqlCredential(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if r.UserID != userID {
			return ErrForbidden
		}
		if r.RevokedAt.IsZero() {
			if _, err := tx.ExecContext(ctx, `UPDATE mosdns_credentials SET revoked_at_ns=?, updated_at_ns=? WHERE id=?`, now.UnixNano(), now.UnixNano(), id); err != nil {
				return err
			}
		}
		return mysqlAudit(ctx, tx, actor, "revoke_credential", "credential", id, nil, now)
	})
}

func (s *MySQLStore) AuthenticateCredential(ctx context.Context, token string) (Identity, error) {
	if s.closed.Load() {
		return Identity{}, ErrUnavailable
	}
	legacy := false
	var id, secret string
	var tokenHash [sha256.Size]byte
	if validUUIDv4(token) {
		tokenHash = hashSecret(token)
	} else {
		var ok bool
		id, secret, ok = splitToken(token)
		if !ok {
			return Identity{}, ErrInvalidCredential
		}
		legacy = true
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	var r mysqlCredentialRecord
	var err error
	if legacy {
		r, err = mysqlCredential(opCtx, s.db, id, false)
	} else {
		r, err = scanMySQLCredential(s.db.QueryRowContext(opCtx, `SELECT `+mysqlCredentialColumns+` FROM mosdns_credentials WHERE token_hash=?`, tokenHash[:]))
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return Identity{}, ErrInvalidCredential
	}
	if err != nil {
		return Identity{}, mysqlStoreError(err)
	}
	if legacy {
		h := hashSecret(secret)
		if subtle.ConstantTimeCompare(h[:], r.LegacySecretHash) != 1 {
			return Identity{}, ErrInvalidCredential
		}
	} else if subtle.ConstantTimeCompare(tokenHash[:], r.TokenHash) != 1 {
		return Identity{}, ErrInvalidCredential
	}
	now := s.clock.Now().UTC()
	if !r.RevokedAt.IsZero() || !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
		return Identity{}, ErrForbidden
	}
	u, err := mysqlUser(opCtx, s.db, r.UserID, false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Identity{}, ErrForbidden
		}
		return Identity{}, mysqlStoreError(err)
	}
	if entitledUser(u, now) != nil {
		return Identity{}, ErrForbidden
	}
	return Identity{UserID: r.UserID, CredentialID: r.ID, CredentialVersion: r.Version}, nil
}

func (s *MySQLStore) Admit(ctx context.Context, identity Identity) error {
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := s.clock.Now().UTC()
		u, err := mysqlUser(ctx, tx, identity.UserID, true)
		if errors.Is(err, ErrNotFound) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		c, err := mysqlCredential(ctx, tx, identity.CredentialID, true)
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidCredential
		}
		if err != nil {
			return err
		}
		if c.UserID != identity.UserID || c.Version != identity.CredentialVersion || !c.RevokedAt.IsZero() || !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
			return ErrForbidden
		}
		if err := entitledUser(u, now); err != nil {
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
			u.QuotaStart, u.QuotaUsed = start.Unix(), 0
		}
		if start.Unix() > u.QuotaHighStart {
			u.QuotaHighStart = start.Unix()
		}
		if u.QuotaUsed >= u.Limit {
			return ErrQuotaExceeded
		}
		if err := consumeRateToken(&u, now); err != nil {
			return err
		}
		u.QuotaUsed++
		if err := updateMySQLUser(ctx, tx, u); err != nil {
			return err
		}
		minute := now.Truncate(time.Minute).Unix()
		for _, row := range [][3]string{{"g", "", ""}, {"u", u.ID, ""}, {"d", u.ID, identity.CredentialID}} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mosdns_usage_minutes
				(scope_kind, scope_id, credential_id, minute_epoch, query_count) VALUES (?, ?, ?, ?, 1)
				ON DUPLICATE KEY UPDATE query_count=query_count+1`, row[0], row[1], row[2], minute); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *MySQLStore) GetUser(ctx context.Context, id string) (User, error) {
	if s.closed.Load() {
		return User{}, ErrUnavailable
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	r, err := mysqlUser(opCtx, s.db, id, false)
	return r.User, mysqlStoreError(err)
}

func (s *MySQLStore) ListUsers(ctx context.Context, p Page) (PageResult[User], error) {
	limit, err := pageLimit(p)
	out := PageResult[User]{Items: []User{}}
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(opCtx, `SELECT `+mysqlUserColumns+` FROM mosdns_users WHERE id>? ORDER BY id LIMIT ?`, p.Cursor, limit+1)
	if err != nil {
		return out, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanMySQLUser(rows)
		if err != nil {
			return out, mysqlStoreError(err)
		}
		out.Items = append(out.Items, r.User)
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

func (s *MySQLStore) ListCredentials(ctx context.Context, userID string, p Page) (PageResult[Credential], error) {
	limit, err := pageLimit(p)
	out := PageResult[Credential]{Items: []Credential{}}
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(opCtx, `SELECT `+mysqlCredentialColumns+` FROM mosdns_credentials WHERE user_id=? AND id>? ORDER BY id LIMIT ?`, userID, p.Cursor, limit+1)
	if err != nil {
		return out, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanMySQLCredential(rows)
		if err != nil {
			return out, mysqlStoreError(err)
		}
		out.Items = append(out.Items, r.Credential)
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

func (s *MySQLStore) CurrentQuota(ctx context.Context, userID string) (QuotaStatus, error) {
	if s.closed.Load() {
		return QuotaStatus{}, ErrUnavailable
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	r, err := mysqlUser(opCtx, s.db, userID, false)
	if err != nil {
		return QuotaStatus{}, mysqlStoreError(err)
	}
	start, end, err := periodBounds(s.clock.Now().UTC(), r.Period, r.Timezone)
	if err != nil {
		return QuotaStatus{}, err
	}
	if r.QuotaHighStart != 0 && start.Unix() < r.QuotaHighStart {
		return QuotaStatus{}, ErrUnavailable
	}
	used := r.QuotaUsed
	if r.QuotaStart != start.Unix() {
		used = 0
	}
	remaining := uint64(0)
	if used < r.Limit {
		remaining = r.Limit - used
	}
	return QuotaStatus{Period: r.Period, Timezone: r.Timezone, PeriodStart: start, PeriodEnd: end, Limit: r.Limit, Used: used, Remaining: remaining}, nil
}

func mysqlUsageCursor(p Page) (int64, error) {
	if p.Cursor == "" {
		return math.MinInt64, nil
	}
	v, err := strconv.ParseInt(p.Cursor, 10, 64)
	if err != nil {
		return 0, ErrInvalidInput
	}
	return v, nil
}

func (s *MySQLStore) Usage(ctx context.Context, userID string, from, to time.Time, p Page) (PageResult[UsagePoint], error) {
	kind, scopeID := "g", ""
	if userID != "" {
		kind, scopeID = "u", userID
	}
	return s.mysqlUsage(ctx, kind, scopeID, "", userID, from, to, p)
}

func (s *MySQLStore) mysqlUsage(ctx context.Context, kind, scopeID, credentialID, userID string, from, to time.Time, p Page) (PageResult[UsagePoint], error) {
	out := PageResult[UsagePoint]{Items: []UsagePoint{}}
	limit, err := pageLimit(p)
	if err != nil || to.Before(from) || to.Sub(from) > maxUsageRange {
		if err == nil {
			err = ErrInvalidInput
		}
		return out, err
	}
	cursor, err := mysqlUsageCursor(p)
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(opCtx, `SELECT minute_epoch, query_count FROM mosdns_usage_minutes
		WHERE scope_kind=? AND scope_id=? AND credential_id=? AND minute_epoch>=? AND minute_epoch<? AND minute_epoch>?
		ORDER BY minute_epoch LIMIT ?`, kind, scopeID, credentialID, from.Truncate(time.Minute).Unix(), to.Unix(), cursor, limit+1)
	if err != nil {
		return out, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var minute int64
		var count uint64
		if err := rows.Scan(&minute, &count); err != nil {
			return out, mysqlStoreError(err)
		}
		out.Items = append(out.Items, UsagePoint{Minute: time.Unix(minute, 0).UTC(), UserID: userID, CredentialID: credentialID, Count: count})
	}
	if err := rows.Err(); err != nil {
		return out, mysqlStoreError(err)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextCursor = strconv.FormatInt(out.Items[limit-1].Minute.Unix(), 10)
	}
	return out, nil
}

func parseDeviceCursor(value string) (int64, string, error) {
	if value == "" {
		return math.MinInt64, "", nil
	}
	minuteText, credentialID, ok := strings.Cut(value, ".")
	minute, err := strconv.ParseInt(minuteText, 10, 64)
	if !ok || err != nil {
		return 0, "", ErrInvalidInput
	}
	return minute, credentialID, nil
}

func (s *MySQLStore) CredentialUsage(ctx context.Context, userID, credentialID string, from, to time.Time, p Page) (PageResult[UsagePoint], error) {
	out := PageResult[UsagePoint]{Items: []UsagePoint{}}
	limit, err := pageLimit(p)
	if err != nil || userID == "" || to.Before(from) || to.Sub(from) > maxUsageRange {
		if err == nil {
			err = ErrInvalidInput
		}
		return out, err
	}
	cursorMinute, cursorCredential, err := parseDeviceCursor(p.Cursor)
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	query := `SELECT minute_epoch, credential_id, query_count FROM mosdns_usage_minutes
		WHERE scope_kind='d' AND scope_id=? AND minute_epoch>=? AND minute_epoch<?
		AND (minute_epoch>? OR (minute_epoch=? AND credential_id>?))`
	args := []any{userID, from.Truncate(time.Minute).Unix(), to.Unix(), cursorMinute, cursorMinute, cursorCredential}
	if credentialID != "" {
		query += ` AND credential_id=?`
		args = append(args, credentialID)
	}
	query += ` ORDER BY minute_epoch, credential_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(opCtx, query, args...)
	if err != nil {
		return out, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var minute int64
		var id string
		var count uint64
		if err := rows.Scan(&minute, &id, &count); err != nil {
			return out, mysqlStoreError(err)
		}
		out.Items = append(out.Items, UsagePoint{Minute: time.Unix(minute, 0).UTC(), UserID: userID, CredentialID: id, Count: count})
	}
	if err := rows.Err(); err != nil {
		return out, mysqlStoreError(err)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.NextCursor = strconv.FormatInt(last.Minute.Unix(), 10) + "." + last.CredentialID
	}
	return out, nil
}

func parseAuditCursor(value string) (int64, string, error) {
	if value == "" {
		return math.MinInt64, "", nil
	}
	timeText, id, ok := strings.Cut(value, ".")
	ns, err := strconv.ParseInt(timeText, 10, 64)
	if !ok || id == "" || err != nil {
		return 0, "", ErrInvalidInput
	}
	return ns, id, nil
}

func (s *MySQLStore) ListAudit(ctx context.Context, from, to time.Time, p Page) (PageResult[AuditRecord], error) {
	out := PageResult[AuditRecord]{Items: []AuditRecord{}}
	limit, err := pageLimit(p)
	if err != nil || to.Before(from) || to.Sub(from) > maxUsageRange {
		if err == nil {
			err = ErrInvalidInput
		}
		return out, err
	}
	cursorNS, cursorID, err := parseAuditCursor(p.Cursor)
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(opCtx, `SELECT id, actor_id, action, target_type, target_id, metadata_json, created_at_ns
		FROM mosdns_audit_logs WHERE created_at_ns>=? AND created_at_ns<?
		AND (created_at_ns>? OR (created_at_ns=? AND id>?)) ORDER BY created_at_ns, id LIMIT ?`,
		from.UnixNano(), to.UnixNano(), cursorNS, cursorNS, cursorID, limit+1)
	if err != nil {
		return out, mysqlStoreError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r AuditRecord
		var metadata []byte
		var created int64
		if err := rows.Scan(&r.ID, &r.ActorID, &r.Action, &r.TargetType, &r.TargetID, &metadata, &created); err != nil {
			return out, mysqlStoreError(err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &r.Metadata); err != nil {
				return out, mysqlStoreError(err)
			}
		}
		r.CreatedAt = time.Unix(0, created).UTC()
		out.Items = append(out.Items, r)
	}
	if err := rows.Err(); err != nil {
		return out, mysqlStoreError(err)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.NextCursor = strconv.FormatInt(last.CreatedAt.UnixNano(), 10) + "." + last.ID
	}
	return out, nil
}

func (s *MySQLStore) Maintain(ctx context.Context) error {
	return s.withTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := s.clock.Now().UTC()
		for _, item := range []struct {
			query string
			args  []any
		}{
			{`DELETE FROM mosdns_sessions WHERE revoked_at_ns IS NOT NULL OR expires_at_ns<=?`, []any{now.UnixNano()}},
			{`DELETE FROM mosdns_usage_minutes WHERE minute_epoch<?`, []any{now.Add(-usageRetention).Unix()}},
			{`DELETE FROM mosdns_audit_logs WHERE created_at_ns<?`, []any{now.Add(-auditRetention).UnixNano()}},
			{`DELETE FROM mosdns_credentials WHERE (revoked_at_ns IS NOT NULL AND revoked_at_ns<?) OR (expires_at_ns IS NOT NULL AND expires_at_ns<?)`, []any{now.Add(-credentialRetention).UnixNano(), now.Add(-credentialRetention).UnixNano()}},
		} {
			if _, err := tx.ExecContext(ctx, item.query, item.args...); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *MySQLStore) RunMaintenance(ctx context.Context, interval time.Duration) error {
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

var _ Service = (*MySQLStore)(nil)
var _ Maintainer = (*MySQLStore)(nil)
