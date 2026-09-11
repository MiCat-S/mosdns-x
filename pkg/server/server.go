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
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
	H "github.com/pmkol/mosdns-x/pkg/server/http_handler"
)

var (
	ErrServerClosed       = errors.New("server closed")
	errMissingHTTPHandler = errors.New("missing http handler")
	errMissingDNSHandler  = errors.New("missing dns handler")
)

var nopLogger = zap.NewNop()

type ServerOpts struct {
	// Logger optionally specifies a logger for the server logging.
	// A nil Logger will disable the logging.
	Logger *zap.Logger

	// DNSHandler is the dns handler required by UDP, TCP, DoT server.
	DNSHandler D.Handler

	// HttpHandler is the http handler required by HTTP, DoH server.
	HttpHandler *H.Handler

	// Certificate files to start DoT, DoH server.
	// Only useful if there is no server certificate specified in TLSConfig.
	Cert, Key string

	// KernelTX and KernelRX control whether kernel TLS offloading is enabled
	// If the kernel is not supported, it is automatically downgraded to the application implementation
	//
	// If this option is enabled, please mount the TLS module before you run application.
	// On Linux, it will try to automatically mount the tls kernel module.
	KernelRX, KernelTX bool

	// IdleTimeout limits the maximum time period that a connection
	// can idle. Default is defaultTCPIdleTimeout.
	IdleTimeout time.Duration

	// DisableEarlyData disables replayable TLS early data and QUIC 0-RTT.
	DisableEarlyData bool
}

func (opts *ServerOpts) init() {
	if opts.Logger == nil {
		opts.Logger = nopLogger
	}

	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 0
	}
}

// Server is a DNS server.
// It's functions, Server.ServeUDP etc., will block and
// close the net.Listener/net.PacketConn and always return
// a non-nil error. If Server was closed, the returned err
// will be ErrServerClosed.
type Server struct {
	opts ServerOpts

	m             sync.Mutex
	closed        bool
	closerTracker map[io.Closer]struct{}
	queryWG       sync.WaitGroup
	closeOnce     sync.Once
	closeErr      error
}

func NewServer(opts ServerOpts) *Server {
	opts.init()
	return &Server{
		opts: opts,
	}
}

// Closed returns true if server was closed.
func (s *Server) Closed() bool {
	s.m.Lock()
	defer s.m.Unlock()
	return s.closed
}

// trackCloser adds or removes c to the Server and return true if Server is not closed.
// We use a pointer in case the underlying value is incomparable.
func (s *Server) trackCloser(c io.Closer, add bool) bool {
	s.m.Lock()
	defer s.m.Unlock()

	if s.closerTracker == nil {
		s.closerTracker = make(map[io.Closer]struct{})
	}

	if add {
		if s.closed {
			return false
		}
		s.closerTracker[c] = struct{}{}
	} else {
		delete(s.closerTracker, c)
	}
	return true
}

// OwnCloser registers a transport that must be closed with the server.
// It returns false and closes c when shutdown has already begun.
func (s *Server) OwnCloser(c io.Closer) bool {
	if s.trackCloser(c, true) {
		return true
	}
	_ = c.Close()
	return false
}

// Close closes the Server and all its inner listeners.
func (s *Server) Close() {
	s.m.Lock()
	s.closed = true
	closers := make([]io.Closer, 0, len(s.closerTracker))
	for c := range s.closerTracker {
		closers = append(closers, c)
	}
	s.m.Unlock()
	for _, c := range closers {
		_ = c.Close()
	}
}

type gracefulCloser interface{ Shutdown(context.Context) error }

func (s *Server) beginQuery() bool {
	s.m.Lock()
	defer s.m.Unlock()
	if s.closed {
		return false
	}
	s.queryWG.Add(1)
	return true
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.m.Lock()
		s.closed = true
		closers := make([]io.Closer, 0, len(s.closerTracker))
		for c := range s.closerTracker {
			closers = append(closers, c)
		}
		s.m.Unlock()
		// Graceful servers stop accepting and drain before auxiliary resources
		// such as certificate watchers are closed.
		var errs []error
		for _, c := range closers {
			if graceful, ok := c.(gracefulCloser); ok {
				if err := graceful.Shutdown(ctx); err != nil {
					errs = append(errs, err)
				}
				// Shutdown drains handlers; Close then releases the listener so
				// the serving goroutine always terminates.
				if closeErr := c.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
					errs = append(errs, closeErr)
				}
			}
		}
		for _, c := range closers {
			if _, ok := c.(gracefulCloser); !ok {
				if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					errs = append(errs, err)
				}
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	done := make(chan struct{})
	go func() {
		s.queryWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return s.closeErr
	case <-ctx.Done():
		return errors.Join(s.closeErr, ctx.Err())
	}
}
