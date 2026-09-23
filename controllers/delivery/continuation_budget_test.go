package delivery

import (
	"net/http"
	"strings"
	"testing"
)

func TestContinuationBudgetBlockUsesOnlySafeAdmissionCodes(t *testing.T) {
	for _, code := range []string{"task_budget_insufficient", "project_budget_insufficient"} {
		reason := continuationBudgetBlock(http.StatusConflict, []byte(`{"error":"`+code+`"}`))
		if !strings.Contains(reason, "presupuesto") || !strings.Contains(reason, "No se ha llamado al modelo") {
			t.Fatalf("missing actionable budget guidance for %s: %s", code, reason)
		}
	}
	for _, body := range []string{`{"error":"database password=secret"}`, `not json`, `{}`} {
		if got := continuationBudgetBlock(http.StatusConflict, []byte(body)); got != "" {
			t.Fatalf("exposed unrecognized details: %s", got)
		}
	}
	if got := continuationBudgetBlock(http.StatusInternalServerError, []byte(`{"error":"task_budget_insufficient"}`)); got != "" {
		t.Fatal("misclassified internal failure as a budget rejection")
	}
}
