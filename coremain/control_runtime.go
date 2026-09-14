package coremain

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/telemetry"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/query_access"
)

func validateControlConfig(cfg *Config) (*url.URL, []netip.Prefix, error) {
	c := cfg.Control
	if c == nil {
		return nil, nil, nil
	}
	controlDriver := effectiveControlDriver(c)
	telemetryDriver := effectiveTelemetryDriver(c)
	if controlDriver != "bbolt" && controlDriver != "mysql" {
		return nil, nil, fmt.Errorf("unsupported control storage driver %q", controlDriver)
	}
	if telemetryDriver != "bbolt" && telemetryDriver != "mysql" {
		return nil, nil, fmt.Errorf("unsupported telemetry storage driver %q", telemetryDriver)
	}
	if controlDriver == "bbolt" && c.Database == "" {
		return nil, nil, errors.New("control database is required for bbolt storage")
	}
	if telemetryDriver == "bbolt" && c.StatsDatabase == "" {
		return nil, nil, errors.New("control stats_database is required for bbolt telemetry")
	}
	if controlDriver == "bbolt" && telemetryDriver == "bbolt" {
		db, _ := filepath.Abs(c.Database)
		stats, _ := filepath.Abs(c.StatsDatabase)
		if filepath.Clean(db) == filepath.Clean(stats) {
			return nil, nil, errors.New("control database and stats_database must differ")
		}
	}
	if controlDriver == "mysql" {
		if err := validateMySQLConfig(c.Storage.MySQL, "control storage"); err != nil {
			return nil, nil, err
		}
	}
	if telemetryDriver == "mysql" {
		mysqlCfg := effectiveTelemetryMySQL(c)
		if err := validateMySQLConfig(mysqlCfg, "telemetry storage"); err != nil {
			return nil, nil, err
		}
	}
	if c.Telemetry.QueueSize < 0 || c.Telemetry.BatchSize < 0 || c.Telemetry.FlushIntervalMS < 0 {
		return nil, nil, errors.New("telemetry queue, batch and flush settings cannot be negative")
	}
	if err := telemetry.ValidateSettings(telemetrySettings(c)); err != nil {
		return nil, nil, fmt.Errorf("invalid telemetry retention: %w", err)
	}
	u, err := url.Parse(c.PublicDNSURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, nil, errors.New("invalid control public_dns_url")
	}
	if u.Path == "" {
		u.Path = "/dns-query"
		c.PublicDNSURL = u.String()
	}
	if c.Development && !loopbackHost(u.Hostname()) {
		return nil, nil, errors.New("development public_dns_url must use loopback")
	}
	panel, panelErr := url.Parse(c.PanelOrigin)
	if panelErr != nil || panel.Host == "" {
		return nil, nil, errors.New("invalid control panel_origin")
	}
	if c.Development && !loopbackHost(panel.Hostname()) {
		return nil, nil, errors.New("development panel_origin must use loopback")
	}
	if cfg.API.HTTP == "" || !loopbackEndpoint(cfg.API.HTTP) {
		return nil, nil, errors.New("control management api must listen on loopback")
	}
	var trusted []netip.Prefix
	for _, raw := range c.TrustedProxies {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid trusted proxy %q", raw)
		}
		trusted = append(trusted, p.Masked())
	}
	for _, serverCfg := range cfg.Servers {
		for _, l := range serverCfg.Listeners {
			if c.Development && !loopbackEndpoint(l.Addr) {
				return nil, nil, errors.New("development listeners must use loopback")
			}
			if l.ProxyProtocol {
				return nil, nil, errors.New("proxy_protocol is not supported in control mode")
			}
			proto := strings.ToLower(l.Protocol)
			httpProto := proto == "http" || proto == "https" || proto == "doh" || proto == "h3" || proto == "doh3"
			if !httpProto && !loopbackEndpoint(l.Addr) {
				return nil, nil, fmt.Errorf("control mode raw DNS listener %q must use loopback", l.Addr)
			}
			if proto == "http" && !loopbackEndpoint(l.Addr) {
				return nil, nil, errors.New("control mode plaintext HTTP listener must use loopback")
			}
			if httpProto {
				path := l.URLPath
				if path == "" {
					path = "/dns-query"
					l.URLPath = path
				}
				if path != u.Path {
					return nil, nil, fmt.Errorf("listener url_path %q differs from public_dns_url path %q", path, u.Path)
				}
			}
		}
	}
	return u, trusted, nil
}

func effectiveControlDriver(c *ControlConfig) string {
	driver := strings.ToLower(strings.TrimSpace(c.Storage.Driver))
	if driver == "" {
		return "bbolt"
	}
	return driver
}

func effectiveTelemetryDriver(c *ControlConfig) string {
	driver := strings.ToLower(strings.TrimSpace(c.Telemetry.Driver))
	if driver == "" {
		if effectiveControlDriver(c) == "mysql" {
			return "mysql"
		}
		return "bbolt"
	}
	return driver
}

func effectiveTelemetryMySQL(c *ControlConfig) MySQLConfig {
	result := c.Telemetry.MySQL
	if strings.TrimSpace(result.DSN) == "" {
		result.DSN = c.Storage.MySQL.DSN
	}
	return result
}

func validateMySQLConfig(c MySQLConfig, name string) error {
	if strings.TrimSpace(c.DSN) == "" {
		return fmt.Errorf("%s mysql dsn is required", name)
	}
	if c.MaxOpenConns < 0 || c.MaxIdleConns < 0 || c.ConnMaxLifetimeSec < 0 || c.OperationTimeoutMS < 0 {
		return fmt.Errorf("%s mysql pool and timeout settings cannot be negative", name)
	}
	if c.MaxOpenConns > 0 && c.MaxIdleConns > c.MaxOpenConns {
		return fmt.Errorf("%s mysql max_idle_conns exceeds max_open_conns", name)
	}
	return nil
}

func mysqlDurations(c MySQLConfig) (time.Duration, time.Duration) {
	return time.Duration(c.ConnMaxLifetimeSec) * time.Second, time.Duration(c.OperationTimeoutMS) * time.Millisecond
}

func openControlStore(ctx context.Context, c *ControlConfig) (control.Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if effectiveControlDriver(c) == "bbolt" {
		return control.Open(c.Database, control.Options{})
	}
	lifetime, timeout := mysqlDurations(c.Storage.MySQL)
	return control.OpenMySQLContext(ctx, control.MySQLOptions{DSN: c.Storage.MySQL.DSN, MaxOpenConns: c.Storage.MySQL.MaxOpenConns, MaxIdleConns: c.Storage.MySQL.MaxIdleConns, ConnMaxLifetime: lifetime, OperationTimeout: timeout})
}

func openTelemetryStore(ctx context.Context, c *ControlConfig) (telemetry.Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	settings := telemetrySettings(c)
	if effectiveTelemetryDriver(c) == "bbolt" {
		return telemetry.Open(telemetry.Options{Path: c.StatsDatabase, QueueSize: c.Telemetry.QueueSize, BatchSize: c.Telemetry.BatchSize, FlushInterval: time.Duration(c.Telemetry.FlushIntervalMS) * time.Millisecond, QueryLogEnabled: c.QueryLog, AggregateRetention: settings.AggregateRetention, QueryRetention: settings.QueryRetention, MaxQueryRecords: settings.MaxQueryRecords})
	}
	mysqlCfg := effectiveTelemetryMySQL(c)
	lifetime, timeout := mysqlDurations(mysqlCfg)
	return telemetry.OpenMySQLContext(ctx, telemetry.MySQLOptions{DSN: mysqlCfg.DSN, MaxOpenConns: mysqlCfg.MaxOpenConns, MaxIdleConns: mysqlCfg.MaxIdleConns, ConnMaxLifetime: lifetime, OperationTimeout: timeout, QueueSize: c.Telemetry.QueueSize, BatchSize: c.Telemetry.BatchSize, FlushInterval: time.Duration(c.Telemetry.FlushIntervalMS) * time.Millisecond, QueryLogEnabled: c.QueryLog, AggregateRetention: settings.AggregateRetention, QueryRetention: settings.QueryRetention, MaxQueryRecords: settings.MaxQueryRecords})
}

func telemetrySettings(c *ControlConfig) telemetry.Settings {
	return telemetry.Settings{
		QueryLogEnabled:    c.QueryLog,
		AggregateRetention: time.Duration(c.Telemetry.AggregateRetentionDays) * 24 * time.Hour,
		QueryRetention:     time.Duration(c.Telemetry.QueryRetentionHours) * time.Hour,
		MaxQueryRecords:    c.Telemetry.MaxQueryRecords,
	}
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func loopbackEndpoint(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func accessError(err error) error {
	switch {
	case errors.Is(err, control.ErrInvalidCredential):
		return query_access.New(query_access.InvalidCredential)
	case errors.Is(err, control.ErrForbidden):
		return query_access.New(query_access.Forbidden)
	case errors.Is(err, control.ErrRateLimited):
		return query_access.New(query_access.RateLimited)
	case errors.Is(err, control.ErrQuotaExceeded):
		return query_access.New(query_access.QuotaExceeded)
	default:
		return query_access.New(query_access.Unavailable)
	}
}

func principal(identity control.Identity) query_context.Principal {
	return query_context.Principal{UserID: identity.UserID, CredentialID: identity.CredentialID, CredentialVersion: identity.CredentialVersion}
}

func identity(p query_context.Principal) control.Identity {
	return control.Identity{UserID: p.UserID, CredentialID: p.CredentialID, CredentialVersion: p.CredentialVersion}
}

func authenticate(store control.Service) func(context.Context, string) (query_context.Principal, error) {
	return func(ctx context.Context, token string) (query_context.Principal, error) {
		v, err := store.AuthenticateCredential(ctx, token)
		if err != nil {
			return query_context.Principal{}, accessError(err)
		}
		return principal(v), nil
	}
}

func admit(store control.Service) func(context.Context, query_context.Principal) error {
	return func(ctx context.Context, p query_context.Principal) error {
		if err := store.Admit(ctx, identity(p)); err != nil {
			return accessError(err)
		}
		return nil
	}
}
