package redisrepository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"events-stocks/configuration"
	"github.com/redis/go-redis/v9"
)

// TestValkeyCompatibility exercises every cache command EventiApp uses against
// a real Valkey server. It is opt-in locally and runs before the controlled
// ElastiCache engine migration.
func TestValkeyCompatibility(t *testing.T) {
	address := os.Getenv("VALKEY_INTEGRATION_ADDR")
	if address == "" {
		t.Skip("set VALKEY_INTEGRATION_ADDR to run against a disposable Valkey server")
	}

	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping Valkey: %v", err)
	}

	previous := configuration.RedisClient
	configuration.RedisClient = client
	t.Cleanup(func() { configuration.RedisClient = previous })

	prefix := fmt.Sprintf("eventi:valkey:test:%d:", os.Getpid())
	t.Cleanup(func() { _ = DeleteKeysByPattern(ctx, prefix+"*") })

	if err := SaveKey(ctx, prefix+"value", "premium", time.Minute); err != nil {
		t.Fatalf("SET with TTL: %v", err)
	}
	value, err := GetKey(ctx, prefix+"value")
	if err != nil || value != "premium" {
		t.Fatalf("GET = %q, %v; want premium, nil", value, err)
	}
	if err := Expire(ctx, prefix+"value", 2*time.Minute); err != nil {
		t.Fatalf("EXPIRE: %v", err)
	}
	if exists, err := ExistKey(ctx, prefix+"value"); err != nil || !exists {
		t.Fatalf("EXISTS = %v, %v; want true, nil", exists, err)
	}
	if count, err := Increment(ctx, prefix+"counter"); err != nil || count != 1 {
		t.Fatalf("INCR = %d, %v; want 1, nil", count, err)
	}
	if count, err := Decrement(ctx, prefix+"counter"); err != nil || count != 0 {
		t.Fatalf("Lua decrement = %d, %v; want 0, nil", count, err)
	}
	if err := SaveKey(ctx, prefix+"pattern:a", "a", time.Minute); err != nil {
		t.Fatalf("seed pattern a: %v", err)
	}
	if err := SaveKey(ctx, prefix+"pattern:b", "b", time.Minute); err != nil {
		t.Fatalf("seed pattern b: %v", err)
	}
	if err := DeleteKeysByPattern(ctx, prefix+"pattern:*"); err != nil {
		t.Fatalf("SCAN/UNLINK invalidation: %v", err)
	}
	if exists, err := ExistKey(ctx, prefix+"pattern:a"); err != nil || exists {
		t.Fatalf("UNLINK result = %v, %v; want false, nil", exists, err)
	}
	if err := DeleteKey(ctx, prefix+"value"); err != nil {
		t.Fatalf("DEL: %v", err)
	}
	if err := FlushAll(ctx); err != nil {
		t.Fatalf("FLUSHALL: %v", err)
	}
}
