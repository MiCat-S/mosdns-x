package coremain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/data_provider"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/safe_close"
	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

type runtimeListenerIdentity struct {
	protocol         string
	addr             string
	unixDomainSocket bool
	cert             string
	key              string
	kernelTX         bool
	kernelRX         bool
	urlPath          string
	userIPHeader     string
	proxyProtocol    bool
	idleTimeout      uint
}

func listenerIdentity(cfg *ServerListenerConfig) runtimeListenerIdentity {
	return runtimeListenerIdentity{
		protocol:         strings.ToLower(cfg.Protocol),
		addr:             cfg.Addr,
		unixDomainSocket: cfg.UnixDomainSocket,
		cert:             cfg.Cert,
		key:              cfg.Key,
		kernelTX:         cfg.KernelTX,
		kernelRX:         cfg.KernelRX,
		urlPath:          cfg.URLPath,
		userIPHeader:     cfg.GetUserIPFromHeader,
		proxyProtocol:    cfg.ProxyProtocol,
		idleTimeout:      cfg.IdleTimeout,
	}
}

func (m *Mosdns) buildRuntimeGeneration(ctx context.Context, cfg *Config) (generation *RuntimeGeneration, retErr error) {
	owner := &Mosdns{
		logger:      m.logger,
		dataManager: data_provider.NewDataManager(),
		execs:       make(map[string]executable_seq.Executable),
		matchers:    make(map[string]executable_seq.Matcher),
		httpAPIMux:  http.NewServeMux(),
		metricsReg:  prometheus.NewRegistry(),
		sc:          safe_close.NewSafeClose(),
	}
	generation = newRuntimeGeneration(nil, nil, owner.httpAPIMux, owner.metricsReg, owner.shutdownRuntimeResources)
	owner.generation = generation
	defer func() {
		if recovered := recover(); recovered != nil {
			retErr = fmt.Errorf("runtime initialization panicked: %v", recovered)
		}
	}()

	if err := ctx.Err(); err != nil {
		return generation, err
	}
	providerTags := make(map[string]struct{})
	for _, dpc := range cfg.DataProviders {
		if len(dpc.Tag) == 0 {
			continue
		}
		if _, ok := providerTags[dpc.Tag]; ok {
			return generation, fmt.Errorf("duplicated provider tag %s", dpc.Tag)
		}
		providerTags[dpc.Tag] = struct{}{}

		dp, err := data_provider.NewDataProvider(m.logger, dpc)
		if err != nil {
			return generation, fmt.Errorf("failed to init data provider %s, %w", dpc.Tag, err)
		}
		owner.dataManager.AddDataProvider(dpc.Tag, dp)
		owner.providers = append(owner.providers, dp)
	}

	presetFuncs := LoadNewPersetPluginFuncs()
	pluginTags := make(map[string]struct{}, len(presetFuncs)+len(cfg.Plugins))
	for tag := range presetFuncs {
		pluginTags[tag] = struct{}{}
	}
	for _, pc := range cfg.Plugins {
		if len(pc.Type) == 0 || len(pc.Tag) == 0 {
			continue
		}
		if _, duplicate := pluginTags[pc.Tag]; duplicate {
			return generation, fmt.Errorf("duplicated plugin tag %s", pc.Tag)
		}
		pluginTags[pc.Tag] = struct{}{}
	}

	for tag, factory := range presetFuncs {
		if err := ctx.Err(); err != nil {
			return generation, err
		}
		plugin, err := factory(NewBP(tag, "preset", owner.logger, owner))
		if err != nil {
			return generation, fmt.Errorf("failed to init preset plugin %s, %w", tag, err)
		}
		owner.addPlugin(plugin)
	}

	for i, pc := range cfg.Plugins {
		if len(pc.Type) == 0 || len(pc.Tag) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return generation, err
		}
		owner.logger.Info("loading plugin", zap.String("tag", pc.Tag), zap.String("type", pc.Type))
		plugin, err := NewPlugin(&pc, owner.logger, owner)
		if err != nil {
			return generation, fmt.Errorf("failed to init plugin #%d, %w", i, err)
		}
		owner.addPlugin(plugin)
		if handler, ok := plugin.(http.Handler); ok {
			owner.httpAPIMux.Handle(fmt.Sprintf("/plugins/%s/", plugin.Tag()), handler)
		}
	}

	if len(cfg.Servers) == 0 {
		return generation, errors.New("no server is configured")
	}
	generation.entries = make([][]D.Handler, len(cfg.Servers))
	generation.topology = make([][]runtimeListenerIdentity, len(cfg.Servers))
	for serverIndex := range cfg.Servers {
		serverCfg := &cfg.Servers[serverIndex]
		if len(serverCfg.Listeners) == 0 {
			return generation, fmt.Errorf("server #%d has no configured listener", serverIndex)
		}
		if len(serverCfg.Exec) == 0 {
			return generation, fmt.Errorf("server #%d has an empty entry", serverIndex)
		}
		entry := owner.execs[serverCfg.Exec]
		if entry == nil {
			return generation, fmt.Errorf("cannot find entry %s", serverCfg.Exec)
		}
		queryTimeout := configuredQueryTimeout(serverCfg)
		generation.entries[serverIndex] = make([]D.Handler, len(serverCfg.Listeners))
		generation.topology[serverIndex] = make([]runtimeListenerIdentity, len(serverCfg.Listeners))
		for listenerIndex, listenerCfg := range serverCfg.Listeners {
			generation.topology[serverIndex][listenerIndex] = listenerIdentity(listenerCfg)
			opts := D.EntryHandlerOpts{Logger: owner.logger, Entry: entry, QueryTimeout: queryTimeout, RecursionAvailable: true}
			if m.control != nil && isHTTPDNSProtocol(listenerCfg.Protocol) {
				captureQueryDetails := m.controlCfg.QueryLog
				if cfg.Control != nil {
					captureQueryDetails = cfg.Control.QueryLog
				}
				opts.Admit = admit(m.control)
				opts.Observe = m.telemetry.Observe
				opts.CaptureQueryDetails = captureQueryDetails
				opts.BeforeExecWithTrace = policyBeforeWithTrace(m.policy)
				opts.AfterExecWithTrace = policyAfterWithTrace(m.policy)
			}
			handler, err := D.NewEntryHandler(opts)
			if err != nil {
				return generation, fmt.Errorf("failed to init entry handler for server #%d listener #%d, %w", serverIndex, listenerIndex, err)
			}
			generation.entries[serverIndex][listenerIndex] = handler
		}
	}

	firstServer := &cfg.Servers[0]
	lookupEntry := owner.execs[firstServer.Exec]
	lookupOpts := D.EntryHandlerOpts{
		Logger: owner.logger, Entry: lookupEntry, QueryTimeout: configuredQueryTimeout(firstServer), RecursionAvailable: true,
	}
	if m.policy != nil {
		lookupOpts.BeforeExecWithTrace = policyBeforeWithTrace(m.policy)
		lookupOpts.AfterExecWithTrace = policyAfterWithTrace(m.policy)
	}
	lookup, err := D.NewEntryHandler(lookupOpts)
	if err != nil {
		return generation, fmt.Errorf("failed to init panel lookup entry handler: %w", err)
	}
	generation.lookup = lookup
	return generation, nil
}
