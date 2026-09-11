package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/deliverypolicy"
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

func TestExecutionGraphShowsActivePRPublicationAsRunning(t *testing.T) {
	if got := executionGraphWorkItemStatus("code_review", true); got != "running" {
		t.Fatalf("active bounded publication should be running, got %q", got)
	}
	if got := executionGraphWorkItemStatus("code_review", false); got != "decision" {
		t.Fatalf("code review without active publication should await a decision, got %q", got)
	}
}

func TestBuildExecutionGraphProjectsVerifiedAuthorityAndFailsClosedOnTampering(t *testing.T) {
	workItemID, projectID, eventID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	occurredAt := time.Date(2026, time.September, 11, 7, 0, 0, 0, time.UTC)
	valid := executionGraphAutonomySnapshotEvent(t, workItemID, projectID, eventID, occurredAt)
	input := executionGraphBuildInput{
		WorkItem:    models.DeliveryWorkItem{ID: workItemID, Title: "Autoridad congelada", State: "implementation", CreatedAt: occurredAt, UpdatedAt: occurredAt},
		Events:      []models.DeliveryEvent{valid},
		GeneratedAt: occurredAt.Add(time.Minute),
	}

	snapshot := buildExecutionGraph(input)
	authority := executionGraphFindNode(t, snapshot.Nodes, executionGraphAuthorityNodeID(eventID))
	if authority.Status != "completed" || authority.Detail != "Gates delegados con evidencia independiente obligatoria" || authority.Metadata["verified"] != true || authority.Metadata["delegated"] != true || authority.Metadata["repositories"] != 1 {
		t.Fatalf("verified authority projection lost its safe status: %#v", authority)
	}
	if edge := executionGraphFindEdge(t, snapshot.Edges, executionGraphEdgeID(executionGraphWorkItemNodeID(workItemID), authority.ID, "freezes_authority")); edge.Kind != "freezes_authority" || edge.Status != "completed" {
		t.Fatalf("authority provenance edge is missing or unsafe: %#v", edge)
	}

	tampered := valid
	tampered.PayloadDigest = strings.Repeat("0", 64)
	input.Events = []models.DeliveryEvent{tampered}
	failed := buildExecutionGraph(input)
	blockedAuthority := executionGraphFindNode(t, failed.Nodes, executionGraphAuthorityNodeID(eventID))
	if blockedAuthority.Status != "attention" || blockedAuthority.Metadata["verified"] != false || blockedAuthority.Detail != "La evidencia de autoridad no pasó la verificación" {
		t.Fatalf("invalid authority evidence must fail closed in the graph: %#v", blockedAuthority)
	}
	if edge := executionGraphFindEdge(t, failed.Edges, executionGraphEdgeID(executionGraphWorkItemNodeID(workItemID), blockedAuthority.ID, "freezes_authority")); edge.Status != "attention" {
		t.Fatalf("invalid authority edge must remain visibly blocked: %#v", edge)
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

func executionGraphFindEdge(t *testing.T, edges []executionGraphEdge, id string) executionGraphEdge {
	t.Helper()
	for _, edge := range edges {
		if edge.ID == id {
			return edge
		}
	}
	t.Fatalf("edge %q not found in %#v", id, edges)
	return executionGraphEdge{}
}

func executionGraphAutonomySnapshotEvent(t *testing.T, workItemID, projectID, eventID uuid.UUID, occurredAt time.Time) models.DeliveryEvent {
	t.Helper()
	changeSetID := "11111111-1111-4111-8111-111111111111"
	repository := "github://itbem/service"
	policyDigest := strings.Repeat("c", 64)
	input := deliveryledger.AutonomySnapshotInput{
		ProjectID: projectID, ChangeSetID: changeSetID,
		Repositories: []deliveryledger.AutonomyRepository{{
			Repository: repository, SourceReference: "workspace://service", SourceRevision: strings.Repeat("a", 40),
			VaultRevisionID: uuid.Must(uuid.NewV4()).String(), VaultVersion: 1, VaultRevision: strings.Repeat("b", 40), VaultDigest: strings.Repeat("d", 64),
			Policy: deliverypolicy.ResolvedPolicy{
				Context:  deliverypolicy.Context{ProjectID: projectID.String(), Repository: repository, ChangeSetID: changeSetID},
				Resolved: true, GateApprovalMode: deliverypolicy.GateApprovalDelegated, Digest: policyDigest,
			},
		}},
	}
	payload, err := json.Marshal(struct {
		SchemaVersion int                                  `json:"schema_version"`
		Input         deliveryledger.AutonomySnapshotInput `json:"input"`
	}{SchemaVersion: 1, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	payloadSum := sha256.Sum256(payload)
	subjectSum := sha256.Sum256([]byte(strings.ToLower(repository) + "|" + strings.Repeat("a", 40) + "|" + strings.Repeat("d", 64) + "|" + policyDigest))
	return models.DeliveryEvent{
		ID: eventID, WorkItemID: workItemID, Sequence: 1, EventType: deliveryledger.EventTypeAutonomySnapshot,
		SubjectDigest: hex.EncodeToString(subjectSum[:]), PayloadJSON: string(payload), PayloadDigest: hex.EncodeToString(payloadSum[:]),
		OccurredAt: occurredAt, CreatedAt: occurredAt,
	}
}

func executionGraphHasAction(actions []executionGraphAction, id string) bool {
	for _, action := range actions {
		if action.ID == id {
			return true
		}
	}
	return false
}
