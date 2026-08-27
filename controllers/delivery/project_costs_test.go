package delivery

import (
	"strings"
	"testing"
)

func TestDeliveryCostLedgerKeepsAgentAndToolStepsSeparate(t *testing.T) {
	for _, expected := range []string{
		"FROM automation_executions", "FROM automation_tool_executions",
		"'agent' AS execution_kind", "'tool' AS execution_kind", "tool FROM automation_tool_executions",
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
