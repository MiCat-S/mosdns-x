package dnsutils

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestSnapshotEDNSBoundsOptionsAndFindsLateECS(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.org.", dns.TypeA)
	m.SetEdns0(1232, true)
	opt := m.IsEdns0()
	for i := 0; i < maxSnapshotEDNSOptions+3; i++ {
		opt.Option = append(opt.Option, &dns.EDNS0_LOCAL{Code: uint16(65000 + i)})
	}
	opt.Option = append(opt.Option, &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24,
		Address: net.ParseIP("192.0.2.129"),
	})

	got := SnapshotEDNS(m)
	if !got.Present || got.UDPSize != 1232 || !got.DNSSECOK {
		t.Fatalf("snapshot = %+v", got)
	}
	if len(got.OptionCodes) != maxSnapshotEDNSOptions || !got.OptionCodesTruncated {
		t.Fatalf("codes=%d truncated=%v", len(got.OptionCodes), got.OptionCodesTruncated)
	}
	if got.ECS == nil || got.ECS.Address != "192.0.2.0" {
		t.Fatalf("late ECS = %+v", got.ECS)
	}
}

func TestSnapshotEDNSAnomaliesAndFirstValidECS(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.org.", dns.TypeAAAA)
	first := new(dns.OPT)
	first.Hdr.Name = "."
	first.Hdr.Rrtype = dns.TypeOPT
	first.SetUDPSize(1400)
	first.Option = []dns.EDNS0{
		&dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 999, SourceNetmask: 24, Address: net.ParseIP("192.0.2.1")},
		&dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 2, SourceNetmask: 64, Address: net.ParseIP("2001:db8::dead:beef")},
	}
	second := new(dns.OPT)
	second.Hdr.Name = "."
	second.Hdr.Rrtype = dns.TypeOPT
	second.SetUDPSize(512)
	m.Extra = []dns.RR{first, second}

	got := SnapshotEDNS(m)
	if got.UDPSize != 1400 || got.ECS == nil || got.ECS.Address != "2001:db8::" {
		t.Fatalf("snapshot = %+v", got)
	}
	want := map[string]bool{EDNSAnomalyInvalidECSFamily: true, EDNSAnomalyMultipleECS: true, EDNSAnomalyMultipleOPT: true}
	for _, anomaly := range got.Anomalies {
		delete(want, anomaly)
	}
	if len(want) != 0 {
		t.Fatalf("missing anomalies: %v from %v", want, got.Anomalies)
	}
}

func TestCloneEDNSSnapshotIsDeep(t *testing.T) {
	original := &EDNSSnapshot{
		Present: true, OptionCodes: []uint16{dns.EDNS0SUBNET},
		ECS: &ECSSnapshot{Address: "192.0.2.0"}, Anomalies: []string{"multiple_ecs"},
	}
	clone := CloneEDNSSnapshot(original)
	clone.OptionCodes[0] = dns.EDNS0COOKIE
	clone.ECS.Address = "198.51.100.0"
	clone.Anomalies[0] = "changed"
	if original.OptionCodes[0] != dns.EDNS0SUBNET || original.ECS.Address != "192.0.2.0" || original.Anomalies[0] != "multiple_ecs" {
		t.Fatalf("clone shares state with original: %+v", original)
	}
}
