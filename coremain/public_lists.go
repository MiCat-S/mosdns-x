package coremain

import "path/filepath"

func publicListDirectory(cfg *Config) string {
	if cfg != nil && cfg.Control != nil {
		if cfg.Control.ManagedConfig != "" {
			return filepath.Join(filepath.Dir(cfg.Control.ManagedConfig), "public-lists")
		}
		if effectiveControlDriver(cfg.Control) == "bbolt" && cfg.Control.Database != "" {
			return filepath.Join(filepath.Dir(cfg.Control.Database), "public-lists")
		}
	}
	if cfg != nil && cfg.sourcePath != "" {
		return filepath.Join(filepath.Dir(cfg.sourcePath), "public-lists")
	}
	return "public-lists"
}
