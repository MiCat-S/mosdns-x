// Package mysqlschema runs MySQL schema upgrades for the control and
// telemetry stores.
package mysqlschema

import (
	"context"
	"database/sql"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
)

// Timeout bounds one store's schema upgrade. Adding a column on MySQL 5.7
// rebuilds the table, which takes minutes on a large one; the service does
// not answer queries until the upgrade finishes.
const Timeout = 30 * time.Minute

// Open returns a single-connection pool for schema upgrades. It drops the
// DSN's read and write timeouts, which are meant for ordinary queries: an
// ALTER that rebuilds a table outlasts them, and the driver would give up on
// the connection while the server finishes the change, failing every start.
// The caller bounds the upgrade with its context instead.
func Open(cfg *mysqlDriver.Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", upgradeConfig(cfg).FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func upgradeConfig(cfg *mysqlDriver.Config) *mysqlDriver.Config {
	upgrade := cfg.Clone()
	upgrade.ReadTimeout = 0
	upgrade.WriteTimeout = 0
	return upgrade
}

// Context bounds a schema upgrade by Timeout, still ending early if parent is
// canceled, such as when the service is stopped.
func Context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, Timeout)
}

// Exec runs one schema change on table. It logs before starting, since a
// rebuild can take minutes and a restart in the middle only starts it over.
func Exec(ctx context.Context, conn *sql.Conn, logger *zap.Logger, table, statement string) error {
	if logger == nil {
		logger = zap.NewNop()
	}
	logger.Warn("upgrading mysql schema; on MySQL 5.7 this rebuilds the table and can take minutes on a large one; do not restart the service",
		zap.String("table", table), zap.String("statement", statement))
	started := time.Now()
	if _, err := conn.ExecContext(ctx, statement); err != nil {
		logger.Error("mysql schema upgrade failed", zap.String("table", table), zap.Duration("elapsed", time.Since(started)), zap.Error(err))
		return err
	}
	logger.Info("mysql schema upgraded", zap.String("table", table), zap.Duration("elapsed", time.Since(started)))
	return nil
}
