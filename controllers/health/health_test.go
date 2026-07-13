package health

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHealthEnvironmentNormalizesDeploymentAliases(t *testing.T) {
	tests := map[string]string{
		"production": "production",
		" PROD ":     "production",
		"staging":    "staging",
		"Stage":      "staging",
		"local":      "local",
		"dev":        "dev",
		"":           "unknown",
		"preview":    "unknown",
	}
	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, expected, healthEnvironment(input))
		})
	}
}
