/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package coremain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/constant"
	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/controlapi"
	"github.com/pmkol/mosdns-x/internal/dnspolicy"
	"github.com/pmkol/mosdns-x/internal/publiclist"
	"github.com/pmkol/mosdns-x/internal/runtimeconfig"
	"github.com/pmkol/mosdns-x/internal/telemetry"
	"github.com/pmkol/mosdns-x/mlog"
	"github.com/pmkol/mosdns-x/pkg/data_provider"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/safe_close"
	"github.com/pmkol/mosdns-x/pkg/server"
	"github.com/pmkol/mosdns-x/web"
)

type Mosdns struct {
	logger         *zap.Logger
	generation     *RuntimeGeneration
	runtimeManager *RuntimeManager
	managedRuntime *ManagedRuntimeService

	// Data
	dataManager *data_provider.DataManager

	// Plugins
	execs    map[string]executable_seq.Executable
	matchers map[string]executable_seq.Matcher

	httpAPIMux      *http.ServeMux
	httpAPIServer   *http.Server
	httpAPIListener net.Listener

	metricsReg        *prometheus.Registry
	plugins           []Plugin
	providers         []*data_provider.DataProvider
	servers           []*server.Server
	ownedClosers      []io.Closer
	control           control.Service
	policy            *dnspolicy.Engine
	publicLists       *publiclist.Service
	telemetry         telemetry.Service
	controlCfg        *ControlConfig
	trustedProxies    []netip.Prefix
	maintenanceCancel context.CancelFunc
	maintenanceCtx    context.Context
	maintenanceWG     sync.WaitGroup
	apiMu             sync.Mutex
	apiAccepting      bool
	apiWG             sync.WaitGroup

	sc *safe_close.SafeClose
}

func RunMosdns(cfg *Config) error {
	return RunMosdnsContext(context.Background(), cfg)
}

func RunMosdnsContext(ctx context.Context, cfg *Config) (retErr error) {
	effectiveCfg, baseCfg, managedStore, managedView, managedRevision, err := prepareManagedRuntimeConfig(cfg)
	if err != nil {
		return err
	}
	cfg = effectiveCfg
	lg, err := mlog.NewLogger(&cfg.Log)
	if err != nil {
		return fmt.Errorf("failed to init logger: %w", err)
	}

	m := &Mosdns{
		logger:         lg,
		httpAPIMux:     http.NewServeMux(),
		metricsReg:     newMetricsReg(),
		runtimeManager: NewRuntimeManager(),
		sc:             safe_close.NewSafeClose(),
	}
	m.managedRuntime = newManagedRuntimeService(m, managedStore, baseCfg, cfg, managedView, managedRevision)
	var runtimeConfigManager runtimeconfig.Manager
	if managedStore != nil {
		runtimeConfigManager = m.managedRuntime
	}
	defer func() { retErr = errors.Join(retErr, m.shutdown()) }()
	_, trustedProxies, err := validateControlConfig(cfg)
	if err != nil {
		return err
	}
	if cfg.Control != nil {
		if addrs := unauthenticatedListenerAddrs(cfg); len(addrs) > 0 {
			m.logger.Warn("raw DNS listeners serve queries without authentication or quota accounting; "+
				"they are confined to loopback but remain reachable from this host",
				zap.Strings("listeners", addrs))
		}
		m.trustedProxies = trustedProxies
		m.controlCfg = cfg.Control
		m.control, err = openControlStore(ctx, cfg.Control)
		if err != nil {
			return fmt.Errorf("failed to open control database: %w", err)
		}
		ready, listErr := hasEnabledAdministrator(ctx, m.control)
		if listErr != nil {
			return fmt.Errorf("failed to inspect control database: %w", listErr)
		}
		if !ready {
			return errors.New("control database has no enabled administrator; run control init-admin first")
		}
		m.publicLists, err = publiclist.New(m.control, publiclist.Options{Directory: publicListDirectory(cfg)})
		if err != nil {
			return fmt.Errorf("failed to initialize public lists: %w", err)
		}
		m.policy = dnspolicy.NewWithPublicLists(m.control, m.publicLists)
		m.telemetry, err = openTelemetryStore(ctx, cfg.Control)
		if err != nil {
			return fmt.Errorf("failed to open telemetry database: %w", err)
		}
		maintCtx, cancel := context.WithCancel(context.Background())
		m.maintenanceCancel = cancel
		m.maintenanceCtx = maintCtx
		maintainer, ok := m.control.(control.Maintainer)
		if !ok {
			return errors.New("control backend does not implement maintenance")
		}
		m.maintenanceWG.Add(1)
		go func() {
			defer m.maintenanceWG.Done()
			if err := maintainer.RunMaintenance(maintCtx, time.Hour); err != nil && !errors.Is(err, context.Canceled) {
				m.logger.Error("control maintenance stopped", zap.Error(err))
				m.sc.SendCloseSignal(fmt.Errorf("control maintenance stopped: %w", err))
			}
		}()
		m.maintenanceWG.Add(1)
		go func() {
			defer m.maintenanceWG.Done()
			if err := m.publicLists.Run(maintCtx); err != nil && !errors.Is(err, context.Canceled) {
				m.logger.Error("public list refresher stopped", zap.Error(err))
				m.sc.SendCloseSignal(fmt.Errorf("public list refresher stopped: %w", err))
			}
		}()
	}

	m.httpAPIMux.Handle("/metrics", promhttp.HandlerFor(prometheus.Gatherers{m.metricsReg, m.runtimeManager}, promhttp.HandlerOpts{}))
	m.httpAPIMux.HandleFunc("/debug/pprof/", pprof.Index)
	m.httpAPIMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	m.httpAPIMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	m.httpAPIMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	m.httpAPIMux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	m.httpAPIMux.Handle("/plugins/", m.runtimeManager.PluginHandler())
	stage, err := m.runtimeManager.Stage(ctx, func(buildCtx context.Context) (*RuntimeGeneration, error) {
		return m.buildRuntimeGeneration(buildCtx, cfg)
	})
	if err != nil {
		return fmt.Errorf("failed to stage initial runtime: %w", err)
	}
	if err := m.runtimeManager.Swap(stage); err != nil {
		return fmt.Errorf("failed to activate initial runtime: %w", err)
	}

	for i := range cfg.Servers {
		if err := m.startServers(i, &cfg.Servers[i]); err != nil {
			return fmt.Errorf("failed to start server #%d, %w", i, err)
		}
	}

	// Start http api server. Bind synchronously so initialization failures roll back.
	if httpAddr := cfg.API.HTTP; len(httpAddr) > 0 {
		var apiHandler http.Handler = m.httpAPIMux
		if cfg.Control != nil {
			startedAt := time.Now()
			lookup, lookupErr := m.newPanelLookup(cfg)
			if lookupErr != nil {
				return fmt.Errorf("failed to init panel lookup: %w", lookupErr)
			}
			apiHandler, err = controlapi.New(controlapi.Options{Control: m.control, Logger: m.logger, Telemetry: m.telemetry, PublicLists: m.publicLists, RuntimeInspector: m.managedRuntime, RuntimeConfig: runtimeConfigManager, PublicDNSURL: cfg.Control.PublicDNSURL, PanelOrigin: cfg.Control.PanelOrigin, SecureCookies: !cfg.Control.Development, Development: cfg.Control.Development, Assets: web.Assets(), Legacy: m.httpAPIMux, EnablePprof: cfg.Control.EnablePprof, TrustedProxyCIDRs: trustedProxies, Lookup: lookup, InvalidatePolicy: m.policy.Invalidate, SystemInfo: func(ctx context.Context) (controlapi.SystemInfo, error) {
				queryLogEnabled := cfg.Control.QueryLog
				if m.managedRuntime != nil {
					if state, stateErr := m.managedRuntime.Get(ctx); stateErr == nil {
						queryLogEnabled = state.Config.QueryLog
					}
				}
				return controlapi.SystemInfo{Version: constant.Version, StartedAt: startedAt, PublicDNSURL: cfg.Control.PublicDNSURL, QueryLogEnabled: queryLogEnabled, Config: controlapi.SystemConfig{DNSProtocols: configuredDNSProtocols(cfg), ManagementEnabled: true, PprofEnabled: cfg.Control.EnablePprof, ControlStorage: effectiveControlDriver(cfg.Control), TelemetryStorage: effectiveTelemetryDriver(cfg.Control)}}, nil
			}})
			if err != nil {
				return fmt.Errorf("failed to init control api: %w", err)
			}
			healthHandler := apiHandler.(*controlapi.Handler)
			if err := m.metricsReg.Register(healthHandler); err != nil {
				return fmt.Errorf("failed to register control health metrics: %w", err)
			}
			m.maintenanceWG.Add(1)
			go func() {
				defer m.maintenanceWG.Done()
				healthHandler.RunHealthMonitor(m.maintenanceCtx)
			}()
		}
		listener, err := net.Listen("tcp", httpAddr)
		if err != nil {
			return fmt.Errorf("failed to listen on api: %w", err)
		}
		m.apiMu.Lock()
		m.apiAccepting = true
		m.apiMu.Unlock()
		apiHandler = m.apiGate(apiHandler)
		httpServer := &http.Server{Handler: apiHandler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
		m.httpAPIServer = httpServer
		m.httpAPIListener = listener
		m.sc.Attach(func(done func(), closeSignal <-chan struct{}) {
			defer done()
			errChan := make(chan error, 1)
			go func() {
				m.logger.Info("starting api http server", zap.String("addr", httpAddr))
				errChan <- httpServer.Serve(listener)
			}()
			serveDone := false
			select {
			case err := <-errChan:
				serveDone = true
				select {
				case <-closeSignal:
				default:
					m.sc.SendCloseSignal(err)
				}
			case <-closeSignal:
			}
			m.stopAPI(httpServer)
			if !serveDone {
				<-errChan
			}
		})
	}

	time.AfterFunc(time.Second*1, func() {
		runtime.GC()
		debug.FreeOSMemory()
	})
	select {
	case <-m.sc.ReceiveCloseSignal():
	case <-ctx.Done():
		m.sc.SendCloseSignal(nil)
	}
	m.sc.Done()
	m.sc.CloseWait()
	return m.sc.Err()
}

func (m *Mosdns) stopAPI(httpServer *http.Server) {
	m.apiMu.Lock()
	m.apiAccepting = false
	m.apiMu.Unlock()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := httpServer.Shutdown(shutdownCtx)
	cancel()
	if err != nil {
		m.logger.Warn("api shutdown exceeded deadline", zap.Error(err))
		_ = httpServer.Close()
	}
	m.apiWG.Wait()
}

func (m *Mosdns) apiGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.apiMu.Lock()
		if !m.apiAccepting {
			m.apiMu.Unlock()
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		m.apiWG.Add(1)
		m.apiMu.Unlock()
		defer m.apiWG.Done()
		next.ServeHTTP(w, r)
	})
}

func configuredDNSProtocols(cfg *Config) []string {
	seen := make(map[string]struct{})
	var protocols []string
	for _, serverCfg := range cfg.Servers {
		for _, listener := range serverCfg.Listeners {
			protocol := strings.ToLower(listener.Protocol)
			if protocol == "" {
				protocol = "udp"
			}
			if _, ok := seen[protocol]; !ok {
				seen[protocol] = struct{}{}
				protocols = append(protocols, protocol)
			}
		}
	}
	return protocols
}

type shutdownPlugin interface{ Shutdown() error }

func (m *Mosdns) shutdown() error {
	if m.sc != nil {
		m.sc.SendCloseSignal(nil)
		m.sc.Done()
		m.sc.CloseWait()
	}
	if m.maintenanceCancel != nil {
		m.maintenanceCancel()
		m.maintenanceWG.Wait()
		m.maintenanceCancel = nil
	}
	var errs []error
	for i := len(m.ownedClosers) - 1; i >= 0; i-- {
		if err := m.ownedClosers[i].Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	m.ownedClosers = nil
	if m.httpAPIServer != nil {
		m.stopAPI(m.httpAPIServer)
		m.httpAPIServer = nil
	}
	if m.httpAPIListener != nil {
		if err := m.httpAPIListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
		m.httpAPIListener = nil
	}
	for i := len(m.servers) - 1; i >= 0; i-- {
		errs = append(errs, m.servers[i].Shutdown(context.Background()))
	}
	m.servers = nil
	if m.runtimeManager != nil {
		errs = append(errs, m.runtimeManager.Drain(context.Background()))
		m.runtimeManager = nil
	}
	errs = append(errs, m.shutdownRuntimeResources())
	if m.telemetry != nil {
		errs = append(errs, m.telemetry.Close())
		m.telemetry = nil
	}
	if m.control != nil {
		errs = append(errs, m.control.Close())
		m.control = nil
	}
	return errors.Join(errs...)
}

func (m *Mosdns) addPlugin(p Plugin) {
	m.plugins = append(m.plugins, p)
	t := p.Tag()
	if p, ok := p.(ExecutablePlugin); ok {
		if m.generation != nil {
			m.execs[t] = m.generation.wrapExecutable(p)
		} else {
			m.execs[t] = p
		}
	}
	if p, ok := p.(MatcherPlugin); ok {
		if m.generation != nil {
			m.matchers[p.Tag()] = m.generation.wrapMatcher(p)
		} else {
			m.matchers[p.Tag()] = p
		}
	}
}

func (m *Mosdns) shutdownRuntimeResources() error {
	if m.sc != nil {
		m.sc.SendCloseSignal(nil)
		m.sc.Done()
		m.sc.CloseWait()
	}
	var errs []error
	for i := len(m.plugins) - 1; i >= 0; i-- {
		if p, ok := m.plugins[i].(shutdownPlugin); ok {
			errs = append(errs, p.Shutdown())
		} else {
			errs = append(errs, m.plugins[i].Close())
		}
	}
	m.plugins = nil
	for i := len(m.providers) - 1; i >= 0; i-- {
		m.providers[i].Close()
	}
	m.providers = nil
	return errors.Join(errs...)
}

func (m *Mosdns) GetDataManager() *data_provider.DataManager {
	return m.dataManager
}

func (m *Mosdns) GetSafeClose() *safe_close.SafeClose {
	return m.sc
}

func (m *Mosdns) GetExecutables() map[string]executable_seq.Executable {
	return m.execs
}

func (m *Mosdns) GetMatchers() map[string]executable_seq.Matcher {
	return m.matchers
}

// GetMetricsReg returns a prometheus.Registerer with a prefix of "mosdns_"
func (m *Mosdns) GetMetricsReg() prometheus.Registerer {
	return prometheus.WrapRegistererWithPrefix("mosdns_", m.metricsReg)
}

// GetHTTPAPIMux returns the api http.ServeMux.
// The pattern "/plugins/plugin_tag/" has been registered if
// Plugin implements http.Handler interface.
// Plugin caller should register path that has "/plugins/plugin_tag/"
// prefix only.
func (m *Mosdns) GetHTTPAPIMux() *http.ServeMux {
	return m.httpAPIMux
}

func (m *Mosdns) ManagedRuntime() *ManagedRuntimeService {
	return m.managedRuntime
}

func newMetricsReg() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewGoCollector())
	return reg
}
