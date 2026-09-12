package runtimeconfig

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

const Version = 1

type Config struct {
	Version   int       `json:"version" yaml:"version"`
	QueryLog  bool      `json:"query_log" yaml:"query_log"`
	Telemetry Telemetry `json:"telemetry" yaml:"telemetry"`
	Plugins   []Plugin  `json:"plugins" yaml:"plugins"`
}

type Telemetry struct {
	AggregateRetentionDays int `json:"aggregate_retention_days" yaml:"aggregate_retention_days"`
	QueryRetentionHours    int `json:"query_retention_hours" yaml:"query_retention_hours"`
	MaxQueryRecords        int `json:"max_query_records" yaml:"max_query_records"`
}

type Plugin struct {
	Tag            string       `json:"tag" yaml:"tag"`
	Type           string       `json:"type" yaml:"type"`
	Editable       bool         `json:"editable" yaml:"editable"`
	ReadOnlyReason string       `json:"read_only_reason,omitempty" yaml:"read_only_reason,omitempty"`
	FastForward    *FastForward `json:"fast_forward,omitempty" yaml:"fast_forward,omitempty"`
	Cache          *Cache       `json:"cache,omitempty" yaml:"cache,omitempty"`
}

type FastForward struct {
	Upstreams []Upstream `json:"upstreams" yaml:"upstreams"`
}

type Upstream struct {
	Addr           string `json:"addr" yaml:"addr"`
	DialAddr       string `json:"dial_addr,omitempty" yaml:"dial_addr,omitempty"`
	Trusted        bool   `json:"trusted,omitempty" yaml:"trusted,omitempty"`
	SoMark         int    `json:"so_mark,omitempty" yaml:"so_mark,omitempty"`
	BindToDevice   string `json:"bind_to_device,omitempty" yaml:"bind_to_device,omitempty"`
	IdleTimeout    int    `json:"idle_timeout,omitempty" yaml:"idle_timeout,omitempty"`
	MaxConns       int    `json:"max_conns,omitempty" yaml:"max_conns,omitempty"`
	EnablePipeline bool   `json:"enable_pipeline,omitempty" yaml:"enable_pipeline,omitempty"`
	Bootstrap      string `json:"bootstrap,omitempty" yaml:"bootstrap,omitempty"`
	Insecure       bool   `json:"insecure,omitempty" yaml:"insecure,omitempty"`
	KernelTX       bool   `json:"kernel_tx,omitempty" yaml:"kernel_tx,omitempty"`
	KernelRX       bool   `json:"kernel_rx,omitempty" yaml:"kernel_rx,omitempty"`
}

type Cache struct {
	Size              int  `json:"size" yaml:"size"`
	LazyCacheTTL      int  `json:"lazy_cache_ttl" yaml:"lazy_cache_ttl"`
	LazyCacheReplyTTL int  `json:"lazy_cache_reply_ttl" yaml:"lazy_cache_reply_ttl"`
	CompressResp      bool `json:"compress_resp" yaml:"compress_resp"`
}

type PluginSource struct {
	Tag  string
	Type string
	Args any
}

var (
	errSensitiveArgs = errors.New("sensitive parameters")
	errUnknownArgs   = errors.New("unsupported parameters")
)

var fastForwardTopKeys = map[string]struct{}{
	"upstream": {},
	"ca":       {},
}

var fastForwardUpstreamKeys = map[string]struct{}{
	"addr": {}, "dial_addr": {}, "trusted": {}, "socks5": {}, "s5_username": {}, "s5_password": {},
	"so_mark": {}, "bind_to_device": {}, "idle_timeout": {}, "max_conns": {}, "enable_pipeline": {},
	"bootstrap": {}, "insecure": {}, "kernel_tx": {}, "kernel_rx": {},
}

var cacheKeys = map[string]struct{}{
	"size": {}, "redis": {}, "redis_timeout": {}, "lazy_cache_ttl": {}, "lazy_cache_reply_ttl": {},
	"cache_everything": {}, "compress_resp": {}, "when_hit": {},
}

type forwardArgs struct {
	Upstream []forwardUpstream `yaml:"upstream"`
	CA       []string          `yaml:"ca"`
}

type forwardUpstream struct {
	Upstream   `yaml:",inline"`
	Socks5     string `yaml:"socks5"`
	S5Username string `yaml:"s5_username"`
	S5Password string `yaml:"s5_password"`
}

type cacheArgs struct {
	Cache           `yaml:",inline"`
	Redis           string `yaml:"redis"`
	RedisTimeout    int    `yaml:"redis_timeout"`
	CacheEverything bool   `yaml:"cache_everything"`
	WhenHit         string `yaml:"when_hit"`
}

func Inspect(queryLog bool, telemetry Telemetry, sources []PluginSource) (Config, error) {
	config := Config{Version: Version, QueryLog: queryLog, Telemetry: telemetry, Plugins: make([]Plugin, 0, len(sources))}
	if err := ValidateTelemetry(telemetry); err != nil {
		return Config{}, err
	}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if source.Tag == "" || source.Type == "" {
			continue
		}
		if _, duplicate := seen[source.Tag]; duplicate {
			return Config{}, fmt.Errorf("duplicated plugin tag %q", source.Tag)
		}
		seen[source.Tag] = struct{}{}
		plugin := Plugin{Tag: source.Tag, Type: source.Type}
		switch source.Type {
		case "fast_forward":
			forward, err := inspectFastForward(source.Args)
			if err != nil {
				plugin.ReadOnlyReason = readOnlyReason(err)
			} else {
				plugin.Editable = true
				plugin.FastForward = forward
			}
		case "cache":
			cache, err := inspectCache(source.Args)
			if err != nil {
				plugin.ReadOnlyReason = readOnlyReason(err)
			} else {
				plugin.Editable = true
				plugin.Cache = cache
			}
		default:
			plugin.ReadOnlyReason = "unsupported_type"
		}
		config.Plugins = append(config.Plugins, plugin)
	}
	return config, nil
}

func Validate(config Config) error {
	if config.Version != Version {
		return fmt.Errorf("unsupported managed config version %d", config.Version)
	}
	if err := ValidateTelemetry(config.Telemetry); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(config.Plugins))
	for i, plugin := range config.Plugins {
		if plugin.Tag == "" || plugin.Type == "" {
			return fmt.Errorf("plugin #%d requires tag and type", i)
		}
		if _, duplicate := seen[plugin.Tag]; duplicate {
			return fmt.Errorf("duplicated plugin tag %q", plugin.Tag)
		}
		seen[plugin.Tag] = struct{}{}
		if !plugin.Editable {
			if plugin.FastForward != nil || plugin.Cache != nil {
				return fmt.Errorf("read-only plugin %q must not contain parameters", plugin.Tag)
			}
			continue
		}
		switch plugin.Type {
		case "fast_forward":
			if plugin.FastForward == nil || plugin.Cache != nil {
				return fmt.Errorf("plugin %q requires fast_forward parameters", plugin.Tag)
			}
			if len(plugin.FastForward.Upstreams) == 0 {
				return fmt.Errorf("plugin %q requires at least one upstream", plugin.Tag)
			}
			for j, upstream := range plugin.FastForward.Upstreams {
				if strings.TrimSpace(upstream.Addr) == "" {
					return fmt.Errorf("plugin %q upstream #%d requires addr", plugin.Tag, j)
				}
				if hasURLUserinfo(upstream.Addr) || hasURLUserinfo(upstream.DialAddr) || hasURLUserinfo(upstream.Bootstrap) {
					return fmt.Errorf("plugin %q upstream #%d contains URL userinfo", plugin.Tag, j)
				}
				if upstream.IdleTimeout < 0 || upstream.MaxConns < 0 {
					return fmt.Errorf("plugin %q upstream #%d has a negative limit", plugin.Tag, j)
				}
			}
		case "cache":
			if plugin.Cache == nil || plugin.FastForward != nil {
				return fmt.Errorf("plugin %q requires cache parameters", plugin.Tag)
			}
			if plugin.Cache.Size < 0 || plugin.Cache.LazyCacheTTL < 0 || plugin.Cache.LazyCacheReplyTTL < 0 {
				return fmt.Errorf("plugin %q cache values cannot be negative", plugin.Tag)
			}
		default:
			return fmt.Errorf("plugin %q has unsupported editable type %q", plugin.Tag, plugin.Type)
		}
	}
	return nil
}

func ValidateTelemetry(telemetry Telemetry) error {
	if telemetry.AggregateRetentionDays < 1 || telemetry.AggregateRetentionDays > 31 {
		return errors.New("aggregate retention must be between 1 and 31 days")
	}
	if telemetry.QueryRetentionHours < 1 || telemetry.QueryRetentionHours > 720 {
		return errors.New("query retention must be between 1 and 720 hours")
	}
	if telemetry.MaxQueryRecords < 1000 || telemetry.MaxQueryRecords > 5000000 {
		return errors.New("max query records must be between 1000 and 5000000")
	}
	return nil
}

func Apply(sources []PluginSource, desired Config) ([]PluginSource, error) {
	if err := Validate(desired); err != nil {
		return nil, err
	}
	current, err := Inspect(desired.QueryLog, desired.Telemetry, sources)
	if err != nil {
		return nil, err
	}
	sourceByTag := make(map[string]PluginSource, len(sources))
	viewByTag := make(map[string]Plugin, len(current.Plugins))
	for _, source := range sources {
		sourceByTag[source.Tag] = source
	}
	for _, plugin := range current.Plugins {
		viewByTag[plugin.Tag] = plugin
	}
	overrides := make(map[string]Plugin, len(desired.Plugins))
	for _, plugin := range desired.Plugins {
		view, exists := viewByTag[plugin.Tag]
		if !exists || view.Type != plugin.Type {
			return nil, fmt.Errorf("plugin %q does not match the source configuration", plugin.Tag)
		}
		if !view.Editable {
			if plugin.Editable || plugin.FastForward != nil || plugin.Cache != nil {
				return nil, fmt.Errorf("plugin %q is read-only", plugin.Tag)
			}
			continue
		}
		if !plugin.Editable {
			return nil, fmt.Errorf("editable plugin %q cannot be changed to read-only", plugin.Tag)
		}
		overrides[plugin.Tag] = plugin
	}

	result := make([]PluginSource, 0, len(sources))
	for _, source := range sources {
		override, ok := overrides[source.Tag]
		if !ok {
			result = append(result, source)
			continue
		}
		raw, err := argsMap(source.Args)
		if err != nil {
			return nil, fmt.Errorf("plugin %q args: %w", source.Tag, err)
		}
		switch source.Type {
		case "fast_forward":
			raw["upstream"] = override.FastForward.Upstreams
		case "cache":
			raw["size"] = override.Cache.Size
			raw["lazy_cache_ttl"] = override.Cache.LazyCacheTTL
			raw["lazy_cache_reply_ttl"] = override.Cache.LazyCacheReplyTTL
			raw["compress_resp"] = override.Cache.CompressResp
		}
		result = append(result, PluginSource{Tag: source.Tag, Type: source.Type, Args: raw})
	}
	return result, nil
}

// ImmutableArgsEqual reports whether two source plugins differ only in fields
// that the managed runtime is allowed to edit.
func ImmutableArgsEqual(before, after PluginSource) (bool, error) {
	if before.Tag != after.Tag || before.Type != after.Type {
		return false, nil
	}
	beforeView, err := Inspect(true, Telemetry{AggregateRetentionDays: 1, QueryRetentionHours: 1, MaxQueryRecords: 1000}, []PluginSource{before})
	if err != nil {
		return false, err
	}
	afterView, err := Inspect(true, Telemetry{AggregateRetentionDays: 1, QueryRetentionHours: 1, MaxQueryRecords: 1000}, []PluginSource{after})
	if err != nil {
		return false, err
	}
	beforeRaw, err := argsMap(before.Args)
	if err != nil {
		return false, err
	}
	afterRaw, err := argsMap(after.Args)
	if err != nil {
		return false, err
	}
	if beforeView.Plugins[0].Editable && afterView.Plugins[0].Editable {
		switch before.Type {
		case "fast_forward":
			delete(beforeRaw, "upstream")
			delete(afterRaw, "upstream")
		case "cache":
			for _, key := range []string{"size", "lazy_cache_ttl", "lazy_cache_reply_ttl", "compress_resp"} {
				delete(beforeRaw, key)
				delete(afterRaw, key)
			}
		}
	}
	beforeEncoded, err := yaml.Marshal(beforeRaw)
	if err != nil {
		return false, err
	}
	afterEncoded, err := yaml.Marshal(afterRaw)
	if err != nil {
		return false, err
	}
	return bytes.Equal(beforeEncoded, afterEncoded), nil
}

func inspectFastForward(args any) (*FastForward, error) {
	raw, err := argsMap(args)
	if err != nil {
		return nil, errUnknownArgs
	}
	if !onlyKeys(raw, fastForwardTopKeys) {
		return nil, errUnknownArgs
	}
	upstreamValues, ok := raw["upstream"]
	if !ok {
		return nil, errUnknownArgs
	}
	items, ok := toSlice(upstreamValues)
	if !ok {
		return nil, errUnknownArgs
	}
	for _, item := range items {
		upstreamMap, err := argsMap(item)
		if err != nil || !onlyKeys(upstreamMap, fastForwardUpstreamKeys) {
			return nil, errUnknownArgs
		}
	}
	var decoded forwardArgs
	if err := decode(args, &decoded); err != nil {
		return nil, errUnknownArgs
	}
	forward := &FastForward{Upstreams: make([]Upstream, 0, len(decoded.Upstream))}
	for _, upstream := range decoded.Upstream {
		if upstream.Socks5 != "" || upstream.S5Username != "" || upstream.S5Password != "" ||
			hasURLUserinfo(upstream.Addr) || hasURLUserinfo(upstream.DialAddr) || hasURLUserinfo(upstream.Bootstrap) {
			return nil, errSensitiveArgs
		}
		forward.Upstreams = append(forward.Upstreams, upstream.Upstream)
	}
	return forward, nil
}

func inspectCache(args any) (*Cache, error) {
	raw, err := argsMap(args)
	if err != nil || !onlyKeys(raw, cacheKeys) {
		return nil, errUnknownArgs
	}
	var decoded cacheArgs
	if err := decode(args, &decoded); err != nil {
		return nil, errUnknownArgs
	}
	if decoded.Redis != "" || hasURLUserinfo(decoded.Redis) {
		return nil, errSensitiveArgs
	}
	cache := decoded.Cache
	return &cache, nil
}

func argsMap(args any) (map[string]any, error) {
	if args == nil {
		return make(map[string]any), nil
	}
	encoded, err := yaml.Marshal(args)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := yaml.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	if result == nil {
		result = make(map[string]any)
	}
	return result, nil
}

func decode(value any, target any) error {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(encoded, target)
}

func onlyKeys(values map[string]any, allowed map[string]struct{}) bool {
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func toSlice(value any) ([]any, bool) {
	switch values := value.(type) {
	case []any:
		return values, true
	default:
		encoded, err := yaml.Marshal(value)
		if err != nil {
			return nil, false
		}
		var result []any
		if err := yaml.Unmarshal(encoded, &result); err != nil {
			return nil, false
		}
		return result, true
	}
}

func hasURLUserinfo(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, "@") {
		return true
	}
	if !strings.Contains(value, "://") {
		value = "udp://" + value
	}
	parsed, err := url.Parse(value)
	return err != nil || parsed.User != nil
}

func readOnlyReason(err error) string {
	if errors.Is(err, errSensitiveArgs) {
		return "sensitive_parameters"
	}
	return "unsupported_parameters"
}
