package fastforward

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestUDPMEDetailedCapturesInternalOPTBoundaries(t *testing.T) {
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{
		PacketConn: packetConn,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
			opt := q.IsEdns0()
			if opt == nil || opt.UDPSize() != 512 {
				t.Errorf("wire request OPT = %+v", opt)
			}
			r := new(dns.Msg)
			r.SetReply(q)
			r.SetEdns0(1232, false)
			_ = w.WriteMsg(r)
		}),
	}
	go func() { _ = server.ActivateAndServe() }()
	defer server.Shutdown()

	upstream := newUDPME(packetConn.LocalAddr().String(), true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := upstream.ExchangeDetailed(ctx, new(dns.Msg).SetQuestion("example.org.", dns.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestEDNS == nil || !result.RequestEDNS.Present || result.RequestEDNS.UDPSize != 512 {
		t.Fatalf("request snapshot = %+v", result.RequestEDNS)
	}
	if result.ResponseEDNS == nil || !result.ResponseEDNS.Present || result.ResponseEDNS.UDPSize != 1232 {
		t.Fatalf("response snapshot = %+v", result.ResponseEDNS)
	}
	if result.Response == nil || result.Response.IsEdns0() != nil {
		t.Fatalf("post-processed response retained OPT: %+v", result.Response)
	}
}
