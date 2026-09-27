package automation

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
)

func TestAutomationCostPricingBasisCountsLegacyAndUnpricedAsUnknown(t *testing.T) {
	tests := []struct {
		basis    string
		unpriced bool
	}{
		{basis: "", unpriced: true},
		{basis: "   ", unpriced: true},
		{basis: "legacy", unpriced: true},
		{basis: " LEGACY ", unpriced: true},
		{basis: "unpriced", unpriced: true},
		{basis: "UNPRICED", unpriced: true},
		{basis: "snapshot", unpriced: false},
		{basis: "api-equivalent", unpriced: false},
		{basis: "provider-rate-card-v2", unpriced: false},
	}
	for _, test := range tests {
		t.Run(test.basis, func(t *testing.T) {
			if got := automationCostPricingBasisCountsAsUnpriced(test.basis); got != test.unpriced {
				t.Fatalf("basis %q unpriced = %t, want %t", test.basis, got, test.unpriced)
			}
		})
	}

	predicate := automationCostUnpricedPricingBasisPredicate("execution.pricing_basis")
	for _, fragment := range []string{
		"LOWER(BTRIM(COALESCE(execution.pricing_basis, ''))) ",
		"IN ('', 'legacy', 'unpriced')",
	} {
		if !strings.Contains(predicate, fragment) {
			t.Fatalf("database predicate %q omitted %q", predicate, fragment)
		}
	}
}

func TestAggregateAutomationCostSummaryCountsUnpricedRowsAcrossFilteredQuery(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	projectID := uuid.Must(uuid.NewV4())
	snapshotAt := time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC)
	filteredQuery := db.Table("(SELECT * FROM automation_executions) AS execution").
		Where("execution.project_id = ?", projectID).
		Where("execution.completed_at <= ?", snapshotAt)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT ")+`(?s).*COUNT\(\*\) FILTER \(WHERE LOWER\(BTRIM\(COALESCE\(execution\.pricing_basis, ''\)\)\) IN \('', 'legacy', 'unpriced'\)\) AS unpriced_executions.*FROM \(SELECT \* FROM automation_executions\) AS execution WHERE execution\.project_id = \$1 AND execution\.completed_at <= \$2`).
		WithArgs(projectID, snapshotAt).
		WillReturnRows(sqlmock.NewRows([]string{
			"executions", "tasks", "input_tokens", "output_tokens", "cached_input_tokens", "cache_write_tokens", "reasoning_tokens", "total_tokens",
			"input_cost_micros", "output_cost_micros", "cached_cost_micros", "cache_write_cost_micros", "total_cost_micros", "unpriced_executions",
		}).AddRow(5, 3, 800, 200, 0, 0, 0, 1000, 0, 0, 0, 0, 0, 3))

	summary, err := aggregateAutomationCostSummary(filteredQuery)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Executions != 5 || summary.UnpricedExecutions != 3 {
		t.Fatalf("summary executions/unpriced = %d/%d, want 5/3 (three legacy/unpriced plus two priced rows)", summary.Executions, summary.UnpricedExecutions)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if got, ok := payload["unpriced_executions"].(float64); !ok || got != 3 {
		t.Fatalf("serialized summary unpriced_executions = %#v, want 3", payload["unpriced_executions"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
