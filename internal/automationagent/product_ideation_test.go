package automationagent

import "testing"

const validProductBrief = `{"summary":"Compare safe product directions.","directions":[{"name":"Guided flow","user_outcome":"Finish the core action faster.","smallest_slice":"One guided entry point.","trade_off":"Less flexibility initially.","risk":"Users may bypass it.","success_signal":"Completion rate increases."},{"name":"Automation feed","user_outcome":"See progress without asking.","smallest_slice":"Live activity feed.","trade_off":"More operational data.","risk":"Noisy updates.","success_signal":"Fewer status requests."}],"recommendation":{"direction":"Guided flow","rationale":"It validates the main need with the smallest slice.","first_experiment":"Test it with five operators."},"open_questions":["Which user segment is primary?"]}`

func TestParseProductIdeationAcceptsBoundedDecisionBrief(t *testing.T) {
	brief, err := ParseProductIdeation(validProductBrief)
	if err != nil {
		t.Fatal(err)
	}
	if brief["recommendation"].(map[string]any)["direction"] != "Guided flow" {
		t.Fatalf("unexpected recommendation: %#v", brief)
	}
}

func TestParseProductIdeationRejectsUnreviewableRecommendation(t *testing.T) {
	invalid := `{"summary":"Compare safe product directions.","directions":[{"name":"Guided flow","user_outcome":"Finish faster.","smallest_slice":"One entry point.","trade_off":"Less flexibility.","risk":"Bypass.","success_signal":"Completion rises."},{"name":"Automation feed","user_outcome":"See progress.","smallest_slice":"Activity feed.","trade_off":"More data.","risk":"Noise.","success_signal":"Fewer requests."}],"recommendation":{"direction":"Invented","rationale":"Unknown.","first_experiment":"Test."}}`
	if _, err := ParseProductIdeation(invalid); err == nil {
		t.Fatal("recommendation for an unknown direction must be rejected")
	}
}
