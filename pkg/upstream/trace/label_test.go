package trace

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"
)

func TestDisplayNameHidesPrivateServers(t *testing.T) {
	tests := []struct {
		addr, label, want string
	}{
		{"223.5.5.5:53", "", "223.5.5.5 (UDP)"},
		{"223.5.5.5", "", "223.5.5.5 (UDP)"},
		{"tcp://119.29.29.29", "", "119.29.29.29 (TCP)"},
		{"https://dns.alidns.com/dns-query", "", "dns.alidns.com (DoH)"},
		{"https://DNS.Google./dns-query", "", "dns.google (DoH)"},
		{"tls://[2606:4700:4700::1111]:853", "", "2606:4700:4700::1111 (DoT)"},
		{"h3://cloudflare-dns.com/dns-query", "", "cloudflare-dns.com (DoH3)"},
		// A private DoH host, a subdomain of a public service, a path token and
		// a private IP must never appear.
		{"https://hk.pro.xns.one/fRjim30bFUc/dns-query", "", "forward_remote #2 (DoH)"},
		{"https://12345.alidns.com/dns-query", "", "forward_remote #2 (DoH)"},
		{"10.0.0.53:53", "", "forward_remote #2 (UDP)"},
		{"quic://dns.example.net", "", "forward_remote #2 (DoQ)"},
		{"https://hk.pro.xns.one/token/dns-query", "  香港私有 DoH ", "香港私有 DoH"},
	}
	for _, test := range tests {
		got := DisplayName("forward_remote", 1, test.addr, test.label)
		if got != test.want {
			t.Errorf("DisplayName(%q, %q) = %q, want %q", test.addr, test.label, got, test.want)
		}
		if test.label == "" && strings.Contains(test.want, "#") {
			for _, secret := range []string{"xns.one", "fRjim", "12345", "10.0.0.53", "example.net"} {
				if strings.Contains(got, secret) {
					t.Errorf("DisplayName(%q) leaked %q: %q", test.addr, secret, got)
				}
			}
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestErrorKindCategorizesWithoutTheMessage(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 443}, Err: errors.New("no route to host")}
	tests := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{context.Canceled, ErrorCanceled},
		{fmt.Errorf("exchange: %w", context.DeadlineExceeded), ErrorTimeout},
		{&net.OpError{Op: "read", Err: timeoutError{}}, ErrorTimeout},
		{fmt.Errorf("handshake: %w", x509.UnknownAuthorityError{}), ErrorTLS},
		{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, ErrorRefused},
		{fmt.Errorf("read: %w", syscall.ECONNRESET), ErrorReset},
		{&net.DNSError{Err: "no such host", Name: "private.example"}, ErrorResolve},
		{dial, ErrorConnect},
		{errors.New("unexpected status 502: 502 Bad Gateway"), ErrorHTTPStatus},
		{errors.New("unexpected content type: text/html"), ErrorBadResponse},
		{errors.New("something else"), ErrorOther},
	}
	for _, test := range tests {
		if got := ErrorKind(test.err); got != test.want {
			t.Errorf("ErrorKind(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}
