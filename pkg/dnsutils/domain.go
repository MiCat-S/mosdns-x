package dnsutils

import (
	"errors"
	"strings"

	"golang.org/x/net/idna"
)

// NormalizeDomain checks a domain typed by a person and returns it as a lower
// case FQDN. Internationalized names are converted to their ASCII form. Only
// host name characters and "_" are accepted, and the root is refused, so the
// result can safely be matched label by label.
func NormalizeDomain(s string) (string, error) {
	name := strings.TrimSpace(s)
	if name == "" {
		return "", errors.New("domain is empty")
	}
	for i := 0; i < len(name); i++ {
		if name[i] >= 0x80 {
			ascii, err := idna.Lookup.ToASCII(name)
			if err != nil {
				return "", errors.New("invalid internationalized domain")
			}
			name = ascii
			break
		}
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" {
		return "", errors.New("the root domain is not allowed")
	}
	if len(name) > 253 {
		return "", errors.New("domain is too long")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return "", errors.New("invalid domain label")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
				return "", errors.New("invalid character in domain")
			}
		}
	}
	return name + ".", nil
}
