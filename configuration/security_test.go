package configuration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clearSecurityEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ENV",
		"EVENT_PREVIEW_SECRET",
		"EVENT_ACCESS_SECRET",
		"INTERNAL_API_SECRET",
		"INTERNAL_API_SECRET_PREVIOUS",
		"COGNITO_CLIENT_SECRET",
	} {
		t.Setenv(name, "")
	}
}

func TestValidateSecurityConfigurationAllowsUnconfiguredLocalDevelopment(t *testing.T) {
	clearSecurityEnvironment(t)
	t.Setenv("ENV", "local")
	require.NoError(t, ValidateSecurityConfiguration())
}

func TestValidateSecurityConfigurationRequiresDedicatedDeployedSecrets(t *testing.T) {
	clearSecurityEnvironment(t)
	t.Setenv("ENV", "production")
	err := ValidateSecurityConfiguration()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EVENT_PREVIEW_SECRET")
}

func TestValidateSecurityConfigurationRejectsWeakOrReusedSecrets(t *testing.T) {
	clearSecurityEnvironment(t)
	t.Setenv("ENV", "staging")
	t.Setenv("EVENT_PREVIEW_SECRET", "too-short")
	t.Setenv("EVENT_ACCESS_SECRET", strings.Repeat("a", 32))
	t.Setenv("INTERNAL_API_SECRET", strings.Repeat("b", 32))
	err := ValidateSecurityConfiguration()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EVENT_PREVIEW_SECRET")

	t.Setenv("EVENT_PREVIEW_SECRET", strings.Repeat("a", 32))
	err = ValidateSecurityConfiguration()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "independent values")
}

func TestValidateSecurityConfigurationAcceptsIndependentRotationSecrets(t *testing.T) {
	clearSecurityEnvironment(t)
	t.Setenv("ENV", "production")
	t.Setenv("EVENT_PREVIEW_SECRET", strings.Repeat("p", 32))
	t.Setenv("EVENT_ACCESS_SECRET", strings.Repeat("a", 32))
	t.Setenv("INTERNAL_API_SECRET", strings.Repeat("i", 32))
	t.Setenv("INTERNAL_API_SECRET_PREVIOUS", strings.Repeat("r", 32))
	t.Setenv("COGNITO_CLIENT_SECRET", strings.Repeat("c", 32))
	require.NoError(t, ValidateSecurityConfiguration())
}
