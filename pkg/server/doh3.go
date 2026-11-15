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
	"net/http"
	"net/url"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	C "github.com/pmkol/mosdns-x/pkg/query_context"
	H "github.com/pmkol/mosdns-x/pkg/server/http_handler"
)

const defaultQUICIdleTimeout = 30 * time.Second

func (s *Server) ServeH3(l *quic.EarlyListener) error {
	defer l.Close()

	if s.opts.HttpHandler == nil {
		return errMissingHTTPHandler
	}

	idleTimeout := s.opts.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = defaultQUICIdleTimeout
	}

	hs := h3Server{Server: &http3.Server{
		Handler:        &sHandler{h: s.opts.HttpHandler, s: s},
		IdleTimeout:    idleTimeout,
		MaxHeaderBytes: 2048,
	}, s: s}
	if ok := s.trackCloser(hs, true); !ok {
		return ErrServerClosed
	}
	defer s.trackCloser(hs, false)

	err := hs.ServeListener(l)
	if err == http.ErrServerClosed { // Replace http.ErrServerClosed with our ErrServerClosed
		return ErrServerClosed
	} else if err != nil {
		return err
	}
	return nil
}

// h3IdleGrace is how long a graceful HTTP/3 shutdown waits, after the last
// in-flight query ended, for clients to close their connections. It also
// lets the last responses, sent after their handlers return, reach the
// clients: a DNS answer fits well inside QUIC's initial flow-control window
// and needs about one round trip. A client that does not take its answer
// within the grace, such as one that stopped reading, loses it and retries
// against the restarted server, rather than holding every shutdown open.
var h3IdleGrace = time.Second

// h3Server bounds a graceful shutdown by the server's in-flight queries.
// http3.Server.Shutdown sends GOAWAY and then waits for the clients to close
// their connections; a client that keeps an idle connection until its own
// idle timeout held every shutdown until the deadline. The DoH server closes
// idle connections at once, and DNS needs nothing more than the queries to
// finish, so connections still open h3IdleGrace after them are closed.
type h3Server struct {
	*http3.Server
	s *Server
}

// Shutdown runs after Server.Shutdown marked the server closed, so no new
// query can join queryWG while it waits.
func (h h3Server) Shutdown(ctx context.Context) error {
	drainCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		h.s.queryWG.Wait()
		select {
		case <-time.After(h3IdleGrace):
			cancel()
		case <-drainCtx.Done():
		}
	}()
	err := h.Server.Shutdown(drainCtx)
	if errors.Is(err, context.Canceled) && ctx.Err() == nil {
		return nil
	}
	return err
}

type sHandler struct {
	h *H.Handler
	s *Server
}

func (h *sHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.s.beginQuery(C.ProtocolH3); err != nil {
		if errors.Is(err, errQueryLimitReached) {
			w.Header().Set("Retry-After", "1")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer h.s.endQuery()
	h.h.ServeHTTP(&sWriter{w}, &sRequest{r})
}

type sRequest struct {
	r *http.Request
}

func (r *sRequest) URL() *url.URL {
	return r.r.URL
}

func (r *sRequest) TLS() *H.TlsInfo {
	if r.r.TLS == nil {
		return nil
	} else {
		return &H.TlsInfo{
			Version:            r.r.TLS.Version,
			ServerName:         r.r.TLS.ServerName,
			NegotiatedProtocol: r.r.TLS.NegotiatedProtocol,
		}
	}
}

func (r *sRequest) Body() io.ReadCloser {
	return r.r.Body
}

func (r *sRequest) Header() H.Header {
	return r.r.Header
}

func (r *sRequest) Method() string {
	return r.r.Method
}

func (r *sRequest) Context() context.Context {
	return r.r.Context()
}

func (r *sRequest) RequestURI() string {
	return r.r.RequestURI
}

func (r *sRequest) GetRemoteAddr() string {
	return r.r.RemoteAddr
}

func (r *sRequest) SetRemoteAddr(addr string) {
	r.r.RemoteAddr = addr
}

type sWriter struct {
	w http.ResponseWriter
}

func (w *sWriter) Header() H.Header {
	return w.w.Header()
}

func (w *sWriter) Write(b []byte) (int, error) {
	return w.w.Write(b)
}

func (w *sWriter) WriteHeader(statusCode int) {
	w.w.WriteHeader(statusCode)
}
