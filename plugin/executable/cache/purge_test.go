package cache

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/pkg/cache"
	"github.com/pmkol/mosdns-x/pkg/cache/redis_cache"
	"github.com/pmkol/mosdns-x/pkg/dnsutils"
)

func purgeTestKey(t *testing.T, name string, qtype uint16, ecs bool) string {
	t.Helper()
	q := new(dns.Msg).SetQuestion(name, qtype)
	q.Id = 4321
	if ecs {
		opt := new(dns.OPT)
		opt.Hdr.Name = "."
		opt.Hdr.Rrtype = dns.TypeOPT
		opt.Option = []dns.EDNS0{&dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: []byte{192, 0, 2, 0}}}
		q.Extra = []dns.RR{opt}
	}
	key, err := dnsutils.GetMsgKey(q, 0)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Clone(key)
}

// redisGlobMatch is Redis' stringmatchlen without the nocase flag, enough
// to check the patterns produced by domainKeys.glob.
func redisGlobMatch(pattern, s string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			for len(pattern) > 1 && pattern[1] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if redisGlobMatch(pattern[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			s = s[1:]
			pattern = pattern[1:]
		case '[':
			end := strings.IndexByte(pattern, ']')
			if len(s) == 0 || end < 0 || !strings.Contains(pattern[1:end], s[:1]) {
				return false
			}
			s = s[1:]
			pattern = pattern[end+1:]
		case '\\':
			if len(pattern) < 2 || len(s) == 0 || s[0] != pattern[1] {
				return false
			}
			s = s[1:]
			pattern = pattern[2:]
		default:
			if len(s) == 0 || s[0] != pattern[0] {
				return false
			}
			s = s[1:]
			pattern = pattern[1:]
		}
	}
	return len(s) == 0
}

func TestDomainKeysMatch(t *testing.T) {
	names := []string{
		"example.com.", "EXAMPLE.com.", "www.example.com.", "a.b.Example.COM.",
		"aexample.com.", "example.com.cn.", "example.org.", "com.", "_dmarc.example.com.",
		// A label length of 42 is the byte '*' and must not act as a wildcard.
		strings.Repeat("x", 42) + ".example.com.", strings.Repeat("y", 63) + ".example.com.",
	}
	tests := []struct {
		domain     string
		subdomains bool
		want       []string
	}{
		{"example.com", false, []string{"example.com.", "EXAMPLE.com."}},
		{"Example.Com.", true, []string{
			"example.com.", "EXAMPLE.com.", "www.example.com.", "a.b.Example.COM.", "_dmarc.example.com.",
			strings.Repeat("x", 42) + ".example.com.", strings.Repeat("y", 63) + ".example.com.",
		}},
		{"www.example.com", true, []string{"www.example.com."}},
		{"com", false, []string{"com."}},
		{strings.Repeat("x", 42) + ".example.com", false, []string{strings.Repeat("x", 42) + ".example.com."}},
	}
	for _, tt := range tests {
		keys, err := newDomainKeys(tt.domain, tt.subdomains)
		if err != nil {
			t.Fatal(err)
		}
		glob := keys.glob()
		for _, name := range names {
			wanted := false
			for _, w := range tt.want {
				wanted = wanted || w == name
			}
			for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeHTTPS} {
				for _, ecs := range []bool{false, true} {
					key := purgeTestKey(t, name, qtype, ecs)
					if got := keys.match(key); got != wanted {
						t.Errorf("%s subdomains=%v: match(%s type %d ecs %v) = %v", tt.domain, tt.subdomains, name, qtype, ecs, got)
					}
					if wanted && !redisGlobMatch(glob, key) {
						t.Errorf("%s subdomains=%v: glob rejects %s type %d ecs %v", tt.domain, tt.subdomains, name, qtype, ecs)
					}
				}
			}
		}
	}
}

func TestDomainKeysRejectMalformedKeys(t *testing.T) {
	keys, err := newDomainKeys("example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	valid := purgeTestKey(t, "example.com.", dns.TypeA, false)
	noQuestion := []byte(valid)
	noQuestion[4], noQuestion[5] = 0, 0
	pointer := []byte(valid[:headerLen] + "\xc0\x0c")
	for _, key := range []string{"", valid[:headerLen], valid[:len(valid)-6], string(noQuestion), string(pointer), valid[:headerLen] + "\x07example\x03co"} {
		if keys.match(key) {
			t.Errorf("match(%q) = true", key)
		}
	}
	if _, err := newDomainKeys("*.example.com", true); err == nil {
		t.Fatal("wildcard domain accepted")
	}
}

func storeTestAnswer(t *testing.T, p *cachePlugin, name string, qtype uint16) string {
	t.Helper()
	key := purgeTestKey(t, name, qtype, false)
	r := new(dns.Msg).SetQuestion(name, qtype)
	r.Response = true
	rr, err := dns.NewRR(name + " 300 IN TXT cached")
	if err != nil {
		t.Fatal(err)
	}
	r.Answer = []dns.RR{rr}
	if err := p.tryStoreMsg(key, r, "test"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := p.backend.Get(key); v == nil {
		t.Fatalf("%s was not stored", name)
	}
	return key
}

func checkPurged(t *testing.T, p *cachePlugin, keys map[string]string, gone ...string) {
	t.Helper()
	for name, key := range keys {
		removed := false
		for _, g := range gone {
			removed = removed || g == name
		}
		if v, _, _ := p.backend.Get(key); (v == nil) != removed {
			t.Errorf("%s: cached=%v, want removed=%v", name, v != nil, removed)
		}
	}
}

func TestPurgeDomainMemoryCache(t *testing.T) {
	p := newTestCache(false)
	defer p.backend.Close()
	keys := map[string]string{}
	for _, name := range []string{"example.com.", "www.example.com.", "example.net."} {
		keys[name] = storeTestAnswer(t, p, name, dns.TypeA)
	}
	keys["example.com. AAAA"] = storeTestAnswer(t, p, "Example.com.", dns.TypeAAAA)

	n, err := p.PurgeDomain(context.Background(), "EXAMPLE.com", false)
	if err != nil || n != 2 {
		t.Fatalf("exact purge = %d, %v; want 2", n, err)
	}
	checkPurged(t, p, keys, "example.com.", "example.com. AAAA")

	n, err = p.PurgeDomain(context.Background(), "example.com", true)
	if err != nil || n != 1 {
		t.Fatalf("subdomain purge = %d, %v; want 1", n, err)
	}
	checkPurged(t, p, keys, "example.com.", "example.com. AAAA", "www.example.com.")

	if _, err := p.PurgeDomain(context.Background(), "", false); err == nil {
		t.Fatal("empty domain accepted")
	}
}

func TestPurgeHTTPHandler(t *testing.T) {
	p := newTestCache(false)
	defer p.backend.Close()
	keys := map[string]string{
		"example.com.":     storeTestAnswer(t, p, "example.com.", dns.TypeA),
		"www.example.com.": storeTestAnswer(t, p, "www.example.com.", dns.TypeA),
	}
	path := "/plugins/" + p.Tag() + "/purge"

	for _, tc := range []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, path + "?domain=example.com", http.StatusMethodNotAllowed},
		{http.MethodPost, path, http.StatusBadRequest},
		{http.MethodPost, path + "?domain=*.example.com", http.StatusBadRequest},
		{http.MethodPost, path + "?domain=example.com&subdomains=maybe", http.StatusBadRequest},
		{http.MethodPost, "/plugins/" + p.Tag() + "/flush", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(tc.method, tc.target, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.target, w.Code, tc.status)
		}
	}
	checkPurged(t, p, keys)

	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path+"?domain=example.com&subdomains=true", nil))
	var body struct{ Removed int }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Removed != 2 {
		t.Fatalf("purge = %d %s", w.Code, w.Body.String())
	}
	checkPurged(t, p, keys, "example.com.", "www.example.com.")
}

func TestPurgeDomainRunsOneAtATime(t *testing.T) {
	p := newTestCache(false)
	defer p.backend.Close()
	keys := map[string]string{"example.com.": storeTestAnswer(t, p, "example.com.", dns.TypeA)}
	atomic.StoreUint32(&p.purging, 1)
	if _, err := p.PurgeDomain(context.Background(), "example.com", false); !errors.Is(err, cache.ErrPurgeBusy) {
		t.Fatalf("busy purge: %v", err)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/plugins/"+p.Tag()+"/purge?domain=example.com", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("busy http = %d %s", w.Code, w.Body.String())
	}
	checkPurged(t, p, keys)
	atomic.StoreUint32(&p.purging, 0)
	if n, err := p.PurgeDomain(context.Background(), "example.com", false); err != nil || n != 1 {
		t.Fatalf("purge after busy = %d, %v", n, err)
	}
	if atomic.LoadUint32(&p.purging) != 0 {
		t.Fatal("purge flag not released")
	}
}

type stubBackend struct{ cache.Backend }

func TestPurgeDomainNeedsAPurger(t *testing.T) {
	p := newTestCache(false)
	p.backend.Close()
	p.backend = stubBackend{}
	if _, err := p.PurgeDomain(context.Background(), "example.com", false); err == nil {
		t.Fatal("purge on a backend without Purge succeeded")
	}
}

// TestPurgeDomainRedis runs against a real server when MOSDNS_TEST_REDIS_URL
// names a scratch database, e.g. redis://127.0.0.1:6379/15. The database is
// flushed.
func TestPurgeDomainRedis(t *testing.T) {
	url := os.Getenv("MOSDNS_TEST_REDIS_URL")
	if url == "" {
		t.Skip("MOSDNS_TEST_REDIS_URL is not set")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opt)
	if err := client.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	backend, err := redis_cache.NewRedisCache(redis_cache.RedisCacheOpts{Client: client, ClientCloser: client, ClientTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p := newTestCache(false)
	p.backend.Close()
	p.backend = backend
	defer backend.Close()

	keys := map[string]string{}
	long := strings.Repeat("x", 42) + ".example.com."
	for _, name := range []string{"example.com.", "WWW.example.com.", "example.net.", "aexample.com.", long} {
		keys[name] = storeTestAnswer(t, p, name, dns.TypeA)
	}
	for i := 0; i < 2500; i++ { // more than one SCAN batch
		storeTestAnswer(t, p, "filler"+strconv.Itoa(i)+".example.org.", dns.TypeA)
	}
	n, err := p.PurgeDomain(context.Background(), long, false)
	if err != nil || n != 1 {
		t.Fatalf("purge %s = %d, %v", long, n, err)
	}
	n, err = p.PurgeDomain(context.Background(), "example.com", true)
	if err != nil || n != 2 {
		t.Fatalf("purge example.com = %d, %v", n, err)
	}
	checkPurged(t, p, keys, "example.com.", "WWW.example.com.", long)
}
