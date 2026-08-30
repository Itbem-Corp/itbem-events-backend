package automationagent

import (
	"encoding/json"
	"fmt"

	"events-stocks/internal/releasegate"
)

const maxReleaseGateInputBytes = 1 << 20

// RunReleaseGate validates the control-plane candidate and returns only the
// exact structured input to the signed callback. It never calls a model and it
// never supplies a human approval; the backend binds the authenticated task
// requester after re-evaluating the same deterministic subject.
func RunReleaseGate(delivery json.RawMessage) (releasegate.Input, error) {
	if len(delivery) == 0 || len(delivery) > maxReleaseGateInputBytes {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper input size is invalid")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(delivery, &envelope); err != nil || envelope == nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper input is invalid")
	}
	allowed := map[string]struct{}{
		"project": {}, "work_item": {}, "approved_plan": {}, "autonomy_policy": {}, "context_sources": {},
		"repository_topology": {}, "client_context": {}, "conversation": {}, "change_sets": {}, "evidence": {},
		"gates": {}, "human_request": {}, "publication": {}, "gatekeeper": {}, "release_environment": {},
	}
	for key := range envelope {
		if _, ok := allowed[key]; !ok {
			return releasegate.Input{}, fmt.Errorf("release Gatekeeper input contains an unsupported field")
		}
	}
	raw, ok := envelope["gatekeeper"]
	if !ok {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper input is missing")
	}
	input, err := releasegate.DecodeInput(raw)
	if err != nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper candidate is invalid")
	}
	if input.SchemaVersion != releasegate.SchemaVersion || input.Action != releasegate.ActionRelease || input.HumanApproval != nil {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper candidate authority is invalid")
	}
	decision := releasegate.Evaluate(input)
	if decision.SubjectDigest == "" {
		return releasegate.Input{}, fmt.Errorf("release Gatekeeper candidate cannot identify an exact subject")
	}
	return input, nil
}

func releaseGateHandoff(input releasegate.Input, environment any) map[string]any {
	return map[string]any{"schema_version": 2, "gatekeeper_input": input, "environment_observation": environment}
}
