/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package redis_cache

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/cache"
	"github.com/pmkol/mosdns-x/pkg/pool"
	"github.com/pmkol/mosdns-x/pkg/utils"
)

var nopLogger = zap.NewNop()

type RedisCacheOpts struct {
	// Client cannot be nil.
	Client redis.Cmdable

	// ClientCloser closes Client when RedisCache.Close is called.
	// Optional.
	ClientCloser io.Closer

	// ClientTimeout specifies the timeout for read and write operations.
	// Default is 1s. Cache I/O sits on the DNS reply path, so deployments
	// should set it from the measured Redis RTT.
	ClientTimeout time.Duration

	// Logger is the *zap.Logger for this RedisCache.
	// A nil Logger will disable logging.
	Logger *zap.Logger
}

func (opts *RedisCacheOpts) Init() error {
	if opts.Client == nil {
		return errors.New("nil client")
	}
	utils.SetDefaultNum(&opts.ClientTimeout, time.Second)
	if opts.Logger == nil {
		opts.Logger = nopLogger
	}
	return nil
}

type RedisCache struct {
	opts           RedisCacheOpts
	clientDisabled uint32

	// lifecycleMu orders closed against recoveryWG.Add so Close never
	// waits while a new recovery task can still start.
	lifecycleMu    sync.Mutex
	closed         atomic.Bool
	recoveryCtx    context.Context
	cancelRecovery context.CancelFunc
	recoveryWG     sync.WaitGroup
	closeOnce      sync.Once
	closeErr       error
}

func NewRedisCache(opts RedisCacheOpts) (*RedisCache, error) {
	if err := opts.Init(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &RedisCache{
		opts:           opts,
		recoveryCtx:    ctx,
		cancelRecovery: cancel,
	}, nil
}

func (r *RedisCache) disabled() bool {
	return r.closed.Load() || atomic.LoadUint32(&r.clientDisabled) != 0
}

func (r *RedisCache) disableClient() {
	if !atomic.CompareAndSwapUint32(&r.clientDisabled, 0, 1) {
		return
	}
	r.lifecycleMu.Lock()
	if r.closed.Load() {
		r.lifecycleMu.Unlock()
		return
	}
	r.recoveryWG.Add(1)
	r.lifecycleMu.Unlock()
	r.opts.Logger.Warn("redis temporarily disabled")
	go r.recoverClient()
}

// recoverClient pings Redis with a jittered backoff until it answers or
// the cache is closed.
func (r *RedisCache) recoverClient() {
	defer r.recoveryWG.Done()
	const maxBackoff = time.Second * 30
	backoff := time.Millisecond * 100
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	for {
		select {
		case <-r.recoveryCtx.Done():
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(r.recoveryCtx, time.Millisecond*500)
		err := r.opts.Client.Ping(ctx).Err()
		cancel()
		if err == nil {
			atomic.StoreUint32(&r.clientDisabled, 0)
			return
		}
		if r.recoveryCtx.Err() != nil {
			return
		}
		backoff = min(backoff+time.Duration(rand.Intn(1000))*time.Millisecond+time.Second, maxBackoff)
		r.opts.Logger.Warn("redis ping failed", zap.Error(err), zap.Duration("next_ping", backoff))
		timer.Reset(backoff)
	}
}

func (r *RedisCache) Get(key string) (v []byte, storedTime, expirationTime time.Time) {
	if r.disabled() {
		return nil, time.Time{}, time.Time{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.opts.ClientTimeout)
	defer cancel()
	b, err := r.opts.Client.Get(ctx, key).Bytes()
	if err != nil {
		if err != redis.Nil {
			r.opts.Logger.Warn("redis get", zap.Error(err))
			r.disableClient()
		}
		return nil, time.Time{}, time.Time{}
	}

	storedTime, expirationTime, m, err := unpackRedisValue(b)
	if err != nil {
		r.opts.Logger.Warn("redis data unpack error", zap.Error(err))
		return nil, time.Time{}, time.Time{}
	}
	return m, storedTime, expirationTime
}

// Store stores kv into redis.
func (r *RedisCache) Store(key string, v []byte, storedTime, expirationTime time.Time) {
	if r.disabled() {
		return
	}

	now := time.Now()
	ttl := expirationTime.Sub(now)
	if ttl <= 0 { // For redis, zero ttl means the key has no expiration time.
		return
	}

	data := packRedisData(storedTime, expirationTime, v)
	defer data.Release()
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.ClientTimeout)
	defer cancel()
	if err := r.opts.Client.Set(ctx, key, data.Bytes(), ttl).Err(); err != nil {
		r.opts.Logger.Warn("redis set", zap.Error(err))
		r.disableClient()
	}
}

type KV struct {
	Key            string
	V              []byte
	StoreTime      time.Time
	ExpirationTime time.Time
}

// BatchStore stores a batch of kv into redis via redis pipeline.
func (r *RedisCache) BatchStore(b []KV) {
	if r.disabled() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.opts.ClientTimeout)
	defer cancel()
	pipeline := r.opts.Client.Pipeline()
	buffers := make([]*pool.Buffer, 0, len(b))
	for _, kv := range b {
		now := time.Now()
		ttl := kv.ExpirationTime.Sub(now)
		if ttl <= 0 {
			continue
		}

		data := packRedisData(kv.StoreTime, kv.ExpirationTime, kv.V)
		buffers = append(buffers, data)
		pipeline.Set(ctx, kv.Key, data.Bytes(), ttl)
	}

	if _, err := pipeline.Exec(ctx); err != nil {
		r.opts.Logger.Warn("redis pipeline set", zap.Error(err))
		r.disableClient()
	}
	for _, buffer := range buffers {
		buffer.Release()
	}
}

// Close stops the recovery task and closes the redis client. Later calls
// return the first result.
func (r *RedisCache) Close() error {
	r.closeOnce.Do(func() {
		r.lifecycleMu.Lock()
		r.closed.Store(true)
		r.cancelRecovery()
		r.lifecycleMu.Unlock()
		r.recoveryWG.Wait()
		if f := r.opts.ClientCloser; f != nil {
			r.closeErr = f.Close()
		}
	})
	return r.closeErr
}

// purgeScanCount is the SCAN batch size. MATCH is applied server side to
// each batch, so a larger count only cuts round trips.
const purgeScanCount = 5000

// RedisGlobber is implemented by selectors that can narrow the SCAN with a
// Redis glob that every key they accept satisfies.
type RedisGlobber interface {
	RedisGlob() string
}

// Purge scans the keys and deletes those selector accepts. A selector that
// implements RedisGlobber filters the scan server side. Each round trip
// gets the client timeout; ctx bounds the whole scan.
func (r *RedisCache) Purge(ctx context.Context, selector cache.KeySelector) (int, error) {
	if r.disabled() {
		return 0, errors.New("redis is temporarily unavailable")
	}
	glob := "*"
	if g, ok := selector.(RedisGlobber); ok {
		glob = g.RedisGlob()
	}
	removed := 0
	var cursor uint64
	for {
		scanCtx, cancel := context.WithTimeout(ctx, r.opts.ClientTimeout)
		keys, next, err := r.opts.Client.Scan(scanCtx, cursor, glob, purgeScanCount).Result()
		cancel()
		if err != nil {
			return removed, fmt.Errorf("redis scan: %w", err)
		}
		selected := keys[:0]
		for _, key := range keys {
			if selector.Match(key) {
				selected = append(selected, key)
			}
		}
		if len(selected) > 0 {
			delCtx, cancel := context.WithTimeout(ctx, r.opts.ClientTimeout)
			n, err := r.opts.Client.Del(delCtx, selected...).Result()
			cancel()
			removed += int(n)
			if err != nil {
				return removed, fmt.Errorf("redis del: %w", err)
			}
		}
		if next == 0 {
			return removed, nil
		}
		cursor = next
	}
}

func (r *RedisCache) Len() int {
	if r.closed.Load() {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*50)
	defer cancel()
	i, err := r.opts.Client.DBSize(ctx).Result()
	if err != nil {
		r.opts.Logger.Error("dbsize", zap.Error(err))
		return 0
	}
	return int(i)
}

// packRedisData packs storedTime, expirationTime and v into one byte slice.
// The returned []byte should be released by pool.ReleaseBuf().
func packRedisData(storedTime, expirationTime time.Time, v []byte) *pool.Buffer {
	buf := pool.GetBuf(8 + 8 + len(v))
	b := buf.Bytes()
	binary.BigEndian.PutUint64(b[:8], uint64(storedTime.Unix()))
	binary.BigEndian.PutUint64(b[8:16], uint64(expirationTime.Unix()))
	copy(b[16:], v)
	return buf
}

func unpackRedisValue(b []byte) (storedTime, expirationTime time.Time, v []byte, err error) {
	if len(b) < 16 {
		return time.Time{}, time.Time{}, nil, errors.New("b is too short")
	}
	storedTime = time.Unix(int64(binary.BigEndian.Uint64(b[:8])), 0)
	expirationTime = time.Unix(int64(binary.BigEndian.Uint64(b[8:16])), 0)
	return storedTime, expirationTime, b[16:], nil
}
