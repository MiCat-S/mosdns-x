package coremain

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pmkol/mosdns-x/internal/control"
)

const maxPasswordInput = 1026 // 1024-byte password plus one CRLF line ending.

func init() { AddSubCmd(newControlCommand()) }

func newControlCommand() *cobra.Command {
	c := &cobra.Command{Use: "control", Short: "Manage the multi-user control database", SilenceUsage: true}
	c.AddCommand(newControlInitAdminCommand(), newControlBackupCommand(), newControlRestoreCommand())
	return c
}

func newControlInitAdminCommand() *cobra.Command {
	var database, username string
	c := &cobra.Command{Use: "init-admin", Short: "Initialize the first administrator using a password from stdin", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if database == "" || strings.TrimSpace(username) == "" {
			return errors.New("--database and --username are required")
		}
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		password, err := readPassword(cmd.InOrStdin())
		if err != nil {
			return err
		}
		if err = ensureParent(database); err != nil {
			return fmt.Errorf("prepare database directory: %w", err)
		}
		s, err := control.Open(database, control.Options{})
		if err != nil {
			return fmt.Errorf("open control database: %w", err)
		}
		_, opErr := s.InitializeAdmin(cmd.Context(), control.UserSpec{Username: username, Password: password, Role: control.RoleAdmin, Enabled: true, Period: control.PeriodDaily, Timezone: "UTC", Limit: 1_000_000, QPS: 1_000, Burst: 100, MaxCredentials: 10})
		closeErr := s.Close()
		if err := operationAndCloseError("initialize administrator", opErr, closeErr); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "管理员 %q 已初始化。\n", strings.TrimSpace(username))
		return err
	}}
	c.Flags().StringVar(&database, "database", "", "control database path")
	c.Flags().StringVar(&username, "username", "", "initial administrator username")
	return c
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
		errs = append(errs, fmt.Errorf("close control database: %w", closeErr))
	}
	return errors.Join(errs...)
}
