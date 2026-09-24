package coremain

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unreachableMySQL is a DSN no server listens on, the state MySQL is in when
// mosdns starts before it during boot.
const unreachableMySQL = "u:p@tcp(127.0.0.1:1)/mosdns?timeout=1s"

// A constructor's nil pointer returned through an interface is not a nil
// interface. Every failure path must hand back a true nil, or shutdown calls
// Close on a nil receiver.
func TestStoreOpenFailuresReturnNilInterfaces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	unwritable := filepath.Join(t.TempDir(), "missing", "\x00bad", "x.db")
	cases := []struct {
		name string
		open func() (any, error)
	}{
		{"control mysql", func() (any, error) {
			return openControlStore(ctx, &ControlConfig{Storage: StorageConfig{Driver: "mysql", MySQL: MySQLConfig{DSN: unreachableMySQL}}})
		}},
		{"control bbolt", func() (any, error) {
			return openControlStore(ctx, &ControlConfig{Database: ""})
		}},
		{"telemetry mysql", func() (any, error) {
			return openTelemetryStore(ctx, &ControlConfig{Storage: StorageConfig{Driver: "mysql", MySQL: MySQLConfig{DSN: unreachableMySQL}}})
		}},
		{"telemetry bbolt", func() (any, error) {
			return openTelemetryStore(ctx, &ControlConfig{StatsDatabase: unwritable})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, err := c.open()
			if err == nil {
				t.Fatal("expected an open failure")
			}
			if store != nil {
				t.Fatalf("failure returned a non-nil interface holding %T", store)
			}
		})
	}
}

// End to end: starting with MySQL down must fail with an error, not panic
// while shutting down. This is what happened on boot when mosdns started
// eight seconds before MySQL.
func TestStartupWithMySQLDownFailsWithoutPanic(t *testing.T) {
	cfg := validControlConfig()
	cfg.Control.Database = ""
	cfg.Control.StatsDatabase = ""
	cfg.Control.Storage = StorageConfig{Driver: "mysql", MySQL: MySQLConfig{DSN: unreachableMySQL}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		err = RunMosdnsContext(ctx, cfg)
	}()
	if err == nil {
		t.Fatal("startup succeeded with MySQL unreachable")
	}
	if strings.HasPrefix(err.Error(), "panic:") {
		t.Fatalf("startup panicked instead of failing: %v", err)
	}
	if !strings.Contains(err.Error(), "control database") {
		t.Fatalf("unexpected error: %v", err)
	}
}
