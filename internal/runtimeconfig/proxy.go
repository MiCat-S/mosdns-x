package runtimeconfig

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pmkol/mosdns-x/pkg/upstream/dialer"
)

// ProxySettings is how the panel sees and edits an upstream's proxy. The
// managed file keeps the full URL in Upstream.Proxy, which never leaves the
// server: responses carry these settings with Password empty and
// PasswordSet true, and a request that leaves Password empty keeps the
// saved one, but only for the same type, server, method and username, so
// a saved secret cannot be pointed at another server.
type ProxySettings struct {
	Type     string `json:"type"`               // "shadowsocks" or "socks5"
	Server   string `json:"server"`             // host:port
	Method   string `json:"method,omitempty"`   // Shadowsocks cipher
	Username string `json:"username,omitempty"` // SOCKS5
	// Password is the Shadowsocks key or SOCKS5 password. Write-only.
	Password    string `json:"password,omitempty"`
	PasswordSet bool   `json:"password_set,omitempty"`
}

// PublicView returns a copy of config for the panel: every proxy URL is
// replaced by settings without the secret.
func PublicView(config Config) Config {
	result := config
	result.Plugins = make([]Plugin, len(config.Plugins))
	for i, plugin := range config.Plugins {
		if plugin.FastForward != nil {
			forward := *plugin.FastForward
			forward.Upstreams = make([]Upstream, len(plugin.FastForward.Upstreams))
			for j, upstream := range plugin.FastForward.Upstreams {
				upstream.ProxySettings = nil
				if upstream.Proxy != "" {
					// Inspect and Validate only admit proxies that parse.
					if spec, err := dialer.ParseProxyURL(upstream.Proxy); err == nil {
						upstream.ProxySettings = &ProxySettings{
							Type: spec.Type, Server: spec.Server, Method: spec.Method,
							Username: spec.Username, PasswordSet: spec.Password != "",
						}
					}
				}
				upstream.Proxy = ""
				forward.Upstreams[j] = upstream
			}
			plugin.FastForward = &forward
		}
		result.Plugins[i] = plugin
	}
	return result
}

// ResolveProxies turns the panel's proxy settings in desired into proxy
// URLs, taking a password left empty from the matching proxy saved in
// current. Upstreams without settings keep their URL as given (rollback
// passes stored configs); the panel cannot send one, as Proxy is never
// decoded from JSON.
func ResolveProxies(current, desired Config) (Config, error) {
	editable := make(map[string]bool, len(current.Plugins))
	for _, plugin := range current.Plugins {
		editable[plugin.Tag] = plugin.Editable
	}
	for _, plugin := range desired.Plugins {
		// Apply tolerates read-only entries for editable plugins so managed
		// files from older versions still load; a request may not send one.
		if editable[plugin.Tag] && !plugin.Editable {
			return Config{}, fmt.Errorf("editable plugin %q cannot be changed to read-only", plugin.Tag)
		}
	}
	saved := make(map[string][]savedProxy)
	for _, plugin := range current.Plugins {
		if plugin.FastForward == nil {
			continue
		}
		for j, upstream := range plugin.FastForward.Upstreams {
			if spec, err := dialer.ParseProxyURL(upstream.Proxy); upstream.Proxy != "" && err == nil {
				saved[plugin.Tag] = append(saved[plugin.Tag], savedProxy{spec: spec, url: upstream.Proxy, addr: upstream.Addr, index: j})
			}
		}
	}
	result := desired
	result.Plugins = make([]Plugin, len(desired.Plugins))
	for i, plugin := range desired.Plugins {
		if plugin.FastForward != nil {
			forward := *plugin.FastForward
			forward.Upstreams = make([]Upstream, len(plugin.FastForward.Upstreams))
			for j, upstream := range plugin.FastForward.Upstreams {
				if upstream.ProxySettings != nil {
					url, err := resolveProxy(*upstream.ProxySettings, saved[plugin.Tag], upstream.Addr, j)
					if err != nil {
						return Config{}, fmt.Errorf("plugin %q upstream #%d proxy: %w", plugin.Tag, j, err)
					}
					upstream.Proxy, upstream.ProxySettings = url, nil
				}
				forward.Upstreams[j] = upstream
			}
			plugin.FastForward = &forward
		}
		result.Plugins[i] = plugin
	}
	return result, nil
}

type savedProxy struct {
	spec  dialer.ProxySpec
	url   string
	addr  string // the upstream it belongs to
	index int    // its position in the plugin's upstream list
}

func sameEndpoint(a, b dialer.ProxySpec) bool {
	return a.Type == b.Type && a.Server == b.Server && a.Method == b.Method && a.Username == b.Username
}

func resolveProxy(settings ProxySettings, saved []savedProxy, addr string, index int) (string, error) {
	spec := dialer.ProxySpec{
		Type: settings.Type, Server: strings.TrimSpace(settings.Server),
		Method:   strings.ToLower(strings.TrimSpace(settings.Method)),
		Username: settings.Username, Password: settings.Password,
	}
	switch spec.Type {
	case dialer.ProxyShadowsocks:
		if spec.Username != "" {
			return "", fmt.Errorf("shadowsocks takes no username")
		}
		if !slices.Contains(dialer.ShadowsocksMethods(), spec.Method) {
			return "", fmt.Errorf("unsupported shadowsocks method")
		}
	case dialer.ProxySocks5:
		if spec.Method != "" {
			return "", fmt.Errorf("socks5 takes no method")
		}
	default:
		return "", fmt.Errorf("unsupported proxy type")
	}
	// An empty password means "keep the saved one" when one can apply: for
	// Shadowsocks always, for SOCKS5 when the panel showed one as saved or
	// a username is set. Several upstreams may share a server with
	// different keys (multi-user servers), so the upstream's own saved
	// proxy is found by its address first (positions shift when an earlier
	// upstream is deleted), then by position; failing that, exactly one
	// saved password for this endpoint must exist.
	if spec.Password == "" && (spec.Type == dialer.ProxyShadowsocks || settings.PasswordSet || spec.Username != "") {
		password, ok := savedPassword(spec, saved, addr, index)
		switch {
		case ok:
			spec.Password = password
		case spec.Type == dialer.ProxyShadowsocks:
			return "", fmt.Errorf("enter the key: no single saved key matches this server and method")
		}
		// A SOCKS5 proxy without a matching saved password stays without one.
	}
	// An unchanged proxy keeps its saved URL byte for byte, so an untouched
	// draft compares equal to the running config.
	for _, candidate := range saved {
		if candidate.spec == spec {
			return candidate.url, nil
		}
	}
	url := spec.URL()
	if err := dialer.ValidateProxyURL(url); err != nil {
		return "", err
	}
	return url, nil
}

// savedPassword picks the saved password for an endpoint, preferring the
// same upstream address, then the same position, then the only distinct
// password saved for the endpoint.
func savedPassword(spec dialer.ProxySpec, saved []savedProxy, addr string, index int) (string, bool) {
	unique := func(keep func(savedProxy) bool) (string, bool) {
		var passwords []string
		for _, candidate := range saved {
			if sameEndpoint(candidate.spec, spec) && keep(candidate) && !slices.Contains(passwords, candidate.spec.Password) {
				passwords = append(passwords, candidate.spec.Password)
			}
		}
		if len(passwords) == 1 {
			return passwords[0], true
		}
		return "", false
	}
	for _, keep := range []func(savedProxy) bool{
		func(c savedProxy) bool { return c.addr == addr && c.index == index },
		func(c savedProxy) bool { return c.addr == addr },
		func(c savedProxy) bool { return c.index == index },
		func(savedProxy) bool { return true },
	} {
		if password, ok := unique(keep); ok {
			return password, true
		}
	}
	return "", false
}
