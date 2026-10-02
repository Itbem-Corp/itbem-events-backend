package automationagent

import (
	"encoding/json"
	"events-stocks/internal/environmentevidence"
	"events-stocks/internal/releasegate"
	"strings"
	"testing"
)

func TestReleaseObservationClientRejectsChangedSubjectAndInventedApproval(t *testing.T) {
	const task = "11111111-1111-4111-8111-111111111111"
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: task, Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: strings.Repeat("a", 40)}}, Policy: releasegate.Policy{RequiredTestKinds: []string{}}}
	delivery, err := json.Marshal(map[string]any{"gatekeeper": candidate})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil {
		t.Fatal(err)
	}
	environment := environmentevidence.Observation{SchemaVersion: 1, TaskID: task, MatrixDigest: digest, Repositories: []environmentevidence.Repository{{Repository: "example/service", HeadSHA: strings.Repeat("a", 40), Workflow: ".github/workflows/deploy.yml", Environment: "production", WorkflowExists: true, EnvironmentExists: true, RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{}, MissingSecretReferences: []string{}, MissingVariableReferences: []string{}}}}
	for _, name := range []string{"valid", "sha", "change-set", "approval", "task", "digest", "extra-field"} {
		t.Run(name, func(t *testing.T) {
			observed, env := candidate, environment
			observed.Revisions = append([]releasegate.Revision(nil), candidate.Revisions...)
			switch name {
			case "sha":
				observed.Revisions[0].SHA = strings.Repeat("b", 40)
			case "change-set":
				observed.ChangeSetID = "other"
			case "approval":
				observed.HumanApproval = &releasegate.HumanApproval{Approved: true}
			case "task":
				env.TaskID = "22222222-2222-4222-8222-222222222222"
			case "digest":
				env.MatrixDigest = strings.Repeat("c", 64)
			}
			handoff := releaseGateHandoff(observed, env)
			if name == "extra-field" {
				handoff["invented_authority"] = true
			}
			raw, err := json.Marshal(handoff)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validateReleaseObservation(task, delivery, raw)
			if (name == "valid") != (err == nil) {
				t.Fatalf("unexpected acceptance: %v", err)
			}
		})
	}
}
