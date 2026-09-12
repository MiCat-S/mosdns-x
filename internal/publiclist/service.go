package publiclist

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
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
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/pmkol/mosdns-x/internal/control"
)

const defaultMaxSize int64 = 8 << 20

const snapshotMagic = "MOSDNS-X-PUBLIC-LIST\x00"

type Store interface {
	GetPublicList(context.Context, string) (control.PublicList, error)
	ListPublicLists(context.Context, control.Page) (control.PageResult[control.PublicList], error)
	ListUserPublicLists(context.Context, string, control.Page) (control.PageResult[control.UserPublicList], error)
	RecordPublicListRefresh(context.Context, string, control.PublicListRefreshResult) error
}

type Options struct {
	Directory   string
	Timeout     time.Duration
	MaxSize     int64
	Concurrency int
}

type Service struct {
	store   Store
	dir     string
	client  *http.Client
	maxSize int64
	sem     chan struct{}

	mu         sync.RWMutex
	compiled   map[string]*compiledList
	selections map[string]selection
	attempts   map[string]time.Time
}

type selection struct {
	expires time.Time
	lists   []control.PublicList
}
type compiledRule struct {
	kind  string
	value string
	re    *regexp.Regexp
}
type compiledList struct{ rules []compiledRule }

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
	client := secureHTTPClient(opts.Timeout)
	return &Service{store: store, dir: opts.Directory, client: client, maxSize: opts.MaxSize, sem: make(chan struct{}, opts.Concurrency), compiled: make(map[string]*compiledList), selections: make(map[string]selection), attempts: make(map[string]time.Time)}, nil
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
		for _, ip := range ips {
			ip = ip.Unmap()
			if !publicAddress(ip) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return nil, errors.New("public list host has no public address")
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
		return errors.New("public list redirect uses a non-public literal address")
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
		return errors.New("public list URL must use HTTPS without credentials")
	}
	return nil
}

func (s *Service) Refresh(ctx context.Context, id string) (resultErr error) {
	if !validListID(id) {
		return errors.New("invalid public list id")
	}
	list, err := s.store.GetPublicList(ctx, id)
	if err != nil {
		return err
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
	if err := validateListURL(list.URL); err != nil {
		return err
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, list.URL, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("public list HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, s.maxSize+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > s.maxSize {
		return errors.New("public list exceeds size limit")
	}
	sum := sha256.Sum256(data)
	if list.SHA256 != "" && !strings.EqualFold(list.SHA256, hex.EncodeToString(sum[:])) {
		return errors.New("public list sha256 mismatch")
	}
	compiled, err := compile(data, list.Format)
	if err != nil {
		return err
	}
	if err := s.writeSnapshot(id, list.Format, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.compiled[id] = compiled
	s.mu.Unlock()
	statusRecorded = true
	if err := s.store.RecordPublicListRefresh(ctx, id, control.PublicListRefreshResult{
		Status:      control.PublicListRefreshSuccess,
		EntryCount:  uint64(len(compiled.rules)),
		RefreshedAt: refreshedAt,
	}); err != nil {
		return fmt.Errorf("record public list refresh success: %w", err)
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
		return errors.New("public list URL uses a non-public literal address")
	}
	return nil
}

func (s *Service) writeSnapshot(id string, format control.PublicListFormat, data []byte) error {
	encoded, err := encodeSnapshot(format, data)
	if err != nil {
		return err
	}
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
	if _, err := tmp.Write(encoded); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.snapshotPath(id)); err != nil {
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
		compiled, err := s.load(list)
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
		s.selections = make(map[string]selection)
		s.compiled = make(map[string]*compiledList)
		s.attempts = make(map[string]time.Time)
	} else {
		delete(s.selections, userID)
	}
	s.mu.Unlock()
}

func (s *Service) InvalidateID(id string) {
	s.mu.Lock()
	delete(s.compiled, id)
	delete(s.attempts, id)
	s.selections = make(map[string]selection)
	s.mu.Unlock()
}

func (s *Service) Delete(id string) error {
	if !validListID(id) {
		return errors.New("invalid public list id")
	}
	s.InvalidateID(id)
	if err := os.Remove(s.snapshotPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Service) RefreshAll(ctx context.Context) error {
	var lists []control.PublicList
	cursor := ""
	for {
		page, err := s.store.ListPublicLists(ctx, control.Page{Limit: 1000, Cursor: cursor})
		if err != nil {
			return err
		}
		for _, list := range page.Items {
			// Enabled is the default user selection. A user can explicitly
			// enable a default-disabled catalog entry, so every published list
			// needs a current snapshot.
			lists = append(lists, list)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	errs := make(chan error, len(lists))
	var wg sync.WaitGroup
	for _, list := range lists {
		list := list
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Refresh(ctx, list.ID); err != nil {
				errs <- fmt.Errorf("refresh %s: %w", list.ID, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	var joined []error
	for err := range errs {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.refreshDue(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) refreshDue(ctx context.Context) {
	var refreshes sync.WaitGroup
	defer refreshes.Wait()
	cursor := ""
	now := time.Now()
	for {
		page, err := s.store.ListPublicLists(ctx, control.Page{Limit: 1000, Cursor: cursor})
		if err != nil {
			return
		}
		for _, list := range page.Items {
			s.mu.Lock()
			last := s.attempts[list.ID]
			due := last.IsZero() || now.Sub(last) >= time.Duration(list.RefreshSeconds)*time.Second
			if due {
				s.attempts[list.ID] = now
			}
			s.mu.Unlock()
			if due {
				refreshes.Add(1)
				go func(id string) {
					defer refreshes.Done()
					_ = s.Refresh(ctx, id)
				}(list.ID)
			}
		}
		if page.NextCursor == "" {
			return
		}
		cursor = page.NextCursor
	}
}

func (s *Service) userLists(ctx context.Context, userID string) ([]control.PublicList, error) {
	now := time.Now()
	s.mu.RLock()
	cached, ok := s.selections[userID]
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
			if item.Enabled {
				lists = append(lists, item.List)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	s.mu.Lock()
	s.selections[userID] = selection{expires: now.Add(5 * time.Second), lists: lists}
	s.mu.Unlock()
	return lists, nil
}
func (s *Service) load(list control.PublicList) (*compiledList, error) {
	if !validListID(list.ID) {
		return nil, errors.New("invalid public list id")
	}
	s.mu.RLock()
	compiled := s.compiled[list.ID]
	s.mu.RUnlock()
	if compiled != nil {
		return compiled, nil
	}
	data, err := os.ReadFile(s.snapshotPath(list.ID))
	if err != nil {
		return nil, err
	}
	format, contents, err := decodeSnapshot(data, list.Format)
	if err != nil {
		return nil, err
	}
	compiled, err = compile(contents, format)
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
	for _, r := range c.rules {
		switch r.kind {
		case "exact":
			if name == r.value {
				return true
			}
		case "suffix":
			if name == r.value || strings.HasSuffix(name, "."+r.value) {
				return true
			}
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
	switch format {
	case control.PublicListFormatMosDNS:
		return compileMosDNS(data)
	case control.PublicListFormatHosts:
		return compileHosts(data)
	default:
		return nil, errors.New("unknown public list format")
	}
}
func compileMosDNS(data []byte) (*compiledList, error) {
	out := new(compiledList)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
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
			return nil, errors.New("empty public list rule")
		}
		if kind == "regexp" {
			re, err := regexp.Compile(value)
			if err != nil {
				return nil, err
			}
			rule.re = re
		}
		out.rules = append(out.rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
func compileHosts(data []byte) (*compiledList, error) {
	out := new(compiledList)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if _, err := netip.ParseAddr(fields[0]); err != nil {
			continue
		}
		for _, host := range fields[1:] {
			host = strings.ToLower(strings.TrimSuffix(dns.Fqdn(host), "."))
			if _, ok := dns.IsDomainName(host + "."); !ok {
				return nil, fmt.Errorf("invalid hosts domain %q", host)
			}
			out.rules = append(out.rules, compiledRule{kind: "exact", value: host})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
