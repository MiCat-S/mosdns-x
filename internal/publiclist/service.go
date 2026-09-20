package publiclist

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/pmkol/mosdns-x/internal/control"
)

const defaultMaxSize int64 = 8 << 20
const defaultMaxEntries = 500_000
const validationTTL = 15 * time.Minute
const maxValidationCandidates = 32
const maxInvalidEntryDetails = 20
const maxValidationSamples = 8
const maxLinearRules = 10_000

const snapshotMagic = "MOSDNS-X-PUBLIC-LIST\x00"

type Store interface {
	CreatePublicList(context.Context, string, control.PublicListSpec) (control.PublicList, error)
	UpdatePublicList(context.Context, string, string, control.PublicListPatch) (control.PublicList, error)
	CommitPublicListSnapshot(context.Context, string, string, time.Time, control.PublicListSpec, control.PublicListRefreshResult) (control.PublicList, error)
	DeletePublicList(context.Context, string, string) error
	GetPublicList(context.Context, string) (control.PublicList, error)
	ListPublicLists(context.Context, control.Page) (control.PageResult[control.PublicList], error)
	ListUserPublicLists(context.Context, string, control.Page) (control.PageResult[control.UserPublicList], error)
	RecordPublicListRefresh(context.Context, string, control.PublicListRefreshResult) error
}

type Options struct {
	Directory   string
	Timeout     time.Duration
	MaxSize     int64
	MaxEntries  int
	Concurrency int
}

type Service struct {
	store      Store
	dir        string
	client     *http.Client
	maxSize    int64
	maxEntries int
	sem        chan struct{}

	mu                       sync.RWMutex
	compiled                 map[string]*compiledList
	selections               map[string]selection
	attempts                 map[string]time.Time
	candidates               map[string]candidate
	listLocks                map[string]*sync.Mutex
	selectionGeneration      uint64
	userSelectionGenerations map[string]uint64
}

type candidate struct {
	path              string
	validation        control.PublicListValidation
	claimed           bool
	listID            string
	expectedUpdatedAt time.Time
}

var (
	ErrValidationTokenInvalid = errors.New("public list validation token invalid")
	ErrValidationTokenExpired = errors.New("public list validation token expired")
	ErrInvalidSource          = errors.New("public list source invalid")
	ErrContentTooLarge        = errors.New("public list content too large")
	ErrContentRejected        = errors.New("public list content rejected")
	ErrNotPublished           = errors.New("public list is not published")
	ErrTooManyPending         = errors.New("too many pending public list validations")
	ErrSnapshotCleanup        = errors.New("public list snapshot cleanup pending")
	errInvalidSnapshot        = errors.New("invalid public list snapshot")
)

type selection struct {
	expires time.Time
	lists   []control.PublicList
}
type compiledRule struct {
	kind  string
	value string
	re    *regexp.Regexp
}
type compiledList struct {
	rules    []compiledRule
	exact    map[string]struct{}
	suffix   map[string]struct{}
	patterns []compiledRule
}

func New(store Store, opts Options) (*Service, error) {
	if store == nil || opts.Directory == "" {
		return nil, errors.New("public list store and directory are required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.Timeout <= 0 || opts.Timeout > time.Minute {
		return nil, errors.New("invalid public list timeout")
	}
	if opts.MaxSize == 0 {
		opts.MaxSize = defaultMaxSize
	}
	if opts.MaxSize < 1024 || opts.MaxSize > 64<<20 {
		return nil, errors.New("invalid public list size limit")
	}
	if opts.MaxEntries == 0 {
		opts.MaxEntries = defaultMaxEntries
	}
	if opts.MaxEntries < 1 || opts.MaxEntries > 5_000_000 {
		return nil, errors.New("invalid public list entry limit")
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = 4
	}
	if opts.Concurrency < 1 || opts.Concurrency > 32 {
		return nil, errors.New("invalid public list concurrency")
	}
	if err := os.MkdirAll(opts.Directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(opts.Directory, 0o700); err != nil {
		return nil, err
	}
	if err := recoverSnapshotDirectory(store, opts.Directory); err != nil {
		return nil, err
	}
	client := secureHTTPClient(opts.Timeout)
	service := &Service{
		store: store, dir: opts.Directory, client: client, maxSize: opts.MaxSize, maxEntries: opts.MaxEntries,
		sem: make(chan struct{}, opts.Concurrency), compiled: make(map[string]*compiledList), selections: make(map[string]selection),
		attempts: make(map[string]time.Time), candidates: make(map[string]candidate), listLocks: make(map[string]*sync.Mutex),
		userSelectionGenerations: make(map[string]uint64),
	}
	if err := service.reconcilePublishedSnapshots(context.Background()); err != nil {
		return nil, err
	}
	return service, nil
}

func recoverSnapshotDirectory(store Store, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		switch {
		case strings.HasPrefix(entry.Name(), ".candidate-"), strings.HasPrefix(entry.Name(), ".public-list-"):
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove stale public list temporary file: %w", err)
			}
		case strings.HasPrefix(entry.Name(), ".deleted-public-list-"):
			originalName := strings.TrimPrefix(entry.Name(), ".deleted-public-list-")
			id, ok := snapshotListID(originalName)
			if !ok {
				continue
			}
			_, getErr := store.GetPublicList(context.Background(), id)
			original := filepath.Join(dir, originalName)
			if getErr == nil {
				if _, statErr := os.Stat(original); statErr == nil {
					if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
						return fmt.Errorf("remove recovered public list tombstone: %w", err)
					}
				} else if errors.Is(statErr, os.ErrNotExist) {
					if err := os.Rename(path, original); err != nil {
						return fmt.Errorf("restore public list snapshot: %w", err)
					}
				} else {
					return statErr
				}
			} else if errors.Is(getErr, control.ErrNotFound) {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove deleted public list tombstone: %w", err)
				}
			} else {
				return getErr
			}
		case strings.HasSuffix(entry.Name(), ".list"):
			id, ok := snapshotListID(entry.Name())
			if !ok {
				continue
			}
			list, getErr := store.GetPublicList(context.Background(), id)
			if errors.Is(getErr, control.ErrNotFound) {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove orphaned public list snapshot: %w", err)
				}
				continue
			}
			if getErr != nil {
				return getErr
			}
			if list.SnapshotStatus == control.PublicListSnapshotMissing {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove uncommitted public list snapshot: %w", err)
				}
				continue
			}
			activeName := id + ".list"
			if list.SnapshotSHA256 != "" {
				activeName = id + "-" + string(list.Format) + "-" + strings.ToLower(list.SnapshotSHA256) + ".list"
			}
			if entry.Name() != activeName {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove superseded public list snapshot: %w", err)
				}
			}
		}
	}
	return nil
}

func snapshotListID(name string) (string, bool) {
	if !strings.HasSuffix(name, ".list") {
		return "", false
	}
	stem := strings.TrimSuffix(name, ".list")
	if len(stem) > 65 && stem[len(stem)-65] == '-' {
		if digest, err := hex.DecodeString(stem[len(stem)-64:]); err == nil && len(digest) == sha256.Size {
			stem = stem[:len(stem)-65]
			for _, format := range []control.PublicListFormat{control.PublicListFormatMosDNS, control.PublicListFormatHosts} {
				if strings.HasSuffix(stem, "-"+string(format)) {
					stem = strings.TrimSuffix(stem, "-"+string(format))
					break
				}
			}
		}
	}
	return stem, validListID(stem)
}

func (s *Service) reconcilePublishedSnapshots(ctx context.Context) error {
	cursor := ""
	for {
		page, err := s.store.ListPublicLists(ctx, control.Page{Limit: 1000, Cursor: cursor})
		if err != nil {
			return err
		}
		for _, list := range page.Items {
			if !list.Published || (list.SnapshotStatus != control.PublicListSnapshotCurrent && list.SnapshotStatus != control.PublicListSnapshotStale) {
				continue
			}
			if err := s.verifySnapshot(list); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errInvalidSnapshot) {
				return fmt.Errorf("verify public list snapshot %s: %w", list.ID, err)
			} else if recordErr := s.store.RecordPublicListRefresh(ctx, list.ID, control.PublicListRefreshResult{
				Status: control.PublicListRefreshError, RefreshedAt: time.Now().UTC(), Error: err.Error(), SnapshotMissing: true,
			}); recordErr != nil {
				return errors.Join(err, recordErr)
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

func (s *Service) verifySnapshot(list control.PublicList) error {
	data, err := os.ReadFile(s.activeSnapshotPath(list))
	if err != nil {
		return err
	}
	format, contents, err := decodeSnapshot(data, list.Format)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidSnapshot, err)
	}
	if format != list.Format {
		return fmt.Errorf("%w: format mismatch", errInvalidSnapshot)
	}
	if list.SnapshotSHA256 != "" {
		sum := sha256.Sum256(contents)
		if !strings.EqualFold(list.SnapshotSHA256, hex.EncodeToString(sum[:])) {
			return fmt.Errorf("%w: checksum mismatch", errInvalidSnapshot)
		}
	}
	compiled, _, err := analyze(contents, format, s.maxEntries)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidSnapshot, err)
	}
	if len(compiled.rules) == 0 {
		return fmt.Errorf("%w: contains no valid entries", errInvalidSnapshot)
	}
	return nil
}

func secureHTTPClient(timeout time.Duration) *http.Client {
	dialer := new(net.Dialer)
	resolver := net.DefaultResolver
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		// Try every public address rather than only the first. A host that
		// advertises an unreachable address first, which an IPv6 record on an
		// IPv4-only egress commonly does, must not fail the whole refresh.
		var dialErr error
		public := false
		for _, ip := range ips {
			ip = ip.Unmap()
			if !publicAddress(ip) {
				continue
			}
			public = true
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = errors.Join(dialErr, err)
			if ctx.Err() != nil {
				return nil, dialErr
			}
		}
		if !public {
			return nil, fmt.Errorf("%w: host has no public address", ErrInvalidSource)
		}
		return nil, dialErr
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: checkRedirect}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if err := validateFetchURL(req.URL.Scheme, req.URL.Host, req.URL.User != nil); err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(req.URL.Hostname()); err == nil && !publicAddress(ip.Unmap()) {
		return fmt.Errorf("%w: redirect uses a non-public literal address", ErrInvalidSource)
	}
	return nil
}

func publicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func validateFetchURL(scheme, host string, hasUser bool) error {
	if scheme != "https" || host == "" || hasUser {
		return fmt.Errorf("%w: URL must use HTTPS without credentials", ErrInvalidSource)
	}
	return nil
}

func (s *Service) Refresh(ctx context.Context, id string) (resultErr error) {
	if !validListID(id) {
		return errors.New("invalid public list id")
	}
	if err := s.acquireWork(ctx); err != nil {
		return err
	}
	defer s.releaseWork()
	unlock := s.lockList(id)
	defer unlock()
	list, err := s.store.GetPublicList(ctx, id)
	if err != nil {
		return err
	}
	if !list.Published {
		return ErrNotPublished
	}
	refreshedAt := time.Now().UTC()
	statusRecorded := false
	defer func() {
		if resultErr == nil || statusRecorded {
			return
		}
		statusCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		statusErr := s.store.RecordPublicListRefresh(statusCtx, id, control.PublicListRefreshResult{
			Status:      control.PublicListRefreshError,
			RefreshedAt: refreshedAt,
			Error:       refreshErrorText(resultErr),
		})
		if statusErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("record public list refresh failure: %w", statusErr))
		}
	}()
	data, digest, err := s.download(ctx, list.URL, list.SHA256)
	if err != nil {
		return err
	}
	compiled, _, err := analyze(data, list.Format, s.maxEntries)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrContentRejected, err)
	}
	if len(compiled.rules) == 0 {
		return fmt.Errorf("%w: public list contains no valid entries", ErrContentRejected)
	}
	encoded, err := encodeSnapshot(list.Format, data)
	if err != nil {
		return err
	}
	newPath := s.versionedSnapshotPath(id, list.Format, digest)
	oldPath := s.activeSnapshotPath(list)
	if err := s.writeAtomic(newPath, encoded); err != nil {
		return err
	}
	if err := s.store.RecordPublicListRefresh(ctx, id, control.PublicListRefreshResult{
		Status:      control.PublicListRefreshSuccess,
		EntryCount:  uint64(len(compiled.rules)),
		SHA256:      digest,
		RefreshedAt: refreshedAt,
	}); err != nil {
		if newPath != oldPath {
			_ = os.Remove(newPath)
		}
		return fmt.Errorf("record public list refresh success: %w", err)
	}
	statusRecorded = true
	s.mu.Lock()
	s.compiled[id] = compiled
	s.mu.Unlock()
	if oldPath != newPath {
		_ = os.Remove(oldPath)
	}
	return nil
}

func refreshErrorText(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err.Error()
	}
	return err.Error()
}

func validateListURL(raw string) error {
	u, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	if err := validateFetchURL(u.URL.Scheme, u.URL.Host, u.URL.User != nil); err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(u.URL.Hostname()); err == nil && !publicAddress(ip.Unmap()) {
		return fmt.Errorf("%w: URL uses a non-public literal address", ErrInvalidSource)
	}
	return nil
}

func (s *Service) download(ctx context.Context, rawURL, expectedSHA256 string) ([]byte, string, error) {
	if err := validateListURL(rawURL); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("public list HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, s.maxSize+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > s.maxSize {
		return nil, "", fmt.Errorf("%w: size limit exceeded", ErrContentTooLarge)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if expectedSHA256 != "" && !strings.EqualFold(expectedSHA256, digest) {
		return nil, "", fmt.Errorf("%w: sha256 mismatch", ErrContentRejected)
	}
	return data, digest, nil
}

func (s *Service) Validate(ctx context.Context, listID string, spec control.PublicListSpec) (control.PublicListValidation, error) {
	normalized, err := control.NormalizePublicListSpec(spec)
	if err != nil {
		return control.PublicListValidation{}, err
	}
	published := false
	normalized.Published = &published
	var expectedUpdatedAt time.Time
	if listID != "" {
		if !validListID(listID) {
			return control.PublicListValidation{}, control.ErrInvalidInput
		}
		current, err := s.store.GetPublicList(ctx, listID)
		if err != nil {
			return control.PublicListValidation{}, err
		}
		expectedUpdatedAt = current.UpdatedAt
	}
	if err := s.acquireWork(ctx); err != nil {
		return control.PublicListValidation{}, err
	}
	defer s.releaseWork()
	data, digest, err := s.download(ctx, normalized.URL, normalized.SHA256)
	if err != nil {
		return control.PublicListValidation{}, err
	}
	compiled, diagnostics, err := analyze(data, normalized.Format, s.maxEntries)
	if err != nil {
		return control.PublicListValidation{}, fmt.Errorf("%w: %v", ErrContentRejected, err)
	}
	result := control.PublicListValidation{
		Valid:             len(compiled.rules) > 0,
		Format:            normalized.Format,
		EntryCount:        uint64(len(compiled.rules)),
		InvalidEntryCount: diagnostics.invalidCount,
		InvalidEntries:    diagnostics.invalid,
		Samples:           diagnostics.samples,
		SHA256:            digest,
		Spec:              normalized,
	}
	if !result.Valid {
		return result, nil
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return control.PublicListValidation{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	encoded, err := encodeSnapshot(normalized.Format, data)
	if err != nil {
		return control.PublicListValidation{}, err
	}
	path := filepath.Join(s.dir, ".candidate-"+token)
	if err := s.writeAtomic(path, encoded); err != nil {
		return control.PublicListValidation{}, err
	}
	now := time.Now().UTC()
	result.ValidationToken = token
	result.ExpiresAt = now.Add(validationTTL)
	s.mu.Lock()
	for existingToken, existing := range s.candidates {
		if !existing.claimed && !now.Before(existing.validation.ExpiresAt) {
			delete(s.candidates, existingToken)
			_ = os.Remove(existing.path)
		}
	}
	if len(s.candidates) >= maxValidationCandidates {
		s.mu.Unlock()
		_ = os.Remove(path)
		return control.PublicListValidation{}, ErrTooManyPending
	}
	s.candidates[token] = candidate{path: path, validation: result, listID: listID, expectedUpdatedAt: expectedUpdatedAt}
	s.mu.Unlock()
	return result, nil
}

func (s *Service) acquireWork(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseWork() { <-s.sem }

func (s *Service) Publish(ctx context.Context, actorID, listID, token string) (control.PublicList, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	candidate, ok := s.candidates[token]
	if ok && !candidate.claimed {
		candidate.claimed = true
		s.candidates[token] = candidate
	} else {
		ok = false
	}
	s.mu.Unlock()
	if !ok || token == "" {
		return control.PublicList{}, ErrValidationTokenInvalid
	}
	consume := false
	defer func() {
		s.mu.Lock()
		current, exists := s.candidates[token]
		if exists && current.path == candidate.path {
			if consume {
				delete(s.candidates, token)
			} else {
				current.claimed = false
				s.candidates[token] = current
			}
		}
		s.mu.Unlock()
		if consume {
			_ = os.Remove(candidate.path)
		}
	}()
	if candidate.listID != listID {
		consume = true
		return control.PublicList{}, ErrValidationTokenInvalid
	}
	if !now.Before(candidate.validation.ExpiresAt) {
		consume = true
		return control.PublicList{}, ErrValidationTokenExpired
	}
	if err := s.acquireWork(ctx); err != nil {
		return control.PublicList{}, err
	}
	defer s.releaseWork()
	now = time.Now().UTC()
	if !now.Before(candidate.validation.ExpiresAt) {
		consume = true
		return control.PublicList{}, ErrValidationTokenExpired
	}
	encoded, err := os.ReadFile(candidate.path)
	if err != nil {
		consume = true
		return control.PublicList{}, ErrValidationTokenInvalid
	}
	format, contents, err := decodeSnapshot(encoded, candidate.validation.Format)
	if err != nil || format != candidate.validation.Format {
		consume = true
		return control.PublicList{}, ErrValidationTokenInvalid
	}
	sum := sha256.Sum256(contents)
	if !strings.EqualFold(candidate.validation.SHA256, hex.EncodeToString(sum[:])) {
		consume = true
		return control.PublicList{}, ErrValidationTokenInvalid
	}
	creating := listID == ""
	if creating {
		listID, err = randomListID()
		if err != nil {
			return control.PublicList{}, err
		}
	}
	unlock := s.lockList(listID)
	defer unlock()
	var oldPath string
	if current, getErr := s.store.GetPublicList(ctx, listID); getErr == nil {
		oldPath = s.activeSnapshotPath(current)
	} else if !errors.Is(getErr, control.ErrNotFound) || !creating {
		return control.PublicList{}, getErr
	}
	newPath := s.versionedSnapshotPath(listID, format, candidate.validation.SHA256)
	if err := s.writeAtomic(newPath, encoded); err != nil {
		return control.PublicList{}, err
	}
	spec := candidate.validation.Spec
	published := true
	spec.Published = &published
	list, err := s.store.CommitPublicListSnapshot(ctx, actorID, listID, candidate.expectedUpdatedAt, spec, control.PublicListRefreshResult{
		Status: control.PublicListRefreshSuccess, EntryCount: candidate.validation.EntryCount,
		SHA256: candidate.validation.SHA256, RefreshedAt: now,
	})
	if err != nil {
		if oldPath != newPath {
			_ = os.Remove(newPath)
		}
		return control.PublicList{}, err
	}
	consume = true
	s.mu.Lock()
	s.compiled[list.ID], _, _ = analyze(contents, format, s.maxEntries)
	s.selectionGeneration++
	s.selections = make(map[string]selection)
	s.mu.Unlock()
	if oldPath != "" && oldPath != newPath {
		_ = os.Remove(oldPath)
	}
	return list, nil
}

func randomListID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (s *Service) writeSnapshot(id string, format control.PublicListFormat, data []byte) error {
	encoded, err := encodeSnapshot(format, data)
	if err != nil {
		return err
	}
	return s.writeAtomic(s.snapshotPath(id), encoded)
}

func (s *Service) writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(s.dir, ".public-list-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	keep := false
	defer func() {
		tmp.Close()
		if !keep {
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		dir.Close()
		return err
	}
	if err := dir.Close(); err != nil {
		return err
	}
	keep = true
	return nil
}
func (s *Service) snapshotPath(id string) string { return filepath.Join(s.dir, id+".list") }

func (s *Service) versionedSnapshotPath(id string, format control.PublicListFormat, digest string) string {
	return filepath.Join(s.dir, id+"-"+string(format)+"-"+strings.ToLower(digest)+".list")
}

func (s *Service) activeSnapshotPath(list control.PublicList) string {
	if list.SnapshotSHA256 != "" {
		preferred := s.versionedSnapshotPath(list.ID, list.Format, list.SnapshotSHA256)
		if _, err := os.Stat(preferred); err == nil || !errors.Is(err, os.ErrNotExist) {
			return preferred
		}
		for _, format := range []control.PublicListFormat{control.PublicListFormatMosDNS, control.PublicListFormatHosts} {
			alternative := s.versionedSnapshotPath(list.ID, format, list.SnapshotSHA256)
			if alternative == preferred {
				continue
			}
			if _, err := os.Stat(alternative); err == nil {
				return alternative
			}
		}
		return preferred
	}
	return s.snapshotPath(list.ID)
}

func (s *Service) Match(ctx context.Context, userID, name string) (bool, error) {
	id, err := s.MatchID(ctx, userID, name)
	return id != "", err
}

func (s *Service) MatchID(ctx context.Context, userID, name string) (string, error) {
	lists, err := s.userLists(ctx, userID)
	if err != nil {
		return "", err
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, list := range lists {
		compiled, err := s.load(ctx, list)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if compiled.match(name) {
			return list.ID, nil
		}
	}
	return "", nil
}
func (s *Service) Invalidate(userID string) {
	s.mu.Lock()
	if userID == "" {
		s.selectionGeneration++
		s.selections = make(map[string]selection)
		s.compiled = make(map[string]*compiledList)
		s.attempts = make(map[string]time.Time)
	} else {
		s.userSelectionGenerations[userID]++
		delete(s.selections, userID)
	}
	s.mu.Unlock()
}

func (s *Service) InvalidateID(id string) {
	s.mu.Lock()
	s.selectionGeneration++
	delete(s.compiled, id)
	delete(s.attempts, id)
	s.selections = make(map[string]selection)
	s.mu.Unlock()
}

func (s *Service) Update(ctx context.Context, actorID, id string, patch control.PublicListPatch) (control.PublicList, error) {
	if !validListID(id) {
		return control.PublicList{}, fmt.Errorf("%w: invalid public list id", control.ErrInvalidInput)
	}
	unlock := s.lockList(id)
	defer unlock()
	list, err := s.store.UpdatePublicList(ctx, actorID, id, patch)
	if err != nil {
		return control.PublicList{}, err
	}
	s.Invalidate("")
	return list, nil
}

func (s *Service) Delete(ctx context.Context, actorID, id string) error {
	if !validListID(id) {
		return fmt.Errorf("%w: invalid public list id", control.ErrInvalidInput)
	}
	unlock := s.lockList(id)
	defer unlock()
	paths := []string{s.snapshotPath(id)}
	versioned, err := filepath.Glob(filepath.Join(s.dir, id+"-*.list"))
	if err != nil {
		return err
	}
	paths = append(paths, versioned...)
	tombstones := make(map[string]string, len(paths))
	for _, path := range paths {
		tombstone := filepath.Join(s.dir, ".deleted-public-list-"+filepath.Base(path))
		if err := os.Rename(path, tombstone); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			restoreErr := restoreTombstones(tombstones)
			if restoreErr != nil {
				s.InvalidateID(id)
			}
			return errors.Join(err, restoreErr)
		}
		tombstones[path] = tombstone
	}
	if err := s.store.DeletePublicList(ctx, actorID, id); err != nil {
		restoreErr := restoreTombstones(tombstones)
		if restoreErr != nil {
			s.InvalidateID(id)
		}
		return errors.Join(err, restoreErr)
	}
	s.InvalidateID(id)
	var cleanupErrors []error
	for _, tombstone := range tombstones {
		if err := os.Remove(tombstone); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if err := errors.Join(cleanupErrors...); err != nil {
		return fmt.Errorf("%w: %v", ErrSnapshotCleanup, err)
	}
	return nil
}

func restoreTombstones(tombstones map[string]string) error {
	var restoreErrors []error
	for original, tombstone := range tombstones {
		if err := os.Rename(tombstone, original); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if _, statErr := os.Stat(original); statErr == nil {
					continue
				}
			}
			restoreErrors = append(restoreErrors, fmt.Errorf("restore %s: %w", filepath.Base(original), err))
		}
	}
	return errors.Join(restoreErrors...)
}

func (s *Service) RefreshAll(ctx context.Context) error {
	result, err := s.RefreshAllDetailed(ctx)
	if err != nil {
		return err
	}
	var joined []error
	for _, item := range result.Items {
		if item.Error != "" {
			joined = append(joined, fmt.Errorf("refresh %s: %s", item.ID, item.Error))
		}
	}
	return errors.Join(joined...)
}

func (s *Service) RefreshAllDetailed(ctx context.Context) (control.PublicListRefreshAllResult, error) {
	var lists []control.PublicList
	cursor := ""
	for {
		page, err := s.store.ListPublicLists(ctx, control.Page{Limit: 1000, Cursor: cursor})
		if err != nil {
			return control.PublicListRefreshAllResult{}, err
		}
		for _, list := range page.Items {
			// Enabled is the default user selection. A user can explicitly
			// enable a default-disabled catalog entry, so every published list
			// needs a current snapshot.
			if list.Published {
				lists = append(lists, list)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	ids := make([]string, 0, len(lists))
	for _, list := range lists {
		ids = append(ids, list.ID)
	}
	items, err := s.refreshIDs(ctx, ids)
	if err != nil {
		return control.PublicListRefreshAllResult{}, err
	}
	result := control.PublicListRefreshAllResult{Items: items}
	slices.SortFunc(result.Items, func(a, b control.PublicListRefreshItem) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.cleanupExpiredCandidates(time.Now().UTC())
		s.refreshDue(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) refreshDue(ctx context.Context) {
	var ids []string
	cursor := ""
	now := time.Now()
	for {
		page, err := s.store.ListPublicLists(ctx, control.Page{Limit: 1000, Cursor: cursor})
		if err != nil {
			return
		}
		for _, list := range page.Items {
			if !list.Published {
				continue
			}
			s.mu.Lock()
			last := s.attempts[list.ID]
			due := last.IsZero() || now.Sub(last) >= time.Duration(list.RefreshSeconds)*time.Second
			if due {
				s.attempts[list.ID] = now
			}
			s.mu.Unlock()
			if due {
				ids = append(ids, list.ID)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(ids) == 0 {
		return
	}
	_, _ = s.refreshIDs(ctx, ids)
}

func (s *Service) refreshIDs(ctx context.Context, ids []string) ([]control.PublicListRefreshItem, error) {
	jobs := make(chan string)
	items := make(chan control.PublicListRefreshItem, len(ids))
	var wg sync.WaitGroup
	workers := min(cap(s.sem), len(ids))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				item := control.PublicListRefreshItem{ID: id}
				if err := s.Refresh(ctx, id); err != nil {
					item.Error = refreshErrorText(err)
				}
				items <- item
			}
		}()
	}
	for _, id := range ids {
		select {
		case jobs <- id:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			close(items)
			return nil, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	close(items)
	out := make([]control.PublicListRefreshItem, 0, len(ids))
	for item := range items {
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) cleanupExpiredCandidates(now time.Time) {
	var paths []string
	s.mu.Lock()
	for token, candidate := range s.candidates {
		if !candidate.claimed && !now.Before(candidate.validation.ExpiresAt) {
			delete(s.candidates, token)
			paths = append(paths, candidate.path)
		}
	}
	s.mu.Unlock()
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func (s *Service) userLists(ctx context.Context, userID string) ([]control.PublicList, error) {
	for {
		now := time.Now()
		s.mu.RLock()
		cached, ok := s.selections[userID]
		generation := s.selectionGeneration
		userGeneration := s.userSelectionGenerations[userID]
		s.mu.RUnlock()
		if ok && now.Before(cached.expires) {
			return cached.lists, nil
		}
		var lists []control.PublicList
		cursor := ""
		for {
			page, err := s.store.ListUserPublicLists(ctx, userID, control.Page{Limit: 1000, Cursor: cursor})
			if err != nil {
				return nil, err
			}
			for _, item := range page.Items {
				if item.List.Published && item.Enabled {
					lists = append(lists, item.List)
				}
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		s.mu.Lock()
		if s.selectionGeneration != generation || s.userSelectionGenerations[userID] != userGeneration {
			s.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		s.selections[userID] = selection{expires: now.Add(5 * time.Second), lists: lists}
		s.mu.Unlock()
		return lists, nil
	}
}
func (s *Service) load(ctx context.Context, list control.PublicList) (*compiledList, error) {
	if !validListID(list.ID) {
		return nil, errors.New("invalid public list id")
	}
	s.mu.RLock()
	compiled := s.compiled[list.ID]
	s.mu.RUnlock()
	if compiled != nil {
		return compiled, nil
	}
	if err := s.acquireWork(ctx); err != nil {
		return nil, err
	}
	defer s.releaseWork()
	unlock := s.lockList(list.ID)
	defer unlock()
	current, err := s.store.GetPublicList(ctx, list.ID)
	if err != nil {
		return nil, err
	}
	if !current.Published || current.SnapshotStatus == control.PublicListSnapshotMissing {
		return nil, os.ErrNotExist
	}
	list = current
	s.mu.RLock()
	compiled = s.compiled[list.ID]
	s.mu.RUnlock()
	if compiled != nil {
		return compiled, nil
	}
	data, err := os.ReadFile(s.activeSnapshotPath(list))
	if err != nil {
		return nil, err
	}
	format, contents, err := decodeSnapshot(data, list.Format)
	if err != nil {
		return nil, err
	}
	if format != list.Format {
		return nil, errors.New("public list snapshot format mismatch")
	}
	if list.SnapshotSHA256 != "" {
		sum := sha256.Sum256(contents)
		if !strings.EqualFold(list.SnapshotSHA256, hex.EncodeToString(sum[:])) {
			return nil, fmt.Errorf("public list snapshot checksum mismatch")
		}
	}
	compiled, _, err = analyze(contents, format, s.maxEntries)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if existing := s.compiled[list.ID]; existing != nil {
		compiled = existing
	} else {
		s.compiled[list.ID] = compiled
	}
	s.mu.Unlock()
	return compiled, nil
}

func (s *Service) lockList(id string) func() {
	s.mu.Lock()
	lock := s.listLocks[id]
	if lock == nil {
		lock = new(sync.Mutex)
		s.listLocks[id] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func encodeSnapshot(format control.PublicListFormat, data []byte) ([]byte, error) {
	var marker byte
	switch format {
	case control.PublicListFormatMosDNS:
		marker = 1
	case control.PublicListFormatHosts:
		marker = 2
	default:
		return nil, errors.New("unknown public list format")
	}
	encoded := make([]byte, len(snapshotMagic)+1+len(data))
	copy(encoded, snapshotMagic)
	encoded[len(snapshotMagic)] = marker
	copy(encoded[len(snapshotMagic)+1:], data)
	return encoded, nil
}

func decodeSnapshot(data []byte, fallback control.PublicListFormat) (control.PublicListFormat, []byte, error) {
	if !bytes.HasPrefix(data, []byte(snapshotMagic)) {
		// Feature previews wrote the downloaded body directly. Accept those
		// snapshots so an upgrade can refresh them in place.
		return fallback, data, nil
	}
	if len(data) == len(snapshotMagic) {
		return "", nil, errors.New("invalid public list snapshot")
	}
	var format control.PublicListFormat
	switch data[len(snapshotMagic)] {
	case 1:
		format = control.PublicListFormatMosDNS
	case 2:
		format = control.PublicListFormatHosts
	default:
		return "", nil, errors.New("invalid public list snapshot format")
	}
	return format, data[len(snapshotMagic)+1:], nil
}

func validListID(id string) bool {
	if id == "" || len(id) > 64 || filepath.Base(id) != id {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
func (c *compiledList) match(name string) bool {
	if _, ok := c.exact[name]; ok {
		return true
	}
	for suffix := name; suffix != ""; {
		if _, ok := c.suffix[suffix]; ok {
			return true
		}
		dot := strings.IndexByte(suffix, '.')
		if dot < 0 {
			break
		}
		suffix = suffix[dot+1:]
	}
	for _, r := range c.patterns {
		switch r.kind {
		case "keyword":
			if strings.Contains(name, r.value) {
				return true
			}
		case "regexp":
			if r.re.MatchString(name) {
				return true
			}
		}
	}
	return false
}

func compile(data []byte, format control.PublicListFormat) (*compiledList, error) {
	compiled, _, err := analyze(data, format, int(^uint(0)>>1))
	return compiled, err
}

type analysisDiagnostics struct {
	invalidCount uint64
	invalid      []control.PublicListInvalidEntry
	samples      []string
}

func (d *analysisDiagnostics) reject(line uint64, reason string) {
	d.invalidCount++
	if len(d.invalid) < maxInvalidEntryDetails {
		d.invalid = append(d.invalid, control.PublicListInvalidEntry{Line: line, Reason: reason})
	}
}

func (d *analysisDiagnostics) sample(value string) {
	if len(d.samples) < maxValidationSamples {
		d.samples = append(d.samples, value)
	}
}

func analyze(data []byte, format control.PublicListFormat, maxEntries int) (*compiledList, analysisDiagnostics, error) {
	switch format {
	case control.PublicListFormatMosDNS:
		return analyzeMosDNS(data, maxEntries)
	case control.PublicListFormatHosts:
		return analyzeHosts(data, maxEntries)
	default:
		return nil, analysisDiagnostics{}, errors.New("unknown public list format")
	}
}

func appendRule(out *compiledList, rule compiledRule, maxEntries int) error {
	if len(out.rules) >= maxEntries {
		return errors.New("public list exceeds entry limit")
	}
	out.rules = append(out.rules, rule)
	switch rule.kind {
	case "exact":
		if out.exact == nil {
			out.exact = make(map[string]struct{})
		}
		out.exact[rule.value] = struct{}{}
	case "suffix":
		if out.suffix == nil {
			out.suffix = make(map[string]struct{})
		}
		out.suffix[rule.value] = struct{}{}
	case "keyword", "regexp":
		if len(out.patterns) >= maxLinearRules {
			return errors.New("public list exceeds keyword and regexp entry limit")
		}
		out.patterns = append(out.patterns, rule)
	}
	return nil
}

func analyzeMosDNS(data []byte, maxEntries int) (*compiledList, analysisDiagnostics, error) {
	out := new(compiledList)
	var diagnostics analysisDiagnostics
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	lineNumber := uint64(0)
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		kind, value := "suffix", line
		if prefix, rest, ok := strings.Cut(line, ":"); ok && (prefix == "domain" || prefix == "full" || prefix == "keyword" || prefix == "regexp") {
			kind, value = prefix, strings.TrimSpace(rest)
			if kind == "domain" {
				kind = "suffix"
			}
			if kind == "full" {
				kind = "exact"
			}
		}
		rule := compiledRule{kind: kind, value: strings.ToLower(strings.TrimSuffix(value, "."))}
		if rule.value == "" {
			diagnostics.reject(lineNumber, "empty rule")
			continue
		}
		if kind == "regexp" {
			re, err := regexp.Compile(value)
			if err != nil {
				diagnostics.reject(lineNumber, "invalid regular expression")
				continue
			}
			rule.re = re
		} else if kind != "keyword" {
			if _, ok := dns.IsDomainName(rule.value + "."); !ok {
				diagnostics.reject(lineNumber, "invalid domain name")
				continue
			}
		}
		if err := appendRule(out, rule, maxEntries); err != nil {
			return nil, diagnostics, err
		}
		diagnostics.sample(line)
	}
	if err := scanner.Err(); err != nil {
		return nil, diagnostics, err
	}
	return out, diagnostics, nil
}

func analyzeHosts(data []byte, maxEntries int) (*compiledList, analysisDiagnostics, error) {
	out := new(compiledList)
	var diagnostics analysisDiagnostics
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	lineNumber := uint64(0)
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			diagnostics.reject(lineNumber, "hosts entry requires an address and domain")
			continue
		}
		if _, err := netip.ParseAddr(fields[0]); err != nil {
			diagnostics.reject(lineNumber, "invalid hosts address")
			continue
		}
		for _, host := range fields[1:] {
			host = strings.ToLower(strings.TrimSuffix(dns.Fqdn(host), "."))
			if _, ok := dns.IsDomainName(host + "."); !ok {
				diagnostics.reject(lineNumber, "invalid hosts domain")
				continue
			}
			if err := appendRule(out, compiledRule{kind: "exact", value: host}, maxEntries); err != nil {
				return nil, diagnostics, err
			}
			diagnostics.sample(host)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, diagnostics, err
	}
	return out, diagnostics, nil
}
