package dialer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	shadowsocks "github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
)

// Errors from this file never quote the proxy URL: it carries the
// Shadowsocks key or SOCKS5 password.

// newProxyDialer returns a dialer for an upstream "proxy" URL:
//
//	ss://METHOD:PASSWORD@HOST:PORT   SIP022 (password percent-encoded)
//	ss://BASE64(METHOD:PASSWORD)@HOST:PORT   SIP002
//	ss://BASE64(METHOD:PASSWORD@HOST:PORT)   legacy share link
//	socks5://[USER:PASS@]HOST:PORT
func newProxyDialer(dialer *net.Dialer, raw string) (Dialer, error) {
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok {
		return nil, errors.New("proxy must be a URL such as ss://… or socks5://…")
	}
	switch strings.ToLower(scheme) {
	case "ss":
		cfg, err := ParseShadowsocksURL(raw)
		if err != nil {
			return nil, err
		}
		return newShadowsocksDialer(dialer, cfg)
	case "socks5", "socks5h":
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return nil, errors.New("invalid socks5 proxy URL")
		}
		password, _ := u.User.Password()
		return newSocksDialer(dialer, u.Host, u.User.Username(), password)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q; use ss:// or socks5://", scheme)
	}
}

// ShadowsocksConfig is a parsed ss:// URL.
type ShadowsocksConfig struct {
	Method   string
	Password string
	Server   string // host:port
}

// shadowsocksMethods are the AEAD and 2022 ciphers. The stream ciphers and
// "none" that the library also offers are not accepted: they are not
// authenticated.
var shadowsocksMethods = slices.Concat(shadowaead_2022.List, shadowaead.List)

// ParseShadowsocksURL accepts the SIP022, SIP002 and legacy share forms.
func ParseShadowsocksURL(raw string) (ShadowsocksConfig, error) {
	invalid := errors.New("invalid shadowsocks URL")
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "ss") {
		return ShadowsocksConfig{}, invalid
	}
	if u.Query().Has("plugin") {
		return ShadowsocksConfig{}, errors.New("shadowsocks plugins are not supported")
	}
	var cfg ShadowsocksConfig
	switch {
	case u.User == nil:
		// Legacy: ss://BASE64(method:password@host:port)#tag
		decoded, ok := decodeBase64(u.Host + u.Path)
		if !ok {
			return ShadowsocksConfig{}, invalid
		}
		at := strings.LastIndex(decoded, "@")
		if at < 0 {
			return ShadowsocksConfig{}, invalid
		}
		cfg.Server = decoded[at+1:]
		cfg.Method, cfg.Password, ok = strings.Cut(decoded[:at], ":")
		if !ok {
			return ShadowsocksConfig{}, invalid
		}
	default:
		cfg.Server = u.Host
		if password, set := u.User.Password(); set {
			// SIP022: method and password in plain (percent-encoded) userinfo.
			cfg.Method, cfg.Password = u.User.Username(), password
		} else {
			// SIP002: userinfo is BASE64(method:password).
			decoded, ok := decodeBase64(u.User.Username())
			if !ok {
				return ShadowsocksConfig{}, invalid
			}
			cfg.Method, cfg.Password, ok = strings.Cut(decoded, ":")
			if !ok {
				return ShadowsocksConfig{}, invalid
			}
		}
	}
	cfg.Method = strings.ToLower(cfg.Method)
	if !slices.Contains(shadowsocksMethods, cfg.Method) {
		return ShadowsocksConfig{}, fmt.Errorf("unsupported shadowsocks method %q; use one of %s",
			cfg.Method, strings.Join(shadowsocksMethods, ", "))
	}
	if cfg.Password == "" {
		return ShadowsocksConfig{}, errors.New("shadowsocks password is empty")
	}
	if host, port, err := net.SplitHostPort(cfg.Server); err != nil || host == "" || port == "" {
		return ShadowsocksConfig{}, errors.New("shadowsocks server must be host:port")
	}
	return cfg, nil
}

func decodeBase64(s string) (string, bool) {
	s = strings.TrimRight(s, "=")
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}

// ShadowsocksDialer relays TCP and UDP through a Shadowsocks server, so
// every upstream protocol (UDP, TCP, DoT, DoH, DoQ, DoH3) works through it.
type ShadowsocksDialer struct {
	dialer *net.Dialer
	server string
	method shadowsocks.Method
}

func newShadowsocksDialer(dialer *net.Dialer, cfg ShadowsocksConfig) (*ShadowsocksDialer, error) {
	var (
		method shadowsocks.Method
		err    error
	)
	if slices.Contains(shadowaead_2022.List, cfg.Method) {
		method, err = shadowaead_2022.NewWithPassword(cfg.Method, cfg.Password, time.Now)
	} else {
		method, err = shadowaead.New(cfg.Method, nil, cfg.Password)
	}
	if err != nil {
		// The library's message may describe the key; keep only the method.
		return nil, fmt.Errorf("invalid shadowsocks key for %s; 2022 methods need a base64 key of the cipher's length", cfg.Method)
	}
	return &ShadowsocksDialer{dialer: dialer, server: cfg.Server, method: method}, nil
}

func (d *ShadowsocksDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	destination := M.ParseSocksaddr(addr)
	if !destination.IsValid() || destination.Port == 0 {
		return nil, fmt.Errorf("invalid destination %q", addr)
	}
	switch network {
	case "tcp":
		conn, err := d.dialer.DialContext(ctx, "tcp", d.server)
		if err != nil {
			return nil, fmt.Errorf("dial shadowsocks server: %w", err)
		}
		// The request header goes out with the first write; DNS clients
		// (TCP, TLS, HTTP) always write first.
		return d.method.DialEarlyConn(conn, destination), nil
	case "udp":
		conn, err := d.dialer.DialContext(ctx, "udp", d.server)
		if err != nil {
			return nil, fmt.Errorf("dial shadowsocks server: %w", err)
		}
		var remote net.Addr = destination
		if destination.IsIP() {
			remote = destination.UDPAddr()
		}
		// A net.Conn bound to the destination that is also a
		// net.PacketConn, as the QUIC upstreams require.
		bound := bufio.NewBindPacketConn(d.method.DialPacketConn(conn), remote)
		if udp, ok := conn.(*net.UDPConn); ok {
			return &shadowsocksPacketConn{BindPacketConn: bound, udp: udp}, nil
		}
		return bound, nil
	default:
		return nil, fmt.Errorf("unsupported network type: %s", network)
	}
}

// shadowsocksPacketConn lets QUIC size the real socket's buffers. It must
// not expose SyscallConn: QUIC would then treat it as a raw UDP socket and
// send without the Shadowsocks framing.
type shadowsocksPacketConn struct {
	bufio.BindPacketConn
	udp *net.UDPConn
}

func (c *shadowsocksPacketConn) SetReadBuffer(bytes int) error {
	return c.udp.SetReadBuffer(bytes)
}

func (c *shadowsocksPacketConn) SetWriteBuffer(bytes int) error {
	return c.udp.SetWriteBuffer(bytes)
}
