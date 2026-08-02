package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"

	"events-stocks/configuration"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"
)

var fixedWindowScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return current
`)

type distributedRateLimitStore struct {
	name       string
	limit      int64
	window     time.Duration
	failClosed bool
	fallback   middleware.RateLimiterStore
}

func newDistributedRateLimitStore(
	name string,
	limit int64,
	window time.Duration,
	failClosed bool,
	fallback middleware.RateLimiterStore,
) middleware.RateLimiterStore {
	return &distributedRateLimitStore{
		name:       strings.TrimSpace(name),
		limit:      limit,
		window:     window,
		failClosed: failClosed,
		fallback:   fallback,
	}
}

func (store *distributedRateLimitStore) Allow(identifier string) (bool, error) {
	client := configuration.RedisClient
	if client == nil {
		return store.fallback.Allow(identifier)
	}

	digest := sha256.Sum256([]byte(strings.TrimSpace(identifier)))
	key := "security:rate-limit:" + store.name + ":" + hex.EncodeToString(digest[:])
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	current, err := fixedWindowScript.Run(ctx, client, []string{key}, store.window.Milliseconds()).Int64()
	if err == nil {
		return current <= store.limit, nil
	}

	slog.Error("distributed rate limiter unavailable", "limiter", store.name, "error", err)
	if store.failClosed {
		return false, nil
	}
	return store.fallback.Allow(identifier)
}
