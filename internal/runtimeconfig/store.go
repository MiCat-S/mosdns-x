package runtimeconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var ErrRevisionConflict = errors.New("managed config revision conflict")

type Revision struct {
	Revision  string    `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct {
	path         string
	historyPath  string
	historyLimit int
	mu           sync.Mutex
}

func NewStore(path string, historyLimit int) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("managed config path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve managed config path: %w", err)
	}
	if historyLimit <= 0 {
		historyLimit = 10
	}
	return &Store{
		path:         abs,
		historyPath:  abs + ".history",
		historyLimit: historyLimit,
	}, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Read() (Config, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(s.path)
}

func (s *Store) readLocked(path string) (Config, string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, "", err
	}
	config, err := decodeConfig(contents)
	if err != nil {
		return Config{}, "", err
	}
	return config, revisionOf(contents), nil
}

func decodeConfig(contents []byte) (Config, error) {
	var config Config
	decoder := yaml.NewDecoder(strings.NewReader(string(contents)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode managed config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("managed config contains multiple documents")
		}
		return Config{}, fmt.Errorf("decode managed config: %w", err)
	}
	if err := Validate(config); err != nil {
		return Config{}, fmt.Errorf("validate managed config: %w", err)
	}
	return config, nil
}

func encodeConfig(config Config) ([]byte, error) {
	if err := Validate(config); err != nil {
		return nil, err
	}
	contents, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	return contents, nil
}

func revisionOf(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

// Write atomically replaces the managed file if expectedRevision still
// matches. The replaced version is archived before the rename.
func (s *Store) Write(expectedRevision string, config Config) (string, error) {
	contents, err := encodeConfig(config)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, currentRevision, readErr := s.readRawLocked()
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	if currentRevision != expectedRevision {
		return "", ErrRevisionConflict
	}
	newRevision := revisionOf(contents)
	if newRevision == currentRevision {
		return currentRevision, nil
	}
	if len(current) > 0 {
		if err := s.archiveLocked(currentRevision, current); err != nil {
			return "", err
		}
	}
	if err := s.pruneHistoryLocked(currentRevision); err != nil {
		return "", err
	}
	committed, err := atomicWrite(s.path, contents)
	if err != nil {
		if committed {
			// The replacement is already visible. Return its revision so the
			// caller can activate the matching runtime instead of diverging
			// from the file that a restart would load.
			return newRevision, err
		}
		return "", err
	}
	return newRevision, nil
}

// Restore replaces the current managed file with an exact archived revision.
// An empty target revision restores the state where no managed file existed.
// The current revision guard prevents a failed runtime activation from
// overwriting a concurrent external edit.
func (s *Store) Restore(currentRevision, targetRevision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, actualRevision, err := s.readRawLocked()
	if err != nil {
		return err
	}
	if actualRevision != currentRevision {
		return ErrRevisionConflict
	}
	if targetRevision == "" {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(filepath.Dir(s.path))
	}
	if len(targetRevision) != sha256.Size*2 {
		return errors.New("invalid managed config revision")
	}
	if _, err := hex.DecodeString(targetRevision); err != nil {
		return errors.New("invalid managed config revision")
	}
	contents, err := os.ReadFile(filepath.Join(s.historyPath, targetRevision+".yaml"))
	if err != nil {
		return err
	}
	if _, err := decodeConfig(contents); err != nil {
		return err
	}
	if revisionOf(contents) != targetRevision {
		return errors.New("managed config history checksum mismatch")
	}
	committed, err := atomicWrite(s.path, contents)
	if committed {
		return nil
	}
	return err
}

func (s *Store) readRawLocked() ([]byte, string, error) {
	contents, err := os.ReadFile(s.path)
	if err != nil {
		return nil, "", err
	}
	if _, err := decodeConfig(contents); err != nil {
		return nil, "", err
	}
	return contents, revisionOf(contents), nil
}

func (s *Store) archiveLocked(revision string, contents []byte) error {
	if revision == "" {
		return nil
	}
	if err := os.MkdirAll(s.historyPath, 0o700); err != nil {
		return fmt.Errorf("create managed config history: %w", err)
	}
	path := filepath.Join(s.historyPath, revision+".yaml")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	committed, err := atomicWrite(path, contents)
	if committed {
		return nil
	}
	return err
}

func (s *Store) History() ([]Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.historyPath)
	if errors.Is(err, os.ErrNotExist) {
		return []Revision{}, nil
	}
	if err != nil {
		return nil, err
	}
	revisions := make([]Revision, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		revision := strings.TrimSuffix(entry.Name(), ".yaml")
		if len(revision) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(revision); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, Revision{Revision: revision, CreatedAt: info.ModTime().UTC()})
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].CreatedAt.After(revisions[j].CreatedAt) })
	if len(revisions) > s.historyLimit {
		revisions = revisions[:s.historyLimit]
	}
	return revisions, nil
}

func (s *Store) LoadRevision(revision string) (Config, error) {
	if len(revision) != sha256.Size*2 {
		return Config{}, errors.New("invalid managed config revision")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return Config{}, errors.New("invalid managed config revision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config, actual, err := s.readLocked(filepath.Join(s.historyPath, revision+".yaml"))
	if err != nil {
		return Config{}, err
	}
	if actual != revision {
		return Config{}, errors.New("managed config history checksum mismatch")
	}
	return config, nil
}

func (s *Store) pruneHistoryLocked(protectedRevision string) error {
	entries, err := os.ReadDir(s.historyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type historyFile struct {
		path    string
		modTime time.Time
	}
	files := make([]historyFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, historyFile{path: filepath.Join(s.historyPath, entry.Name()), modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	if protectedRevision != "" {
		protectedPath := filepath.Join(s.historyPath, protectedRevision+".yaml")
		for i := range files {
			if files[i].path == protectedPath {
				files[0], files[i] = files[i], files[0]
				break
			}
		}
	}
	for _, file := range files[safeMin(len(files), s.historyLimit):] {
		if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func atomicWrite(path string, contents []byte) (committed bool, retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, err)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return false, err
	}
	if _, err := temporary.Write(contents); err != nil {
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return false, err
	}
	committed = true
	return true, syncDirectory(dir)
}

func syncDirectory(dir string) error {
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func safeMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
