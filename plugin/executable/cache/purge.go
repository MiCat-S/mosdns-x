package cache

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/cache"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
)

// headerLen is the size of the DNS header that starts every cache key.
const headerLen = 12

// purgeTimeout bounds a purge requested through the plugin API.
const purgeTimeout = 30 * time.Second

// domainKeys selects cache keys by the name in their question. Keys are
// packed queries (dnsutils.GetMsgKey), so the name follows the header in
// uncompressed wire format.
type domainKeys struct {
	labels     []string // lower case, root excluded
	subdomains bool
}

func newDomainKeys(domain string, subdomains bool) (*domainKeys, error) {
	fqdn, err := dnsutils.NormalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	return &domainKeys{labels: strings.Split(strings.TrimSuffix(fqdn, "."), "."), subdomains: subdomains}, nil
}

func (d *domainKeys) match(key string) bool {
	if len(key) < headerLen || key[4] == 0 && key[5] == 0 { // no question
		return false
	}
	var starts [127]int
	n := 0
	off := headerLen
	for {
		if off >= len(key) {
			return false
		}
		l := int(key[off])
		if l == 0 {
			break
		}
		if l > 63 || n == len(starts) { // compression pointer or malformed
			return false
		}
		starts[n] = off
		n++
		off += 1 + l
	}
	want := len(d.labels)
	if n < want || !d.subdomains && n != want {
		return false
	}
	for i, label := range d.labels {
		s := starts[n-want+i]
		if !equalFoldASCII(key[s+1:s+1+int(key[s])], label) {
			return false
		}
	}
	return true
}

// equalFoldASCII reports whether s equals the lower case label, ignoring
// ASCII case only, as DNS does.
func equalFoldASCII(s, label string) bool {
	if len(s) != len(label) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != label[i] {
			return false
		}
	}
	return true
}

// glob returns a Redis pattern that every key match accepts satisfies.
func (d *domainKeys) glob() string {
	var b strings.Builder
	b.WriteString(strings.Repeat("?", headerLen))
	if d.subdomains {
		b.WriteByte('*')
	}
	for _, label := range d.labels {
		writeGlobByte(&b, byte(len(label)))
		for i := 0; i < len(label); i++ {
			writeGlobByte(&b, label[i])
		}
	}
	writeGlobByte(&b, 0)
	b.WriteByte('*')
	return b.String()
}

func writeGlobByte(b *strings.Builder, c byte) {
	switch {
	case 'a' <= c && c <= 'z':
		b.WriteByte('[')
		b.WriteByte(c)
		b.WriteByte(c - 'a' + 'A')
		b.WriteByte(']')
	case c == '*' || c == '?' || c == '[' || c == ']' || c == '\\':
		b.WriteByte('\\')
		b.WriteByte(c)
	default:
		b.WriteByte(c)
	}
}

// PurgeDomain removes the cached responses for domain, and with subdomains
// also those for every name below it, regardless of query type.
func (c *cachePlugin) PurgeDomain(ctx context.Context, domain string, subdomains bool) (int, error) {
	keys, err := newDomainKeys(domain, subdomains)
	if err != nil {
		return 0, err
	}
	purger, ok := c.backend.(cache.Purger)
	if !ok {
		return 0, errors.New("cache backend cannot remove selected entries")
	}
	return purger.Purge(ctx, keys.glob(), keys.match)
}

// ServeHTTP serves POST /plugins/<tag>/purge?domain=<name>[&subdomains=true].
func (c *cachePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/plugins/"+c.Tag()+"/purge" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	domain := query.Get("domain")
	subdomains := false
	if v := query.Get("subdomains"); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			http.Error(w, "invalid subdomains value", http.StatusBadRequest)
			return
		}
		subdomains = parsed
	}
	name, err := dnsutils.NormalizeDomain(domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), purgeTimeout)
	defer cancel()
	removed, err := c.PurgeDomain(ctx, name, subdomains)
	if err != nil {
		c.L().Warn("cache purge", zap.String("domain", name), zap.Bool("subdomains", subdomains), zap.Error(err))
		http.Error(w, "cache purge failed", http.StatusInternalServerError)
		return
	}
	c.L().Info("cache purged", zap.String("domain", name), zap.Bool("subdomains", subdomains), zap.Int("removed", removed))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"removed": removed})
}
