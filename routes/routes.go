package routes

import (
	"events-stocks/controllers/cache"
	"events-stocks/controllers/events"
	"events-stocks/controllers/resources"
	"events-stocks/middleware/redis"
	"events-stocks/middleware/token"
	"events-stocks/models"
	"github.com/labstack/echo/v4"
)

func ConfigurarRutas(e *echo.Echo, cfg *models.Config) {
	e.GET("/health", events.GetEvents)

	api := e.Group("/api")
	api.GET("/resources/:id", resources.GetResource)
	api.GET("/resources/section/:id", resources.ListResourcesBySection)
	api.POST("/resources", resources.CreateResource)
	api.PUT("/resources/:id", resources.UpdateResourceMetadata)
	api.DELETE("/resources/:id", resources.DeleteResource)
	// Aquí podrías añadir middlewares si lo necesitas, por ejemplo, autenticación
	api.Use(token.Autenticacion(cfg))
	api.Use(redisMiddleware.RetrieveCache)

	api.GET("/cache/flush/:key", cache.FlushKey)
	api.GET("/cache/flush-all", cache.FlushAll)

	// Redis cache key-based fetch
	api.GET("/events/:key", events.GetEvents)

	// 📌 Event CRUD routes
	api.POST("/events", events.CreateEvent)
	api.PUT("/events/:id", events.UpdateEvent)
	api.DELETE("/events/:id", events.DeleteEvent)
}
