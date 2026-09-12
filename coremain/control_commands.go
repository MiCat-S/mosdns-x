package coremain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/telemetry"
)

const maxPasswordInput = 1026 // 1024-byte password plus one CRLF line ending.

const (
	controlStatusReady         = "ready"
	controlStatusDisabled      = "disabled"
	controlStatusUninitialized = "uninitialized"
	controlStatusStorageError  = "storage_error"

	controlStatusExitReady         = 0
	controlStatusExitDisabled      = 10
	controlStatusExitUninitialized = 11
	controlStatusExitStorageError  = 12
)

// ExitCodeError lets a command request a deliberate process exit status
// without changing the error handling of the other commands.
type ExitCodeError struct {
	code int
}

func (e *ExitCodeError) Error() string { return "command completed with a non-zero status" }

func (e *ExitCodeError) ExitCode() int { return e.code }

func init() { AddSubCmd(newControlCommand()) }

func newControlCommand() *cobra.Command {
	c := &cobra.Command{Use: "control", Short: "Manage the multi-user control database", SilenceUsage: true}
	c.AddCommand(newControlInitAdminCommand(), newControlStatusCommand(), newControlBackupCommand(), newControlRestoreCommand(), newControlMigrateMySQLCommand())
	return c
}

func newControlStatusCommand() *cobra.Command {
	var configFile string
	c := &cobra.Command{Use: "status --config CONFIG", Short: "Report whether control storage is ready for service startup", Args: cobra.NoArgs, SilenceErrors: true, RunE: func(cmd *cobra.Command, _ []string) error {
		status := inspectControlStatus(cmd.Context(), configFile)
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), status); err != nil {
			return err
		}
		switch status {
		case controlStatusReady:
			return nil
		case controlStatusDisabled:
			return &ExitCodeError{code: controlStatusExitDisabled}
		case controlStatusUninitialized:
			return &ExitCodeError{code: controlStatusExitUninitialized}
		default:
			return &ExitCodeError{code: controlStatusExitStorageError}
		}
	}}
	c.Flags().StringVarP(&configFile, "config", "c", "", "read control storage from a Mosdns config file")
	_ = c.MarkFlagRequired("config")
	return c
}

func inspectControlStatus(ctx context.Context, configFile string) string {
	cfg, fileUsed, err := loadConfig(configFile)
	if err == nil {
		err = mergeInclude(cfg, 0, []string{fileUsed})
	}
	if err != nil || cfg.Control == nil {
		if err == nil {
			return controlStatusDisabled
		}
		return controlStatusStorageError
	}

	var store control.Service
	switch effectiveControlDriver(cfg.Control) {
	case "bbolt":
		if err := requireExistingFile(cfg.Control.Database); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return controlStatusUninitialized
			}
			return controlStatusStorageError
		}
		store, err = control.Open(cfg.Control.Database, control.Options{})
	case "mysql":
		lifetime, timeout := mysqlDurations(cfg.Control.Storage.MySQL)
		store, err = control.OpenMySQL(control.MySQLOptions{
			DSN:              cfg.Control.Storage.MySQL.DSN,
			MaxOpenConns:     cfg.Control.Storage.MySQL.MaxOpenConns,
			MaxIdleConns:     cfg.Control.Storage.MySQL.MaxIdleConns,
			ConnMaxLifetime:  lifetime,
			OperationTimeout: timeout,
		})
	default:
		return controlStatusStorageError
	}
	if err != nil {
		return controlStatusStorageError
	}
	ready, listErr := hasEnabledAdministrator(ctx, store)
	closeErr := store.Close()
	if listErr != nil || closeErr != nil {
		return controlStatusStorageError
	}
	if !ready {
		return controlStatusUninitialized
	}
	return controlStatusReady
}

func newControlInitAdminCommand() *cobra.Command {
	var configFile, database, mysqlDSN, username string
	c := &cobra.Command{Use: "init-admin", Short: "Initialize the first administrator using a password from stdin", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(username) == "" {
			return errors.New("--username is required")
		}
		if configFile != "" {
			if database != "" || mysqlDSN != "" {
				return errors.New("--config cannot be combined with --database or --mysql-dsn")
			}
			cfg, fileUsed, err := loadConfig(configFile)
			if err != nil {
				return err
			}
			if err := mergeInclude(cfg, 0, []string{fileUsed}); err != nil {
				return err
			}
			if cfg.Control == nil {
				return errors.New("config does not enable control mode")
			}
			switch effectiveControlDriver(cfg.Control) {
			case "mysql":
				mysqlDSN = cfg.Control.Storage.MySQL.DSN
			case "bbolt":
				database = cfg.Control.Database
			default:
				return fmt.Errorf("unsupported control storage driver %q", cfg.Control.Storage.Driver)
			}
		}
		if (database == "") == (mysqlDSN == "") {
			return errors.New("exactly one of --config, --database, or --mysql-dsn must select control storage")
		}
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		password, err := readPassword(cmd.InOrStdin())
		if err != nil {
			return err
		}
		var s control.Service
		if mysqlDSN != "" {
			s, err = control.OpenMySQL(control.MySQLOptions{DSN: mysqlDSN})
		} else {
			if err = ensureParent(database); err != nil {
				return fmt.Errorf("prepare database directory: %w", err)
			}
			s, err = control.Open(database, control.Options{})
		}
		if err != nil {
			return fmt.Errorf("open control storage: %w", err)
		}
		_, opErr := s.InitializeAdmin(cmd.Context(), control.UserSpec{Username: username, Password: password, Role: control.RoleAdmin, Enabled: true, Period: control.PeriodDaily, Timezone: "UTC", Limit: 1_000_000, QPS: 1_000, Burst: 100, MaxCredentials: 10})
		closeErr := s.Close()
		if err := operationAndCloseError("initialize administrator", opErr, closeErr); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "管理员 %q 已初始化。\n", strings.TrimSpace(username))
		return err
	}}
	c.Flags().StringVarP(&configFile, "config", "c", "", "read control storage from a Mosdns config file")
	c.Flags().StringVar(&database, "database", "", "control database path")
	c.Flags().StringVar(&mysqlDSN, "mysql-dsn", "", "MySQL DSN for the control storage")
	c.Flags().StringVar(&username, "username", "", "initial administrator username")
	return c
}

func newControlMigrateMySQLCommand() *cobra.Command {
	var configFile, database, statsDatabase, mysqlDSN, telemetryMySQLDSN, component string
	var dryRun bool
	c := &cobra.Command{Use: "migrate-mysql", Short: "Migrate offline bbolt control and telemetry data to empty MySQL tables", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		component = strings.ToLower(strings.TrimSpace(component))
		if component != "all" && component != "control" && component != "telemetry" {
			return errors.New("--component must be all, control, or telemetry")
		}
		migrateControl := component == "all" || component == "control"
		migrateTelemetry := component == "all" || component == "telemetry"
		if migrateControl && database == "" {
			return errors.New("--database is required for the control component")
		}
		if migrateTelemetry && statsDatabase == "" {
			return errors.New("--stats-database is required for the telemetry component")
		}
		if configFile != "" {
			if mysqlDSN != "" || telemetryMySQLDSN != "" {
				return errors.New("--config cannot be combined with MySQL DSN flags")
			}
			cfg, _, err := loadConfig(configFile)
			if err != nil {
				return err
			}
			if cfg.Control == nil {
				return errors.New("config does not enable control mode")
			}
			if migrateControl {
				if effectiveControlDriver(cfg.Control) != "mysql" {
					return errors.New("config control storage is not mysql")
				}
				mysqlDSN = cfg.Control.Storage.MySQL.DSN
			}
			if migrateTelemetry {
				if effectiveTelemetryDriver(cfg.Control) != "mysql" {
					return errors.New("config telemetry storage is not mysql")
				}
				telemetryMySQLDSN = effectiveTelemetryMySQL(cfg.Control).DSN
			}
		}
		if telemetryMySQLDSN == "" {
			telemetryMySQLDSN = mysqlDSN
		}
		if !dryRun && migrateControl && strings.TrimSpace(mysqlDSN) == "" {
			return errors.New("--mysql-dsn or a MySQL --config is required for the control component")
		}
		if !dryRun && migrateTelemetry && strings.TrimSpace(telemetryMySQLDSN) == "" {
			return errors.New("--telemetry-mysql-dsn, --mysql-dsn, or a MySQL --config is required for the telemetry component")
		}
		var controlReport control.MySQLMigrationReport
		var telemetryReport telemetry.MySQLMigrationReport
		if migrateControl {
			if err := requireExistingFile(database); err != nil {
				return fmt.Errorf("control migration source: %w", err)
			}
			report, err := control.InspectBoltForMySQL(cmd.Context(), database)
			if err != nil {
				return fmt.Errorf("inspect control migration source: %w", err)
			}
			controlReport = report
		}
		if migrateTelemetry {
			if err := requireExistingFile(statsDatabase); err != nil {
				return fmt.Errorf("telemetry migration source: %w", err)
			}
			report, err := telemetry.InspectBoltForMySQL(cmd.Context(), statsDatabase)
			if err != nil {
				return fmt.Errorf("inspect telemetry migration source: %w", err)
			}
			telemetryReport = report
		}
		if dryRun {
			if migrateControl {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "控制数据：用户 %d，策略设置 %d，策略规则 %d，公共列表 %d，列表覆盖 %d，会话 %d，凭证 %d，用量 %d，审计 %d。\n", controlReport.Users, controlReport.PolicySettings, controlReport.PolicyRules, controlReport.PublicLists, controlReport.ListOverrides, controlReport.Sessions, controlReport.Credentials, controlReport.Usage, controlReport.Audit); err != nil {
					return err
				}
			}
			if migrateTelemetry {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "统计数据：分钟聚合 %d，上游聚合 %d，查询明细 %d。\n", telemetryReport.Minutes, telemetryReport.Upstreams, telemetryReport.Queries); err != nil {
					return err
				}
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "检查完成；未连接或修改 MySQL。")
			return err
		}
		if migrateControl {
			if err := runControlMySQLMigration(cmd, database, mysqlDSN); err != nil {
				return err
			}
		}
		if migrateTelemetry {
			if err := runTelemetryMySQLMigration(cmd, statsDatabase, telemetryMySQLDSN); err != nil {
				return err
			}
		}
		return nil
	}}
	c.Flags().StringVarP(&configFile, "config", "c", "", "read destination MySQL DSNs from a Mosdns config file")
	c.Flags().StringVar(&database, "database", "", "offline bbolt control database path")
	c.Flags().StringVar(&statsDatabase, "stats-database", "", "offline bbolt telemetry database path")
	c.Flags().StringVar(&mysqlDSN, "mysql-dsn", "", "destination control MySQL DSN")
	c.Flags().StringVar(&telemetryMySQLDSN, "telemetry-mysql-dsn", "", "destination telemetry MySQL DSN; defaults to --mysql-dsn")
	c.Flags().StringVar(&component, "component", "all", "component to migrate: all, control, or telemetry")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "validate and count source records without connecting to MySQL")
	return c
}

func runControlMySQLMigration(cmd *cobra.Command, source, dsn string) error {
	destination, err := control.OpenMySQL(control.MySQLOptions{DSN: dsn, OperationTimeout: 30 * time.Minute})
	if err != nil {
		return fmt.Errorf("open MySQL control destination: %w", err)
	}
	report, opErr := control.MigrateBoltToMySQL(cmd.Context(), source, destination)
	closeErr := destination.Close()
	if err := operationAndCloseError("migrate control data", opErr, closeErr); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "控制数据迁移完成：用户 %d，策略设置 %d，策略规则 %d，公共列表 %d，列表覆盖 %d，会话 %d，凭证 %d，用量 %d，审计 %d。\n", report.Users, report.PolicySettings, report.PolicyRules, report.PublicLists, report.ListOverrides, report.Sessions, report.Credentials, report.Usage, report.Audit)
	return err
}

func runTelemetryMySQLMigration(cmd *cobra.Command, source, dsn string) error {
	destination, err := telemetry.OpenMySQL(telemetry.MySQLOptions{DSN: dsn, OperationTimeout: 30 * time.Minute, QueryLogEnabled: true})
	if err != nil {
		return fmt.Errorf("open MySQL telemetry destination: %w", err)
	}
	report, opErr := telemetry.MigrateBoltToMySQL(cmd.Context(), source, destination)
	closeErr := destination.Close()
	if err := operationAndCloseError("migrate telemetry data", opErr, closeErr); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "统计数据迁移完成：分钟聚合 %d，上游聚合 %d，查询明细 %d。\n", report.Minutes, report.Upstreams, report.Queries)
	return err
}

func newControlBackupCommand() *cobra.Command {
	var database, output string
	c := &cobra.Command{Use: "backup", Short: "Create a consistent control database backup", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if database == "" || output == "" {
			return errors.New("--database and --output are required")
		}
		if err := requireExistingFile(database); err != nil {
			return fmt.Errorf("source database: %w", err)
		}
		if err := control.ValidateBackup(cmd.Context(), database); err != nil {
			return fmt.Errorf("validate offline source database: %w", err)
		}
		if err := ensureParent(output); err != nil {
			return fmt.Errorf("prepare output directory: %w", err)
		}
		s, err := control.Open(database, control.Options{})
		if err != nil {
			return fmt.Errorf("open source database (CLI backup requires an offline, unlocked database): %w", err)
		}
		opErr := s.Backup(cmd.Context(), output)
		closeErr := s.Close()
		if err := operationAndCloseError("backup control database", opErr, closeErr); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "备份已写入 %s。\n", output)
		return err
	}}
	c.Flags().StringVar(&database, "database", "", "source control database path")
	c.Flags().StringVar(&output, "output", "", "new backup path")
	return c
}

func newControlRestoreCommand() *cobra.Command {
	var input, database string
	c := &cobra.Command{Use: "restore", Short: "Restore an offline backup to a new control database", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if input == "" || database == "" {
			return errors.New("--input and --database are required")
		}
		if err := requireExistingFile(input); err != nil {
			return fmt.Errorf("backup input: %w", err)
		}
		if err := ensureParent(database); err != nil {
			return fmt.Errorf("prepare database directory: %w", err)
		}
		if err := control.Restore(cmd.Context(), input, database); err != nil {
			return fmt.Errorf("restore control database (destination must be new and backup must be offline): %w", err)
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "备份已恢复到 %s。\n", database)
		return err
	}}
	c.Flags().StringVar(&input, "input", "", "offline backup path")
	c.Flags().StringVar(&database, "database", "", "new destination database path")
	return c
}

func readPassword(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPasswordInput+1))
	if err != nil {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	if len(data) > maxPasswordInput {
		return "", errors.New("password from stdin exceeds 1024 bytes")
	}
	text := string(data)
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(text, "\n")
		text = strings.TrimSuffix(text, "\r")
	}
	if strings.ContainsAny(text, "\r\n") {
		return "", errors.New("password input must contain exactly one line")
	}
	if len(text) < 12 || len(text) > 1024 {
		return "", errors.New("password must contain 12 to 1024 bytes")
	}
	return text, nil
}
func ensureParent(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}
	parent := filepath.Dir(path)
	return os.MkdirAll(parent, 0o700)
}
func requireExistingFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}

func operationAndCloseError(operation string, operationErr, closeErr error) error {
	if operationErr == nil && closeErr == nil {
		return nil
	}
	var errs []error
	if operationErr != nil {
		errs = append(errs, fmt.Errorf("%s: %w", operation, operationErr))
	}
	if closeErr != nil {
		errs = append(errs, fmt.Errorf("close storage: %w", closeErr))
	}
	return errors.Join(errs...)
}
