package configuration

import (
	"fmt"
	"net"
	"net/url"
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

	for _, legacyCredential := range []string{"COGNITO_CLIENT_ID", "COGNITO_CLIENT_SECRET", "S3_CLIENT_ID", "S3_CLIENT_SECRET"} {
		if securityEnvironmentValue(legacyCredential) != "" {
			return fmt.Errorf("%s is a deprecated static AWS credential alias and must not be configured in %s; use the workload IAM role", legacyCredential, environment)
		}
	}

	required := []string{
		"EVENT_PREVIEW_SECRET",
		"EVENT_ACCESS_SECRET",
		"INTERNAL_API_SECRET",
		"ORGANIZATION_CONTEXT_SECRET",
	}
	if securityEnvironmentValue("SQS_AUTOMATION_QUEUE_URL") != "" {
		required = append(required, "AUTOMATION_CALLBACK_SECRET")
		if bucket := securityEnvironmentValue("AUTOMATION_INPUT_BUCKET"); !strings.HasPrefix(bucket, "itbem-ai-inputs-") {
			return fmt.Errorf("AUTOMATION_INPUT_BUCKET must identify the dedicated ITBEM automation input bucket")
		}
		if bucket := securityEnvironmentValue("AUTOMATION_OUTPUT_BUCKET"); !strings.HasPrefix(bucket, "itbem-ai-outputs-") {
			return fmt.Errorf("AUTOMATION_OUTPUT_BUCKET must identify the dedicated ITBEM automation output bucket")
		}
	}
	values := make(map[string]string, len(required)+3)
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
	if previous := securityEnvironmentValue("ORGANIZATION_CONTEXT_SECRET_PREVIOUS"); previous != "" {
		if len([]byte(previous)) < minimumSigningSecretBytes {
			return fmt.Errorf("ORGANIZATION_CONTEXT_SECRET_PREVIOUS must contain at least %d bytes when configured", minimumSigningSecretBytes)
		}
		values["ORGANIZATION_CONTEXT_SECRET_PREVIOUS"] = previous
	}
	if previous := securityEnvironmentValue("AUTOMATION_CALLBACK_SECRET_PREVIOUS"); previous != "" {
		if len([]byte(previous)) < minimumSigningSecretBytes {
			return fmt.Errorf("AUTOMATION_CALLBACK_SECRET_PREVIOUS must contain at least %d bytes when configured", minimumSigningSecretBytes)
		}
		values["AUTOMATION_CALLBACK_SECRET_PREVIOUS"] = previous
	}
	if err := validateProductionOrigins(securityEnvironmentValue("CORS_ALLOW_ORIGINS")); err != nil {
		return err
	}
	if err := validateTrustedProxyCIDRs(securityEnvironmentValue("TRUSTED_PROXY_CIDRS")); err != nil {
		return err
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

func validateProductionOrigins(raw string) error {
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if candidate == "*" {
			return fmt.Errorf("CORS_ALLOW_ORIGINS must not contain wildcard origins in deployed environments")
		}
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("CORS_ALLOW_ORIGINS contains invalid production origin %q", candidate)
		}
		if parsed.Path != "" && parsed.Path != "/" {
			return fmt.Errorf("CORS_ALLOW_ORIGINS must contain origins without paths: %q", candidate)
		}
	}
	return nil
}

func validateTrustedProxyCIDRs(raw string) error {
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		_, network, err := net.ParseCIDR(candidate)
		if err != nil {
			return fmt.Errorf("TRUSTED_PROXY_CIDRS contains invalid CIDR %q", candidate)
		}
		ones, bits := network.Mask.Size()
		if ones == 0 && (bits == 32 || bits == 128) {
			return fmt.Errorf("TRUSTED_PROXY_CIDRS must not trust the entire internet")
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
