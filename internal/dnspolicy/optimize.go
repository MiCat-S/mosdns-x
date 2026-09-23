package dnspolicy

import (
	"math/rand/v2"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/control"
)

// optimizeResponse applies the user's answer rewrites to response in place.
//
// In place is deliberate. The entry handler treats a different response
// object as a local replacement and marks the upstream result discarded, which
// would misattribute every optimized answer in the query log. It is also safe:
// the cache packs a response to bytes before this runs and unpacks a fresh
// object on every hit, and its singleflight only drives background refreshes
// on a copy. No response object reaches two clients.
func optimizeResponse(settings control.DNSPolicySettings, request, response *dns.Msg) {
	if response == nil || response.Rcode != dns.RcodeSuccess || len(response.Answer) == 0 || len(request.Question) != 1 {
		return
	}
	question := request.Question[0]
	if settings.FlattenCNAME {
		flattenCNAME(response, question.Name)
	}
	if settings.ShuffleAnswers && (question.Qtype == dns.TypeA || question.Qtype == dns.TypeAAAA) {
		shuffleAddresses(response)
	}
	if settings.TTLMin > 0 || settings.TTLMax > 0 {
		clampTTL(response, settings.TTLMin, settings.TTLMax)
	}
}

// flattenCNAME drops the CNAME chain and gives the remaining records the
// queried name. It leaves the answer alone when there is no address to keep,
// since removing the chain would then return nothing, and when a DNAME is
// present, since its synthesized CNAMEs are what make the answer valid.
func flattenCNAME(msg *dns.Msg, qname string) {
	hasAddress := false
	for _, rr := range msg.Answer {
		switch rr.Header().Rrtype {
		case dns.TypeDNAME:
			return
		case dns.TypeA, dns.TypeAAAA:
			hasAddress = true
		}
	}
	if !hasAddress {
		return
	}
	kept := msg.Answer[:0]
	for _, rr := range msg.Answer {
		if rr.Header().Rrtype == dns.TypeCNAME {
			continue
		}
		rr.Header().Name = qname
		kept = append(kept, rr)
	}
	msg.Answer = kept
}

// shuffleAddresses permutes the A and AAAA records among the positions they
// already occupy, so a CNAME that precedes them stays in front.
func shuffleAddresses(msg *dns.Msg) {
	var positions []int
	for i, rr := range msg.Answer {
		if t := rr.Header().Rrtype; t == dns.TypeA || t == dns.TypeAAAA {
			positions = append(positions, i)
		}
	}
	if len(positions) < 2 {
		return
	}
	records := make([]dns.RR, len(positions))
	for i, pos := range positions {
		records[i] = msg.Answer[pos]
	}
	rand.Shuffle(len(records), func(i, j int) { records[i], records[j] = records[j], records[i] })
	for i, pos := range positions {
		msg.Answer[pos] = records[i]
	}
}

// clampTTL bounds answer record TTLs. A zero bound is open. The authority
// section is left alone, so an SOA keeps governing negative caching.
func clampTTL(msg *dns.Msg, min, max uint32) {
	for _, rr := range msg.Answer {
		hdr := rr.Header()
		if min > 0 && hdr.Ttl < min {
			hdr.Ttl = min
		}
		if max > 0 && hdr.Ttl > max {
			hdr.Ttl = max
		}
	}
}
