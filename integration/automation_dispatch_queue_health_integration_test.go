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

// TestAutomationDispatchQueueHealthCountersUseAuthorizedFilteredSnapshot
// verifies the queue's blocked and expired-lease summary is derived from the
// authorized project's persisted assignment/step rows and honors filters.
func TestAutomationDispatchQueueHealthCountersUseAuthorizedFilteredSnapshot(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	subject := "dispatch-health-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	rollback := errors.New("rollback dispatch health fixture")

	clientType := models.ClientType{
		ID: uuid.Must(uuid.NewV4()), Name: "Dispatch health type " + suffix,
		Code: "DISPATCHH_" + strings.ToUpper(suffix), Level: 10, IsActive: true,
	}
	organization := models.Client{
		ID: uuid.Must(uuid.NewV4()), Name: "Dispatch health organization " + suffix,
		Code: "dispatch-health-org-" + suffix, ClientTypeID: clientType.ID, IsActive: true,
	}
	selectedProject := models.DeliveryProject{
		ID: uuid.Must(uuid.NewV4()), ClientID: organization.ID,
		Name: "Selected dispatch health project " + suffix, Slug: "dispatch-health-selected-" + suffix,
		Status: "active", CreatedBy: subject,
	}
	siblingProject := models.DeliveryProject{
		ID: uuid.Must(uuid.NewV4()), ClientID: organization.ID,
		Name: "Sibling dispatch health project " + suffix, Slug: "dispatch-health-sibling-" + suffix,
		Status: "active", CreatedBy: subject,
	}
	member := models.DeliveryProjectMember{
		ID: uuid.Must(uuid.NewV4()), ProjectID: selectedProject.ID, CognitoSub: subject,
		Role: "viewer", Permissions: `[]`, CreatedBy: subject, CreatedAt: now, UpdatedAt: now,
	}

	var allResults [][]byte
	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(cognitoSub string) (*models.User, error) {
		if cognitoSub != subject {
			return nil, fmt.Errorf("unexpected Cognito subject %q", cognitoSub)
		}
		return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsRoot: false, RootLevel: models.RootLevelNone, IsActive: true}, nil
	}})
	t.Cleanup(restoreAuth)

	err := db.Transaction(func(tx *gorm.DB) error {
		previousDB := configuration.DB
		configuration.DB = tx
		defer func() { configuration.DB = previousDB }()

		for _, value := range []any{&clientType, &organization, &selectedProject, &siblingProject, &member} {
			if err := tx.Create(value).Error; err != nil {
				return err
			}
		}

		blockedID, _, err := seedAutomationDispatchQueueScopeAssignment(tx, selectedProject, subject, suffix, "blocked", now, false)
		if err != nil {
			return err
		}
		if err := setDispatchHealthAssignmentState(tx, blockedID, models.DeliveryPlanStepAssignmentBlocked, models.DeliveryPlanStepBlocked, nil); err != nil {
			return err
		}

		expiredAt := now.Add(-time.Minute)
		expiredID, _, err := seedAutomationDispatchQueueScopeAssignment(tx, selectedProject, subject, suffix, "expired", now, false)
		if err != nil {
			return err
		}
		if err := setDispatchHealthAssignmentState(tx, expiredID, models.DeliveryPlanStepAssignmentRunning, models.DeliveryPlanStepRunning, &expiredAt); err != nil {
			return err
		}

		outsideID, _, err := seedAutomationDispatchQueueScopeAssignment(tx, siblingProject, subject, suffix, "sibling-blocked", now, false)
		if err != nil {
			return err
		}
		if err := setDispatchHealthAssignmentState(tx, outsideID, models.DeliveryPlanStepAssignmentBlocked, models.DeliveryPlanStepBlocked, nil); err != nil {
			return err
		}

		requestQueue := func(query string) []byte {
			request := httptest.NewRequest(http.MethodGet, "/api/automation/dispatch/queue?project_id="+selectedProject.ID.String()+query, nil)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			ctx.Set("cognito_sub", subject)
			ctx.Set("workspace_mode", "organization")
			ctx.Set("organization_id", organization.ID)
			if err := automation.GetDispatchQueue(ctx); err != nil {
				return append([]byte(nil), recorder.Body.Bytes()...)
			}
			return append([]byte(nil), recorder.Body.Bytes()...)
		}
		allResults = append(allResults,
			requestQueue(""),
			requestQueue("&status=blocked"),
			requestQueue("&agent_key=qa"),
		)
		return rollback
	})
	require.ErrorIs(t, err, rollback, "all dispatch health fixtures must roll back")
	require.Len(t, allResults, 3)

	type response struct {
		Data struct {
			BlockedAssignments    int64 `json:"blocked_assignments"`
			ExpiredPlanStepLeases int64 `json:"expired_plan_step_leases"`
			Items                 []any `json:"items"`
		} `json:"data"`
	}
	decode := func(index int) response {
		var value response
		require.NoError(t, json.Unmarshal(allResults[index], &value), string(allResults[index]))
		return value
	}

	all := decode(0)
	require.EqualValues(t, 1, all.Data.BlockedAssignments, "sibling project blocked rows must not contribute")
	require.EqualValues(t, 1, all.Data.ExpiredPlanStepLeases, "count only expired running step leases for this project")
	require.Len(t, all.Data.Items, 2)

	blocked := decode(1)
	require.EqualValues(t, 1, blocked.Data.BlockedAssignments)
	require.Zero(t, blocked.Data.ExpiredPlanStepLeases, "status filter must apply to both counters")
	require.Len(t, blocked.Data.Items, 1)

	otherAgent := decode(2)
	require.Zero(t, otherAgent.Data.BlockedAssignments)
	require.Zero(t, otherAgent.Data.ExpiredPlanStepLeases)
	require.Empty(t, otherAgent.Data.Items)
}

func setDispatchHealthAssignmentState(tx *gorm.DB, assignmentID uuid.UUID, assignmentStatus, stepStatus string, leaseExpiry *time.Time) error {
	var assignment models.DeliveryPlanStepAssignment
	if err := tx.Select("id", "delivery_plan_step_id").Take(&assignment, "id = ?", assignmentID).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ?", assignmentID).Update("status", assignmentStatus).Error; err != nil {
		return err
	}
	updates := map[string]any{"status": stepStatus, "lease_expires_at": leaseExpiry}
	if leaseExpiry != nil {
		updates["lease_fence"] = int64(1)
		updates["run_id"] = "expired-dispatch-health-run"
		updates["agent_key"] = "generalist"
	}
	return tx.Model(&models.DeliveryPlanStep{}).Where("id = ?", assignment.DeliveryPlanStepID).Updates(updates).Error
}
