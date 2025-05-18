package redisrepository

import (
	"context"
	"events-stocks/configuration"
	"time"
)

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
	iter := configuration.RedisClient.Scan(ctx, 0, pattern, 0).Iterator()
	for iter.Next(ctx) {
		if err := configuration.RedisClient.Del(ctx, iter.Val()).Err(); err != nil {
			return err
		}
	}
	return iter.Err()
}
