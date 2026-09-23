//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	delivery "events-stocks/controllers/delivery"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestDeliveryMessageConcurrentIdempotentAttachment(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db)

	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Integration customer " + suffix, Code: "INTEGRATION_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Delivery customer " + suffix, Code: "delivery-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Concurrent messages " + suffix, Slug: "concurrent-messages-" + suffix, Summary: "integration", Status: "active", CreatedBy: "integration-root"}
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: "integration-root", Title: "Concurrent context", Description: "Validate message identity", ExpectedOutcome: "One durable message", State: "implementation"}
	sourceID := uuid.Must(uuid.NewV4())
	snapshot := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, SourceID: sourceID, Kind: "repository", Name: "Fixture", Reference: "workspace://fixture", Revision: "rev-1", MetadataJSON: `{}`, CapturedAt: time.Now().UTC()}
	for _, value := range []any{&clientType, &client, &project, &item, &snapshot} {
		require.NoError(t, db.Create(value).Error)
	}
	t.Cleanup(func() {
		db.Unscoped().Where("work_item_id = ?", item.ID).Delete(&models.DeliveryMessage{})
		db.Unscoped().Delete(&models.DeliveryContextSnapshot{}, snapshot.ID)
		db.Unscoped().Delete(&models.DeliveryWorkItem{}, item.ID)
		db.Unscoped().Delete(&models.DeliveryProject{}, project.ID)
		db.Unscoped().Delete(&models.Client{}, client.ID)
		db.Unscoped().Delete(&models.ClientType{}, clientType.ID)
	})

	const subject = "integration-delivery-root"
	restoreHooks := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			return nil, requireAuthUserError{}
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsActive: true, RootLevel: models.RootLevelOperational}, nil
	}})
	t.Cleanup(restoreHooks)

	messageID := uuid.Must(uuid.NewV4()).String()
	payload := map[string]any{
		"client_message_id": messageID,
		"phase":             "implementation",
		"body":              "Preserva la evidencia congelada y no cambies el alcance.",
		"attachments":       []map[string]string{{"kind": "context", "id": snapshot.ID.String()}},
	}
	const callers = 16
	type result struct {
		status int
		err    error
	}
	responses := make(chan result, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			encoded, err := json.Marshal(payload)
			if err != nil {
				responses <- result{err: err}
				return
			}
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/api/automation/work-items/"+item.ID.String()+"/messages", bytes.NewReader(encoded))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			recorder := httptest.NewRecorder()
			ctx := e.NewContext(req, recorder)
			ctx.SetPath("/api/automation/work-items/:id/messages")
			ctx.SetParamNames("id")
			ctx.SetParamValues(item.ID.String())
			ctx.Set("cognito_sub", subject)
			ctx.Set("tenant_code", "itbem")
			err = delivery.CreateMessage(ctx)
			if err != nil {
				responses <- result{status: http.StatusInternalServerError, err: err}
				return
			}
			responses <- result{status: recorder.Code}
		}()
	}
	wait.Wait()
	close(responses)
	for response := range responses {
		require.NoError(t, response.err)
		if response.status != http.StatusCreated {
			t.Fatalf("concurrent idempotent request returned status %d", response.status)
		}
	}
	var messages []models.DeliveryMessage
	require.NoError(t, db.Where("work_item_id = ?", item.ID).Find(&messages).Error)
	require.Len(t, messages, 1, "the same client identity must produce one durable message")
	var attachments []models.DeliveryMessageAttachment
	require.NoError(t, json.Unmarshal([]byte(messages[0].AttachmentsJSON), &attachments))
	require.Len(t, attachments, 1)
	require.Equal(t, snapshot.ID.String(), attachments[0].ID)

	// Persist one completed chat inference and verify the real work-item API
	// projects it separately from the phase totals consumed by the dashboard.
	taskID := uuid.Must(uuid.NewV4())
	completedAt := time.Now().UTC()
	task := models.AutomationTask{ID: taskID, JobID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &item.ID, RequestedBy: subject, CorrelationID: item.ID.String(), Operation: "delivery.chat", MaxCompletionTokens: 512, InputRef: "s3://local/input", OutputRef: "s3://local/output", Status: "completed", Provider: "minimax", Model: "MiniMax-M3", CompletedAt: &completedAt}
	require.NoError(t, db.Create(&task).Error)
	execution := models.AutomationExecution{ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, DeliveryWorkItemID: &item.ID, RunID: uuid.Must(uuid.NewV4()).String(), StepKey: "chat", Provider: "minimax", Model: "MiniMax-M3", InputTokens: 120, OutputTokens: 40, TotalTokens: 160, InputCostMicros: 12, OutputCostMicros: 8, TotalCostMicros: 20, PricingBasis: "integration", RequestRef: "s3://local/request", ResponseRef: "s3://local/response", CompletedAt: time.Now().UTC()}
	require.NoError(t, db.Create(&execution).Error)
	t.Cleanup(func() {
		db.Unscoped().Delete(&models.AutomationExecution{}, execution.ID)
		db.Unscoped().Delete(&models.AutomationTask{}, task.ID)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/automation/work-items/"+item.ID.String(), nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(req, recorder)
	ctx.SetPath("/api/automation/work-items/:id")
	ctx.SetParamNames("id")
	ctx.SetParamValues(item.ID.String())
	ctx.Set("cognito_sub", subject)
	ctx.Set("tenant_code", "itbem")
	require.NoError(t, delivery.GetWorkItem(ctx))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	data, ok := response["data"].(map[string]any)
	require.True(t, ok)
	costSummary, ok := data["cost_summary"].(map[string]any)
	require.True(t, ok)
	conversation, ok := costSummary["conversation"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(1), conversation["executions"])
	require.Equal(t, float64(160), conversation["total_tokens"])
	require.Equal(t, float64(20), conversation["total_cost_microusd"])
}

type requireAuthUserError struct{}

func (requireAuthUserError) Error() string { return "unknown integration subject" }
