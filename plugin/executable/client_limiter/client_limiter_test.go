package client_limiter

import (
	"net/netip"
	"testing"

	"github.com/pmkol/mosdns-x/coremain"
)

func TestNewLimiterAppliesConfiguredMasks(t *testing.T) {
	l, err := NewLimiter(coremain.NewBP("limiter", PluginType, nil, nil), &Args{MaxQPS: 1, V4Mask: 24, V6Mask: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	for addr, want := range map[string]string{
		"192.0.2.77":         "192.0.2.0/24",
		"2001:db8:1:2:3::1":  "2001:db8:1:2::/64",
		"::ffff:192.0.2.200": "192.0.2.0/24",
	} {
		if got := l.hpLimiter.ApplyMask(netip.MustParseAddr(addr)); got != netip.MustParsePrefix(want) {
			t.Errorf("%s is limited as %s, want %s", addr, got, want)
		}
	}
}
