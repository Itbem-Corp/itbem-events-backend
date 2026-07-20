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
		"ORGANIZATION_CONTEXT_SECRET",
		"ORGANIZATION_CONTEXT_SECRET_PREVIOUS",
		"INTERNAL_API_SECRET_PREVIOUS",
		"COGNITO_CLIENT_ID",
		"COGNITO_CLIENT_SECRET",
		"S3_CLIENT_ID",
		"S3_CLIENT_SECRET",
		"CORS_ALLOW_ORIGINS",
		"TRUSTED_PROXY_CIDRS",
	} {
		t.Setenv(name, "")
	}
}

func TestValidateSecurityConfigurationRejectsUnsafeProductionNetworkTrust(t *testing.T) {
	clearSecurityEnvironment(t)
	t.Setenv("ENV", "production")
	t.Setenv("EVENT_PREVIEW_SECRET", strings.Repeat("p", 32))
	t.Setenv("EVENT_ACCESS_SECRET", strings.Repeat("a", 32))
	t.Setenv("INTERNAL_API_SECRET", strings.Repeat("i", 32))
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", strings.Repeat("o", 32))

	t.Setenv("CORS_ALLOW_ORIGINS", "*")
	require.ErrorContains(t, ValidateSecurityConfiguration(), "wildcard")

	t.Setenv("CORS_ALLOW_ORIGINS", "http://dashboard.example.com")
	require.ErrorContains(t, ValidateSecurityConfiguration(), "invalid production origin")

	t.Setenv("CORS_ALLOW_ORIGINS", "https://dashboard.example.com")
	t.Setenv("TRUSTED_PROXY_CIDRS", "0.0.0.0/0")
	require.ErrorContains(t, ValidateSecurityConfiguration(), "entire internet")

	t.Setenv("TRUSTED_PROXY_CIDRS", "10.20.0.0/16,2001:db8::/64")
	require.NoError(t, ValidateSecurityConfiguration())
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
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", strings.Repeat("o", 32))
	t.Setenv("ORGANIZATION_CONTEXT_SECRET_PREVIOUS", strings.Repeat("q", 32))
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
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", strings.Repeat("o", 32))
	t.Setenv("INTERNAL_API_SECRET_PREVIOUS", strings.Repeat("r", 32))
	require.NoError(t, ValidateSecurityConfiguration())
}

func TestValidateSecurityConfigurationRejectsStaticAWSCredentialsInDeployments(t *testing.T) {
	for _, name := range []string{"COGNITO_CLIENT_ID", "COGNITO_CLIENT_SECRET", "S3_CLIENT_ID", "S3_CLIENT_SECRET"} {
		t.Run(name, func(t *testing.T) {
			clearSecurityEnvironment(t)
			t.Setenv("ENV", "production")
			t.Setenv(name, "legacy-static-credential")
			require.ErrorContains(t, ValidateSecurityConfiguration(), "workload IAM role")
		})
	}
}
