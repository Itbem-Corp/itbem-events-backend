//go:build integration

package integration_test

import (
	"encoding/json"
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
)

func TestGlobalAutomationTracesOrganizationScopeIncludesDescendantsAndHidesOutsideProjects(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	subject := "trace-org-scope-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	clientType := models.ClientType{
		ID: uuid.Must(uuid.NewV4()), Name: "Trace scope client type " + suffix,
		Code: "TRACE_SCOPE_" + suffix, Level: 10, IsActive: true,
	}
	require.NoError(t, db.Create(&clientType).Error)

	newClient := func(name, code string, parentID *uuid.UUID) models.Client {
		client := models.Client{
			ID: uuid.Must(uuid.NewV4()), Name: name + " " + suffix,
			Code: code + "-" + suffix, ClientTypeID: clientType.ID,
			ParentID: parentID, IsActive: true,
		}
		require.NoError(t, db.Create(&client).Error)
		return client
	}
	organization := newClient("Trace organization", "trace-org", nil)
	child := newClient("Trace child client", "trace-child", &organization.ID)
	grandchild := newClient("Trace grandchild client", "trace-grandchild", &child.ID)
	siblingClient := newClient("Trace sibling client", "trace-sibling", &organization.ID)
	outsideOrganization := newClient("Outside trace organization", "trace-outside", nil)

	type seededProjectTrace struct {
		project models.DeliveryProject
		eventID uuid.UUID
	}
	seedProjectTrace := func(client models.Client, key string) seededProjectTrace {
		project := models.DeliveryProject{
			ID: uuid.Must(uuid.NewV4()), ClientID: client.ID,
			Name: "Trace scope " + key + " project " + suffix,
			Slug: "trace-scope-" + key + "-" + suffix, Status: "active", CreatedBy: subject,
		}
		workItem := models.DeliveryWorkItem{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject,
			Title: "Trace scope " + key + " task " + suffix, ExpectedOutcome: "Exercise organization trace filtering",
			State: "implementation",
		}
		plan := models.DeliveryPlan{
			ID: uuid.Must(uuid.NewV4()), WorkItemID: workItem.ID, Version: 1,
			Status: "approved", Summary: "Trace scope fixture", StructuredJSON: `{}`,
			CreatedAt: now.Add(-time.Minute),
		}
		step := models.DeliveryPlanStep{
			ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "trace-scope-" + key,
			IdempotencyKey: "trace-scope-" + key + "-" + suffix, DisplayOrder: 1,
			Title: "Trace scope " + key, AcceptanceCriteriaJSON: `[]`,
			Status: models.DeliveryPlanStepCompleted,
		}
		task := models.AutomationTask{
			ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()),
			DeliveryWorkItemID: &workItem.ID, RequestedBy: subject,
			CorrelationID: workItem.ID.String(), Operation: "delivery.implementation",
			AgentKey: "generalist", RunID: uuid.Must(uuid.NewV4()).String(),
			InputRef: "s3://trace-scope-fixture/input", Status: "completed",
			CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
		}
		workerID, machineID, runID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		event := models.DeliveryPlanStepActivityEvent{
			ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID,
			AutomationTaskID: task.ID, RunID: runID.String(), WorkerID: workerID.String(),
			AgentKey: "generalist", MachineID: machineID.String(), FencingToken: 1,
			Sequence: 1, Action: models.DeliveryPlanStepActivityFileChange,
			Phase:       models.DeliveryPlanStepActivityCompleted,
			Summary:     "Trace scope event " + key,
			DetailsJSON: `{"changed_files":["src/trace_scope.go"]}`,
			OccurredAt:  now.Add(-10 * time.Second), CreatedAt: now.Add(-10 * time.Second),
		}
		for _, value := range []any{&project, &workItem, &plan, &step, &task, &event} {
			require.NoError(t, db.Create(value).Error)
		}
		return seededProjectTrace{project: project, eventID: event.ID}
	}

	// The organization tree has a descendant two levels deep plus an in-scope
	// sibling project. A separate organization provides a truly out-of-scope
	// project for the authorization-denial assertion.
	descendantTrace := seedProjectTrace(grandchild, "descendant")
	siblingTrace := seedProjectTrace(siblingClient, "sibling")
	outsideTrace := seedProjectTrace(outsideOrganization, "outside")
	require.NotEqual(t, descendantTrace.project.ID, siblingTrace.project.ID)
	require.NotEqual(t, descendantTrace.project.ID, outsideTrace.project.ID)

	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			t.Fatalf("unexpected cognito subject %q", cognitoSub)
		}
		return &models.User{
			ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsRoot: true,
			RootLevel: models.RootLevelOperational, IsActive: true,
		}, nil
	}})
	t.Cleanup(restoreHooks)

	requestTracePage := func(projectID uuid.UUID) (int, []byte, struct {
		Items []struct {
			ID        uuid.UUID  `json:"id"`
			Kind      string     `json:"kind"`
			ProjectID *uuid.UUID `json:"project_id"`
			Summary   string     `json:"summary"`
		} `json:"items"`
	}) {
		query := url.Values{"project_id": {projectID.String()}, "limit": {"100"}}
		request := httptest.NewRequest(http.MethodGet, "/api/automation/traces?"+query.Encode(), nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		ctx.Set("cognito_sub", subject)
		ctx.Set("workspace_mode", "organization")
		ctx.Set("organization_id", organization.ID)
		err := automation.GetAutomationTraces(ctx)
		if recorder.Code == http.StatusOK {
			require.NoError(t, err)
		}
		var page struct {
			Items []struct {
				ID        uuid.UUID  `json:"id"`
				Kind      string     `json:"kind"`
				ProjectID *uuid.UUID `json:"project_id"`
				Summary   string     `json:"summary"`
			} `json:"items"`
		}
		if recorder.Code == http.StatusOK {
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.NoError(t, json.Unmarshal(envelope.Data, &page))
		} else {
			require.Error(t, err, "a denied project filter must stop before trace lookup")
		}
		return recorder.Code, recorder.Body.Bytes(), page
	}

	descendantStatus, descendantBody, descendantPage := requestTracePage(descendantTrace.project.ID)
	require.Equal(t, http.StatusOK, descendantStatus, string(descendantBody))
	require.NotEmpty(t, descendantPage.Items)
	foundDescendantEvent := false
	for _, item := range descendantPage.Items {
		require.NotNil(t, item.ProjectID)
		require.Equal(t, descendantTrace.project.ID, *item.ProjectID, "project_id filter must exclude sibling project events")
		if item.ID == descendantTrace.eventID {
			foundDescendantEvent = true
			require.Equal(t, "step_activity", item.Kind)
		}
	}
	require.True(t, foundDescendantEvent, "the event attached to the grandchild client's project must be visible")
	for _, outOfFilter := range []string{
		siblingTrace.project.ID.String(), siblingTrace.eventID.String(), siblingTrace.project.Name,
		outsideTrace.project.ID.String(), outsideTrace.eventID.String(), outsideTrace.project.Name,
	} {
		require.NotContains(t, string(descendantBody), outOfFilter)
	}

	outsideStatus, outsideBody, _ := requestTracePage(outsideTrace.project.ID)
	require.Equal(t, http.StatusNotFound, outsideStatus, string(outsideBody))
	for _, privateValue := range []string{
		outsideOrganization.ID.String(), outsideTrace.project.ID.String(), outsideTrace.eventID.String(),
		outsideTrace.project.Name, "Trace scope outside", "s3://trace-scope-fixture",
	} {
		require.NotContains(t, string(outsideBody), privateValue)
	}

	// Compare the denial with a nonexistent project to ensure the API does not
	// reveal whether an out-of-scope project ID is real.
	missingStatus, missingBody, _ := requestTracePage(uuid.Must(uuid.NewV4()))
	require.Equal(t, http.StatusNotFound, missingStatus)
	var outsideEnvelope, missingEnvelope map[string]any
	require.NoError(t, json.Unmarshal(outsideBody, &outsideEnvelope))
	require.NoError(t, json.Unmarshal(missingBody, &missingEnvelope))
	require.Equal(t, missingEnvelope, outsideEnvelope, "out-of-scope and nonexistent projects must have indistinguishable error envelopes")
	for _, privateValue := range []string{outsideTrace.project.ID.String(), outsideTrace.eventID.String(), outsideTrace.project.Name} {
		require.NotContains(t, string(missingBody), privateValue)
	}
}
