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
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/go-viper/mapstructure/v2"
	"github.com/kardianos/service"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/mlog"
)

type serverFlags struct {
	c         string
	dir       string
	cpu       int
	asService bool
}

var rootCmd = &cobra.Command{
	Use: "mosdns",
}

func init() {
	sf := new(serverFlags)
	startCmd := &cobra.Command{
		Use:   "start [-c config_file] [-d working_dir]",
		Short: "Start mosdns main program.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if sf.asService {
				svc, err := service.New(&serverService{f: sf}, svcCfg)
				if err != nil {
					return fmt.Errorf("failed to init service, %w", err)
				}
				return svc.Run()
			}
			return StartServer(sf)
		},
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
	}
	rootCmd.AddCommand(startCmd)
	fs := startCmd.Flags()
	fs.StringVarP(&sf.c, "config", "c", "", "config file")
	fs.StringVarP(&sf.dir, "dir", "d", "", "working dir")
	fs.IntVar(&sf.cpu, "cpu", 0, "set runtime.GOMAXPROCS")
	fs.BoolVar(&sf.asService, "as-service", false, "start as a service")
	fs.MarkHidden("as-service")

	serviceCmd := &cobra.Command{
		Use:   "service",
		Short: "Manage mosdns as a system service.",
	}
	serviceCmd.PersistentPreRunE = initService
	serviceCmd.AddCommand(
		newSvcInstallCmd(),
		newSvcUninstallCmd(),
		newSvcStartCmd(),
		newSvcStopCmd(),
		newSvcRestartCmd(),
		newSvcStatusCmd(),
	)
	rootCmd.AddCommand(serviceCmd)
}

func AddSubCmd(c *cobra.Command) {
	rootCmd.AddCommand(c)
}

func Run() error {
	return rootCmd.Execute()
}

func StartServer(sf *serverFlags) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return StartServerContext(ctx, sf)
}

func StartServerContext(ctx context.Context, sf *serverFlags) error {
	if sf.cpu > 0 {
		runtime.GOMAXPROCS(sf.cpu)
	}

	if len(sf.dir) > 0 {
		err := os.Chdir(sf.dir)
		if err != nil {
			return fmt.Errorf("failed to change the current working directory, %w", err)
		}
		mlog.L().Info("working directory changed", zap.String("path", sf.dir))
	}

	cfg, err := loadMergedConfig(sf.c)
	if err != nil {
		return err
	}

	if err := RunMosdnsContext(ctx, cfg); err != nil {
		return fmt.Errorf("mosdns exited, %w", err)
	}
	return nil
}

func loadMergedConfig(filePath string) (*Config, error) {
	cfg, fileUsed, err := loadConfig(filePath)
	if err != nil {
		return nil, fmt.Errorf("fail to load config, %w", err)
	}
	if err := mergeInclude(cfg, fileUsed); err != nil {
		return nil, fmt.Errorf("failed to load sub config file, %w", err)
	}
	absPath, err := filepath.Abs(fileUsed)
	if err != nil {
		return nil, fmt.Errorf("resolve config source path: %w", err)
	}
	cfg.sourcePath = absPath
	return cfg, nil
}

// loadConfig load a config from a file. If filePath is empty, it will
// automatically search and load a file which name start with "config".
func loadConfig(filePath string) (*Config, string, error) {
	v := viper.New()

	if len(filePath) > 0 {
		v.SetConfigFile(filePath)
	} else {
		v.SetConfigName("config")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, "", fmt.Errorf("failed to read config: %w", err)
	}

	decoderOpt := func(cfg *mapstructure.DecoderConfig) {
		cfg.ErrorUnused = true
		cfg.TagName = "yaml"
		cfg.WeaklyTypedInput = true
	}

	cfg := new(Config)
	if err := v.Unmarshal(cfg, decoderOpt); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal config: %w", err)
	}
	return cfg, v.ConfigFileUsed(), nil
}

// maxIncludeDepth bounds how deep include chains may nest. Cycles are
// detected explicitly; this is only a safety net against pathological trees.
const maxIncludeDepth = 8

// includeWalker carries the state of one include expansion.
type includeWalker struct {
	// visiting holds the canonical paths on the current recursion stack.
	visiting map[string]struct{}
	// loaded holds the canonical paths already merged anywhere in the tree.
	loaded map[string]struct{}
}

// mergeInclude expands cfg.Include recursively. filePath is the file cfg was
// read from and anchors relative includes. Relative include paths resolve
// against the directory of the file that contains the include (as it was
// reached, before symlink evaluation); absolute paths are used as-is.
//
// Every file is identified by its canonical path (absolute, symlinks
// evaluated). A file that is already on the current include stack is a cycle
// and fails. A file reached again through another branch is merged only once:
// the first occurrence wins and later references are skipped.
//
// Included items are placed before the including file's own items, in include
// order.
func mergeInclude(cfg *Config, filePath string) error {
	absPath, canonical, err := canonicalConfigPath(filePath)
	if err != nil {
		return err
	}
	w := &includeWalker{
		visiting: map[string]struct{}{canonical: {}},
		loaded:   map[string]struct{}{canonical: {}},
	}
	return w.merge(cfg, absPath, 0, []string{filePath})
}

// merge merges the includes of cfg, which was read from absPath. chain holds
// the original spellings of the files on the stack, including absPath's.
func (w *includeWalker) merge(cfg *Config, absPath string, depth int, chain []string) error {
	depth++
	if depth > maxIncludeDepth {
		return fmt.Errorf("maximum include depth reached, include path is %s", strings.Join(chain, " -> "))
	}

	baseDir := filepath.Dir(absPath)
	includedCfg := new(Config)
	for _, subCfgFile := range cfg.Include {
		subChain := append(chain[:len(chain):len(chain)], subCfgFile)
		resolved := subCfgFile
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(baseDir, resolved)
		}
		subAbs, subCanonical, err := canonicalConfigPath(resolved)
		if err != nil {
			return err
		}
		if _, ok := w.visiting[subCanonical]; ok {
			return fmt.Errorf("include cycle: %s", strings.Join(subChain, " -> "))
		}
		if _, ok := w.loaded[subCanonical]; ok {
			mlog.L().Info("skipping sub config that is already included",
				zap.String("file", subCfgFile), zap.String("path", subCanonical))
			continue
		}

		mlog.L().Info("reading sub config", zap.String("file", subCfgFile), zap.String("path", subAbs))
		subCfg, _, err := loadConfig(subAbs)
		if err != nil {
			return fmt.Errorf("failed to load sub config %s, %w", subCfgFile, err)
		}
		w.loaded[subCanonical] = struct{}{}
		w.visiting[subCanonical] = struct{}{}
		err = w.merge(subCfg, subAbs, depth, subChain)
		delete(w.visiting, subCanonical)
		if err != nil {
			return err
		}

		includedCfg.DataProviders = append(includedCfg.DataProviders, subCfg.DataProviders...)
		includedCfg.Plugins = append(includedCfg.Plugins, subCfg.Plugins...)
		includedCfg.Servers = append(includedCfg.Servers, subCfg.Servers...)
	}

	cfg.DataProviders = append(includedCfg.DataProviders, cfg.DataProviders...)
	cfg.Plugins = append(includedCfg.Plugins, cfg.Plugins...)
	cfg.Servers = append(includedCfg.Servers, cfg.Servers...)
	return nil
}

// canonicalConfigPath returns the absolute form of path and its canonical form
// with symlinks evaluated. When the file cannot be resolved (for example it
// does not exist yet), the canonical form falls back to the absolute path so
// that the subsequent read reports the real error.
func canonicalConfigPath(path string) (string, string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve config path %s: %w", path, err)
	}
	canonical, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		canonical = absPath
	}
	return absPath, canonical, nil
}
