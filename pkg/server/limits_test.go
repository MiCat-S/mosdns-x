package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/quic-go/quic-go"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	C "github.com/pmkol/mosdns-x/pkg/query_context"
	H "github.com/pmkol/mosdns-x/pkg/server/http_handler"
)

// gatedHandler blocks every query until release is closed or, unless
// ignoreCtx is set, the query context is cancelled.
type gatedHandler struct {
	entered   chan struct{}
	release   chan struct{}
	ignoreCtx bool
	served    atomic.Int64
}

func newGatedHandler() *gatedHandler {
	return &gatedHandler{entered: make(chan struct{}, 1024), release: make(chan struct{})}
}

func (h *gatedHandler) ServeDNS(ctx context.Context, req *dns.Msg, _ *C.RequestMeta) (*dns.Msg, error) {
	h.entered <- struct{}{}
	if h.ignoreCtx {
		<-h.release
		h.served.Add(1)
		return new(dns.Msg).SetReply(req), nil
	}
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h.served.Add(1)
	return new(dns.Msg).SetReply(req), nil
}

func (h *gatedHandler) waitEntered(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-h.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d queries reached the handler", i, n)
		}
	}
}

func counterValue(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 16)
	c.Collect(ch)
	close(ch)
	var sum float64
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatal(err)
		}
		switch {
		case pb.Counter != nil:
			sum += pb.Counter.GetValue()
		case pb.Gauge != nil:
			sum += pb.Gauge.GetValue()
		}
	}
	return sum
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func startUDP(t *testing.T, s *Server) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.ServeUDP(c)
	return c.LocalAddr().String()
}

func startTCP(t *testing.T, s *Server) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.ServeTCP(l)
	return l.Addr().String()
}

func newQuery(id uint16) *dns.Msg {
	q := new(dns.Msg).SetQuestion("example.com.", dns.TypeA)
	q.Id = id
	return q
}

func dialUDP(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func sendUDP(t *testing.T, c net.Conn, id uint16) {
	t.Helper()
	b, err := newQuery(id).Pack()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

func readUDP(c net.Conn, timeout time.Duration) (*dns.Msg, error) {
	c.SetReadDeadline(time.Now().Add(timeout))
	b := make([]byte, 4096)
	n, err := c.Read(b)
	if err != nil {
		return nil, err
	}
	r := new(dns.Msg)
	return r, r.Unpack(b[:n])
}

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func sendTCP(t *testing.T, c net.Conn, id uint16) {
	t.Helper()
	if _, err := dnsutils.WriteMsgToTCP(c, newQuery(id)); err != nil {
		t.Fatal(err)
	}
}

func readTCP(c net.Conn, timeout time.Duration) (*dns.Msg, error) {
	c.SetReadDeadline(time.Now().Add(timeout))
	r, _, err := dnsutils.ReadMsgFromTCP(c)
	return r, err
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func TestMaxConcurrentQueriesUDP(t *testing.T) {
	h := newGatedHandler()
	s := NewServer(ServerOpts{DNSHandler: h, MaxConcurrentQueries: 2})
	defer s.Close()
	addr := startUDP(t, s)

	c1, c2, c3 := dialUDP(t, addr), dialUDP(t, addr), dialUDP(t, addr)
	sendUDP(t, c1, 1)
	sendUDP(t, c2, 2)
	h.waitEntered(t, 2)

	sendUDP(t, c3, 3)
	rejected := s.metrics.rejectedQueries.WithLabelValues(C.ProtocolUDP)
	waitFor(t, "udp rejection", func() bool { return counterValue(t, rejected) == 1 })
	if v := counterValue(t, s.metrics.inflightQueries); v != 2 {
		t.Fatalf("inflight gauge = %v, want 2", v)
	}

	close(h.release)
	for i, c := range []net.Conn{c1, c2} {
		if _, err := readUDP(c, 2*time.Second); err != nil {
			t.Fatalf("query %d: %v", i+1, err)
		}
	}
	if _, err := readUDP(c3, 200*time.Millisecond); !isTimeout(err) {
		t.Fatalf("rejected query got a response or unexpected error: %v", err)
	}
	if got := h.served.Load(); got != 2 {
		t.Fatalf("handler served %d queries, want 2", got)
	}

	// Slots are released once the handlers return.
	waitFor(t, "inflight drain", func() bool { return counterValue(t, s.metrics.inflightQueries) == 0 })
	c4 := dialUDP(t, addr)
	sendUDP(t, c4, 4)
	if _, err := readUDP(c4, 2*time.Second); err != nil {
		t.Fatalf("query after release: %v", err)
	}
}

func TestMaxConcurrentQueriesTCP(t *testing.T) {
	h := newGatedHandler()
	s := NewServer(ServerOpts{DNSHandler: h, MaxConcurrentQueries: 2})
	defer s.Close()
	addr := startTCP(t, s)

	c1, c2 := dialTCP(t, addr), dialTCP(t, addr)
	sendTCP(t, c1, 1)
	sendTCP(t, c2, 2)
	h.waitEntered(t, 2)

	c3 := dialTCP(t, addr)
	sendTCP(t, c3, 3)
	if _, err := readTCP(c3, 2*time.Second); err == nil || isTimeout(err) {
		t.Fatalf("expected connection to be closed, got %v", err)
	}
	if v := counterValue(t, s.metrics.rejectedQueries.WithLabelValues(C.ProtocolTCP)); v != 1 {
		t.Fatalf("rejected tcp queries = %v, want 1", v)
	}

	close(h.release)
	for i, c := range []net.Conn{c1, c2} {
		if _, err := readTCP(c, 2*time.Second); err != nil {
			t.Fatalf("query %d: %v", i+1, err)
		}
	}

	waitFor(t, "inflight drain", func() bool { return counterValue(t, s.metrics.inflightQueries) == 0 })
	c4 := dialTCP(t, addr)
	sendTCP(t, c4, 4)
	if _, err := readTCP(c4, 2*time.Second); err != nil {
		t.Fatalf("query after release: %v", err)
	}
}

func TestMaxConnectionsTCP(t *testing.T) {
	h := newGatedHandler()
	close(h.release)
	s := NewServer(ServerOpts{DNSHandler: h, MaxConnections: 1})
	defer s.Close()
	addr := startTCP(t, s)

	// A completed round trip proves the first connection holds the slot.
	c1 := dialTCP(t, addr)
	sendTCP(t, c1, 1)
	if _, err := readTCP(c1, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if v := counterValue(t, s.metrics.openConnections); v != 1 {
		t.Fatalf("open connections = %v, want 1", v)
	}

	c2 := dialTCP(t, addr)
	if _, err := readTCP(c2, 2*time.Second); err == nil || isTimeout(err) {
		t.Fatalf("expected second connection to be closed, got %v", err)
	}
	if v := counterValue(t, s.metrics.rejectedConnections.WithLabelValues(C.ProtocolTCP)); v != 1 {
		t.Fatalf("rejected tcp connections = %v, want 1", v)
	}

	// The first connection is unaffected.
	sendTCP(t, c1, 2)
	if _, err := readTCP(c1, 2*time.Second); err != nil {
		t.Fatalf("first connection broken: %v", err)
	}

	c1.Close()
	waitFor(t, "connection release", func() bool { return counterValue(t, s.metrics.openConnections) == 0 })
	c3 := dialTCP(t, addr)
	sendTCP(t, c3, 3)
	if _, err := readTCP(c3, 2*time.Second); err != nil {
		t.Fatalf("connection after release: %v", err)
	}
}

// TestDefaultLimitsUnbounded checks that zero limits keep today's behaviour:
// many concurrent queries and connections are all served.
func TestDefaultLimitsUnbounded(t *testing.T) {
	const n = 64
	h := newGatedHandler()
	s := NewServer(ServerOpts{DNSHandler: h})
	defer s.Close()
	udpAddr, tcpAddr := startUDP(t, s), startTCP(t, s)

	udpConns := make([]net.Conn, n)
	tcpConns := make([]net.Conn, n)
	for i := 0; i < n; i++ {
		udpConns[i] = dialUDP(t, udpAddr)
		sendUDP(t, udpConns[i], uint16(i))
		tcpConns[i] = dialTCP(t, tcpAddr)
		sendTCP(t, tcpConns[i], uint16(i))
	}
	h.waitEntered(t, 2*n)
	if v := counterValue(t, s.metrics.inflightQueries); v != 2*n {
		t.Fatalf("inflight gauge = %v, want %d", v, 2*n)
	}
	close(h.release)

	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(c net.Conn) { defer wg.Done(); _, err := readUDP(c, 2*time.Second); errs <- err }(udpConns[i])
		go func(c net.Conn) { defer wg.Done(); _, err := readTCP(c, 2*time.Second); errs <- err }(tcpConns[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if v := counterValue(t, s.metrics.rejectedQueries) + counterValue(t, s.metrics.rejectedConnections); v != 0 {
		t.Fatalf("rejections with default limits: %v", v)
	}
}

func TestShutdownWaitsForInflightQueriesWithLimit(t *testing.T) {
	h := newGatedHandler()
	// Closing the UDP listener cancels the query context, so the handler must
	// ignore it for the query to stay in flight across Shutdown.
	h.ignoreCtx = true
	s := NewServer(ServerOpts{DNSHandler: h, MaxConcurrentQueries: 2})
	addr := startUDP(t, s)
	c1 := dialUDP(t, addr)
	sendUDP(t, c1, 1)
	h.waitEntered(t, 1)

	done := make(chan error, 1)
	go func() { done <- s.Shutdown(context.Background()) }()
	select {
	case <-done:
		t.Fatal("shutdown returned before the in-flight query finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(h.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if v := counterValue(t, s.metrics.inflightQueries); v != 0 {
		t.Fatalf("inflight gauge = %v after shutdown", v)
	}
}

func TestMaxConcurrentQueriesHTTP(t *testing.T) {
	h := newGatedHandler()
	close(h.release)
	hh, err := H.NewHandler(H.HandlerOpts{DNSHandler: h})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(ServerOpts{HttpHandler: hh, MaxConcurrentQueries: 1})
	defer s.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.ServeHTTP(l)

	// Occupy the only slot.
	if err := s.beginQuery("test"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + l.Addr().String() + "/dns-query?dns=AAABAAABAAAAAAAAB2V4YW1wbGUDY29tAAABAAE")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("got status %d Retry-After %q, want 503 and 1", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if v := counterValue(t, s.metrics.rejectedQueries.WithLabelValues(C.ProtocolHTTP)); v != 1 {
		t.Fatalf("rejected http queries = %v, want 1", v)
	}
	s.endQuery()

	resp, err = http.Get("http://" + l.Addr().String() + "/dns-query?dns=AAABAAABAAAAAAAAB2V4YW1wbGUDY29tAAABAAE")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status after release = %d, want 200", resp.StatusCode)
	}
}

func startDoQ(t *testing.T, s *Server) string {
	t.Helper()
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
	return pc.LocalAddr().String()
}

func dialDoQ(ctx context.Context, addr string) (*quic.Conn, error) {
	return quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"doq"}}, nil)
}

func TestMaxConcurrentQueriesDoQ(t *testing.T) {
	h := newGatedHandler()
	close(h.release)
	s := NewServer(ServerOpts{DNSHandler: h, Cert: "testdata/test.test.cert", Key: "testdata/test.test.key", MaxConcurrentQueries: 1})
	defer s.Close()
	addr := startDoQ(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialDoQ(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")

	query := func() error {
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			return err
		}
		if _, err := dnsutils.WriteMsgToTCP(stream, newQuery(0)); err != nil {
			return err
		}
		stream.Close()
		_, _, err = dnsutils.ReadMsgFromTCP(stream)
		return err
	}

	if err := s.beginQuery("test"); err != nil {
		t.Fatal(err)
	}
	var streamErr *quic.StreamError
	if err := query(); !errors.As(err, &streamErr) || streamErr.ErrorCode != doqExcessiveLoad {
		t.Fatalf("expected stream reset with DOQ_EXCESSIVE_LOAD, got %v", err)
	}
	if v := counterValue(t, s.metrics.rejectedQueries.WithLabelValues(C.ProtocolQUIC)); v != 1 {
		t.Fatalf("rejected quic queries = %v, want 1", v)
	}
	s.endQuery()
	if err := query(); err != nil {
		t.Fatalf("query after release: %v", err)
	}
}

func TestMaxConnectionsDoQ(t *testing.T) {
	h := newGatedHandler()
	close(h.release)
	s := NewServer(ServerOpts{DNSHandler: h, Cert: "testdata/test.test.cert", Key: "testdata/test.test.key", MaxConnections: 1})
	defer s.Close()
	addr := startDoQ(t, s)

	// Occupy the only connection slot.
	if !s.beginConn("test") {
		t.Fatal("initial connection rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialDoQ(ctx, addr)
	if err == nil {
		select {
		case <-conn.Context().Done():
			err = context.Cause(conn.Context())
		case <-ctx.Done():
			t.Fatal("rejected connection was not closed")
		}
	}
	// The listener accepts connections before the handshake is confirmed. An
	// application close sent that early is carried as the transport error
	// APPLICATION_ERROR (RFC 9000 section 10.2.3), so accept either form.
	var appErr *quic.ApplicationError
	var transportErr *quic.TransportError
	switch {
	case errors.As(err, &appErr) && appErr.Remote && appErr.ErrorCode == doqExcessiveLoad:
	case errors.As(err, &transportErr) && transportErr.Remote && transportErr.ErrorCode == quic.ApplicationErrorErrorCode:
	default:
		t.Fatalf("expected remote close with DOQ_EXCESSIVE_LOAD, got %T %v", err, err)
	}
	if v := counterValue(t, s.metrics.rejectedConnections.WithLabelValues(C.ProtocolQUIC)); v != 1 {
		t.Fatalf("rejected quic connections = %v, want 1", v)
	}
	s.endConn()
}
