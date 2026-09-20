package runtimeconfig

import "testing"

func TestSanitizeEndpointRedactsCredentialPath(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"device url path is a bearer token", "addr", "https://dns.example/dns-query/8b1f0c4e-2a6d-4f3b-9c5a-7d8e9f0a1b2c", "https://dns.example/dns-query/[redacted]"},
		{"single segment endpoint is kept", "addr", "https://dns.example/dns-query", "https://dns.example/dns-query"},
		{"root path is kept", "addr", "https://dns.example/", "https://dns.example/"},
		{"no path is kept", "addr", "https://dns.example", "https://dns.example"},
		{"deep path is fully redacted", "url", "https://dns.example/a/b/c", "https://dns.example/a/[redacted]/[redacted]"},
		{"query is still redacted", "addr", "https://dns.example/dns-query/tok?k=v", "https://dns.example/dns-query/[redacted]?k=%5Bredacted%5D"},
		{"userinfo is still dropped", "addr", "https://u:p@dns.example/dns-query/tok", "https://dns.example/dns-query/[redacted]"},
		// Pre-existing behavior: url.Parse rejects a bare host:port, so it is
		// redacted wholesale. Pinned here so the path change does not alter it.
		{"unparsable host:port stays fully redacted", "addr", "8.8.8.8:53", "[redacted]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeEndpoint(tt.key, tt.value); got != tt.want {
				t.Fatalf("sanitizeEndpoint(%q, %q) = %q, want %q", tt.key, tt.value, got, tt.want)
			}
		})
	}
}
