package redis_cache

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
)

type pingCounter struct{ n atomic.Int64 }

func (p *pingCounter) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	if cmd.Name() == "ping" {
		p.n.Add(1)
	}
	return ctx, nil
}
func (p *pingCounter) AfterProcess(context.Context, redis.Cmder) error { return nil }
func (p *pingCounter) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (p *pingCounter) AfterProcessPipeline(context.Context, []redis.Cmder) error { return nil }

// unreachableCache returns a cache whose client always fails to connect.
func unreachableCache(t *testing.T) (*RedisCache, *pingCounter) {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 50 * time.Millisecond,
		MaxRetries:  -1,
	})
	pings := &pingCounter{}
	client.AddHook(pings)
	c, err := NewRedisCache(RedisCacheOpts{Client: client, ClientCloser: client, ClientTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return c, pings
}

func TestCloseStopsRecovery(t *testing.T) {
	c, pings := unreachableCache(t)
	c.Get("k") // fails and starts the recovery task
	if !c.disabled() {
		t.Fatal("cache should be disabled after a client error")
	}
	deadline := time.Now().Add(2 * time.Second)
	for pings.n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pings.n.Load() == 0 {
		t.Fatal("recovery task never pinged")
	}

	// The task now sleeps at least a second before its next ping; Close
	// must cancel that wait instead of riding it out.
	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("Close waited %v for the recovery task", d)
	}
	after := pings.n.Load()
	time.Sleep(1500 * time.Millisecond)
	if got := pings.n.Load(); got != after {
		t.Fatalf("pings after Close: %d, want %d", got, after)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestClosedCacheShortCircuits(t *testing.T) {
	c, pings := unreachableCache(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := c.Get("k"); v != nil {
		t.Fatal("Get after Close returned a value")
	}
	c.Store("k", []byte("v"), time.Now(), time.Now().Add(time.Minute))
	c.disableClient()
	time.Sleep(300 * time.Millisecond)
	if n := pings.n.Load(); n != 0 {
		t.Fatalf("closed cache pinged %d times", n)
	}
}

func TestConcurrentCloseAndFailure(t *testing.T) {
	for i := 0; i < 20; i++ {
		c, _ := unreachableCache(t)
		done := make(chan struct{})
		go func() {
			c.Get("k")
			close(done)
		}()
		_ = c.Close()
		<-done
		c.recoveryWG.Wait()
	}
}

// With a reachable Redis the recovery task re-enables the client, and the
// cache reads and writes again.
func TestRecoveryReenablesTheClient(t *testing.T) {
	url := os.Getenv("MOSDNS_TEST_REDIS_URL")
	if url == "" {
		t.Skip("MOSDNS_TEST_REDIS_URL is not set")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opt)
	c, err := NewRedisCache(RedisCacheOpts{Client: client, ClientCloser: client})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.disableClient()
	deadline := time.Now().Add(3 * time.Second)
	for c.disabled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.disabled() {
		t.Fatal("client not re-enabled")
	}
	key := "mosdns-test-recovery-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	c.Store(key, []byte("v"), time.Now(), time.Now().Add(time.Minute))
	if v, _, _ := c.Get(key); string(v) != "v" {
		t.Fatalf("Get = %q", v)
	}
	c.recoveryWG.Wait()
}
