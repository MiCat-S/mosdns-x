package dnsutils

import "testing"

func TestNormalizeDomain(t *testing.T) {
	valid := map[string]string{
		"example.com":            "example.com.",
		" WWW.Example.COM. ":     "www.example.com.",
		"_dmarc.example.com":     "_dmarc.example.com.",
		"例子.中国":                  "xn--fsqu00a.xn--fiqs8s.",
		"xn--fsqu00a.xn--fiqs8s": "xn--fsqu00a.xn--fiqs8s.",
		"localhost":              "localhost.",
	}
	for in, want := range valid {
		got, err := NormalizeDomain(in)
		if err != nil || got != want {
			t.Errorf("NormalizeDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", " ", ".", "*.example.com", "example..com", ".example.com",
		"https://example.com/", "exa mple.com", "example.com:443",
		string(make([]byte, 64)) + ".com",
	} {
		if got, err := NormalizeDomain(in); err == nil {
			t.Errorf("NormalizeDomain(%q) = %q, want an error", in, got)
		}
	}
}
