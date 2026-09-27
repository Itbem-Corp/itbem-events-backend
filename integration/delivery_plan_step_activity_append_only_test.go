//go:build integration

package integration_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeliveryPlanStepActivityEventsAreAppendOnly(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			rollbackFixture := errors.New("rollback activity fixture")
			var mutationErr error
			err := configuration.DB.Transaction(func(tx *gorm.DB) error {
				lifecycleEvent := createStepEventFixture(t, tx)
				activity := models.DeliveryPlanStepActivityEvent{
					ID: uuid.Must(uuid.NewV4()), PlanID: lifecycleEvent.PlanID, StepID: lifecycleEvent.StepID,
					AutomationTaskID: lifecycleEvent.AutomationTaskID, RunID: uuid.Must(uuid.NewV4()).String(),
					WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", FencingToken: 1,
					Sequence: 1, Action: models.DeliveryPlanStepActivityFileChange, Phase: models.DeliveryPlanStepActivityCompleted,
					Summary: "File change completed", DetailsJSON: `{"resource_references":["workspace://backend"],"changed_files":["src/orders.go"]}`, OccurredAt: time.Now().UTC(),
				}
				require.NoError(t, tx.Create(&activity).Error)
				switch operation {
				case "update":
					mutationErr = tx.Model(&models.DeliveryPlanStepActivityEvent{}).Where("id = ?", activity.ID).Update("summary", "rewritten").Error
				case "delete":
					mutationErr = tx.Delete(&activity).Error
				}
				return rollbackFixture
			})
			require.ErrorIs(t, err, rollbackFixture, "the fixture transaction must roll back")
			require.Error(t, mutationErr)
			require.Contains(t, strings.ToLower(mutationErr.Error()), "delivery plan step activity is append-only")
		})
	}
}

func TestRecordPlanStepActivityFencesIdentityAndDeduplicates(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	profile := models.DefaultGeneralistAgentProfile()
	require.NoError(t, db.Where("agent_key = ?", profile.AgentKey).FirstOrCreate(&profile).Error)
	taskID, runID, workerUUID, machineUUID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	encodedPublicKey, err := agentcallbackauth.EncodePublicKey(publicKey)
	require.NoError(t, err)
	publicKeyFingerprint, err := agentcallbackauth.PublicKeyFingerprint(publicKey)
	require.NoError(t, err)
	instance := models.AutomationAgentInstance{
		ID: uuid.Must(uuid.NewV4()), AgentKey: profile.AgentKey, MachineID: machineUUID.String(),
		PublicKey: encodedPublicKey, PublicKeyFingerprint: publicKeyFingerprint, Status: "active",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&instance).Error)
	t.Cleanup(func() {
		_ = db.Unscoped().Where("instance_id = ?", instance.ID).Delete(&models.AutomationAgentCallbackNonce{}).Error
		_ = db.Unscoped().Delete(&instance).Error
	})

	var fixture models.DeliveryPlanStepEvent
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		fixture = createStepEventFixture(t, tx)
		var plan models.DeliveryPlan
		if err := tx.First(&plan, "id = ?", fixture.PlanID).Error; err != nil {
			return err
		}
		gate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: plan.WorkItemID, Kind: "plan_approval", Decision: "approved", DecidedBy: "integration", DecidedAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
		if err := tx.Create(&gate).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DeliveryPlan{}).Where("id = ?", plan.ID).Update("approved_gate_id", gate.ID).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		snapshot := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: plan.WorkItemID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Backend", Reference: "workspace://backend", CapturedAt: now, CreatedAt: now}
		if err := tx.Create(&snapshot).Error; err != nil {
			return err
		}
		leaseExpiry := now.Add(5 * time.Minute)
		itemID := plan.WorkItemID
		task := models.AutomationTask{
			ID: taskID, JobID: uuid.Must(uuid.NewV4()), RequestedBy: "integration", DeliveryWorkItemID: &itemID,
			WorkerID: workerUUID.String(), AgentKey: profile.AgentKey, MachineID: machineUUID.String(),
			CorrelationID: itemID.String(), Operation: "delivery.implementation", InputRef: "private://integration-input",
			Status: "running", RunID: runID.String(), LeaseExpiresAt: &leaseExpiry, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DeliveryPlanStep{}).Where("id = ?", fixture.StepID).Updates(map[string]any{
			"status": models.DeliveryPlanStepRunning, "automation_task_id": task.ID, "run_id": runID.String(),
			"worker_id": workerUUID.String(), "agent_key": profile.AgentKey, "machine_id": machineUUID.String(),
			"lease_fence": int64(9), "lease_expires_at": leaseExpiry,
		}).Error; err != nil {
			return err
		}
		return nil
	}))

	var plan models.DeliveryPlan
	require.NoError(t, db.Select("id", "work_item_id", "version").First(&plan, "id = ?", fixture.PlanID).Error)
	var task models.AutomationTask
	require.NoError(t, db.First(&task, "id = ?", taskID).Error)
	var step models.DeliveryPlanStep
	require.NoError(t, db.First(&step, "id = ?", fixture.StepID).Error)
	var approvedPlan models.DeliveryPlan
	require.NoError(t, db.Select("approved_gate_id").First(&approvedPlan, "id = ?", plan.ID).Error)
	require.NotNil(t, approvedPlan.ApprovedGateID)
	var gate models.DeliveryGate
	require.NoError(t, db.First(&gate, "id = ?", *approvedPlan.ApprovedGateID).Error)
	workerID := workerUUID
	machineID := machineUUID
	eventID := uuid.Must(uuid.NewV4())
	call := func(id, taskID, runID uuid.UUID, fence int64, sequence int64, action, phase string, details ...*models.DeliveryPlanStepActivityDetails) (int, string) {
		payload := map[string]any{
			"event_id": id.String(), "task_id": taskID.String(), "run_id": runID.String(), "worker_id": workerID.String(),
			"agent_key": task.AgentKey, "machine_id": machineID.String(), "fencing_token": strconv.FormatInt(fence, 10),
			"sequence": sequence, "action": action, "phase": phase,
		}
		if len(details) > 0 && details[0] != nil {
			payload["details"] = details[0]
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		requestURI := "/api/internal/automation/steps/" + step.ID.String() + "/activity"
		request := httptest.NewRequest(http.MethodPost, requestURI, strings.NewReader(string(body)))
		timestamp := time.Now().Unix()
		nonce := uuid.Must(uuid.NewV4()).String()
		signature, err := agentcallbackauth.SignRequest(privateKey, instance.ID.String(), request.Method, request.URL.RequestURI(), timestamp, nonce, body)
		require.NoError(t, err)
		encodedSignature, err := agentcallbackauth.EncodeSignature(signature)
		require.NoError(t, err)
		request.Header.Set(agentcallbackauth.InstanceIDHeader, instance.ID.String())
		request.Header.Set(agentcallbackauth.TimestampHeader, strconv.FormatInt(timestamp, 10))
		request.Header.Set(agentcallbackauth.NonceHeader, nonce)
		request.Header.Set(agentcallbackauth.SignatureHeader, encodedSignature)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		ctx.SetParamNames("id")
		ctx.SetParamValues(step.ID.String())
		handler := automation.AgentCallbackAuthentication(automation.RecordDeliveryPlanStepActivity)
		require.NoError(t, handler(ctx))
		return recorder.Code, recorder.Body.String()
	}

	status, body := call(eventID, task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 1, "inference", "started")
	require.Equalf(t, http.StatusOK, status, body)
	require.Contains(t, body, `"idempotent":false`)
	terminalEventID := uuid.Must(uuid.NewV4())
	safeDetails := &models.DeliveryPlanStepActivityDetails{ResourceReferences: []string{"workspace://backend"}, ChangedFiles: []string{"controllers/orders.go"}}
	status, body = call(terminalEventID, task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 2, "file_change", "completed", safeDetails)
	require.Equalf(t, http.StatusOK, status, body)
	require.Contains(t, body, `"idempotent":false`)
	status, body = call(terminalEventID, task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 2, "file_change", "completed", safeDetails)
	require.Equalf(t, http.StatusOK, status, body)
	require.Contains(t, body, `"idempotent":true`)
	status, body = call(terminalEventID, task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 2, "file_change", "completed", &models.DeliveryPlanStepActivityDetails{ResourceReferences: []string{"workspace://backend"}, ChangedFiles: []string{"controllers/other.go"}})
	require.Equalf(t, http.StatusConflict, status, body)
	status, body = call(uuid.Must(uuid.NewV4()), task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 3, "file_read", "completed", &models.DeliveryPlanStepActivityDetails{ResourceReferences: []string{"workspace://unapproved/private.go"}})
	require.Equalf(t, http.StatusConflict, status, body)
	var terminal models.DeliveryPlanStepActivityEvent
	require.NoError(t, db.Where("step_id = ? AND run_id = ? AND sequence = ?", step.ID, task.RunID, 2).First(&terminal).Error)
	projected, valid := decodeIntegrationActivityDetails(terminal.Action, terminal.Phase, terminal.DetailsJSON)
	require.True(t, valid)
	require.Equal(t, []string{"controllers/orders.go"}, projected.ChangedFiles)
	require.Equal(t, []string{"workspace://backend"}, projected.ResourceReferences)
	// The first write committed, but the worker could have lost its response.
	// A later claim advances the step fence; replaying that exact durable event
	// must still be acknowledged without accepting new events under the old fence.
	require.NoError(t, db.Model(&models.DeliveryPlanStep{}).Where("id = ?", step.ID).Update("lease_fence", int64(10)).Error)
	status, body = call(eventID, task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 1, "inference", "started")
	require.Equalf(t, http.StatusOK, status, body)
	require.Contains(t, body, `"idempotent":true`)
	status, body = call(uuid.Must(uuid.NewV4()), task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 1, "inference", "started")
	require.Equalf(t, http.StatusConflict, status, body)
	require.NotContains(t, body, `"idempotent":true`)
	status, body = call(uuid.Must(uuid.NewV4()), task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 1, "tool", "failed")
	require.Equalf(t, http.StatusConflict, status, body)
	status, body = call(uuid.Must(uuid.NewV4()), task.ID, uuid.Must(uuid.FromString(task.RunID)), 8, 2, "tool", "started")
	require.Equalf(t, http.StatusConflict, status, body)
	status, body = call(uuid.Must(uuid.NewV4()), task.ID, uuid.Must(uuid.NewV4()), 9, 2, "tool", "started")
	require.Equalf(t, http.StatusConflict, status, body)
	status, body = call(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.FromString(task.RunID)), 9, 2, "tool", "started")
	require.NotEqual(t, http.StatusOK, status, body)

	// Bind this live child task to a different step in the execution. The
	// callback must reject the step even though the task/step fence itself was
	// otherwise valid.
	otherStep := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "other-activity-step", IdempotencyKey: "other-activity-" + uuid.Must(uuid.NewV4()).String(), DisplayOrder: 2, Title: "Other", AcceptanceCriteriaJSON: `[]`, Status: models.DeliveryPlanStepPlanned, CreatedBy: "integration", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	parentTask := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: "integration", DeliveryWorkItemID: &plan.WorkItemID, CorrelationID: plan.WorkItemID.String(), Operation: "delivery.implementation", InputRef: "private://integration-parent", Status: "queued", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	execution := models.DeliveryPlanExecution{ID: uuid.Must(uuid.NewV4()), AutomationTaskID: parentTask.ID, IdempotencyKey: "activity-binding-" + uuid.Must(uuid.NewV4()).String(), PlanID: plan.ID, PlanVersion: plan.Version, ApprovedGateID: gate.ID, PlanHash: strings.Repeat("a", 64), MaxConcurrency: 1, Status: models.DeliveryPlanExecutionRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	assignment := models.DeliveryPlanStepAssignment{ID: uuid.Must(uuid.NewV4()), ExecutionID: execution.ID, DeliveryPlanStepID: otherStep.ID, ChildAutomationTaskID: task.ID, Status: models.DeliveryPlanStepAssignmentRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	for _, value := range []any{&otherStep, &parentTask, &execution, &assignment} {
		require.NoError(t, db.Create(value).Error)
	}
	status, body = call(uuid.Must(uuid.NewV4()), task.ID, uuid.Must(uuid.FromString(task.RunID)), 9, 2, "tool", "started")
	require.Equalf(t, http.StatusConflict, status, body)
}

func decodeIntegrationActivityDetails(action, phase, raw string) (*models.DeliveryPlanStepActivityDetails, bool) {
	var details models.DeliveryPlanStepActivityDetails
	if err := json.Unmarshal([]byte(raw), &details); err != nil || models.ValidateDeliveryPlanStepActivityDetails(action, phase, &details) != nil {
		return nil, false
	}
	return &details, true
}
