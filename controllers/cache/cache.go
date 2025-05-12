package cache

import (
	"context"
	"events-stocks/services/redisService"
	"github.com/labstack/echo/v4"
	"net/http"
)

func FlushKey(c echo.Context) error {
	key := c.Param("key")
	if key == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Key is required"})
	}

	err := redisService.DeleteKey(context.Background(), key)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to delete key"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "Cache key deleted", "key": key})
}

func FlushAll(c echo.Context) error {
	err := redisService.FlushAll(context.Background())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to flush Redis"})
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Redis cache flushed completely"})
}
