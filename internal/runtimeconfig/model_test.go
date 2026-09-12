package runtimeconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func validTelemetry() Telemetry {
	return Telemetry{AggregateRetentionDays: 7, QueryRetentionHours: 24, MaxQueryRecords: 100000}
}

func TestInspectOnlyExposesSafeEditableParameters(t *testing.T) {
	config, err := Inspect(true, validTelemetry(), []PluginSource{
		{Tag: "forward", Type: "fast_forward", Args: map[string]any{"upstream": []any{map[string]any{"addr": "https://dns.example/dns-query", "trusted": true}}}},
		{Tag: "cache", Type: "cache", Args: map[string]any{"size": 1024, "lazy_cache_ttl": 60, "compress_resp": true, "cache_everything": true}},
		{Tag: "secret-forward", Type: "fast_forward", Args: map[string]any{"upstream": []any{map[string]any{"addr": "https://user:password@dns.example/dns-query"}}}},
		{Tag: "scheme-less-secret-forward", Type: "fast_forward", Args: map[string]any{"upstream": []any{map[string]any{"addr": "user:password@dns.example:53"}}}},
		{Tag: "redis", Type: "cache", Args: map[string]any{"redis": "redis://user:password@localhost/0"}},
		{Tag: "unknown", Type: "cache", Args: map[string]any{"size": 1, "future_option": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !config.Plugins[0].Editable || config.Plugins[0].FastForward == nil {
		t.Fatalf("safe forward is not editable: %+v", config.Plugins[0])
	}
	if !config.Plugins[1].Editable || config.Plugins[1].Cache == nil || config.Plugins[1].Cache.Size != 1024 {
		t.Fatalf("safe cache is not editable: %+v", config.Plugins[1])
	}
	for _, index := range []int{2, 3, 4, 5} {
		plugin := config.Plugins[index]
		if plugin.Editable || plugin.FastForward != nil || plugin.Cache != nil {
			t.Fatalf("unsafe plugin leaked parameters: %+v", plugin)
		}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"password", "redis://", "user:"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("response leaked %q: %s", secret, encoded)
		}
	}
}

func TestApplyOverridesEditableFieldsAndPreservesOtherArgs(t *testing.T) {
	sources := []PluginSource{
		{Tag: "forward", Type: "fast_forward", Args: map[string]any{"ca": []string{"ca.pem"}, "upstream": []any{map[string]any{"addr": "udp://127.0.0.1:53"}}}},
		{Tag: "cache", Type: "cache", Args: map[string]any{"size": 128, "cache_everything": true, "when_hit": "metrics"}},
	}
	desired, err := Inspect(false, validTelemetry(), sources)
	if err != nil {
		t.Fatal(err)
	}
	desired.Plugins[0].FastForward.Upstreams[0].Addr = "tcp://127.0.0.1:53"
	desired.Plugins[1].Cache.Size = 512
	desired.Plugins[1].Cache.LazyCacheTTL = 30
	applied, err := Apply(sources, desired)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := yaml.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{"tcp://127.0.0.1:53", "ca.pem", "cache_everything: true", "when_hit: metrics", "size: 512", "lazy_cache_ttl: 30"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("applied args missing %q:\n%s", expected, text)
		}
	}
}

func TestValidateRejectsSensitiveAndOutOfRangeInput(t *testing.T) {
	config := Config{Version: Version, Telemetry: validTelemetry(), Plugins: []Plugin{{
		Tag: "forward", Type: "fast_forward", Editable: true,
		FastForward: &FastForward{Upstreams: []Upstream{{Addr: "https://user:secret@dns.example/dns-query"}}},
	}}}
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("Validate error = %v, want userinfo rejection", err)
	}
	config.Plugins[0].FastForward.Upstreams[0].Addr = "user:secret@dns.example:53"
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("Validate error = %v, want scheme-less userinfo rejection", err)
	}
	config.Plugins[0].FastForward.Upstreams[0].Addr = "https://dns.example/dns-query"
	config.Telemetry.MaxQueryRecords = 999
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "1000") {
		t.Fatalf("Validate error = %v, want record limit rejection", err)
	}
}
