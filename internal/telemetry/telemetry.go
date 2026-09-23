package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

const (
	// Minute aggregates are retained for seven days. Query records use the
	// shorter limits below because they contain per-request details.
	aggregateRetention    = 7 * 24 * time.Hour
	queryRetention        = 24 * time.Hour
	maxQueryRecords       = 100000
	maxRange              = 31 * 24 * time.Hour
	maxPageLimit          = 1000
	minAggregateRetention = 24 * time.Hour
	maxAggregateRetention = 31 * 24 * time.Hour
	minQueryRetention     = time.Hour
	maxQueryRetention     = 720 * time.Hour
	minQueryRecords       = 1000
	maxQueryRecordLimit   = 5000000
)

var (
	bucketMinutes  = []byte("minutes")
	bucketUpstream = []byte("upstreams")
	bucketQueries  = []byte("queries")
	bucketUserQ    = []byte("user_queries")
	bucketExpiry   = []byte("expiry")
	bucketMeta     = []byte("meta")
	keyUpdatedAt   = []byte("updated_at")
	keyQueryCount  = []byte("query_count")
)

type Options struct {
	Path               string
	QueueSize          int
	BatchSize          int
	FlushInterval      time.Duration
	QueryLogEnabled    bool
	AggregateRetention time.Duration
	QueryRetention     time.Duration
	MaxQueryRecords    int
	Now                func() time.Time
}

type Settings struct {
	QueryLogEnabled    bool          `json:"query_log_enabled"`
	AggregateRetention time.Duration `json:"aggregate_retention"`
	QueryRetention     time.Duration `json:"query_retention"`
	MaxQueryRecords    int           `json:"max_query_records"`
}

type SeriesPoint struct {
	Time         time.Time `json:"time"`
	Completed    uint64    `json:"completed"`
	Failed       uint64    `json:"failed"`
	CacheHits    uint64    `json:"cache_hits"`
	AvgLatencyMS float64   `json:"avg_latency_ms"`
}

type UpstreamStats struct {
	ID           string  `json:"id"`
	Attempts     uint64  `json:"attempts"`
	Failures     uint64  `json:"failures"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

type StatsSnapshot struct {
	From            time.Time         `json:"from"`
	To              time.Time         `json:"to"`
	Completed       uint64            `json:"completed"`
	Failed          uint64            `json:"failed"`
	CacheHits       uint64            `json:"cache_hits"`
	AvgLatencyMS    float64           `json:"avg_latency_ms"`
	P95LatencyMS    float64           `json:"p95_latency_ms"`
	RcodeCounts     map[string]uint64 `json:"rcode_counts"`
	Series          []SeriesPoint     `json:"series"`
	Upstreams       []UpstreamStats   `json:"upstreams"`
	Dropped         uint64            `json:"dropped"`
	UpdatedAt       time.Time         `json:"updated_at"`
	QueryLogEnabled bool              `json:"query_log_enabled"`
}

type QueryRecord struct {
	ID                   string                 `json:"id"`
	Time                 time.Time              `json:"time"`
	UserID               string                 `json:"user_id"`
	CredentialID         string                 `json:"credential_id"`
	ClientIP             string                 `json:"client_ip"`
	Name                 string                 `json:"name"`
	QType                string                 `json:"qtype"`
	Rcode                string                 `json:"rcode"`
	DurationMS           float64                `json:"duration_ms"`
	CacheHit             bool                   `json:"cache_hit"`
	Protocol             string                 `json:"protocol"`
	AnswerIPs            []string               `json:"answer_ips"`
	EDNS                 dns_handler.EDNSInfo   `json:"edns"`
	EDNSTraceVersion     uint8                  `json:"edns_trace_version"`
	UpstreamStageStatus  string                 `json:"upstream_stage_status"`
	UpstreamRequestEDNS  *dnsutils.EDNSSnapshot `json:"upstream_request_edns"`
	UpstreamResponseEDNS *dnsutils.EDNSSnapshot `json:"upstream_response_edns"`
	ResponseEDNS         *dnsutils.EDNSSnapshot `json:"response_edns"`
	ResponseSource       string                 `json:"response_source"`
	ResponseSourceID     string                 `json:"response_source_id"`
	UpstreamID           string                 `json:"upstream_id"`
	MatchedRuleID        string                 `json:"matched_rule_id"`
	MatchedPublicListID  string                 `json:"matched_public_list_id"`
}

type Page struct {
	Limit  int
	Cursor string
}

type QueryFilter struct {
	Name           string
	QType          string
	Rcode          string
	CredentialID   string
	Protocol       string
	Address        string
	ResponseSource string
	UpstreamID     string
	CacheHit       *bool
}

type QueryPage struct {
	Items      []QueryRecord `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type minuteAggregate struct {
	Completed uint64            `json:"completed"`
	Failed    uint64            `json:"failed"`
	CacheHits uint64            `json:"cache_hits"`
	LatencyUS uint64            `json:"latency_us"`
	Histogram [32]uint64        `json:"histogram"`
	Rcodes    map[string]uint64 `json:"rcodes"`
}

type upstreamAggregate struct {
	Attempts  uint64 `json:"attempts"`
	Failures  uint64 `json:"failures"`
	LatencyUS uint64 `json:"latency_us"`
}

type event struct {
	result   *dns_handler.Result
	attempt  *query_context.UpstreamAttempt
	time     time.Time
	flushAck chan error
}

type Store struct {
	db                  *bolt.DB
	mysql               *sql.DB
	mysqlTimeout        time.Duration
	mysqlPrunedAt       atomic.Int64
	userPrunedAt        atomic.Int64
	logPolicy           atomic.Pointer[UserLogPolicy]
	queue               chan event
	stop                chan struct{}
	done                chan struct{}
	batchSize           int
	flushInterval       time.Duration
	queryLogEnabled     bool
	aggregateRetention  time.Duration
	queryRetention      time.Duration
	maxQueryRecords     int
	settingsMu          sync.RWMutex
	settingsUpdateMu    sync.Mutex
	boltExpiryRetention time.Duration
	now                 func() time.Time
	closed              atomic.Bool
	dropped             atomic.Uint64
	droppedMu           sync.RWMutex
	droppedByWindow     map[string]uint64
	droppedPrunedAt     atomic.Int64
	updatedUnixNano     atomic.Int64
	closeOnce           sync.Once
	closeErr            error
	runErr              error
	enqueueMu           sync.RWMutex
}

func Open(opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("empty telemetry database path")
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
	aggregateRetentionValue, queryRetentionValue, maxQueryRecordsValue, err := normalizeRetention(opts.AggregateRetention, opts.QueryRetention, opts.MaxQueryRecords)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(opts.Path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(opts.Path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMinutes, bucketUpstream, bucketQueries, bucketUserQ, bucketExpiry, bucketMeta, bucketUserQueryCounts} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		meta := tx.Bucket(bucketMeta)
		if meta.Get(keyQueryCount) == nil {
			var count [8]byte
			binary.BigEndian.PutUint64(count[:], uint64(tx.Bucket(bucketQueries).Stats().KeyN))
			if err := meta.Put(keyQueryCount, count[:]); err != nil {
				return err
			}
		}
		return rebuildUserQueryCounts(tx)
	}); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, queue: make(chan event, opts.QueueSize), stop: make(chan struct{}), done: make(chan struct{}), batchSize: opts.BatchSize, flushInterval: opts.FlushInterval, queryLogEnabled: opts.QueryLogEnabled, aggregateRetention: aggregateRetentionValue, queryRetention: queryRetentionValue, maxQueryRecords: maxQueryRecordsValue, boltExpiryRetention: aggregateRetentionValue, now: opts.Now, droppedByWindow: make(map[string]uint64)}
	if err := db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketMeta).Get(keyUpdatedAt); len(v) == 8 {
			s.updatedUnixNano.Store(int64(binary.BigEndian.Uint64(v)))
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}
	go s.run()
	return s, nil
}

func normalizeRetention(aggregate, query time.Duration, records int) (time.Duration, time.Duration, int, error) {
	if aggregate == 0 {
		aggregate = aggregateRetention
	}
	if query == 0 {
		query = queryRetention
	}
	if records == 0 {
		records = maxQueryRecords
	}
	if aggregate < minAggregateRetention || aggregate > maxAggregateRetention {
		return 0, 0, 0, fmt.Errorf("aggregate retention must be between 1 and 31 days")
	}
	if query < minQueryRetention || query > maxQueryRetention {
		return 0, 0, 0, fmt.Errorf("query retention must be between 1 and 720 hours")
	}
	if records < minQueryRecords || records > maxQueryRecordLimit {
		return 0, 0, 0, fmt.Errorf("max query records must be between 1000 and 5000000")
	}
	return aggregate, query, records, nil
}

func ValidateSettings(settings Settings) error {
	_, _, _, err := normalizeRetention(settings.AggregateRetention, settings.QueryRetention, settings.MaxQueryRecords)
	return err
}

func (s *Store) Settings() Settings {
	s.settingsMu.RLock()
	settings := Settings{
		QueryLogEnabled:    s.queryLogEnabled,
		AggregateRetention: s.aggregateRetention,
		QueryRetention:     s.queryRetention,
		MaxQueryRecords:    s.maxQueryRecords,
	}
	s.settingsMu.RUnlock()
	if settings.AggregateRetention == 0 {
		settings.AggregateRetention = aggregateRetention
	}
	if settings.QueryRetention == 0 {
		settings.QueryRetention = queryRetention
	}
	if settings.MaxQueryRecords == 0 {
		settings.MaxQueryRecords = maxQueryRecords
	}
	return settings
}

// UpdateSettings applies telemetry settings without reopening the database.
// Pruning uses the new limits on the next flush.
func (s *Store) UpdateSettings(settings Settings) error {
	s.settingsUpdateMu.Lock()
	defer s.settingsUpdateMu.Unlock()
	aggregate, query, records, err := normalizeRetention(settings.AggregateRetention, settings.QueryRetention, settings.MaxQueryRecords)
	if err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.queryLogEnabled = settings.QueryLogEnabled
	s.aggregateRetention = aggregate
	s.queryRetention = query
	s.maxQueryRecords = records
	s.settingsMu.Unlock()
	if s.db != nil && s.boltExpiryRetention != aggregate {
		// Expiry keys include the retention value that was active when an
		// aggregate was written. Rebuild them when the setting changes so
		// increasing retention does not delete data early and decreasing it
		// takes effect without waiting for the old deadline.
		if err := s.db.Update(func(tx *bolt.Tx) error {
			if err := tx.DeleteBucket(bucketExpiry); err != nil && !errors.Is(err, bolterrors.ErrBucketNotFound) {
				return err
			}
			if _, err := tx.CreateBucket(bucketExpiry); err != nil {
				return err
			}
			for _, item := range []struct {
				bucket []byte
				code   byte
			}{{bucketMinutes, 'm'}, {bucketUpstream, 'u'}} {
				bucket := tx.Bucket(item.bucket)
				if err := bucket.ForEach(func(key, _ []byte) error {
					minute, err := aggregateMinute(key)
					if err != nil {
						return err
					}
					return putExpiry(tx, minute.Add(aggregate), item.code, key)
				}); err != nil {
					return err
				}
			}
			return s.prune(tx, s.now().UTC())
		}); err != nil {
			return err
		}
		s.boltExpiryRetention = aggregate
	}
	return nil
}

func (s *Store) Observe(result dns_handler.Result) {
	if !result.Admitted || result.Rejected {
		return
	}
	r := result
	r.AnswerIPs = append([]string(nil), result.AnswerIPs...)
	r.EDNS = *dnsutils.CloneEDNSSnapshot(&result.EDNS)
	r.UpstreamRequestEDNS = dnsutils.CloneEDNSSnapshot(result.UpstreamRequestEDNS)
	r.UpstreamResponseEDNS = dnsutils.CloneEDNSSnapshot(result.UpstreamResponseEDNS)
	r.ResponseEDNS = dnsutils.CloneEDNSSnapshot(result.ResponseEDNS)
	s.enqueue(event{result: &r, time: s.now().UTC()})
}

func (s *Store) ObserveUpstream(attempt query_context.UpstreamAttempt) {
	a := attempt
	s.enqueue(event{attempt: &a, time: s.now().UTC()})
}

func (s *Store) enqueue(e event) {
	s.enqueueMu.RLock()
	defer s.enqueueMu.RUnlock()
	if s.closed.Load() {
		s.addDropped(e.userID(), e.time)
		return
	}
	select {
	case s.queue <- e:
	default:
		s.addDropped(e.userID(), e.time)
	}
}

func (e event) userID() string {
	if e.result != nil {
		return e.result.Principal.UserID
	}
	if e.attempt != nil {
		return e.attempt.Principal.UserID
	}
	return ""
}

func (s *Store) addDropped(userID string, at time.Time) {
	s.dropped.Add(1)
	if at.IsZero() {
		at = time.Now().UTC()
	}
	minute := at.Truncate(time.Minute).Unix()
	s.droppedMu.Lock()
	if minute > s.droppedPrunedAt.Load() {
		cutoff := at.Add(-s.Settings().AggregateRetention).Truncate(time.Minute).Unix()
		for key := range s.droppedByWindow {
			parts := strings.Split(key, "\x00")
			stored, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
			if err != nil || stored < cutoff {
				delete(s.droppedByWindow, key)
			}
		}
		s.droppedPrunedAt.Store(minute)
	}
	s.droppedByWindow[fmt.Sprintf("\x00%d", minute)]++
	if userID != "" {
		s.droppedByWindow[fmt.Sprintf("%s\x00%d", userID, minute)]++
	}
	s.droppedMu.Unlock()
}

func (s *Store) droppedFor(userID string, from, to time.Time) uint64 {
	s.droppedMu.RLock()
	defer s.droppedMu.RUnlock()
	var total uint64
	start := from.Truncate(time.Minute)
	if start.Before(from) {
		start = start.Add(time.Minute)
	}
	for minute := start; minute.Before(to); minute = minute.Add(time.Minute) {
		total += s.droppedByWindow[fmt.Sprintf("%s\x00%d", userID, minute.Unix())]
	}
	return total
}

func (s *Store) Flush(ctx context.Context) error {
	s.enqueueMu.RLock()
	if s.closed.Load() {
		s.enqueueMu.RUnlock()
		return errors.New("telemetry closed")
	}
	ack := make(chan error, 1)
	select {
	case s.queue <- event{flushAck: ack}:
		s.enqueueMu.RUnlock()
	case <-ctx.Done():
		s.enqueueMu.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.enqueueMu.Lock()
		s.closed.Store(true)
		close(s.stop)
		s.enqueueMu.Unlock()
		<-s.done
		var backendErr error
		if s.db != nil {
			backendErr = s.db.Close()
		}
		if s.mysql != nil {
			backendErr = errors.Join(backendErr, s.mysql.Close())
		}
		s.closeErr = errors.Join(s.runErr, backendErr)
	})
	return s.closeErr
}

func (s *Store) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	batch := make([]event, 0, s.batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := s.writeBatch(batch)
		if err != nil {
			for _, e := range batch {
				s.addDropped(e.userID(), e.time)
			}
		}
		batch = batch[:0]
		return err
	}
	for {
		select {
		case e := <-s.queue:
			if e.flushAck != nil {
				e.flushAck <- flush()
				continue
			}
			batch = append(batch, e)
			if len(batch) >= s.batchSize {
				_ = flush()
			}
		case <-ticker.C:
			_ = flush()
		case <-s.stop:
			for {
				select {
				case e := <-s.queue:
					if e.flushAck != nil {
						e.flushAck <- flush()
					} else {
						batch = append(batch, e)
					}
				default:
					s.runErr = flush()
					return
				}
			}
		}
	}
}

func (s *Store) writeBatch(events []event) error {
	if s.mysql != nil {
		return s.writeMySQLBatch(events)
	}
	now := s.now().UTC()
	// Both lookups run before the write transaction, so no call into the
	// control store holds the telemetry write lock.
	detailed := s.detailedLogging(events)
	cutoffs, pruneUsers, err := s.dueUserCutoffs(now)
	if err != nil {
		return err
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		for _, e := range events {
			if e.result != nil {
				if err := s.writeResult(tx, e.time, *e.result, detailed[e.result.Principal.UserID]); err != nil {
					return err
				}
			}
			if e.attempt != nil {
				if err := s.writeAttempt(tx, e.time, *e.attempt); err != nil {
					return err
				}
			}
		}
		if pruneUsers {
			if err := pruneByUserRetention(tx, cutoffs); err != nil {
				return err
			}
		}
		if err := s.prune(tx, now); err != nil {
			return err
		}
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(now.UnixNano()))
		return tx.Bucket(bucketMeta).Put(keyUpdatedAt, encoded[:])
	})
	if err == nil {
		s.updatedUnixNano.Store(now.UnixNano())
		if pruneUsers {
			s.userPrunedAt.Store(now.Truncate(time.Minute).Unix())
		}
	}
	return err
}

// dueUserCutoffs returns each user's retention cutoff when a minute has passed
// since the last per-user sweep. Retention is per user, so the sweep walks
// every user holding records; once a minute bounds that cost while keeping
// expiry within a minute of its deadline.
func (s *Store) dueUserCutoffs(now time.Time) (map[string]time.Time, bool, error) {
	if now.Truncate(time.Minute).Unix() <= s.userPrunedAt.Load() {
		return nil, false, nil
	}
	var users []string
	if err := s.db.View(func(tx *bolt.Tx) error {
		users = queryUsers(tx)
		return nil
	}); err != nil {
		return nil, false, err
	}
	return s.retentionCutoffs(users, now), true, nil
}

func minuteKey(scope, userID string, minute time.Time, suffix string) []byte {
	return []byte(scope + "\x00" + userID + "\x00" + fmt.Sprintf("%020d", minute.Unix()) + "\x00" + suffix)
}

func aggregateMinute(key []byte) (time.Time, error) {
	parts := bytes.SplitN(key, []byte{0}, 4)
	if len(parts) != 4 || len(parts[2]) != 20 {
		return time.Time{}, errors.New("invalid telemetry aggregate key")
	}
	seconds, err := strconv.ParseInt(string(parts[2]), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid telemetry aggregate key: %w", err)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func putExpiry(tx *bolt.Tx, expires time.Time, bucketCode byte, target []byte) error {
	key := append([]byte(fmt.Sprintf("%020d\x00%c\x00", expires.Unix(), bucketCode)), target...)
	return tx.Bucket(bucketExpiry).Put(key, nil)
}

func metaUint64(tx *bolt.Tx, key []byte) uint64 {
	v := tx.Bucket(bucketMeta).Get(key)
	if len(v) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

func putMetaUint64(tx *bolt.Tx, key []byte, value uint64) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return tx.Bucket(bucketMeta).Put(key, encoded[:])
}

func latencyBucket(d time.Duration) int {
	ms := uint64(max(0, d.Milliseconds())) + 1
	i := bits.Len64(ms) - 1
	if i >= 32 {
		return 31
	}
	return i
}

func (s *Store) writeResult(tx *bolt.Tx, now time.Time, r dns_handler.Result, detailed bool) error {
	settings := s.Settings()
	minute := now.Truncate(time.Minute)
	failed := r.ExecError || r.Rcode == dns.RcodeServerFailure || r.Rcode == dns.RcodeRefused
	for _, pair := range [][2]string{{"g", ""}, {"u", r.Principal.UserID}} {
		key := minuteKey(pair[0], pair[1], minute, "")
		var a minuteAggregate
		b := tx.Bucket(bucketMinutes)
		if v := b.Get(key); v != nil {
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
		}
		if a.Rcodes == nil {
			a.Rcodes = make(map[string]uint64)
		}
		a.Completed++
		if failed {
			a.Failed++
		}
		if r.CacheHit {
			a.CacheHits++
		}
		a.LatencyUS += uint64(max(0, r.Duration.Microseconds()))
		a.Histogram[latencyBucket(r.Duration)]++
		rcode := dns.RcodeToString[r.Rcode]
		if rcode == "" {
			rcode = strconv.Itoa(r.Rcode)
		}
		a.Rcodes[rcode]++
		v, _ := json.Marshal(a)
		if err := b.Put(key, v); err != nil {
			return err
		}
		if err := putExpiry(tx, minute.Add(settings.AggregateRetention), 'm', key); err != nil {
			return err
		}
	}
	// Aggregates above are always written: quota and usage charts need them.
	// Only the detailed record honors the user's choice.
	if settings.QueryLogEnabled && detailed {
		sequence, err := tx.Bucket(bucketQueries).NextSequence()
		if err != nil {
			return err
		}
		id := fmt.Sprintf("%020d-%020d", now.UnixNano(), sequence)
		qtype := dns.TypeToString[r.QuestionType]
		if qtype == "" {
			qtype = strconv.Itoa(int(r.QuestionType))
		}
		rcode := dns.RcodeToString[r.Rcode]
		if rcode == "" {
			rcode = strconv.Itoa(r.Rcode)
		}
		clientIP := ""
		if r.ClientAddr.IsValid() {
			clientIP = r.ClientAddr.String()
		}
		answerIPs := append([]string(nil), r.AnswerIPs...)
		if answerIPs == nil {
			answerIPs = []string{}
		}
		edns := *dnsutils.CloneEDNSSnapshot(&r.EDNS)
		if edns.OptionCodes == nil {
			edns.OptionCodes = []uint16{}
		}
		record := QueryRecord{
			ID: id, Time: now, UserID: r.Principal.UserID, CredentialID: r.Principal.CredentialID,
			ClientIP: clientIP, Name: r.QuestionName, QType: qtype, Rcode: rcode,
			DurationMS: float64(r.Duration.Microseconds()) / 1000, CacheHit: r.CacheHit,
			Protocol: r.Protocol, AnswerIPs: answerIPs, EDNS: edns,
			EDNSTraceVersion: r.EDNSTraceVersion, UpstreamStageStatus: r.UpstreamStageStatus,
			UpstreamRequestEDNS:  dnsutils.CloneEDNSSnapshot(r.UpstreamRequestEDNS),
			UpstreamResponseEDNS: dnsutils.CloneEDNSSnapshot(r.UpstreamResponseEDNS),
			ResponseEDNS:         dnsutils.CloneEDNSSnapshot(r.ResponseEDNS),
			ResponseSource:       r.ResponseSource, ResponseSourceID: r.ResponseSourceID,
			UpstreamID: r.UpstreamID, MatchedRuleID: r.MatchedRuleID,
			MatchedPublicListID: r.MatchedPublicListID,
		}
		v, _ := json.Marshal(record)
		if err := tx.Bucket(bucketQueries).Put([]byte(id), v); err != nil {
			return err
		}
		if err := tx.Bucket(bucketUserQ).Put([]byte(r.Principal.UserID+"\x00"+id), v); err != nil {
			return err
		}
		if err := putMetaUint64(tx, keyQueryCount, metaUint64(tx, keyQueryCount)+1); err != nil {
			return err
		}
		if err := putUserQueryCount(tx, r.Principal.UserID, userQueryCount(tx, r.Principal.UserID)+1); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) writeAttempt(tx *bolt.Tx, now time.Time, a query_context.UpstreamAttempt) error {
	minute := now.Truncate(time.Minute)
	retention := s.Settings().AggregateRetention
	for _, pair := range [][2]string{{"g", ""}, {"u", a.Principal.UserID}} {
		key := minuteKey(pair[0], pair[1], minute, a.UpstreamID)
		var agg upstreamAggregate
		b := tx.Bucket(bucketUpstream)
		if v := b.Get(key); v != nil {
			if err := json.Unmarshal(v, &agg); err != nil {
				return err
			}
		}
		agg.Attempts++
		if a.Failed {
			agg.Failures++
		}
		agg.LatencyUS += uint64(max(0, a.Duration.Microseconds()))
		v, _ := json.Marshal(agg)
		if err := b.Put(key, v); err != nil {
			return err
		}
		if err := putExpiry(tx, minute.Add(retention), 'u', key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) prune(tx *bolt.Tx, now time.Time) error {
	settings := s.Settings()
	expiry := tx.Bucket(bucketExpiry)
	c := expiry.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		parts := bytes.SplitN(k, []byte{0}, 3)
		if len(parts) != 3 {
			if err := c.Delete(); err != nil {
				return err
			}
			continue
		}
		expires, err := strconv.ParseInt(string(parts[0]), 10, 64)
		if err != nil {
			if err := c.Delete(); err != nil {
				return err
			}
			continue
		}
		if expires > now.Unix() {
			break
		}
		var target *bolt.Bucket
		switch string(parts[1]) {
		case "m":
			target = tx.Bucket(bucketMinutes)
		case "u":
			target = tx.Bucket(bucketUpstream)
		}
		if target != nil {
			if err := target.Delete(parts[2]); err != nil {
				return err
			}
		}
		if err := c.Delete(); err != nil {
			return err
		}
	}
	// Age is enforced per user by the minute sweep, since a user may keep
	// records longer than the default. This pass only enforces the ceiling no
	// choice can exceed, which also catches users whose choice could not be
	// read.
	q := tx.Bucket(bucketQueries)
	ceiling := fmt.Sprintf("%020d", now.Add(-maxQueryRetention).UnixNano())
	c = q.Cursor()
	count := metaUint64(tx, keyQueryCount)
	for k, v := c.First(); len(k) >= 20 && string(k[:20]) < ceiling; k, v = c.Next() {
		var record QueryRecord
		_ = json.Unmarshal(v, &record)
		if err := tx.Bucket(bucketUserQ).Delete([]byte(record.UserID + "\x00" + string(k))); err != nil {
			return err
		}
		if err := c.Delete(); err != nil {
			return err
		}
		if n := userQueryCount(tx, record.UserID); n > 0 {
			if err := putUserQueryCount(tx, record.UserID, n-1); err != nil {
				return err
			}
		}
		if count > 0 {
			count--
		}
	}
	if err := putMetaUint64(tx, keyQueryCount, count); err != nil {
		return err
	}
	return evictOverCap(tx, settings.MaxQueryRecords)
}

func validateRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > maxRange {
		return errors.New("invalid telemetry range")
	}
	return nil
}

type storedKV struct {
	key   string
	value []byte
}

func (s *Store) Snapshot(ctx context.Context, userID string, from, to time.Time) (StatsSnapshot, error) {
	if s.mysql != nil {
		return s.mysqlSnapshot(ctx, userID, from, to)
	}
	if err := validateRange(from, to); err != nil {
		return StatsSnapshot{}, err
	}
	requestedFrom := from
	settings := s.Settings()
	retainedFrom := s.now().UTC().Add(-settings.AggregateRetention)
	if from.Before(retainedFrom) {
		from = retainedFrom
	}
	snapshot := StatsSnapshot{From: requestedFrom, To: to, RcodeCounts: make(map[string]uint64), Series: []SeriesPoint{}, Upstreams: []UpstreamStats{}, Dropped: s.droppedFor(userID, from, to), QueryLogEnabled: settings.QueryLogEnabled}
	if !from.Before(to) {
		return snapshot, nil
	}
	if n := s.updatedUnixNano.Load(); n != 0 {
		snapshot.UpdatedAt = time.Unix(0, n).UTC()
	}
	var minuteRows, upstreamRows []storedKV
	err := s.db.View(func(tx *bolt.Tx) error {
		scope := "g"
		if userID != "" {
			scope = "u"
		}
		prefix := []byte(scope + "\x00" + userID + "\x00")
		copyRange := func(bucket []byte, dst *[]storedKV) error {
			c := tx.Bucket(bucket).Cursor()
			seek := append(append([]byte(nil), prefix...), []byte(fmt.Sprintf("%020d", from.Truncate(time.Minute).Unix()))...)
			for k, v := c.Seek(seek); k != nil && strings.HasPrefix(string(k), string(prefix)); k, v = c.Next() {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				parts := strings.Split(string(k), "\x00")
				if len(parts) < 3 {
					continue
				}
				ts, _ := strconv.ParseInt(parts[2], 10, 64)
				tm := time.Unix(ts, 0).UTC()
				if !tm.Before(to) {
					break
				}
				if tm.Before(from) {
					continue
				}
				*dst = append(*dst, storedKV{key: string(k), value: append([]byte(nil), v...)})
			}
			return nil
		}
		if err := copyRange(bucketMinutes, &minuteRows); err != nil {
			return err
		}
		return copyRange(bucketUpstream, &upstreamRows)
	})
	if err != nil {
		return snapshot, err
	}
	var totalUS uint64
	var hist [32]uint64
	for _, row := range minuteRows {
		parts := strings.Split(row.key, "\x00")
		ts, _ := strconv.ParseInt(parts[2], 10, 64)
		tm := time.Unix(ts, 0).UTC()
		var a minuteAggregate
		if err := json.Unmarshal(row.value, &a); err != nil {
			return snapshot, err
		}
		p := SeriesPoint{Time: tm, Completed: a.Completed, Failed: a.Failed, CacheHits: a.CacheHits}
		if a.Completed > 0 {
			p.AvgLatencyMS = float64(a.LatencyUS) / 1000 / float64(a.Completed)
		}
		snapshot.Series = append(snapshot.Series, p)
		snapshot.Completed += a.Completed
		snapshot.Failed += a.Failed
		snapshot.CacheHits += a.CacheHits
		totalUS += a.LatencyUS
		for key, n := range a.Rcodes {
			snapshot.RcodeCounts[key] += n
		}
		for i, n := range a.Histogram {
			hist[i] += n
		}
	}
	if snapshot.Completed > 0 {
		snapshot.AvgLatencyMS = float64(totalUS) / 1000 / float64(snapshot.Completed)
		rank := (snapshot.Completed*95 + 99) / 100
		var seen uint64
		for i, n := range hist {
			seen += n
			if seen >= rank {
				snapshot.P95LatencyMS = float64((uint64(1) << (i + 1)) - 1)
				break
			}
		}
	}
	up := make(map[string]*UpstreamStats)
	for _, row := range upstreamRows {
		parts := strings.Split(row.key, "\x00")
		if len(parts) < 4 {
			continue
		}
		var a upstreamAggregate
		if err := json.Unmarshal(row.value, &a); err != nil {
			return snapshot, err
		}
		x := up[parts[3]]
		if x == nil {
			x = &UpstreamStats{ID: parts[3]}
			up[parts[3]] = x
		}
		x.Attempts += a.Attempts
		x.Failures += a.Failures
		x.AvgLatencyMS += float64(a.LatencyUS) / 1000
	}
	for _, x := range up {
		if x.Attempts > 0 {
			x.AvgLatencyMS /= float64(x.Attempts)
		}
		snapshot.Upstreams = append(snapshot.Upstreams, *x)
	}
	sort.Slice(snapshot.Upstreams, func(i, j int) bool { return snapshot.Upstreams[i].ID < snapshot.Upstreams[j].ID })
	return snapshot, nil
}

func (s *Store) Queries(ctx context.Context, userID string, from, to time.Time, filter QueryFilter, page Page) (QueryPage, error) {
	if s.mysql != nil {
		return s.mysqlQueries(ctx, userID, from, to, filter, page)
	}
	result := QueryPage{Items: []QueryRecord{}}
	settings := s.Settings()
	if !settings.QueryLogEnabled {
		return result, nil
	}
	if err := validateRange(from, to); err != nil {
		return result, err
	}
	retainedFrom := s.now().UTC().Add(-s.visibleRetention(ctx, userID, settings.QueryRetention))
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
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketQueries)
		prefix := ""
		if userID != "" {
			b = tx.Bucket(bucketUserQ)
			prefix = userID + "\x00"
		}
		seek := prefix + fmt.Sprintf("%020d", to.UnixNano())
		if page.Cursor != "" {
			seek = prefix + page.Cursor
		}
		c := b.Cursor()
		// Seek lands on the first key at or after the bound; both branches then
		// step to the newest key strictly before it, so Seek's value is unused.
		k, _ := c.Seek([]byte(seek))
		var v []byte
		if k == nil {
			k, v = c.Last()
		} else {
			k, v = c.Prev()
		}
		for ; k != nil && (prefix == "" || strings.HasPrefix(string(k), prefix)); k, v = c.Prev() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			var r QueryRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if r.AnswerIPs == nil {
				r.AnswerIPs = []string{}
			}
			if r.EDNS.OptionCodes == nil {
				r.EDNS.OptionCodes = []uint16{}
			}
			normalizeEDNSTrace(&r)
			if r.Time.Before(from) {
				break
			}
			if !r.Time.Before(to) {
				continue
			}
			if !filter.matches(r) {
				continue
			}
			result.Items = append(result.Items, r)
			if len(result.Items) > page.Limit {
				result.Items = result.Items[:page.Limit]
				result.NextCursor = result.Items[page.Limit-1].ID
				break
			}
		}
		return nil
	})
	return result, err
}

func normalizeEDNSTrace(record *QueryRecord) {
	if record.UpstreamStageStatus == "" {
		record.UpstreamStageStatus = query_context.UpstreamStageUnavailable
	}
}

func (f QueryFilter) matches(r QueryRecord) bool {
	if f.Name != "" && !strings.Contains(strings.ToLower(r.Name), strings.ToLower(f.Name)) {
		return false
	}
	if f.QType != "" && !strings.EqualFold(r.QType, f.QType) {
		return false
	}
	if f.Rcode != "" && !strings.EqualFold(r.Rcode, f.Rcode) {
		return false
	}
	if f.CredentialID != "" && r.CredentialID != f.CredentialID {
		return false
	}
	if f.Protocol != "" && !strings.EqualFold(r.Protocol, f.Protocol) {
		return false
	}
	if f.Address != "" {
		found := r.ClientIP == f.Address
		for _, answer := range r.AnswerIPs {
			found = found || answer == f.Address
		}
		if !found {
			return false
		}
	}
	if f.ResponseSource != "" && !strings.EqualFold(r.ResponseSource, f.ResponseSource) {
		return false
	}
	if f.UpstreamID != "" && r.UpstreamID != f.UpstreamID {
		return false
	}
	return f.CacheHit == nil || r.CacheHit == *f.CacheHit
}

type Service interface {
	Observe(dns_handler.Result)
	ObserveUpstream(query_context.UpstreamAttempt)
	Snapshot(context.Context, string, time.Time, time.Time) (StatsSnapshot, error)
	Queries(context.Context, string, time.Time, time.Time, QueryFilter, Page) (QueryPage, error)
	Flush(context.Context) error
	Close() error
}

var _ Service = (*Store)(nil)
