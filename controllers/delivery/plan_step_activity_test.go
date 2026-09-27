package delivery

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
)

func TestPlanStepActivityCursorAndProjectionAreScopedAndAllowListed(t *testing.T) {
	planID, stepID, eventID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	at := time.Date(2026, 9, 23, 14, 30, 5, 123000000, time.UTC)
	scope := planStepActivityCursorScope(planID, stepID)
	encoded := encodePlanStepActivityCursor(deliveryPlanStepActivityCursor{Version: 1, Scope: scope, OccurredAt: at, ID: eventID.String()})
	decoded, err := decodePlanStepActivityCursor(encoded, scope)
	if err != nil || decoded == nil || decoded.ID != eventID.String() || !decoded.OccurredAt.Equal(at) {
		t.Fatalf("activity cursor did not round-trip: %#v, %v", decoded, err)
	}
	if _, err := decodePlanStepActivityCursor(encoded, planStepActivityCursorScope(planID, uuid.Must(uuid.NewV4()))); err == nil {
		t.Fatal("cursor for another step must be rejected")
	}
	if _, err := decodePlanStepActivityCursor("not-a-cursor", scope); err == nil {
		t.Fatal("malformed cursor must be rejected")
	}
	if got, err := planStepActivityPageSize(""); err != nil || got != defaultPlanStepActivityPageSize {
		t.Fatalf("default page size = %d, %v", got, err)
	}
	for _, raw := range []string{"0", "-1", "101", "many"} {
		if _, err := planStepActivityPageSize(raw); err == nil {
			t.Fatalf("invalid page size %q was accepted", raw)
		}
	}
	unsafe := models.DeliveryPlanStepActivityEvent{
		ID: eventID, Sequence: 7, Action: "inference", Phase: "completed", ToolName: "Bearer_secret",
		AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: "prompt secret", WorkerID: "not-a-worker-uuid",
		AgentKey: "Generalist", MachineID: "C:\\Users\\secret", Summary: "Authorization: Bearer secret", OccurredAt: at,
	}
	instanceID := uuid.Must(uuid.NewV4())
	unsafe.AgentInstanceID = &instanceID
	dto := safePlanStepActivityDTO(unsafe)
	encodedDTO, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encodedDTO)
	if dto.Summary != "Inference completed" || dto.ToolName != "" || dto.RunID != "" || dto.WorkerID != "" || dto.AgentKey != "" || dto.MachineID != "" || dto.DurationMS != nil {
		t.Fatalf("untrusted persisted values escaped the safe projection: %#v", dto)
	}
	for _, forbidden := range []string{"Authorization", "Bearer_secret", "C:\\\\Users", "prompt secret", "fencing_token", "command"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("safe projection exposed %q: %s", forbidden, body)
		}
	}
	if dto.AgentInstanceID == nil || *dto.AgentInstanceID != instanceID {
		t.Fatalf("authorized projection omitted registered agent instance ID: %#v", dto)
	}
	for _, expected := range []string{`"id"`, `"sequence"`, `"action"`, `"phase"`, `"tool_name"`, `"automation_task_id"`, `"agent_instance_id"`, `"duration_ms"`, `"occurred_at"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("projection omitted contract field %s: %s", expected, body)
		}
	}
}

func TestPlanStepActivityInferenceProjectionIsReceiptDerivedAndExact(t *testing.T) {
	eventID, stepID, taskID, runID, workerID, machineID, callID, receiptID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	event := models.DeliveryPlanStepActivityEvent{
		ID: eventID, StepID: stepID, AutomationTaskID: taskID, RunID: runID.String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(),
		Action: models.DeliveryPlanStepActivityInference, Phase: models.DeliveryPlanStepActivityCompleted, InferenceCallID: &callID, InferenceReceiptID: &receiptID,
	}
	receiptStepID := stepID
	receipt := planStepActivityInferenceReceiptRow{
		ID: receiptID, PlanStepID: &receiptStepID, CallID: callID, AutomationTaskID: taskID, RunID: runID.String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(),
		Status: "accepted", Provider: "openrouter", Model: "deepseek/deepseek-chat-v3.1:free", InputTokens: 120, OutputTokens: 33, CachedInputTokens: 5,
		CacheWriteTokens: 2, ReasoningTokens: 4, TotalTokens: 153, TotalCostMicros: 47, Currency: "USD", PricingBasis: "catalog_v1",
	}
	projection, ok := safePlanStepActivityInferenceProjection(event, receipt)
	if !ok || projection == nil || projection.ReceiptID != receiptID || projection.TotalCostMicrousd != 47 || projection.InputTokens != 120 || projection.ReasoningTokens != 4 {
		t.Fatalf("exact canonical receipt projection failed: %#v, ok=%v", projection, ok)
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"receipt_id"`, `"provider"`, `"model"`, `"status"`, `"input_tokens"`, `"output_tokens"`, `"cached_input_tokens"`, `"cache_write_tokens"`, `"reasoning_tokens"`, `"total_tokens"`, `"total_cost_microusd"`, `"currency"`, `"pricing_basis"`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("receipt projection omitted %s: %s", key, encoded)
		}
	}
	for _, forbidden := range []string{"call_id", "prompt", "completion", "credential", "reasoning_content", "pricing_snapshot", "usage_json"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("receipt projection exposed %q: %s", forbidden, encoded)
		}
	}
	for _, mutate := range []func(*models.DeliveryPlanStepActivityEvent, *planStepActivityInferenceReceiptRow){
		func(_ *models.DeliveryPlanStepActivityEvent, receipt *planStepActivityInferenceReceiptRow) {
			receipt.CallID = uuid.Must(uuid.NewV4())
		},
		func(_ *models.DeliveryPlanStepActivityEvent, receipt *planStepActivityInferenceReceiptRow) {
			receipt.AutomationTaskID = uuid.Must(uuid.NewV4())
		},
		func(_ *models.DeliveryPlanStepActivityEvent, receipt *planStepActivityInferenceReceiptRow) {
			receipt.RunID = uuid.Must(uuid.NewV4()).String()
		},
		func(_ *models.DeliveryPlanStepActivityEvent, receipt *planStepActivityInferenceReceiptRow) {
			wrongStep := uuid.Must(uuid.NewV4())
			receipt.PlanStepID = &wrongStep
		},
		func(_ *models.DeliveryPlanStepActivityEvent, receipt *planStepActivityInferenceReceiptRow) {
			receipt.WorkerID = uuid.Must(uuid.NewV4()).String()
		},
		func(_ *models.DeliveryPlanStepActivityEvent, receipt *planStepActivityInferenceReceiptRow) {
			receipt.Status = "reserved"
		},
	} {
		badEvent, badReceipt := event, receipt
		mutate(&badEvent, &badReceipt)
		if _, ok := safePlanStepActivityInferenceProjection(badEvent, badReceipt); ok {
			t.Fatalf("mismatched or unresolved canonical receipt was exposed: %#v", badReceipt)
		}
	}
	if _, ok := safePlanStepActivityInferenceProjection(event, planStepActivityInferenceReceiptRow{}); ok {
		t.Fatal("unknown receipt was exposed")
	}
}

func TestListPlanStepActivityHidesCrossOrganizationScope(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configurePlanStepsNonRootAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, ownerID, otherOrganizationID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectPlanStepEventOrganizationScope(mock, planID, workItemID, projectID, ownerID)
	expectDeliveryOrganizationClientIDs(mock, otherOrganizationID, otherOrganizationID)
	ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/activity", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	setPlanStepEventTestOrganizationContext(ctx, otherOrganizationID)
	_ = ListPlanStepActivity(ctx) // The shared scoped guard writes the generic 404 and stops the handler.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-organization activity status = %d, want generic 404: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{planID.String(), stepID.String(), projectID.String(), ownerID.String(), otherOrganizationID.String()} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("cross-organization denial disclosed %q: %s", forbidden, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPlanStepActivityPagesByOccurredAtAndID(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepsTestDB(t)
	defer restoreDB()
	configureEpicTestAuth(t)
	mock := *mockPtr
	planID, workItemID, projectID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstID, secondID, thirdID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	agentInstanceID := uuid.Must(uuid.NewV4())
	taskID, runID, workerID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	firstAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	secondAt, thirdAt := firstAt.Add(-time.Minute), firstAt.Add(-2*time.Minute)
	eventQuery := regexp.QuoteMeta(`SELECT "id","step_id","automation_task_id","run_id","worker_id","agent_key","machine_id","agent_instance_id","sequence","action","phase","tool_name","duration_ms","summary","details_json","occurred_at" FROM "delivery_plan_step_activity_events" WHERE plan_id = $1 AND step_id = $2 ORDER BY occurred_at DESC, id DESC LIMIT $3`)
	secondPageQuery := regexp.QuoteMeta(`SELECT "id","step_id","automation_task_id","run_id","worker_id","agent_key","machine_id","agent_instance_id","sequence","action","phase","tool_name","duration_ms","summary","details_json","occurred_at" FROM "delivery_plan_step_activity_events" WHERE (plan_id = $1 AND step_id = $2) AND ((occurred_at < $3 OR (occurred_at = $4 AND id < $5))) ORDER BY occurred_at DESC, id DESC LIMIT $6`)
	setCommon := func() {
		expectPlanStepsPlan(mock, planID, workItemID, 9, `{}`, false)
		expectPlanStepsWorkItem(mock, workItemID, projectID)
		mock.ExpectQuery(`SELECT "id","plan_id" FROM "delivery_plan_steps" WHERE plan_id = \$1 AND id = \$2 ORDER BY "delivery_plan_steps"\."id" LIMIT \$3`).
			WithArgs(planID, stepID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(stepID, planID))
	}
	setCommon()
	mock.ExpectQuery(eventQuery).WithArgs(planID, stepID, 3).WillReturnRows(planStepActivityRows(
		planStepActivityRow(firstID, 1, "inference", "started", "", nil, taskID, runID, workerID, machineID, "private summary", firstAt, "", agentInstanceID),
		planStepActivityRow(secondID, 2, "file_change", "completed", "", int64Ptr(19), taskID, runID, workerID, machineID, "secret-bearing stored summary", secondAt, `{"resource_references":["workspace://repo"],"changed_files":["src/orders.go"]}`),
		planStepActivityRow(thirdID, 3, "validation", "failed", "", nil, taskID, runID, workerID, machineID, "Validation failed", thirdAt),
	))
	ctx, recorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/activity?limit=2", []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	if err := ListPlanStepActivity(ctx); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data deliveryPlanStepActivityPage `json:"data"`
	}
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &envelope) != nil {
		t.Fatalf("first page status = %d: %s", recorder.Code, recorder.Body.String())
	}
	page := envelope.Data
	if page.PlanID != planID || page.PlanVersion != 9 || page.StepID != stepID || len(page.Items) != 2 || page.Items[0].ID != firstID || page.Items[1].ID != secondID || page.NextCursor == "" {
		t.Fatalf("unexpected activity page/order: %#v", page)
	}
	if page.Items[0].Summary != "Inference started" || page.Items[1].Summary != "File change completed" || page.Items[1].DurationMS == nil || *page.Items[1].DurationMS != 19 || page.Items[0].AgentInstanceID == nil || *page.Items[0].AgentInstanceID != agentInstanceID {
		t.Fatalf("activity DTO did not use fixed safe summary or duration: %#v", page.Items)
	}
	if page.Items[0].Details != nil || page.Items[1].Details == nil || len(page.Items[1].Details.ChangedFiles) != 1 || page.Items[1].Details.ChangedFiles[0] != "src/orders.go" || page.Items[1].Details.ResourceReferences[0] != "workspace://repo" {
		t.Fatalf("authorized activity projection lost safe typed details or included start details: %#v", page.Items)
	}
	if strings.Contains(recorder.Body.String(), "secret-bearing") || strings.Contains(recorder.Body.String(), "fencing_token") {
		t.Fatalf("activity response leaked untrusted summary or private fields: %s", recorder.Body.String())
	}
	cursor, err := decodePlanStepActivityCursor(page.NextCursor, planStepActivityCursorScope(planID, stepID))
	if err != nil || cursor == nil || cursor.ID != secondID.String() || !cursor.OccurredAt.Equal(secondAt) {
		t.Fatalf("next cursor did not anchor the last row: %#v, %v", cursor, err)
	}

	setCommon()
	mock.ExpectQuery(secondPageQuery).WithArgs(planID, stepID, secondAt, secondAt, secondID, 3).WillReturnRows(planStepActivityRows(
		planStepActivityRow(thirdID, 3, "validation", "failed", "", nil, taskID, runID, workerID, machineID, "not exposed", thirdAt),
	))
	query := url.Values{"limit": {"2"}, "cursor": {page.NextCursor}}
	secondCtx, secondRecorder := planStepEventTestContext(http.MethodGet, "/api/automation/plans/"+planID.String()+"/steps/"+stepID.String()+"/activity?"+query.Encode(), []string{"id", "stepId"}, []string{planID.String(), stepID.String()}, "")
	if err := ListPlanStepActivity(secondCtx); err != nil {
		t.Fatal(err)
	}
	var secondEnvelope struct {
		Data deliveryPlanStepActivityPage `json:"data"`
	}
	if secondRecorder.Code != http.StatusOK || json.Unmarshal(secondRecorder.Body.Bytes(), &secondEnvelope) != nil || len(secondEnvelope.Data.Items) != 1 || secondEnvelope.Data.Items[0].ID != thirdID || secondEnvelope.Data.NextCursor != "" {
		t.Fatalf("unexpected second activity page: %d %s", secondRecorder.Code, secondRecorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func planStepActivityRows(items ...models.DeliveryPlanStepActivityEvent) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "step_id", "automation_task_id", "run_id", "worker_id", "agent_key", "machine_id", "agent_instance_id", "sequence", "action", "phase", "tool_name", "duration_ms", "summary", "details_json", "occurred_at"})
	for _, item := range items {
		var instanceID any
		if item.AgentInstanceID != nil {
			instanceID = item.AgentInstanceID.String()
		}
		rows.AddRow(item.ID, item.StepID, item.AutomationTaskID, item.RunID, item.WorkerID, item.AgentKey, item.MachineID, instanceID, item.Sequence, item.Action, item.Phase, item.ToolName, item.DurationMS, item.Summary, item.DetailsJSON, item.OccurredAt)
	}
	return rows
}

func planStepActivityRow(id uuid.UUID, sequence int64, action, phase, toolName string, duration *int64, taskID, runID, workerID, machineID uuid.UUID, summary string, occurredAt time.Time, details ...any) models.DeliveryPlanStepActivityEvent {
	detailsJSON := "{}"
	if len(details) > 0 {
		if raw, ok := details[0].(string); ok {
			detailsJSON = raw
		}
	}
	event := models.DeliveryPlanStepActivityEvent{
		ID: id, Sequence: sequence, Action: action, Phase: phase, ToolName: toolName, DurationMS: duration,
		AutomationTaskID: taskID, RunID: runID.String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(),
		Summary: summary, DetailsJSON: detailsJSON, OccurredAt: occurredAt,
	}
	if len(details) > 1 {
		if instanceID, ok := details[1].(uuid.UUID); ok && instanceID != uuid.Nil {
			event.AgentInstanceID = &instanceID
		}
	}
	return event
}

func int64Ptr(value int64) *int64 { return &value }
