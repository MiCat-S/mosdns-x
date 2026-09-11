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
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/quic-go/quic-go"
	eTLS "gitlab.com/go-extension/tls"
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

func tryCreateWatchCert[T tls.Certificate | eTLS.Certificate](certFile, keyFile string, load func(string, string) (T, error)) (*cert[T], error) {
	loaded, err := load(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err = w.Add(certFile); err != nil {
		w.Close()
		return nil, err
	}
	if err = w.Add(keyFile); err != nil {
		w.Close()
		return nil, err
	}
	c := &cert[T]{c: &loaded, stop: make(chan struct{}), done: make(chan struct{})}
	reload := func() {
		if v, err := load(certFile, keyFile); err == nil {
			c.set(v)
		}
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
				if event.Has(fsnotify.Chmod) || event.Has(fsnotify.Remove) {
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
			case <-w.Errors:
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
	c, err := tryCreateWatchCert(s.opts.Cert, s.opts.Key, tls.LoadX509KeyPair)
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
	c, err := tryCreateWatchCert(s.opts.Cert, s.opts.Key, eTLS.LoadX509KeyPair)
	if err != nil {
		return nil, err
	}
	if !s.trackCloser(c, true) {
		c.Close()
		return nil, ErrServerClosed
	}
	return eTLS.NewListener(l, &eTLS.Config{KernelTX: s.opts.KernelTX, KernelRX: s.opts.KernelRX, AllowEarlyData: !s.opts.DisableEarlyData, MaxEarlyData: 4096, NextProtos: nextProtos, Defaults: eTLS.Defaults{AllSecureCipherSuites: true, AllSecureCurves: true}, GetCertificate: func(*eTLS.ClientHelloInfo) (*eTLS.Certificate, error) { return c.get(), nil }}), nil
}
