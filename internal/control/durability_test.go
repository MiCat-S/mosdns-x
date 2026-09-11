package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const durabilityChildEnv = "MOSDNS_CONTROL_DURABILITY_CHILD"

type durabilityMarker struct {
	UserID string `json:"user_id"`
	Token  string `json:"token"`
}

func TestCrashDurability(t *testing.T) {
	if os.Getenv(durabilityChildEnv) == "1" {
		if err := runDurabilityChild(os.Getenv("MOSDNS_CONTROL_DURABILITY_DB")); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	dbPath := filepath.Join(t.TempDir(), "control.db")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashDurability$", "-test.count=1")
	cmd.Env = append(os.Environ(), durabilityChildEnv+"=1", "MOSDNS_CONTROL_DURABILITY_DB="+dbPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	type scanResult struct {
		line []byte
		err  error
	}
	scanned := make(chan scanResult, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			scanned <- scanResult{err: scanner.Err()}
			return
		}
		scanned <- scanResult{line: append([]byte(nil), scanner.Bytes()...)}
	}()
	var result scanResult
	select {
	case result = <-scanned:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("timed out waiting for committed marker")
	}
	if result.err != nil || len(result.line) == 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child marker error=%v stderr=%s", result.err, stderr.String())
	}
	var marker durabilityMarker
	if err = json.Unmarshal(result.line, &marker); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("decode marker: %v; stderr=%s", err, stderr.String())
	}
	if marker.UserID == "" || marker.Token == "" {
		t.Fatal("empty marker")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("child exited cleanly instead of being killed")
	}
	if ctx.Err() != nil {
		t.Fatalf("child cleanup exceeded timeout: %v", ctx.Err())
	}
	clock := &fakeClock{t: durabilityTime()}
	s, err := Open(dbPath, Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	quota, err := s.CurrentQuota(context.Background(), marker.UserID)
	if err != nil || quota.Used != 4 || quota.Remaining != 1 {
		t.Fatalf("reopened quota=%+v err=%v", quota, err)
	}
	identity, err := s.AuthenticateCredential(context.Background(), marker.Token)
	if err != nil {
		t.Fatalf("credential after crash: %v", err)
	}
	from := durabilityTime().Truncate(time.Minute)
	usage, err := s.Usage(context.Background(), marker.UserID, from, from.Add(time.Minute), Page{})
	if err != nil || len(usage.Items) != 1 || usage.Items[0].Count != 4 {
		t.Fatalf("usage after crash=%+v err=%v", usage, err)
	}
	if err = s.Admit(context.Background(), identity); err != nil {
		t.Fatalf("last admission: %v", err)
	}
	if err = s.Admit(context.Background(), identity); err != ErrQuotaExceeded {
		t.Fatalf("duplicate quota available: %v", err)
	}
}

func durabilityTime() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }

func runDurabilityChild(path string) error {
	if path == "" {
		return fmt.Errorf("empty child database path")
	}
	clock := &fakeClock{t: durabilityTime()}
	s, err := Open(path, Options{Clock: clock})
	if err != nil {
		return err
	}
	ctx := context.Background()
	admin, err := s.InitializeAdmin(ctx, adminSpec())
	if err != nil {
		return err
	}
	spec := userSpec("durable", 5, 100, 0)
	u, err := s.CreateUser(ctx, admin.ID, spec)
	if err != nil {
		return err
	}
	issued, err := s.CreateCredential(ctx, u.ID, u.ID, "device", time.Time{})
	if err != nil {
		return err
	}
	identity, err := s.AuthenticateCredential(ctx, issued.Token)
	if err != nil {
		return err
	}
	for range 4 {
		if err = s.Admit(ctx, identity); err != nil {
			return err
		}
	}
	if err = json.NewEncoder(os.Stdout).Encode(durabilityMarker{UserID: u.ID, Token: issued.Token}); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, os.Stdin)
	return err
}
