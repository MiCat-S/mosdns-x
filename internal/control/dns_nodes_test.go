package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDNSNodeValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nodes      []DNSNode
		dev, valid bool
	}{
		{"empty", nil, false, true},
		{"public", []DNSNode{{"广州", "https://gz.example/dns-query"}, {"成都", "https://cd.example/custom/path/"}}, false, true},
		{"root path", []DNSNode{{"Root", "https://dns.example/"}}, false, true},
		{"32 Unicode chars", []DNSNode{{strings.Repeat("广", 32), "https://dns.example/dns-query"}}, false, true},
		{"long name", []DNSNode{{strings.Repeat("广", 33), "https://dns.example/dns-query"}}, false, false},
		{"no name", []DNSNode{{"", "https://dns.example/dns-query"}}, false, false},
		{"spaces", []DNSNode{{" 广州", "https://dns.example/dns-query"}}, false, false},
		{"control", []DNSNode{{"广\n州", "https://dns.example/dns-query"}}, false, false},
		{"duplicate names", []DNSNode{{"GZ", "https://gz.example/dns-query"}, {"gz", "https://cd.example/dns-query"}}, false, false},
		{"duplicate endpoints", []DNSNode{{"one", "https://DNS.example/dns-query"}, {"two", "https://dns.example/dns-query/"}}, false, false},
		{"no path", []DNSNode{{"GZ", "https://dns.example"}}, false, false},
		{"plain HTTP", []DNSNode{{"GZ", "http://dns.example/dns-query"}}, true, false},
		{"production loopback", []DNSNode{{"local", "http://localhost/dns-query"}}, false, false},
		{"development loopback", []DNSNode{{"local", "http://127.0.0.1/dns-query"}}, true, true},
		{"credentials", []DNSNode{{"GZ", "https://user:secret@dns.example/dns-query"}}, false, false},
		{"query", []DNSNode{{"GZ", "https://dns.example/dns-query?"}}, false, false},
		{"fragment", []DNSNode{{"GZ", "https://dns.example/dns-query#"}}, false, false},
		{"over capacity", make([]DNSNode, 17), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateDNSNodes(tc.nodes, tc.dev); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestSnapshotDNSNodesAreOptionalAndValidated(t *testing.T) {
	snapshot := identityTestSnapshot(time.Now().UTC())
	snapshot.Nodes = []DNSNode{{"广州", "https://gz.example/dns-query"}, {"成都", "https://cd.example/dns-query"}}
	wire, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeIdentitySnapshot(bytes.NewReader(wire))
	if err != nil || len(decoded.Nodes) != 2 || decoded.Nodes[1].Name != "成都" {
		t.Fatalf("roundtrip nodes=%v err=%v", decoded.Nodes, err)
	}
	var legacy map[string]json.RawMessage
	if err = json.Unmarshal(wire, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "nodes")
	wire, _ = json.Marshal(legacy)
	decoded, err = DecodeIdentitySnapshot(bytes.NewReader(wire))
	if err != nil || len(decoded.Nodes) != 0 {
		t.Fatalf("legacy snapshot nodes=%v err=%v", decoded.Nodes, err)
	}
	snapshot.Nodes = []DNSNode{{"invalid", "https://secret@dns.example/dns-query"}}
	wire, _ = json.Marshal(snapshot)
	if _, err = DecodeIdentitySnapshot(bytes.NewReader(wire)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad metadata accepted: %v", err)
	}
}
