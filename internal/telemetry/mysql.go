package telemetry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/miekg/dns"
)

const mysqlTelemetrySchemaVersion = 1

type MySQLOptions struct {
	DSN              string
	MaxOpenConns     int
	MaxIdleConns     int
	ConnMaxLifetime  time.Duration
	OperationTimeout time.Duration
	QueueSize        int
	BatchSize        int
	FlushInterval    time.Duration
	QueryLogEnabled  bool
	Now              func() time.Time
}

var mysqlTelemetryMigrations = []string{
	`CREATE TABLE IF NOT EXISTS mosdns_schema_migrations (
		component VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		version INT UNSIGNED NOT NULL
	) ENGINE=InnoDB`,
	`CREATE TABLE IF NOT EXISTS mosdns_telemetry_meta (
		id TINYINT UNSIGNED PRIMARY KEY,
		updated_at_ns BIGINT NOT NULL DEFAULT 0
	) ENGINE=InnoDB`,
	`INSERT INTO mosdns_telemetry_meta (id, updated_at_ns) VALUES (1, 0)
		ON DUPLICATE KEY UPDATE id=VALUES(id)`,
	`CREATE TABLE IF NOT EXISTS mosdns_telemetry_minutes (
		scope_kind CHAR(1) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		minute_epoch BIGINT NOT NULL,
		completed BIGINT UNSIGNED NOT NULL,
		failed BIGINT UNSIGNED NOT NULL,
		cache_hits BIGINT UNSIGNED NOT NULL,
		latency_us BIGINT UNSIGNED NOT NULL,
		PRIMARY KEY (scope_kind, scope_id, minute_epoch),
		KEY ix_mosdns_telemetry_minutes_retention (minute_epoch)
	) ENGINE=InnoDB`,
	`CREATE TABLE IF NOT EXISTS mosdns_telemetry_rcodes (
		scope_kind CHAR(1) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		minute_epoch BIGINT NOT NULL,
		rcode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		query_count BIGINT UNSIGNED NOT NULL,
		PRIMARY KEY (scope_kind, scope_id, minute_epoch, rcode),
		KEY ix_mosdns_telemetry_rcodes_retention (minute_epoch)
	) ENGINE=InnoDB`,
	`CREATE TABLE IF NOT EXISTS mosdns_telemetry_latency (
		scope_kind CHAR(1) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		minute_epoch BIGINT NOT NULL,
		bucket TINYINT UNSIGNED NOT NULL,
		query_count BIGINT UNSIGNED NOT NULL,
		PRIMARY KEY (scope_kind, scope_id, minute_epoch, bucket),
		KEY ix_mosdns_telemetry_latency_retention (minute_epoch)
	) ENGINE=InnoDB`,
	`CREATE TABLE IF NOT EXISTS mosdns_telemetry_upstreams (
		scope_kind CHAR(1) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		minute_epoch BIGINT NOT NULL,
		upstream_id VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
		attempts BIGINT UNSIGNED NOT NULL,
		failures BIGINT UNSIGNED NOT NULL,
		latency_us BIGINT UNSIGNED NOT NULL,
		PRIMARY KEY (scope_kind, scope_id, minute_epoch, upstream_id),
		KEY ix_mosdns_telemetry_upstreams_retention (minute_epoch)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS mosdns_query_logs (
		id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
		time_ns BIGINT NOT NULL,
		user_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		credential_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		client_ip VARCHAR(45) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		name VARCHAR(255) NOT NULL,
		qtype VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		rcode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		duration_ms DOUBLE NOT NULL,
		cache_hit BOOLEAN NOT NULL,
		protocol VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		answer_ips_json LONGTEXT NOT NULL,
		edns_json LONGTEXT NOT NULL,
		KEY ix_mosdns_query_logs_time (time_ns, id),
		KEY ix_mosdns_query_logs_user (user_id, time_ns, id),
		KEY ix_mosdns_query_logs_credential (credential_id, time_ns)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,
}

func OpenMySQL(opts MySQLOptions) (*Store, error) {
	if strings.TrimSpace(opts.DSN) == "" {
		return nil, errors.New("empty mysql telemetry dsn")
	}
	parsed, err := mysqlDriver.ParseDSN(opts.DSN)
	if err != nil || parsed.DBName == "" {
		return nil, errors.New("invalid mysql telemetry dsn")
	}
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = 16
	}
	if opts.MaxIdleConns < 0 {
		return nil, errors.New("negative mysql telemetry max idle connections")
	}
	if opts.MaxIdleConns == 0 {
		opts.MaxIdleConns = 4
	}
	if opts.MaxIdleConns > opts.MaxOpenConns {
		return nil, errors.New("mysql telemetry max idle connections exceed max open connections")
	}
	if opts.ConnMaxLifetime <= 0 {
		opts.ConnMaxLifetime = 30 * time.Minute
	}
	if opts.OperationTimeout <= 0 {
		opts.OperationTimeout = 2 * time.Second
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 4096
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 128
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	db, err := sql.Open("mysql", parsed.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(opts.MaxOpenConns)
	db.SetMaxIdleConns(opts.MaxIdleConns)
	db.SetConnMaxLifetime(opts.ConnMaxLifetime)
	startupTimeout := max(opts.OperationTimeout, 15*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql telemetry: %w", err)
	}
	if err := initializeMySQLTelemetry(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{mysql: db, mysqlTimeout: opts.OperationTimeout, queue: make(chan event, opts.QueueSize), stop: make(chan struct{}), done: make(chan struct{}), batchSize: opts.BatchSize, flushInterval: opts.FlushInterval, queryLogEnabled: opts.QueryLogEnabled, now: opts.Now, droppedByWindow: make(map[string]uint64)}
	var updated int64
	if err := db.QueryRowContext(ctx, `SELECT updated_at_ns FROM mosdns_telemetry_meta WHERE id=1`).Scan(&updated); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql telemetry: %w", err)
	}
	s.updatedUnixNano.Store(updated)
	go s.run()
	return s, nil
}

func initializeMySQLTelemetry(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("mysql telemetry: %w", err)
	}
	defer conn.Close()
	var locked int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('mosdns_x_telemetry_schema', 10)`).Scan(&locked); err != nil || locked != 1 {
		if err == nil {
			err = errors.New("mysql telemetry migration lock unavailable")
		}
		return fmt.Errorf("mysql telemetry: %w", err)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(releaseCtx, `SELECT RELEASE_LOCK('mosdns_x_telemetry_schema')`)
	}()
	for _, statement := range mysqlTelemetryMigrations {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("mysql telemetry: %w", err)
		}
	}
	var version int
	err = conn.QueryRowContext(ctx, `SELECT version FROM mosdns_schema_migrations WHERE component='telemetry'`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = conn.ExecContext(ctx, `INSERT INTO mosdns_schema_migrations (component, version) VALUES ('telemetry', ?)`, mysqlTelemetrySchemaVersion)
		if err != nil {
			return fmt.Errorf("mysql telemetry: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("mysql telemetry: %w", err)
	case version != mysqlTelemetrySchemaVersion:
		return fmt.Errorf("unsupported mysql telemetry schema version %d", version)
	default:
		return nil
	}
}

func (s *Store) mysqlContext(parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= s.mysqlTimeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, s.mysqlTimeout)
}

type mysqlMinuteKey struct {
	Scope  string
	UserID string
	Minute int64
}

type mysqlRcodeKey struct {
	mysqlMinuteKey
	Rcode string
}

type mysqlLatencyKey struct {
	mysqlMinuteKey
	Bucket int
}

type mysqlUpstreamKey struct {
	mysqlMinuteKey
	Upstream string
}

func (s *Store) writeMySQLBatch(events []event) error {
	minutes := make(map[mysqlMinuteKey]minuteAggregate)
	rcodes := make(map[mysqlRcodeKey]uint64)
	latencies := make(map[mysqlLatencyKey]uint64)
	upstreams := make(map[mysqlUpstreamKey]upstreamAggregate)
	queries := make([]QueryRecord, 0, len(events))
	for _, e := range events {
		if e.result != nil {
			r := *e.result
			failed := r.ExecError || r.Rcode == dns.RcodeServerFailure || r.Rcode == dns.RcodeRefused
			rcode := dns.RcodeToString[r.Rcode]
			if rcode == "" {
				rcode = strconv.Itoa(r.Rcode)
			}
			minute := e.time.Truncate(time.Minute).Unix()
			for _, scope := range [][2]string{{"g", ""}, {"u", r.Principal.UserID}} {
				key := mysqlMinuteKey{Scope: scope[0], UserID: scope[1], Minute: minute}
				a := minutes[key]
				a.Completed++
				if failed {
					a.Failed++
				}
				if r.CacheHit {
					a.CacheHits++
				}
				a.LatencyUS += uint64(max(0, r.Duration.Microseconds()))
				minutes[key] = a
				rcodes[mysqlRcodeKey{mysqlMinuteKey: key, Rcode: rcode}]++
				latencies[mysqlLatencyKey{mysqlMinuteKey: key, Bucket: latencyBucket(r.Duration)}]++
			}
			if s.queryLogEnabled {
				idSuffix, err := randomTelemetryID()
				if err != nil {
					return err
				}
				qtype := dns.TypeToString[r.QuestionType]
				if qtype == "" {
					qtype = strconv.Itoa(int(r.QuestionType))
				}
				clientIP := ""
				if r.ClientAddr.IsValid() {
					clientIP = r.ClientAddr.String()
				}
				answerIPs := append([]string(nil), r.AnswerIPs...)
				if answerIPs == nil {
					answerIPs = []string{}
				}
				edns := r.EDNS
				edns.OptionCodes = append([]uint16(nil), r.EDNS.OptionCodes...)
				if edns.OptionCodes == nil {
					edns.OptionCodes = []uint16{}
				}
				if r.EDNS.ECS != nil {
					ecs := *r.EDNS.ECS
					edns.ECS = &ecs
				}
				queries = append(queries, QueryRecord{ID: fmt.Sprintf("%020d.%s", e.time.UnixNano(), idSuffix), Time: e.time, UserID: r.Principal.UserID, CredentialID: r.Principal.CredentialID, ClientIP: clientIP, Name: r.QuestionName, QType: qtype, Rcode: rcode, DurationMS: float64(r.Duration.Microseconds()) / 1000, CacheHit: r.CacheHit, Protocol: r.Protocol, AnswerIPs: answerIPs, EDNS: edns})
			}
		}
		if e.attempt != nil {
			a := *e.attempt
			minute := e.time.Truncate(time.Minute).Unix()
			for _, scope := range [][2]string{{"g", ""}, {"u", a.Principal.UserID}} {
				key := mysqlUpstreamKey{mysqlMinuteKey: mysqlMinuteKey{Scope: scope[0], UserID: scope[1], Minute: minute}, Upstream: a.UpstreamID}
				agg := upstreams[key]
				agg.Attempts++
				if a.Failed {
					agg.Failures++
				}
				agg.LatencyUS += uint64(max(0, a.Duration.Microseconds()))
				upstreams[key] = agg
			}
		}
	}
	ctx, cancel := s.mysqlContext(context.Background())
	defer cancel()
	tx, err := s.mysql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("mysql telemetry: %w", err)
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return fmt.Errorf("mysql telemetry: %w", err)
	}
	for key, a := range minutes {
		_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_telemetry_minutes
			(scope_kind, scope_id, minute_epoch, completed, failed, cache_hits, latency_us) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE completed=completed+VALUES(completed), failed=failed+VALUES(failed), cache_hits=cache_hits+VALUES(cache_hits), latency_us=latency_us+VALUES(latency_us)`,
			key.Scope, key.UserID, key.Minute, a.Completed, a.Failed, a.CacheHits, a.LatencyUS)
		if err != nil {
			return rollback(err)
		}
	}
	for key, count := range rcodes {
		_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_telemetry_rcodes
			(scope_kind, scope_id, minute_epoch, rcode, query_count) VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE query_count=query_count+VALUES(query_count)`, key.Scope, key.UserID, key.Minute, key.Rcode, count)
		if err != nil {
			return rollback(err)
		}
	}
	for key, count := range latencies {
		_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_telemetry_latency
			(scope_kind, scope_id, minute_epoch, bucket, query_count) VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE query_count=query_count+VALUES(query_count)`, key.Scope, key.UserID, key.Minute, key.Bucket, count)
		if err != nil {
			return rollback(err)
		}
	}
	for key, a := range upstreams {
		_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_telemetry_upstreams
			(scope_kind, scope_id, minute_epoch, upstream_id, attempts, failures, latency_us) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE attempts=attempts+VALUES(attempts), failures=failures+VALUES(failures), latency_us=latency_us+VALUES(latency_us)`,
			key.Scope, key.UserID, key.Minute, key.Upstream, a.Attempts, a.Failures, a.LatencyUS)
		if err != nil {
			return rollback(err)
		}
	}
	for _, q := range queries {
		answerJSON, err := json.Marshal(q.AnswerIPs)
		if err != nil {
			return rollback(err)
		}
		ednsJSON, err := json.Marshal(q.EDNS)
		if err != nil {
			return rollback(err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mosdns_query_logs
			(id, time_ns, user_id, credential_id, client_ip, name, qtype, rcode, duration_ms, cache_hit, protocol, answer_ips_json, edns_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, q.ID, q.Time.UnixNano(), q.UserID, q.CredentialID, q.ClientIP, q.Name, q.QType, q.Rcode, q.DurationMS, q.CacheHit, q.Protocol, answerJSON, ednsJSON)
		if err != nil {
			return rollback(err)
		}
	}
	now := s.now().UTC()
	minute := now.Truncate(time.Minute).Unix()
	lastPrune := s.mysqlPrunedAt.Load()
	shouldPrune := lastPrune < minute
	if shouldPrune {
		for _, table := range []string{"mosdns_telemetry_minutes", "mosdns_telemetry_rcodes", "mosdns_telemetry_latency", "mosdns_telemetry_upstreams"} {
			if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE minute_epoch<?`, now.Add(-aggregateRetention).Unix()); err != nil {
				return rollback(err)
			}
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM mosdns_query_logs WHERE time_ns<?`, now.Add(-queryRetention).UnixNano()); err != nil {
			return rollback(err)
		}
		var count uint64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mosdns_query_logs`).Scan(&count); err != nil {
			return rollback(err)
		}
		if count > maxQueryRecords {
			if _, err = tx.ExecContext(ctx, `DELETE FROM mosdns_query_logs ORDER BY time_ns, id LIMIT ?`, count-maxQueryRecords); err != nil {
				return rollback(err)
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mosdns_telemetry_meta SET updated_at_ns=? WHERE id=1`, now.UnixNano()); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("mysql telemetry: %w", err)
	}
	if shouldPrune {
		s.mysqlPrunedAt.Store(minute)
	}
	s.updatedUnixNano.Store(now.UnixNano())
	return nil
}

func randomTelemetryID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Store) mysqlSnapshot(ctx context.Context, userID string, from, to time.Time) (StatsSnapshot, error) {
	if err := validateRange(from, to); err != nil {
		return StatsSnapshot{}, err
	}
	requestedFrom := from
	retainedFrom := s.now().UTC().Add(-aggregateRetention)
	if from.Before(retainedFrom) {
		from = retainedFrom
	}
	snapshot := StatsSnapshot{From: requestedFrom, To: to, RcodeCounts: make(map[string]uint64), Series: []SeriesPoint{}, Upstreams: []UpstreamStats{}, Dropped: s.droppedFor(userID, from, to), QueryLogEnabled: s.queryLogEnabled}
	if !from.Before(to) {
		return snapshot, nil
	}
	if updated := s.updatedUnixNano.Load(); updated != 0 {
		snapshot.UpdatedAt = time.Unix(0, updated).UTC()
	}
	kind := "g"
	if userID != "" {
		kind = "u"
	}
	opCtx, cancel := s.mysqlContext(ctx)
	defer cancel()
	rows, err := s.mysql.QueryContext(opCtx, `SELECT minute_epoch, completed, failed, cache_hits, latency_us
		FROM mosdns_telemetry_minutes WHERE scope_kind=? AND scope_id=? AND minute_epoch>=? AND minute_epoch<? ORDER BY minute_epoch`,
		kind, userID, from.Truncate(time.Minute).Unix(), to.Unix())
	if err != nil {
		return snapshot, fmt.Errorf("mysql telemetry: %w", err)
	}
	var totalUS uint64
	for rows.Next() {
		var minute int64
		var p SeriesPoint
		var latency uint64
		if err := rows.Scan(&minute, &p.Completed, &p.Failed, &p.CacheHits, &latency); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("mysql telemetry: %w", err)
		}
		p.Time = time.Unix(minute, 0).UTC()
		if p.Completed > 0 {
			p.AvgLatencyMS = float64(latency) / 1000 / float64(p.Completed)
		}
		snapshot.Series = append(snapshot.Series, p)
		snapshot.Completed += p.Completed
		snapshot.Failed += p.Failed
		snapshot.CacheHits += p.CacheHits
		totalUS += latency
	}
	if err := rows.Close(); err != nil {
		return snapshot, fmt.Errorf("mysql telemetry: %w", err)
	}
	if snapshot.Completed > 0 {
		snapshot.AvgLatencyMS = float64(totalUS) / 1000 / float64(snapshot.Completed)
	}
	rcodeRows, err := s.mysql.QueryContext(opCtx, `SELECT rcode, SUM(query_count) FROM mosdns_telemetry_rcodes
		WHERE scope_kind=? AND scope_id=? AND minute_epoch>=? AND minute_epoch<? GROUP BY rcode`, kind, userID, from.Truncate(time.Minute).Unix(), to.Unix())
	if err != nil {
		return snapshot, fmt.Errorf("mysql telemetry: %w", err)
	}
	for rcodeRows.Next() {
		var code string
		var count uint64
		if err := rcodeRows.Scan(&code, &count); err != nil {
			rcodeRows.Close()
			return snapshot, fmt.Errorf("mysql telemetry: %w", err)
		}
		snapshot.RcodeCounts[code] = count
	}
	if err := rcodeRows.Close(); err != nil {
		return snapshot, fmt.Errorf("mysql telemetry: %w", err)
	}
	latencyRows, err := s.mysql.QueryContext(opCtx, `SELECT bucket, SUM(query_count) FROM mosdns_telemetry_latency
		WHERE scope_kind=? AND scope_id=? AND minute_epoch>=? AND minute_epoch<? GROUP BY bucket ORDER BY bucket`, kind, userID, from.Truncate(time.Minute).Unix(), to.Unix())
	if err != nil {
		return snapshot, fmt.Errorf("mysql telemetry: %w", err)
	}
	rank := (snapshot.Completed*95 + 99) / 100
	var seen uint64
	for latencyRows.Next() {
		var bucket int
		var count uint64
		if err := latencyRows.Scan(&bucket, &count); err != nil {
			latencyRows.Close()
			return snapshot, fmt.Errorf("mysql telemetry: %w", err)
		}
		seen += count
		if rank > 0 && snapshot.P95LatencyMS == 0 && seen >= rank {
			snapshot.P95LatencyMS = float64((uint64(1) << (bucket + 1)) - 1)
		}
	}
	if err := latencyRows.Close(); err != nil {
		return snapshot, fmt.Errorf("mysql telemetry: %w", err)
	}
	upstreamRows, err := s.mysql.QueryContext(opCtx, `SELECT upstream_id, SUM(attempts), SUM(failures), SUM(latency_us)
		FROM mosdns_telemetry_upstreams WHERE scope_kind=? AND scope_id=? AND minute_epoch>=? AND minute_epoch<? GROUP BY upstream_id`,
		kind, userID, from.Truncate(time.Minute).Unix(), to.Unix())
	if err != nil {
		return snapshot, fmt.Errorf("mysql telemetry: %w", err)
	}
	for upstreamRows.Next() {
		var item UpstreamStats
		var latency uint64
		if err := upstreamRows.Scan(&item.ID, &item.Attempts, &item.Failures, &latency); err != nil {
			upstreamRows.Close()
			return snapshot, fmt.Errorf("mysql telemetry: %w", err)
		}
		if item.Attempts > 0 {
			item.AvgLatencyMS = float64(latency) / 1000 / float64(item.Attempts)
		}
		snapshot.Upstreams = append(snapshot.Upstreams, item)
	}
	if err := upstreamRows.Close(); err != nil {
		return snapshot, fmt.Errorf("mysql telemetry: %w", err)
	}
	sort.Slice(snapshot.Upstreams, func(i, j int) bool { return snapshot.Upstreams[i].ID < snapshot.Upstreams[j].ID })
	return snapshot, nil
}

func mysqlQueryCursor(value string) (int64, string, error) {
	if value == "" {
		return math.MinInt64, "", nil
	}
	if len(value) < 22 {
		return 0, "", errors.New("invalid query cursor")
	}
	ns, err := strconv.ParseInt(value[:20], 10, 64)
	if err != nil {
		return 0, "", errors.New("invalid query cursor")
	}
	return ns, value, nil
}

func (s *Store) mysqlQueries(ctx context.Context, userID string, from, to time.Time, page Page) (QueryPage, error) {
	result := QueryPage{Items: []QueryRecord{}}
	if !s.queryLogEnabled {
		return result, nil
	}
	if err := validateRange(from, to); err != nil {
		return result, err
	}
	retainedFrom := s.now().UTC().Add(-queryRetention)
	if from.Before(retainedFrom) {
		from = retainedFrom
	}
	if !from.Before(to) {
		return result, nil
	}
	if page.Limit <= 0 {
		page.Limit = 100
	}
	if page.Limit > maxPageLimit {
		return result, errors.New("limit exceeds 1000")
	}
	cursorTime, cursorID, err := mysqlQueryCursor(page.Cursor)
	if err != nil {
		return result, err
	}
	opCtx, cancel := s.mysqlContext(ctx)
	defer cancel()
	query := `SELECT id, time_ns, user_id, credential_id, client_ip, name, qtype, rcode, duration_ms, cache_hit, protocol, answer_ips_json, edns_json
		FROM mosdns_query_logs WHERE time_ns>=? AND time_ns<? AND (time_ns>? OR (time_ns=? AND id>?))`
	args := []any{from.UnixNano(), to.UnixNano(), cursorTime, cursorTime, cursorID}
	if userID != "" {
		query += ` AND user_id=?`
		args = append(args, userID)
	}
	query += ` ORDER BY time_ns, id LIMIT ?`
	args = append(args, page.Limit+1)
	rows, err := s.mysql.QueryContext(opCtx, query, args...)
	if err != nil {
		return result, fmt.Errorf("mysql telemetry: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r QueryRecord
		var ns int64
		var answerJSON, ednsJSON []byte
		if err := rows.Scan(&r.ID, &ns, &r.UserID, &r.CredentialID, &r.ClientIP, &r.Name, &r.QType, &r.Rcode, &r.DurationMS, &r.CacheHit, &r.Protocol, &answerJSON, &ednsJSON); err != nil {
			return result, fmt.Errorf("mysql telemetry: %w", err)
		}
		r.Time = time.Unix(0, ns).UTC()
		if err := json.Unmarshal(answerJSON, &r.AnswerIPs); err != nil {
			return result, fmt.Errorf("mysql telemetry: %w", err)
		}
		if err := json.Unmarshal(ednsJSON, &r.EDNS); err != nil {
			return result, fmt.Errorf("mysql telemetry: %w", err)
		}
		if r.AnswerIPs == nil {
			r.AnswerIPs = []string{}
		}
		if r.EDNS.OptionCodes == nil {
			r.EDNS.OptionCodes = []uint16{}
		}
		result.Items = append(result.Items, r)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("mysql telemetry: %w", err)
	}
	if len(result.Items) > page.Limit {
		result.Items = result.Items[:page.Limit]
		result.NextCursor = result.Items[page.Limit-1].ID
	}
	return result, nil
}

var _ Service = (*Store)(nil)
