package automationagent

import (
	"encoding/json"
	"strings"
	"testing"

	"events-stocks/internal/releasegate"
)

func TestRunReleaseGateAcceptsOnlyAReleaseCandidateWithoutHumanAuthority(t *testing.T) {
	input := releasegate.Input{
		SchemaVersion: releasegate.SchemaVersion,
		Action:        releasegate.ActionRelease,
		ChangeSetID:   "change-set-42",
		Revisions: []releasegate.Revision{{
			Repository: "example/service", Branch: "feature/release", SHA: strings.Repeat("a", 40),
		}},
		Policy: releasegate.Policy{Resolved: false, RequiredTestKinds: []string{}},
	}
	delivery, err := json.Marshal(map[string]any{"project": map[string]any{"id": "private"}, "gatekeeper": input})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := RunReleaseGate(delivery)
	if err != nil || actual.ChangeSetID != input.ChangeSetID || actual.HumanApproval != nil {
		t.Fatalf("exact release candidate was rejected or changed: %#v / %v", actual, err)
	}

	withHuman := input
	withHuman.HumanApproval = &releasegate.HumanApproval{Actor: "invented", ActorType: "human", SubjectDigest: strings.Repeat("b", 64), Approved: true}
	for _, invalid := range []map[string]any{
		{"gatekeeper": withHuman},
		{"gatekeeper": input, "unknown_authority": true},
		{"project": map[string]any{}},
	} {
		payload, _ := json.Marshal(invalid)
		if _, err := RunReleaseGate(payload); err == nil {
			t.Fatalf("untrusted Gatekeeper authority was accepted: %s", payload)
		}
	}
}
