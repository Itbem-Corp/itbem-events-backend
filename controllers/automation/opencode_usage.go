package automation

import (
	"net/http"
	"time"

	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
)

// GetOpenCodeUsage returns only computed quota windows; raw CSV rows and the
// Console service-account key remain in-process.
func GetOpenCodeUsage(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	resolver, ok := inferenceCredentials.(usageCredentialResolver)
	if !ok || resolver == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "El almacén de credenciales de uso no está disponible", "")
	}
	apiKey, err := resolver.UsageAPIKey(c.Request().Context(), string(automationagent.ProviderOpenCodeGo))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Primero guarda una service-account key de OpenCode Console", "")
	}
	usage, err := automationagent.FetchOpenCodeUsage(c.Request().Context(), apiKey, nil, time.Now())
	if err != nil {
		return utils.Error(c, http.StatusBadGateway, "No se pudo obtener la cuota de OpenCode", "")
	}
	return utils.Success(c, http.StatusOK, "Cuota de OpenCode obtenida", usage)
}
