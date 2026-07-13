package redisrepository

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"events-stocks/configuration"
	"github.com/redis/go-redis/v9"
)

// BenchmarkDeleteKeysByPatternRedis measures the real network path against a
// disposable key prefix. It is opt-in so the regular test suite never depends
// on a locally running Redis/Valkey instance.
//
// Run with:
//
//	REDIS_BENCH_ADDR=127.0.0.1:16379 go test ./repositories/redisrepository \
//		-run '^$' -bench '^BenchmarkDeleteKeysByPatternRedis$' -benchtime=5x
func BenchmarkDeleteKeysByPatternRedis(b *testing.B) {
	address := os.Getenv("REDIS_BENCH_ADDR")
	if address == "" {
		b.Skip("set REDIS_BENCH_ADDR to a disposable Redis/Valkey instance")
	}

	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		b.Fatalf("ping benchmark Redis: %v", err)
	}

	previousClient := configuration.RedisClient
	configuration.RedisClient = client
	defer func() { configuration.RedisClient = previousClient }()

	const keyCount = 2_048
	prefix := fmt.Sprintf("eventi:bench:delete-pattern:%d:", os.Getpid())
	pattern := prefix + "*"
	payload := strings.Repeat("x", 64)

	defer func() {
		keys, err := scanAllKeys(ctx, client, pattern)
		if err == nil && len(keys) > 0 {
			_ = client.Unlink(ctx, keys...).Err()
		}
	}()

	b.ReportMetric(keyCount, "keys/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		pipe := client.Pipeline()
		for keyIndex := 0; keyIndex < keyCount; keyIndex++ {
			pipe.Set(ctx, fmt.Sprintf("%s%d", prefix, keyIndex), payload, 0)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			b.Fatalf("seed benchmark keys: %v", err)
		}
		b.StartTimer()

		if err := DeleteKeysByPattern(ctx, pattern); err != nil {
			b.Fatalf("delete benchmark keys: %v", err)
		}

		b.StopTimer()
		remaining, err := scanAllKeys(ctx, client, pattern)
		if err != nil {
			b.Fatalf("verify benchmark deletion: %v", err)
		}
		if len(remaining) != 0 {
			b.Fatalf("%d benchmark keys survived pattern deletion", len(remaining))
		}
		b.StartTimer()
	}
}

func scanAllKeys(ctx context.Context, client *redis.Client, pattern string) ([]string, error) {
	var (
		cursor uint64
		keys   []string
	)
	for {
		batch, nextCursor, err := client.Scan(ctx, cursor, pattern, 512).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		cursor = nextCursor
		if cursor == 0 {
			return keys, nil
		}
	}
}
