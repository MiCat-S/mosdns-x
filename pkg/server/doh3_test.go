package server

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	C "github.com/pmkol/mosdns-x/pkg/query_context"
	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
	H "github.com/pmkol/mosdns-x/pkg/server/http_handler"
)

// blockingDNSHandler answers once release is closed.
type blockingDNSHandler struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingDNSHandler) ServeDNS(_ context.Context, req *dns.Msg, _ *C.RequestMeta) (*dns.Msg, error) {
	close(b.entered)
	<-b.release
	r := new(dns.Msg)
	r.SetReply(req)
	return r, nil
}

func startDoH3(t *testing.T, dnsHandler D.Handler) (*Server, string) {
	t.Helper()
	h, err := H.NewHandler(H.HandlerOpts{DNSHandler: dnsHandler, Path: "/dns-query"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(ServerOpts{HttpHandler: h, Cert: "testdata/test.test.cert", Key: "testdata/test.test.key"})
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.CreateQUICListner(conn, []string{"h3"})
	if err != nil {
		t.Fatal(err)
	}
	go s.ServeH3(l)
	t.Cleanup(func() { s.Close() })
	return s, conn.LocalAddr().String()
}

func doh3URL(addr string) string {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	b, _ := q.Pack()
	return "https://" + addr + "/dns-query?dns=" + base64.RawURLEncoding.EncodeToString(b)
}

// A client may keep its connection after GOAWAY until its own idle timeout.
// Shutdown must not wait for it once no query is in flight.
func TestDoH3ShutdownClosesIdleConnections(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)
	s, addr := startDoH3(t, &D.DummyServerHandler{WantMsg: m})
	qc, err := quic.DialAddrEarly(context.Background(), addr,
		&tls.Config{InsecureSkipVerify: true, ServerName: "test.test", NextProtos: []string{"h3"}}, &quic.Config{KeepAlivePeriod: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer qc.CloseWithError(0, "")
	time.Sleep(100 * time.Millisecond) // let the server accept the connection

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if d := time.Since(start); d > h3IdleGrace+time.Second {
		t.Fatalf("shutdown waited %v for an idle connection", d)
	}
	select {
	case <-qc.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("idle connection was left open")
	}
}

func TestDoH3ShutdownWaitsForInflightQuery(t *testing.T) {
	b := &blockingDNSHandler{entered: make(chan struct{}), release: make(chan struct{})}
	s, addr := startDoH3(t, b)
	client := &http.Client{Transport: &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "test.test"}}}
	result := make(chan error, 1)
	go func() {
		result <- func() error {
			resp, err := client.Get(doh3URL(addr))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("read body: %w", err)
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("status %d", resp.StatusCode)
			}
			r := new(dns.Msg)
			if err := r.Unpack(body); err != nil {
				return fmt.Errorf("unpack %d byte response: %w", len(body), err)
			}
			if !r.Response || r.Rcode != dns.RcodeSuccess || len(r.Question) != 1 || r.Question[0].Name != "example.com." {
				return fmt.Errorf("unexpected response %v", r)
			}
			return nil
		}()
	}()
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("query did not reach the handler")
	}

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- s.Shutdown(ctx)
	}()
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before the in-flight query ended: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(b.release)
	if err := <-result; err != nil {
		t.Fatalf("in-flight query did not get its complete answer: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after the query ended")
	}
}
