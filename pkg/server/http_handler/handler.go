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

package http_handler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/pool"
	C "github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/dns_handler"
	"github.com/pmkol/mosdns-x/pkg/server/query_access"
)

var nopLogger = zap.NewNop()

const (
	defaultPath        = "/dns-query"
	maxCredentialLen   = 512
	maxDNSMessageSize  = 65535
	maxEncodedDNSQuery = (maxDNSMessageSize*8 + 5) / 6
)

type HandlerOpts struct {
	// DNSHandler is required.
	DNSHandler dns_handler.Handler

	// Path specifies the query endpoint. If it is empty, Handler
	// will ignore the request path.
	Path string

	// SrcIPHeader specifies the sole header containing the client source address.
	// If empty, only X-Forwarded-For is considered. Headers are read only from
	// peers matched by TrustedProxies.
	SrcIPHeader string

	// TrustedProxies controls which socket peers may supply client IP headers.
	TrustedProxies []netip.Prefix

	// Authenticate validates a path or Bearer credential. Nil disables access
	// protection and preserves the original path behavior.
	Authenticate func(context.Context, string) (C.Principal, error)

	UpstreamObserver C.UpstreamObserver

	// Logger specifies the logger which Handler writes its log to.
	// Default is a nop logger.
	Logger *zap.Logger
}

func (opts *HandlerOpts) Init() error {
	if opts.DNSHandler == nil {
		return errors.New("nil dns handler")
	}
	if opts.Logger == nil {
		opts.Logger = nopLogger
	}
	return nil
}

type Handler struct {
	opts HandlerOpts
}

func NewHandler(opts HandlerOpts) (*Handler, error) {
	if err := opts.Init(); err != nil {
		return nil, err
	}
	return &Handler{opts: opts}, nil
}

func (h *Handler) warnErr(req Request, message string) {
	h.opts.Logger.Warn(message, zap.String("from", req.GetRemoteAddr()))
}

type ResponseWriter interface {
	Header() Header
	Write([]byte) (int, error)
	WriteHeader(statusCode int)
}

type Header interface {
	Get(key string) string
	Set(key string, value string)
}

type Request interface {
	URL() *url.URL
	TLS() *TlsInfo
	Body() io.ReadCloser
	Header() Header
	Method() string
	Context() context.Context
	RequestURI() string
	GetRemoteAddr() string
	SetRemoteAddr(addr string)
}

type TlsInfo struct {
	Version            uint16
	ServerName         string
	NegotiatedProtocol string
}

func (h *Handler) ServeHTTP(w ResponseWriter, req Request) {
	protected := h.opts.Authenticate != nil
	if protected {
		w.Header().Set("Cache-Control", "private, no-store")
	}

	// get remote addr from header and request
	meta := new(C.RequestMeta)
	meta.SetUpstreamObserver(h.opts.UpstreamObserver)
	if addr, err := getRemoteAddr(req, h.opts.SrcIPHeader, h.opts.TrustedProxies); err == nil {
		meta.SetClientAddr(addr)
	}

	if tlsInfo := req.TLS(); tlsInfo != nil {
		meta.SetServerName(tlsInfo.ServerName)
		switch tlsInfo.NegotiatedProtocol {
		case http3.NextProtoH3:
			meta.SetProtocol(C.ProtocolH3)
		case "h2":
			meta.SetProtocol(C.ProtocolH2)
		default:
			meta.SetProtocol(C.ProtocolHTTPS)
		}
	} else {
		meta.SetProtocol(C.ProtocolHTTP)
	}

	basePath := h.opts.Path
	if protected && basePath == "" {
		basePath = defaultPath
	}
	credential, credentialInPath, credentialErr := pathCredential(req.URL().Path, basePath, protected)
	if credentialErr != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("invalid request path"))
		h.warnErr(req, "invalid request path")
		return
	}
	if protected {
		headerCredential, hasHeader, err := bearerCredential(req.Header().Get("Authorization"))
		if err != nil || (credentialInPath && hasHeader) || (!credentialInPath && !hasHeader) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mosdns"`)
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("invalid credential"))
			h.warnErr(req, "invalid credential")
			return
		}
		if hasHeader {
			credential = headerCredential
		}
		principal, err := h.opts.Authenticate(req.Context(), credential)
		if err != nil {
			status := query_access.HTTPStatus(err)
			if status == http.StatusUnauthorized {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mosdns"`)
			}
			w.WriteHeader(status)
			w.Write([]byte("request rejected"))
			h.warnErr(req, "authentication failed")
			return
		}
		meta.SetPrincipal(principal)
	}

	var b []byte
	var err error

	switch req.Method() {
	case http.MethodGet:
		accept := req.Header().Get("Accept")
		var matched bool
		if accept == "" {
			matched = true
		}
		for _, v := range strings.Split(accept, ",") {
			mediatype := strings.TrimSpace(strings.SplitN(v, ";", 2)[0])
			if mediatype == "application/dns-message" || mediatype == "*/*" {
				matched = true
				break
			}
		}
		if !matched {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid Accept header"))
			h.warnErr(req, "invalid Accept header")
			return
		}

		s := req.URL().Query().Get("dns")
		if len(s) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("no dns param"))
			h.warnErr(req, "no dns param")
			return
		}
		if len(s) > maxEncodedDNSQuery {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			w.Write([]byte("dns message too large"))
			h.warnErr(req, "dns message too large")
			return
		}

		b, err = base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid dns param"))
			h.warnErr(req, "decode base64 query failed")
			return
		}
	case http.MethodPost:
		if contentType := req.Header().Get("Content-Type"); contentType != "application/dns-message" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid Content-Type header"))
			h.warnErr(req, "invalid Content-Type header")
			return
		}

		b, err = io.ReadAll(io.LimitReader(req.Body(), maxDNSMessageSize+1))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid request body"))
			h.warnErr(req, "read request body failed")
			return
		}
		if len(b) > maxDNSMessageSize {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			w.Write([]byte("dns message too large"))
			h.warnErr(req, "dns message too large")
			return
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("invalid request method"))
		h.warnErr(req, "invalid method")
		return
	}

	// read msg
	m := new(dns.Msg)
	if err := m.Unpack(b); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid request message"))
		h.warnErr(req, "unpack request failed")
		return
	}

	if m.Id != 0 {
		h.opts.Logger.Debug(fmt.Sprintf("irregular message id: %d", m.Id))
	}

	r, err := h.opts.DNSHandler.ServeDNS(req.Context(), m, meta)
	if err != nil {
		status := query_access.HTTPStatus(err)
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mosdns"`)
		}
		w.WriteHeader(status)
		w.Write([]byte("request rejected"))
		h.warnErr(req, "handle response failed")
		return
	}

	b, buf, err := pool.PackBuffer(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("pack response failed"))
		h.warnErr(req, "pack response failed")
		return
	}
	defer buf.Release()

	w.Header().Set("Content-Type", "application/dns-message")
	if !protected {
		w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", dnsutils.GetMinimalTTL(r)))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(b); err != nil {
		h.warnErr(req, "write response failed")
		return
	}
}

func getRemoteAddr(req Request, customHeader string, trusted []netip.Prefix) (netip.Addr, error) {
	addrport, err := netip.ParseAddrPort(req.GetRemoteAddr())
	if err != nil {
		return netip.Addr{}, err
	}
	peer := addrport.Addr().Unmap()
	if !prefixContains(trusted, peer) {
		return peer, nil
	}
	name := customHeader
	if name == "" {
		name = "X-Forwarded-For"
	}
	name = http.CanonicalHeaderKey(name)
	value := req.Header().Get(name)
	if value == "" {
		return peer, nil
	}
	if name == "X-Forwarded-For" {
		addr, err := clientFromXFF(value, peer, trusted)
		if err != nil {
			return peer, nil
		}
		return addr, nil
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return peer, nil
	}
	return addr.Unmap(), nil
}

func prefixContains(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func clientFromXFF(value string, peer netip.Addr, trusted []netip.Prefix) (netip.Addr, error) {
	parts := strings.Split(value, ",")
	addrs := make([]netip.Addr, len(parts))
	for i, part := range parts {
		addr, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return netip.Addr{}, err
		}
		addrs[i] = addr.Unmap()
	}
	current := peer
	for i := len(addrs) - 1; i >= 0 && prefixContains(trusted, current); i-- {
		current = addrs[i]
	}
	return current, nil
}

func pathCredential(path, base string, protected bool) (string, bool, error) {
	if !protected {
		if base != "" && path != base {
			return "", false, errors.New("invalid path")
		}
		return "", false, nil
	}
	if path == base {
		return "", false, nil
	}
	if len(path) > len(base)+1+maxCredentialLen || !strings.HasPrefix(path, base+"/") {
		return "", false, errors.New("invalid path")
	}
	credential := strings.TrimPrefix(path, base+"/")
	if !validCredential(credential) || strings.Contains(credential, "/") {
		return "", false, errors.New("invalid credential path")
	}
	return credential, true, nil
}

func bearerCredential(value string) (string, bool, error) {
	if value == "" {
		return "", false, nil
	}
	if len(value) < len("Bearer ") || len(value) > len("Bearer ")+maxCredentialLen || !strings.EqualFold(value[:len("Bearer ")], "Bearer ") {
		return "", true, errors.New("invalid bearer credential")
	}
	credential := value[len("Bearer "):]
	if !validCredential(credential) || strings.ContainsAny(credential, " \t\r\n") {
		return "", true, errors.New("invalid bearer credential")
	}
	return credential, true, nil
}

func validCredential(credential string) bool {
	if len(credential) == 0 || len(credential) > maxCredentialLen {
		return false
	}
	dot := strings.IndexByte(credential, '.')
	return dot > 0 && dot == strings.LastIndexByte(credential, '.') && dot < len(credential)-1
}
