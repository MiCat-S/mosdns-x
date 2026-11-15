package controlapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
)

type IdentitySyncInfo struct {
	Mode           string `json:"mode"`
	MasterPanelURL string `json:"master_panel_url,omitempty"`
}

type IdentitySyncHealth struct {
	Mode          string     `json:"mode"`
	Source        string     `json:"source"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     string     `json:"last_error,omitempty"`
	Users         int        `json:"users"`
	Credentials   int        `json:"credentials"`
}

type IdentitySyncOptions struct {
	Nodes    func() []control.DNSNode
	Info     IdentitySyncInfo
	Token    string
	Export   func(context.Context) (control.IdentitySnapshot, error)
	Health   func() IdentitySyncHealth
	Interval time.Duration
}

func (h *Handler) identitySnapshot(w http.ResponseWriter, r *http.Request) {
	cfg := h.opts.IdentitySync
	if cfg == nil || cfg.Info.Mode != "master" || cfg.Export == nil {
		writeError(w, 404, "not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, 405, "invalid_input")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(token) != len(cfg.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(cfg.Token)) != 1 {
		writeError(w, 401, "invalid_credential")
		return
	}
	snapshot, err := cfg.Export(r.Context())
	if err != nil {
		h.serviceError(w, err)
		return
	}
	writeJSON(w, 200, snapshot)
}

func (h *Handler) identitySyncInfo() *IdentitySyncInfo {
	if h.opts.IdentitySync == nil {
		return nil
	}
	return &h.opts.IdentitySync.Info
}

func (h *Handler) identitySyncMetric(now time.Time) *HealthMetric {
	cfg := h.opts.IdentitySync
	if cfg == nil || cfg.Health == nil {
		return nil
	}
	status := cfg.Health()
	var value *float64
	if status.LastSuccessAt != nil {
		value = number(max(0, now.Sub(*status.LastSuccessAt).Seconds()))
	}
	warning, critical := max(90, cfg.Interval.Seconds()*3), max(300, cfg.Interval.Seconds()*10)
	m := healthMetric("identity_sync", "账户同步延迟", "秒", value, warning, critical, "")
	if value == nil {
		m.Status, m.Reason = "critical", "sync_never"
	} else if status.LastError != "" {
		m.Reason = "sync_failed"
		if status.LastError == "period_mismatch" {
			m.Reason = "period_mismatch"
		}
		if m.Status == "healthy" {
			m.Status = "warning"
		}
	}
	return &m
}
