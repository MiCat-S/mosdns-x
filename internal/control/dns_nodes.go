package control

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DNSNode advertises a node's public DoH endpoint. It contains no credentials.
type DNSNode struct {
	Name         string `json:"name" yaml:"name"`
	PublicDNSURL string `json:"public_dns_url" yaml:"public_dns_url"`
}

// ValidateDNSNodes validates configuration or incoming snapshot metadata.
// Development permits the same loopback HTTP endpoints as the panel API.
func ValidateDNSNodes(nodes []DNSNode, development bool) error {
	if len(nodes) > 16 {
		return fmt.Errorf("%w: at most 16 DNS nodes are allowed", ErrInvalidInput)
	}
	names, addresses := map[string]bool{}, map[string]bool{}
	for _, node := range nodes {
		if utf8.RuneCountInString(node.Name) < 1 || utf8.RuneCountInString(node.Name) > 32 || strings.TrimSpace(node.Name) != node.Name || strings.ContainsFunc(node.Name, unicode.IsControl) {
			return fmt.Errorf("%w: DNS node names must contain 1 to 32 characters without surrounding whitespace or control characters", ErrInvalidInput)
		}
		name := strings.ToLower(node.Name)
		if names[name] {
			return fmt.Errorf("%w: duplicate DNS node name", ErrInvalidInput)
		}
		names[name] = true
		u, err := url.Parse(node.PublicDNSURL)
		if err != nil || len(node.PublicDNSURL) > 2048 || u.Hostname() == "" || u.User != nil || u.Path == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(node.PublicDNSURL, "#") || u.Opaque != "" {
			return fmt.Errorf("%w: DNS node URL must have a host and path without credentials, query or fragment", ErrInvalidInput)
		}
		ip, _ := netip.ParseAddr(u.Hostname())
		if u.Scheme != "https" && !(development && u.Scheme == "http" && (u.Hostname() == "localhost" || ip.IsLoopback())) {
			return fmt.Errorf("%w: DNS node URL requires HTTPS or development loopback HTTP", ErrInvalidInput)
		}
		// Joining a credential removes trailing slashes, so these bases produce
		// the same personal address. Scheme and host are case-insensitive.
		address := strings.ToLower(u.Scheme+"://"+u.Host) + strings.TrimRight(u.Path, "/")
		if addresses[address] {
			return fmt.Errorf("%w: duplicate DNS node URL", ErrInvalidInput)
		}
		addresses[address] = true
	}
	return nil
}
