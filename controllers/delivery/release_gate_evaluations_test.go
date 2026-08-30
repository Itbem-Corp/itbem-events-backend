package delivery

import (
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

func TestBuildReleaseGateEvaluationSnapshotStartsWithAnExplicitEmptyCollection(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	snapshot, err := buildReleaseGateEvaluationSnapshot(workItemID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.WorkItemID != workItemID || snapshot.Evaluations == nil || len(snapshot.Evaluations) != 0 || snapshot.Truncated {
		t.Fatalf("unexpected empty release gate snapshot: %#v", snapshot)
	}
}

func TestBuildReleaseGateEvaluationSnapshotRejectsCrossWorkItemEvidence(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	events := []models.DeliveryEvent{{WorkItemID: uuid.Must(uuid.NewV4())}}
	if _, err := buildReleaseGateEvaluationSnapshot(workItemID, events); err == nil {
		t.Fatal("a release gate event from another work item must fail closed")
	}
	if _, err := buildReleaseGateEvaluationSnapshot(uuid.Nil, nil); err == nil {
		t.Fatal("a missing work item identity must fail closed")
	}
}
