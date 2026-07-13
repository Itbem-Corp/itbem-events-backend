package token

import (
	"events-stocks/models"
	"testing"
	"time"
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
