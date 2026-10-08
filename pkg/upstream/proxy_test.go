package upstream

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	C "github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server"
	shadowsocks "github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// relay is a Shadowsocks server handler that forwards to the destination
// and counts what it relayed.
type relay struct {
	tcp, udp atomic.Int32
}

func (r *relay) NewConnection(ctx context.Context, conn net.Conn, metadata M.Metadata) error {
	r.tcp.Add(1)
	remote, err := net.Dial("tcp", metadata.Destination.String())
	if err != nil {
		conn.Close()
		return err
	}
	return bufio.CopyConn(ctx, conn, remote)
}

func (r *relay) NewPacketConnection(ctx context.Context, conn N.PacketConn, _ M.Metadata) error {
	r.udp.Add(1)
	remote, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		conn.Close()
		return err
	}
	return bufio.CopyPacketConn(ctx, conn, bufio.NewPacketConn(remote))
}

func (r *relay) NewError(context.Context, error) {}

// newShadowsocksServer serves method/password on one TCP and UDP port.
func newShadowsocksServer(t *testing.T, method, password string) (string, *relay) {
	t.Helper()
	r := new(relay)
	var (
		service shadowsocks.Service
		err     error
	)
	if strings.HasPrefix(method, "2022-") {
		service, err = shadowaead_2022.NewServiceWithPassword(method, password, 60, r, time.Now)
	} else {
		service, err = shadowaead.NewService(method, nil, password, 60, r)
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var (
		listener net.Listener
		packet   net.PacketConn
	)
	for range 10 { // the UDP port of the same number may be taken
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		packet, err = net.ListenPacket("udp", listener.Addr().String())
		if err == nil {
			break
		}
		listener.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close(); packet.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go service.NewConnection(ctx, conn, M.Metadata{Source: M.SocksaddrFromNet(conn.RemoteAddr())})
		}
	}()
	go func() {
		server := bufio.NewPacketConn(packet)
		for {
			buffer := buf.NewPacket()
			source, err := server.ReadPacket(buffer)
			if err != nil {
				buffer.Release()
				return
			}
			_ = service.NewPacket(ctx, server, buffer, M.Metadata{Source: source})
		}
	}()
	return listener.Addr().String(), r
}

func TestUpstreamThroughShadowsocks(t *testing.T) {
	key := make([]byte, 16)
	rand.Read(key)
	ssKey := base64.StdEncoding.EncodeToString(key)
	server2022, relay2022 := newShadowsocksServer(t, "2022-blake3-aes-128-gcm", ssKey)
	// SIP022: the key's '+', '/' and '=' are percent-encoded by url.UserPassword.
	proxy2022 := (&url.URL{Scheme: "ss", User: url.UserPassword("2022-blake3-aes-128-gcm", ssKey), Host: server2022}).String()

	serverAEAD, relayAEAD := newShadowsocksServer(t, "aes-256-gcm", "legacy-password")
	proxyAEAD := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:legacy-password")) + "@" + serverAEAD

	cases := []struct {
		scheme, proxy string
		relay         *relay
		udp           bool
	}{
		{"udp", proxy2022, relay2022, true},
		{"tcp", proxy2022, relay2022, false},
		{"tls", proxy2022, relay2022, false},
		{"tcp", proxyAEAD, relayAEAD, false},
	}
	for _, c := range cases {
		addr, shutdown := m[c.scheme](t, &vServer{})
		tcpBefore, udpBefore := c.relay.tcp.Load(), c.relay.udp.Load()
		u, err := NewUpstream(c.scheme+"://"+addr, &Opt{Proxy: c.proxy, Insecure: true, IdleTimeout: -1})
		if err != nil {
			t.Fatalf("%s: %v", c.scheme, err)
		}
		if err := testUpstream(u); err != nil {
			t.Fatalf("%s through %s: %v", c.scheme, strings.SplitN(c.proxy, ":", 3)[1], err)
		}
		u.Close()
		shutdown()
		if c.udp && c.relay.udp.Load() == udpBefore {
			t.Fatalf("%s: no UDP session reached the Shadowsocks server", c.scheme)
		}
		if !c.udp && c.relay.tcp.Load() == tcpBefore {
			t.Fatalf("%s: no TCP connection reached the Shadowsocks server", c.scheme)
		}
	}
}

type replyHandler struct{}

func (replyHandler) ServeDNS(_ context.Context, req *dns.Msg, _ *C.RequestMeta) (*dns.Msg, error) {
	return new(dns.Msg).SetReply(req), nil
}

// DoQ runs QUIC over the Shadowsocks UDP relay, which needs the dialer's
// UDP conn to work as a net.PacketConn.
func TestDoQThroughShadowsocks(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	ssKey := base64.StdEncoding.EncodeToString(key)
	ssServer, r := newShadowsocksServer(t, "2022-blake3-chacha20-poly1305", ssKey)
	proxy := (&url.URL{Scheme: "ss", User: url.UserPassword("2022-blake3-chacha20-poly1305", ssKey), Host: ssServer}).String()

	s := server.NewServer(server.ServerOpts{DNSHandler: replyHandler{}, Cert: "../server/testdata/test.test.cert", Key: "../server/testdata/test.test.key"})
	defer s.Close()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.OwnCloser(pc)
	l, err := s.CreateQUICListner(pc, []string{"doq"})
	if err != nil {
		t.Fatal(err)
	}
	go s.ServeQUIC(l)

	u, err := NewUpstream("quic://"+pc.LocalAddr().String(), &Opt{Proxy: proxy, Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if err := testUpstream(u); err != nil {
		t.Fatal(err)
	}
	if r.udp.Load() == 0 {
		t.Fatal("no UDP session reached the Shadowsocks server")
	}
}

func TestUpstreamProxyErrorHidesKey(t *testing.T) {
	_, err := NewUpstream("udp://192.0.2.1", &Opt{Proxy: "ss://2022-blake3-aes-128-gcm:dG9vLXNob3J0@192.0.2.2:8388"})
	if err == nil || strings.Contains(err.Error(), "dG9vLXNob3J0") {
		t.Fatalf("bad key: %v", err)
	}
}
