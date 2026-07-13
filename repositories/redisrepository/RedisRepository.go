package redisrepository

import (
	"context"
	"events-stocks/configuration"
	"time"
)

const (
	deletePatternScanCount       int64 = 512
	deletePatternUnlinkBatchSize       = 512
)

type scanPatternKeys func(context.Context, uint64, string, int64) ([]string, uint64, error)
type unlinkPatternKeys func(context.Context, ...string) error

func SaveKey(ctx context.Context, clave string, valor string, expiracion time.Duration) error {
	return configuration.RedisClient.Set(ctx, clave, valor, expiracion).Err()
}

func GetKey(ctx context.Context, clave string) (string, error) {
	valor, err := configuration.RedisClient.Get(ctx, clave).Result()
	if err != nil {
		return "", err
	}
	return valor, nil
}

func Increment(ctx context.Context, clave string) (int64, error) {
	return configuration.RedisClient.Incr(ctx, clave).Result()
}

const decrementCounterScript = `
local current = redis.call('GET', KEYS[1])
if not current then
  return 0
end
current = tonumber(current) or 0
if current <= 1 then
  redis.call('DEL', KEYS[1])
  return 0
end
return redis.call('DECR', KEYS[1])
`

// Decrement atomically releases a reserved upload slot without ever creating
// a missing key or allowing a counter to become negative.
func Decrement(ctx context.Context, clave string) (int64, error) {
	return configuration.RedisClient.Eval(ctx, decrementCounterScript, []string{clave}).Int64()
}

func Expire(ctx context.Context, clave string, ttl time.Duration) error {
	return configuration.RedisClient.Expire(ctx, clave, ttl).Err()
}

func DeleteKey(ctx context.Context, clave string) error {
	return configuration.RedisClient.Del(ctx, clave).Err()
}

func ExistKey(ctx context.Context, clave string) (bool, error) {
	existe, err := configuration.RedisClient.Exists(ctx, clave).Result()
	if err != nil {
		return false, err
	}
	return existe > 0, nil
}

func FlushAll(ctx context.Context) error {
	return configuration.RedisClient.FlushAll(ctx).Err()
}

func DeleteKeysByPattern(ctx context.Context, pattern string) error {
	return deleteKeysByPattern(
		ctx,
		pattern,
		func(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
			return configuration.RedisClient.Scan(ctx, cursor, match, count).Result()
		},
		func(ctx context.Context, keys ...string) error {
			return configuration.RedisClient.Unlink(ctx, keys...).Err()
		},
	)
}

// deleteKeysByPattern keeps invalidation work bounded: SCAN results are removed
// with batched, non-blocking UNLINK commands instead of one synchronous DEL
// round trip per key. SCAN remains incremental, so broad invalidations do not
// materialize the complete keyspace in application memory. The explicit slice
// cap matters because Redis documents COUNT as a hint, not a hard batch limit.
func deleteKeysByPattern(ctx context.Context, pattern string, scan scanPatternKeys, unlink unlinkPatternKeys) error {
	var cursor uint64
	for {
		keys, nextCursor, err := scan(ctx, cursor, pattern, deletePatternScanCount)
		if err != nil {
			return err
		}
		for start := 0; start < len(keys); start += deletePatternUnlinkBatchSize {
			end := min(start+deletePatternUnlinkBatchSize, len(keys))
			if err := unlink(ctx, keys[start:end]...); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			return nil
		}
	}
}

func Invalidate(resource string, key string) error {
	ctx := context.Background()
	redisKey := key + ":" + resource
	return DeleteKey(ctx, redisKey)
}

func InvalidateByPattern(pattern string) error {
	ctx := context.Background()
	return DeleteKeysByPattern(ctx, pattern)
}

// RedisRepo implements ports.CacheRepository using the global Redis client.
type RedisRepo struct{}

func NewRedisRepo() *RedisRepo { return &RedisRepo{} }

func (r *RedisRepo) Invalidate(resource string, key string) error {
	return Invalidate(resource, key)
}

func (r *RedisRepo) DeleteKeysByPattern(ctx context.Context, pattern string) error {
	return DeleteKeysByPattern(ctx, pattern)
}

func (r *RedisRepo) GetKey(ctx context.Context, key string) (string, error) {
	return GetKey(ctx, key)
}

func (r *RedisRepo) Increment(ctx context.Context, key string) (int64, error) {
	return Increment(ctx, key)
}

func (r *RedisRepo) Decrement(ctx context.Context, key string) (int64, error) {
	return Decrement(ctx, key)
}

func (r *RedisRepo) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return Expire(ctx, key, ttl)
}

func (r *RedisRepo) SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error {
	return SaveKey(ctx, key, value, ttl)
}

func (r *RedisRepo) DeleteKey(ctx context.Context, key string) error {
	return DeleteKey(ctx, key)
}

func (r *RedisRepo) FlushAll(ctx context.Context) error {
	return FlushAll(ctx)
}
