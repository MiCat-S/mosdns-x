package recursor

import (
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func TestMarkLameDropsExpiredZones(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	in := newInfra(clock.Now)
	addr := netip.MustParseAddr("192.0.2.1")
	for i := 0; i < 2*maxEDNSZones; i++ {
		in.markLame(addr, "zone"+strconv.Itoa(i)+".example.")
	}
	clock.Advance(lameDuration + time.Second)
	in.markLame(addr, "fresh.example.")

	s := in.get(addr)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lame) != 1 {
		t.Fatalf("%d lame zones kept, want only the live one", len(s.lame))
	}
	if _, ok := s.lame["fresh.example."]; !ok {
		t.Fatal("live lame zone dropped")
	}
}
