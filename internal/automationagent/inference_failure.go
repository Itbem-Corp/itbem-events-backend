package automationagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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
	case "provider_response_invalid", "provider_response_incomplete", "provider_timeout", "request_canceled", "provider_transport", "provider_model_limits_unavailable", "credentials_unavailable", "routing_invalid", "accounting_unavailable", "accounting_identity_missing", "accounting_usage_unverified", "accounting_usage_encoding", "accounting_receipt_binding", "accounting_receipt_resolved", "accounting_canceled", "accounting_deadline", "provider_unclassified":
		return value
	}
	if suffix, ok := strings.CutPrefix(value, "accounting_db_"); ok && len(suffix) == 5 {
		for _, char := range suffix {
			if (char < '0' || char > '9') && (char < 'A' || char > 'Z') {
				return ""
			}
		}
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
	var transport net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &transport) && transport.Timeout()) {
		return "provider_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "request_canceled"
	}
	if errors.As(err, &transport) {
		return "provider_transport"
	}
	var readFailure *ProviderResponseReadError
	if errors.As(err, &readFailure) {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "provider_response_incomplete"
		}
		return "provider_response_invalid"
	}
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
