package configuration

import (
	"context"
	"crypto/tls"
	"events-stocks/models"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

// validateRedisAddress rejects a malformed endpoint before a deployment starts
// a candidate container. go-redis requires an explicit host:port address;
// accepting a hostname alone would otherwise fail only after the container is
// running and consume the release health-gate timeout.
func validateRedisAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("REDIS_HOST must be a non-empty host:port address")
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("REDIS_HOST port must be between 1 and 65535")
	}

	return nil
}

func InicializarRedis(cfg *models.Config) {
	if err := validateRedisAddress(cfg.RedisHost); err != nil {
		slog.Error("invalid Redis address", "error", err)
		os.Exit(1)
	}

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
