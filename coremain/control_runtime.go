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

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/query_access"
)

func validateControlConfig(cfg *Config) (*url.URL, []netip.Prefix, error) {
	c := cfg.Control
	if c == nil {
		return nil, nil, nil
	}
	if c.Database == "" || c.StatsDatabase == "" {
		return nil, nil, errors.New("control database and stats_database are required")
	}
	db, _ := filepath.Abs(c.Database)
	stats, _ := filepath.Abs(c.StatsDatabase)
	if filepath.Clean(db) == filepath.Clean(stats) {
		return nil, nil, errors.New("control database and stats_database must differ")
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

func authenticate(store *control.Store) func(context.Context, string) (query_context.Principal, error) {
	return func(ctx context.Context, token string) (query_context.Principal, error) {
		v, err := store.AuthenticateCredential(ctx, token)
		if err != nil {
			return query_context.Principal{}, accessError(err)
		}
		return principal(v), nil
	}
}

func admit(store *control.Store) func(context.Context, query_context.Principal) error {
	return func(ctx context.Context, p query_context.Principal) error {
		if err := store.Admit(ctx, identity(p)); err != nil {
			return accessError(err)
		}
		return nil
	}
}
