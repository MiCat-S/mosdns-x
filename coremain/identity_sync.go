package coremain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/controlapi"
	"go.uber.org/zap"
	"net"
)

func identitySyncMode(c *ControlConfig) string {
	if c.IdentitySync == nil {
		return "standalone"
	}
	return c.IdentitySync.Mode
}

func identitySyncInterval(c *IdentitySyncConfig) time.Duration {
	if c.IntervalSeconds == 0 {
		return 30 * time.Second
	}
	return time.Duration(c.IntervalSeconds) * time.Second
}

func validateIdentitySyncConfig(c *ControlConfig) error {
	v := c.IdentitySync
	if v == nil {
		return nil
	}
	if effectiveControlDriver(c) != "mysql" {
		return errors.New("identity_sync requires MySQL control storage")
	}
	if v.Mode != "master" && v.Mode != "follower" {
		return errors.New("identity_sync mode must be master or follower")
	}
	if len(v.Token) < 32 || len(v.Token) > 256 {
		return errors.New("identity_sync token must contain 32 to 256 printable non-whitespace ASCII characters")
	}
	for _, r := range v.Token {
		if r < 33 || r > 126 {
			return errors.New("identity_sync token must contain 32 to 256 printable non-whitespace ASCII characters")
		}
	}
	if v.Mode == "master" {
		if v.MasterURL != "" || v.IntervalSeconds != 0 {
			return errors.New("identity_sync master_url and interval_seconds are follower-only")
		}
		return nil
	}
	u, err := url.Parse(v.MasterURL)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery {
		return errors.New("identity_sync master_url must be a panel origin without a path, credentials, query or fragment")
	}
	if u.Scheme != "https" && !(c.Development && u.Scheme == "http" && loopbackHost(u.Hostname())) {
		return errors.New("identity_sync master_url requires HTTPS (development loopback HTTP is allowed)")
	}
	if v.IntervalSeconds < 0 || v.IntervalSeconds > 3600 {
		return errors.New("identity_sync interval_seconds must be between 1 and 3600, or 0 for 30 seconds")
	}
	return nil
}

type identityFollower struct {
	logger        *zap.Logger
	cleanupFailed bool
	cfg           *IdentitySyncConfig
	store         *control.MySQLStore
	client        *http.Client
	mu            sync.Mutex
	status        controlapi.IdentitySyncHealth
}

func newIdentityFollower(ctx context.Context, cfg *IdentitySyncConfig, store *control.MySQLStore) (*identityFollower, bool, error) {
	state, err := store.IdentitySyncState(ctx)
	if err != nil {
		return nil, false, err
	}
	source, _ := url.Parse(cfg.MasterURL) // validated configuration
	f := &identityFollower{cfg: cfg, store: store, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, status: controlapi.IdentitySyncHealth{Mode: "follower", Source: source.Host, LastSuccessAt: state.LastSuccessAt, Users: state.Users, Credentials: state.Credentials}}
	return f, state.Bootstrapped, nil
}

func (f *identityFollower) health() controlapi.IdentitySyncHealth {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := f.status
	if status.LastError == "" && f.cleanupFailed {
		status.LastError = "cleanup_failed"
	}
	return status
}

func (f *identityFollower) pull(ctx context.Context) (retErr error) {
	now := time.Now().UTC()
	f.mu.Lock()
	f.status.LastAttemptAt = &now
	f.mu.Unlock()
	fail := func(code string) error {
		f.mu.Lock()
		f.status.LastError = code
		f.mu.Unlock()
		return fmt.Errorf("identity sync: %s", code)
	}
	snapshot, code, cause := fetchIdentitySnapshot(ctx, f.client, f.cfg)
	if code != "" {
		f.logger.Warn("identity sync fetch failed", zap.String("code", code), zap.String("cause", identitySyncCause(cause)), zap.String("source", f.status.Source))
		return fail(code)
	}
	periodMismatch, err := f.store.ApplyIdentitySnapshot(ctx, snapshot)
	if err != nil {
		f.logger.Warn("identity sync import failed", zap.String("cause", identitySyncCause(err)), zap.String("source", f.status.Source))
		return fail("apply_failed")
	}
	success := time.Now().UTC()
	f.mu.Lock()
	f.status.LastSuccessAt = &success
	f.status.Users = len(snapshot.Users)
	f.status.Credentials = len(snapshot.Credentials)
	f.status.LastError = ""
	if periodMismatch {
		f.status.LastError = "period_mismatch"
	}
	f.mu.Unlock()

	return nil
}

func fetchIdentitySnapshot(ctx context.Context, client *http.Client, cfg *IdentitySyncConfig) (control.IdentitySnapshot, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.MasterURL, "/")+"/api/v1/identity/snapshot", nil)
	if err != nil {
		return control.IdentitySnapshot{}, "fetch_failed", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	response, err := client.Do(req)
	if err != nil {
		return control.IdentitySnapshot{}, "fetch_failed", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return control.IdentitySnapshot{}, fmt.Sprintf("http_%d", response.StatusCode), nil
	}
	const maxSnapshot = 32 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSnapshot+1))
	if err != nil {
		return control.IdentitySnapshot{}, "fetch_failed", err
	}
	if len(body) > maxSnapshot {
		return control.IdentitySnapshot{}, "invalid_snapshot", control.ErrInvalidInput
	}
	snapshot, err := control.DecodeIdentitySnapshot(bytes.NewReader(body))
	if err != nil {
		return control.IdentitySnapshot{}, "invalid_snapshot", control.ErrInvalidInput
	}
	return snapshot, "", nil
}

func (f *identityFollower) run(ctx context.Context) {
	ticker := time.NewTicker(identitySyncInterval(f.cfg))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = f.pull(ctx)
		}
	}
}

// prepareIdentitySync runs before the administrator readiness check. A new
// follower cannot reuse its pre-sync local administrator as a bootstrap signal.
func prepareIdentitySync(ctx context.Context, c *ControlConfig, store control.Service, logger *zap.Logger) (*controlapi.IdentitySyncOptions, *identityFollower, error) {
	cfg := c.IdentitySync
	if cfg == nil {
		return nil, nil, nil
	}
	mysql := store.(*control.MySQLStore) // validateIdentitySyncConfig requires MySQL
	options := &controlapi.IdentitySyncOptions{Info: controlapi.IdentitySyncInfo{Mode: cfg.Mode}, Token: cfg.Token}
	if cfg.Mode == "master" {
		options.Export = mysql.ExportIdentitySnapshot
		return options, nil, nil
	}
	follower, ready, err := newIdentityFollower(ctx, cfg, mysql)
	if err != nil {
		return nil, nil, err
	}
	follower.logger = logger
	if err = follower.pull(ctx); err != nil && !ready && follower.health().LastSuccessAt == nil {
		return nil, nil, fmt.Errorf("initial identity sync failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	options.Info.MasterPanelURL = cfg.MasterURL
	options.Health = follower.health
	options.Interval = identitySyncInterval(cfg)
	return options, follower, nil
}

// Diagnostics classify causes without logging DSNs, Authorization headers,
// database values (which can contain credential hashes), or response bodies.
func identitySyncCause(err error) string {
	if err == nil {
		return "http_status"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var sqlErr *mysql.MySQLError
	if errors.As(err, &sqlErr) {
		return fmt.Sprintf("mysql_%d", sqlErr.Number)
	}
	if errors.Is(err, control.ErrConflict) {
		return "conflict"
	}
	if errors.Is(err, control.ErrInvalidInput) {
		return "invalid_input"
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "network_timeout"
		}
		return "network_error"
	}
	return "storage_error"
}
