package service

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Tencent/WeKnora/internal/logger"
)

// ssoReplayCache remembers consumed SSO jti values so a token can only be
// exchanged once. Implementations must be safe for concurrent use.
type ssoReplayCache interface {
	// Consume records the jti and reports whether it was seen before.
	Consume(jti string, now time.Time) bool
}

// memorySSOReplayCache is the single-instance fallback. Entries are pruned
// lazily; only a process-local map, so multi-replica deployments must run
// with REDIS_ADDR set to get the Redis implementation.
type memorySSOReplayCache struct {
	mu   sync.Mutex
	seen map[string]int64
	ttl  time.Duration
}

func newMemorySSOReplayCache(ttl time.Duration) *memorySSOReplayCache {
	return &memorySSOReplayCache{seen: map[string]int64{}, ttl: ttl}
}

func (c *memorySSOReplayCache) Consume(jti string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := now.Add(-c.ttl).Unix()
	for key, expiry := range c.seen {
		if expiry < cutoff {
			delete(c.seen, key)
		}
	}
	if _, dup := c.seen[jti]; dup {
		return false
	}
	c.seen[jti] = now.Add(c.ttl).Unix()
	return true
}

// redisSSOReplayCache stores jti keys with SETNX + TTL, so replay protection
// is shared across replicas and survives restarts.
type redisSSOReplayCache struct {
	client *redis.Client
	ttl    time.Duration
}

func newRedisSSOReplayCache(addr, password string, db int, ttl time.Duration) *redisSSOReplayCache {
	return &redisSSOReplayCache{
		client: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		}),
		ttl: ttl,
	}
}

func (c *redisSSOReplayCache) Consume(jti string, now time.Time) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ok, err := c.client.SetNX(ctx, "sso:jti:"+jti, 1, c.ttl).Result()
	if err != nil {
		// Fail closed: an unavailable Redis must not open a replay window.
		logger.Errorf(ctx, "[sso] replay cache unavailable, rejecting jti %s: %v", jti, err)
		return false
	}
	return ok
}

// ssoReplayTTL bounds how long a consumed jti is remembered. It only has to
// outlive the token itself, plus clock skew.
const ssoReplayTTL = 10 * time.Minute

func envNonEmpty(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func envInt(key string) int {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return 0
}

// newSSOReplayCacheFromEnv picks the Redis implementation when REDIS_ADDR is
// configured, otherwise the in-memory one. Redis connection problems surface
// at exchange time (fail closed), not here.
func newSSOReplayCacheFromEnv() ssoReplayCache {
	if addr := envNonEmpty("REDIS_ADDR"); addr != "" {
		return newRedisSSOReplayCache(
			addr,
			envNonEmpty("REDIS_PASSWORD"),
			envInt("REDIS_DB"),
			ssoReplayTTL,
		)
	}
	return newMemorySSOReplayCache(ssoReplayTTL)
}
