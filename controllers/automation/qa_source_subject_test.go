package automation

import (
	"encoding/json"
	"strings"
	"testing"

	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestQASourceSelectionBindsFrozenMatrixAndRejectsAmbiguity(t *testing.T) {
	item := uuid.Must(uuid.NewV4())
	revisions := []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: strings.Repeat("a", 40)}}
	digest, err := releasegate.RevisionMatrixDigest(revisions)
	if err != nil {
		t.Fatal(err)
	}
	task := models.AutomationTask{Operation: "delivery.qa", DeliveryWorkItemID: &item, EvidenceSubjectDigest: digest}
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: item.String(), Revisions: revisions, Policy: releasegate.Policy{RequiredTestKinds: []string{}}}
	change := map[string]any{"repository_ref": "workspace://repo", "branch": "itbem-agent/11111111-1111-4111-8111-111111111111", "commit_sha": revisions[0].SHA, "review_type": "pull_request", "ci_status": "passed", "metadata": map[string]string{"remote_repository": "example/service", "target_branch": "main"}}
	encode := func(changes []any) []byte {
		raw, e := json.Marshal(map[string]any{"gatekeeper": candidate, "change_sets": changes})
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	raw := encode([]any{change})
	subject, err := qaSourceSubjectForTask(&task, raw, "workspace://repo")
	if err != nil || subject.SHA != revisions[0].SHA || subject.Repository != revisions[0].Repository {
		t.Fatalf("valid source rejected: %#v %v", subject, err)
	}
	if _, err := qaSourceSubjectForTask(&task, encode([]any{change, change}), "workspace://repo"); err == nil {
		t.Fatal("ambiguous source admitted")
	}
	if _, err := qaSourceSubjectForTask(&task, raw, "workspace://other"); err == nil {
		t.Fatal("unselected workspace admitted")
	}
	task.EvidenceSubjectDigest = strings.Repeat("b", 64)
	if _, err := qaSourceSubjectForTask(&task, raw, "workspace://repo"); err == nil {
		t.Fatal("different frozen matrix admitted")
	}
	task.EvidenceSubjectDigest = digest
	change["commit_sha"] = strings.Repeat("c", 40)
	if _, err := qaSourceSubjectForTask(&task, encode([]any{change}), "workspace://repo"); err == nil {
		t.Fatal("substituted SHA admitted")
	}
}
