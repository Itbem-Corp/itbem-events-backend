package automation

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestPlanStepActivityCallbackRequiresEnrolledInstanceAndRejectsUnknownPayload(t *testing.T) {
	stepID := uuid.Must(uuid.NewV4())
	for _, test := range []struct {
		name          string
		authenticated bool
		body          string
		wantStatus    int
	}{
		{name: "missing instance", body: `{}`, wantStatus: http.StatusUnauthorized},
		{name: "body cannot set instance attribution", authenticated: true, body: `{"agent_instance_id":"` + uuid.Must(uuid.NewV4()).String() + `"}`, wantStatus: http.StatusBadRequest},
		{name: "unknown raw content", authenticated: true, body: `{"action":"command","command":"private-command-marker"}`, wantStatus: http.StatusBadRequest},
		{name: "unknown nested detail field", authenticated: true, body: `{"details":{"stdout":"private-output-marker"}}`, wantStatus: http.StatusBadRequest},
		{name: "unsafe nested path", authenticated: true, body: `{"details":{"changed_files":["../.env.local"]}}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/steps/"+stepID.String()+"/activity", strings.NewReader(test.body))
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.SetParamNames("id")
			ctx.SetParamValues(stepID.String())
			if test.authenticated {
				ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String()})
			}
			if err := RecordDeliveryPlanStepActivity(ctx); err != nil {
				t.Fatalf("handler returned an unhandled error: %v", err)
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "private-command-marker") || strings.Contains(recorder.Body.String(), "private-output-marker") {
				t.Fatalf("callback response echoed private payload: %s", recorder.Body.String())
			}
		})
	}
}

func TestNormalizePlanStepActivityRequestAllowsOnlyBoundedSafeFields(t *testing.T) {
	taskID, runID, workerID, machineID, eventID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	duration := int64(37)
	valid := planStepActivityRequest{
		EventID: eventID.String(), TaskID: taskID.String(), RunID: runID.String(), WorkerID: workerID.String(),
		AgentKey: "generalist", MachineID: machineID.String(), FencingToken: "9", Sequence: 1,
		Action: models.DeliveryPlanStepActivityTool, Phase: models.DeliveryPlanStepActivityCompleted,
		ToolName: "stagehand_click", DurationMS: &duration,
	}
	parsed, err := normalizePlanStepActivityRequest(valid)
	if err != nil || parsed.EventID != eventID || parsed.Fence != 9 || parsed.ToolName != "stagehand_click" || parsed.DurationMS == nil || *parsed.DurationMS != duration {
		t.Fatalf("valid activity payload was rejected or normalized incorrectly: %#v, %v", parsed, err)
	}

	bad := []struct {
		name   string
		mutate func(*planStepActivityRequest)
	}{
		{name: "invalid action", mutate: func(value *planStepActivityRequest) { value.Action = "prompt" }},
		{name: "invalid phase", mutate: func(value *planStepActivityRequest) { value.Phase = "reasoning" }},
		{name: "tool name can not be arbitrary text", mutate: func(value *planStepActivityRequest) { value.ToolName = "Bearer sk-secret" }},
		{name: "tool name only belongs to tool action", mutate: func(value *planStepActivityRequest) { value.Action = models.DeliveryPlanStepActivityInference }},
		{name: "sequence positive", mutate: func(value *planStepActivityRequest) { value.Sequence = 0 }},
		{name: "fence positive", mutate: func(value *planStepActivityRequest) { value.FencingToken = "0" }},
		{name: "started event has no duration", mutate: func(value *planStepActivityRequest) { value.Phase = models.DeliveryPlanStepActivityStarted }},
		{name: "duration bounded", mutate: func(value *planStepActivityRequest) {
			tooLong := maxPlanStepActivityDurationMS + 1
			value.DurationMS = &tooLong
		}},
		{name: "event id valid", mutate: func(value *planStepActivityRequest) { value.EventID = "not-a-uuid" }},
		{name: "started details forbidden", mutate: func(value *planStepActivityRequest) {
			value.Details = &models.DeliveryPlanStepActivityDetails{ChangedFiles: []string{"src/a.go"}}
		}},
		{name: "unsafe resource traversal rejected", mutate: func(value *planStepActivityRequest) {
			value.Action, value.Phase, value.ToolName = models.DeliveryPlanStepActivityFileRead, models.DeliveryPlanStepActivityCompleted, ""
			value.Details = &models.DeliveryPlanStepActivityDetails{ResourceReferences: []string{"workspace://repo/../../.env"}}
		}},
		{name: "secret-like changed file rejected", mutate: func(value *planStepActivityRequest) {
			value.Action, value.Phase, value.ToolName = models.DeliveryPlanStepActivityFileChange, models.DeliveryPlanStepActivityCompleted, ""
			value.Details = &models.DeliveryPlanStepActivityDetails{ChangedFiles: []string{"config/api_token.json"}}
		}},
		{name: "common credential files rejected", mutate: func(value *planStepActivityRequest) {
			value.Action, value.Phase, value.ToolName = models.DeliveryPlanStepActivityFileChange, models.DeliveryPlanStepActivityCompleted, ""
			value.Details = &models.DeliveryPlanStepActivityDetails{ChangedFiles: []string{".npmrc"}}
		}},
		{name: "cloud credential directories rejected", mutate: func(value *planStepActivityRequest) {
			value.Action, value.Phase, value.ToolName = models.DeliveryPlanStepActivityFileChange, models.DeliveryPlanStepActivityCompleted, ""
			value.Details = &models.DeliveryPlanStepActivityDetails{ChangedFiles: []string{".aws/credentials"}}
		}},
		{name: "unsupported executable rejected", mutate: func(value *planStepActivityRequest) {
			value.Action, value.Phase, value.ToolName = models.DeliveryPlanStepActivityCommand, models.DeliveryPlanStepActivityCompleted, ""
			args, code, output := 2, 0, 17
			value.Details = &models.DeliveryPlanStepActivityDetails{ExecutableName: "sh", ArgumentCount: &args, ExitCode: &code, CapturedOutputBytes: &output}
		}},
		{name: "acceptance evidence only on completed evidence action", mutate: func(value *planStepActivityRequest) {
			value.Action, value.Phase, value.ToolName = models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityFailed, ""
			value.Details = validAcceptanceActivityDetails("criterion")
		}},
		{name: "acceptance hashes must be lowercase sha256", mutate: func(value *planStepActivityRequest) {
			value.Action, value.Phase, value.ToolName = models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted, ""
			value.Details = validAcceptanceActivityDetails("criterion")
			value.Details.AcceptanceChecks[0].CriterionSHA256 = strings.ToUpper(value.Details.AcceptanceChecks[0].CriterionSHA256)
		}},
		{name: "acceptance attestation must pass", mutate: func(value *planStepActivityRequest) {
			value.Action, value.Phase, value.ToolName = models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted, ""
			value.Details = validAcceptanceActivityDetails("criterion")
			value.Details.AcceptanceChecks[0].Passed = false
		}},
	}
	for _, test := range bad {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if _, err := normalizePlanStepActivityRequest(candidate); err == nil {
				t.Fatalf("invalid activity payload was accepted: %#v", candidate)
			}
		})
	}
}

func TestNormalizePlanStepActivityRequiresOpaqueIDsOnlyForTerminalInference(t *testing.T) {
	request := planStepActivityRequest{
		EventID: uuid.Must(uuid.NewV4()).String(), TaskID: uuid.Must(uuid.NewV4()).String(), RunID: uuid.Must(uuid.NewV4()).String(),
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(),
		FencingToken: "3", Sequence: 2, Action: models.DeliveryPlanStepActivityInference, Phase: models.DeliveryPlanStepActivityCompleted,
		CallID: uuid.Must(uuid.NewV4()).String(), ReceiptID: uuid.Must(uuid.NewV4()).String(),
	}
	parsed, err := normalizePlanStepActivityRequest(request)
	if err != nil || parsed.CallID == nil || parsed.ReceiptID == nil || parsed.CallID.String() != request.CallID || parsed.ReceiptID.String() != request.ReceiptID {
		t.Fatalf("valid inference receipt IDs were rejected or changed: %#v, %v", parsed, err)
	}
	for _, mutate := range []struct {
		name string
		fn   func(*planStepActivityRequest)
	}{
		{name: "call id without receipt", fn: func(value *planStepActivityRequest) { value.ReceiptID = "" }},
		{name: "receipt id without call", fn: func(value *planStepActivityRequest) { value.CallID = "" }},
		{name: "non canonical call id", fn: func(value *planStepActivityRequest) { value.CallID = " " + value.CallID }},
		{name: "IDs on a non inference action", fn: func(value *planStepActivityRequest) { value.Action = models.DeliveryPlanStepActivityTool }},
		{name: "IDs on start", fn: func(value *planStepActivityRequest) { value.Phase = models.DeliveryPlanStepActivityStarted }},
		{name: "completed inference without receipt", fn: func(value *planStepActivityRequest) { value.CallID, value.ReceiptID = "", "" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			candidate := request
			mutate.fn(&candidate)
			if _, err := normalizePlanStepActivityRequest(candidate); err == nil {
				t.Fatalf("invalid inference identifiers were accepted: %#v", candidate)
			}
		})
	}
}

func TestPlanStepActivityInferenceReceiptJoinRequiresExactTaskRunWorkerAndInstance(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepActivityTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	taskID, runID, workerID, machineID, instanceID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	callID, receiptID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	task := models.AutomationTask{ID: taskID, RunID: runID.String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(), AgentInstanceID: &instanceID}
	parsed := normalizedPlanStepActivity{TaskID: taskID, RunID: runID.String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(), Action: models.DeliveryPlanStepActivityInference, Phase: models.DeliveryPlanStepActivityCompleted, CallID: &callID, ReceiptID: &receiptID}
	query := `(?s)SELECT \* FROM "automation_inference_receipts" WHERE .* ORDER BY "automation_inference_receipts"\."id" LIMIT \$[0-9]+ FOR SHARE`
	columns := []string{"id", "automation_task_id", "run_id", "call_id", "plan_step_id", "operation", "worker_id", "agent_key", "machine_id", "policy_snapshot_hash", "quota_limit", "status", "provider", "model", "input_tokens", "output_tokens", "cached_input_tokens", "cache_write_tokens", "reasoning_tokens", "total_tokens", "total_cost_micros", "currency", "pricing_basis", "pricing_snapshot_json", "usage_json", "created_at"}
	validReceipt := func() *sqlmock.Rows {
		return sqlmock.NewRows(columns).AddRow(receiptID, taskID, runID.String(), callID, stepID, "delivery.implementation", workerID.String(), "generalist", machineID.String(), strings.Repeat("a", 64), 10, "accepted", "openrouter", "deepseek/deepseek-chat-v3.1:free", 100, 30, 5, 0, 0, 130, 47, "USD", "test-catalog", `{}`, `{}`, time.Now().UTC())
	}
	expect := func(rows *sqlmock.Rows) {
		mock.ExpectQuery(query).WithArgs(receiptID, taskID, runID.String(), callID, workerID.String(), "generalist", machineID.String(), "accepted", "rejected", 1).WillReturnRows(rows)
	}
	expect(validReceipt())
	if err := validatePlanStepActivityInferenceReceipt(configuration.DB, task, instanceID, stepID, parsed); err != nil {
		t.Fatalf("exact canonical inference receipt was rejected: %v", err)
	}
	wrongInstanceID := uuid.Must(uuid.NewV4())
	wrongInstanceTask := task
	wrongInstanceTask.AgentInstanceID = &wrongInstanceID
	if err := validatePlanStepActivityInferenceReceipt(configuration.DB, wrongInstanceTask, instanceID, stepID, parsed); !errors.Is(err, deliveryplansteps.ErrStepLeaseConflict) {
		t.Fatalf("receipt from another active task instance should be rejected, got %v", err)
	}
	wrongTuple := sqlmock.NewRows(columns).AddRow(uuid.Must(uuid.NewV4()), taskID, runID.String(), uuid.Must(uuid.NewV4()), stepID, "delivery.implementation", workerID.String(), "other-agent", machineID.String(), strings.Repeat("a", 64), 10, "accepted", "openrouter", "deepseek/deepseek-chat-v3.1:free", 100, 30, 5, 0, 0, 130, 47, "USD", "test-catalog", `{}`, `{}`, time.Now().UTC())
	expect(wrongTuple)
	if err := validatePlanStepActivityInferenceReceipt(configuration.DB, task, instanceID, stepID, parsed); !errors.Is(err, deliveryplansteps.ErrStepLeaseConflict) {
		t.Fatalf("mismatched receipt tuple should be rejected, got %v", err)
	}
	wrongStep := uuid.Must(uuid.NewV4())
	wrongStepReceipt := sqlmock.NewRows(columns).AddRow(receiptID, taskID, runID.String(), callID, wrongStep, "delivery.implementation", workerID.String(), "generalist", machineID.String(), strings.Repeat("a", 64), 10, "accepted", "openrouter", "deepseek/deepseek-chat-v3.1:free", 100, 30, 5, 0, 0, 130, 47, "USD", "test-catalog", `{}`, `{}`, time.Now().UTC())
	expect(wrongStepReceipt)
	if err := validatePlanStepActivityInferenceReceipt(configuration.DB, task, instanceID, stepID, parsed); !errors.Is(err, deliveryplansteps.ErrStepLeaseConflict) {
		t.Fatalf("receipt issued for another plan step should be rejected, got %v", err)
	}
	unknown := sqlmock.NewRows(columns)
	expect(unknown)
	if err := validatePlanStepActivityInferenceReceipt(configuration.DB, task, instanceID, stepID, parsed); !errors.Is(err, deliveryplansteps.ErrStepLeaseConflict) {
		t.Fatalf("unknown receipt should be rejected, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanStepActivityAcceptanceEvidenceMatchesExactFrozenCriteria(t *testing.T) {
	criteria := []string{"Exact criterion with trailing space ", "Criterio UTF-8: café"}
	criteriaJSON, err := json.Marshal(criteria)
	if err != nil {
		t.Fatal(err)
	}
	details := &models.DeliveryPlanStepActivityDetails{
		AcceptanceChecks: []models.DeliveryPlanStepAcceptanceCheck{
			{CriterionSHA256: criterionSHA256(criteria[0]), Passed: true},
			{CriterionSHA256: criterionSHA256(criteria[1]), Passed: true},
		},
		ReviewDiffSHA256: criterionSHA256("review diff"),
	}
	event := acceptanceActivityEvent(details, models.DeliveryPlanStepActivityCompleted)
	if err := validatePlanStepActivityAcceptanceCriteria(string(criteriaJSON), event); err != nil {
		t.Fatalf("complete exact UTF-8 criterion hash set was rejected: %v", err)
	}
	if got := criterionSHA256(criteria[0]); got != fmt.Sprintf("%x", sha256.Sum256([]byte(criteria[0]))) {
		t.Fatalf("criterion digest is not the full SHA-256 of exact UTF-8 text: %q", got)
	}

	cases := []struct {
		name   string
		mutate func(*models.DeliveryPlanStepActivityDetails, *models.DeliveryPlanStepActivityEvent)
	}{
		{name: "omitted criterion", mutate: func(value *models.DeliveryPlanStepActivityDetails, _ *models.DeliveryPlanStepActivityEvent) {
			value.AcceptanceChecks = value.AcceptanceChecks[:1]
		}},
		{name: "duplicate criterion", mutate: func(value *models.DeliveryPlanStepActivityDetails, _ *models.DeliveryPlanStepActivityEvent) {
			value.AcceptanceChecks[1].CriterionSHA256 = value.AcceptanceChecks[0].CriterionSHA256
		}},
		{name: "extra criterion", mutate: func(value *models.DeliveryPlanStepActivityDetails, _ *models.DeliveryPlanStepActivityEvent) {
			value.AcceptanceChecks[1].CriterionSHA256 = criterionSHA256("not approved")
		}},
		{name: "failed check", mutate: func(value *models.DeliveryPlanStepActivityDetails, _ *models.DeliveryPlanStepActivityEvent) {
			value.AcceptanceChecks[1].Passed = false
		}},
		{name: "failed event phase", mutate: func(_ *models.DeliveryPlanStepActivityDetails, event *models.DeliveryPlanStepActivityEvent) {
			event.Phase = models.DeliveryPlanStepActivityFailed
		}},
		{name: "criterion text cannot be represented", mutate: func(value *models.DeliveryPlanStepActivityDetails, _ *models.DeliveryPlanStepActivityEvent) {
			value.AcceptanceChecks[0].CriterionSHA256 = "criterion text"
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := *details
			candidate.AcceptanceChecks = append([]models.DeliveryPlanStepAcceptanceCheck(nil), details.AcceptanceChecks...)
			candidateEvent := event
			test.mutate(&candidate, &candidateEvent)
			candidateEvent.DetailsJSON = acceptanceActivityEvent(&candidate, candidateEvent.Phase).DetailsJSON
			if err := validatePlanStepActivityAcceptanceCriteria(string(criteriaJSON), candidateEvent); err == nil {
				t.Fatal("invalid acceptance evidence was accepted")
			}
		})
	}
}

func TestPlanStepActivityAcceptanceEvidenceRejectsTextAndSecretPayloadFields(t *testing.T) {
	stepID := uuid.Must(uuid.NewV4())
	for _, property := range []string{`"criterion":"private acceptance text"`, `"output":"private output marker"`, `"prompt":"private prompt marker"`, `"api_key":"sk-secret-marker"`} {
		body := `{"event_id":"` + uuid.Must(uuid.NewV4()).String() + `","task_id":"` + uuid.Must(uuid.NewV4()).String() + `","run_id":"` + uuid.Must(uuid.NewV4()).String() + `","worker_id":"` + uuid.Must(uuid.NewV4()).String() + `","agent_key":"generalist","fencing_token":"2","sequence":1,"action":"evidence","phase":"completed","details":{"acceptance_checks":[{"criterion_sha256":"` + criterionSHA256("criterion") + `","passed":true,` + property + `}],"review_diff_sha256":"` + criterionSHA256("diff") + `"}}`
		request := httptest.NewRequest(http.MethodPost, "/steps/"+stepID.String()+"/activity", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		ctx.SetParamNames("id")
		ctx.SetParamValues(stepID.String())
		ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String()})
		if err := RecordDeliveryPlanStepActivity(ctx); err != nil {
			t.Fatalf("handler returned an unhandled error: %v", err)
		}
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("unapproved text/secret detail %s should be rejected before persistence, got %d: %s", property, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "private") || strings.Contains(recorder.Body.String(), "sk-secret-marker") {
			t.Fatalf("rejected private payload was echoed: %s", recorder.Body.String())
		}
	}
}

func TestPlanStepActivityAcceptanceEvidenceRetryUsesExistingIdempotency(t *testing.T) {
	base := planStepActivityReplayFixture()
	base.Action = models.DeliveryPlanStepActivityEvidence
	base.Phase = models.DeliveryPlanStepActivityCompleted
	base.DetailsJSON = fmt.Sprintf(`{"acceptance_checks":[{"criterion_sha256":%q,"passed":true}],"review_diff_sha256":%q}`, criterionSHA256("approved criterion"), criterionSHA256("review diff"))
	retry := base
	if !samePlanStepActivity(base, retry) {
		t.Fatal("an exact acceptance-event retry must be idempotent")
	}
	retry.DetailsJSON = fmt.Sprintf(`{"acceptance_checks":[{"criterion_sha256":%q,"passed":true}],"review_diff_sha256":%q}`, criterionSHA256("different criterion"), criterionSHA256("review diff"))
	if samePlanStepActivity(base, retry) {
		t.Fatal("changed acceptance evidence under the same event ID must conflict")
	}
}

func validAcceptanceActivityDetails(criterion string) *models.DeliveryPlanStepActivityDetails {
	return &models.DeliveryPlanStepActivityDetails{
		AcceptanceChecks: []models.DeliveryPlanStepAcceptanceCheck{{CriterionSHA256: criterionSHA256(criterion), Passed: true}},
		ReviewDiffSHA256: criterionSHA256("review diff"),
	}
}

func criterionSHA256(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func acceptanceActivityEvent(details *models.DeliveryPlanStepActivityDetails, phase string) models.DeliveryPlanStepActivityEvent {
	encoded, _ := json.Marshal(details)
	return models.DeliveryPlanStepActivityEvent{
		Action: models.DeliveryPlanStepActivityEvidence, Phase: phase, DetailsJSON: string(encoded),
	}
}

func TestPlanStepActivityDuplicateComparisonRequiresIdenticalSemanticPayload(t *testing.T) {
	base := models.DeliveryPlanStepActivityEvent{
		ID: uuid.Must(uuid.NewV4()), PlanID: uuid.Must(uuid.NewV4()), StepID: uuid.Must(uuid.NewV4()),
		AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: uuid.Must(uuid.NewV4()).String(), WorkerID: uuid.Must(uuid.NewV4()).String(),
		AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(), FencingToken: 4, Sequence: 2,
		Action: models.DeliveryPlanStepActivityValidation, Phase: models.DeliveryPlanStepActivityCompleted,
		Summary: "Validation completed", DetailsJSON: `{"executable_name":"go","argument_count":2,"exit_code":0,"captured_output_bytes":17}`,
	}
	retry := base
	if !samePlanStepActivity(base, retry) {
		t.Fatal("exact event payload should be idempotent")
	}
	retry.ID = uuid.Must(uuid.NewV4())
	if samePlanStepActivity(base, retry) {
		t.Fatal("a sequence reused with a different event ID must conflict")
	}
	retry.ID = base.ID
	retry.FencingToken++
	if samePlanStepActivity(base, retry) {
		t.Fatal("a sequence reused under a different fence must conflict")
	}
	retry = base
	retry.DetailsJSON = `{"executable_name":"go","argument_count":1,"exit_code":0,"captured_output_bytes":17}`
	if samePlanStepActivity(base, retry) {
		t.Fatal("a sequence reused with changed structured details must conflict")
	}
}

func TestPlanStepActivityReplayRequiresSameInferenceReceiptPair(t *testing.T) {
	callID, receiptID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	base := models.DeliveryPlanStepActivityEvent{
		ID: uuid.Must(uuid.NewV4()), PlanID: uuid.Must(uuid.NewV4()), StepID: uuid.Must(uuid.NewV4()),
		AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: uuid.Must(uuid.NewV4()).String(), WorkerID: uuid.Must(uuid.NewV4()).String(),
		AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(), FencingToken: 7, Sequence: 9,
		Action: models.DeliveryPlanStepActivityInference, Phase: models.DeliveryPlanStepActivityCompleted,
		InferenceCallID: &callID, InferenceReceiptID: &receiptID, Summary: "Inference completed", DetailsJSON: "{}",
	}
	retry := base
	if !samePlanStepActivity(base, retry) {
		t.Fatal("exact receipt-bound inference event should be idempotent")
	}
	otherCallID := uuid.Must(uuid.NewV4())
	retry.InferenceCallID = &otherCallID
	if samePlanStepActivity(base, retry) {
		t.Fatal("replay with a different opaque call ID must conflict")
	}
	retry = base
	otherReceiptID := uuid.Must(uuid.NewV4())
	retry.InferenceReceiptID = &otherReceiptID
	if samePlanStepActivity(base, retry) {
		t.Fatal("replay with a different opaque receipt ID must conflict")
	}
}

func TestRecordPlanStepActivityReplaysExactEventBeforeCurrentLeaseValidation(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepActivityTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	fixture := planStepActivityReplayFixture()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_activity_events" WHERE id = \$1 ORDER BY "delivery_plan_step_activity_events"\."id" LIMIT \$2 FOR SHARE`).
		WithArgs(fixture.ID, 1).WillReturnRows(planStepActivityReplayRows(fixture))
	mock.ExpectQuery(`(?s).*delivery_plan_steps.*`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(fixture.StepID, fixture.PlanID))
	mock.ExpectCommit()

	ctx, recorder := planStepActivityCallbackContext(fixture, fixture.ID, fixture.AutomationTaskID, fixture.RunID, fixture.WorkerID, fixture.AgentKey, fixture.MachineID, fixture.FencingToken, fixture.Sequence, fixture.Action, fixture.Phase, fixture.ToolName, fixture.DurationMS)
	if err := RecordDeliveryPlanStepActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"idempotent":true`) {
		t.Fatalf("exact stored event should replay read-only with 200 despite stale lease, got %d: %s; expectations: %v", recorder.Code, recorder.Body.String(), mock.ExpectationsWereMet())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordPlanStepActivityRejectsEventIDAndSequenceCollisionsBeforeLeaseValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		sequence int64
		action   string
		phase    string
		newID    bool
	}{
		{name: "same event id changed action", sequence: 1, action: "tool", phase: "failed"},
		{name: "sequence reused with different event id", sequence: 1, action: models.DeliveryPlanStepActivityInference, phase: models.DeliveryPlanStepActivityStarted, newID: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mockPtr, restoreDB := configurePlanStepActivityTestDB(t)
			defer restoreDB()
			mock := *mockPtr
			fixture := planStepActivityReplayFixture()
			requestEventID := fixture.ID
			if test.newID {
				requestEventID = uuid.Must(uuid.NewV4())
			}
			mock.ExpectBegin()
			if test.name == "same event id changed action" {
				mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_activity_events" WHERE id = \$1 ORDER BY "delivery_plan_step_activity_events"\."id" LIMIT \$2 FOR SHARE`).
					WithArgs(fixture.ID, 1).WillReturnRows(planStepActivityReplayRows(fixture))
				mock.ExpectQuery(`(?s).*delivery_plan_steps.*`).
					WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(fixture.StepID, fixture.PlanID))
			} else {
				mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_activity_events" WHERE id = \$1 ORDER BY "delivery_plan_step_activity_events"\."id" LIMIT \$2 FOR SHARE`).
					WithArgs(requestEventID, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
				mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_activity_events" WHERE step_id = \$1 AND run_id = \$2 AND sequence = \$3 ORDER BY "delivery_plan_step_activity_events"\."id" LIMIT \$4 FOR SHARE`).
					WithArgs(fixture.StepID, fixture.RunID, test.sequence, 1).WillReturnRows(planStepActivityReplayRows(fixture))
				mock.ExpectQuery(`(?s).*delivery_plan_steps.*`).
					WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id"}).AddRow(fixture.StepID, fixture.PlanID))
			}
			mock.ExpectRollback()
			ctx, recorder := planStepActivityCallbackContext(fixture, requestEventID, fixture.AutomationTaskID, fixture.RunID, fixture.WorkerID, fixture.AgentKey, fixture.MachineID, fixture.FencingToken, test.sequence, test.action, test.phase, fixture.ToolName, fixture.DurationMS)
			if err := RecordDeliveryPlanStepActivity(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusConflict {
				t.Fatalf("changed event identity or sequence collision should be 409, got %d: %s; expectations: %v", recorder.Code, recorder.Body.String(), mock.ExpectationsWereMet())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecordPlanStepActivityRejectsNewEventWithStaleFence(t *testing.T) {
	mockPtr, restoreDB := configurePlanStepActivityTestDB(t)
	defer restoreDB()
	mock := *mockPtr
	fixture := planStepActivityReplayFixture()
	newEventID := uuid.Must(uuid.NewV4())
	leaseExpiry := time.Now().UTC().Add(time.Minute)
	workItemID := uuid.Must(uuid.NewV4())
	stepRow := sqlmock.NewRows([]string{
		"id", "plan_id", "step_key", "idempotency_key", "display_order", "title", "objective", "acceptance_criteria_json", "status",
		"agent_key", "worker_id", "machine_id", "automation_task_id", "run_id", "lease_fence", "lease_expires_at", "created_at", "updated_at",
	}).AddRow(fixture.StepID, fixture.PlanID, "implement", "step-implement", 1, "Implement", "", `[]`, models.DeliveryPlanStepRunning,
		fixture.AgentKey, fixture.WorkerID, fixture.MachineID, fixture.AutomationTaskID, fixture.RunID, int64(5), leaseExpiry,
		leaseExpiry.Add(-time.Minute), leaseExpiry.Add(-time.Minute))
	taskRows := sqlmock.NewRows([]string{
		"id", "job_id", "requested_by", "delivery_work_item_id", "worker_id", "agent_key", "machine_id", "correlation_id", "operation", "input_ref", "status", "run_id", "lease_expires_at", "created_at", "updated_at",
	}).AddRow(fixture.AutomationTaskID, uuid.Must(uuid.NewV4()), "test", workItemID, fixture.WorkerID, fixture.AgentKey, fixture.MachineID, workItemID.String(), "delivery.implementation", "private://input", "running", fixture.RunID, leaseExpiry, leaseExpiry.Add(-time.Minute), leaseExpiry.Add(-time.Minute))
	profileRows := sqlmock.NewRows([]string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}).
		AddRow(uuid.Must(uuid.NewV4()), fixture.AgentKey, "Generalist", "Generalist", "test profile", `["delivery.implementation"]`, `[]`, true, leaseExpiry.Add(-time.Minute), leaseExpiry.Add(-time.Minute))

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_activity_events" WHERE id = \$1 ORDER BY "delivery_plan_step_activity_events"\."id" LIMIT \$2 FOR SHARE`).
		WithArgs(newEventID, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_activity_events" WHERE step_id = \$1 AND run_id = \$2 AND sequence = \$3 ORDER BY "delivery_plan_step_activity_events"\."id" LIMIT \$4 FOR SHARE`).
		WithArgs(fixture.StepID, fixture.RunID, int64(2), 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks" WHERE id = \$1 ORDER BY "automation_tasks"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(fixture.AutomationTaskID, 1).WillReturnRows(taskRows)
	mock.ExpectQuery(`SELECT "id","operation" FROM "automation_tasks" WHERE "automation_tasks"\."id" = \$1 ORDER BY "automation_tasks"\."id" LIMIT \$2`).
		WithArgs(fixture.AutomationTaskID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "operation"}).AddRow(fixture.AutomationTaskID, "delivery.implementation"))
	mock.ExpectQuery(`SELECT \* FROM "automation_agent_profiles" WHERE agent_key = \$1 AND active = \$2 ORDER BY "automation_agent_profiles"\."id" LIMIT \$3`).
		WithArgs(fixture.AgentKey, true, 1).WillReturnRows(profileRows)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE id = \$1 ORDER BY "delivery_plan_steps"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(fixture.StepID, 1).WillReturnRows(stepRow)
	mock.ExpectRollback()

	ctx, recorder := planStepActivityCallbackContext(fixture, newEventID, fixture.AutomationTaskID, fixture.RunID, fixture.WorkerID, fixture.AgentKey, fixture.MachineID, 4, 2, models.DeliveryPlanStepActivityTool, models.DeliveryPlanStepActivityStarted, "", nil)
	if err := RecordDeliveryPlanStepActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("a new event under a stale fence should be rejected with 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func configurePlanStepActivityTestDB(t *testing.T) (*sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	return &mock, func() { configuration.DB = previousDB }
}

func planStepActivityReplayFixture() models.DeliveryPlanStepActivityEvent {
	return models.DeliveryPlanStepActivityEvent{
		ID: uuid.Must(uuid.NewV4()), PlanID: uuid.Must(uuid.NewV4()), StepID: uuid.Must(uuid.NewV4()),
		AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: uuid.Must(uuid.NewV4()).String(), WorkerID: uuid.Must(uuid.NewV4()).String(),
		AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(), FencingToken: 4, Sequence: 1,
		Action: models.DeliveryPlanStepActivityInference, Phase: models.DeliveryPlanStepActivityStarted,
		Summary: "Inference started", OccurredAt: time.Date(2026, 9, 23, 12, 30, 0, 0, time.UTC), CreatedAt: time.Date(2026, 9, 23, 12, 30, 0, 0, time.UTC),
	}
}

func planStepActivityReplayRows(event models.DeliveryPlanStepActivityEvent) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "plan_id", "step_id", "automation_task_id", "run_id", "worker_id", "agent_key", "machine_id", "agent_instance_id", "inference_call_id", "inference_receipt_id", "fencing_token", "sequence", "action", "phase", "tool_name", "duration_ms", "summary", "details_json", "occurred_at", "created_at",
	}).AddRow(event.ID, event.PlanID, event.StepID, event.AutomationTaskID, event.RunID, event.WorkerID, event.AgentKey, event.MachineID, event.AgentInstanceID, event.InferenceCallID, event.InferenceReceiptID, event.FencingToken, event.Sequence, event.Action, event.Phase, event.ToolName, event.DurationMS, event.Summary, event.DetailsJSON, event.OccurredAt, event.CreatedAt)
}

func planStepActivityCallbackContext(event models.DeliveryPlanStepActivityEvent, eventID, taskID uuid.UUID, runID string, workerID, agentKey, machineID string, fence, sequence int64, action, phase, toolName string, duration *int64) (echo.Context, *httptest.ResponseRecorder) {
	payload := map[string]any{
		"event_id": eventID.String(), "task_id": taskID.String(), "run_id": runID, "worker_id": workerID, "agent_key": agentKey,
		"machine_id": machineID, "fencing_token": strconv.FormatInt(fence, 10), "sequence": sequence, "action": action, "phase": phase,
	}
	if toolName != "" {
		payload["tool_name"] = toolName
	}
	if duration != nil {
		payload["duration_ms"] = *duration
	}
	body, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/steps/"+event.StepID.String()+"/activity", strings.NewReader(string(body)))
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetParamNames("id")
	ctx.SetParamValues(event.StepID.String())
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: agentKey, MachineID: machineID})
	return ctx, recorder
}

func TestPlanStepActivityLeaseBindingRejectsStaleFenceTaskRunIdentityAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	taskID, runID, workerID, machineID, stepID, planID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	leaseExpires := now.Add(time.Minute)
	task := models.AutomationTask{ID: taskID, RunID: runID.String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(), LeaseExpiresAt: &leaseExpires}
	step := models.DeliveryPlanStep{ID: stepID, PlanID: planID, Status: models.DeliveryPlanStepRunning, RunID: runID.String(), WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(), LeaseFence: 5, LeaseExpiresAt: &leaseExpires, AutomationTaskID: &taskID}
	if !planStepActivityLeaseMatches(step, task, runID.String(), workerID.String(), "generalist", machineID.String(), 5, now) {
		t.Fatal("live matching step lease was rejected")
	}
	cases := []struct {
		name    string
		step    models.DeliveryPlanStep
		task    models.AutomationTask
		runID   string
		worker  string
		machine string
		fence   int64
		now     time.Time
	}{
		{name: "stale fence", step: step, task: task, runID: runID.String(), worker: workerID.String(), machine: machineID.String(), fence: 4, now: now},
		{name: "wrong task", step: func() models.DeliveryPlanStep {
			value := step
			other := uuid.Must(uuid.NewV4())
			value.AutomationTaskID = &other
			return value
		}(), task: task, runID: runID.String(), worker: workerID.String(), machine: machineID.String(), fence: 5, now: now},
		{name: "stale run", step: step, task: task, runID: uuid.Must(uuid.NewV4()).String(), worker: workerID.String(), machine: machineID.String(), fence: 5, now: now},
		{name: "wrong worker", step: step, task: task, runID: runID.String(), worker: uuid.Must(uuid.NewV4()).String(), machine: machineID.String(), fence: 5, now: now},
		{name: "wrong machine", step: step, task: task, runID: runID.String(), worker: workerID.String(), machine: uuid.Must(uuid.NewV4()).String(), fence: 5, now: now},
		{name: "expired lease", step: step, task: task, runID: runID.String(), worker: workerID.String(), machine: machineID.String(), fence: 5, now: leaseExpires.Add(time.Second)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if planStepActivityLeaseMatches(test.step, test.task, test.runID, test.worker, "generalist", test.machine, test.fence, test.now) {
				t.Fatal("stale or mismatched lease tuple was accepted")
			}
		})
	}
}

func TestRecordPlanStepActivityRequiresDatabaseAfterPayloadValidation(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	taskID, runID, workerID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	body := `{"event_id":"` + uuid.Must(uuid.NewV4()).String() + `","task_id":"` + taskID.String() + `","run_id":"` + runID.String() + `","worker_id":"` + workerID.String() + `","agent_key":"generalist","fencing_token":"2","sequence":1,"action":"inference","phase":"started"}`
	request := httptest.NewRequest(http.MethodPost, "/steps/step/activity", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetParamNames("id")
	ctx.SetParamValues(uuid.Must(uuid.NewV4()).String())
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String()})
	if err := RecordDeliveryPlanStepActivity(ctx); err != nil {
		t.Fatalf("database unavailable should be handled: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("valid bounded event without DB must fail closed with 503, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestPlanStepActivityPatchArtifactReferencesAreBoundedAndEvidenceOnly(t *testing.T) {
	baseSHA := strings.Repeat("a", 40)
	digest := strings.Repeat("b", 64)
	valid := &models.DeliveryPlanStepActivityDetails{PatchArtifacts: []models.DeliveryPlanStepPatchArtifactReference{{
		RepositoryRef: "workspace://repo_1", BaseSHA: baseSHA, SHA256: digest, SizeBytes: 1,
	}}}
	manifestSHA256, err := models.DeliveryPlanStepPatchArtifactManifestSHA256(valid.PatchArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	valid.ReviewDiffSHA256 = manifestSHA256
	if err := models.ValidateDeliveryPlanStepActivityDetails(models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted, valid); err != nil {
		t.Fatalf("valid patch artifact reference rejected: %v", err)
	}
	cases := []struct {
		name   string
		action string
		phase  string
		mutate func(*models.DeliveryPlanStepActivityDetails)
	}{
		{name: "not evidence", action: models.DeliveryPlanStepActivityValidation, phase: models.DeliveryPlanStepActivityCompleted},
		{name: "not completed", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityFailed},
		{name: "nested repository", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityCompleted, mutate: func(d *models.DeliveryPlanStepActivityDetails) { d.PatchArtifacts[0].RepositoryRef += "/src" }},
		{name: "uppercase digest", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityCompleted, mutate: func(d *models.DeliveryPlanStepActivityDetails) { d.PatchArtifacts[0].SHA256 = strings.Repeat("B", 64) }},
		{name: "invalid base sha", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityCompleted, mutate: func(d *models.DeliveryPlanStepActivityDetails) { d.PatchArtifacts[0].BaseSHA = strings.Repeat("A", 40) }},
		{name: "incorrect manifest digest", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityCompleted, mutate: func(d *models.DeliveryPlanStepActivityDetails) { d.ReviewDiffSHA256 = strings.Repeat("c", 64) }},
		{name: "zero size", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityCompleted, mutate: func(d *models.DeliveryPlanStepActivityDetails) { d.PatchArtifacts[0].SizeBytes = 0 }},
		{name: "oversized", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityCompleted, mutate: func(d *models.DeliveryPlanStepActivityDetails) {
			d.PatchArtifacts[0].SizeBytes = models.MaxDeliveryPlanStepPatchArtifactBytes + 1
		}},
		{name: "too many patches", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityCompleted, mutate: func(d *models.DeliveryPlanStepActivityDetails) {
			d.PatchArtifacts = make([]models.DeliveryPlanStepPatchArtifactReference, models.MaxDeliveryPlanStepPatchArtifacts+1)
			for i := range d.PatchArtifacts {
				d.PatchArtifacts[i] = valid.PatchArtifacts[0]
				d.PatchArtifacts[i].SHA256 = fmt.Sprintf("%064x", i+1)
			}
		}},
		{name: "duplicate repository", action: models.DeliveryPlanStepActivityEvidence, phase: models.DeliveryPlanStepActivityCompleted, mutate: func(d *models.DeliveryPlanStepActivityDetails) {
			d.PatchArtifacts = append(d.PatchArtifacts, d.PatchArtifacts[0])
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			copy := *valid
			copy.PatchArtifacts = append([]models.DeliveryPlanStepPatchArtifactReference(nil), valid.PatchArtifacts...)
			if test.mutate != nil {
				test.mutate(&copy)
			}
			if err := models.ValidateDeliveryPlanStepActivityDetails(test.action, test.phase, &copy); err == nil {
				t.Fatal("invalid patch artifact reference was accepted")
			}
		})
	}
}

func TestPatchArtifactManifestSHA256IsCanonicalAcrossRepositoryOrder(t *testing.T) {
	artifacts := []models.DeliveryPlanStepPatchArtifactReference{
		{RepositoryRef: "workspace://repo_z", BaseSHA: strings.Repeat("d", 40), SHA256: strings.Repeat("4", 64), SizeBytes: 21},
		{RepositoryRef: "workspace://repo_a", BaseSHA: strings.Repeat("c", 64), SHA256: strings.Repeat("3", 64), SizeBytes: 9},
	}
	canonical := "workspace://repo_a\n" + strings.Repeat("c", 64) + "\n" + strings.Repeat("3", 64) + "\n9\n" +
		"workspace://repo_z\n" + strings.Repeat("d", 40) + "\n" + strings.Repeat("4", 64) + "\n21\n"
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
	actual, err := models.DeliveryPlanStepPatchArtifactManifestSHA256(artifacts)
	if err != nil || actual != expected {
		t.Fatalf("manifest digest = %q, err=%v; want %q", actual, err, expected)
	}
	artifacts[0], artifacts[1] = artifacts[1], artifacts[0]
	reversed, err := models.DeliveryPlanStepPatchArtifactManifestSHA256(artifacts)
	if err != nil || reversed != expected {
		t.Fatalf("manifest digest changed with input order: %q, err=%v; want %q", reversed, err, expected)
	}
	details := &models.DeliveryPlanStepActivityDetails{PatchArtifacts: artifacts, ReviewDiffSHA256: expected}
	if err := models.ValidateDeliveryPlanStepActivityDetails(models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted, details); err != nil {
		t.Fatalf("correct multi-repository manifest was rejected: %v", err)
	}
	if err := validatePlanStepActivityAcceptanceCriteria(`[]`, acceptanceActivityEvent(details, models.DeliveryPlanStepActivityCompleted)); err != nil {
		t.Fatalf("controller rejected the canonical multi-repository manifest: %v", err)
	}
	incorrect := *details
	incorrect.ReviewDiffSHA256 = strings.Repeat("e", 64)
	if err := validatePlanStepActivityAcceptanceCriteria(`[]`, acceptanceActivityEvent(&incorrect, models.DeliveryPlanStepActivityCompleted)); err == nil {
		t.Fatal("controller accepted an incorrect patch-manifest digest")
	}
	sameDigest := append([]models.DeliveryPlanStepPatchArtifactReference(nil), artifacts...)
	sameDigest[1].SHA256 = sameDigest[0].SHA256
	sameDigest[1].BaseSHA = sameDigest[0].BaseSHA
	sameDigest[1].SizeBytes = sameDigest[0].SizeBytes
	sameDigestManifest, err := models.DeliveryPlanStepPatchArtifactManifestSHA256(sameDigest)
	if err != nil {
		t.Fatal(err)
	}
	sameDigestDetails := &models.DeliveryPlanStepActivityDetails{PatchArtifacts: sameDigest, ReviewDiffSHA256: sameDigestManifest}
	if err := models.ValidateDeliveryPlanStepActivityDetails(models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted, sameDigestDetails); err != nil {
		t.Fatalf("identical patch bytes across distinct repositories should be representable: %v", err)
	}
	duplicateRepo := append(append([]models.DeliveryPlanStepPatchArtifactReference(nil), artifacts...), artifacts[0])
	if _, err := models.DeliveryPlanStepPatchArtifactManifestSHA256(duplicateRepo); err == nil {
		t.Fatal("manifest with repeated repository was accepted")
	}
}

func TestValidateUnifiedGitPatchRejectsUnsafeHeadersAndSecrets(t *testing.T) {
	valid := "diff --git a/src/main.go b/src/main.go\nindex 1111111..2222222 100644\n--- a/src/main.go\n+++ b/src/main.go\n@@ -1 +1 @@\n-old\n+new\n"
	if err := validateUnifiedGitPatch([]byte(valid)); err != nil {
		t.Fatalf("ordinary unified Git patch rejected: %v", err)
	}
	for _, test := range []struct {
		name  string
		patch string
	}{
		{name: "path traversal and env", patch: "diff --git a/../../.env b/../../.env\n--- a/../../.env\n+++ b/../../.env\n"},
		{name: "file header does not match git header", patch: "diff --git a/src/main.go b/src/main.go\n--- a/src/other.go\n+++ b/src/main.go\n"},
		{name: "quoted path", patch: "diff --git \"a/src/file name.go\" \"b/src/file name.go\"\n--- \"a/src/file name.go\"\n+++ \"b/src/file name.go\"\n"},
		{name: "binary patch", patch: "diff --git a/logo.png b/logo.png\nGIT binary patch\nliteral 1\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateUnifiedGitPatch([]byte(test.patch)); err == nil {
				t.Fatal("unsafe or malformed Git patch was accepted")
			}
		})
	}
	for _, secret := range []string{"AKIAIOSFODNN7EXAMPLE", "ghp_abcdefghijklmnopqrstuvwxyz123456", "password = \"abcdefghijklmnopqrstuvwxyz123456\"", "-----BEGIN PRIVATE KEY-----"} {
		if !containsHighConfidencePatchSecret([]byte(valid + "+" + secret)) {
			t.Fatalf("high-confidence credential pattern was not detected: %q", secret)
		}
	}
}

func TestPlanStepPatchObjectKeyIsServerDerived(t *testing.T) {
	taskID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4()).String()
	digest := strings.Repeat("a", 64)
	want := "automation/" + taskID.String() + "/runs/" + runID + "/steps/" + stepID.String() + "/patches/" + digest + ".patch"
	if got := planStepPatchObjectKey(taskID, runID, stepID, digest); got != want {
		t.Fatalf("derived object key = %q, want %q", got, want)
	}
}
