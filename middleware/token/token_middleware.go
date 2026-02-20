package token

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/utils"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

var (
	jwks   *keyfunc.JWKS
	jwksMu sync.RWMutex
)

// initJWKS descarga las llaves públicas de AWS una sola vez y las cachea.
// Goroutine-safe via double-checked locking.
func initJWKS(cfg *models.Config) error {
	jwksMu.RLock()
	if jwks != nil {
		jwksMu.RUnlock()
		return nil
	}
	jwksMu.RUnlock()

	jwksMu.Lock()
	defer jwksMu.Unlock()
	if jwks != nil { // double-check after acquiring write lock
		return nil
	}

	jwksURL := fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s/.well-known/jwks.json", cfg.CognitoAwsRegion, cfg.CognitoUserPoolId)

	options := keyfunc.Options{
		RefreshInterval:  time.Hour,
		RefreshRateLimit: time.Minute * 5,
		RefreshErrorHandler: func(err error) {
			slog.Warn("jwks refresh failed", "error", err)
		},
	}

	var err error
	jwks, err = keyfunc.Get(jwksURL, options)
	if err != nil {
		return fmt.Errorf("failed to get JWKS from AWS: %w", err)
	}
	return nil
}

func Autenticacion(cfg *models.Config) echo.MiddlewareFunc {
	if err := initJWKS(cfg); err != nil {
		slog.Warn("jwks initial load failed", "error", err)
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				// 👇 Usamos utils.Error
				return utils.Error(c, http.StatusUnauthorized, "Acceso denegado", "Falta header de autorización")
			}

			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				return utils.Error(c, http.StatusUnauthorized, "Formato inválido", "El token debe ser Bearer <token>")
			}
			tokenString := parts[1]

			if err := initJWKS(cfg); err != nil {
				return utils.Error(c, http.StatusInternalServerError, "Error interno", "No se pudo inicializar la configuración de autenticación")
			}

			// Validamos la firma del token con las llaves de AWS
			token, err := jwt.Parse(tokenString, jwks.Keyfunc)
			if err != nil {
				return utils.Error(c, http.StatusUnauthorized, "Token inválido", err.Error())
			}

			if !token.Valid {
				return utils.Error(c, http.StatusUnauthorized, "Token no válido", "La firma del token no coincide o ha expirado")
			}

			// Extraer Claims
			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				return utils.Error(c, http.StatusUnauthorized, "Token corrupto", "No se pudieron leer los claims")
			}

			// Validamos que sea de nuestro User Pool (Issuer)
			issuer := fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", cfg.CognitoAwsRegion, cfg.CognitoUserPoolId)
			if claims["iss"] != issuer {
				return utils.Error(c, http.StatusUnauthorized, "Fuente no confiable", "El issuer del token no coincide con el pool configurado")
			}

			// Obtenemos el ID único de usuario (SUB)
			cognitoSub, ok := claims["sub"].(string)
			if !ok {
				return utils.Error(c, http.StatusUnauthorized, "Token incompleto", "El claim 'sub' es requerido")
			}

			// Inyectamos datos al contexto para usarlos en los controladores
			c.Set("cognito_sub", cognitoSub)
			if email, ok := claims["email"].(string); ok {
				c.Set("user_email", email)
			}
			c.Set("config", cfg)
			// Mantener tu lógica de inyectar configuración
			req := c.Request()
			ctx := configuration.WithConfig(req.Context(), cfg)
			c.SetRequest(req.WithContext(ctx))

			return next(c)
		}
	}
}
