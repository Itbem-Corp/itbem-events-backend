package automation

import (
	"context"
	"errors"

	"events-stocks/internal/automationagent"
)

// Persist no database message, statement, detail, table/constraint name or
// provider response. Only a controlled category or PostgreSQL SQLSTATE crosses
// this boundary; the original error remains local to the accounting path.
func inferenceAccountingFailureCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "accounting_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "accounting_deadline"
	}
	var databaseError interface{ SQLState() string }
	if errors.As(err, &databaseError) {
		if safe := automationagent.SafeInferenceFailureCode("accounting_db_" + databaseError.SQLState()); safe != "" {
			return safe
		}
	}
	if err != nil {
		switch err.Error() {
		case "accepted inference response is missing accounting identity":
			return "accounting_identity_missing"
		case "provider usage could not be verified":
			return "accounting_usage_unverified"
		case "provider usage could not be recorded":
			return "accounting_usage_encoding"
		case "inference receipt reservation does not match the gateway call":
			return "accounting_receipt_binding"
		case "inference receipt was already resolved":
			return "accounting_receipt_resolved"
		}
	}
	return "accounting_unavailable"
}
