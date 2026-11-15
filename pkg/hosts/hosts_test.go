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

package hosts

import (
	"bytes"
	"net"
	"slices"
	"sync"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/matcher/domain"
)

var test_hosts = `
# comment
     # empty line
dns.google 8.8.8.8 8.8.4.4 2001:4860:4860::8844 2001:4860:4860::8888
regexp:^123456789 192.168.1.1
test.com 1.2.3.4 # will be replaced
test.com 2.3.4.5 
# nxdomain.com 1.2.3.4
`

func Test_hostsContainer_Match(t *testing.T) {
	m := domain.NewMixMatcher[*IPs]()
	m.SetDefaultMatcher(domain.MatcherDomain)
	err := domain.LoadFromTextReader[*IPs](m, bytes.NewBuffer([]byte(test_hosts)), ParseIPs)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHosts(m)

	type args struct {
		name string
		typ  uint16
	}
	tests := []struct {
		name        string
		args        args
		wantMatched bool
		wantAddr    []string
	}{
		{"matched A", args{name: "dns.google.", typ: dns.TypeA}, true, []string{"8.8.8.8", "8.8.4.4"}},
		{"matched AAAA", args{name: "dns.google.", typ: dns.TypeAAAA}, true, []string{"2001:4860:4860::8844", "2001:4860:4860::8888"}},
		{"not matched A", args{name: "nxdomain.com.", typ: dns.TypeA}, false, nil},
		{"not matched A", args{name: "sub.dns.google.", typ: dns.TypeA}, false, nil},
		{"matched regexp A", args{name: "123456789.test.", typ: dns.TypeA}, true, []string{"192.168.1.1"}},
		{"not matched regexp A", args{name: "0123456789.test.", typ: dns.TypeA}, false, nil},
		{"test replacement", args{name: "test.com.", typ: dns.TypeA}, true, []string{"2.3.4.5"}},
		{"test matched domain with mismatched type", args{name: "test.com.", typ: dns.TypeAAAA}, true, nil},
	}
	for _, tt := range tests {
		q := new(dns.Msg)
		q.SetQuestion(tt.args.name, tt.args.typ)

		t.Run(tt.name, func(t *testing.T) {
			r := h.LookupMsg(q)
			if tt.wantMatched && r == nil {
				t.Fatal("Lookup() should not return a nil result")
			}

			for _, s := range tt.wantAddr {
				wantIP := net.ParseIP(s)
				if wantIP == nil {
					t.Fatal("invalid test case addr")
				}
				found := false
				for _, rr := range r.Answer {
					var ip net.IP
					switch rr := rr.(type) {
					case *dns.A:
						ip = rr.A
					case *dns.AAAA:
						ip = rr.AAAA
					default:
						continue
					}
					if ip.Equal(wantIP) {
						found = true
						break
					}
				}
				if !found {
					t.Fatal("wanted ip is not found in response")
				}
			}
		})
	}
}

func TestLookupMsgLeavesSharedAddressesAlone(t *testing.T) {
	m := domain.NewMixMatcher[*IPs]()
	m.SetDefaultMatcher(domain.MatcherDomain)
	const list = "many.example 192.0.2.1 192.0.2.2 192.0.2.3 192.0.2.4 192.0.2.5 192.0.2.6 192.0.2.7 192.0.2.8\n"
	if err := domain.LoadFromTextReader[*IPs](m, bytes.NewBufferString(list), ParseIPs); err != nil {
		t.Fatal(err)
	}
	h := NewHosts(m)
	before, _ := h.Lookup("many.example.")
	want := slices.Clone(before)

	q := new(dns.Msg)
	q.SetQuestion("many.example.", dns.TypeA)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if r := h.LookupMsg(q); len(r.Answer) != len(want) {
					t.Errorf("got %d answers, want %d", len(r.Answer), len(want))
					return
				}
			}
		}()
	}
	wg.Wait()

	after, _ := h.Lookup("many.example.")
	if !slices.Equal(after, want) {
		t.Fatalf("LookupMsg reordered the shared address list: %v, want %v", after, want)
	}
}
