//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestDeliveryDecompositionApplyIsAtomicAndContextBound proves the request
// level decomposition contract through the same HTTP handlers used by the
// dashboard. A valid three-node DAG is proposed, a changed source revision is
// rejected without materialising partial work, then a fresh proposal is
// applied and its snapshots and dependency links are checked exactly.
//
// This test is local-only: it uses the integration package's ephemeral
// PostgreSQL database and never starts an agent, queue, GitHub client or
// EventiApp workflow.
func TestDeliveryDecompositionApplyIsAtomicAndContextBound(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	subject := "integration-decomposition-" + suffix
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Decomposition customer " + suffix, Code: "DECOMP_TYPE_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Decomposition customer " + suffix, Code: "decomposition-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Local decomposition " + suffix, Slug: "local-decomposition-" + suffix, Summary: "integration", Status: "active", CreatedBy: subject}
	request := models.DeliveryRequest{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: subject, Title: "Split bounded delivery", Body: "Create a verifiable local DAG", ExpectedOutcome: "Three independently traceable work items", Status: "open"}
	synced := time.Now().UTC()
	source := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, Kind: "repository", Name: "Local workspace", Reference: "workspace://eventiapp", Revision: "rev-1", Status: "ready", MetadataJSON: `{}`, SyncedAt: &synced}
	for _, value := range []any{&clientType, &client, &project, &request, &source} {
		require.NoError(t, db.Create(value).Error)
	}
	t.Cleanup(func() {
		var itemIDs []uuid.UUID
		_ = db.Model(&models.DeliveryWorkItem{}).Where("project_id = ?", project.ID).Pluck("id", &itemIDs).Error
		for _, itemID := range itemIDs {
			_ = db.Unscoped().Where("work_item_id = ? OR depends_on_work_item_id = ?", itemID, itemID).Delete(&models.DeliveryWorkItemDependency{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryContextSnapshot{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryContinuation{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryEvidence{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.DeliveryGate{}).Error
			_ = db.Unscoped().Where("work_item_id = ?", itemID).Delete(&models.AutomationTask{}).Error
		}
		_ = db.Unscoped().Where("request_id = ?", request.ID).Delete(&models.DeliveryDecomposition{}).Error
		_ = db.Unscoped().Where("project_id = ?", project.ID).Delete(&models.DeliveryWorkItem{}).Error
		_ = db.Unscoped().Where("project_id = ?", project.ID).Delete(&models.DeliveryContextSource{}).Error
		_ = db.Unscoped().Where("id = ?", request.ID).Delete(&models.DeliveryRequest{}).Error
		_ = db.Unscoped().Where("id = ?", project.ID).Delete(&models.DeliveryProject{}).Error
		_ = db.Unscoped().Where("id = ?", client.ID).Delete(&models.Client{}).Error
		_ = db.Unscoped().Where("id = ?", clientType.ID).Delete(&models.ClientType{}).Error
	})
	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			t.Fatalf("unexpected cognito subject %q", cognitoSub)
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsRoot: true, RootLevel: models.RootLevelOperational, IsActive: true}, nil
	}})
	t.Cleanup(restoreHooks)

	proposal := map[string]any{
		"summary": "Entrega local en tres etapas",
		"tasks": []any{
			map[string]any{"key": "foundation", "title": "Preparar base", "description": "Preparar el contexto", "expected_outcome": "Base lista", "included_scope": []string{"workspace"}, "excluded_scope": []string{"produccion"}, "acceptance_criteria": []string{"base comprobable"}, "context_references": []string{"workspace://eventiapp"}, "depends_on": []string{}, "budget_microusd": 0},
			map[string]any{"key": "independent", "title": "Validar pista paralela", "description": "Validar una pista independiente", "expected_outcome": "Pista lista", "included_scope": []string{"validacion"}, "excluded_scope": []string{"produccion"}, "acceptance_criteria": []string{"validacion comprobable"}, "context_references": []string{"workspace://eventiapp"}, "depends_on": []string{}, "budget_microusd": 0},
			map[string]any{"key": "implementation", "title": "Implementar cambio", "description": "Aplicar el cambio", "expected_outcome": "Cambio verificable", "included_scope": []string{"codigo"}, "excluded_scope": []string{"secretos"}, "acceptance_criteria": []string{"pruebas verdes"}, "context_references": []string{"workspace://eventiapp"}, "depends_on": []string{"foundation"}, "budget_microusd": 0},
			map[string]any{"key": "qa", "title": "Verificar entrega", "description": "Validar el resultado", "expected_outcome": "Resultado aprobado", "included_scope": []string{"tests"}, "excluded_scope": []string{"produccion"}, "acceptance_criteria": []string{"evidencia persistida"}, "context_references": []string{"workspace://eventiapp"}, "depends_on": []string{"implementation"}, "budget_microusd": 0},
		},
	}

	create := func() (models.DeliveryDecomposition, *httptest.ResponseRecorder) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"structured": proposal})
		require.NoError(t, err)
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/api/automation/projects/"+project.ID.String()+"/requests/"+request.ID.String()+"/decompositions", bytes.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		recorder := httptest.NewRecorder()
		c := e.NewContext(req, recorder)
		c.SetPath("/api/automation/projects/:id/requests/:requestID/decompositions")
		c.SetParamNames("id", "requestID")
		c.SetParamValues(project.ID.String(), request.ID.String())
		c.Set("cognito_sub", subject)
		c.Set("tenant_code", "itbem")
		require.NoError(t, delivery.CreateRequestDecomposition(c))
		require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
		var created models.DeliveryDecomposition
		require.NoError(t, db.Where("request_id = ?", request.ID).Order("version DESC").First(&created).Error)
		return created, recorder
	}

	apply := func(decompositionID uuid.UUID) *httptest.ResponseRecorder {
		t.Helper()
		body := bytes.NewBufferString(`{"comment":"Aprobación local verificable"}`)
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/api/automation/projects/"+project.ID.String()+"/requests/"+request.ID.String()+"/decompositions/"+decompositionID.String()+"/apply", body)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		recorder := httptest.NewRecorder()
		c := e.NewContext(req, recorder)
		c.SetPath("/api/automation/projects/:id/requests/:requestID/decompositions/:decompositionID/apply")
		c.SetParamNames("id", "requestID", "decompositionID")
		c.SetParamValues(project.ID.String(), request.ID.String(), decompositionID.String())
		c.Set("cognito_sub", subject)
		c.Set("tenant_code", "itbem")
		require.NoError(t, delivery.ApplyRequestDecomposition(c))
		return recorder
	}

	first, _ := create()
	// Changing a ready source invalidates the frozen proposal. The handler must
	// reject before creating even one child or snapshot.
	require.NoError(t, db.Model(&source).Update("revision", "rev-2").Error)
	stale := apply(first.ID)
	require.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
	var childCount int64
	require.NoError(t, db.Model(&models.DeliveryWorkItem{}).Where("request_id = ?", request.ID).Count(&childCount).Error)
	require.Zero(t, childCount, "stale context must not materialise partial work")
	require.NoError(t, db.Model(&models.DeliveryDecomposition{}).Where("id = ?", first.ID).Select("status").Scan(&first).Error)
	require.Equal(t, "proposed", first.Status)

	second, _ := create()
	result := apply(second.ID)
	require.Equal(t, http.StatusCreated, result.Code, result.Body.String())
	var applied models.DeliveryDecomposition
	require.NoError(t, db.First(&applied, second.ID).Error)
	require.Equal(t, "applied", applied.Status)
	var persistedRequest models.DeliveryRequest
	require.NoError(t, db.First(&persistedRequest, request.ID).Error)
	require.Equal(t, "planned", persistedRequest.Status)

	var children []models.DeliveryWorkItem
	require.NoError(t, db.Where("request_id = ?", request.ID).Order("created_at ASC").Find(&children).Error)
	require.Len(t, children, 4)
	byTitle := map[string]models.DeliveryWorkItem{}
	for _, child := range children {
		byTitle[child.Title] = child
		require.Equal(t, deliveryworkflow.StatePlanning, child.State)
		var snapshots []models.DeliveryContextSnapshot
		require.NoError(t, db.Where("work_item_id = ?", child.ID).Find(&snapshots).Error)
		require.Len(t, snapshots, 1)
		require.Equal(t, "workspace://eventiapp", snapshots[0].Reference)
	}
	var links []models.DeliveryWorkItemDependency
	require.NoError(t, db.Where("work_item_id IN ?", []uuid.UUID{children[0].ID, children[1].ID, children[2].ID, children[3].ID}).Find(&links).Error)
	require.Len(t, links, 2)
	require.Equal(t, byTitle["Preparar base"].ID, links[0].DependsOnWorkItemID)
	require.Equal(t, byTitle["Implementar cambio"].ID, links[1].DependsOnWorkItemID)

	// The scheduler admits independent planning work without waiting for a
	// human to click each child, but keeps the downstream node pending until its
	// prerequisite is released. This creates durable intent only; provider
	// admission remains the continuation dispatcher's responsibility.
	require.NoError(t, delivery.ScheduleReadyDecompositionPlansOnce(db))
	var planIntents []models.DeliveryContinuation
	require.NoError(t, db.Where("work_item_id IN ? AND phase = ? AND status = ?", []uuid.UUID{children[0].ID, children[1].ID, children[2].ID}, "plan", "pending").Find(&planIntents).Error)
	require.Len(t, planIntents, 2)
	initialIntentIDs := map[uuid.UUID]bool{}
	for _, intent := range planIntents {
		initialIntentIDs[intent.WorkItemID] = true
	}
	require.True(t, initialIntentIDs[byTitle["Preparar base"].ID])
	require.True(t, initialIntentIDs[byTitle["Validar pista paralela"].ID])
	require.NoError(t, db.Model(&models.DeliveryWorkItem{}).Where("id = ?", byTitle["Preparar base"].ID).Update("state", deliveryworkflow.StateBlocked).Error)
	require.NoError(t, delivery.ScheduleReadyDecompositionPlansOnce(db))
	var blockedImplementation models.DeliveryWorkItem
	require.NoError(t, db.First(&blockedImplementation, byTitle["Implementar cambio"].ID).Error)
	require.Equal(t, "blocked", blockedImplementation.AgentProgress)
	require.Contains(t, blockedImplementation.BlockedReason, "dependencia")
	require.NoError(t, db.Model(&models.DeliveryWorkItem{}).Where("id = ?", byTitle["Preparar base"].ID).Updates(map[string]any{"state": deliveryworkflow.StatePlanning, "agent_progress": "", "blocked_reason": ""}).Error)
	// The remainder of this test exercises the explicit dependency gate. Mark
	// the foundation scheduler instruction superseded so the direct transition
	// represents an operator retry rather than a duplicate automatic admission.
	require.NoError(t, db.Model(&models.DeliveryContinuation{}).Where("work_item_id = ? AND phase = ?", byTitle["Preparar base"].ID, "plan").Update("status", "superseded").Error)

	// A downstream plan may be drafted, but its first review gate cannot open
	// before the prerequisite is released. This is the execution-side guard
	// that turns the persisted DAG into an operational dependency, rather than
	// merely a visual graph.
	implementation := byTitle["Implementar cambio"]
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: implementation.ID, Version: 1, Status: "proposed", Summary: "Plan downstream", StructuredJSON: `{}`}
	require.NoError(t, db.Create(&plan).Error)
	now := time.Now().UTC()
	agentTask := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &implementation.ID, RequestedBy: subject, CorrelationID: implementation.ID.String(), Operation: "delivery.plan", MaxCompletionTokens: 256, InputRef: "s3://local/decomposition/input", OutputRef: "s3://local/decomposition/result", Provider: "fake", Model: "local-test", Status: "completed", CompletedAt: &now, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&agentTask).Error)
	gateBody, err := json.Marshal(map[string]any{"action": string(deliveryworkflow.ActionSubmitPlan), "comment": "Plan downstream listo", "evidence_checklist": []string{"plan estructurado"}})
	require.NoError(t, err)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+implementation.ID.String()+"/transitions", bytes.NewReader(gateBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	c := e.NewContext(req, recorder)
	c.SetPath("/api/automation/work-items/:id/transitions")
	c.SetParamNames("id")
	c.SetParamValues(implementation.ID.String())
	c.Set("cognito_sub", subject)
	c.Set("tenant_code", "itbem")
	require.NoError(t, delivery.TransitionWorkItem(c))
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "dependencies")
	var unchanged models.DeliveryWorkItem
	require.NoError(t, db.First(&unchanged, implementation.ID).Error)
	require.Equal(t, deliveryworkflow.StatePlanning, unchanged.State)
	// Remove the synthetic downstream plan/run used for the rejected gate, then
	// release the prerequisite. The next scheduler tick may now admit exactly
	// that downstream plan and still must not duplicate it.
	require.NoError(t, db.Unscoped().Delete(&agentTask).Error)
	require.NoError(t, db.Unscoped().Delete(&plan).Error)
	require.NoError(t, db.Model(&models.DeliveryWorkItem{}).Where("id = ?", byTitle["Preparar base"].ID).Update("state", deliveryworkflow.StateReleased).Error)
	require.NoError(t, delivery.ScheduleReadyDecompositionPlansOnce(db))
	var downstreamIntent models.DeliveryContinuation
	require.NoError(t, db.Where("work_item_id = ? AND phase = ? AND status = ?", implementation.ID, "plan", "pending").First(&downstreamIntent).Error)
	var unblockedImplementation models.DeliveryWorkItem
	require.NoError(t, db.First(&unblockedImplementation, implementation.ID).Error)
	require.Empty(t, unblockedImplementation.BlockedReason)
	require.NoError(t, delivery.ScheduleReadyDecompositionPlansOnce(db))
	var downstreamIntentCount int64
	require.NoError(t, db.Model(&models.DeliveryContinuation{}).Where("work_item_id = ? AND phase = ? AND status = ?", implementation.ID, "plan", "pending").Count(&downstreamIntentCount).Error)
	require.Equal(t, int64(1), downstreamIntentCount)
	var parallelIntentCount int64
	require.NoError(t, db.Model(&models.DeliveryContinuation{}).Where("work_item_id = ? AND phase = ? AND status = ?", byTitle["Validar pista paralela"].ID, "plan", "pending").Count(&parallelIntentCount).Error)
	require.Equal(t, int64(1), parallelIntentCount, "the independent root must remain admitted exactly once")

	// Two already-proposed roots that target the same frozen repository/file are
	// a real collaboration conflict, not a reason to guess an ordering. The
	// scheduler blocks both owners, invalidates pending plan intents, and can
	// later clear the block when an operator narrows one plan's file scope.
	require.NoError(t, db.Model(&models.DeliveryWorkItem{}).Where("id = ?", byTitle["Preparar base"].ID).Updates(map[string]any{
		"state": deliveryworkflow.StatePlanning, "plan_json": `{"files_impacted":["src/shared.ts"],"repository_impact":[{"reference":"workspace://eventiapp","impact":"changes"}]}`,
	}).Error)
	require.NoError(t, db.Model(&models.DeliveryWorkItem{}).Where("id = ?", byTitle["Validar pista paralela"].ID).Updates(map[string]any{
		"plan_json": `{"files_impacted":["src/shared.ts"],"repository_impact":[{"reference":"workspace://eventiapp","impact":"changes"}]}`,
	}).Error)
	for _, root := range []models.DeliveryWorkItem{byTitle["Preparar base"], byTitle["Validar pista paralela"]} {
		rootPlan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: root.ID, Version: 1, Status: "proposed", Summary: "Conflict fixture", StructuredJSON: `{"files_impacted":["src/shared.ts"],"repository_impact":[{"reference":"workspace://eventiapp","impact":"changes"}]}`}
		require.NoError(t, db.Create(&rootPlan).Error)
	}
	require.NoError(t, delivery.ScheduleReadyDecompositionPlansOnce(db))
	var conflictedRoots []models.DeliveryWorkItem
	require.NoError(t, db.Where("id IN ?", []uuid.UUID{byTitle["Preparar base"].ID, byTitle["Validar pista paralela"].ID}).Order("created_at ASC").Find(&conflictedRoots).Error)
	require.Len(t, conflictedRoots, 2)
	for _, root := range conflictedRoots {
		require.Equal(t, "blocked", root.AgentProgress)
		require.Contains(t, root.BlockedReason, "Conflicto de cambios")
	}
	var supersededRootPlans int64
	require.NoError(t, db.Model(&models.DeliveryContinuation{}).Where("work_item_id IN ? AND phase = ? AND status = ?", []uuid.UUID{byTitle["Preparar base"].ID, byTitle["Validar pista paralela"].ID}, "plan", "pending").Count(&supersededRootPlans).Error)
	require.Zero(t, supersededRootPlans, "a conflict must not leave a plan intent executable")
	require.NoError(t, db.Model(&models.DeliveryWorkItem{}).Where("id = ?", byTitle["Validar pista paralela"].ID).Update("plan_json", `{"files_impacted":["src/independent.ts"],"repository_impact":[{"reference":"workspace://eventiapp","impact":"changes"}]}`).Error)
	require.NoError(t, delivery.ScheduleReadyDecompositionPlansOnce(db))
	var resolvedRoot models.DeliveryWorkItem
	require.NoError(t, db.First(&resolvedRoot, byTitle["Validar pista paralela"].ID).Error)
	require.Empty(t, resolvedRoot.BlockedReason, "narrowing the plan scope should clear the recoverable conflict")
	require.NotEqual(t, "blocked", resolvedRoot.AgentProgress)
}
