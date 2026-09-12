package runtimeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func storedConfig(records int) Config {
	return Config{Version: Version, QueryLog: true, Telemetry: Telemetry{AggregateRetentionDays: 7, QueryRetentionHours: 24, MaxQueryRecords: records}}
}

func TestStoreAtomicWriteRevisionAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed.yaml")
	store, err := NewStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.Write("", storedConfig(1000))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("managed config mode = %o, want 600", got)
	}
	if _, err := store.Write("wrong", storedConfig(1001)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("Write error = %v, want revision conflict", err)
	}
	read, readRevision, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if readRevision != revision || read.Telemetry.MaxQueryRecords != 1000 {
		t.Fatalf("Read = (%+v, %s), want initial revision", read, readRevision)
	}

	firstRevision := revision
	for records := 1001; records <= 1012; records++ {
		revision, err = store.Write(revision, storedConfig(records))
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 10 {
		t.Fatalf("history length = %d, want 10", len(history))
	}
	if _, err := store.LoadRevision(firstRevision); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old pruned revision error = %v, want not exist", err)
	}
	restored, err := store.LoadRevision(history[0].Revision)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Telemetry.MaxQueryRecords < 1000 || restored.Telemetry.MaxQueryRecords > 1011 {
		t.Fatalf("unexpected restored config: %+v", restored)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) && entry.Name() != filepath.Base(path)+".history" {
			t.Fatalf("temporary file was not removed: %s", entry.Name())
		}
	}
}

func TestStoreRestoreExactRevisionAndMissingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed.yaml")
	store, err := NewStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Write("", storedConfig(1000))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Write(first, storedConfig(2000))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(second, first); err != nil {
		t.Fatal(err)
	}
	config, revision, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if revision != first || config.Telemetry.MaxQueryRecords != 1000 {
		t.Fatalf("restored = (%s, %+v), want first revision", revision, config)
	}
	if err := store.Restore(first, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed file still exists after restoring missing state: %v", err)
	}
}
