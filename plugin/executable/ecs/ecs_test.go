/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package ecs

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	C "github.com/pmkol/mosdns-x/pkg/query_context"
)

type traceCaptureExecutable struct{}

func (*traceCaptureExecutable) Exec(_ context.Context, qCtx *C.Context, _ executable_seq.ExecutableChainNode) error {
	requestSnapshot := dnsutils.SnapshotEDNS(qCtx.Q())
	response := qCtx.Q().Copy()
	response.Response = true
	responseSnapshot := dnsutils.SnapshotEDNS(response)
	qCtx.SetResponseWithTrace(response, C.ResponseTrace{
		Source:               C.ResponseSourceUpstream,
		UpstreamID:           "forward/0",
		UpstreamStageStatus:  C.UpstreamStageSelected,
		UpstreamRequestEDNS:  &requestSnapshot,
		UpstreamResponseEDNS: &responseSnapshot,
	})
	return nil
}

func Test_ecsPlugin(t *testing.T) {
	tests := []struct {
		name       string
		args       Args
		qtype      uint16
		qHasEDNS0  bool
		qHasECS    string
		clientAddr string
		wantAddr   string
		rWantEDNS0 bool
		rWantECS   bool
	}{
		{"edns0 contingency", Args{Auto: true}, dns.TypeA, false, "", "1.0.0.0", "1.0.0.0", false, false},
		{"edns0 contingency2", Args{Auto: true}, dns.TypeA, true, "", "1.0.0.0", "1.0.0.0", true, false},
		{"ecs contingency", Args{Auto: true}, dns.TypeA, true, "", "1.0.0.0", "1.0.0.0", true, false},
		{"ecs contingency2", Args{Auto: true}, dns.TypeA, true, "1.0.0.0", "1.0.0.0", "1.0.0.0", true, true},

		{"auto", Args{Auto: true}, dns.TypeA, false, "", "1.0.0.0", "1.0.0.0", false, false},
		{"auto2", Args{Auto: true}, dns.TypeA, false, "", "", "", false, false},

		{"overwrite off", Args{Auto: true}, dns.TypeA, true, "1.2.3.4", "1.0.0.0", "1.2.3.4", true, true},
		{"overwrite on", Args{Auto: true, ForceOverwrite: true}, dns.TypeA, true, "1.2.3.4", "1.0.0.0", "1.0.0.0", true, true},

		{"preset v4", Args{IPv4: "1.2.3.4"}, dns.TypeA, false, "", "", "1.2.3.4", false, false},
		{"preset v6", Args{IPv6: "::1"}, dns.TypeA, false, "", "", "::1", false, false},
		{"preset both", Args{IPv4: "1.2.3.4", IPv6: "::1"}, dns.TypeA, false, "", "", "1.2.3.4", false, false},
		{"preset both2", Args{IPv4: "1.2.3.4", IPv6: "::1"}, dns.TypeAAAA, false, "", "", "::1", false, false},
	}
	for _, tt := range tests {
		p, err := newPlugin(coremain.NewBP("ecs", PluginType, nil, nil), &tt.args)
		if err != nil {
			t.Fatal(err)
		}

		t.Run(tt.name, func(t *testing.T) {
			q := new(dns.Msg)
			q.SetQuestion(".", tt.qtype)
			r := new(dns.Msg)
			r.SetReply(q)

			if tt.qHasEDNS0 {
				optQ := dnsutils.UpgradeEDNS0(q)
				optR := dnsutils.UpgradeEDNS0(r)

				if len(tt.qHasECS) > 0 {
					ip, err := netip.ParseAddr(tt.qHasECS)
					if err != nil {
						t.Fatal(err)
					}
					dnsutils.AddECS(optR, dnsutils.NewEDNS0Subnet(net.IPv6loopback, 24, false), true)
					dnsutils.AddECS(optQ, dnsutils.NewEDNS0Subnet(ip.AsSlice(), 24, false), true)
				}
			}

			var ip netip.Addr
			if len(tt.clientAddr) > 0 {
				ip, err = netip.ParseAddr(tt.clientAddr)
				if err != nil {
					t.Fatal(err)
				}
			}
			meta := C.NewRequestMeta(ip)
			qCtx := C.NewContext(q, meta)

			next := executable_seq.WrapExecutable(&executable_seq.DummyExecutable{
				WantR: r,
			})
			if err := p.Exec(context.Background(), qCtx, next); err != nil {
				t.Fatal(err)
			}

			var qECS net.IP
			e := dnsutils.GetMsgECS(q)
			if e != nil {
				qECS = e.Address
			}
			wantAddr := net.ParseIP(tt.wantAddr)
			if !qECS.Equal(wantAddr) {
				t.Fatalf("want addr %v, got %v", tt.wantAddr, qECS)
			}

			if res := dnsutils.GetMsgECS(qCtx.R()) != nil; res != tt.rWantECS {
				t.Fatalf("want rWantECS %v, got %v", tt.rWantECS, res)
			}
			if res := qCtx.R().IsEdns0() != nil; res != tt.rWantEDNS0 {
				t.Fatalf("want rWantEDNS0 %v, got %v", tt.rWantEDNS0, res)
			}
		})
	}
}

func TestECSCleanupDoesNotMutateCapturedUpstreamSnapshots(t *testing.T) {
	p, err := newPlugin(coremain.NewBP("ecs", PluginType, nil, nil), &Args{Auto: true})
	if err != nil {
		t.Fatal(err)
	}
	meta := C.NewRequestMeta(netip.MustParseAddr("192.0.2.129"))
	qCtx := C.NewContext(new(dns.Msg).SetQuestion("example.org.", dns.TypeA), meta)
	qCtx.SetCaptureQueryDetails(true)
	if err := p.Exec(context.Background(), qCtx, executable_seq.WrapExecutable(new(traceCaptureExecutable))); err != nil {
		t.Fatal(err)
	}
	if qCtx.R() == nil || qCtx.R().IsEdns0() != nil {
		t.Fatalf("ECS plugin did not clean its response OPT: %v", qCtx.R())
	}
	trace := qCtx.ResponseTrace()
	if trace.UpstreamRequestEDNS == nil || trace.UpstreamRequestEDNS.ECS == nil || trace.UpstreamRequestEDNS.ECS.Address != "192.0.2.0" {
		t.Fatalf("upstream request snapshot = %+v", trace.UpstreamRequestEDNS)
	}
	if trace.UpstreamResponseEDNS == nil || trace.UpstreamResponseEDNS.ECS == nil || trace.UpstreamResponseEDNS.ECS.Address != "192.0.2.0" {
		t.Fatalf("upstream response snapshot = %+v", trace.UpstreamResponseEDNS)
	}
}

func TestNoECSCapturesOPTWithoutECSAtUpstreamBoundary(t *testing.T) {
	request := new(dns.Msg).SetQuestion("example.org.", dns.TypeA)
	request.SetEdns0(1232, false)
	request.IsEdns0().Option = append(request.IsEdns0().Option, &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP("192.0.2.129"),
	})
	qCtx := C.NewContext(request, nil)
	qCtx.SetCaptureQueryDetails(true)
	plugin := &noECS{BP: coremain.NewBP("no_ecs", PluginType, nil, nil)}
	if err := plugin.Exec(context.Background(), qCtx, executable_seq.WrapExecutable(new(traceCaptureExecutable))); err != nil {
		t.Fatal(err)
	}
	trace := qCtx.ResponseTrace()
	if trace.UpstreamRequestEDNS == nil || !trace.UpstreamRequestEDNS.Present || trace.UpstreamRequestEDNS.ECS != nil || len(trace.UpstreamRequestEDNS.OptionCodes) != 0 {
		t.Fatalf("no_ecs request snapshot = %+v", trace.UpstreamRequestEDNS)
	}
}
