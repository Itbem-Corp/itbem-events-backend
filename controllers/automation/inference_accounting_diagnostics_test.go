package automation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestInferenceAccountingDiagnosticsDiscardPrivateDetails(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{context.Canceled, "accounting_canceled"},
		{fmt.Errorf("private wrapper: %w", context.DeadlineExceeded), "accounting_deadline"},
		{&pgconn.PgError{Code: "23514", Message: "private statement", Detail: "private value", ConstraintName: "private constraint"}, "accounting_db_23514"},
		{&pgconn.PgError{Code: "SECRET-VALUE", Message: "private"}, "accounting_unavailable"},
		{errors.New("provider usage could not be verified"), "accounting_usage_unverified"},
		{errors.New("inference receipt reservation does not match the gateway call"), "accounting_receipt_binding"},
		{errors.New("arbitrary private database error"), "accounting_unavailable"},
	} {
		if got := inferenceAccountingFailureCode(test.err); got != test.want {
			t.Fatalf("got %q, want %q", got, test.want)
		}
	}
}
