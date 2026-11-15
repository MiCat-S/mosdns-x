package runtimeconfig

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func recursiveSource(args map[string]any) []PluginSource {
	return []PluginSource{{Tag: "recursive", Type: "recursive", Args: args}}
}

func recursiveConfig(t *testing.T, args map[string]any) Config {
	t.Helper()
	config, err := Inspect(false, validTelemetry(), recursiveSource(args))
	if err != nil {
		t.Fatal(err)
	}
	if !config.Plugins[0].Editable || config.Plugins[0].Recursive == nil {
		t.Fatalf("recursive is not editable: %+v", config.Plugins[0])
	}
	return config
}

func TestRecursiveApplyPreservesFileAndSocketConfiguration(t *testing.T) {
	args := map[string]any{
		"root_hints": "/etc/mosdns/hints", "trust_anchor": "/etc/mosdns/anchor",
		"local_root_source": "https://root.example/root.zone", "local_root_file": "/var/lib/mosdns/root.zone",
		"auto_trust_anchor_file": "/var/lib/mosdns/root.key", "so_mark": 10, "bind_to_device": "eth0",
		"local_root": true, "prefetch": true, "no_ipv6": true,
		"socks5": "127.0.0.1:1080", "s5_username": "alice", "s5_password": "saved-socks-secret",
	}
	sources := recursiveSource(args)
	config := recursiveConfig(t, args)
	public := PublicView(config)
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"saved-socks-secret", "root_hints", "trust_anchor", "local_root_source", "local_root_file", "auto_trust_anchor_file", "so_mark", "bind_to_device"} {
		if strings.Contains(string(encoded), hidden) {
			t.Fatalf("editable JSON exposed %q: %s", hidden, encoded)
		}
	}
	var desired Config
	if err := json.Unmarshal(encoded, &desired); err != nil {
		t.Fatal(err)
	}
	r := desired.Plugins[0].Recursive
	r.Label, r.CacheSize = "本机解析", 4096
	r.DisableDNSSEC, r.DisableQNameMin, r.NoIPv4, r.NoIPv6 = true, true, true, false
	r.LocalRoot, r.OpportunisticDoT, r.Prefetch, r.PrefetchKey = false, true, false, true
	desired, err = ResolveProxies(config, desired)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(sources, desired)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := argsMap(applied[0].Args)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"root_hints", "trust_anchor", "local_root_source", "local_root_file", "auto_trust_anchor_file", "so_mark", "bind_to_device"} {
		if !reflect.DeepEqual(raw[key], args[key]) {
			t.Fatalf("file-only %s changed: %v -> %v", key, args[key], raw[key])
		}
	}
	for _, key := range []string{"socks5", "s5_username", "s5_password"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("legacy field %q survived proxy normalization", key)
		}
	}
	if raw["proxy"] != "socks5://alice:saved-socks-secret@127.0.0.1:1080" {
		t.Fatal("unchanged proxy lost its saved secret")
	}
	view, err := Inspect(false, validTelemetry(), applied)
	if err != nil || !reflect.DeepEqual(view, desired) {
		t.Fatalf("applied recursive view differs: %+v, %v", view, err)
	}
	if equal, err := ImmutableArgsEqual(sources[0], applied[0]); err != nil || !equal {
		t.Fatalf("editable changes required restart: %v, %v", equal, err)
	}
	raw["local_root_file"] = "/other/root.zone"
	applied[0].Args = raw
	if equal, err := ImmutableArgsEqual(sources[0], applied[0]); err != nil || equal {
		t.Fatalf("file path change allowed hot reload: %v, %v", equal, err)
	}
}

func TestRecursiveProxySecretsSurviveHistoryAndCannotMoveToAnotherEndpoint(t *testing.T) {
	secretURL := "ss://2022-blake3-aes-128-gcm:c2VjcmV0LWtleS0xNmJ5dA%3D%3D@proxy.example:8388"
	config := recursiveConfig(t, map[string]any{"proxy": secretURL})
	public := PublicView(config)
	if public.Plugins[0].Recursive.ProxySettings == nil || !public.Plugins[0].Recursive.ProxySettings.PasswordSet || public.Plugins[0].Recursive.ProxySettings.Password != "" {
		t.Fatal("proxy secret was not presented as write-only")
	}
	resolved, err := ResolveProxies(config, public)
	if err != nil || !reflect.DeepEqual(resolved, config) {
		t.Fatalf("unchanged resubmit altered saved proxy: %v", err)
	}
	changed := PublicView(config)
	changed.Plugins[0].Recursive.ProxySettings.Server = "other.example:8388"
	if _, err := ResolveProxies(config, changed); err == nil || strings.Contains(err.Error(), "c2VjcmV0") {
		t.Fatalf("saved key moved to another server or leaked: %v", err)
	}
	store, err := NewStore(filepath.Join(t.TempDir(), "managed.yaml"), 10)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := store.Write("", config)
	if err != nil {
		t.Fatal(err)
	}
	read, readRev, err := store.Read()
	if err != nil || readRev != rev || !reflect.DeepEqual(read, config) {
		t.Fatalf("managed recursive config changed after restart: %+v, %v", read, err)
	}
	disabled := PublicView(config)
	disabled.Plugins[0].Recursive.ProxySettings = nil
	disabled, err = ResolveProxies(config, disabled)
	if err != nil || disabled.Plugins[0].Recursive.Proxy != "" {
		t.Fatalf("cannot disable proxy: %v", err)
	}
	if _, err := store.Write(rev, disabled); err != nil {
		t.Fatal(err)
	}
	old, err := store.LoadRevision(rev)
	if err != nil || !reflect.DeepEqual(old, config) {
		t.Fatalf("rollback lost the original proxy: %+v, %v", old, err)
	}
	data, err := json.Marshal(PublicView(old))
	if err != nil || strings.Contains(string(data), "c2VjcmV0") {
		t.Fatalf("history's public view leaked a secret: %s, %v", data, err)
	}
}

func TestRecursiveValidationAndLegacyReadOnlyUpgrade(t *testing.T) {
	for name, recursive := range map[string]Recursive{
		"negative cache":   {CacheSize: -1},
		"no families":      {NoIPv4: true, NoIPv6: true},
		"label too long":   {Label: strings.Repeat("界", 65)},
		"control label":    {Label: "bad\nlabel"},
		"unresolved proxy": {ProxySettings: &ProxySettings{Type: "socks5", Server: "proxy.example:1080"}},
		"invalid proxy":    {Proxy: "ss://2022-blake3-aes-128-gcm:invalid-key@proxy.example:8388"},
	} {
		t.Run(name, func(t *testing.T) {
			config := Config{Version: Version, Telemetry: validTelemetry(), Plugins: []Plugin{{Tag: "recursive", Type: "recursive", Editable: true, Recursive: &recursive}}}
			if err := Validate(config); err == nil || strings.Contains(err.Error(), "invalid-key") {
				t.Fatalf("invalid config accepted or secret leaked: %v", err)
			}
		})
	}
	unknown, err := Inspect(false, validTelemetry(), recursiveSource(map[string]any{"future_option": true}))
	if err != nil || unknown.Plugins[0].Editable || unknown.Plugins[0].ReadOnlyReason != "unsupported_parameters" {
		t.Fatalf("unknown recursive option did not stay read-only: %+v, %v", unknown, err)
	}
	sources := recursiveSource(map[string]any{"local_root": true, "local_root_file": "/etc/mosdns/root.zone", "prefetch": true, "no_ipv6": true})
	var stored Config
	if err := yaml.Unmarshal([]byte("version: 1\ntelemetry:\n  aggregate_retention_days: 7\n  query_retention_hours: 24\n  max_query_records: 100000\nplugins:\n  - tag: recursive\n    type: recursive\n    editable: false\n    read_only_reason: unsupported_type\n"), &stored); err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(sources, stored)
	if err != nil || !reflect.DeepEqual(applied, sources) {
		t.Fatalf("upgrade changed old read-only source: %+v, %v", applied, err)
	}
}
