package token

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/utils"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

func jwtClockSkew(cfg *models.Config) time.Duration {
	if cfg == nil || strings.TrimSpace(cfg.JwtClockSkewSeconds) == "" {
		return 0
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(cfg.JwtClockSkewSeconds))
	if err != nil || seconds < 0 {
		slog.Warn("invalid JWT clock skew; using strict validation", "value", cfg.JwtClockSkewSeconds)
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func parseTenantClientMap(value string) map[string]string {
	result := make(map[string]string)
	for _, entry := range strings.Split(value, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			result[strings.TrimSpace(parts[0])] = strings.ToLower(strings.TrimSpace(parts[1]))
		}
	}
	return result
}

func allowedClientIDs(cfg *models.Config) map[string]struct{} {
	result := make(map[string]struct{})
	for _, id := range strings.Split(cfg.CognitoAllowedClientIds, ",") {
		if id = strings.TrimSpace(id); id != "" {
			result[id] = struct{}{}
		}
	}
	for id := range parseTenantClientMap(cfg.CognitoTenantClientMap) {
		result[id] = struct{}{}
	}
	return result
}

func validateCognitoIdentityClaims(claims jwt.MapClaims, cfg *models.Config) (string, string, string, error) {
	issuer := fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", cfg.CognitoAwsRegion, cfg.CognitoUserPoolId)
	if claims["iss"] != issuer {
		return "", "", "", fmt.Errorf("issuer does not match configured user pool")
	}
	if tokenUse, _ := claims["token_use"].(string); tokenUse != "id" {
		return "", "", "", fmt.Errorf("token_use must be id")
	}
	audience, ok := claims["aud"].(string)
	if !ok || strings.TrimSpace(audience) == "" {
		return "", "", "", fmt.Errorf("aud claim is required")
	}
	allowed := allowedClientIDs(cfg)
	if len(allowed) == 0 {
		return "", "", "", fmt.Errorf("no Cognito app clients are configured")
	}
	if _, ok := allowed[audience]; !ok {
		return "", "", "", fmt.Errorf("token audience is not allowed")
	}
	subject, ok := claims["sub"].(string)
	if !ok || strings.TrimSpace(subject) == "" {
		return "", "", "", fmt.Errorf("sub claim is required")
	}
	return subject, audience, parseTenantClientMap(cfg.CognitoTenantClientMap)[audience], nil
}

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
			token, err := jwt.Parse(tokenString, jwks.Keyfunc, jwt.WithLeeway(jwtClockSkew(cfg)))
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
			_, ok = claims["sub"].(string)
			if !ok {
				return utils.Error(c, http.StatusUnauthorized, "Token incompleto", "El claim 'sub' es requerido")
			}

			// Inyectamos datos al contexto para usarlos en los controladores
			validatedSub, audience, tenantCode, validationErr := validateCognitoIdentityClaims(claims, cfg)
			if validationErr != nil {
				return utils.Error(c, http.StatusUnauthorized, "Token no confiable", validationErr.Error())
			}
			cognitoSub := validatedSub
			c.Set("cognito_sub", cognitoSub)
			c.Set("auth_client_id", audience)
			if tenantCode != "" {
				c.Set("tenant_code", tenantCode)
			}
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
