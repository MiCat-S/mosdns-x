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

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/pmkol/mosdns-x/mlog"
)

func newConvCmd() *cobra.Command {
	var (
		in  string
		out string
	)

	c := &cobra.Command{
		Use:   "conv -i input_cfg -o output_cfg",
		Args:  cobra.NoArgs,
		Short: "Convert configuration file format. Supported extensions: " + strings.Join(viper.SupportedExts, ", "),
		Run: func(cmd *cobra.Command, args []string) {
			if err := convCfg(in, out); err != nil {
				mlog.S().Fatal(err)
			}
		},
		DisableFlagsInUseLine: true,
	}
	c.Flags().StringVarP(&in, "in", "i", "", "input config")
	c.Flags().StringVarP(&out, "out", "o", "", "output config")
	c.MarkFlagRequired("in")
	c.MarkFlagRequired("out")
	c.MarkFlagFilename("in")
	c.MarkFlagFilename("out")
	return c
}

func newGenCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "gen config_file",
		Short: "Generate a template config. Supported extensions: " + strings.Join(viper.SupportedExts, ", "),
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if err := genCfg(args[0]); err != nil {
				mlog.S().Fatal(err)
			}
		},
		DisableFlagsInUseLine: true,
	}
	return c
}

func convCfg(in, out string) error {
	v := viper.New()
	v.SetConfigFile(in)
	if err := v.ReadInConfig(); err != nil {
		return err
	}
	return v.SafeWriteConfigAs(out)
}

// templateConfig is the generated starting configuration. Optional keys are
// written with the value the code would apply when they are absent, so the
// effective configuration is visible without cross-referencing the docs.
// Changing a value here changes behavior; deleting a key restores the default.
//
// The control panel is deliberately absent. Enabling it requires an
// administrator in the control database, and mosdns refuses to start without
// one, so a generated default carrying an active control section would fail on
// a fresh install. See examples/control-*.yaml for complete control
// configurations with every default written out.
const templateConfig = `log:
  # Log level: debug, info, warn or error.
  level: info
  # Log destination. Empty writes to stderr.
  file: ""

plugins:
  - tag: forward_google
    type: fast_forward
    args:
      upstream:
        - addr: https://8.8.8.8/dns-query
          # Answers are accepted without further validation when trusted.
          trusted: false
          # Seconds an idle connection is reused. 0 selects the protocol default.
          idle_timeout: 0
          # 0 means no limit.
          max_conns: 0
          enable_pipeline: false
          # Skips upstream certificate verification when true. Leave false.
          insecure: false

servers:
  - exec: forward_google
    # Query timeout in seconds. 0 selects 5.
    timeout: 5
    listeners:
      - protocol: udp
        addr: 127.0.0.1:5533
      - protocol: tcp
        addr: 127.0.0.1:5533
        # Connection idle timeout in seconds. 0 selects 10 for tcp, dot and
        # doh, and 30 for doq and doh3.
        idle_timeout: 10
        # PROXY protocol is rejected in control mode.
        proxy_protocol: false

# Management API. Keep it on loopback; control mode requires that.
api:
  http: "127.0.0.1:9080"
`

func genCfg(out string) error {
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(templateConfig)); err != nil {
		return err
	}
	if err := backupExisting(out); err != nil {
		return err
	}
	// A YAML target is written verbatim. Round-tripping through viper drops
	// every comment and reorders keys alphabetically, which would strip the
	// documented defaults this template exists to show. Other extensions still
	// go through viper, which is what converts the format.
	switch strings.ToLower(filepath.Ext(out)) {
	case ".yaml", ".yml":
		return os.WriteFile(out, []byte(templateConfig), 0o600)
	default:
		return v.WriteConfigAs(out)
	}
}

// backupExisting renames a config already at path out before it is replaced.
// Generating a template over a live configuration would otherwise discard it
// with no warning and no copy.
func backupExisting(out string) error {
	info, err := os.Lstat(out)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace %s: not a regular file", out)
	}
	backup := out + ".bak." + time.Now().UTC().Format("20060102T150405Z")
	if _, err := os.Lstat(backup); err == nil {
		return fmt.Errorf("refusing to replace %s: backup %s already exists", out, backup)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(out, backup); err != nil {
		return fmt.Errorf("failed to back up %s: %w", out, err)
	}
	mlog.S().Infof("existing config backed up to %s", backup)
	return nil
}
