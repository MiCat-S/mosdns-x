package udp

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/upstream/transport"
)

// startServer runs a loopback UDP server that answers each query with the
// raw datagrams returned by handle, in order.
func startServer(t *testing.T, handle func(q *dns.Msg) []*dns.Msg) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		b := make([]byte, 65535)
		for {
			n, addr, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			q := new(dns.Msg)
			if q.Unpack(b[:n]) != nil {
				continue
			}
			for _, r := range handle(q) {
				raw, err := r.Pack()
				if err != nil {
					panic(err)
				}
				_, _ = pc.WriteTo(raw, addr)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func newTestUpstream(t *testing.T, addr string) *Upstream {
	t.Helper()
	u, err := NewUDPUpstream(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", addr)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	return u
}

func reply(q *dns.Msg, ip string) *dns.Msg {
	r := new(dns.Msg)
	r.SetReply(q)
	r.Answer = append(r.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.ParseIP(ip),
	})
	return r
}

func answerIP(t *testing.T, r *dns.Msg) string {
	t.Helper()
	if len(r.Answer) != 1 {
		t.Fatalf("want one answer, got %v", r.Answer)
	}
	return r.Answer[0].(*dns.A).A.String()
}

func exchange(t *testing.T, u *Upstream, q *dns.Msg) *dns.Msg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := u.ExchangeContext(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExchangeIgnoresRepliesThatDoNotAnswerTheQuery(t *testing.T) {
	addr := startServer(t, func(q *dns.Msg) []*dns.Msg {
		wrongName := reply(q, "198.51.100.1")
		wrongName.Question[0].Name = "other.example."
		wrongType := reply(q, "198.51.100.2")
		wrongType.Question[0].Qtype = dns.TypeAAAA
		notResponse := reply(q, "198.51.100.3")
		notResponse.Response = false
		extraQuestion := reply(q, "198.51.100.4")
		extraQuestion.Question = append(extraQuestion.Question, dns.Question{Name: "other.example.", Qtype: dns.TypeA, Qclass: dns.ClassINET})
		wrongOpcode := reply(q, "198.51.100.5")
		wrongOpcode.Opcode = dns.OpcodeNotify
		questionless := reply(q, "198.51.100.6")
		questionless.Question = nil
		good := reply(q, "192.0.2.1")
		good.Question[0].Name = "WwW.ExAmPlE.oRg."
		return []*dns.Msg{wrongName, wrongType, notResponse, extraQuestion, wrongOpcode, questionless, good}
	})
	u := newTestUpstream(t, addr)

	q := new(dns.Msg)
	q.SetQuestion("www.example.org.", dns.TypeA)
	q.Id = 4242
	r := exchange(t, u, q)
	if got := answerIP(t, r); got != "192.0.2.1" {
		t.Fatalf("accepted a reply that does not answer the query: %s", got)
	}
	if r.Id != 4242 {
		t.Fatalf("reply id = %d, want the caller's 4242", r.Id)
	}
}

func TestExchangeAcceptsErrorReplyWithoutQuestion(t *testing.T) {
	addr := startServer(t, func(q *dns.Msg) []*dns.Msg {
		r := new(dns.Msg)
		r.SetRcode(q, dns.RcodeFormatError)
		r.Question = nil
		return []*dns.Msg{r}
	})
	u := newTestUpstream(t, addr)

	q := new(dns.Msg)
	q.SetQuestion("www.example.org.", dns.TypeA)
	if r := exchange(t, u, q); r.Rcode != dns.RcodeFormatError {
		t.Fatalf("rcode = %s, want FORMERR", dns.RcodeToString[r.Rcode])
	}
}

func TestExchangeReadsRepliesLargerThan4096Bytes(t *testing.T) {
	addr := startServer(t, func(q *dns.Msg) []*dns.Msg {
		r := new(dns.Msg)
		r.SetReply(q)
		for range 24 {
			r.Answer = append(r.Answer, &dns.TXT{
				Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60},
				Txt: []string{strings.Repeat("a", 250)},
			})
		}
		return []*dns.Msg{r}
	})
	u := newTestUpstream(t, addr)

	q := new(dns.Msg)
	q.SetQuestion("big.example.org.", dns.TypeTXT)
	q.SetEdns0(dns.MaxMsgSize, false)
	if r := exchange(t, u, q); len(r.Answer) != 24 {
		t.Fatalf("got %d answers, want 24", len(r.Answer))
	}
}

func TestQueryIDsAreNotSequential(t *testing.T) {
	var mu sync.Mutex
	var ids []uint16
	addr := startServer(t, func(q *dns.Msg) []*dns.Msg {
		mu.Lock()
		ids = append(ids, q.Id)
		mu.Unlock()
		return []*dns.Msg{reply(q, "192.0.2.1")}
	})
	u := newTestUpstream(t, addr)

	for range 16 {
		q := new(dns.Msg)
		q.SetQuestion("www.example.org.", dns.TypeA)
		exchange(t, u, q)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			return
		}
	}
	t.Fatalf("upstream query ids are sequential: %v", ids)
}

func TestFailedSocketOnlyFailsItsOwnQueries(t *testing.T) {
	u, err := NewUDPUpstream(func(context.Context) (net.Conn, error) { return nil, net.ErrClosed }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	oldConn, oldPeer := net.Pipe()
	defer oldPeer.Close()
	newConn, newPeer := net.Pipe()
	defer newConn.Close()
	defer newPeer.Close()

	q := new(dns.Msg)
	q.SetQuestion("www.example.org.", dns.TypeA)
	_, oldCh, err := u.claimID(q, oldConn)
	if err != nil {
		t.Fatal(err)
	}
	newID, newCh, err := u.claimID(q, newConn)
	if err != nil {
		t.Fatal(err)
	}

	// A reply that reaches the old socket must not complete a query sent on
	// the new one, even when the id matches.
	r := reply(q, "192.0.2.1")
	r.Id = newID
	u.deliver(oldConn, r)

	u.handleConnClosed(oldConn, net.ErrClosed)
	select {
	case m := <-oldCh:
		if m != nil {
			t.Fatalf("query on the failed socket got %v, want nil", m)
		}
	default:
		t.Fatal("query on the failed socket was not failed")
	}
	select {
	case m := <-newCh:
		t.Fatalf("query on the live socket was completed by another socket: %v", m)
	default:
	}

	u.deliver(newConn, r)
	select {
	case m := <-newCh:
		if m != r {
			t.Fatalf("query on the live socket got %v", m)
		}
	default:
		t.Fatal("query on the live socket did not get its reply")
	}
}

func TestCloseClosesTCPFallback(t *testing.T) {
	tt, err := transport.NewTransport(transport.Opts{
		DialFunc: func(context.Context) (net.Conn, error) {
			t.Error("closed upstream dialed its tcp fallback")
			return nil, net.ErrClosed
		},
		WriteFunc: dnsutils.WriteMsgToTCP,
		ReadFunc:  dnsutils.ReadMsgFromTCP,
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewUDPUpstream(func(context.Context) (net.Conn, error) { return nil, net.ErrClosed }, tt)
	if err != nil {
		t.Fatal(err)
	}
	_ = u.Close()

	q := new(dns.Msg)
	q.SetQuestion("www.example.org.", dns.TypeA)
	if _, err := tt.ExchangeContext(context.Background(), q); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("tcp fallback still usable after Close: %v", err)
	}
}

func TestDialPanicIsReturnedAsError(t *testing.T) {
	u, err := NewUDPUpstream(func(context.Context) (net.Conn, error) { panic("boom") }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	q := new(dns.Msg)
	q.SetQuestion("www.example.org.", dns.TypeA)
	_, err = u.ExchangeContext(context.Background(), q)
	if err == nil || !strings.Contains(err.Error(), "dial panic: boom") {
		t.Fatalf("err = %v, want the recovered dial panic", err)
	}
}
