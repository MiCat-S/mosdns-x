package coremain

import (
	"testing"

	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/server"
)

func TestListenerLimitsArePartOfTopology(t *testing.T) {
	base := ServerListenerConfig{Protocol: "tcp", Addr: "127.0.0.1:53"}
	for name, mutate := range map[string]func(*ServerListenerConfig){
		"max_concurrent_queries": func(c *ServerListenerConfig) { c.MaxConcurrentQueries = 8 },
		"max_connections":        func(c *ServerListenerConfig) { c.MaxConnections = 8 },
	} {
		changed := base
		mutate(&changed)
		if listenerIdentity(&base) == listenerIdentity(&changed) {
			t.Fatalf("changing %s does not change the listener identity", name)
		}
	}
}

func TestRegisterServerMetrics(t *testing.T) {
	m := &Mosdns{logger: zap.NewNop(), metricsReg: newMetricsReg()}
	s := server.NewServer(server.ServerOpts{MaxConcurrentQueries: 4})
	m.registerServerMetrics(&ServerListenerConfig{Addr: "127.0.0.1:53"}, s)

	families, err := m.metricsReg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"mosdns_server_inflight_queries": false,
		"mosdns_server_open_connections": false,
	}
	for _, f := range families {
		if _, ok := want[f.GetName()]; !ok {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "listener" && l.GetValue() == "udp://127.0.0.1:53" {
					want[f.GetName()] = true
				}
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("metric %s with listener label not registered", name)
		}
	}
}
