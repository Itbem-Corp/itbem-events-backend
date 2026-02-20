package configuration

import (
	"context"
	"crypto/tls"
	"events-stocks/models"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

func InicializarRedis(cfg *models.Config) {
	redisDb, _ := strconv.Atoi(cfg.RedisDb)

	options := &redis.Options{
		Addr:     cfg.RedisHost,
		Password: cfg.RedisPassword,
		DB:       redisDb,
	}

	// Habilita TLS solo si lo defines en tu configuración
	if cfg.RedisTls == "true" {
		options.TLSConfig = &tls.Config{}
	}

	RedisClient = redis.NewClient(options)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := RedisClient.Ping(ctx).Result()
	if err != nil {
		slog.Error("redis connection failed", "error", err)
		os.Exit(1)
	}

	slog.Info("redis connected")
}
