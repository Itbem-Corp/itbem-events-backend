package deliverydecomposition

import "testing"

func TestParseAcceptsBoundedAcyclicTaskGraph(t *testing.T) {
	proposal, err := Parse([]byte(`{"summary":"Split delivery safely","tasks":[{"key":"api-contract","title":"Validate API contract","expected_outcome":"Documented contract","included_scope":["API schema"],"excluded_scope":[],"acceptance_criteria":["Contract is bounded"],"context_references":["workspace://backend"],"depends_on":[],"budget_microusd":12000},{"key":"dashboard-qa","title":"Verify dashboard","expected_outcome":"Evidence captured","included_scope":["Dashboard route"],"excluded_scope":[],"acceptance_criteria":["Screenshot is present"],"context_references":["workspace://dashboard"],"depends_on":["api-contract"],"budget_microusd":8000}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Tasks) != 2 || proposal.Tasks[1].DependsOn[0] != "api-contract" {
		t.Fatalf("unexpected proposal: %#v", proposal)
	}
}

func TestParseRejectsCyclesAndUnpinnedContext(t *testing.T) {
	_, err := Parse([]byte(`{"summary":"Bad graph","tasks":[{"key":"one","title":"One","expected_outcome":"One","included_scope":["one"],"acceptance_criteria":["one"],"context_references":["workspace://backend"],"depends_on":["two"]},{"key":"two","title":"Two","expected_outcome":"Two","included_scope":["two"],"acceptance_criteria":["two"],"context_references":["workspace://dashboard"],"depends_on":["one"]}]}`))
	if err == nil {
		t.Fatal("expected cycle rejection")
	}
	_, err = Parse([]byte(`{"summary":"Bad context","tasks":[{"key":"one","title":"One","expected_outcome":"One","included_scope":["one"],"acceptance_criteria":["one"],"context_references":["C:/repo"],"depends_on":[]}]}`))
	if err == nil {
		t.Fatal("expected context reference rejection")
	}
}
