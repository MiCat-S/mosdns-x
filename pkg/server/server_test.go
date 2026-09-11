package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type callbackCloser struct {
	close func()
}

func (c callbackCloser) Close() error { c.close(); return nil }

type blockingGracefulCloser struct{ closed atomic.Bool }

func (c *blockingGracefulCloser) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *blockingGracefulCloser) Close() error { c.closed.Store(true); return nil }

func TestShutdownDeadlineForceClosesTransport(t *testing.T) {
	s := NewServer(ServerOpts{})
	c := new(blockingGracefulCloser)
	s.trackCloser(c, true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); err == nil {
		t.Fatal("expected deadline error")
	}
	if !c.closed.Load() {
		t.Fatal("transport was not force closed")
	}
}

func TestShutdownDoesNotHoldServerMutexWhileClosing(t *testing.T) {
	s := NewServer(ServerOpts{})
	s.trackCloser(&callbackCloser{close: func() { _ = s.Closed() }}, true)
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked")
	}
}

func TestShutdownRejectsNewAndWaitsForActiveQuery(t *testing.T) {
	s := NewServer(ServerOpts{})
	if !s.beginQuery() {
		t.Fatal("initial query rejected")
	}
	var finished atomic.Bool
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(context.Background()); finished.Store(true) }()
	for !s.Closed() {
		time.Sleep(time.Millisecond)
	}
	if s.beginQuery() {
		t.Fatal("query accepted after shutdown")
	}
	select {
	case <-done:
		t.Fatal("shutdown returned before active query")
	case <-time.After(20 * time.Millisecond):
	}
	s.queryWG.Done()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	if !finished.Load() {
		t.Fatal("shutdown goroutine did not finish")
	}
}

func TestCloseInterruptsConcurrentGracefulHTTPShutdown(t *testing.T) {
	s := NewServer(ServerOpts{})
	entered := make(chan struct{})
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.beginQuery() {
			return
		}
		defer s.queryWG.Done()
		close(entered)
		<-r.Context().Done()
	})}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if !s.trackCloser(hs, true) {
		t.Fatal("failed to track HTTP server")
	}
	go hs.Serve(l)
	requestDone := make(chan struct{})
	go func() {
		_, _ = http.Get("http://" + l.Addr().String())
		close(requestDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- s.Shutdown(context.Background()) }()
	select {
	case <-shutdownDone:
		t.Fatal("graceful shutdown returned while handler was blocked")
	case <-time.After(20 * time.Millisecond):
	}
	s.Close()
	select {
	case err := <-shutdownDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt graceful shutdown")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("request did not terminate")
	}
}
