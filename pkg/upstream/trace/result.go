package trace

import (
	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
)

// Result binds EDNS snapshots to the exact response from one upstream
// exchange. Snapshots describe logical messages at protocol boundaries.
type Result struct {
	Response         *dns.Msg
	RequestEDNS      *dnsutils.EDNSSnapshot
	ResponseEDNS     *dnsutils.EDNSSnapshot
	DetailsAvailable bool
}

func NewResult(response *dns.Msg, requestSnapshot dnsutils.EDNSSnapshot) Result {
	result := Result{Response: response, RequestEDNS: dnsutils.CloneEDNSSnapshot(&requestSnapshot), DetailsAvailable: true}
	if response != nil {
		responseSnapshot := dnsutils.SnapshotEDNS(response)
		result.ResponseEDNS = &responseSnapshot
	}
	return result
}
