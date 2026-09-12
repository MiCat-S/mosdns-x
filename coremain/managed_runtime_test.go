package coremain

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/pmkol/mosdns-x/internal/runtimeconfig"
	"github.com/pmkol/mosdns-x/internal/telemetry"
	"github.com/pmkol/mosdns-x/pkg/query_context"
)

func managedRuntimeTestConfig(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	return &Config{
		Control: &ControlConfig{
			Database:      filepath.Join(dir, "control.db"),
			StatsDatabase: filepath.Join(dir, "stats.db"),
			PublicDNSURL:  "https://dns.example.test/dns-query",
			PanelOrigin:   "https://panel.example.test",
			ManagedConfig: filepath.Join(dir, "managed.yaml"),
		},
		API: APIConfig{HTTP: "127.0.0.1:8080"},
		Plugins: []PluginConfig{
			{Tag: "forward", Type: "fast_forward", Args: map[string]any{"upstream": []any{map[string]any{"addr": "udp://127.0.0.1:9"}}}},
			{Tag: "cache", Type: "cache", Args: map[string]any{"size": 128}},
			{Tag: "answer", Type: "blackhole", Args: map[string]any{"rcode": 0}},
		},
		Servers: []ServerConfig{{Exec: "answer", Listeners: []*ServerListenerConfig{{Protocol: "udp", Addr: "127.0.0.1:5353"}}}},
	}
}

func newManagedRuntimeTestService(t *testing.T, config *Config) (*ManagedRuntimeService, *Mosdns) {
	t.Helper()
	stats, err := telemetry.Open(telemetry.Options{Path: config.Control.StatsDatabase})
	if err != nil {
		t.Fatal(err)
	}
	host := &Mosdns{
		logger:         zap.NewNop(),
		runtimeManager: NewRuntimeManager(),
		telemetry:      stats,
		controlCfg:     config.Control,
	}
	stage, err := host.runtimeManager.Stage(context.Background(), func(ctx context.Context) (*RuntimeGeneration, error) {
		return host.buildRuntimeGeneration(ctx, config)
	})
	if err != nil {
		stats.Close()
		t.Fatal(err)
	}
	if err := host.runtimeManager.Swap(stage); err != nil {
		stats.Close()
		t.Fatal(err)
	}
	store, err := runtimeconfig.NewStore(config.Control.ManagedConfig, managedHistoryLimit)
	if err != nil {
		t.Fatal(err)
	}
	base, err := cloneConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	view, err := inspectManagedConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	service := newManagedRuntimeService(host, store, base, config, view, "")
	t.Cleanup(func() {
		_ = host.runtimeManager.Drain(context.Background())
		_ = stats.Close()
	})
	return service, host
}

func applyManagedRuntimeTestConfig(t *testing.T, service *ManagedRuntimeService, revision string, desired runtimeconfig.Config) ManagedRuntimeApplyResult {
	t.Helper()
	validation, err := service.Validate(context.Background(), "admin-session", revision, desired)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), "admin-session", validation.Token)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestManagedRuntimeValidateApplyAndRollback(t *testing.T) {
	config := managedRuntimeTestConfig(t)
	service, host := newManagedRuntimeTestService(t, config)
	state, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	desired := state.Config
	desired.QueryLog = true
	for i := range desired.Plugins {
		if desired.Plugins[i].Tag == "cache" {
			desired.Plugins[i].Cache.Size = 256
		}
	}
	first := applyManagedRuntimeTestConfig(t, service, state.Revision, desired)
	if first.Revision == "" || !first.CachesCleared {
		t.Fatalf("first apply = %+v", first)
	}
	if settings := host.telemetry.(*telemetry.Store).Settings(); !settings.QueryLogEnabled {
		t.Fatal("telemetry query logging was not updated after swap")
	}
	if err := host.runtimeManager.WaitForPreviousDrain(context.Background()); err != nil {
		t.Fatal(err)
	}

	secondDesired := first.Config
	for i := range secondDesired.Plugins {
		if secondDesired.Plugins[i].Tag == "cache" {
			secondDesired.Plugins[i].Cache.Size = 512
		}
	}
	second := applyManagedRuntimeTestConfig(t, service, first.Revision, secondDesired)
	if err := host.runtimeManager.WaitForPreviousDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	history, err := service.History(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Revision != first.Revision {
		t.Fatalf("history = %+v, want first applied revision", history)
	}
	rolledBack, err := service.Rollback(context.Background(), second.Revision, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var cacheSize int
	for _, plugin := range rolledBack.Config.Plugins {
		if plugin.Tag == "cache" {
			cacheSize = plugin.Cache.Size
		}
	}
	if cacheSize != 256 || !rolledBack.CachesCleared {
		t.Fatalf("rollback = %+v", rolledBack)
	}
}

func TestManagedRuntimeWarnsWhenNonCacheChangeRebuildsMemoryCache(t *testing.T) {
	config := managedRuntimeTestConfig(t)
	service, host := newManagedRuntimeTestService(t, config)
	state, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	desired := state.Config
	for i := range desired.Plugins {
		if desired.Plugins[i].Tag == "forward" {
			desired.Plugins[i].FastForward.Upstreams[0].MaxConns = 2
		}
	}
	validation, err := service.Validate(context.Background(), "admin-session", state.Revision, desired)
	if err != nil {
		t.Fatal(err)
	}
	if !validation.WillClearCaches {
		t.Fatal("validation did not warn that rebuilding the generation clears memory caches")
	}
	result, err := service.Apply(context.Background(), "admin-session", validation.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CachesCleared {
		t.Fatal("apply did not report cleared memory caches")
	}
	if err := host.runtimeManager.WaitForPreviousDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeBadCandidateDoesNotWriteOrSwitch(t *testing.T) {
	config := managedRuntimeTestConfig(t)
	service, host := newManagedRuntimeTestService(t, config)
	state, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	desired := state.Config
	for i := range desired.Plugins {
		if desired.Plugins[i].Tag == "forward" {
			desired.Plugins[i].FastForward.Upstreams[0].Addr = "unsupported://dns.example.test"
		}
	}
	if _, err := service.Validate(context.Background(), "admin-session", state.Revision, desired); err == nil {
		t.Fatal("Validate accepted an unsupported upstream protocol")
	}
	if _, err := os.Stat(config.Control.ManagedConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed file changed after failed validation: %v", err)
	}
	request := new(dns.Msg).SetQuestion("still-current.test.", dns.TypeA)
	response, err := host.runtimeManager.DNSHandler(0, 0).ServeDNS(context.Background(), request, query_context.NewRequestMeta(netip.Addr{}))
	if err != nil || response.Rcode != dns.RcodeSuccess {
		t.Fatalf("current generation changed after failed validation: response=%v err=%v", response, err)
	}
}

func TestManagedRuntimeSwapFailureRestoresManagedFile(t *testing.T) {
	config := managedRuntimeTestConfig(t)
	service, host := newManagedRuntimeTestService(t, config)
	state, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	desired := state.Config
	desired.QueryLog = true
	service.swapRuntime = func(*RuntimeStage) error { return errors.New("injected swap failure") }
	validation, err := service.Validate(context.Background(), "admin-session", state.Revision, desired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "admin-session", validation.Token); err == nil || !strings.Contains(err.Error(), "injected swap failure") {
		t.Fatalf("Apply error = %v, want injected swap failure", err)
	}
	if _, err := os.Stat(config.Control.ManagedConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed file was not restored after swap failure: %v", err)
	}
	current, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != state.Revision || current.Config.QueryLog != state.Config.QueryLog {
		t.Fatalf("service state changed after swap failure: before=%+v after=%+v", state, current)
	}
	request := new(dns.Msg).SetQuestion("still-current.test.", dns.TypeA)
	response, err := host.runtimeManager.DNSHandler(0, 0).ServeDNS(context.Background(), request, query_context.NewRequestMeta(netip.Addr{}))
	if err != nil || response.Rcode != dns.RcodeSuccess {
		t.Fatalf("current generation changed after swap failure: response=%v err=%v", response, err)
	}
}

type failingTelemetryUpdater struct {
	telemetry.Service
	calls int
}

func (t *failingTelemetryUpdater) UpdateSettings(telemetry.Settings) error {
	t.calls++
	return errors.New("injected telemetry update failure")
}

func TestManagedRuntimeTelemetryFailureDoesNotReportCommittedApplyAsFailed(t *testing.T) {
	config := managedRuntimeTestConfig(t)
	service, host := newManagedRuntimeTestService(t, config)
	failing := &failingTelemetryUpdater{Service: host.telemetry}
	host.telemetry = failing
	state, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	desired := state.Config
	desired.QueryLog = true
	result := applyManagedRuntimeTestConfig(t, service, state.Revision, desired)
	if failing.calls != 1 {
		t.Fatalf("telemetry update calls = %d, want 1", failing.calls)
	}
	if result.Revision == "" || !result.Config.QueryLog {
		t.Fatalf("committed apply result = %+v", result)
	}
	persisted, revision, err := service.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if revision != result.Revision || !persisted.QueryLog {
		t.Fatalf("persisted config = (%s, %+v), want committed result", revision, persisted)
	}
	current, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != result.Revision || !current.Config.QueryLog {
		t.Fatalf("runtime state = %+v, want committed result", current)
	}
}

func TestManagedRuntimeReloadReportsImmutableChanges(t *testing.T) {
	config := managedRuntimeTestConfig(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	config.sourcePath = configPath
	encoded, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	service, _ := newManagedRuntimeTestService(t, config)
	config.API.HTTP = "127.0.0.1:9090"
	encoded, err = yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.Reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RestartRequired) != 1 || result.RestartRequired[0] != "api" {
		t.Fatalf("restart_required = %v, want [api]", result.RestartRequired)
	}
}

func TestManagedRuntimeProbeResponseDoesNotExposeAddress(t *testing.T) {
	config := managedRuntimeTestConfig(t)
	service, _ := newManagedRuntimeTestService(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	results, err := service.Probe(ctx, "forward")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "127.0.0.1") || strings.Contains(string(encoded), "udp://") {
		t.Fatalf("probe leaked upstream address: %s", encoded)
	}
}

func TestManagedRuntimeProbeSupportsUDPME(t *testing.T) {
	connection, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, dns.MaxMsgSize)
		n, client, readErr := connection.ReadFrom(buffer)
		if readErr != nil {
			serverDone <- readErr
			return
		}
		request := new(dns.Msg)
		if unpackErr := request.Unpack(buffer[:n]); unpackErr != nil {
			serverDone <- unpackErr
			return
		}
		response := new(dns.Msg)
		response.SetReply(request)
		response.SetEdns0(1232, false)
		packed, packErr := response.Pack()
		if packErr != nil {
			serverDone <- packErr
			return
		}
		_, writeErr := connection.WriteTo(packed, client)
		serverDone <- writeErr
	}()

	config := managedRuntimeTestConfig(t)
	config.Plugins[0].Args = map[string]any{"upstream": []any{map[string]any{"addr": "udpme://" + connection.LocalAddr().String()}}}
	service, _ := newManagedRuntimeTestService(t, config)
	results, err := service.Probe(context.Background(), "forward")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].Rcode != dns.RcodeSuccess {
		t.Fatalf("probe results = %+v, want one successful udpme response", results)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
