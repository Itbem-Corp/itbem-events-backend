package token

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"github.com/labstack/echo/v4"
	"net/http"
)

func Autenticacion(cfg *models.Config) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token := c.Request().Header.Get("Authorization")
			if token == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "No autorizado"})
			}

			// Aquí podrías validar el token (si quieres agregar lógica)

			// Si pasa, inyectas el cfg al contexto
			req := c.Request()
			ctx := configuration.WithConfig(req.Context(), cfg)
			c.SetRequest(req.WithContext(ctx))

			return next(c)
		}
	}
}
