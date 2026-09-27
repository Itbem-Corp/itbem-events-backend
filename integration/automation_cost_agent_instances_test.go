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

type automationCostAgentInstancesIntegrationItem struct {
	AgentKey           string     `json:"agent_key"`
	AgentInstanceID    *uuid.UUID `json:"agent_instance_id"`
	InstanceAttributed bool       `json:"instance_attributed"`
	Executions         int64      `json:"executions"`
	TotalTokens        int64      `json:"total_tokens"`
	TotalCostMicros    int64      `json:"total_cost_microusd"`
}

type automationCostAgentInstancesIntegrationPage struct {
	RangeDays  int                                           `json:"range_days"`
	Items      []automationCostAgentInstancesIntegrationItem `json:"items"`
	Limit      int                                           `json:"limit"`
	HasMore    bool                                          `json:"has_more"`
	NextCursor string                                        `json:"next_cursor"`
}

func TestAutomationCostByAgentInstanceScopesAndPagesWithoutLeaks(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	actor := "agent-instance-cost-actor-" + suffix
	otherActor := "agent-instance-cost-other-" + suffix
	stranger := "agent-instance-cost-stranger-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	rollback := errors.New("rollback agent-instance cost fixture")

	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: cognitoSub, IsRoot: false, RootLevel: models.RootLevelNone, IsActive: true}, nil
	}})
	t.Cleanup(restoreAuth)

	err := db.Transaction(func(tx *gorm.DB) error {
		clientType := models.ClientType{
			ID: uuid.Must(uuid.NewV4()), Name: "Cost integration " + suffix,
			Code: "COST_AGENT_INSTANCE_" + strings.ToUpper(suffix), Level: 10, IsActive: true,
		}
		orgA := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Cost organization A " + suffix, Code: "cost-org-a-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
		orgB := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Cost organization B " + suffix, Code: "cost-org-b-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
		projectA := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: orgA.ID, Name: "Cost project A " + suffix, Slug: "cost-project-a-" + suffix, Status: "active", CreatedBy: otherActor}
		projectAHidden := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: orgA.ID, Name: "Cost hidden project " + suffix, Slug: "cost-hidden-project-" + suffix, Status: "active", CreatedBy: otherActor}
		projectB := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: orgB.ID, Name: "Cost project B " + suffix, Slug: "cost-project-b-" + suffix, Status: "active", CreatedBy: otherActor}
		workItemA := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: projectA.ID, RequestedBy: otherActor, Title: "Cost work item A " + suffix, ExpectedOutcome: "fixture", State: "implementation"}
		workItemAHidden := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: projectAHidden.ID, RequestedBy: otherActor, Title: "Cost hidden work item " + suffix, ExpectedOutcome: "fixture", State: "implementation"}
		workItemB := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: projectB.ID, RequestedBy: otherActor, Title: "Cost work item B " + suffix, ExpectedOutcome: "fixture", State: "implementation"}
		for _, value := range []any{&clientType, &orgA, &orgB, &projectA, &projectAHidden, &projectB, &workItemA, &workItemAHidden, &workItemB} {
			if err := tx.Create(value).Error; err != nil {
				return err
			}
		}

		// The actor can read project A and project B, but the active organization
		// must still hide project B. A same-organization project without membership
		// must also remain hidden from this non-owner.
		for _, project := range []models.DeliveryProject{projectA, projectB} {
			membership := models.DeliveryProjectMember{
				ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, CognitoSub: actor,
				Role: "viewer", Permissions: `[]`, CreatedBy: actor, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&membership).Error; err != nil {
				return err
			}
		}

		instanceOne, instanceTwo := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		promptSentinel := "PROMPT_SENTINEL_" + suffix
		requestSentinel := "REQUEST_REF_SENTINEL_" + suffix
		responseSentinel := "RESPONSE_REF_SENTINEL_" + suffix
		createExecution := func(item *models.DeliveryWorkItem, requestedBy, agentKey string, instanceID *uuid.UUID, costMicros, totalTokens int64, label string) error {
			completedAt := now.Add(-time.Duration(len(label)+1) * time.Minute)
			taskID := uuid.Must(uuid.NewV4())
			runID := uuid.Must(uuid.NewV4()).String()
			task := models.AutomationTask{
				ID: taskID, JobID: uuid.Must(uuid.NewV4()), RequestedBy: requestedBy,
				DeliveryWorkItemID: &item.ID, CorrelationID: item.ID.String(), Operation: "delivery.implementation",
				InputRef: "s3://integration-private/" + promptSentinel, OutputRef: "s3://integration-private/" + responseSentinel,
				Provider: "openrouter", Model: "cost-test-model", Status: "completed", RunID: runID,
				CompletedAt: &completedAt, CreatedAt: completedAt.Add(-time.Minute), UpdatedAt: completedAt,
			}
			if err := tx.Create(&task).Error; err != nil {
				return err
			}
			execution := models.AutomationExecution{
				ID: uuid.Must(uuid.NewV4()), AutomationTaskID: task.ID, DeliveryWorkItemID: &item.ID,
				RunID: runID, WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: agentKey,
				MachineID: uuid.Must(uuid.NewV4()).String(), AgentInstanceID: instanceID,
				StepKey: "implementation", Provider: "openrouter", Model: "cost-test-model",
				InputTokens: totalTokens / 2, OutputTokens: totalTokens - totalTokens/2, TotalTokens: totalTokens,
				TotalCostMicros: costMicros, Currency: "USD", PricingBasis: "integration-fixture",
				RequestRef:  "s3://integration-private/" + requestSentinel,
				ResponseRef: "s3://integration-private/" + responseSentinel,
				CompletedAt: completedAt, CreatedAt: completedAt,
			}
			return tx.Create(&execution).Error
		}

		// One logical agent has two separately attributed runtime instances plus
		// an older nullable identity bucket. The first instance has two ledger rows
		// to prove the aggregate sums count, tokens, and integer micro-USD exactly.
		for _, fixture := range []struct {
			instance *uuid.UUID
			cost     int64
			tokens   int64
			label    string
		}{
			{&instanceOne, 200, 20, "instance-one-a"},
			{&instanceOne, 300, 30, "instance-one-b"},
			{&instanceTwo, 300, 30, "instance-two"},
			{nil, 100, 10, "legacy-null"},
		} {
			if err := createExecution(&workItemA, otherActor, "generalist", fixture.instance, fixture.cost, fixture.tokens, fixture.label); err != nil {
				return err
			}
		}
		// These high-cost rows would sort ahead of the authorized buckets if
		// organization or project/actor scope were accidentally dropped.
		foreignInstance, hiddenInstance := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		if err := createExecution(&workItemB, otherActor, "generalist", &foreignInstance, 5_000, 500, "foreign-org-control"); err != nil {
			return err
		}
		if err := createExecution(&workItemAHidden, otherActor, "generalist", &hiddenInstance, 4_000, 400, "non-member-project-control"); err != nil {
			return err
		}

		previousDB := configuration.DB
		configuration.DB = tx
		defer func() { configuration.DB = previousDB }()

		getPage := func(requestActor string, organizationID uuid.UUID, query url.Values) (int, automationCostAgentInstancesIntegrationPage, string) {
			request := httptest.NewRequest(http.MethodGet, "/api/automation/costs/agent-instances?"+query.Encode(), nil)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.Set("cognito_sub", requestActor)
			ctx.Set("workspace_mode", "organization")
			ctx.Set("organization_id", organizationID)
			if err := automation.CostAgentInstances(ctx); err != nil {
				t.Fatalf("CostAgentInstances returned an error: %v", err)
			}
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			page := automationCostAgentInstancesIntegrationPage{}
			if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
				require.NoError(t, json.Unmarshal(envelope.Data, &page), "cost response data: %s", recorder.Body.String())
			}
			return recorder.Code, page, recorder.Body.String()
		}

		baseQuery := url.Values{"days": {"30"}, "agent_instance_limit": {"1"}}
		firstStatus, firstPage, firstBody := getPage(actor, orgA.ID, baseQuery)
		require.Equal(t, http.StatusOK, firstStatus, firstBody)
		require.Equal(t, 30, firstPage.RangeDays)
		require.Equal(t, 1, firstPage.Limit)
		require.True(t, firstPage.HasMore)
		require.Len(t, firstPage.Items, 1)
		require.NotEmpty(t, firstPage.NextCursor)

		seen := make(map[string]automationCostAgentInstancesIntegrationItem)
		ordered := make([]automationCostAgentInstancesIntegrationItem, 0, 3)
		currentPage, currentBody := firstPage, firstBody
		for pageNumber := 1; pageNumber <= 4; pageNumber++ {
			forbidden := []string{promptSentinel, requestSentinel, responseSentinel, "request_ref", "response_ref", "input_ref", "output_ref"}
			for _, value := range forbidden {
				require.NotContains(t, strings.ToLower(currentBody), strings.ToLower(value), "sensitive reference appeared on page %d", pageNumber)
			}
			for _, item := range currentPage.Items {
				instanceKey := "legacy-null"
				if item.AgentInstanceID != nil {
					instanceKey = item.AgentInstanceID.String()
				}
				key := item.AgentKey + "|" + instanceKey
				require.NotContains(t, seen, key, "cursor pagination repeated bucket %q", key)
				seen[key] = item
				ordered = append(ordered, item)
			}
			if !currentPage.HasMore {
				require.Empty(t, currentPage.NextCursor)
				break
			}
			require.NotEmpty(t, currentPage.NextCursor)
			require.Less(t, pageNumber, 4, "pagination did not terminate")
			query := url.Values{"days": {"30"}, "agent_instance_limit": {"1"}, "agent_instance_cursor": {currentPage.NextCursor}}
			status, nextPage, body := getPage(actor, orgA.ID, query)
			require.Equal(t, http.StatusOK, status, body)
			require.Equal(t, 1, nextPage.Limit)
			currentPage, currentBody = nextPage, body
		}

		require.Len(t, ordered, 3, "authorized organization should expose exactly the two instances and one legacy bucket")
		require.Equal(t, int64(500), ordered[0].TotalCostMicros)
		require.Equal(t, int64(300), ordered[1].TotalCostMicros)
		require.Equal(t, int64(100), ordered[2].TotalCostMicros)
		instanceOneKey, instanceTwoKey := "generalist|"+instanceOne.String(), "generalist|"+instanceTwo.String()
		firstBucket, hasFirst := seen[instanceOneKey]
		secondBucket, hasSecond := seen[instanceTwoKey]
		legacyBucket, hasLegacy := seen["generalist|legacy-null"]
		require.True(t, hasFirst, "first registered instance bucket was omitted")
		require.True(t, hasSecond, "second registered instance bucket was omitted")
		require.True(t, hasLegacy, "legacy null-instance bucket was omitted")
		require.Equal(t, int64(2), firstBucket.Executions)
		require.Equal(t, int64(50), firstBucket.TotalTokens)
		require.Equal(t, int64(500), firstBucket.TotalCostMicros)
		require.True(t, firstBucket.InstanceAttributed)
		require.Equal(t, int64(1), secondBucket.Executions)
		require.Equal(t, int64(30), secondBucket.TotalTokens)
		require.Equal(t, int64(300), secondBucket.TotalCostMicros)
		require.True(t, secondBucket.InstanceAttributed)
		require.Nil(t, legacyBucket.AgentInstanceID)
		require.False(t, legacyBucket.InstanceAttributed)
		require.Equal(t, int64(1), legacyBucket.Executions)
		require.Equal(t, int64(10), legacyBucket.TotalTokens)
		require.Equal(t, int64(100), legacyBucket.TotalCostMicros)

		// The other organization has a real, authorized high-cost control row,
		// but it must not appear in organization A even when explicitly filtered.
		orgBStatus, orgBPage, orgBBody := getPage(actor, orgB.ID, baseQuery)
		require.Equal(t, http.StatusOK, orgBStatus, orgBBody)
		require.Len(t, orgBPage.Items, 1)
		require.Equal(t, int64(5_000), orgBPage.Items[0].TotalCostMicros)
		filteredForeignOrg := url.Values{
			"days": {"30"}, "agent_instance_limit": {"1"}, "project_id": {projectB.ID.String()},
		}
		foreignStatus, foreignPage, foreignBody := getPage(actor, orgA.ID, filteredForeignOrg)
		require.Equal(t, http.StatusOK, foreignStatus, foreignBody)
		require.Empty(t, foreignPage.Items, "active organization must exclude another organization's project")

		// Same organization is not sufficient: the actor also needs project
		// membership or task ownership. This project has neither for this actor.
		filteredHiddenProject := url.Values{
			"days": {"30"}, "agent_instance_limit": {"1"}, "project_id": {projectAHidden.ID.String()},
		}
		hiddenStatus, hiddenPage, hiddenBody := getPage(actor, orgA.ID, filteredHiddenProject)
		require.Equal(t, http.StatusOK, hiddenStatus, hiddenBody)
		require.Empty(t, hiddenPage.Items, "unreadable project in the same organization must be excluded")

		// A different actor with no membership and no owned tasks sees no rows,
		// even when requesting the otherwise readable project's ID.
		actorStatus, actorPage, actorBody := getPage(stranger, orgA.ID, url.Values{
			"days": {"30"}, "agent_instance_limit": {"1"}, "project_id": {projectA.ID.String()},
		})
		require.Equal(t, http.StatusOK, actorStatus, actorBody)
		require.Empty(t, actorPage.Items, "project data must remain actor-scoped")

		return rollback
	})
	require.ErrorIs(t, err, rollback, "the disposable agent-instance cost fixture must roll back")
}
