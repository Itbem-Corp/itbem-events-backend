package health

import (
	"context"
	"events-stocks/configuration"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var processStart time.Time
var processStartOnce sync.Once

// MarkProcessStart records the monotonic start time before serving requests.
func MarkProcessStart() {
	processStartOnce.Do(func() { processStart = time.Now() })
}

func Check(c echo.Context) error {
	return check(c, processStart, time.Now, func() error {
		sqlDB, err := configuration.DB.DB()
		if err != nil {
			return err
		}
		return sqlDB.Ping()
	}, func(ctx context.Context) error {
		return configuration.RedisClient.Ping(ctx).Err()
	})
}

func check(c echo.Context, start time.Time, now func() time.Time, pingDB func() error, pingRedis func(context.Context) error) error {
	current := now()
	uptime := current.Sub(start) / time.Second
	if uptime < 0 {
		uptime = 0
	}
	result := map[string]interface{}{
		"status":          "ok",
		"environment":     healthEnvironment(os.Getenv("ENV")),
		"source_revision": strings.TrimSpace(os.Getenv("SOURCE_REVISION")),
		"time":            current.UTC().Format(time.RFC3339),
		"uptimeSeconds":   uptime,
	}

	// Verificar PostgreSQL
	if err := pingDB(); err != nil {
		result["status"] = "degraded"
		result["db"] = "unreachable"
	} else {
		result["db"] = "ok"
	}

	// Verificar Redis/Valkey
	if err := pingRedis(c.Request().Context()); err != nil {
		result["status"] = "degraded"
		result["redis"] = "unreachable"
	} else {
		result["redis"] = "ok"
	}

	status := http.StatusOK
	if result["status"] == "degraded" {
		status = http.StatusServiceUnavailable
	}

	return utils.Success(c, status, "Health check", result)
}

func healthEnvironment(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "production", "prod":
		return "production"
	case "staging", "stage":
		return "staging"
	case "local", "development", "dev", "test":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}
