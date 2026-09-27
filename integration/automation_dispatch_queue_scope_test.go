//go:build integration

package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type automationDispatchQueueScopeResponse struct {
	Data struct {
		Items []struct {
			AssignmentID         uuid.UUID  `json:"assignment_id"`
			ProjectID            uuid.UUID  `json:"project_id"`
			ProjectName          string     `json:"project_name"`
			TargetMachineID      *string    `json:"target_machine_id"`
			TargetAvailability   string     `json:"target_availability"`
			TargetConcurrency    int        `json:"target_concurrency"`
			TargetActiveRuns     int        `json:"target_active_runs"`
			TargetAvailableSlots int        `json:"target_available_slots"`
			TargetLastSeenAt     *time.Time `json:"target_last_seen_at"`
		} `json:"items"`
	} `json:"data"`
}

type automationDispatchQueueHTTPResult struct {
	status int
	body   []byte
	err    error
}

// TestAutomationDispatchQueueOrganizationScopeUsesProjectAuthorizationAndHidesWorkerSignals exercises
// the real queue SQL and project authorization against disposable PostgreSQL.
// It verifies a project viewer sees only the selected project's approved-plan
// assignments, never its local machine or heartbeat/capacity data, and cannot
// distinguish an out-of-organization project from a nonexistent one.
func TestAutomationDispatchQueueOrganizationScopeUsesProjectAuthorizationAndHidesWorkerSignals(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	subject := "dispatch-queue-scope-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	rollback := errors.New("rollback dispatch queue scope fixture")

	clientType := models.ClientType{
		ID: uuid.Must(uuid.NewV4()), Name: "Dispatch queue type " + suffix,
		Code: "DISPATCHQ_" + strings.ToUpper(suffix), Level: 10, IsActive: true,
	}
	organization := models.Client{
		ID: uuid.Must(uuid.NewV4()), Name: "Dispatch queue organization " + suffix,
		Code: "dispatchq-org-" + suffix, ClientTypeID: clientType.ID, IsActive: true,
	}
	outsideOrganization := models.Client{
		ID: uuid.Must(uuid.NewV4()), Name: "Outside dispatch organization " + suffix,
		Code: "dispatchq-outside-" + suffix, ClientTypeID: clientType.ID, IsActive: true,
	}
	selectedProject := models.DeliveryProject{
		ID: uuid.Must(uuid.NewV4()), ClientID: organization.ID,
		Name: "Selected dispatch project " + suffix, Slug: "dispatchq-selected-" + suffix,
		Status: "active", CreatedBy: subject,
	}
	siblingProject := models.DeliveryProject{
		ID: uuid.Must(uuid.NewV4()), ClientID: organization.ID,
		Name: "Sibling dispatch project " + suffix, Slug: "dispatchq-sibling-" + suffix,
		Status: "active", CreatedBy: subject,
	}
	outsideProject := models.DeliveryProject{
		ID: uuid.Must(uuid.NewV4()), ClientID: outsideOrganization.ID,
		Name: "Outside dispatch project " + suffix, Slug: "dispatchq-outside-project-" + suffix,
		Status: "active", CreatedBy: subject,
	}
	selectedMember := models.DeliveryProjectMember{
		ID: uuid.Must(uuid.NewV4()), ProjectID: selectedProject.ID, CognitoSub: subject,
		Role: "viewer", Permissions: `[]`, CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}
	siblingMember := models.DeliveryProjectMember{
		ID: uuid.Must(uuid.NewV4()), ProjectID: siblingProject.ID, CognitoSub: subject,
		Role: "viewer", Permissions: `[]`, CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}

	var selectedAssignmentID, siblingAssignmentID uuid.UUID
	var selectedMachineID string
	var selectedQueue, outsideQueue, missingQueue automationDispatchQueueHTTPResult
	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			return nil, fmt.Errorf("unexpected Cognito subject %q", cognitoSub)
		}
		return &models.User{
			ID: uuid.Must(uuid.NewV4()), CognitoSub: subject,
			IsRoot: false, RootLevel: models.RootLevelNone, IsActive: true,
		}, nil
	}})
	t.Cleanup(restoreAuth)

	err := db.Transaction(func(tx *gorm.DB) error {
		previousDB := configuration.DB
		configuration.DB = tx
		defer func() { configuration.DB = previousDB }()

		for _, value := range []any{&clientType, &organization, &outsideOrganization, &selectedProject, &siblingProject, &outsideProject, &selectedMember, &siblingMember} {
			if err := tx.Create(value).Error; err != nil {
				return err
			}
		}

		var err error
		selectedAssignmentID, selectedMachineID, err = seedAutomationDispatchQueueScopeAssignment(tx, selectedProject, subject, suffix, "selected", now, true)
		if err != nil {
			return err
		}
		siblingAssignmentID, _, err = seedAutomationDispatchQueueScopeAssignment(tx, siblingProject, subject, suffix, "sibling", now, false)
		if err != nil {
			return err
		}

		var outsideCount int64
		if err := tx.Model(&models.DeliveryProject{}).Where("id = ? AND client_id = ?", outsideProject.ID, outsideOrganization.ID).Count(&outsideCount).Error; err != nil {
			return err
		}
		if outsideCount != 1 {
			return fmt.Errorf("outside project fixture was not persisted")
		}

		requestQueue := func(projectID uuid.UUID) automationDispatchQueueHTTPResult {
			request := httptest.NewRequest(http.MethodGet, "/api/automation/dispatch/queue?project_id="+projectID.String(), nil)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.Set("cognito_sub", subject)
			ctx.Set("workspace_mode", "organization")
			ctx.Set("organization_id", organization.ID)
			handlerErr := automation.GetDispatchQueue(ctx)
			return automationDispatchQueueHTTPResult{status: recorder.Code, body: append([]byte(nil), recorder.Body.Bytes()...), err: handlerErr}
		}
		selectedQueue = requestQueue(selectedProject.ID)
		outsideQueue = requestQueue(outsideProject.ID)
		missingQueue = requestQueue(uuid.Must(uuid.NewV4()))
		return rollback
	})
	require.ErrorIs(t, err, rollback, "all disposable project, plan, assignment and heartbeat rows must roll back")

	require.Equal(t, http.StatusOK, selectedQueue.status, string(selectedQueue.body))
	require.NoError(t, selectedQueue.err)
	var page automationDispatchQueueScopeResponse
	require.NoError(t, json.Unmarshal(selectedQueue.body, &page))
	require.Len(t, page.Data.Items, 1, "organization queue must include only the explicitly selected project")
	item := page.Data.Items[0]
	require.Equal(t, selectedAssignmentID, item.AssignmentID)
	require.Equal(t, selectedProject.ID, item.ProjectID)
	require.Equal(t, selectedProject.Name, item.ProjectName)
	require.Nil(t, item.TargetMachineID, "organization queue must not expose the assignment's machine target")
	require.Equal(t, "unknown", item.TargetAvailability, "organization queue must not infer availability from worker heartbeat")
	require.Zero(t, item.TargetConcurrency, "organization queue must not expose heartbeat concurrency")
	require.Zero(t, item.TargetActiveRuns, "organization queue must not expose machine-wide active runs")
	require.Zero(t, item.TargetAvailableSlots, "organization queue must not expose machine-wide free capacity")
	require.Nil(t, item.TargetLastSeenAt, "organization queue must not expose heartbeat timestamps")
	for _, privateValue := range []string{
		siblingAssignmentID.String(), siblingProject.ID.String(), siblingProject.Name,
		selectedMachineID, "dispatch-queue-private-heartbeat-" + suffix,
	} {
		require.NotContains(t, string(selectedQueue.body), privateValue, "organization response leaked another project or worker signal")
	}
	require.NotContains(t, string(selectedQueue.body), "\"target_concurrency\":9")
	require.NotContains(t, string(selectedQueue.body), "\"target_active_runs\":1")

	require.Equal(t, http.StatusNotFound, outsideQueue.status, string(outsideQueue.body))
	require.Error(t, outsideQueue.err, "denied out-of-organization access must stop the handler")
	require.Equal(t, http.StatusNotFound, missingQueue.status, string(missingQueue.body))
	require.Error(t, missingQueue.err)
	var outsideEnvelope, missingEnvelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(outsideQueue.body, &outsideEnvelope))
	require.NoError(t, json.Unmarshal(missingQueue.body, &missingEnvelope))
	require.Equal(t, missingEnvelope, outsideEnvelope, "an existing out-of-organization project must be indistinguishable from a nonexistent project")
	for _, privateValue := range []string{
		outsideOrganization.ID.String(), outsideProject.ID.String(), outsideProject.Name,
		selectedProject.ID.String(), selectedAssignmentID.String(), selectedMachineID,
	} {
		require.NotContains(t, string(outsideQueue.body), privateValue, "out-of-organization error revealed fixture identifiers")
	}
}

func seedAutomationDispatchQueueScopeAssignment(
	tx *gorm.DB,
	project models.DeliveryProject,
	subject, suffix, key string,
	now time.Time,
	withHeartbeat bool,
) (uuid.UUID, string, error) {
	workItem := models.DeliveryWorkItem{
		ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject,
		Title: "Dispatch queue " + key + " work " + suffix, ExpectedOutcome: "exercise project scope",
		State: "implementation", IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`,
		AcceptanceJSON: `[]`, ClientContextJSON: `{}`, PlanJSON: `{}`,
		CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now,
	}
	gate := models.DeliveryGate{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: workItem.ID, Kind: "plan",
		Decision: "approved", DecidedBy: subject, EvidenceChecklist: `[]`,
		DecidedAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Minute),
	}
	plan := models.DeliveryPlan{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: workItem.ID, Version: 1,
		Status: "approved", Summary: "Dispatch queue integration fixture", StructuredJSON: `{}`,
		ContextDigest: "dispatch-queue-scope-v1", ProposedBy: "integration",
		ApprovedGateID: &gate.ID, CreatedAt: now.Add(-time.Minute),
	}
	step := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "scope-" + key,
		IdempotencyKey: "dispatch-scope-" + key + "-" + suffix, Role: models.DeliveryPlanStepRoleImplementation,
		DisplayOrder: 1, Title: "Dispatch scope " + key, Objective: "Verify selected project queue scope",
		AcceptanceCriteriaJSON: `[]`, EvidenceRequirementsJSON: `[]`, Status: models.DeliveryPlanStepReady,
		CreatedBy: subject, CreatedAt: now.Add(-45 * time.Second), UpdatedAt: now,
	}
	parentTaskID := uuid.Must(uuid.NewV4())
	workerID := "dispatch-queue-worker-" + suffix
	parentTask := models.AutomationTask{
		ID: parentTaskID, JobID: uuid.Must(uuid.NewV4()), RequestedBy: subject,
		DeliveryWorkItemID: &workItem.ID, WorkerID: workerID, AgentKey: "generalist",
		MachineID: "", CorrelationID: workItem.ID.String(), Operation: "delivery.implementation",
		InputRef: "private://dispatch-queue-scope/parent", Status: "running",
		CreatedAt: now.Add(-30 * time.Second), UpdatedAt: now,
	}
	childTask := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: subject,
		DeliveryWorkItemID: &workItem.ID, CorrelationID: workItem.ID.String(),
		Operation: "delivery.implementation", InputRef: "private://dispatch-queue-scope/child",
		Status: "queued", CreatedAt: now.Add(-20 * time.Second), UpdatedAt: now,
	}
	execution := models.DeliveryPlanExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: parentTask.ID,
		IdempotencyKey: "dispatch-execution-" + key + "-" + suffix,
		PlanID:         plan.ID, PlanVersion: plan.Version, ApprovedGateID: gate.ID,
		PlanHash: strings.Repeat("a", 64), MaxConcurrency: 2,
		Status:    models.DeliveryPlanExecutionRunning,
		CreatedAt: now.Add(-25 * time.Second), UpdatedAt: now,
	}
	machineID := ""
	if withHeartbeat {
		machineID = uuid.Must(uuid.NewV4()).String()
	}
	assignment := models.DeliveryPlanStepAssignment{
		ID: uuid.Must(uuid.NewV4()), ExecutionID: execution.ID,
		DeliveryPlanStepID: step.ID, ChildAutomationTaskID: childTask.ID,
		TargetMachineID: machineID, TargetAgentKey: "generalist",
		Status:    models.DeliveryPlanStepAssignmentQueued,
		QueuedAt:  timePointerForDispatchQueueScope(now.Add(-10 * time.Second)),
		CreatedAt: now.Add(-15 * time.Second), UpdatedAt: now,
	}
	for _, value := range []any{&workItem, &gate, &plan, &step, &parentTask, &childTask, &execution, &assignment} {
		if err := tx.Create(value).Error; err != nil {
			return uuid.Nil, "", err
		}
	}
	if withHeartbeat {
		heartbeat := models.AutomationAgentHeartbeat{
			ID: uuid.Must(uuid.NewV4()), WorkerID: workerID, AgentKey: "generalist", MachineID: machineID,
			Concurrency: 9, Draining: false, StartedAt: now.Add(-time.Hour),
			LastSeenAt: now.Add(-time.Second), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Second),
		}
		if err := tx.Create(&heartbeat).Error; err != nil {
			return uuid.Nil, "", err
		}
	}
	return assignment.ID, machineID, nil
}

func timePointerForDispatchQueueScope(value time.Time) *time.Time { return &value }
