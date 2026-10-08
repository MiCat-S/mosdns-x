package runtimeconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A valid 2022 key (16 bytes) that tests look for in encoded output.
const testKey = "c2VjcmV0LWtleS0xNmJ5dA=="

func forwardSource(upstreams ...map[string]any) []PluginSource {
	items := make([]any, len(upstreams))
	for i, upstream := range upstreams {
		items[i] = upstream
	}
	return []PluginSource{{Tag: "forward", Type: "fast_forward", Args: map[string]any{"upstream": items}}}
}

func TestInspectMakesProxiedUpstreamsEditable(t *testing.T) {
	ssURL := "ss://2022-blake3-aes-128-gcm:" + strings.ReplaceAll(testKey, "=", "%3D") + "@hk.example.net:8388"
	config, err := Inspect(false, validTelemetry(), forwardSource(
		map[string]any{"addr": "udp://192.0.2.1", "proxy": ssURL},
		map[string]any{"addr": "udp://192.0.2.2", "socks5": "127.0.0.1:1080", "s5_username": "u", "s5_password": "p"},
	))
	if err != nil {
		t.Fatal(err)
	}
	plugin := config.Plugins[0]
	if !plugin.Editable || plugin.FastForward == nil {
		t.Fatalf("proxied upstreams should be editable: %+v", plugin)
	}
	if got := plugin.FastForward.Upstreams[0].Proxy; got != ssURL {
		t.Fatalf("proxy = %q", got)
	}
	if got := plugin.FastForward.Upstreams[1].Proxy; got != "socks5://u:p@127.0.0.1:1080" {
		t.Fatalf("legacy socks5 fields became %q", got)
	}

	for name, upstream := range map[string]map[string]any{
		"invalid key":        {"addr": "udp://192.0.2.1", "proxy": "ss://2022-blake3-aes-128-gcm:short@h:1"},
		"proxy and socks5":   {"addr": "udp://192.0.2.1", "proxy": ssURL, "socks5": "127.0.0.1:1080"},
		"credentials alone":  {"addr": "udp://192.0.2.1", "s5_password": "p"},
		"unsupported scheme": {"addr": "udp://192.0.2.1", "proxy": "http://127.0.0.1:8080"},
	} {
		config, err := Inspect(false, validTelemetry(), forwardSource(upstream))
		if err != nil {
			t.Fatal(err)
		}
		if plugin := config.Plugins[0]; plugin.Editable || plugin.ReadOnlyReason != "unsupported_parameters" {
			t.Fatalf("%s: want read-only, got %+v", name, plugin)
		}
	}
}

func TestPublicViewNeverCarriesProxySecrets(t *testing.T) {
	config, err := Inspect(false, validTelemetry(), forwardSource(
		map[string]any{"addr": "udp://192.0.2.1", "proxy": "ss://2022-blake3-aes-128-gcm:" + strings.ReplaceAll(testKey, "=", "%3D") + "@hk.example.net:8388"},
		map[string]any{"addr": "udp://192.0.2.2", "proxy": "socks5://alice:socks-secret@127.0.0.1:1080"},
		map[string]any{"addr": "udp://192.0.2.3"},
	))
	if err != nil {
		t.Fatal(err)
	}
	// Even the internal view must not encode the URL to JSON.
	for _, value := range []any{config, PublicView(config)} {
		encoded, _ := json.Marshal(value)
		for _, secret := range []string{"c2VjcmV0LWtleS0xNmJ5dA", "socks-secret"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("JSON leaked %q: %s", secret, encoded)
			}
		}
	}
	snapshot, err := Snapshot("running_generation", config, forwardSource(), nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if encoded, _ := json.Marshal(snapshot); strings.Contains(string(encoded), "socks-secret") {
		t.Fatalf("snapshot leaked the password: %s", encoded)
	}
	view := PublicView(config).Plugins[0].FastForward.Upstreams
	if got := *view[0].ProxySettings; got != (ProxySettings{Type: "shadowsocks", Server: "hk.example.net:8388", Method: "2022-blake3-aes-128-gcm", PasswordSet: true}) {
		t.Fatalf("shadowsocks settings = %+v", got)
	}
	if got := *view[1].ProxySettings; got != (ProxySettings{Type: "socks5", Server: "127.0.0.1:1080", Username: "alice", PasswordSet: true}) {
		t.Fatalf("socks5 settings = %+v", got)
	}
	if view[0].Proxy != "" || view[2].ProxySettings != nil {
		t.Fatalf("public view kept a URL or invented settings: %+v", view)
	}
	if config.Plugins[0].FastForward.Upstreams[0].ProxySettings != nil {
		t.Fatal("PublicView changed its input")
	}
}

func TestResolveProxiesKeepsASavedSecretOnlyForTheSameServer(t *testing.T) {
	current, err := Inspect(false, validTelemetry(), forwardSource(
		map[string]any{"addr": "udp://192.0.2.1", "proxy": "ss://2022-blake3-aes-128-gcm:" + strings.ReplaceAll(testKey, "=", "%3D") + "@hk.example.net:8388"},
	))
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(settings ProxySettings) (string, error) {
		desired := PublicView(current)
		desired.Plugins[0].FastForward.Upstreams[0].ProxySettings = &settings
		resolved, err := ResolveProxies(current, desired)
		if err != nil {
			return "", err
		}
		upstream := resolved.Plugins[0].FastForward.Upstreams[0]
		if upstream.ProxySettings != nil {
			t.Fatal("settings left after resolving")
		}
		if err := Validate(resolved); err != nil {
			t.Fatalf("resolved config is invalid: %v", err)
		}
		return upstream.Proxy, nil
	}
	ss := ProxySettings{Type: "shadowsocks", Server: "hk.example.net:8388", Method: "2022-blake3-aes-128-gcm", PasswordSet: true}

	if got, err := resolve(ss); err != nil || got != current.Plugins[0].FastForward.Upstreams[0].Proxy {
		t.Fatalf("an untouched proxy should keep its saved URL exactly: %q %v", got, err)
	}
	untouched, err := ResolveProxies(current, PublicView(current))
	if err != nil || !reflect.DeepEqual(untouched, current) {
		t.Fatalf("an untouched public view should resolve to the running config: %v", err)
	}
	moved := ss
	moved.Server = "attacker.example:8388"
	if _, err := resolve(moved); err == nil {
		t.Fatal("a saved key was reused for another server")
	}
	recipher := ss
	recipher.Method = "2022-blake3-aes-256-gcm"
	if _, err := resolve(recipher); err == nil {
		t.Fatal("a saved key was reused for another method")
	}
	replaced := moved
	replaced.Password = "bmV3LWtleS0xNi1ieXRlcw=="
	if got, err := resolve(replaced); err != nil || !strings.Contains(got, "attacker.example:8388") || !strings.Contains(got, "bmV3LWtleS0xNi1ieXRlcw") {
		t.Fatalf("a new key for a new server: %q %v", got, err)
	}
	if got, err := resolve(ProxySettings{Type: "socks5", Server: "127.0.0.1:1080"}); err != nil || got != "socks5://127.0.0.1:1080" {
		t.Fatalf("socks5 without auth: %q %v", got, err)
	}
	for name, settings := range map[string]ProxySettings{
		"bad key":       {Type: "shadowsocks", Server: "h:1", Method: "2022-blake3-aes-128-gcm", Password: "too-short"},
		"stream cipher": {Type: "shadowsocks", Server: "h:1", Method: "rc4-md5", Password: "x"},
		"no port":       {Type: "socks5", Server: "127.0.0.1"},
		"unknown type":  {Type: "http", Server: "h:1"},
		"ss username":   {Type: "shadowsocks", Server: "h:1", Method: "aes-128-gcm", Username: "u", Password: "x"},
		"socks5 method": {Type: "socks5", Server: "h:1", Method: "aes-128-gcm"},
	} {
		_, err := resolve(settings)
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		if strings.Contains(err.Error(), "too-short") {
			t.Fatalf("%s: error quotes the key: %v", name, err)
		}
	}
}

func TestValidateRejectsUnresolvedOrInvalidProxies(t *testing.T) {
	config := Config{Version: Version, Telemetry: validTelemetry(), Plugins: []Plugin{{
		Tag: "forward", Type: "fast_forward", Editable: true,
		FastForward: &FastForward{Upstreams: []Upstream{{Addr: "udp://192.0.2.1", ProxySettings: &ProxySettings{Type: "socks5", Server: "h:1"}}}},
	}}}
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "not resolved") {
		t.Fatalf("unresolved settings: %v", err)
	}
	config.Plugins[0].FastForward.Upstreams[0] = Upstream{Addr: "udp://192.0.2.1", Proxy: "ss://2022-blake3-aes-128-gcm:short-key@h:1"}
	if err := Validate(config); err == nil || strings.Contains(err.Error(), "short-key") {
		t.Fatalf("invalid key: %v", err)
	}
}

func TestApplyKeepsSourceForPluginsStoredAsReadOnly(t *testing.T) {
	sources := forwardSource(map[string]any{"addr": "udp://192.0.2.1", "socks5": "127.0.0.1:1080"})
	// What an older version stored while socks5 plugins were read-only.
	stored := Config{Version: Version, Telemetry: validTelemetry(), Plugins: []Plugin{{
		Tag: "forward", Type: "fast_forward", ReadOnlyReason: "sensitive_parameters",
	}}}
	applied, err := Apply(sources, stored)
	if err != nil {
		t.Fatalf("upgrade with a read-only entry: %v", err)
	}
	if !reflect.DeepEqual(applied, sources) {
		t.Fatalf("source changed: %+v", applied)
	}
}

func TestResolveProxiesRoundTripsSharedServersAndPasswordOnlySocks5(t *testing.T) {
	keyA := strings.ReplaceAll("c2VjcmV0LWtleS0xNmJ5dA==", "=", "%3D")
	keyB := strings.ReplaceAll("YW5vdGhlci1rZXktMTZieQ==", "=", "%3D")
	current, err := Inspect(false, validTelemetry(), forwardSource(
		// One multi-user server, a different user key per upstream.
		map[string]any{"addr": "udp://192.0.2.1", "proxy": "ss://2022-blake3-aes-128-gcm:" + keyA + "@hk.example.net:8388"},
		map[string]any{"addr": "udp://192.0.2.2", "proxy": "ss://2022-blake3-aes-128-gcm:" + keyB + "@hk.example.net:8388"},
		// SOCKS5 with a password and no username.
		map[string]any{"addr": "udp://192.0.2.3", "socks5": "127.0.0.1:1080", "s5_password": "only-pw"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if !current.Plugins[0].Editable {
		t.Fatalf("plugin not editable: %+v", current.Plugins[0])
	}
	resolved, err := ResolveProxies(current, PublicView(current))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved, current) {
		t.Fatalf("untouched resubmit changed the proxies:\n%+v\n%+v", resolved.Plugins[0].FastForward.Upstreams, current.Plugins[0].FastForward.Upstreams)
	}
	// Deleting the first upstream moves the second into its position; it
	// must keep its own key, not inherit the deleted one's.
	trimmed := PublicView(current)
	trimmed.Plugins[0].FastForward.Upstreams = trimmed.Plugins[0].FastForward.Upstreams[1:]
	resolved, err = ResolveProxies(current, trimmed)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Plugins[0].FastForward.Upstreams[0].Proxy; got != current.Plugins[0].FastForward.Upstreams[1].Proxy {
		t.Fatalf("after deleting upstream #0 the next one uses %q", got)
	}
}

func TestProxyRulesForUDPMEAndReadOnlySubmissions(t *testing.T) {
	config, err := Inspect(false, validTelemetry(), forwardSource(
		map[string]any{"addr": "udpme://1.1.1.1", "proxy": "socks5://127.0.0.1:1080"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if config.Plugins[0].Editable {
		t.Fatal("udpme with a proxy should stay read-only")
	}
	bad := Config{Version: Version, Telemetry: validTelemetry(), Plugins: []Plugin{{
		Tag: "forward", Type: "fast_forward", Editable: true,
		FastForward: &FastForward{Upstreams: []Upstream{{Addr: "udpme://1.1.1.1", Proxy: "socks5://127.0.0.1:1080"}}},
	}}}
	if err := Validate(bad); err == nil || !strings.Contains(err.Error(), "udpme") {
		t.Fatalf("udpme with a proxy: %v", err)
	}

	current, err := Inspect(false, validTelemetry(), forwardSource(map[string]any{"addr": "udp://192.0.2.1"}))
	if err != nil {
		t.Fatal(err)
	}
	readOnly := PublicView(current)
	readOnly.Plugins[0] = Plugin{Tag: "forward", Type: "fast_forward"}
	if _, err := ResolveProxies(current, readOnly); err == nil {
		t.Fatal("a request turned an editable plugin read-only")
	}
}
