package configuration

import (
	"context"
	"crypto/tls"
	"events-stocks/models"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

func InicializarRedis(cfg *models.Config) {
	redisDb, _ := strconv.Atoi(cfg.RedisDb)

	RedisClient = redis.NewClient(&redis.Options{
		Addr:      cfg.RedisHost,     // ← ahora toma del cfg
		Password:  cfg.RedisPassword, // ← ahora toma del cfg
		DB:        redisDb,
		TLSConfig: &tls.Config{},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := RedisClient.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("Error al conectar con Redis: %v", err)
	}

	log.Println("Conectado a Redis")
}
