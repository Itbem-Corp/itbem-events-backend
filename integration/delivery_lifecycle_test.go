//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	delivery "events-stocks/controllers/delivery"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// TestDeliveryLifecycleThroughController traverses the local control-plane
// handlers with a real database. It provides stronger evidence than the pure
// state-machine test: every review gate requires the same persisted plan,
// agent result, change set, preview and release report that production uses.
// No provider, GitHub App, remote publication or EventiApp record is touched.
func TestDeliveryLifecycleThroughController(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	subject := "integration-lifecycle-" + suffix
	now := time.Now().UTC()
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Lifecycle customer " + suffix, Code: "LIFECYCLE_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Lifecycle customer " + suffix, Code: "lifecycle-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Local lifecycle " + suffix, Slug: "local-lifecycle-" + suffix, Summary: "integration", Status: "active", CreatedBy: subject}
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Local lifecycle", Description: "Traverse every delivery gate", ExpectedOutcome: "Released only after evidence", State: deliveryworkflow.StatePlanning, PlanJSON: `{}`}
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "proposed", Summary: "Bounded local plan", StructuredJSON: `{"summary":"local","repository_impact":[]}`}
	change := models.DeliveryChangeSet{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, RepositoryRef: "workspace://fixture", Branch: "itbem-agent/" + item.ID.String(), CommitSHA: "local-sha", ReviewType: "local_worktree", CIStatus: "passed", PreviewURL: "http://preview.local/lifecycle", Environment: "preview", MetadataJSON: `{}`, CreatedBy: subject}
	release := models.DeliveryRelease{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, WorkItemID: item.ID, Status: "ready", ExecutiveJSON: `{"what_changed":"Local lifecycle"}`, TechnicalJSON: `{"decisions":["all gates recorded"]}`, ReportRef: "artifact://lifecycle-report"}
	for _, value := range []any{&clientType, &client, &project, &item, &plan, &change, &release} {
		require.NoError(t, db.Create(value).Error)
	}
	t.Cleanup(func() {
		for _, model := range []any{&models.DeliveryEvidence{}, &models.DeliveryGate{}, &models.DeliveryContinuation{}, &models.AutomationTask{}, &models.DeliveryRelease{}, &models.DeliveryChangeSet{}, &models.DeliveryPlan{}, &models.DeliveryWorkItem{}, &models.DeliveryProjectMember{}, &models.DeliveryProject{}, &models.Client{}, &models.ClientType{}} {
			_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(model).Error
			_ = db.Unscoped().Where("project_id = ?", project.ID).Delete(model).Error
			_ = db.Unscoped().Where("id = ?", client.ID).Delete(model).Error
			_ = db.Unscoped().Where("id = ?", clientType.ID).Delete(model).Error
		}
	})
	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			t.Fatalf("unexpected cognito subject %q", cognitoSub)
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelOperational, IsActive: true}, nil
	}})
	t.Cleanup(restoreHooks)

	completedTask := func(operation, phase string) {
		t.Helper()
		completed := now.Add(time.Duration(len(operation)) * time.Second)
		task := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &item.ID, RequestedBy: subject, CorrelationID: item.ID.String(), Operation: operation, MaxCompletionTokens: 512, InputRef: "s3://local/" + phase + "/input", OutputRef: "s3://local/" + phase + "/result", Provider: "minimax", Model: "MiniMax-M3", Status: "completed", CompletedAt: &completed, CreatedAt: now, UpdatedAt: completed}
		require.NoError(t, db.Create(&task).Error)
	}
	transition := func(action deliveryworkflow.Action, comment string, checklist []string) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"action": action, "comment": comment, "evidence_checklist": checklist})
		require.NoError(t, err)
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+item.ID.String()+"/transitions", bytes.NewReader(payload))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		recorder := httptest.NewRecorder()
		c := e.NewContext(req, recorder)
		c.SetPath("/api/automation/work-items/:id/transitions")
		c.SetParamNames("id")
		c.SetParamValues(item.ID.String())
		c.Set("cognito_sub", subject)
		c.Set("tenant_code", "itbem")
		require.NoError(t, delivery.TransitionWorkItem(c))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.NoError(t, db.First(&item, item.ID).Error)
	}

	completedTask("delivery.plan", "plan")
	transition(deliveryworkflow.ActionSubmitPlan, "Plan listo para revisión", nil)
	transition(deliveryworkflow.ActionApprovePlan, "Alcance y validaciones revisados", []string{"plan estructurado", "alcance confirmado"})
	completedTask("delivery.implementation", "implementation")
	transition(deliveryworkflow.ActionSubmitCodeReview, "Cambio listo para revisión", nil)
	transition(deliveryworkflow.ActionApproveCodeReview, "Cambio revisado y CI local aprobado", []string{"diff revisado", "CI local aprobado"})
	transition(deliveryworkflow.ActionPreviewReady, "Preview local verificado", nil)
	completedTask("delivery.qa", "qa")
	transition(deliveryworkflow.ActionSubmitQA, "QA local listo para revisión", nil)
	transition(deliveryworkflow.ActionApproveQA, "QA y evidencia visual revisados", []string{"checks QA", "evidencia revisada"})
	completedTask("delivery.summary", "summary")
	transition(deliveryworkflow.ActionApproveRelease, "Entrega local autorizada", []string{"resumen revisado", "release autorizado"})

	require.Equal(t, deliveryworkflow.StateReleased, item.State)
	var persisted models.DeliveryRelease
	require.NoError(t, db.Where("work_item_id = ?", item.ID).First(&persisted).Error)
	require.Equal(t, "released", persisted.Status)
	var gates []models.DeliveryGate
	require.NoError(t, db.Where("work_item_id = ?", item.ID).Order("decided_at ASC").Find(&gates).Error)
	require.Len(t, gates, 4)
}

// TestDeliveryMultiRepositoryLifecycleRequiresIntegratedPreview proves that
// the strict multirepository gate is part of the same persisted work-item
// traversal, rather than only a standalone helper test. Both reviewed
// repositories must have an authorized publication bound to their exact
// reviewed diff, and both publications must expose the same integrated
// preview before QA can begin. No provider, GitHub App, remote publication or
// EventiApp record is touched.
func TestDeliveryMultiRepositoryLifecycleRequiresIntegratedPreview(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	subject := "integration-multirepo-" + suffix
	now := time.Now().UTC()
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Multirepo customer " + suffix, Code: "MULTI_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Multirepo customer " + suffix, Code: "multirepo-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Integrated preview " + suffix, Slug: "integrated-preview-" + suffix, Summary: "integration", Status: "active", CreatedBy: subject}
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Integrated multirepo lifecycle", Description: "Traverse every delivery gate with two repositories", ExpectedOutcome: "Released only after one integrated preview", State: deliveryworkflow.StatePlanning}
	planJSON := `{"summary":"integrated multirepo","repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://dashboard","impact":"changes"}]}`
	item.PlanJSON = planJSON
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "proposed", Summary: "Integrated multirepo plan", StructuredJSON: planJSON}
	sharedPreview := "http://preview.local/integrated-" + suffix
	digest := strings.Repeat("a", 64)
	branchFor := func(repository string) string {
		return "itbem-agent/" + item.ID.String() + "-" + repository
	}
	reviewMetadata := func(repository, branch string, taskID uuid.UUID) string {
		encoded, err := json.Marshal(map[string]string{
			"verification_source": "itbem-local-agent",
			"automation_task_id":  taskID.String(),
			"worktree":            repository + "#" + branch,
			"review_diff_sha256":  digest,
		})
		require.NoError(t, err)
		return string(encoded)
	}
	publishedMetadata := func() string {
		encoded, err := json.Marshal(map[string]any{
			"branch_published":    true,
			"verification_source": "itbem-github-app",
			"review_diff_sha256":  digest,
		})
		require.NoError(t, err)
		return string(encoded)
	}
	apiBranch := branchFor("api")
	dashboardBranch := branchFor("dashboard")
	apiReviewTaskID := uuid.Must(uuid.NewV4())
	dashboardReviewTaskID := uuid.Must(uuid.NewV4())
	apiReview := models.DeliveryChangeSet{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, RepositoryRef: "workspace://api", Branch: apiBranch, CommitSHA: "api-local-sha", ReviewType: "local_worktree", CIStatus: "passed", Environment: "local", MetadataJSON: reviewMetadata("workspace://api", apiBranch, apiReviewTaskID), CreatedBy: "itbem-local-agent"}
	dashboardReview := models.DeliveryChangeSet{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, RepositoryRef: "workspace://dashboard", Branch: dashboardBranch, CommitSHA: "dashboard-local-sha", ReviewType: "local_worktree", CIStatus: "passed", Environment: "local", MetadataJSON: reviewMetadata("workspace://dashboard", dashboardBranch, dashboardReviewTaskID), CreatedBy: "itbem-local-agent"}
	apiPublication := models.DeliveryChangeSet{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, RepositoryRef: apiReview.RepositoryRef, Branch: apiBranch, CommitSHA: apiReview.CommitSHA, ReviewType: "pull_request", CIStatus: "passed", PreviewURL: sharedPreview, Environment: "preview", MetadataJSON: publishedMetadata(), CreatedBy: "itbem-github-app"}
	dashboardPublication := models.DeliveryChangeSet{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, RepositoryRef: dashboardReview.RepositoryRef, Branch: dashboardBranch, CommitSHA: dashboardReview.CommitSHA, ReviewType: "pull_request", CIStatus: "passed", PreviewURL: sharedPreview, Environment: "preview", MetadataJSON: publishedMetadata(), CreatedBy: "itbem-github-app"}
	release := models.DeliveryRelease{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, WorkItemID: item.ID, Status: "ready", ExecutiveJSON: `{"what_changed":"Integrated multirepo lifecycle"}`, TechnicalJSON: `{"decisions":["one shared preview bound to two reviewed diffs"]}`, ReportRef: "artifact://integrated-multirepo-report"}
	for _, value := range []any{&clientType, &client, &project, &item, &plan, &apiReview, &dashboardReview, &apiPublication, &dashboardPublication, &release} {
		require.NoError(t, db.Create(value).Error)
	}
	t.Cleanup(func() {
		for _, model := range []any{&models.DeliveryEvidence{}, &models.DeliveryGate{}, &models.DeliveryContinuation{}, &models.AutomationTask{}, &models.DeliveryRelease{}, &models.DeliveryChangeSet{}, &models.DeliveryPlan{}, &models.DeliveryWorkItem{}, &models.DeliveryProjectMember{}, &models.DeliveryProject{}, &models.Client{}, &models.ClientType{}} {
			_ = db.Unscoped().Where("work_item_id = ?", item.ID).Delete(model).Error
			_ = db.Unscoped().Where("project_id = ?", project.ID).Delete(model).Error
			_ = db.Unscoped().Where("id = ?", client.ID).Delete(model).Error
			_ = db.Unscoped().Where("id = ?", clientType.ID).Delete(model).Error
		}
	})
	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			t.Fatalf("unexpected cognito subject %q", cognitoSub)
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelOperational, IsActive: true}, nil
	}})
	t.Cleanup(restoreHooks)

	completedTask := func(operation, phase string) {
		t.Helper()
		completed := now.Add(time.Duration(len(operation)+len(phase)) * time.Second)
		task := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &item.ID, RequestedBy: subject, CorrelationID: item.ID.String(), Operation: operation, MaxCompletionTokens: 512, InputRef: "s3://local/" + phase + "/input", OutputRef: "s3://local/" + phase + "/result-" + uuid.Must(uuid.NewV4()).String(), Provider: "minimax", Model: "MiniMax-M3", Status: "completed", CompletedAt: &completed, CreatedAt: now, UpdatedAt: completed}
		require.NoError(t, db.Create(&task).Error)
	}
	transition := func(action deliveryworkflow.Action, comment string, checklist []string) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"action": action, "comment": comment, "evidence_checklist": checklist})
		require.NoError(t, err)
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+item.ID.String()+"/transitions", bytes.NewReader(payload))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		recorder := httptest.NewRecorder()
		c := e.NewContext(req, recorder)
		c.SetPath("/api/automation/work-items/:id/transitions")
		c.SetParamNames("id")
		c.SetParamValues(item.ID.String())
		c.Set("cognito_sub", subject)
		c.Set("tenant_code", "itbem")
		require.NoError(t, delivery.TransitionWorkItem(c))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.NoError(t, db.First(&item, item.ID).Error)
	}

	completedTask("delivery.plan", "plan")
	transition(deliveryworkflow.ActionSubmitPlan, "Plan multirepo listo para revisión", nil)
	transition(deliveryworkflow.ActionApprovePlan, "Alcance de API y dashboard revisado", []string{"matriz de repositorios", "alcance confirmado"})
	completedTask("delivery.implementation", "implementation")
	transition(deliveryworkflow.ActionSubmitCodeReview, "Ambos cambios listos para revisión", nil)
	transition(deliveryworkflow.ActionApproveCodeReview, "Dos diffs revisados y CI aprobado", []string{"diff API", "diff dashboard", "CI aprobado"})
	completedTask("delivery.qa", "qa")
	transition(deliveryworkflow.ActionPreviewReady, "Preview integrado de ambos repositorios verificado", nil)
	transition(deliveryworkflow.ActionSubmitQA, "QA integrado listo para revisión", nil)
	transition(deliveryworkflow.ActionApproveQA, "QA y evidencia integrada revisados", []string{"preview integrado", "checks API", "checks dashboard"})
	completedTask("delivery.summary", "summary")
	transition(deliveryworkflow.ActionApproveRelease, "Entrega multirepo local autorizada", []string{"resumen revisado", "release autorizado"})

	require.Equal(t, deliveryworkflow.StateReleased, item.State)
	var persisted models.DeliveryRelease
	require.NoError(t, db.Where("work_item_id = ?", item.ID).First(&persisted).Error)
	require.Equal(t, "released", persisted.Status)
	var persistedChanges []models.DeliveryChangeSet
	require.NoError(t, db.Where("work_item_id = ?", item.ID).Order("repository_ref ASC, review_type ASC").Find(&persistedChanges).Error)
	require.Len(t, persistedChanges, 4)
	for _, change := range persistedChanges {
		if change.ReviewType == "pull_request" {
			require.Equal(t, sharedPreview, change.PreviewURL)
		}
	}
	var gates []models.DeliveryGate
	require.NoError(t, db.Where("work_item_id = ?", item.ID).Order("decided_at ASC").Find(&gates).Error)
	require.Len(t, gates, 4)
}
