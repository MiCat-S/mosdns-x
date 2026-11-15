package dialer

import (
	"bytes"
	"testing"
)

// unpack reads the packet that ReadFrom stored in the cache, so the tests do
// the same.
func unpackCached(b []byte) ([]byte, string, error) {
	s := &SocksPacketConn{cache: b}
	payload, addr, err := s.unpack(b)
	if err != nil {
		return nil, "", err
	}
	return payload, addr.String(), nil
}

func TestUnpackFqdn(t *testing.T) {
	pkt := []byte{Reversed, Reversed, NoFragment, TypeFqdn, 11}
	pkt = append(pkt, "example.com"...)
	pkt = append(pkt, 0, 53)
	pkt = append(pkt, "payload"...)
	payload, addr, err := unpackCached(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "example.com:53" || !bytes.Equal(payload, []byte("payload")) {
		t.Fatalf("got addr %q payload %q", addr, payload)
	}
}

// A length byte of 249 or more used to overflow in uint8 arithmetic, pass the
// bounds check and slice past the packet, panicking the reader.
func TestUnpackFqdnLengthOverflow(t *testing.T) {
	for _, n := range []byte{249, 250, 255} {
		pkt := []byte{Reversed, Reversed, NoFragment, TypeFqdn, n, 'a', 'b', 0, 53}
		if _, _, err := unpackCached(pkt); err == nil {
			t.Fatalf("length %d: truncated fqdn accepted", n)
		}
	}
	if _, _, err := unpackCached([]byte{Reversed, Reversed, NoFragment, TypeFqdn}); err == nil {
		t.Fatal("fqdn without a length byte accepted")
	}
}
