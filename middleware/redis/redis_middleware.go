package redisMiddleware

import (
	"context"
	cacheLoader "events-stocks/services/cacheLoaderService"
	"events-stocks/services/redisService"
	"github.com/labstack/echo/v4"
	"strings"
	"time"
)

func RetrieveCache(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := context.Background()

		keyParam := c.Param("key") // "all", "page1", etc.
		routePath := c.Path()      // "/api/events/:key"

		// Extraer "events" desde el path
		var resource string
		if strings.HasPrefix(routePath, "/api/") {
			parts := strings.Split(routePath, "/")
			if len(parts) >= 3 {
				resource = parts[2] // "events"
			}
		}

		if resource == "" || keyParam == "" {
			return next(c)
		}

		// ✅ Clave final: "all:events", "page1:events", etc.
		redisKey := keyParam + ":" + resource

		// Buscar en Redis
		data, err := redisService.GetKey(ctx, redisKey)
		if err != nil {
			loaderFunc, exists := cacheLoader.GetLoader(redisKey)
			if !exists {
				return next(c)
			}

			data, err = loaderFunc()
			if err != nil {
				return next(c)
			}

			_ = redisService.SaveKey(ctx, redisKey, data, 600*time.Second)
		}

		// Guardar en el contexto con la misma clave
		c.Set(redisKey, data)

		return next(c)
	}
}
