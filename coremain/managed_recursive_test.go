package coremain

import (
	"context"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/internal/runtimeconfig"
)

func TestManagedRecursiveValidateApplyAndRollback(t *testing.T) {
	config := managedRuntimeTestConfig(t)
	config.Plugins = []PluginConfig{{Tag: "recursive", Type: "recursive", Args: map[string]any{
		"label": "原始名称", "cache_size": 1024, "no_ipv6": true,
		"disable_dnssec": true, "bind_to_device": "", "so_mark": 0,
	}}}
	config.Servers[0].Exec = "recursive"
	service, host := newManagedRuntimeTestService(t, config)
	state, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Config.Plugins[0].Editable || state.Config.Plugins[0].Recursive == nil || !state.Sources.Running.Plugins[0].Capability.ClearsMemoryCaches {
		t.Fatalf("recursive edit/cache capability missing: %+v", state)
	}

	assertRunningLabel := func(want string) {
		t.Helper()
		active, err := host.runtimeManager.ActiveUpstreams()
		if err != nil || len(active) != 1 || active[0].Label != want {
			t.Fatalf("running upstreams = %+v, %v; want %s", active, err, want)
		}
		// Exercise the rebuilt entry without depending on public DNS.
		q := new(dns.Msg).SetQuestion("example.test.", dns.TypeAXFR)
		r, err := host.runtimeManager.DNSHandler(0, 0).ServeDNS(context.Background(), q, nil)
		if err != nil || r.Rcode != dns.RcodeNotImplemented {
			t.Fatalf("rebuilt recursive entry: %v, %v", r, err)
		}
	}
	assertRunningLabel("原始名称")
	desired := state.Config
	desired.Plugins[0].Recursive.Label = "面板名称"
	desired.Plugins[0].Recursive.CacheSize = 2048
	validation, err := service.Validate(context.Background(), "admin-session", state.Revision, desired)
	if err != nil || !validation.WillClearCaches {
		t.Fatalf("validation cache preview = %+v, %v", validation, err)
	}
	assertRunningLabel("原始名称") // validation stages and discards; it does not switch
	first, err := service.Apply(context.Background(), "admin-session", validation.Token)
	if err != nil || !first.CachesCleared {
		t.Fatalf("apply = %+v, %v", first, err)
	}
	if err := host.runtimeManager.WaitForPreviousDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRunningLabel("面板名称")

	invalid := first.Config
	invalid.Plugins = append([]runtimeconfig.Plugin(nil), first.Config.Plugins...)
	invalidRecursive := *first.Config.Plugins[0].Recursive
	invalidRecursive.NoIPv4 = true
	invalid.Plugins[0].Recursive = &invalidRecursive
	if _, err := service.Validate(context.Background(), "admin-session", first.Revision, invalid); err == nil {
		t.Fatal("both address families disabled passed validation")
	}
	assertRunningLabel("面板名称")

	secondDesired := first.Config
	secondDesired.Plugins[0].Recursive.Prefetch = true
	second := applyManagedRuntimeTestConfig(t, service, first.Revision, secondDesired)
	if err := host.runtimeManager.WaitForPreviousDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := service.Rollback(context.Background(), second.Revision, first.Revision)
	if err != nil || !rolledBack.CachesCleared || rolledBack.Config.Plugins[0].Recursive.Prefetch || rolledBack.Config.Plugins[0].Recursive.CacheSize != 2048 {
		t.Fatalf("recursive rollback = %+v, %v", rolledBack, err)
	}
	assertRunningLabel("面板名称")
}

func TestRecursiveOnlyRuntimeWarnsAboutCacheReset(t *testing.T) {
	before := runtimeconfig.Config{Plugins: []runtimeconfig.Plugin{{Tag: "recursive", Type: "recursive"}}}
	after := runtimeconfig.Config{QueryLog: true, Plugins: before.Plugins}
	if !managedCachesWillClear(before, after) {
		t.Fatal("recursive cache reset was not reported when rebuilding for query logging")
	}
	if managedCachesWillClear(before, before) {
		t.Fatal("unchanged configuration reported cache reset")
	}
}
