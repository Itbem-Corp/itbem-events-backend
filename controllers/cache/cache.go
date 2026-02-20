package cache

import (
	"context"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
)

func FlushKey(c echo.Context) error {
	key := c.Param("key")
	if key == "" {
		return utils.Error(c, http.StatusBadRequest, "Key is required", "")
	}

	if err := redisrepository.DeleteKey(context.Background(), key); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to delete cache key", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Cache key deleted", map[string]string{"key": key})
}

func FlushAll(c echo.Context) error {
	if err := redisrepository.FlushAll(context.Background()); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to flush cache", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Cache flushed successfully", nil)
}
