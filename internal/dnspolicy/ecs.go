package dnspolicy

import (
	"net/netip"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
)

// ecsOverride returns the subnet to send for request, or nil to leave the
// request's ECS as it is. It picks the family the way the ecs plugin does with
// preset addresses: A prefers the IPv4 prefix and AAAA the IPv6 one, each
// falling back to the other, and other types get none.
//
// It applies only to a request that already carries EDNS. A client that sent
// no OPT must not receive one (RFC 6891), and adding one here would need the
// response stripped of an OPT the client never saw; declining keeps that
// client's exchange untouched. StripECS takes precedence and sends nothing.
func ecsOverride(settings control.DNSPolicySettings, request *dns.Msg) *dns.EDNS0_SUBNET {
	if settings.StripECS || (settings.ECSIPv4 == "" && settings.ECSIPv6 == "") {
		return nil
	}
	if len(request.Question) != 1 || request.IsEdns0() == nil {
		return nil
	}
	var order []string
	switch request.Question[0].Qtype {
	case dns.TypeA:
		order = []string{settings.ECSIPv4, settings.ECSIPv6}
	case dns.TypeAAAA:
		order = []string{settings.ECSIPv6, settings.ECSIPv4}
	default:
		return nil
	}
	for _, value := range order {
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			continue
		}
		addr := prefix.Addr()
		return dnsutils.NewEDNS0Subnet(addr.AsSlice(), uint8(prefix.Bits()), addr.Is6())
	}
	return nil
}

// applyECSOverride replaces the request's client subnet with the user's.
func applyECSOverride(settings control.DNSPolicySettings, request *dns.Msg) {
	if subnet := ecsOverride(settings, request); subnet != nil {
		dnsutils.AddECS(request.IsEdns0(), subnet, true)
	}
}

// clearOverriddenECS removes the ECS option from a response to an overridden
// request. The upstream echoes the override's family and prefix, which no
// longer match what the client sent, and RFC 7871 requires them to match.
// A response without ECS is valid and only drops the scope hint. The OPT
// itself stays, since the client sent one.
func clearOverriddenECS(settings control.DNSPolicySettings, request, response *dns.Msg) {
	if response != nil && ecsOverride(settings, request) != nil {
		dnsutils.RemoveMsgECS(response)
	}
}
