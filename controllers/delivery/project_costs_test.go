package delivery

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
)

func TestProjectCostCursorAndPageSizeAreScoped(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	otherProjectID := uuid.Must(uuid.NewV4())
	workItemID := uuid.Must(uuid.NewV4())
	scope := projectCostWorkItemCursorScope(projectID)
	encoded := encodeProjectCostCursor(projectCostCursor{Version: 1, Scope: scope, TotalCostMicros: 4200, WorkItemID: workItemID.String()})
	decoded, err := decodeProjectCostCursor(encoded, scope)
	if err != nil || decoded.TotalCostMicros != 4200 || decoded.WorkItemID != workItemID.String() {
		t.Fatalf("valid scoped cursor rejected: %#v, %v", decoded, err)
	}
	if _, err := decodeProjectCostCursor(encoded, projectCostWorkItemCursorScope(otherProjectID)); err == nil {
		t.Fatal("cursor from another project was accepted")
	}
	if cursor, err := decodeProjectCostCursor("", scope); err != nil || cursor != nil {
		t.Fatalf("empty cursor should request the first page: %#v, %v", cursor, err)
	}
	for _, invalid := range []string{"not-a-cursor", strings.Repeat("a", maxProjectCostCursorBytes+1)} {
		if _, err := decodeProjectCostCursor(invalid, scope); err == nil {
			t.Fatalf("invalid cursor accepted: %q", invalid)
		}
	}
	invalidID := encodeProjectCostCursor(projectCostCursor{Version: 1, Scope: scope, WorkItemID: "not-a-uuid"})
	if _, err := decodeProjectCostCursor(invalidID, scope); err == nil {
		t.Fatal("cursor with an invalid work-item ID was accepted")
	}
	validPayload, err := json.Marshal(projectCostCursor{Version: 1, Scope: scope, TotalCostMicros: 4200, WorkItemID: workItemID.String()})
	if err != nil {
		t.Fatal(err)
	}
	unknownFieldPayload := strings.TrimSuffix(string(validPayload), "}") + `,"unexpected":true}`
	wrongVersionPayload, err := json.Marshal(projectCostCursor{Version: 2, Scope: scope, TotalCostMicros: 4200, WorkItemID: workItemID.String()})
	if err != nil {
		t.Fatal(err)
	}
	nilIDPayload, err := json.Marshal(projectCostCursor{Version: 1, Scope: scope, TotalCostMicros: 4200, WorkItemID: uuid.Nil.String()})
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]string{
		"unknown field": unknownFieldPayload,
		"trailing JSON": string(validPayload) + ` {}`,
		"wrong version": string(wrongVersionPayload),
		"nil work item": string(nilIDPayload),
	} {
		badCursor := base64.RawURLEncoding.EncodeToString([]byte(payload))
		if _, err := decodeProjectCostCursor(badCursor, scope); err == nil {
			t.Errorf("cursor with %s was accepted", name)
		}
	}

	if limit, err := projectCostPageSize(""); err != nil || limit != defaultProjectCostPageSize {
		t.Fatalf("default page size = %d, %v", limit, err)
	}
	for _, valid := range []string{"1", "50", "100"} {
		if _, err := projectCostPageSize(valid); err != nil {
			t.Fatalf("valid page size %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"0", "101", "nope", "-1"} {
		if _, err := projectCostPageSize(invalid); err == nil {
			t.Fatalf("invalid page size %q accepted", invalid)
		}
	}
}

func TestDeliveryCostLedgerKeepsAgentAndToolStepsSeparate(t *testing.T) {
	for _, expected := range []string{
		"FROM automation_executions", "FROM automation_tool_executions",
		"'agent' AS execution_kind", "'tool' AS execution_kind", "tool FROM automation_tool_executions",
		"currency, pricing_basis, completed_at",
	} {
		if !strings.Contains(deliveryCostLedgerUnion, expected) {
			t.Fatalf("delivery ledger union omitted %q: %s", expected, deliveryCostLedgerUnion)
		}
	}
	for _, forbidden := range []string{"request_ref", "response_ref", "usage_json"} {
		if strings.Contains(deliveryCostLedgerUnion, forbidden) {
			t.Fatalf("delivery ledger union leaked private %q: %s", forbidden, deliveryCostLedgerUnion)
		}
	}
}

func TestProjectCostCoverageRequiresVerifiedUSDPriceAndFiltersUnknownAmounts(t *testing.T) {
	tests := []struct {
		name     string
		currency string
		basis    string
		known    bool
	}{
		{name: "official USD price", currency: "USD", basis: "official_api_price", known: true},
		{name: "custom USD catalog", currency: "usd", basis: "custom-price-list-v4", known: true},
		{name: "legacy basis", currency: "USD", basis: "legacy", known: false},
		{name: "unpriced basis", currency: "USD", basis: "unpriced", known: false},
		{name: "missing basis", currency: "USD", basis: "  ", known: false},
		{name: "unsupported currency", currency: "EUR", basis: "official_api_price", known: false},
		{name: "missing currency", currency: "", basis: "official_api_price", known: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deliveryCostRowHasVerifiedUSDPrice(test.currency, test.basis); got != test.known {
				t.Fatalf("currency/basis %q/%q known = %t, want %t", test.currency, test.basis, got, test.known)
			}
		})
	}

	unknownCondition := deliveryCostUnpricedCondition()
	wantUnknownCondition := "(UPPER(BTRIM(COALESCE(execution.currency, ''))) <> 'USD' OR LOWER(BTRIM(COALESCE(execution.pricing_basis, ''))) IN " + deliveryCostUnknownPricingBases + ")"
	if unknownCondition != wantUnknownCondition {
		t.Fatalf("unknown-price predicate = %q, want %q", unknownCondition, wantUnknownCondition)
	}
	for name, selectSQL := range map[string]string{
		"summary":   projectCostSummarySelect(),
		"step":      projectCostStepSelect(),
		"work item": projectCostWorkItemSelect(),
	} {
		if !strings.Contains(selectSQL, "AS unpriced_executions") {
			t.Errorf("%s cost projection omitted unknown coverage: %s", name, selectSQL)
		}
		for _, column := range []string{"input_cost_micros", "output_cost_micros", "cached_cost_micros", "cache_write_cost_micros", "total_cost_micros"} {
			knownSum := deliveryCostKnownUSDSum("execution."+column) + " AS " + column
			if !strings.Contains(selectSQL, knownSum) {
				t.Errorf("%s cost projection must subtotal only verified USD %s: %s", name, column, selectSQL)
			}
		}
	}
}
