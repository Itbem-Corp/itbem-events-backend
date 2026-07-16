package token

import (
	"events-stocks/models"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJWTClockSkewDefaultsToStrictValidation(t *testing.T) {
	if got := jwtClockSkew(&models.Config{}); got != 0 {
		t.Fatalf("expected no default leeway, got %s", got)
	}
}

func TestJWTClockSkewUsesExplicitSeconds(t *testing.T) {
	if got := jwtClockSkew(&models.Config{JwtClockSkewSeconds: "21600"}); got != 6*time.Hour {
		t.Fatalf("expected six hours, got %s", got)
	}
}

func TestJWTClockSkewRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"invalid", "-1"} {
		if got := jwtClockSkew(&models.Config{JwtClockSkewSeconds: value}); got != 0 {
			t.Fatalf("expected strict validation for %q, got %s", value, got)
		}
	}
}

func TestValidateTenantRequestHostRejectsCrossProductTokens(t *testing.T) {
	hostMap := "api.eventiapp.com.mx=eventiapp,api.itbem.com.mx=itbem,api.cafettonhouse.com=cafettonhouse"

	require.NoError(t, validateTenantRequestHost("api.itbem.com.mx:443", "itbem", hostMap))
	assert.ErrorContains(t, validateTenantRequestHost("api.eventiapp.com.mx", "itbem", hostMap), "does not match")
	assert.ErrorContains(t, validateTenantRequestHost("unknown.example.com", "itbem", hostMap), "not configured")
}
