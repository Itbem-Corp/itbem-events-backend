package app

import (
	"bytes"
	"log/slog"
	"testing"

	"events-stocks/internal/capabilities"
	"events-stocks/models"

	"github.com/stretchr/testify/assert"
)

func TestAuthorizationModesDefaultToOff(t *testing.T) {
	entitlement, ceiling := authorizationModes(&models.Config{})
	assert.Equal(t, capabilities.ModeOff, entitlement)
	assert.Equal(t, capabilities.ModeOff, ceiling)
}

func TestAuthorizationModesParseShadowAndEnforce(t *testing.T) {
	entitlement, ceiling := authorizationModes(&models.Config{AuthzProductEntitlement: "shadow", AuthzPolicyCeiling: " Enforce "})
	assert.Equal(t, capabilities.ModeShadow, entitlement)
	assert.Equal(t, capabilities.ModeEnforce, ceiling)
}

func TestInvalidAuthorizationModeIsOffWithAnErrorLog(t *testing.T) {
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	entitlement, ceiling := authorizationModes(&models.Config{AuthzProductEntitlement: "on", AuthzPolicyCeiling: "true"})
	assert.Equal(t, capabilities.ModeOff, entitlement)
	assert.Equal(t, capabilities.ModeOff, ceiling)
	assert.Contains(t, buffer.String(), "level=ERROR")
	assert.Contains(t, buffer.String(), "AUTHZ_PRODUCT_ENTITLEMENT")
	assert.Contains(t, buffer.String(), "AUTHZ_POLICY_CEILING")
}
