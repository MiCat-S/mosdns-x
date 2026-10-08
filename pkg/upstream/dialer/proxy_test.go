package dialer

import (
	"encoding/base64"
	"net"
	"strings"
	"testing"
)

func TestParseShadowsocksURL(t *testing.T) {
	key := "AAECAwQFBgcICQoLDA0ODw==" // 16 bytes, base64 with '='
	sip002 := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pa:ss"))
	legacy := base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:p@ss@ss.example.net:8389"))
	tests := []struct {
		name, raw string
		want      ShadowsocksConfig
	}{
		{"SIP022 percent-encoded", "ss://2022-blake3-aes-128-gcm:" + strings.ReplaceAll(key, "=", "%3D") + "@203.0.113.7:8388#hk",
			ShadowsocksConfig{"2022-blake3-aes-128-gcm", key, "203.0.113.7:8388"}},
		{"SIP022 multi-user keys", "ss://2022-blake3-aes-128-gcm:k1%3Ak2@[2001:db8::1]:443",
			ShadowsocksConfig{"2022-blake3-aes-128-gcm", "k1:k2", "[2001:db8::1]:443"}},
		{"SIP002 base64 userinfo", "ss://" + sip002 + "@ss.example.net:8388/?#tag",
			ShadowsocksConfig{"aes-256-gcm", "pa:ss", "ss.example.net:8388"}},
		{"legacy whole link", "ss://" + legacy + "#name",
			ShadowsocksConfig{"chacha20-ietf-poly1305", "p@ss", "ss.example.net:8389"}},
		{"method is case-insensitive", "ss://AES-128-GCM:secret@192.0.2.1:1",
			ShadowsocksConfig{"aes-128-gcm", "secret", "192.0.2.1:1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseShadowsocksURL(tt.raw)
			if err != nil {
				t.Fatalf("ParseShadowsocksURL: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseShadowsocksURLRejects(t *testing.T) {
	const secret = "hunter2-secret"
	for _, raw := range []string{
		"ss://rc4-md5:" + secret + "@192.0.2.1:8388",                        // stream cipher
		"ss://none:" + secret + "@192.0.2.1:8388",                           // plaintext
		"ss://aes-128-gcm:" + secret + "@192.0.2.1",                         // no port
		"ss://aes-128-gcm:@192.0.2.1:8388",                                  // no password
		"ss://aes-128-gcm:" + secret + "@192.0.2.1:8388/?plugin=obfs-local", // plugin
		"ss://%%%" + secret,
	} {
		_, err := ParseShadowsocksURL(raw)
		if err == nil {
			t.Fatalf("%q was accepted", raw)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error quotes the password: %v", err)
		}
	}
}

func TestNewDialerProxy(t *testing.T) {
	base := &net.Dialer{}
	d, err := NewDialer(DialerOpts{Dialer: base, Proxy: "socks5://u:p@127.0.0.1:1080"})
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := d.(*SocksDialer); !ok || s.username != "u" || s.password != "p" {
		t.Fatalf("socks5 proxy URL gave %#v", d)
	}
	if _, err := NewDialer(DialerOpts{Dialer: base, Proxy: "ss://2022-blake3-aes-128-gcm:AAECAwQFBgcICQoLDA0ODw%3D%3D@127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDialer(DialerOpts{Dialer: base, Proxy: "socks5://127.0.0.1:1", SocksAddr: "127.0.0.1:1"}); err == nil {
		t.Fatal("proxy and socks5 together were accepted")
	}
	if _, err := NewDialer(DialerOpts{Dialer: base, Proxy: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("http proxy was accepted")
	}
	// A 2022 key of the wrong length fails without quoting it.
	_, err = NewDialer(DialerOpts{Dialer: base, Proxy: "ss://2022-blake3-aes-256-gcm:c2hvcnQta2V5@127.0.0.1:1"})
	if err == nil || strings.Contains(err.Error(), "c2hvcnQta2V5") {
		t.Fatalf("short 2022 key: %v", err)
	}
}
