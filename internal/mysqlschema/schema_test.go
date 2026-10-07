package mysqlschema

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// The production DSN set readTimeout=5s; a table rebuild outlasted it and
// every start failed. Upgrades must not inherit those timeouts.
func TestUpgradeConfigDropsReadAndWriteTimeouts(t *testing.T) {
	cfg, err := mysqlDriver.ParseDSN("mosdns:secret@tcp(127.0.0.1:3306)/mosdns?charset=utf8mb4&timeout=5s&readTimeout=5s&writeTimeout=5s")
	if err != nil {
		t.Fatal(err)
	}
	upgrade := upgradeConfig(cfg)
	if upgrade.ReadTimeout != 0 || upgrade.WriteTimeout != 0 {
		t.Fatalf("upgrade timeouts read=%v write=%v", upgrade.ReadTimeout, upgrade.WriteTimeout)
	}
	if upgrade.Timeout != 5*time.Second || upgrade.DBName != "mosdns" || upgrade.Passwd != "secret" {
		t.Fatalf("upgrade config lost settings: %+v", upgrade)
	}
	if cfg.ReadTimeout != 5*time.Second || cfg.WriteTimeout != 5*time.Second {
		t.Fatal("the service's own config was changed")
	}
}

func TestContextOutlastsStartupTimeouts(t *testing.T) {
	ctx, cancel := Context(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 10*time.Minute {
		t.Fatalf("deadline in %v", time.Until(deadline))
	}
}

func TestExecLogsBeforeAndAfterTheChange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	core, logs := observer.New(zap.InfoLevel)
	statement := "ALTER TABLE mosdns_query_logs ADD COLUMN trace_json LONGTEXT NULL"
	mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	if err := Exec(context.Background(), conn, zap.New(core), "mosdns_query_logs", statement); err != nil {
		t.Fatal(err)
	}
	entries := logs.All()
	if len(entries) != 2 || entries[0].Level != zap.WarnLevel || entries[1].Message != "mysql schema upgraded" {
		t.Fatalf("logs = %+v", entries)
	}

	mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnError(errors.New("lock wait timeout"))
	if err := Exec(context.Background(), conn, nil, "mosdns_query_logs", statement); err == nil {
		t.Fatal("failed change reported success")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
