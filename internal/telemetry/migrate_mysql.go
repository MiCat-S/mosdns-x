package telemetry

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// MySQLMigrationReport describes records copied from a bbolt telemetry database.
type MySQLMigrationReport struct {
	Minutes   uint64 `json:"minutes"`
	Upstreams uint64 `json:"upstreams"`
	Queries   uint64 `json:"queries"`
}

// InspectBoltForMySQL validates an offline bbolt telemetry database and counts
// the source records.
func InspectBoltForMySQL(ctx context.Context, source string) (MySQLMigrationReport, error) {
	var report MySQLMigrationReport
	err := withBoltTelemetrySource(ctx, source, func(tx *bolt.Tx) error {
		report = MySQLMigrationReport{
			Minutes:   uint64(tx.Bucket(bucketMinutes).Stats().KeyN),
			Upstreams: uint64(tx.Bucket(bucketUpstream).Stats().KeyN),
			Queries:   uint64(tx.Bucket(bucketQueries).Stats().KeyN),
		}
		return nil
	})
	return report, err
}

// MigrateBoltToMySQL copies an offline bbolt telemetry database into an empty
// MySQL telemetry schema. The copy is committed atomically.
func MigrateBoltToMySQL(ctx context.Context, source string, destination *Store) (MySQLMigrationReport, error) {
	var report MySQLMigrationReport
	if destination == nil || destination.mysql == nil || destination.closed.Load() {
		return report, errors.New("mysql telemetry destination unavailable")
	}
	err := withBoltTelemetrySource(ctx, source, func(sourceTx *bolt.Tx) error {
		opCtx, cancel := destination.mysqlContext(ctx)
		defer cancel()
		targetTx, err := destination.mysql.BeginTx(opCtx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return fmt.Errorf("mysql telemetry: %w", err)
		}
		rollback := func(err error) error {
			_ = targetTx.Rollback()
			return fmt.Errorf("mysql telemetry migration: %w", err)
		}
		if err := requireEmptyMySQLTelemetry(opCtx, targetTx); err != nil {
			return rollback(err)
		}
		if err := migrateTelemetryMinutes(opCtx, sourceTx, targetTx, &report); err != nil {
			return rollback(err)
		}
		if err := migrateTelemetryUpstreams(opCtx, sourceTx, targetTx, &report); err != nil {
			return rollback(err)
		}
		if err := migrateTelemetryQueries(opCtx, sourceTx, targetTx, &report); err != nil {
			return rollback(err)
		}
		updatedAt := int64(0)
		if raw := sourceTx.Bucket(bucketMeta).Get(keyUpdatedAt); len(raw) == 8 {
			updatedAt = int64(binary.BigEndian.Uint64(raw))
		}
		if _, err := targetTx.ExecContext(opCtx, `UPDATE mosdns_telemetry_meta SET updated_at_ns=? WHERE id=1`, updatedAt); err != nil {
			return rollback(err)
		}
		if err := targetTx.Commit(); err != nil {
			return fmt.Errorf("mysql telemetry migration: %w", err)
		}
		destination.updatedUnixNano.Store(updatedAt)
		return nil
	})
	return report, err
}

func withBoltTelemetrySource(ctx context.Context, source string, fn func(*bolt.Tx) error) error {
	if source == "" {
		return errors.New("empty telemetry migration source")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := bolt.Open(source, 0o600, &bolt.Options{ReadOnly: true, Timeout: 100 * time.Millisecond})
	if err != nil {
		return fmt.Errorf("open telemetry migration source: %w", err)
	}
	defer db.Close()
	return db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMinutes, bucketUpstream, bucketQueries, bucketUserQ, bucketExpiry, bucketMeta} {
			if tx.Bucket(name) == nil {
				return fmt.Errorf("invalid telemetry migration source: missing bucket %q", name)
			}
		}
		return fn(tx)
	})
}

func requireEmptyMySQLTelemetry(ctx context.Context, tx *sql.Tx) error {
	var updatedAt int64
	var minutes, rcodes, latency, upstreams, queries uint64
	err := tx.QueryRowContext(ctx, `SELECT updated_at_ns,
		(SELECT COUNT(*) FROM mosdns_telemetry_minutes),
		(SELECT COUNT(*) FROM mosdns_telemetry_rcodes),
		(SELECT COUNT(*) FROM mosdns_telemetry_latency),
		(SELECT COUNT(*) FROM mosdns_telemetry_upstreams),
		(SELECT COUNT(*) FROM mosdns_query_logs)
		FROM mosdns_telemetry_meta WHERE id=1 FOR UPDATE`).Scan(&updatedAt, &minutes, &rcodes, &latency, &upstreams, &queries)
	if err != nil {
		return err
	}
	if minutes+rcodes+latency+upstreams+queries != 0 {
		return errors.New("mysql telemetry destination is not empty")
	}
	return nil
}

func telemetryKey(key []byte, wantSuffix bool) (scope, userID string, minute int64, suffix string, err error) {
	parts := strings.Split(string(key), "\x00")
	if len(parts) != 4 || parts[0] != "g" && parts[0] != "u" || !wantSuffix && parts[3] != "" || wantSuffix && parts[3] == "" {
		return "", "", 0, "", fmt.Errorf("invalid telemetry key %q", key)
	}
	minute, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", 0, "", fmt.Errorf("invalid telemetry key %q: %w", key, err)
	}
	return parts[0], parts[1], minute, parts[3], nil
}

func migrateTelemetryMinutes(ctx context.Context, source *bolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	return source.Bucket(bucketMinutes).ForEach(func(key, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		scope, userID, minute, _, err := telemetryKey(key, false)
		if err != nil {
			return err
		}
		var record minuteAggregate
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if _, err := target.ExecContext(ctx, `INSERT INTO mosdns_telemetry_minutes
			(scope_kind, scope_id, minute_epoch, completed, failed, cache_hits, latency_us)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, scope, userID, minute, record.Completed, record.Failed, record.CacheHits, record.LatencyUS); err != nil {
			return err
		}
		for rcode, count := range record.Rcodes {
			if _, err := target.ExecContext(ctx, `INSERT INTO mosdns_telemetry_rcodes
				(scope_kind, scope_id, minute_epoch, rcode, query_count) VALUES (?, ?, ?, ?, ?)`, scope, userID, minute, rcode, count); err != nil {
				return err
			}
		}
		for bucket, count := range record.Histogram {
			if count == 0 {
				continue
			}
			if _, err := target.ExecContext(ctx, `INSERT INTO mosdns_telemetry_latency
				(scope_kind, scope_id, minute_epoch, bucket, query_count) VALUES (?, ?, ?, ?, ?)`, scope, userID, minute, bucket, count); err != nil {
				return err
			}
		}
		report.Minutes++
		return nil
	})
}

func migrateTelemetryUpstreams(ctx context.Context, source *bolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	return source.Bucket(bucketUpstream).ForEach(func(key, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		scope, userID, minute, upstream, err := telemetryKey(key, true)
		if err != nil {
			return err
		}
		var record upstreamAggregate
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		_, err = target.ExecContext(ctx, `INSERT INTO mosdns_telemetry_upstreams
			(scope_kind, scope_id, minute_epoch, upstream_id, attempts, failures, latency_us)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, scope, userID, minute, upstream, record.Attempts, record.Failures, record.LatencyUS)
		if err == nil {
			report.Upstreams++
		}
		return err
	})
}

func migrateTelemetryQueries(ctx context.Context, source *bolt.Tx, target *sql.Tx, report *MySQLMigrationReport) error {
	return source.Bucket(bucketQueries).ForEach(func(id, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record QueryRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		answerJSON, err := json.Marshal(record.AnswerIPs)
		if err != nil {
			return err
		}
		ednsJSON, err := json.Marshal(record.EDNS)
		if err != nil {
			return err
		}
		upstreamRequestEDNSJSON, err := marshalOptionalEDNSSnapshot(record.UpstreamRequestEDNS)
		if err != nil {
			return err
		}
		upstreamResponseEDNSJSON, err := marshalOptionalEDNSSnapshot(record.UpstreamResponseEDNS)
		if err != nil {
			return err
		}
		responseEDNSJSON, err := marshalOptionalEDNSSnapshot(record.ResponseEDNS)
		if err != nil {
			return err
		}
		_, err = target.ExecContext(ctx, `INSERT INTO mosdns_query_logs
			(id, time_ns, user_id, credential_id, client_ip, name, qtype, rcode, duration_ms, cache_hit, protocol, answer_ips_json, edns_json,
			 edns_trace_version, upstream_stage_status, upstream_request_edns_json, upstream_response_edns_json, response_edns_json,
			 response_source, response_source_id, upstream_id, matched_rule_id, matched_public_list_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, string(id), record.Time.UnixNano(), record.UserID,
			record.CredentialID, record.ClientIP, record.Name, record.QType, record.Rcode, record.DurationMS,
			record.CacheHit, record.Protocol, answerJSON, ednsJSON, record.EDNSTraceVersion,
			normalizedUpstreamStageStatus(record.UpstreamStageStatus), upstreamRequestEDNSJSON, upstreamResponseEDNSJSON, responseEDNSJSON,
			record.ResponseSource, record.ResponseSourceID,
			record.UpstreamID, record.MatchedRuleID, record.MatchedPublicListID)
		if err == nil {
			report.Queries++
		}
		return err
	})
}
