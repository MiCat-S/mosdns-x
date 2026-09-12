package coremain

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/pmkol/mosdns-x/internal/runtimeconfig"
	"github.com/pmkol/mosdns-x/internal/telemetry"
	"github.com/pmkol/mosdns-x/pkg/upstream"
	"github.com/pmkol/mosdns-x/pkg/utils"
)

var (
	ErrManagedRuntimeDisabled  = runtimeconfig.ErrDisabled
	ErrValidationTokenInvalid  = runtimeconfig.ErrValidationTokenInvalid
	ErrValidationTokenExpired  = runtimeconfig.ErrValidationTokenExpired
	ErrConfigSourceUnavailable = runtimeconfig.ErrConfigSourceUnavailable
)

const (
	managedHistoryLimit = 10
	validationTokenTTL  = 5 * time.Minute
)

type ManagedRuntimeState = runtimeconfig.State
type ManagedRuntimeValidation = runtimeconfig.Validation
type ManagedRuntimeApplyResult = runtimeconfig.ApplyResult
type ManagedRuntimeReloadResult = runtimeconfig.ReloadResult
type RuntimeProbe = runtimeconfig.Probe

type runtimeValidation struct {
	sessionID  string
	revision   string
	expiresAt  time.Time
	config     runtimeconfig.Config
	clearCache bool
}

type telemetrySettingsUpdater interface {
	UpdateSettings(telemetry.Settings) error
}

// ManagedRuntimeService coordinates managed-file persistence with runtime
// generation staging. Its methods are intentionally transport-neutral so the
// control API can bind authentication and HTTP shapes separately.
type ManagedRuntimeService struct {
	mu sync.Mutex

	host        *Mosdns
	store       *runtimeconfig.Store
	base        *Config
	effective   *Config
	view        runtimeconfig.Config
	revision    string
	validations map[string]runtimeValidation
	now         func() time.Time
	swapRuntime func(*RuntimeStage) error
}

func prepareManagedRuntimeConfig(cfg *Config) (*Config, *Config, *runtimeconfig.Store, runtimeconfig.Config, string, error) {
	if cfg.Control == nil || strings.TrimSpace(cfg.Control.ManagedConfig) == "" {
		return cfg, nil, nil, runtimeconfig.Config{}, "", nil
	}
	base, err := cloneConfig(cfg)
	if err != nil {
		return nil, nil, nil, runtimeconfig.Config{}, "", err
	}
	store, err := runtimeconfig.NewStore(cfg.Control.ManagedConfig, managedHistoryLimit)
	if err != nil {
		return nil, nil, nil, runtimeconfig.Config{}, "", err
	}
	desired, revision, err := store.Read()
	if errors.Is(err, os.ErrNotExist) {
		desired, err = inspectManagedConfig(base)
		revision = ""
	}
	if err != nil {
		return nil, nil, nil, runtimeconfig.Config{}, "", fmt.Errorf("load managed config: %w", err)
	}
	effective, err := applyManagedConfig(base, desired)
	if err != nil {
		return nil, nil, nil, runtimeconfig.Config{}, "", fmt.Errorf("apply managed config: %w", err)
	}
	view, err := inspectManagedConfig(effective)
	if err != nil {
		return nil, nil, nil, runtimeconfig.Config{}, "", err
	}
	return effective, base, store, view, revision, nil
}

func newManagedRuntimeService(host *Mosdns, store *runtimeconfig.Store, base, effective *Config, view runtimeconfig.Config, revision string) *ManagedRuntimeService {
	if store == nil {
		return nil
	}
	return &ManagedRuntimeService{
		host: host, store: store, base: base, effective: effective, view: view, revision: revision,
		validations: make(map[string]runtimeValidation), now: time.Now,
		swapRuntime: host.runtimeManager.Swap,
	}
}

func (s *ManagedRuntimeService) Get(context.Context) (ManagedRuntimeState, error) {
	if s == nil {
		return ManagedRuntimeState{}, ErrManagedRuntimeDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	view, err := cloneManagedConfig(s.view)
	if err != nil {
		return ManagedRuntimeState{}, err
	}
	return ManagedRuntimeState{Revision: s.revision, Config: view}, nil
}

func (s *ManagedRuntimeService) Validate(ctx context.Context, sessionID, expectedRevision string, desired runtimeconfig.Config) (ManagedRuntimeValidation, error) {
	if s == nil {
		return ManagedRuntimeValidation{}, ErrManagedRuntimeDisabled
	}
	if strings.TrimSpace(sessionID) == "" {
		return ManagedRuntimeValidation{}, errors.New("administrator session is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != s.revision {
		return ManagedRuntimeValidation{}, runtimeconfig.ErrRevisionConflict
	}
	effective, _, err := s.prepareCandidateLocked(desired)
	if err != nil {
		return ManagedRuntimeValidation{}, err
	}
	stage, err := s.host.runtimeManager.Stage(ctx, func(buildCtx context.Context) (*RuntimeGeneration, error) {
		return s.host.buildRuntimeGeneration(buildCtx, effective)
	})
	if err != nil {
		return ManagedRuntimeValidation{}, err
	}
	if err := s.host.runtimeManager.Discard(context.Background(), stage); err != nil {
		return ManagedRuntimeValidation{}, err
	}
	token, err := newValidationToken()
	if err != nil {
		return ManagedRuntimeValidation{}, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(validationTokenTTL)
	s.pruneValidationsLocked(now)
	copyOfDesired, err := cloneManagedConfig(desired)
	if err != nil {
		return ManagedRuntimeValidation{}, err
	}
	clearCache := managedCachesWillClear(s.view, desired)
	s.validations[token] = runtimeValidation{sessionID: sessionID, revision: expectedRevision, expiresAt: expiresAt, config: copyOfDesired, clearCache: clearCache}
	return ManagedRuntimeValidation{Token: token, ExpiresAt: expiresAt, Revision: expectedRevision, WillClearCaches: clearCache}, nil
}

func (s *ManagedRuntimeService) Apply(ctx context.Context, sessionID, token string) (ManagedRuntimeApplyResult, error) {
	if s == nil {
		return ManagedRuntimeApplyResult{}, ErrManagedRuntimeDisabled
	}
	if strings.TrimSpace(sessionID) == "" {
		return ManagedRuntimeApplyResult{}, ErrValidationTokenInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	validation, ok := s.validations[token]
	delete(s.validations, token)
	if !ok || validation.sessionID != sessionID {
		return ManagedRuntimeApplyResult{}, ErrValidationTokenInvalid
	}
	if !s.now().UTC().Before(validation.expiresAt) {
		return ManagedRuntimeApplyResult{}, ErrValidationTokenExpired
	}
	if validation.revision != s.revision {
		return ManagedRuntimeApplyResult{}, runtimeconfig.ErrRevisionConflict
	}
	return s.applyLocked(ctx, validation.revision, validation.config, validation.clearCache)
}

func (s *ManagedRuntimeService) History(context.Context) ([]runtimeconfig.Revision, error) {
	if s == nil {
		return nil, ErrManagedRuntimeDisabled
	}
	return s.store.History()
}

func (s *ManagedRuntimeService) Rollback(ctx context.Context, expectedRevision, targetRevision string) (ManagedRuntimeApplyResult, error) {
	if s == nil {
		return ManagedRuntimeApplyResult{}, ErrManagedRuntimeDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != s.revision {
		return ManagedRuntimeApplyResult{}, runtimeconfig.ErrRevisionConflict
	}
	desired, err := s.store.LoadRevision(targetRevision)
	if err != nil {
		return ManagedRuntimeApplyResult{}, err
	}
	return s.applyLocked(ctx, expectedRevision, desired, managedCachesWillClear(s.view, desired))
}

func (s *ManagedRuntimeService) Reload(ctx context.Context) (ManagedRuntimeReloadResult, error) {
	if s == nil {
		return ManagedRuntimeReloadResult{}, ErrManagedRuntimeDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.base.sourcePath == "" {
		return ManagedRuntimeReloadResult{}, ErrConfigSourceUnavailable
	}
	freshBase, err := loadMergedConfig(s.base.sourcePath)
	if err != nil {
		return ManagedRuntimeReloadResult{}, err
	}
	restartRequired, err := immutableConfigChanges(s.base, freshBase)
	if err != nil {
		return ManagedRuntimeReloadResult{}, err
	}
	if len(restartRequired) > 0 {
		state, stateErr := s.stateLocked()
		if stateErr != nil {
			return ManagedRuntimeReloadResult{}, stateErr
		}
		return ManagedRuntimeReloadResult{
			State:           state,
			RestartRequired: restartRequired,
		}, nil
	}
	desired, revision, err := s.store.Read()
	if errors.Is(err, os.ErrNotExist) {
		desired, err = inspectManagedConfig(freshBase)
		revision = ""
	}
	if err != nil {
		return ManagedRuntimeReloadResult{}, err
	}
	effective, err := applyManagedConfig(freshBase, desired)
	if err != nil {
		return ManagedRuntimeReloadResult{}, err
	}
	view, err := inspectManagedConfig(effective)
	if err != nil {
		return ManagedRuntimeReloadResult{}, err
	}
	clearCache := managedCachesWillClear(s.view, view)
	if configsEqual(s.effective, effective) {
		s.base, s.effective, s.view, s.revision = freshBase, effective, view, revision
		state, err := s.stateLocked()
		return ManagedRuntimeReloadResult{State: state}, err
	}
	if err := s.stageAndSwapLocked(ctx, effective); err != nil {
		return ManagedRuntimeReloadResult{}, err
	}
	s.base, s.effective, s.view, s.revision = freshBase, effective, view, revision
	s.validations = make(map[string]runtimeValidation)
	state, err := s.stateLocked()
	if err != nil {
		return ManagedRuntimeReloadResult{}, err
	}
	result := ManagedRuntimeReloadResult{
		State:         state,
		CachesCleared: clearCache,
	}
	s.syncTelemetryLocked(effective)
	return result, nil
}

func (s *ManagedRuntimeService) Probe(ctx context.Context, tag string) ([]RuntimeProbe, error) {
	if s == nil {
		return nil, ErrManagedRuntimeDisabled
	}
	s.mu.Lock()
	var forward *runtimeconfig.FastForward
	for _, plugin := range s.view.Plugins {
		if plugin.Tag == tag && plugin.Type == "fast_forward" && plugin.Editable {
			if plugin.FastForward != nil {
				copy := *plugin.FastForward
				copy.Upstreams = append([]runtimeconfig.Upstream(nil), plugin.FastForward.Upstreams...)
				forward = &copy
			}
			break
		}
	}
	caFiles, err := managedForwardCA(s.effective, tag)
	s.mu.Unlock()
	if forward == nil {
		return nil, errors.New("upstream plugin is not editable")
	}
	if err != nil {
		return nil, err
	}
	var rootCAs *x509.CertPool
	if len(caFiles) > 0 {
		rootCAs, err = utils.LoadCertPool(caFiles)
		if err != nil {
			return nil, errors.New("load upstream CA failed")
		}
	}
	results := make([]RuntimeProbe, 0, len(forward.Upstreams))
	for i, config := range forward.Upstreams {
		result := RuntimeProbe{UpstreamID: fmt.Sprintf("%s/%d", tag, i), Rcode: -1}
		started := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		request := new(dns.Msg).SetQuestion(".", dns.TypeNS)
		request.SetEdns0(1232, false)
		var response *dns.Msg
		if strings.HasPrefix(config.Addr, "udpme://") {
			response, err = probeUDPME(probeCtx, strings.TrimPrefix(config.Addr, "udpme://"), request)
		} else {
			var client upstream.Upstream
			client, err = upstream.NewUpstream(config.Addr, &upstream.Opt{
				DialAddr: config.DialAddr, SoMark: config.SoMark, BindToDevice: config.BindToDevice,
				IdleTimeout: time.Duration(config.IdleTimeout) * time.Second, MaxConns: config.MaxConns,
				EnablePipeline: config.EnablePipeline, Bootstrap: config.Bootstrap, Insecure: config.Insecure,
				RootCAs: rootCAs, KernelTX: config.KernelTX, KernelRX: config.KernelRX, Logger: s.host.logger,
			})
			if err == nil {
				response, err = client.ExchangeContext(probeCtx, request)
				_ = client.Close()
			}
		}
		if response != nil {
			result.Rcode = response.Rcode
		}
		cancel()
		result.DurationMS = float64(time.Since(started).Microseconds()) / 1000
		result.Success = err == nil
		if err != nil {
			result.Error = "probe failed"
		}
		results = append(results, result)
	}
	return results, nil
}

func managedForwardCA(config *Config, tag string) ([]string, error) {
	if config == nil {
		return nil, nil
	}
	for _, plugin := range config.Plugins {
		if plugin.Tag != tag || plugin.Type != "fast_forward" {
			continue
		}
		var args struct {
			CA []string `yaml:"ca"`
		}
		contents, err := yaml.Marshal(plugin.Args)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(contents, &args); err != nil {
			return nil, err
		}
		return append([]string(nil), args.CA...), nil
	}
	return nil, errors.New("upstream plugin is unavailable")
}

func probeUDPME(ctx context.Context, address string, request *dns.Msg) (*dns.Msg, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "53")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "udp", address)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	dnsConnection := &dns.Conn{Conn: connection, UDPSize: 1232}
	if err := dnsConnection.WriteMsg(request); err != nil {
		return nil, err
	}
	for {
		response, err := dnsConnection.ReadMsg()
		if err != nil {
			return nil, err
		}
		if response.IsEdns0() != nil {
			return response, nil
		}
	}
}

func (s *ManagedRuntimeService) applyLocked(ctx context.Context, expectedRevision string, desired runtimeconfig.Config, clearCache bool) (ManagedRuntimeApplyResult, error) {
	if reflect.DeepEqual(s.view, desired) {
		if s.revision == "" {
			revision, err := s.store.Write(expectedRevision, desired)
			if err != nil {
				return ManagedRuntimeApplyResult{}, err
			}
			s.revision = revision
		}
		state, err := s.stateLocked()
		return ManagedRuntimeApplyResult{State: state}, err
	}
	effective, view, err := s.prepareCandidateLocked(desired)
	if err != nil {
		return ManagedRuntimeApplyResult{}, err
	}
	stage, err := s.host.runtimeManager.Stage(ctx, func(buildCtx context.Context) (*RuntimeGeneration, error) {
		return s.host.buildRuntimeGeneration(buildCtx, effective)
	})
	if err != nil {
		return ManagedRuntimeApplyResult{}, err
	}
	newRevision, err := s.store.Write(expectedRevision, desired)
	if err != nil {
		if newRevision == "" {
			_ = s.host.runtimeManager.Discard(context.Background(), stage)
			return ManagedRuntimeApplyResult{}, err
		}
		// Rename succeeded, but syncing the containing directory failed.
		// Keep the running generation aligned with the visible file and
		// report the durability warning through the service logger.
		s.host.logger.Warn("managed config replaced but directory sync failed", zap.Error(err))
	}
	if err := s.swapRuntime(stage); err != nil {
		discardErr := s.host.runtimeManager.Discard(context.Background(), stage)
		if errors.Is(discardErr, ErrRuntimeStageConsumed) {
			discardErr = nil
		}
		restoreErr := s.store.Restore(newRevision, expectedRevision)
		return ManagedRuntimeApplyResult{}, errors.Join(err, discardErr, restoreErr)
	}
	s.effective, s.view, s.revision = effective, view, newRevision
	s.validations = make(map[string]runtimeValidation)
	state, err := s.stateLocked()
	if err != nil {
		return ManagedRuntimeApplyResult{}, err
	}
	result := ManagedRuntimeApplyResult{
		State:         state,
		CachesCleared: clearCache,
	}
	s.syncTelemetryLocked(effective)
	return result, nil
}

func (s *ManagedRuntimeService) prepareCandidateLocked(desired runtimeconfig.Config) (*Config, runtimeconfig.Config, error) {
	effective, err := applyManagedConfig(s.base, desired)
	if err != nil {
		return nil, runtimeconfig.Config{}, err
	}
	if _, _, err := validateControlConfig(effective); err != nil {
		return nil, runtimeconfig.Config{}, err
	}
	view, err := inspectManagedConfig(effective)
	if err != nil {
		return nil, runtimeconfig.Config{}, err
	}
	return effective, view, nil
}

func (s *ManagedRuntimeService) stageAndSwapLocked(ctx context.Context, effective *Config) error {
	stage, err := s.host.runtimeManager.Stage(ctx, func(buildCtx context.Context) (*RuntimeGeneration, error) {
		return s.host.buildRuntimeGeneration(buildCtx, effective)
	})
	if err != nil {
		return err
	}
	if err := s.swapRuntime(stage); err != nil {
		discardErr := s.host.runtimeManager.Discard(context.Background(), stage)
		if errors.Is(discardErr, ErrRuntimeStageConsumed) {
			discardErr = nil
		}
		return errors.Join(err, discardErr)
	}
	return nil
}

func (s *ManagedRuntimeService) syncTelemetryLocked(config *Config) {
	updater, ok := s.host.telemetry.(telemetrySettingsUpdater)
	if !ok {
		s.host.logger.Error("telemetry backend does not support runtime settings")
		return
	}
	if err := updater.UpdateSettings(telemetrySettings(config.Control)); err != nil {
		// Runtime config validation uses the same retention bounds as the built-in
		// telemetry store, so this is only possible for a custom backend. The DNS
		// generation and managed file are already committed at this point; report
		// the apply as successful to prevent clients from repeating the commit.
		s.host.logger.Error("failed to update telemetry runtime settings", zap.Error(err))
	}
}

func (s *ManagedRuntimeService) stateLocked() (ManagedRuntimeState, error) {
	view, err := cloneManagedConfig(s.view)
	if err != nil {
		return ManagedRuntimeState{}, err
	}
	return ManagedRuntimeState{Revision: s.revision, Config: view}, nil
}

func (s *ManagedRuntimeService) pruneValidationsLocked(now time.Time) {
	for token, validation := range s.validations {
		if !now.Before(validation.expiresAt) {
			delete(s.validations, token)
		}
	}
}

func newValidationToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func inspectManagedConfig(config *Config) (runtimeconfig.Config, error) {
	if config.Control == nil {
		return runtimeconfig.Config{}, errors.New("control config is required")
	}
	return runtimeconfig.Inspect(config.Control.QueryLog, managedTelemetry(config.Control), pluginSources(config.Plugins))
}

func managedTelemetry(config *ControlConfig) runtimeconfig.Telemetry {
	aggregate := config.Telemetry.AggregateRetentionDays
	if aggregate == 0 {
		aggregate = 7
	}
	query := config.Telemetry.QueryRetentionHours
	if query == 0 {
		query = 24
	}
	records := config.Telemetry.MaxQueryRecords
	if records == 0 {
		records = 100000
	}
	return runtimeconfig.Telemetry{AggregateRetentionDays: aggregate, QueryRetentionHours: query, MaxQueryRecords: records}
}

func pluginSources(plugins []PluginConfig) []runtimeconfig.PluginSource {
	result := make([]runtimeconfig.PluginSource, 0, len(plugins))
	for _, plugin := range plugins {
		result = append(result, runtimeconfig.PluginSource{Tag: plugin.Tag, Type: plugin.Type, Args: plugin.Args})
	}
	return result
}

func applyManagedConfig(base *Config, desired runtimeconfig.Config) (*Config, error) {
	config, err := cloneConfig(base)
	if err != nil {
		return nil, err
	}
	if config.Control == nil {
		return nil, errors.New("control config is required")
	}
	applied, err := runtimeconfig.Apply(pluginSources(config.Plugins), desired)
	if err != nil {
		return nil, err
	}
	for i := range applied {
		config.Plugins[i] = PluginConfig{Tag: applied[i].Tag, Type: applied[i].Type, Args: applied[i].Args}
	}
	config.Control.QueryLog = desired.QueryLog
	config.Control.Telemetry.AggregateRetentionDays = desired.Telemetry.AggregateRetentionDays
	config.Control.Telemetry.QueryRetentionHours = desired.Telemetry.QueryRetentionHours
	config.Control.Telemetry.MaxQueryRecords = desired.Telemetry.MaxQueryRecords
	return config, nil
}

func cloneConfig(config *Config) (*Config, error) {
	encoded, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	var clone Config
	if err := yaml.Unmarshal(encoded, &clone); err != nil {
		return nil, err
	}
	clone.sourcePath = config.sourcePath
	return &clone, nil
}

func cloneManagedConfig(config runtimeconfig.Config) (runtimeconfig.Config, error) {
	encoded, err := yaml.Marshal(config)
	if err != nil {
		return runtimeconfig.Config{}, err
	}
	var clone runtimeconfig.Config
	if err := yaml.Unmarshal(encoded, &clone); err != nil {
		return runtimeconfig.Config{}, err
	}
	return clone, nil
}

func managedCachesWillClear(before, after runtimeconfig.Config) bool {
	if reflect.DeepEqual(before, after) {
		return false
	}
	for _, plugin := range before.Plugins {
		if plugin.Editable && plugin.Type == "cache" && plugin.Cache != nil {
			return true
		}
	}
	return false
}

func configsEqual(before, after *Config) bool {
	beforeEncoded, beforeErr := yaml.Marshal(before)
	afterEncoded, afterErr := yaml.Marshal(after)
	return beforeErr == nil && afterErr == nil && reflect.DeepEqual(beforeEncoded, afterEncoded)
}

func immutableConfigChanges(before, after *Config) ([]string, error) {
	var changes []string
	if !reflect.DeepEqual(before.Log, after.Log) {
		changes = append(changes, "log")
	}
	if !yamlValuesEqual(before.DataProviders, after.DataProviders) {
		changes = append(changes, "data_providers")
	}
	if !yamlValuesEqual(before.Servers, after.Servers) {
		changes = append(changes, "servers")
	}
	if !reflect.DeepEqual(before.API, after.API) {
		changes = append(changes, "api")
	}
	if !reflect.DeepEqual(before.Security, after.Security) {
		changes = append(changes, "security")
	}
	beforeControl, afterControl := before.Control, after.Control
	if (beforeControl == nil) != (afterControl == nil) {
		changes = append(changes, "control")
	} else if beforeControl != nil {
		beforeCopy, afterCopy := *beforeControl, *afterControl
		beforeCopy.QueryLog, afterCopy.QueryLog = false, false
		beforeCopy.Telemetry.AggregateRetentionDays, afterCopy.Telemetry.AggregateRetentionDays = 0, 0
		beforeCopy.Telemetry.QueryRetentionHours, afterCopy.Telemetry.QueryRetentionHours = 0, 0
		beforeCopy.Telemetry.MaxQueryRecords, afterCopy.Telemetry.MaxQueryRecords = 0, 0
		if !reflect.DeepEqual(beforeCopy, afterCopy) {
			changes = append(changes, "control")
		}
	}
	if len(before.Plugins) != len(after.Plugins) {
		changes = append(changes, "plugins")
	} else {
		for i := range before.Plugins {
			beforeSource := runtimeconfig.PluginSource{Tag: before.Plugins[i].Tag, Type: before.Plugins[i].Type, Args: before.Plugins[i].Args}
			afterSource := runtimeconfig.PluginSource{Tag: after.Plugins[i].Tag, Type: after.Plugins[i].Type, Args: after.Plugins[i].Args}
			equal, err := runtimeconfig.ImmutableArgsEqual(beforeSource, afterSource)
			if err != nil {
				return nil, err
			}
			if !equal {
				tag := beforeSource.Tag
				if tag == "" {
					tag = fmt.Sprintf("%d", i)
				}
				changes = append(changes, "plugins."+tag)
			}
		}
	}
	sort.Strings(changes)
	return compactStrings(changes), nil
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func yamlValuesEqual(before, after any) bool {
	beforeEncoded, beforeErr := yaml.Marshal(before)
	afterEncoded, afterErr := yaml.Marshal(after)
	return beforeErr == nil && afterErr == nil && string(beforeEncoded) == string(afterEncoded)
}
