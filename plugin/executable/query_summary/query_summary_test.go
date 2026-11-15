package query_summary

import (
	"testing"

	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/coremain"
)

func TestPluginLoadsFromConfigArgs(t *testing.T) {
	p, err := coremain.NewPlugin(&coremain.PluginConfig{
		Tag:  "summary",
		Type: PluginType,
		Args: map[string]interface{}{"msg": "custom summary"},
	}, zap.NewNop(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*logger).args.Msg; got != "custom summary" {
		t.Fatalf("msg = %q, want the configured one", got)
	}
}
