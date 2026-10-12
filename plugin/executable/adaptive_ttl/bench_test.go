package adaptive_ttl

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/coremain"
)

// BenchmarkObserve measures observe under parallel load: "hot" sends every
// query to one key, "spread" to 1024 keys, and "full" to more keys than
// the table holds so admission sweeps run.
func BenchmarkObserve(b *testing.B) {
	r := new(dns.Msg).SetQuestion("example.org.", dns.TypeA)
	for i := range 4 {
		rr, _ := dns.NewRR("example.org. 300 IN A 192.0.2." + strconv.Itoa(i+1))
		r.Answer = append(r.Answer, rr)
	}
	txt, _ := dns.NewRR(`example.org. 300 IN TXT "v=spf1 include:_spf.example.org ~all"`)
	rTXT := r.Copy()
	rTXT.Answer = append(rTXT.Answer, txt)
	for _, c := range []struct {
		name string
		keys int
		size int64
		resp *dns.Msg
	}{
		{"hot", 1, 0, r},
		{"spread", 1024, 0, r},
		{"spread-txt", 1024, 0, rTXT},
		{"full", 4096, 1024, r},
	} {
		b.Run(c.name, func(b *testing.B) {
			p, err := newAdaptiveTTL(coremain.NewBP("adaptive", PluginType, nil, nil), &Args{Size: c.size})
			if err != nil {
				b.Fatal(err)
			}
			keys := make([]key, c.keys)
			for i := range keys {
				keys[i] = key{name: "n" + strconv.Itoa(i) + ".example.org.", qtype: dns.TypeA}
			}
			var seq atomic.Uint64
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					p.observe(keys[seq.Add(1)%uint64(len(keys))], c.resp, false)
				}
			})
		})
	}
}
