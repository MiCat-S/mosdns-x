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
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

package server

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/quic-go/quic-go"
	eTLS "gitlab.com/go-extension/tls"
	"go.uber.org/zap"
)

type cert[T tls.Certificate | eTLS.Certificate] struct {
	mu   sync.RWMutex
	c    *T
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func (c *cert[T]) get() *T      { c.mu.RLock(); defer c.mu.RUnlock(); return c.c }
func (c *cert[T]) set(v T)      { c.mu.Lock(); c.c = &v; c.mu.Unlock() }
func (c *cert[T]) Close() error { c.once.Do(func() { close(c.stop); <-c.done }); return nil }

func calculateTimeUntilMidnight() time.Duration {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	return next.Sub(now)
}

// tryCreateWatchCert loads the certificate and key and watches them for changes.
//
// The parent directories are watched instead of the files themselves, because
// the common renewal flow (certbot, acme.sh, Caddy, Kubernetes secrets) writes a
// temp file and renames it over the old one. Watching the file would only see
// events on the old inode, and the watch would die with it.
func tryCreateWatchCert[T tls.Certificate | eTLS.Certificate](certFile, keyFile string, load func(string, string) (T, error), logger *zap.Logger) (*cert[T], error) {
	if logger == nil {
		logger = nopLogger
	}
	loaded, err := load(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Clean(certFile), filepath.Clean(keyFile)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	dirs := []string{filepath.Dir(certPath)}
	if d := filepath.Dir(keyPath); d != dirs[0] {
		dirs = append(dirs, d)
	}
	for _, d := range dirs {
		if err = w.Add(d); err != nil {
			w.Close()
			return nil, err
		}
	}
	c := &cert[T]{c: &loaded, stop: make(chan struct{}), done: make(chan struct{})}
	fields := []zap.Field{zap.String("cert", certFile), zap.String("key", keyFile)}
	reload := func() {
		for _, f := range [...]string{certPath, keyPath} {
			if _, err := os.Stat(f); err != nil {
				// Probably in the middle of a replacement. The next event will retry.
				logger.Info("certificate file not ready, skip reloading", append(fields, zap.Error(err))...)
				return
			}
		}
		v, err := load(certFile, keyFile)
		if err != nil {
			logger.Warn("failed to reload certificate, keep using the current one", append(fields, zap.Error(err))...)
			return
		}
		c.set(v)
		logger.Info("certificate reloaded", fields...)
	}
	check := func() {
		current := c.get()
		var raw [][]byte
		switch v := any(current).(type) {
		case *tls.Certificate:
			raw = v.Certificate
		case *eTLS.Certificate:
			raw = v.Certificate
		}
		if len(raw) > 0 {
			if parsed, err := x509.ParseCertificate(raw[0]); err == nil && parsed.NotAfter.Before(time.Now().Add(72*time.Hour)) {
				reload()
			}
		}
	}
	// shouldReload reports whether a directory event may have changed the cert or key.
	shouldReload := func(event fsnotify.Event) bool {
		name := filepath.Clean(event.Name)
		if name == certPath || name == keyPath {
			return event.Has(fsnotify.Create) || event.Has(fsnotify.Write) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove)
		}
		// A temp file (or a symlinked data dir, as in Kubernetes secrets) being
		// renamed or created may replace the targets without naming them.
		return event.Has(fsnotify.Create) || event.Has(fsnotify.Rename)
	}
	go func() {
		defer close(c.done)
		defer w.Close()
		daily := time.NewTimer(calculateTimeUntilMidnight())
		defer daily.Stop()
		var debounce *time.Timer
		var debounceC <-chan time.Time
		defer func() {
			if debounce != nil {
				debounce.Stop()
			}
		}()
		for {
			select {
			case event, ok := <-w.Events:
				if !ok {
					return
				}
				if !shouldReload(event) {
					continue
				}
				if debounce == nil {
					debounce = time.NewTimer(time.Second)
				} else {
					if !debounce.Stop() {
						select {
						case <-debounce.C:
						default:
						}
					}
					debounce.Reset(time.Second)
				}
				debounceC = debounce.C
			case <-debounceC:
				reload()
				debounceC = nil
			case <-daily.C:
				check()
				daily.Reset(calculateTimeUntilMidnight())
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				logger.Warn("certificate watcher error", append(fields, zap.Error(err))...)
			case <-c.stop:
				return
			}
		}
	}()
	return c, nil
}

func (s *Server) CreateQUICListner(conn net.PacketConn, nextProtos []string) (*quic.EarlyListener, error) {
	if s.opts.Cert == "" || s.opts.Key == "" {
		return nil, errors.New("missing certificate for tls listener")
	}
	c, err := tryCreateWatchCert(s.opts.Cert, s.opts.Key, tls.LoadX509KeyPair, s.opts.Logger)
	if err != nil {
		return nil, err
	}
	if !s.trackCloser(c, true) {
		c.Close()
		return nil, ErrServerClosed
	}
	l, err := quic.ListenEarly(conn, &tls.Config{NextProtos: nextProtos, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return c.get(), nil }}, &quic.Config{Allow0RTT: !s.opts.DisableEarlyData, InitialStreamReceiveWindow: 1252, MaxStreamReceiveWindow: 4 * 1024, InitialConnectionReceiveWindow: 8 * 1024, MaxConnectionReceiveWindow: 16 * 1024})
	if err != nil {
		s.trackCloser(c, false)
		c.Close()
		return nil, err
	}
	if !s.OwnCloser(l) {
		return nil, ErrServerClosed
	}
	return l, nil
}

func (s *Server) CreateETLSListner(l net.Listener, nextProtos []string) (net.Listener, error) {
	if s.opts.Cert == "" || s.opts.Key == "" {
		return nil, errors.New("missing certificate for tls listener")
	}
	c, err := tryCreateWatchCert(s.opts.Cert, s.opts.Key, eTLS.LoadX509KeyPair, s.opts.Logger)
	if err != nil {
		return nil, err
	}
	if !s.trackCloser(c, true) {
		c.Close()
		return nil, ErrServerClosed
	}
	return eTLS.NewListener(l, &eTLS.Config{KernelTX: s.opts.KernelTX, KernelRX: s.opts.KernelRX, AllowEarlyData: !s.opts.DisableEarlyData, MaxEarlyData: 4096, NextProtos: nextProtos, Defaults: eTLS.Defaults{AllSecureCipherSuites: true, AllSecureCurves: true}, GetCertificate: func(*eTLS.ClientHelloInfo) (*eTLS.Certificate, error) { return c.get(), nil }}), nil
}
