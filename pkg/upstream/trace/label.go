package trace

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// publicResolvers lists well-known public DNS services, by IP or exact DoH,
// DoT or DoQ host. Only these upstreams are shown by address in query logs;
// any other server is private and is named by its plugin tag instead.
var publicResolvers = map[string]struct{}{
	// AliDNS
	"223.5.5.5": {}, "223.6.6.6": {}, "2400:3200::1": {}, "2400:3200:baba::1": {}, "dns.alidns.com": {},
	// DNSPod / Tencent
	"119.29.29.29": {}, "119.28.28.28": {}, "182.254.116.116": {}, "1.12.12.12": {}, "120.53.53.53": {},
	"2402:4e00::": {}, "doh.pub": {}, "dot.pub": {}, "sm2.doh.pub": {},
	// 114DNS
	"114.114.114.114": {}, "114.114.115.115": {},
	// Baidu, CNNIC, 360
	"180.76.76.76": {}, "2400:da00::6666": {}, "1.2.4.8": {}, "210.2.4.8": {}, "101.226.4.6": {}, "218.30.118.6": {},
	"doh.360.cn": {}, "dot.360.cn": {},
	// Google
	"8.8.8.8": {}, "8.8.4.4": {}, "2001:4860:4860::8888": {}, "2001:4860:4860::8844": {}, "dns.google": {}, "dns.google.com": {},
	// Cloudflare
	"1.1.1.1": {}, "1.0.0.1": {}, "2606:4700:4700::1111": {}, "2606:4700:4700::1001": {},
	"cloudflare-dns.com": {}, "one.one.one.one": {}, "1dot1dot1dot1.cloudflare-dns.com": {},
	// Quad9
	"9.9.9.9": {}, "149.112.112.112": {}, "2620:fe::fe": {}, "2620:fe::9": {}, "dns.quad9.net": {}, "dns9.quad9.net": {},
	// OpenDNS
	"208.67.222.222": {}, "208.67.220.220": {}, "doh.opendns.com": {},
	// AdGuard
	"94.140.14.14": {}, "94.140.15.15": {}, "dns.adguard-dns.com": {}, "dns.adguard.com": {},
}

// ProtocolName reports an upstream address's protocol as shown to users.
func ProtocolName(addr string) string {
	scheme := "udp"
	if i := strings.Index(addr, "://"); i >= 0 {
		scheme = strings.ToLower(addr[:i])
	}
	switch scheme {
	case "", "udp":
		return "UDP"
	case "udpme":
		return "UDP"
	case "tcp":
		return "TCP"
	case "dot", "tls":
		return "DoT"
	case "doq", "quic":
		return "DoQ"
	case "http", "https", "h2", "doh":
		return "DoH"
	case "h3", "doh3":
		return "DoH3"
	default:
		return strings.ToUpper(scheme)
	}
}

// publicHost returns the host of addr when it is a listed public resolver.
func publicHost(addr string) (string, bool) {
	raw := addr
	if !strings.Contains(raw, "://") {
		raw = "udp://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	}
	if _, ok := publicResolvers[host]; !ok {
		return "", false
	}
	return host, true
}

// DisplayName is how a query log names upstream index (zero based) of plugin
// tag. A configured label wins. Otherwise a listed public resolver shows its
// address, and any other server shows only the plugin and position, so a
// private server's host, IP or URL token never reaches the log.
func DisplayName(tag string, index int, addr, label string) string {
	if label = strings.TrimSpace(label); label != "" {
		return label
	}
	protocol := ProtocolName(addr)
	if host, ok := publicHost(addr); ok {
		return fmt.Sprintf("%s (%s)", host, protocol)
	}
	return fmt.Sprintf("%s #%d (%s)", tag, index+1, protocol)
}

// Error categories recorded for failed upstream attempts.
const (
	ErrorCanceled      = "canceled"
	ErrorTimeout       = "timeout"
	ErrorTLS           = "tls"
	ErrorRefused       = "connection_refused"
	ErrorReset         = "connection_reset"
	ErrorConnect       = "connect_failed"
	ErrorResolve       = "resolve_failed"
	ErrorHTTPStatus    = "http_status"
	ErrorBadResponse   = "bad_response"
	ErrorOther         = "error"
	ErrorEmptyResponse = "empty_response"
)

// ErrorKind classifies an upstream error. Raw errors often carry the server's
// address, so only the category is recorded.
func ErrorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCanceled
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ETIMEDOUT) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return ErrorTimeout
	}
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalid          x509.CertificateInvalidError
		verification     *tls.CertificateVerificationError
		recordHeader     tls.RecordHeaderError
		alert            tls.AlertError
	)
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) ||
		errors.As(err, &verification) || errors.As(err, &recordHeader) || errors.As(err, &alert) {
		return ErrorTLS
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ErrorRefused
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return ErrorReset
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ErrorResolve
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return ErrorConnect
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "unexpected status"):
		return ErrorHTTPStatus
	case strings.HasPrefix(message, "unexpected content type"), strings.HasPrefix(message, "empty response"):
		return ErrorBadResponse
	case strings.Contains(message, "tls:"), strings.Contains(message, "x509:"), strings.Contains(message, "CRYPTO_ERROR"):
		return ErrorTLS
	}
	return ErrorOther
}
