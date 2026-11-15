package runtimeconfig

import (
	"errors"
	"fmt"

	"github.com/pmkol/mosdns-x/pkg/upstream/dialer"
	upstreamtrace "github.com/pmkol/mosdns-x/pkg/upstream/trace"
)

// Recursive contains the parameters the panel can edit. Paths, root sources
// and socket options remain in the main configuration and survive Apply.
type Recursive struct {
	Label            string `json:"label,omitempty" yaml:"label,omitempty"`
	DisableDNSSEC    bool   `json:"disable_dnssec" yaml:"disable_dnssec"`
	DisableQNameMin  bool   `json:"disable_qname_min" yaml:"disable_qname_min"`
	NoIPv4           bool   `json:"no_ipv4" yaml:"no_ipv4"`
	NoIPv6           bool   `json:"no_ipv6" yaml:"no_ipv6"`
	CacheSize        int    `json:"cache_size" yaml:"cache_size"`
	LocalRoot        bool   `json:"local_root" yaml:"local_root"`
	OpportunisticDoT bool   `json:"opportunistic_dot" yaml:"opportunistic_dot"`
	Prefetch         bool   `json:"prefetch" yaml:"prefetch"`
	PrefetchKey      bool   `json:"prefetch_key" yaml:"prefetch_key"`
	// As with Upstream.Proxy, the managed file stores the URL while JSON
	// carries only write-only ProxySettings.
	Proxy         string         `json:"-" yaml:"proxy,omitempty"`
	ProxySettings *ProxySettings `json:"proxy,omitempty" yaml:"-"`
}

var recursiveEditableKeys = []string{
	"label", "disable_dnssec", "disable_qname_min", "no_ipv4", "no_ipv6",
	"cache_size", "local_root", "opportunistic_dot", "prefetch", "prefetch_key", "proxy",
	"socks5", "s5_username", "s5_password", // replaced by the normalized proxy
}

var recursiveKeys = map[string]struct{}{
	"label": {}, "disable_dnssec": {}, "disable_qname_min": {}, "no_ipv4": {}, "no_ipv6": {},
	"cache_size": {}, "local_root": {}, "opportunistic_dot": {}, "prefetch": {}, "prefetch_key": {},
	"proxy": {}, "socks5": {}, "s5_username": {}, "s5_password": {},
	"root_hints": {}, "trust_anchor": {}, "local_root_source": {}, "local_root_file": {},
	"auto_trust_anchor_file": {}, "so_mark": {}, "bind_to_device": {},
}

func inspectRecursive(args any) (*Recursive, error) {
	raw, err := argsMap(args)
	if err != nil || !onlyKeys(raw, recursiveKeys) {
		return nil, errUnknownArgs
	}
	var decoded struct {
		Recursive  `yaml:",inline"`
		Socks5     string `yaml:"socks5"`
		S5Username string `yaml:"s5_username"`
		S5Password string `yaml:"s5_password"`
	}
	if err := decode(raw, &decoded); err != nil {
		return nil, errUnknownArgs
	}
	proxy, err := normalizeProxy(decoded.Proxy, decoded.Socks5, decoded.S5Username, decoded.S5Password)
	if err != nil {
		return nil, errUnknownArgs
	}
	decoded.Proxy = proxy
	if err := validateRecursive(decoded.Recursive); err != nil {
		return nil, errUnknownArgs
	}
	return &decoded.Recursive, nil
}

func validateRecursive(config Recursive) error {
	if !upstreamtrace.ValidLabel(config.Label) {
		return fmt.Errorf("label must be at most %d characters without control characters", upstreamtrace.MaxLabel)
	}
	if config.CacheSize < 0 {
		return errors.New("cache_size cannot be negative")
	}
	if config.NoIPv4 && config.NoIPv6 {
		return errors.New("ipv4 and ipv6 cannot both be disabled")
	}
	if config.ProxySettings != nil {
		return errors.New("proxy settings were not resolved")
	}
	if config.Proxy != "" {
		return dialer.ValidateProxyURL(config.Proxy)
	}
	return nil
}

// normalizeProxy reads the legacy SOCKS fields using the same proxy contract
// as the dialer; the managed representation uses only the URL.
func normalizeProxy(proxy, socks5, username, password string) (string, error) {
	switch {
	case socks5 != "" && proxy != "":
		return "", errUnknownArgs
	case socks5 != "":
		proxy = dialer.ProxySpec{Type: dialer.ProxySocks5, Server: socks5, Username: username, Password: password}.URL()
	case username != "" || password != "":
		return "", errUnknownArgs
	}
	return proxy, nil
}
