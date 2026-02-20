package redisMiddleware

import (
	cacheLoader "events-stocks/repositories/cacheloaderrepository"
	"github.com/labstack/echo/v4"
	"strings"
)

func RetrieveCache(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		key := c.Param("key")
		subkey := c.Param("subkey")
		path := c.Path()

		parts := strings.Split(strings.Trim(path, "/"), "/")

		var resource string
		for i, part := range parts {
			if part == "api" && len(parts) > i+1 {
				resource = parts[i+1]
				break
			} else if part != "api" {
				resource = part
				break
			}
		}

		cacheKey := key
		if subkey != "" {
			cacheKey = key + ":" + subkey
		}

		if resource == "" || cacheKey == "" {
			return next(c)
		}

		data, err := cacheLoader.CacheOrLoadAuto(resource, cacheKey)
		if err == nil {
			c.Set(cacheKey+":"+resource, data)
		}

		return next(c)
	}
}
