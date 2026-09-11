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

package coremain

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pires/go-proxyproto"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/coremain/listen"
	"github.com/pmkol/mosdns-x/pkg/server"
	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
	H "github.com/pmkol/mosdns-x/pkg/server/http_handler"
)

const defaultQueryTimeout = time.Second * 5

func (m *Mosdns) startServers(cfg *ServerConfig) error {
	if len(cfg.Listeners) == 0 {
		return errors.New("no server listener is configured")
	}
	if len(cfg.Exec) == 0 {
		return errors.New("empty entry")
	}

	entry := m.execs[cfg.Exec]
	if entry == nil {
		return fmt.Errorf("cannot find entry %s", cfg.Exec)
	}

	queryTimeout := defaultQueryTimeout
	if cfg.Timeout > 0 {
		queryTimeout = time.Duration(cfg.Timeout) * time.Second
	}

	for _, lc := range cfg.Listeners {
		opts := D.EntryHandlerOpts{Logger: m.logger, Entry: entry, QueryTimeout: queryTimeout, RecursionAvailable: true}
		if m.control != nil && isHTTPDNSProtocol(lc.Protocol) {
			opts.Admit = admit(m.control)
			opts.Observe = m.telemetry.Observe
		}
		dnsHandler, err := D.NewEntryHandler(opts)
		if err != nil {
			return fmt.Errorf("failed to init entry handler, %w", err)
		}
		if err := m.startServerListener(lc, dnsHandler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Mosdns) startServerListener(cfg *ServerListenerConfig, dnsHandler D.Handler) error {
	if len(cfg.Addr) == 0 {
		return errors.New("no address to bind")
	}

	m.logger.Info("starting server", zap.String("proto", cfg.Protocol), zap.String("addr", cfg.Addr))

	idleTimeout := time.Duration(0)
	if cfg.IdleTimeout > 0 {
		idleTimeout = time.Duration(cfg.IdleTimeout) * time.Second
	}

	httpOpts := H.HandlerOpts{
		DNSHandler:  dnsHandler,
		Path:        cfg.URLPath,
		SrcIPHeader: cfg.GetUserIPFromHeader,
		Logger:      m.logger,
	}
	if m.control != nil && isHTTPDNSProtocol(cfg.Protocol) {
		httpOpts.Authenticate = authenticate(m.control)
		httpOpts.TrustedProxies = m.trustedProxies
		httpOpts.UpstreamObserver = m.telemetry.ObserveUpstream
	}
	httpHandler, err := H.NewHandler(httpOpts)
	if err != nil {
		return fmt.Errorf("failed to init http handler, %w", err)
	}

	opts := server.ServerOpts{
		DNSHandler:       dnsHandler,
		HttpHandler:      httpHandler,
		Cert:             cfg.Cert,
		Key:              cfg.Key,
		KernelTX:         cfg.KernelTX,
		KernelRX:         cfg.KernelRX,
		IdleTimeout:      idleTimeout,
		DisableEarlyData: m.control != nil,
		Logger:           m.logger,
	}
	s := server.NewServer(opts)

	// helper func for proxy protocol listener
	requirePP := func(_ net.Addr) (proxyproto.Policy, error) {
		return proxyproto.REQUIRE, nil
	}

	config := listen.CreateListenConfig(cfg.UnixDomainSocket)
	abstract := strings.HasPrefix(cfg.Addr, "@")
	ctx := context.Background()

	var run func() error
	switch cfg.Protocol {
	case "", "udp", "quic", "doq", "h3", "doh3":
		var conn net.PacketConn
		var err error
		if cfg.UnixDomainSocket {
			if !abstract {
				os.Remove(cfg.Addr)
			}
			conn, err = config.ListenPacket(ctx, "unixgram", cfg.Addr)
			if !abstract {
				os.Chmod(cfg.Addr, 0x777)
			}
		} else {
			conn, err = config.ListenPacket(ctx, "udp", cfg.Addr)
		}
		if err != nil {
			return err
		}
		m.ownedClosers = append(m.ownedClosers, conn)
		s.OwnCloser(conn)
		switch cfg.Protocol {
		case "", "udp":
			run = func() error { return s.ServeUDP(conn) }
		case "quic", "doq":
			l, err := s.CreateQUICListner(conn, []string{"doq"})
			if err != nil {
				conn.Close()
				return err
			}
			run = func() error { return s.ServeQUIC(l) }
		case "h3", "doh3":
			l, err := s.CreateQUICListner(conn, []string{"h3"})
			if err != nil {
				conn.Close()
				return err
			}
			run = func() error { return s.ServeH3(l) }
		}
	case "tcp", "http", "tls", "dot", "https", "doh":
		var l net.Listener
		var err error
		if cfg.UnixDomainSocket {
			if !abstract {
				os.Remove(cfg.Addr)
			}
			l, err = config.Listen(ctx, "unix", cfg.Addr)
			if !abstract {
				os.Chmod(cfg.Addr, 0x777)
			}
		} else {
			l, err = config.Listen(ctx, "tcp", cfg.Addr)
		}
		if err != nil {
			return err
		}
		m.ownedClosers = append(m.ownedClosers, l)
		s.OwnCloser(l)
		if cfg.ProxyProtocol {
			l = &proxyproto.Listener{Listener: l, Policy: requirePP}
		}
		switch cfg.Protocol {
		case "tcp":
			run = func() error { return s.ServeTCP(l) }
		case "tls", "dot":
			tlsListener, tlsErr := s.CreateETLSListner(l, []string{"dot"})
			if tlsErr != nil {
				l.Close()
				return tlsErr
			}
			l = tlsListener
			run = func() error { return s.ServeTCP(l) }
		case "http":
			run = func() error { return s.ServeHTTP(l) }
		case "https", "doh":
			tlsListener, tlsErr := s.CreateETLSListner(l, []string{"h2"})
			if tlsErr != nil {
				l.Close()
				return tlsErr
			}
			l = tlsListener
			run = func() error { return s.ServeHTTP(l) }
		}
	default:
		return fmt.Errorf("unknown protocol: [%s]", cfg.Protocol)
	}
	m.servers = append(m.servers, s)

	m.sc.Attach(func(done func(), closeSignal <-chan struct{}) {
		defer done()
		errChan := make(chan error, 1)
		go func() {
			errChan <- run()
		}()
		serveDone := false
		select {
		case err := <-errChan:
			serveDone = true
			select {
			case <-closeSignal:
			default:
				m.sc.SendCloseSignal(fmt.Errorf("server exited, %w", err))
			}
		case <-closeSignal:
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := s.Shutdown(shutdownCtx)
		cancel()
		if err != nil {
			m.logger.Warn("server shutdown error", zap.Error(err))
			_ = s.Shutdown(context.Background())
		}
		if !serveDone {
			<-errChan
		}
	})

	return nil
}

func isHTTPDNSProtocol(protocol string) bool {
	switch strings.ToLower(protocol) {
	case "http", "https", "doh", "h3", "doh3":
		return true
	}
	return false
}
