package automationagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"events-stocks/internal/releasegate"
)

func TestQASourceWorkerValidatesEntireMatrixBeforeAnyAcquisition(t *testing.T) {
	revisions := []releasegate.Revision{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}, {Repository: "example/web", Branch: "main", SHA: strings.Repeat("b", 40)}}
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "synthetic-item", Revisions: revisions, Policy: releasegate.Policy{RequiredTestKinds: []string{}}}
	change := func(reference string, r releasegate.Revision) map[string]any {
		return map[string]any{"repository_ref": reference, "branch": "itbem-agent/11111111-1111-4111-8111-111111111111", "commit_sha": r.SHA, "review_type": "pull_request", "ci_status": "passed", "metadata": map[string]string{"remote_repository": r.Repository, "target_branch": r.Branch}}
	}
	api, web := change("workspace://api", revisions[0]), change("workspace://web", revisions[1])
	for _, scenario := range []struct {
		name    string
		changes []any
		valid   bool
	}{
		{"complete", []any{api, web}, true},
		{"missing-last", []any{api}, false},
		{"ambiguous-last", []any{api, web, web}, false},
		{"duplicate-workspace", []any{api, change("workspace://api", revisions[1])}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"gatekeeper": candidate, "change_sets": scenario.changes})
			if err != nil {
				t.Fatal(err)
			}
			var acquired []string
			err = provisionPublishedQASources(context.Background(), "task", "run", payload, func(_ context.Context, task, run, reference string, _ json.RawMessage) (string, error) {
				if task != "task" || run != "run" {
					t.Fatal("acquisition lost task authority")
				}
				acquired = append(acquired, reference)
				return "synthetic-root", nil
			})
			if scenario.valid {
				if err != nil || len(acquired) != 2 || acquired[0] != "workspace://api" || acquired[1] != "workspace://web" {
					t.Fatalf("complete matrix failed: %v %v", acquired, err)
				}
			} else if err == nil || len(acquired) != 0 {
				t.Fatalf("invalid matrix began source acquisition: %v %v", acquired, err)
			}
		})
	}
}
