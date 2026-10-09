package runtimeconfig

import (
	"context"
	"errors"
	"time"
)

var (
	ErrDisabled                = errors.New("managed runtime config is disabled")
	ErrValidationTokenInvalid  = errors.New("runtime validation token is invalid")
	ErrValidationTokenExpired  = errors.New("runtime validation token has expired")
	ErrConfigSourceUnavailable = errors.New("main config source is unavailable")
)

type State struct {
	// Revision and Config are kept for existing clients. Config always describes
	// the running generation represented by Sources.Running.
	Revision     string       `json:"revision"`
	Config       Config       `json:"config"`
	Mode         string       `json:"mode"`
	Setup        Setup        `json:"setup"`
	Capabilities Capabilities `json:"capabilities"`
	Sources      Sources      `json:"sources"`
}

type Setup struct {
	ManagedConfigConfigured bool   `json:"managed_config_configured"`
	ConfigSourceAvailable   bool   `json:"config_source_available"`
	Reason                  string `json:"reason,omitempty"`
}

type Capabilities struct {
	View     bool `json:"view"`
	Edit     bool `json:"edit"`
	Validate bool `json:"validate"`
	Apply    bool `json:"apply"`
	Reload   bool `json:"reload"`
	History  bool `json:"history"`
	Rollback bool `json:"rollback"`
	Probe    bool `json:"probe"`
}

type Sources struct {
	Running   ConfigSnapshot    `json:"running"`
	Base      ConfigSnapshot    `json:"base"`
	Candidate CandidateSnapshot `json:"candidate"`
}

type ConfigSnapshot struct {
	Kind          string                `json:"kind"`
	Status        string                `json:"status"`
	Config        Config                `json:"config"`
	Plugins       []PluginSummary       `json:"plugins"`
	DataProviders []DataProviderSummary `json:"data_providers"`
	Components    []ComponentCapability `json:"components"`
}

type CandidateSnapshot struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type PluginSummary struct {
	Tag        string              `json:"tag"`
	Type       string              `json:"type"`
	SafeArgs   any                 `json:"safe_args,omitempty"`
	Capability ComponentCapability `json:"capability"`
}

type ComponentCapability struct {
	Name               string `json:"name"`
	View               bool   `json:"view"`
	Edit               bool   `json:"edit"`
	HotReload          bool   `json:"hot_reload"`
	RestartRequired    bool   `json:"restart_required"`
	ClearsMemoryCaches bool   `json:"clears_memory_caches"`
	Reason             string `json:"reason,omitempty"`
}

type DataProviderSummary struct {
	Tag          string                   `json:"tag"`
	File         string                   `json:"file"`
	AutoReload   bool                     `json:"auto_reload"`
	Declared     bool                     `json:"declared"`
	Capability   ComponentCapability      `json:"capability"`
	FileState    DataProviderFileState    `json:"file_state"`
	RuntimeState DataProviderRuntimeState `json:"runtime_state"`
}

type DataProviderFileState struct {
	Status     string     `json:"status"`
	SizeBytes  *int64     `json:"size_bytes"`
	ModifiedAt *time.Time `json:"modified_at"`
}

type DataProviderRuntimeState struct {
	Status     string     `json:"status"`
	EntryCount *int64     `json:"entry_count"`
	LoadedAt   *time.Time `json:"loaded_at"`
	Reason     string     `json:"reason,omitempty"`
}

type Validation struct {
	Token           string    `json:"token"`
	ExpiresAt       time.Time `json:"expires_at"`
	Revision        string    `json:"revision"`
	WillClearCaches bool      `json:"will_clear_caches"`
}

type ApplyResult struct {
	State
	CachesCleared bool `json:"caches_cleared"`
}

type ReloadResult struct {
	State
	RestartRequired []string `json:"restart_required"`
	CachesCleared   bool     `json:"caches_cleared"`
}

type Probe struct {
	UpstreamID string  `json:"upstream_id"`
	DurationMS float64 `json:"duration_ms"`
	Rcode      int     `json:"rcode"`
	Success    bool    `json:"success"`
	Error      string  `json:"error,omitempty"`
}

type Inspector interface {
	Get(context.Context) (State, error)
}

type Manager interface {
	Inspector
	Validate(context.Context, string, string, Config) (Validation, error)
	Apply(context.Context, string, string) (ApplyResult, error)
	Reload(context.Context) (ReloadResult, error)
	History(context.Context) ([]Revision, error)
	Rollback(context.Context, string, string) (ApplyResult, error)
	Probe(context.Context, string) ([]Probe, error)
}

// Cache purge errors. ErrRuntimeUnavailable means no runtime generation is
// serving; ErrCachePurgeBusy means a purge is already running on a cache.
var (
	ErrInvalidDomain      = errors.New("invalid domain")
	ErrRuntimeUnavailable = errors.New("no runtime is running")
	ErrCachePurgeBusy     = errors.New("another cache purge is running")
)

// CachePurge reports a purge across the cache plugins of the running
// generation. Domain is the normalized FQDN that was purged.
type CachePurge struct {
	Domain     string `json:"domain"`
	Subdomains bool   `json:"subdomains"`
	Caches     int    `json:"caches"`
	Removed    int    `json:"removed"`
}

// CachePurger removes cached responses for a domain, and with subdomains
// also for every name below it.
type CachePurger interface {
	PurgeDomainCache(ctx context.Context, domain string, subdomains bool) (CachePurge, error)
}
