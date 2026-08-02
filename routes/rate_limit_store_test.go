package routes

import (
	"testing"
	"time"

	"events-stocks/configuration"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type fixedFallbackStore struct{ allowed bool }

func (store fixedFallbackStore) Allow(string) (bool, error) { return store.allowed, nil }

func TestDistributedRateLimitIsSharedAcrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	previous := configuration.RedisClient
	configuration.RedisClient = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = configuration.RedisClient.Close()
		configuration.RedisClient = previous
	})

	first := newDistributedRateLimitStore("test", 2, time.Minute, true, fixedFallbackStore{allowed: true})
	second := newDistributedRateLimitStore("test", 2, time.Minute, true, fixedFallbackStore{allowed: true})

	allowed, err := first.Allow("203.0.113.8")
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = second.Allow("203.0.113.8")
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = first.Allow("203.0.113.8")
	require.NoError(t, err)
	require.False(t, allowed)

	for _, key := range server.Keys() {
		require.NotContains(t, key, "203.0.113.8")
	}
}

func TestSensitiveRateLimitFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	server := miniredis.RunT(t)
	previous := configuration.RedisClient
	configuration.RedisClient = redis.NewClient(&redis.Options{Addr: server.Addr()})
	server.Close()
	t.Cleanup(func() {
		_ = configuration.RedisClient.Close()
		configuration.RedisClient = previous
	})

	store := newDistributedRateLimitStore("sensitive-test", 5, time.Minute, true, fixedFallbackStore{allowed: true})
	allowed, err := store.Allow("203.0.113.9")
	require.NoError(t, err)
	require.False(t, allowed)
}
