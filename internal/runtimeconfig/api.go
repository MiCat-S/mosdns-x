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
	Revision string `json:"revision"`
	Config   Config `json:"config"`
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

type Manager interface {
	Get(context.Context) (State, error)
	Validate(context.Context, string, string, Config) (Validation, error)
	Apply(context.Context, string, string) (ApplyResult, error)
	Reload(context.Context) (ReloadResult, error)
	History(context.Context) ([]Revision, error)
	Rollback(context.Context, string, string) (ApplyResult, error)
	Probe(context.Context, string) ([]Probe, error)
}
