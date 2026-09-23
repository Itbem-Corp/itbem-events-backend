package automation

import (
	"encoding/json"
	"events-stocks/internal/authz"
	"events-stocks/utils"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

type providerCredentialRequest struct {
	APIKey string `json:"api_key"`
}

// UpsertProviderCredential is the Settings write path. Primary platform roots
// may replace a provider key, but neither they nor the dashboard receive its
// previous or stored value. The raw key must never appear in an audit field.
func UpsertProviderCredential(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	if inferenceCredentials == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "AI credential storage unavailable", "")
	}
	var request providerCredentialRequest
	decoder := json.NewDecoder(io.LimitReader(c.Request().Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return utils.Error(c, http.StatusBadRequest, "Invalid provider credential", "")
	}
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if err := inferenceCredentials.ReplaceAPIKey(c.Request().Context(), provider, request.APIKey); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid provider credential", "")
	}
	return utils.Success(c, http.StatusOK, "Provider credential stored", map[string]string{"provider": provider, "status": "stored"})
}
