package dnsutils

import (
	"net"
	"net/netip"

	"github.com/miekg/dns"
)

const (
	EDNSTraceVersion       uint8 = 1
	maxSnapshotEDNSOptions       = 64
)

const (
	EDNSAnomalyMultipleOPT       = "multiple_opt"
	EDNSAnomalyMultipleECS       = "multiple_ecs"
	EDNSAnomalyInvalidECSFamily  = "invalid_ecs_family"
	EDNSAnomalyInvalidECSPrefix  = "invalid_ecs_prefix"
	EDNSAnomalyInvalidECSAddress = "invalid_ecs_address"
)

// EDNSSnapshot contains bounded, non-sensitive metadata from the EDNS records
// observed at one DNS message boundary. It never stores option payloads.
type EDNSSnapshot struct {
	Present              bool         `json:"present"`
	Version              uint8        `json:"version"`
	UDPSize              uint16       `json:"udp_size"`
	DNSSECOK             bool         `json:"dnssec_ok"`
	OptionCodes          []uint16     `json:"option_codes"`
	OptionCodesTruncated bool         `json:"option_codes_truncated,omitempty"`
	ECS                  *ECSSnapshot `json:"ecs,omitempty"`
	Anomalies            []string     `json:"anomalies,omitempty"`
}

type ECSSnapshot struct {
	Address      string `json:"address"`
	Family       uint16 `json:"family"`
	SourcePrefix uint8  `json:"source_prefix"`
	ScopePrefix  uint8  `json:"scope_prefix"`
}

// SnapshotEDNS observes every OPT and ECS in msg without rejecting malformed
// input. Scalar OPT fields come from the first OPT. Option codes retain wire
// order and duplicates and are bounded independently from ECS discovery.
func SnapshotEDNS(msg *dns.Msg) EDNSSnapshot {
	result := EDNSSnapshot{OptionCodes: []uint16{}}
	if msg == nil {
		return result
	}

	var optCount, ecsCount int
	for _, rr := range msg.Extra {
		opt, ok := rr.(*dns.OPT)
		if !ok || opt == nil {
			continue
		}
		optCount++
		if optCount == 1 {
			result.Present = true
			result.Version = opt.Version()
			result.UDPSize = opt.UDPSize()
			result.DNSSECOK = opt.Do()
		}
		for _, option := range opt.Option {
			if option == nil {
				continue
			}
			if len(result.OptionCodes) < maxSnapshotEDNSOptions {
				result.OptionCodes = append(result.OptionCodes, option.Option())
			} else {
				result.OptionCodesTruncated = true
			}
			ecs, ok := option.(*dns.EDNS0_SUBNET)
			if !ok || ecs == nil {
				continue
			}
			ecsCount++
			snapshot, anomaly := snapshotECS(ecs)
			if anomaly != "" {
				appendEDNSAnomaly(&result, anomaly)
				continue
			}
			if result.ECS == nil {
				result.ECS = snapshot
			}
		}
	}
	if optCount > 1 {
		appendEDNSAnomaly(&result, EDNSAnomalyMultipleOPT)
	}
	if ecsCount > 1 {
		appendEDNSAnomaly(&result, EDNSAnomalyMultipleECS)
	}
	return result
}

func snapshotECS(ecs *dns.EDNS0_SUBNET) (*ECSSnapshot, string) {
	var (
		addr    netip.Addr
		ok      bool
		maxBits int
	)
	switch ecs.Family {
	case 1:
		ip := net.IP(ecs.Address).To4()
		if ip == nil {
			return nil, EDNSAnomalyInvalidECSAddress
		}
		addr, ok = netip.AddrFromSlice(ip)
		maxBits = 32
	case 2:
		ip := net.IP(ecs.Address)
		if ip.To4() != nil {
			return nil, EDNSAnomalyInvalidECSAddress
		}
		ip = ip.To16()
		if ip == nil {
			return nil, EDNSAnomalyInvalidECSAddress
		}
		addr, ok = netip.AddrFromSlice(ip)
		maxBits = 128
	default:
		return nil, EDNSAnomalyInvalidECSFamily
	}
	if !ok {
		return nil, EDNSAnomalyInvalidECSAddress
	}
	if int(ecs.SourceNetmask) > maxBits {
		return nil, EDNSAnomalyInvalidECSPrefix
	}
	if int(ecs.SourceScope) > maxBits {
		return nil, EDNSAnomalyInvalidECSPrefix
	}
	return &ECSSnapshot{
		Address:      netip.PrefixFrom(addr, int(ecs.SourceNetmask)).Masked().Addr().String(),
		Family:       ecs.Family,
		SourcePrefix: ecs.SourceNetmask,
		ScopePrefix:  ecs.SourceScope,
	}, ""
}

func appendEDNSAnomaly(snapshot *EDNSSnapshot, anomaly string) {
	for _, existing := range snapshot.Anomalies {
		if existing == anomaly {
			return
		}
	}
	snapshot.Anomalies = append(snapshot.Anomalies, anomaly)
}

func CloneEDNSSnapshot(snapshot *EDNSSnapshot) *EDNSSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	if snapshot.OptionCodes != nil {
		clone.OptionCodes = append([]uint16{}, snapshot.OptionCodes...)
	}
	if snapshot.Anomalies != nil {
		clone.Anomalies = append([]string{}, snapshot.Anomalies...)
	}
	if snapshot.ECS != nil {
		ecs := *snapshot.ECS
		clone.ECS = &ecs
	}
	return &clone
}
