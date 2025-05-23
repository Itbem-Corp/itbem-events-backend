package redisMiddleware

import (
	cacheLoader "events-stocks/repositories/cacheloaderrepository"
	"github.com/labstack/echo/v4"
	"strings"
)

func RetrieveCache(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		key := c.Param("key")
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

		if resource == "" || key == "" {
			return next(c)
		}

		data, err := cacheLoader.CacheOrLoadAuto(resource, key)
		if err == nil {
			c.Set(key+":"+resource, data)
		}

		return next(c)
	}
}
