package redisMiddleware

import (
	"context"
	"events-stocks/configuration/constants"
	cacheLoader "events-stocks/repositories/cacheloaderrepository"
	"events-stocks/repositories/redisrepository"
	"github.com/labstack/echo/v4"
	"strings"
	"time"
)

var cacheTTLs = map[string]time.Duration{
	"events":        constants.ShortTimeTTL,
	"fontsets":      constants.LargeTimeTTL,
	"colorpalettes": constants.MediumTimeTTL,
	"resourcetypes": constants.XLongTimeTTL,
}

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
		data, err := redisrepository.GetKey(ctx, redisKey)
		if err != nil {
			loaderFunc, exists := cacheLoader.GetLoader(redisKey)
			if !exists {
				return next(c)
			}

			data, err = loaderFunc()
			if err != nil {
				return next(c)
			}

			ttl := cacheTTLs[resource] // resource ya lo defines antes de armar la clave
			if ttl == 0 {
				ttl = constants.ShortTimeTTL // default en caso de que no esté mapeado
			}
			_ = redisrepository.SaveKey(ctx, redisKey, data, ttl)

		}

		// Guardar en el contexto con la misma clave
		c.Set(redisKey, data)

		return next(c)
	}
}
