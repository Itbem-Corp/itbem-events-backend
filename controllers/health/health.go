package health

import (
	"events-stocks/configuration"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
	"os"
	"strings"
	"time"
)

func Check(c echo.Context) error {
	result := map[string]interface{}{
		"status":      "ok",
		"environment": healthEnvironment(os.Getenv("ENV")),
		"time":        time.Now().UTC().Format(time.RFC3339),
	}

	// Verificar PostgreSQL
	if sqlDB, err := configuration.DB.DB(); err != nil || sqlDB.Ping() != nil {
		result["status"] = "degraded"
		result["db"] = "unreachable"
	} else {
		result["db"] = "ok"
	}

	// Verificar Redis/Valkey
	if err := configuration.RedisClient.Ping(c.Request().Context()).Err(); err != nil {
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
