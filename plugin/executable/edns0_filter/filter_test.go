package edns0_filter

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

type captureFilteredRequest struct{}

func (*captureFilteredRequest) Exec(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
	requestSnapshot := dnsutils.SnapshotEDNS(qCtx.Q())
	response := new(dns.Msg)
	response.SetReply(qCtx.Q())
	responseSnapshot := dnsutils.SnapshotEDNS(response)
	qCtx.SetResponseWithTrace(response, query_context.ResponseTrace{
		Source:               query_context.ResponseSourceUpstream,
		UpstreamID:           "forward/0",
		UpstreamStageStatus:  query_context.UpstreamStageSelected,
		UpstreamRequestEDNS:  &requestSnapshot,
		UpstreamResponseEDNS: &responseSnapshot,
	})
	return nil
}

func TestECSOnlyFilterCapturesPostFilterRequest(t *testing.T) {
	request := new(dns.Msg).SetQuestion("example.org.", dns.TypeA)
	request.SetEdns0(1232, false)
	request.IsEdns0().Option = append(request.IsEdns0().Option,
		&dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "0011223344556677"},
		&dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP("192.0.2.129")},
		&dns.EDNS0_PADDING{Padding: []byte{0, 0, 0, 0}},
	)
	qCtx := query_context.NewContext(request, nil)
	qCtx.SetCaptureQueryDetails(true)
	filter := NewFilter(coremain.NewBP("filter", PluginType, nil, nil), &Args{Keep: []uint16{dns.EDNS0SUBNET}})
	if err := filter.Exec(context.Background(), qCtx, executable_seq.WrapExecutable(new(captureFilteredRequest))); err != nil {
		t.Fatal(err)
	}
	trace := qCtx.ResponseTrace()
	if trace.UpstreamRequestEDNS == nil || len(trace.UpstreamRequestEDNS.OptionCodes) != 1 || trace.UpstreamRequestEDNS.OptionCodes[0] != dns.EDNS0SUBNET {
		t.Fatalf("filtered request snapshot = %+v", trace.UpstreamRequestEDNS)
	}
	if trace.UpstreamRequestEDNS.ECS == nil || trace.UpstreamRequestEDNS.ECS.Address != "192.0.2.0" {
		t.Fatalf("filtered ECS snapshot = %+v", trace.UpstreamRequestEDNS.ECS)
	}
}
