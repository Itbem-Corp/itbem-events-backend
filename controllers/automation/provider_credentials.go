package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"events-stocks/internal/authz"
	"events-stocks/utils"
	"io"
	"net/http"
	"strings"

	"events-stocks/internal/automationagent"

	"github.com/labstack/echo/v4"
)

const (
	maxProviderCredentialRequestBytes = 16 << 10
	maxProviderCredentialBytes        = 8 << 10
)

type providerCredentialRequest struct {
	APIKey string `json:"api_key"`
}

type usageCredentialResolver interface {
	UsageAPIKey(context.Context, string) (string, error)
	ReplaceUsageAPIKey(context.Context, string, string) error
}

// UpsertOpenCodeUsageCredential stores a Console service-account key used only
// for read-only quota exports. It is deliberately separate from the Go
// inference key and never returned by this endpoint.
func UpsertOpenCodeUsageCredential(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	resolver, ok := inferenceCredentials.(usageCredentialResolver)
	if !ok || resolver == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "El almacén de credenciales de uso no está disponible", "")
	}
	request, err := decodeBoundedProviderCredentialRequest(c)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Credencial de uso inválida", "")
	}
	if err := withAICredentialBundleWriteLock(c.Request().Context(), func() error {
		return resolver.ReplaceUsageAPIKey(c.Request().Context(), string(automationagent.ProviderOpenCodeGo), request.APIKey)
	}); err != nil {
		return utils.Error(c, http.StatusBadRequest, "No se pudo guardar la credencial de uso", "")
	}
	return utils.Success(c, http.StatusOK, "Credencial de uso de OpenCode guardada", map[string]string{"provider": string(automationagent.ProviderOpenCodeGo), "status": "stored"})
}

// UpsertProviderCredential is the Settings write path. Primary platform roots
// may replace a provider key, but neither they nor the dashboard receive its
// previous or stored value. The raw key must never appear in an audit field.
func UpsertProviderCredential(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if _, allowed := automationagent.DefaultProviderEndpoint(automationagent.Provider(provider)); !allowed {
		return utils.Error(c, http.StatusBadRequest, "Invalid provider credential", "")
	}
	if inferenceCredentials == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "AI credential storage unavailable", "")
	}
	request, err := decodeBoundedProviderCredentialRequest(c)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid provider credential", "")
	}
	if err := withAICredentialBundleWriteLock(c.Request().Context(), func() error {
		return inferenceCredentials.ReplaceAPIKey(c.Request().Context(), provider, request.APIKey)
	}); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid provider credential", "")
	}
	return utils.Success(c, http.StatusOK, "Provider credential stored", map[string]string{"provider": provider, "status": "stored"})
}

// decodeBoundedProviderCredentialRequest enforces the bundle's per-key bound
// at the HTTP boundary as well as in the resolver. The submitted value is
// accepted only for storage; callers receive neither it nor resolver errors.
func decodeBoundedProviderCredentialRequest(c echo.Context) (providerCredentialRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, maxProviderCredentialRequestBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxProviderCredentialRequestBytes {
		return providerCredentialRequest{}, errors.New("invalid provider credential payload")
	}
	if err := rejectDuplicateAPIKeyFields(raw); err != nil {
		return providerCredentialRequest{}, errors.New("invalid provider credential payload")
	}
	var request providerCredentialRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return providerCredentialRequest{}, errors.New("invalid provider credential payload")
	}
	request.APIKey = strings.TrimSpace(request.APIKey)
	if request.APIKey == "" || len(request.APIKey) > maxProviderCredentialBytes {
		return providerCredentialRequest{}, errors.New("invalid provider credential payload")
	}
	return request, nil
}

// rejectDuplicateAPIKeyFields rejects ambiguous spellings before decoding into
// a struct. encoding/json accepts duplicate object keys and matches struct
// fields case-insensitively, so require the canonical api_key spelling and
// reject duplicate or escaped equivalents before a later value can win.
func rejectDuplicateAPIKeyFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("credential request must be an object")
	}
	seen := make(map[string]struct{}, 1)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("credential request is invalid")
		}
		key, ok := token.(string)
		if !ok || key != "api_key" {
			return errors.New("credential request field is invalid")
		}
		folded := strings.ToLower(key)
		if _, exists := seen[folded]; exists {
			return errors.New("credential request contains duplicate fields")
		}
		seen[folded] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("credential request value is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 1 {
		return errors.New("credential request is invalid")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("credential request contains trailing content")
	}
	return nil
}
