package runtimeconfig

import (
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

const redactedValue = "[redacted]"

// Snapshot builds a display-only view of a configuration source. SafeArgs is
// deliberately separate from Config so redaction markers can never be applied
// back to a running configuration.
func Snapshot(kind string, config Config, sources []PluginSource, providers []DataProviderSummary, managed, clearsMemoryCaches bool) (ConfigSnapshot, error) {
	viewByTag := make(map[string]Plugin, len(config.Plugins))
	for _, plugin := range config.Plugins {
		viewByTag[plugin.Tag] = plugin
	}
	plugins := make([]PluginSummary, 0, len(sources))
	for _, source := range sources {
		if source.Tag == "" || source.Type == "" {
			continue
		}
		safeArgs, err := sanitizeArgs(source.Args)
		if err != nil {
			return ConfigSnapshot{}, err
		}
		capability := ComponentCapability{Name: source.Tag, View: true, RestartRequired: true, Reason: "configured_in_main_config"}
		if view, ok := viewByTag[source.Tag]; ok && view.Editable {
			if managed {
				capability.Edit = true
				capability.HotReload = true
				capability.RestartRequired = false
				capability.ClearsMemoryCaches = clearsMemoryCaches
				capability.Reason = ""
			} else {
				capability.Reason = "managed_config_not_configured"
			}
		} else if ok && view.ReadOnlyReason != "" {
			capability.Reason = view.ReadOnlyReason
		}
		plugins = append(plugins, PluginSummary{Tag: source.Tag, Type: source.Type, SafeArgs: safeArgs, Capability: capability})
	}
	components := make([]ComponentCapability, 0, 2)
	for _, name := range []string{"query_log", "telemetry"} {
		capability := ComponentCapability{Name: name, View: true, RestartRequired: true, Reason: "managed_config_not_configured"}
		if managed {
			capability.Edit = true
			capability.HotReload = true
			capability.RestartRequired = false
			capability.ClearsMemoryCaches = clearsMemoryCaches
			capability.Reason = ""
		}
		components = append(components, capability)
	}
	if providers == nil {
		providers = []DataProviderSummary{}
	}
	return ConfigSnapshot{
		Kind: kind, Status: "available", Config: config, Plugins: plugins,
		DataProviders: providers, Components: components,
	}, nil
}

func sanitizeArgs(args any) (any, error) {
	if args == nil {
		return nil, nil
	}
	encoded, err := yaml.Marshal(args)
	if err != nil {
		return nil, err
	}
	var value any
	if err := yaml.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	return sanitizeValue("", value), nil
}

func sanitizeValue(parentKey string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveKey(key) {
				result[key] = redactedValue
				continue
			}
			result[key] = sanitizeValue(key, child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			result[i] = sanitizeValue(parentKey, child)
		}
		return result
	case string:
		return sanitizeEndpoint(parentKey, typed)
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if normalized == "key" || normalized == "username" || normalized == "user" {
		return true
	}
	for _, fragment := range []string{"password", "passwd", "secret", "token", "credential", "private_key", "api_key", "access_key", "auth", "bearer", "cookie", "session", "signature", "s5_username"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func sanitizeEndpoint(key, value string) string {
	upperValue := strings.ToUpper(value)
	if strings.Contains(upperValue, "BEGIN PRIVATE KEY") || strings.Contains(upperValue, "BEGIN ENCRYPTED PRIVATE KEY") {
		return redactedValue
	}
	lowerValue := strings.ToLower(value)
	if strings.Contains(lowerValue, "@tcp(") || strings.Contains(lowerValue, "@unix(") {
		return redactedValue
	}
	normalized := strings.ToLower(key)
	endpointKey := strings.Contains(normalized, "addr") || strings.Contains(normalized, "url") || normalized == "redis" || normalized == "socks5" || normalized == "bootstrap" || normalized == "dsn"
	looksLikeURL := strings.Contains(value, "://")
	if !endpointKey && !looksLikeURL {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return redactedValue
	}
	if parsed.Scheme == "" {
		if strings.Contains(value, "@") {
			return redactedValue
		}
		return value
	}
	parsed.User = nil
	query := parsed.Query()
	for queryKey := range query {
		query.Set(queryKey, redactedValue)
	}
	parsed.RawQuery = query.Encode()
	if parsed.Fragment != "" {
		parsed.Fragment = ""
	}
	// A DoH path can itself be the credential: this project issues device URLs
	// as /dns-query/<uuid>, so an upstream pointed at one carries a bearer token
	// in its path. Keep the first path segment, which identifies the endpoint,
	// and redact the rest.
	parsed.Path, parsed.RawPath = redactEndpointPath(parsed.Path)
	if parsed.Host == "" && strings.Contains(value, "@") {
		return redactedValue
	}
	return parsed.String()
}

// redactEndpointPath keeps the leading path segment and replaces every deeper
// segment with the redaction marker. It returns the new Path and RawPath; the
// RawPath is set only when a segment was redacted, so an untouched path keeps
// whatever escaping it already had, and a redacted one renders the marker
// literally instead of percent-encoding its brackets.
func redactEndpointPath(path string) (string, string) {
	if path == "" || path == "/" {
		return path, ""
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) < 2 {
		return path, ""
	}
	redacted := false
	for i := 1; i < len(segments); i++ {
		if segments[i] == "" {
			continue
		}
		segments[i] = redactedValue
		redacted = true
	}
	if !redacted {
		return path, ""
	}
	result := "/" + strings.Join(segments, "/")
	return result, result
}
