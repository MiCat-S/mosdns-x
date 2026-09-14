package telemetry

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

func TestOpenMySQLContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenMySQLContext(ctx, MySQLOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled startup=%v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	const network = "telemetry-security-test"
	started := make(chan struct{})
	mysqlDriver.RegisterDialContext(network, func(ctx context.Context, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer mysqlDriver.DeregisterDialContext(network)
	done := make(chan error, 1)
	go func() {
		store, err := OpenMySQLContext(ctx, MySQLOptions{DSN: "test:test@" + network + "(local)/mosdns"})
		if store != nil {
			_ = store.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("startup ended before dialing: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("startup did not dial")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled dial=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startup ignored cancellation")
	}
}
