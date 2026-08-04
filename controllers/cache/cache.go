package cache

import (
	"context"
	"fmt"

	"events-stocks/internal/authz"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"net/http"
)

type adminCacheStore interface {
	DeleteKey(ctx context.Context, key string) error
	FlushAll(ctx context.Context) error
}

var store adminCacheStore

func InitCacheController(cacheStore adminCacheStore) {
	store = cacheStore
}

func requireStore() (adminCacheStore, error) {
	if store == nil {
		return nil, fmt.Errorf("cache store not configured")
	}
	return store, nil
}

func FlushKey(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}

	cacheStore, err := requireStore()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Cache unavailable", err.Error())
	}

	key := c.Param("key")
	if key == "" {
		return utils.Error(c, http.StatusBadRequest, "Key is required", "")
	}

	if err := cacheStore.DeleteKey(c.Request().Context(), key); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to delete cache key", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Cache key deleted", map[string]string{"key": key})
}

func FlushAll(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}

	cacheStore, err := requireStore()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Cache unavailable", err.Error())
	}

	if err := cacheStore.FlushAll(c.Request().Context()); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to flush cache", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Cache flushed successfully", nil)
}
