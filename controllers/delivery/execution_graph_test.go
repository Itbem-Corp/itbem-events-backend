package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

func TestBuildExecutionGraphConnectsAutomationRecordsWithoutLeakingPrivateData(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	dependencyID := uuid.Must(uuid.NewV4())
	planTaskID := uuid.Must(uuid.NewV4())
	qaTaskID := uuid.Must(uuid.NewV4())
	executionID := uuid.Must(uuid.NewV4())
	toolID := uuid.Must(uuid.NewV4())
	gateID := uuid.Must(uuid.NewV4())
	evidenceID := uuid.Must(uuid.NewV4())
	messageID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	completedAt := createdAt.Add(3 * time.Minute)
	capturedAt := completedAt.Add(time.Minute)

	input := executionGraphBuildInput{
		WorkItem: models.DeliveryWorkItem{
			ID: workItemID, Title: "Actualizar control de automatización", State: "implementation", CreatedAt: createdAt, UpdatedAt: completedAt,
		},
		Dependencies: []models.DeliveryWorkItemDependency{{
			WorkItemID: workItemID, DependsOnWorkItemID: dependencyID, CreatedAt: createdAt.Add(-time.Minute),
			DependsOn: models.DeliveryWorkItem{ID: dependencyID, Title: "Preparar contrato", State: "released", CreatedAt: createdAt.Add(-2 * time.Hour)},
		}},
		Tasks: []models.AutomationTask{
			{ID: planTaskID, RequestedBy: "operator", Operation: "delivery.plan", Status: "completed", AttemptCount: 2, Provider: "minimax", Model: "MiniMax-M3", CreatedAt: createdAt, UpdatedAt: completedAt, CompletedAt: &completedAt, ErrorMessage: "must-not-leak"},
			{ID: qaTaskID, RequestedBy: "operator", Operation: "delivery.qa", Status: "running", AttemptCount: 1, CreatedAt: completedAt, UpdatedAt: capturedAt},
		},
		Executions: []models.AutomationExecution{{
			ID: executionID, AutomationTaskID: planTaskID, StepKey: "plan", Provider: "minimax", Model: "MiniMax-M3", TotalTokens: 240, TotalCostMicros: 81, PricingBasis: "snapshot", RequestRef: "s3://private/request.json", ResponseRef: "s3://private/result.json", CompletedAt: completedAt,
		}},
		ToolCalls: []models.AutomationToolExecution{{
			ID: toolID, AutomationTaskID: qaTaskID, Tool: "stagehand", CallKey: "semantic-assessment", CallStatus: "completed", StepKey: "qa.semantic_browser", Provider: "minimax", Model: "MiniMax-M3", TotalTokens: 60, TotalCostMicros: 21, PricingBasis: "snapshot", RequestRef: "s3://private/report.json", ResponseRef: "s3://private/report.json", CompletedAt: capturedAt,
		}},
		Gates: []models.DeliveryGate{{
			ID: gateID, Kind: "code_review", Decision: "changes_requested", Comment: "sensitive reviewer message", EvidenceChecklist: `["reviewed"]`, DecidedAt: capturedAt,
		}},
		Evidence: []models.DeliveryEvidence{{
			ID: evidenceID, Kind: "screenshot", Phase: "qa", Title: "QA visual · Móvil", Reference: "s3://private/asset.png", MetadataJSON: `{"automation_task_id":"` + qaTaskID.String() + `","private_reference":"must-not-leak"}`, CapturedAt: &capturedAt,
		}},
		Messages: []models.DeliveryMessage{{
			ID: messageID, Phase: "plan_review", AuthorType: "human", Body: "private human message must-not-leak", CreatedAt: capturedAt,
		}},
		ViewerID:    "operator",
		CanManage:   false,
		GeneratedAt: capturedAt.Add(time.Minute),
	}

	snapshot := buildExecutionGraph(input)
	if !snapshot.Live || snapshot.SchemaVersion != 1 || snapshot.WorkItemID != workItemID {
		t.Fatalf("unexpected graph snapshot: %#v", snapshot)
	}
	if len(snapshot.Nodes) != 9 || len(snapshot.Edges) != 8 {
		t.Fatalf("graph should retain every linked record, nodes=%d edges=%d", len(snapshot.Nodes), len(snapshot.Edges))
	}

	planNode := executionGraphFindNode(t, snapshot.Nodes, executionGraphTaskNodeID(planTaskID))
	if planNode.Status != "completed" || planNode.TrackID != "delivery.plan" || planNode.Metadata["attempt_count"] != 2 || !executionGraphHasAction(planNode.Actions, "open_trace") || !executionGraphHasAction(planNode.Actions, "open_result") {
		t.Fatalf("plan task lost graph/action metadata: %#v", planNode)
	}
	qaNode := executionGraphFindNode(t, snapshot.Nodes, executionGraphTaskNodeID(qaTaskID))
	if qaNode.Status != "running" || !executionGraphHasAction(qaNode.Actions, "cancel") {
		t.Fatalf("active owned task must be actionable: %#v", qaNode)
	}
	evidenceNode := executionGraphFindNode(t, snapshot.Nodes, executionGraphEvidenceNodeID(evidenceID))
	if evidenceNode.ParentID != executionGraphTaskNodeID(qaTaskID) || evidenceNode.TrackID != "delivery.qa" {
		t.Fatalf("automation evidence was not connected to its actual task: %#v", evidenceNode)
	}
	gateNode := executionGraphFindNode(t, snapshot.Nodes, executionGraphGateNodeID(gateID))
	if gateNode.Status != "attention" || gateNode.Metadata["kind"] != "code_review" || gateNode.Metadata["decision"] != "changes_requested" {
		t.Fatalf("gate state or safe metadata lost: %#v", gateNode)
	}
	toolNode := executionGraphFindNode(t, snapshot.Nodes, executionGraphToolExecutionNodeID(toolID))
	if toolNode.Status != "completed" || !executionGraphHasAction(toolNode.Actions, "open_tool_report") {
		t.Fatalf("tool call must retain a report inspector action: %#v", toolNode)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"s3://", "must-not-leak", "private_reference", "sensitive reviewer message", "private human message", "request_ref", "response_ref"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("execution graph leaked %q: %s", forbidden, encoded)
		}
	}

	input.GeneratedAt = input.GeneratedAt.Add(2 * time.Minute)
	if repeated := buildExecutionGraph(input); repeated.Revision != snapshot.Revision {
		t.Fatalf("graph revision should ignore per-request generation time: %q != %q", repeated.Revision, snapshot.Revision)
	}
}

func TestExecutionGraphStatusesAndOperatorActionsAreBounded(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"queued", "queued"},
		{"running", "running"},
		{"completed", "completed"},
		{"failed", "failed"},
		{"dispatch_failed", "failed"},
		{"cancel_requested", "attention"},
		{"cancelled", "cancelled"},
		{"unexpected", "unknown"},
	} {
		if got := executionGraphTaskStatus(test.input); got != test.want {
			t.Fatalf("task status %q = %q, want %q", test.input, got, test.want)
		}
	}

	taskID := uuid.Must(uuid.NewV4())
	queued := models.AutomationTask{ID: taskID, RequestedBy: "owner", Status: "queued"}
	if executionGraphHasAction(executionGraphTaskActions(queued, "viewer", false), "cancel") {
		t.Fatal("viewer without task ownership or delivery management cannot receive cancel action")
	}
	if !executionGraphHasAction(executionGraphTaskActions(queued, "owner", false), "cancel") {
		t.Fatal("task owner must receive a cancel action for a queued task")
	}
	if !executionGraphHasAction(executionGraphTaskActions(queued, "viewer", true), "cancel") {
		t.Fatal("delivery manager must receive a cancel action for a queued task")
	}
	cancelling := queued
	cancelling.Status = "cancel_requested"
	if executionGraphHasAction(executionGraphTaskActions(cancelling, "owner", true), "cancel") {
		t.Fatal("a cancellation already in flight must not expose a second cancel action")
	}
	if got := executionGraphExecutionStatus("failed"); got != "failed" {
		t.Fatalf("a billable execution attached to a failed task must remain visibly failed, got %q", got)
	}
	if got := executionGraphExecutionStatus("running"); got != "completed" {
		t.Fatalf("a completed immutable call should stay completed while a multi-step task runs, got %q", got)
	}
	if !executionGraphETagMatches(`W/"abc", "other"`, `"abc"`) || executionGraphETagMatches(`"other"`, `"abc"`) {
		t.Fatal("ETag matching should support normal conditional GET forms without accepting another revision")
	}
}

func TestExecutionGraphEvidenceLinkingAndTextSanitizationFailClosed(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	if linked, ok := executionGraphEvidenceTaskID(`{"automation_task_id":"` + taskID.String() + `"}`); !ok || linked != taskID {
		t.Fatalf("expected task link: %s / %v", linked, ok)
	}
	for _, raw := range []string{"", `{}`, `{"automation_task_id":"not-a-uuid"}`, `[]`} {
		if _, ok := executionGraphEvidenceTaskID(raw); ok {
			t.Fatalf("invalid evidence metadata unexpectedly linked: %s", raw)
		}
	}
	if got := executionGraphText(" one\n two\tthree ", "fallback"); got != "one two three" {
		t.Fatalf("graph labels must collapse control whitespace, got %q", got)
	}
}

func executionGraphFindNode(t *testing.T, nodes []executionGraphNode, id string) executionGraphNode {
	t.Helper()
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("node %q not found in %#v", id, nodes)
	return executionGraphNode{}
}

func executionGraphHasAction(actions []executionGraphAction, id string) bool {
	for _, action := range actions {
		if action.ID == id {
			return true
		}
	}
	return false
}
