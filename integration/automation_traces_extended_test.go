//go:build integration

package integration_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// This regression covers the extended cross-source trace projection using only
// the disposable integration database. The fixture and requests share one SQL
// transaction, which is rolled back even when assertions pass.
func TestGlobalAutomationTracesIncludeAssignmentGateAndEvidenceSafely(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	subject := "extended-traces-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	rollback := errors.New("rollback extended trace fixture")

	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			t.Fatalf("unexpected cognito subject %q", cognitoSub)
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelPrimary, IsActive: true}, nil
	}})
	t.Cleanup(restoreAuth)

	err := db.Transaction(func(tx *gorm.DB) error {
		clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Extended trace type " + suffix, Code: "EXT_TRACE_" + strings.ToUpper(suffix), Level: 10, IsActive: true}
		client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Extended trace client " + suffix, Code: "ext-trace-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
		project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Extended trace project " + suffix, Slug: "extended-trace-" + suffix, Status: "active", CreatedBy: subject}
		workItem := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Trace marker work " + suffix, ExpectedOutcome: "Exercise the unified trace timeline", State: "implementation"}
		epic := models.DeliveryEpic{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, Title: "Trace marker epic " + suffix, Status: "active", CreatedBy: subject, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
		epicWorkItem := models.DeliveryEpicWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, EpicID: epic.ID, WorkItemID: workItem.ID, CreatedBy: subject, CreatedAt: now.Add(-time.Minute)}
		gateCommentSecret := "PRIVATE_GATE_COMMENT_SECRET_" + suffix
		gate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: workItem.ID, Kind: "plan", Decision: "approved", DecidedBy: subject, Comment: gateCommentSecret, EvidenceChecklist: `[]`, DecidedAt: now.Add(-45 * time.Second), CreatedAt: now.Add(-45 * time.Second)}
		plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: workItem.ID, Version: 1, Status: "approved", Summary: "Safe trace plan", StructuredJSON: `{}`, ProposedBy: "integration", ApprovedGateID: &gate.ID, CreatedAt: now.Add(-time.Minute)}
		step := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "trace-safe-step", IdempotencyKey: "trace-safe-step-" + suffix, Role: models.DeliveryPlanStepRoleImplementation, DisplayOrder: 1, Title: "Record a safe trace marker", AcceptanceCriteriaJSON: `[]`, EvidenceRequirementsJSON: `[]`, Status: models.DeliveryPlanStepCompleted, CreatedAt: now.Add(-time.Minute), UpdatedAt: now}

		parentTask := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &workItem.ID, RequestedBy: subject, CorrelationID: workItem.ID.String(), Operation: "delivery.implementation", AgentKey: "generalist", RunID: uuid.Must(uuid.NewV4()).String(), InputRef: "s3://private-trace/PRIVATE_INPUT_" + suffix, Status: "completed", CreatedAt: now.Add(-time.Minute), UpdatedAt: now}
		childTask := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &workItem.ID, RequestedBy: subject, CorrelationID: workItem.ID.String(), Operation: "delivery.implementation", AgentKey: "builder", RunID: uuid.Must(uuid.NewV4()).String(), InputRef: "s3://private-trace/PRIVATE_CHILD_INPUT_" + suffix, Status: "completed", CreatedAt: now.Add(-30 * time.Second), UpdatedAt: now}
		for _, value := range []any{&clientType, &client, &project, &workItem, &epic, &epicWorkItem, &gate, &plan, &step, &parentTask, &childTask} {
			if err := tx.Create(value).Error; err != nil {
				return err
			}
		}

		planExecution := models.DeliveryPlanExecution{
			ID: uuid.Must(uuid.NewV4()), AutomationTaskID: parentTask.ID, IdempotencyKey: "trace-execution-" + suffix,
			PlanID: plan.ID, PlanVersion: 1, ApprovedGateID: gate.ID, PlanHash: strings.Repeat("a", 64),
			MaxConcurrency: 2, Status: models.DeliveryPlanExecutionRunning, CreatedAt: now.Add(-30 * time.Second), UpdatedAt: now,
		}
		if err := tx.Create(&planExecution).Error; err != nil {
			return err
		}
		previousMachineID, targetMachineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
		assignment := models.DeliveryPlanStepAssignment{
			ID: uuid.Must(uuid.NewV4()), ExecutionID: planExecution.ID, DeliveryPlanStepID: step.ID,
			ChildAutomationTaskID: childTask.ID, TargetMachineID: previousMachineID, TargetAgentKey: "worker-before",
			Status: models.DeliveryPlanStepAssignmentQueued, CreatedAt: now.Add(-20 * time.Second), UpdatedAt: now.Add(-20 * time.Second),
		}
		if err := tx.Create(&assignment).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ?", assignment.ID).Updates(map[string]any{
			"status":            models.DeliveryPlanStepAssignmentDispatched,
			"target_agent_key":  "worker-after",
			"target_machine_id": targetMachineID,
			"updated_at":        now,
		}).Error; err != nil {
			return err
		}
		var assignmentEvents []models.DeliveryPlanStepAssignmentEvent
		if err := tx.Where("assignment_id = ?", assignment.ID).Order("occurred_at ASC, id ASC").Find(&assignmentEvents).Error; err != nil {
			return err
		}
		if len(assignmentEvents) != 2 {
			return errors.New("assignment trigger did not write both immutable events")
		}

		workerID, machineID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		stepEvent := models.DeliveryPlanStepEvent{
			ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID, EventType: models.DeliveryPlanStepEventTransitioned,
			FromStatus: models.DeliveryPlanStepRunning, ToStatus: models.DeliveryPlanStepCompleted,
			AutomationTaskID: childTask.ID, RunID: childTask.RunID, WorkerID: workerID.String(), AgentKey: "builder",
			MachineID: machineID.String(), AgentInstanceID: &instanceID, LeaseFence: 1, Summary: "Evidence captured",
			OccurredAt: now.Add(-10 * time.Second), CreatedAt: now.Add(-10 * time.Second),
		}
		if err := tx.Create(&stepEvent).Error; err != nil {
			return err
		}
		fileNameSecret := "PRIVATE_FILENAME_SECRET_" + suffix + ".txt"
		objectKeySecret := "PRIVATE_OBJECT_KEY_SECRET_" + suffix
		contentSecret := "PRIVATE_CONTENT_SECRET_" + suffix
		stepEvidence := models.DeliveryPlanStepEvidence{
			ID: uuid.Must(uuid.NewV4()), EventID: stepEvent.ID, PlanID: plan.ID, PlanVersion: 1, StepID: step.ID,
			RequirementKey: "trace-report", AutomationTaskID: childTask.ID, RunID: childTask.RunID,
			WorkerID: workerID.String(), AgentKey: "builder", MachineID: machineID.String(), AgentInstanceID: instanceID,
			FencingToken: 1, FileName: fileNameSecret, ContentType: "text/plain", Bucket: "private-trace-bucket",
			ObjectKey: objectKeySecret + "/" + contentSecret, SHA256: strings.Repeat("b", 64), SizeBytes: 32, CreatedAt: now.Add(-8 * time.Second),
		}
		if err := tx.Create(&stepEvidence).Error; err != nil {
			return err
		}
		// A legacy evidence record makes the content/reference redaction assertion
		// robust if trace aggregation later consumes both evidence stores.
		legacyEvidence := models.DeliveryEvidence{
			ID: uuid.Must(uuid.NewV4()), WorkItemID: workItem.ID, Kind: "report", Phase: "implementation",
			Title: "Trace marker evidence", Reference: "s3://private-trace-bucket/" + objectKeySecret,
			MetadataJSON: `{"content":"` + contentSecret + `","token":"provider-secret-` + suffix + `"}`,
			CapturedBy:   "builder", CreatedAt: now.Add(-8 * time.Second),
		}
		if err := tx.Create(&legacyEvidence).Error; err != nil {
			return err
		}

		previousDB := configuration.DB
		configuration.DB = tx
		defer func() { configuration.DB = previousDB }()

		getPage := func(query url.Values) (int, struct {
			Items      []map[string]any `json:"items"`
			HasMore    bool             `json:"has_more"`
			NextCursor string           `json:"next_cursor"`
			SnapshotAt time.Time        `json:"snapshot_at"`
			Limit      int              `json:"limit"`
		}, string) {
			request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?"+query.Encode(), nil)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.Set("cognito_sub", subject)
			ctx.Set("workspace_mode", "platform")
			if err := automation.GetAutomationTraces(ctx); err != nil {
				t.Fatalf("GetAutomationTraces returned an error: %v", err)
			}
			require.Equal(t, "private, no-store", recorder.Header().Get(echo.HeaderCacheControl))
			var envelope struct {
				Data struct {
					Items      []map[string]any `json:"items"`
					HasMore    bool             `json:"has_more"`
					NextCursor string           `json:"next_cursor"`
					SnapshotAt time.Time        `json:"snapshot_at"`
					Limit      int              `json:"limit"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
			return recorder.Code, envelope.Data, recorder.Body.String()
		}

		base := url.Values{
			"client_id": {client.ID.String()}, "project_id": {project.ID.String()},
			"epic_id": {epic.ID.String()}, "work_item_id": {workItem.ID.String()}, "q": {workItem.Title}, "limit": {"1"},
		}
		seenKinds := make(map[string]int)
		seenTraceItems := make(map[string]struct{})
		seenAssignmentEvents := make(map[string]map[string]any)
		seenStepEvidence := false
		seenGateDecision := false
		var gateDecisionItem map[string]any
		query := cloneTraceQuery(base)
		var firstPage struct {
			Items      []map[string]any `json:"items"`
			HasMore    bool             `json:"has_more"`
			NextCursor string           `json:"next_cursor"`
			SnapshotAt time.Time        `json:"snapshot_at"`
			Limit      int              `json:"limit"`
		}
		for pageNumber := 0; pageNumber < 30; pageNumber++ {
			status, page, body := getPage(query)
			require.Equal(t, http.StatusOK, status, body)
			require.Equal(t, 1, page.Limit)
			if pageNumber == 0 {
				firstPage = page
				require.True(t, page.HasMore, "the small page size must exercise cursor pagination: %s", body)
				require.NotEmpty(t, page.NextCursor)
			} else {
				require.True(t, page.SnapshotAt.Equal(firstPage.SnapshotAt), "cursor pages must retain the first page snapshot")
			}
			for _, privateValue := range []string{gateCommentSecret, fileNameSecret, objectKeySecret, contentSecret, "provider-secret-" + suffix, "private-trace-bucket", "s3://private-trace/PRIVATE_"} {
				require.NotContains(t, body, privateValue, "sensitive trace field leaked on page %d", pageNumber+1)
			}
			for _, item := range page.Items {
				kind, _ := item["kind"].(string)
				itemID, _ := item["id"].(string)
				key := kind + ":" + itemID
				_, alreadySeen := seenTraceItems[key]
				require.False(t, alreadySeen, "cursor pagination repeated trace item %s", key)
				seenTraceItems[key] = struct{}{}
				seenKinds[kind]++
				if kind == "assignment_event" {
					if eventType, _ := item["event_type"].(string); eventType == models.DeliveryPlanStepAssignmentEventStatusAndTargetChanged {
						seenAssignmentEvents[eventType] = item
					}
				}
				if kind == "step_evidence" {
					seenStepEvidence = true
				}
				if kind == "gate_decision" {
					seenGateDecision = true
					gateDecisionItem = item
				}
				for key, expected := range map[string]string{
					"client_id": client.ID.String(), "project_id": project.ID.String(),
					"epic_id": epic.ID.String(), "work_item_id": workItem.ID.String(),
				} {
					if actual, _ := item[key].(string); actual != "" {
						require.Equal(t, expected, actual, "incorrect hierarchy field %s for %s trace: %s", key, kind, body)
					}
				}
				if kind == "assignment_event" || kind == "step_evidence" {
					require.Equal(t, step.ID.String(), item["step_id"], "step-level event lost its step hierarchy: %s", body)
					require.Equal(t, step.StepKey, item["step_key"], "step-level event lost its stable step key: %s", body)
				}
			}
			if !page.HasMore {
				require.Empty(t, page.NextCursor)
				break
			}
			require.NotEmpty(t, page.NextCursor)
			query = cloneTraceQuery(base)
			query.Set("cursor", page.NextCursor)
			query.Set("snapshot_at", page.SnapshotAt.Format(time.RFC3339Nano))
			if pageNumber == 29 {
				t.Fatal("trace cursor pagination did not terminate")
			}
		}
		// A cursor is tied to the original filter set; changing even a search
		// filter while continuing must fail before a different result set is read.
		changedFilterQuery := cloneTraceQuery(base)
		changedFilterQuery.Set("cursor", firstPage.NextCursor)
		changedFilterQuery.Set("snapshot_at", firstPage.SnapshotAt.Format(time.RFC3339Nano))
		changedFilterQuery.Set("q", "a different trace search")
		changedFilterStatus, _, changedFilterBody := getPage(changedFilterQuery)
		require.Equal(t, http.StatusBadRequest, changedFilterStatus, changedFilterBody)
		require.GreaterOrEqual(t, seenKinds["assignment_event"], 2)
		require.True(t, seenStepEvidence, "timeline omitted server-verified step evidence")
		require.True(t, seenGateDecision, "timeline omitted the human gate decision")
		require.Equal(t, "plan", gateDecisionItem["event_type"])
		require.Equal(t, "approved", gateDecisionItem["status"])
		changedAssignment, ok := seenAssignmentEvents[models.DeliveryPlanStepAssignmentEventStatusAndTargetChanged]
		require.True(t, ok, "timeline omitted status/target transition event")
		require.Equal(t, models.DeliveryPlanStepAssignmentQueued, changedAssignment["previous_status"])
		require.Equal(t, models.DeliveryPlanStepAssignmentDispatched, changedAssignment["status"])
		require.Equal(t, "worker-before", changedAssignment["previous_agent_key"])
		require.Equal(t, "worker-after", changedAssignment["agent_key"])
		require.Equal(t, previousMachineID, changedAssignment["previous_machine_id"])
		require.Equal(t, targetMachineID, changedAssignment["machine_id"])

		// The same global endpoint must narrow the timeline by a stable plan-step
		// identifier, while the q filter must find the work item without exposing
		// any private gate/evidence material.
		stepQuery := url.Values{"client_id": {client.ID.String()}, "project_id": {project.ID.String()}, "step_id": {step.ID.String()}, "q": {"trace marker"}, "limit": {"100"}}
		stepStatus, stepPage, stepBody := getPage(stepQuery)
		require.Equal(t, http.StatusOK, stepStatus, stepBody)
		stepKinds := map[string]bool{}
		for _, item := range stepPage.Items {
			kind, _ := item["kind"].(string)
			stepKinds[kind] = true
			require.Equal(t, step.ID.String(), item["step_id"])
		}
		require.True(t, stepKinds["assignment_event"], "step_id filter omitted assignments: %s", stepBody)
		require.True(t, stepKinds["step_evidence"], "step_id filter omitted evidence: %s", stepBody)
		require.NotContains(t, stepBody, gateCommentSecret)
		require.NotContains(t, stepBody, fileNameSecret)
		require.NotContains(t, stepBody, objectKeySecret)
		require.NotContains(t, stepBody, contentSecret)

		return rollback
	})
	require.ErrorIs(t, err, rollback, "the extended timeline fixture must roll back")
}

func cloneTraceQuery(source url.Values) url.Values {
	clone := make(url.Values, len(source))
	for key, values := range source {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}
