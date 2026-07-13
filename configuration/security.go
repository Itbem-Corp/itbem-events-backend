package configuration

import (
	"fmt"
	"os"
	"strings"
)

const minimumSigningSecretBytes = 32

// ValidateSecurityConfiguration enforces independent, high-entropy bearer
// credential keys in deployed environments. Local development remains
// opt-in, but token generation itself still fails closed when a dedicated key
// is absent.
func ValidateSecurityConfiguration() error {
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("ENV")))
	if environment != "production" && environment != "prod" && environment != "staging" && environment != "stage" {
		return nil
	}

	required := []string{
		"EVENT_PREVIEW_SECRET",
		"EVENT_ACCESS_SECRET",
		"INTERNAL_API_SECRET",
	}
	values := make(map[string]string, len(required)+2)
	for _, name := range required {
		value := securityEnvironmentValue(name)
		if len([]byte(value)) < minimumSigningSecretBytes {
			return fmt.Errorf("%s must contain at least %d bytes in %s", name, minimumSigningSecretBytes, environment)
		}
		values[name] = value
	}

	if previous := securityEnvironmentValue("INTERNAL_API_SECRET_PREVIOUS"); previous != "" {
		if len([]byte(previous)) < minimumSigningSecretBytes {
			return fmt.Errorf("INTERNAL_API_SECRET_PREVIOUS must contain at least %d bytes when configured", minimumSigningSecretBytes)
		}
		values["INTERNAL_API_SECRET_PREVIOUS"] = previous
	}
	if cognito := securityEnvironmentValue("COGNITO_CLIENT_SECRET"); cognito != "" {
		values["COGNITO_CLIENT_SECRET"] = cognito
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if values[names[i]] == values[names[j]] {
				return fmt.Errorf("%s and %s must use independent values", names[i], names[j])
			}
		}
	}
	return nil
}

func securityEnvironmentValue(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if strings.EqualFold(value, "none") {
		return ""
	}
	return value
}
