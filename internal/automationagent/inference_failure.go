package automationagent

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const InferenceFailureHeader = "X-ITBEM-Inference-Failure"

// ProviderHTTPError retains only the response status. Provider response bodies,
// messages and request headers never enter this diagnostic boundary.
type ProviderHTTPError struct{ StatusCode int }

func (e *ProviderHTTPError) Error() string {
	return fmt.Sprintf("provider request rejected (%d)", e.StatusCode)
}

func SafeInferenceFailureCode(value string) string {
	switch value {
	case "provider_model_limits_unavailable", "credentials_unavailable", "routing_invalid", "accounting_unavailable", "provider_unclassified":
		return value
	}
	if suffix, ok := strings.CutPrefix(value, "provider_http_"); ok && len(suffix) == 3 {
		status, err := strconv.Atoi(suffix)
		if err == nil && status >= 400 && status <= 599 {
			return value
		}
	}
	return ""
}

func InferenceFailureCode(err error) string {
	var rejected *ProviderHTTPError
	if errors.As(err, &rejected) {
		return SafeInferenceFailureCode(fmt.Sprintf("provider_http_%d", rejected.StatusCode))
	}
	var retryable *RetryableError
	if errors.As(err, &retryable) && retryable.StatusCode != 0 {
		return SafeInferenceFailureCode(fmt.Sprintf("provider_http_%d", retryable.StatusCode))
	}
	if err != nil {
		switch err.Error() {
		case "provider model limits are unavailable":
			return "provider_model_limits_unavailable"
		case "AI gateway unavailable":
			return "credentials_unavailable"
		case "AI routing configuration is invalid":
			return "routing_invalid"
		}
	}
	return "provider_unclassified"
}
