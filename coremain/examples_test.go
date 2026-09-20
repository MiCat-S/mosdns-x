package coremain

import (
	"path/filepath"
	"testing"
)

// Every shipped example must decode under the strict decoder, which rejects
// unknown keys. Two of these files are packaged into each release archive, so
// a typo would reach users and only surface on their first start.
func TestShippedExamplesLoad(t *testing.T) {
	matches, err := filepath.Glob("../examples/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no example configs found")
	}
	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cfg, _, err := loadConfig(path)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if cfg.Control == nil {
				t.Fatalf("%s: expected a control section", path)
			}
			// The admit defaults are documented as the values the code applies.
			// Pin them so the examples cannot drift away from the code.
			if got := cfg.Control.Admit; got != (AdmitConfig{QueueSize: 1024, WaitMS: 250, BatchDelayMS: 1, BatchSize: 128}) {
				t.Fatalf("%s: admit = %+v, does not match the documented defaults", path, got)
			}
			if _, _, err := validateControlConfig(cfg); err != nil {
				t.Fatalf("%s: validation: %v", path, err)
			}
		})
	}
}
