/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package server

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// errQueryLimitReached is returned by beginQuery when ServerOpts.MaxConcurrentQueries
// in-flight queries are already being served.
var errQueryLimitReached = errors.New("concurrent query limit reached")

// DoQ application error code DOQ_EXCESSIVE_LOAD (RFC 9250 section 4.3).
const doqExcessiveLoad = 0x4

// rejectLogInterval bounds how often rejections are logged. Rejections are
// counted in between and reported as one debug line, so a flood of dropped
// packets does not turn into a flood of log entries.
const rejectLogInterval = 10 * time.Second

// serverMetrics holds the Prometheus collectors of a Server. The gauges read
// the Server's atomic counters at scrape time so the hot path only pays for
// the atomic add it already needs for limit accounting.
type serverMetrics struct {
	inflightQueries     prometheus.GaugeFunc
	rejectedQueries     *prometheus.CounterVec
	openConnections     prometheus.GaugeFunc
	rejectedConnections *prometheus.CounterVec
}

func newServerMetrics(s *Server) *serverMetrics {
	return &serverMetrics{
		inflightQueries: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "server_inflight_queries",
			Help: "Number of DNS queries currently being served by the listener.",
		}, func() float64 { return float64(s.inflightQueries.Load()) }),
		rejectedQueries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "server_rejected_queries_total",
			Help: "Number of DNS queries rejected because max_concurrent_queries was reached.",
		}, []string{"protocol"}),
		openConnections: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "server_open_connections",
			Help: "Number of open TCP, DoT and DoQ connections of the listener.",
		}, func() float64 { return float64(s.openConns.Load()) }),
		rejectedConnections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "server_rejected_connections_total",
			Help: "Number of connections closed on accept because max_connections was reached.",
		}, []string{"protocol"}),
	}
}

// Metrics returns the Prometheus collectors of this Server. The caller is
// responsible for registering them, typically with a label that identifies
// the listener.
func (s *Server) Metrics() []prometheus.Collector {
	return []prometheus.Collector{
		s.metrics.inflightQueries,
		s.metrics.rejectedQueries,
		s.metrics.openConnections,
		s.metrics.rejectedConnections,
	}
}

// rejectLog is a rate-limited debug logger for rejected queries or connections.
type rejectLog struct {
	count  atomic.Uint64
	lastAt atomic.Int64 // unix nano of the last emitted log line
}

func (l *rejectLog) note(logger *zap.Logger, msg string, limit int) {
	l.count.Add(1)
	if !logger.Core().Enabled(zap.DebugLevel) {
		return
	}
	now := time.Now().UnixNano()
	last := l.lastAt.Load()
	if now-last < int64(rejectLogInterval) || !l.lastAt.CompareAndSwap(last, now) {
		return
	}
	logger.Debug(msg, zap.Uint64("rejected", l.count.Swap(0)), zap.Int("limit", limit))
}

// beginQuery registers an in-flight query. It never blocks. It returns
// ErrServerClosed once shutdown has begun and errQueryLimitReached when
// ServerOpts.MaxConcurrentQueries is exhausted; protocol only labels the
// rejection metric. Each nil return must be paired with endQuery.
func (s *Server) beginQuery(protocol string) error {
	n := s.inflightQueries.Add(1)
	if limit := s.opts.MaxConcurrentQueries; limit > 0 && n > int64(limit) {
		s.inflightQueries.Add(-1)
		s.metrics.rejectedQueries.WithLabelValues(protocol).Inc()
		s.queryRejectLog.note(s.opts.Logger, "queries rejected: concurrent query limit reached", limit)
		return errQueryLimitReached
	}

	s.m.Lock()
	defer s.m.Unlock()
	if s.closed {
		s.inflightQueries.Add(-1)
		return ErrServerClosed
	}
	s.queryWG.Add(1)
	return nil
}

// endQuery releases a query registered by a successful beginQuery.
func (s *Server) endQuery() {
	s.inflightQueries.Add(-1)
	s.queryWG.Done()
}

// beginConn registers an accepted TCP, DoT or DoQ connection. It never
// blocks and returns false when ServerOpts.MaxConnections is exhausted; the
// caller must then close the connection. Each true return must be paired
// with endConn.
func (s *Server) beginConn(protocol string) bool {
	n := s.openConns.Add(1)
	if limit := s.opts.MaxConnections; limit > 0 && n > int64(limit) {
		s.openConns.Add(-1)
		s.metrics.rejectedConnections.WithLabelValues(protocol).Inc()
		s.connRejectLog.note(s.opts.Logger, "connections rejected: connection limit reached", limit)
		return false
	}
	return true
}

// endConn releases a connection registered by a successful beginConn.
func (s *Server) endConn() {
	s.openConns.Add(-1)
}
