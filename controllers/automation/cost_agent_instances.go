package automation

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/organizationscope"
	"events-stocks/utils"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	defaultAutomationCostAgentInstanceLimit = 25
	maxAutomationCostAgentInstanceLimit     = 100
	maxAutomationCostAgentInstanceCursor    = 2048
)

type automationCostAgentInstanceCursor struct {
	Version         int       `json:"v"`
	SnapshotAt      time.Time `json:"snapshot_at"`
	TotalCostMicros int64     `json:"total_cost_microusd"`
	AgentKey        string    `json:"agent_key"`
	InstanceID      string    `json:"agent_instance_id"`
	ScopeHash       string    `json:"scope_hash"`
}

type automationCostAgentInstanceRow struct {
	AgentKey        string `gorm:"column:agent_key"`
	AgentInstanceID string `gorm:"column:agent_instance_id"`
	Executions      int64  `gorm:"column:executions"`
	TotalTokens     int64  `gorm:"column:total_tokens"`
	TotalCostMicros int64  `gorm:"column:total_cost_microusd"`
}

type automationCostAgentInstance struct {
	AgentKey           string     `json:"agent_key"`
	AgentInstanceID    *uuid.UUID `json:"agent_instance_id"`
	InstanceAttributed bool       `json:"instance_attributed"`
	Executions         int64      `json:"executions"`
	TotalTokens        int64      `json:"total_tokens"`
	TotalCostMicros    int64      `json:"total_cost_microusd"`
}

type automationCostAgentInstancePage struct {
	RangeDays      int                           `json:"range_days"`
	SnapshotAt     time.Time                     `json:"snapshot_at"`
	AppliedFilters map[string]any                `json:"applied_filters"`
	Items          []automationCostAgentInstance `json:"items"`
	Limit          int                           `json:"limit"`
	HasMore        bool                          `json:"has_more"`
	NextCursor     string                        `json:"next_cursor,omitempty"`
}

// CostAgentInstances provides a safely paginated cost breakdown grouped by
// the agent identity and its registered runtime instance. Unattributed legacy
// rows remain visible with a null instance ID and instance_attributed=false.
func CostAgentInstances(c echo.Context) error {
	setAutomationCostNoStoreHeaders(c)
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation costs unavailable", "Database is unavailable")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	costQuery, queryErr := parseAutomationCostQuery(c)
	if queryErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation cost query", queryErr.Error())
	}
	limit, cursor, paginationErr := parseAutomationCostAgentInstancePagination(c)
	if paginationErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent-instance cost query", paginationErr.Error())
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	workspaceMode, _ := c.Get("workspace_mode").(string)
	organizationID, hasOrganizationID := c.Get("organization_id").(uuid.UUID)
	workspaceClientIDs, clientScopeErr := automationCostWorkspaceClientIDs(configuration.DB, workspaceMode, organizationID, hasOrganizationID)
	if clientScopeErr != nil {
		if errors.Is(clientScopeErr, organizationscope.ErrOrganizationNotFound) {
			return utils.Error(c, http.StatusNotFound, "Automation cost workspace not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not resolve automation cost workspace")
	}
	costQuery.WorkspaceClientIDs = workspaceClientIDs
	snapshotAt := costQuery.SnapshotAt
	if cursor != nil {
		if !snapshotAt.IsZero() && !snapshotAt.Equal(cursor.SnapshotAt) {
			return utils.Error(c, http.StatusBadRequest, "Invalid agent-instance cost cursor", "Cursor snapshot does not match the requested snapshot")
		}
		snapshotAt = cursor.SnapshotAt
	}
	if snapshotAt.IsZero() {
		snapshotAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	costQuery.SnapshotAt = snapshotAt
	cursorScope := automationCostAgentInstanceCursorScope(costQuery, limit, workspaceMode, organizationID, hasOrganizationID, user.CognitoSub)
	if cursor != nil && cursor.ScopeHash != cursorScope {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent-instance cost cursor", "Cursor does not match the selected filters or workspace")
	}
	ledgerSource, ledgerCoverage, ledgerErr := automationCostLedgerSource(configuration.DB)
	if ledgerErr != nil {
		return utils.ErrorWithData(c, http.StatusServiceUnavailable, "Automation costs unavailable", "Cost ledger is initializing", map[string]any{"ledger_coverage": ledgerCoverage})
	}
	baseQuery := configuration.DB.Table("(" + ledgerSource + ") AS execution").
		Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id").
		Joins("LEFT JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id").
		Joins("LEFT JOIN delivery_projects AS project ON project.id = work_item.project_id")
	baseQuery = applyAutomationCostAgentInstanceSnapshot(baseQuery, snapshotAt, costQuery)
	baseQuery, scopeErr := applyAutomationCostWorkspaceScope(baseQuery, workspaceMode, organizationID, hasOrganizationID, user.IsPlatformAdmin(), workspaceClientIDs)
	if scopeErr != nil {
		return utils.Error(c, http.StatusNotFound, "Automation cost workspace not found", "")
	}
	if !user.IsPlatformAdmin() {
		projectIDs, membershipErr := deliveryReadableProjectIDs(user.CognitoSub)
		if membershipErr != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not resolve delivery project access")
		}
		baseQuery = applyAutomationCostActorScope(baseQuery, requestedBy, projectIDs, false)
	}
	filteredQuery := applyAutomationCostFilters(baseQuery.Session(&gorm.Session{}), costQuery)
	query := automationCostAgentInstanceAggregateQuery(filteredQuery, cursor, limit+1)
	var rows []automationCostAgentInstanceRow
	if err := query.Scan(&rows).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not read agent-instance cost breakdown")
	}

	page := automationCostAgentInstancePage{
		RangeDays: costQuery.Days, SnapshotAt: snapshotAt,
		AppliedFilters: map[string]any{
			"client_id": costQuery.ClientID, "project_id": costQuery.ProjectID, "epic_id": costQuery.EpicID, "work_item_id": costQuery.WorkItemID,
			"agent_key": costQuery.AgentKey, "agent_instance_id": costQuery.AgentInstanceID,
			"step_key": costQuery.StepKey, "provider": costQuery.Provider, "model": costQuery.Model,
			"from_at": automationCostAppliedTimestamp(costQuery.FromAt), "to_at": automationCostAppliedTimestamp(costQuery.ToAt),
		},
		Items: make([]automationCostAgentInstance, 0, minInt(len(rows), limit)),
		Limit: limit,
	}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for _, row := range rows {
		item := automationCostAgentInstance{
			AgentKey: row.AgentKey, Executions: row.Executions,
			TotalTokens: row.TotalTokens, TotalCostMicros: row.TotalCostMicros,
			InstanceAttributed: row.AgentInstanceID != "",
		}
		if row.AgentInstanceID != "" {
			instanceID, parseErr := uuid.FromString(row.AgentInstanceID)
			if parseErr != nil || instanceID == uuid.Nil {
				return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Cost ledger contains an invalid agent instance identifier")
			}
			item.AgentInstanceID = &instanceID
		}
		page.Items = append(page.Items, item)
	}
	if page.HasMore && len(page.Items) > 0 {
		last := rows[len(rows)-1]
		nextCursor, encodeErr := encodeAutomationCostAgentInstanceCursor(automationCostAgentInstanceCursor{
			SnapshotAt: snapshotAt, TotalCostMicros: last.TotalCostMicros, AgentKey: last.AgentKey,
			InstanceID: last.AgentInstanceID, ScopeHash: cursorScope,
		})
		if encodeErr != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not prepare the next agent-instance page")
		}
		page.NextCursor = nextCursor
	}
	return utils.Success(c, http.StatusOK, "Automation cost by agent instance", page)
}

func parseAutomationCostAgentInstancePagination(c echo.Context) (int, *automationCostAgentInstanceCursor, error) {
	for _, name := range []string{"page", "page_size", "cursor", "work_item_limit", "work_item_cursor"} {
		if len(c.QueryParams()[name]) > 0 {
			return 0, nil, fmt.Errorf("%s is not supported; use agent_instance_limit and agent_instance_cursor", name)
		}
	}
	limitValues := c.QueryParams()["agent_instance_limit"]
	if len(limitValues) > 1 {
		return 0, nil, fmt.Errorf("agent_instance_limit must appear once")
	}
	limit, err := parseAutomationCostPageLimit("", defaultAutomationCostAgentInstanceLimit, maxAutomationCostAgentInstanceLimit)
	if len(limitValues) == 1 {
		limit, err = parseAutomationCostPageLimit(limitValues[0], defaultAutomationCostAgentInstanceLimit, maxAutomationCostAgentInstanceLimit)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("agent_instance_limit must be from 1 to %d", maxAutomationCostAgentInstanceLimit)
	}
	cursorValues := c.QueryParams()["agent_instance_cursor"]
	if len(cursorValues) > 1 {
		return 0, nil, fmt.Errorf("agent_instance_cursor must appear once")
	}
	if len(cursorValues) == 0 || strings.TrimSpace(cursorValues[0]) == "" {
		return limit, nil, nil
	}
	cursor, err := decodeAutomationCostAgentInstanceCursor(strings.TrimSpace(cursorValues[0]))
	if err != nil {
		return 0, nil, fmt.Errorf("agent_instance_cursor is invalid")
	}
	return limit, &cursor, nil
}

func automationCostAgentInstanceCursorScope(filters automationCostQuery, limit int, workspaceMode string, organizationID uuid.UUID, hasOrganizationID bool, actorSub string) string {
	baseScope := automationCostCursorScope(filters, workspaceMode, organizationID, hasOrganizationID, actorSub)
	canonical, _ := json.Marshal([]string{"agent-instance-costs-v2", strconv.Itoa(limit), baseScope})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func encodeAutomationCostAgentInstanceCursor(cursor automationCostAgentInstanceCursor) (string, error) {
	if cursor.SnapshotAt.IsZero() || cursor.SnapshotAt.After(time.Now().UTC().Add(time.Minute)) || cursor.TotalCostMicros < 0 || len(cursor.AgentKey) > 128 || !utf8.ValidString(cursor.AgentKey) {
		return "", fmt.Errorf("agent-instance cost cursor source is invalid")
	}
	if cursor.InstanceID != "" {
		parsed, err := uuid.FromString(cursor.InstanceID)
		if err != nil || parsed == uuid.Nil {
			return "", fmt.Errorf("agent-instance cost cursor identifier is invalid")
		}
		cursor.InstanceID = parsed.String()
	}
	if len(cursor.ScopeHash) != sha256.Size*2 {
		return "", fmt.Errorf("agent-instance cost cursor scope is incomplete")
	}
	if _, err := hex.DecodeString(cursor.ScopeHash); err != nil {
		return "", fmt.Errorf("agent-instance cost cursor scope is invalid")
	}
	cursor.Version = 2
	cursor.SnapshotAt = cursor.SnapshotAt.UTC().Truncate(time.Microsecond)
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeAutomationCostAgentInstanceCursor(raw string) (automationCostAgentInstanceCursor, error) {
	if len(raw) > maxAutomationCostAgentInstanceCursor {
		return automationCostAgentInstanceCursor{}, fmt.Errorf("agent-instance cost cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 {
		return automationCostAgentInstanceCursor{}, fmt.Errorf("agent-instance cost cursor encoding is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	var cursor automationCostAgentInstanceCursor
	if err := decoder.Decode(&cursor); err != nil {
		return automationCostAgentInstanceCursor{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return automationCostAgentInstanceCursor{}, fmt.Errorf("agent-instance cost cursor contains trailing data")
	}
	if cursor.Version != 2 || cursor.SnapshotAt.IsZero() || cursor.SnapshotAt.After(time.Now().UTC().Add(time.Minute)) || cursor.TotalCostMicros < 0 || len(cursor.AgentKey) > 128 || !utf8.ValidString(cursor.AgentKey) || len(cursor.ScopeHash) != sha256.Size*2 {
		return automationCostAgentInstanceCursor{}, fmt.Errorf("agent-instance cost cursor is incomplete")
	}
	cursor.SnapshotAt = cursor.SnapshotAt.UTC().Truncate(time.Microsecond)
	if _, err := hex.DecodeString(cursor.ScopeHash); err != nil {
		return automationCostAgentInstanceCursor{}, fmt.Errorf("agent-instance cost cursor scope is invalid")
	}
	if cursor.InstanceID != "" {
		instanceID, parseErr := uuid.FromString(cursor.InstanceID)
		if parseErr != nil || instanceID == uuid.Nil {
			return automationCostAgentInstanceCursor{}, fmt.Errorf("agent-instance cost cursor identifier is invalid")
		}
		cursor.InstanceID = instanceID.String()
	}
	return cursor, nil
}

func automationCostAgentInstanceAggregateQuery(query *gorm.DB, cursor *automationCostAgentInstanceCursor, limit int) *gorm.DB {
	query = query.Select("execution.agent_key AS agent_key, COALESCE(execution.agent_instance_id::text, '') AS agent_instance_id, COUNT(*) AS executions, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_microusd").
		Group("execution.agent_key, execution.agent_instance_id")
	if cursor != nil {
		query = query.Having(
			"SUM(execution.total_cost_micros) < ? OR (SUM(execution.total_cost_micros) = ? AND (execution.agent_key > ? OR (execution.agent_key = ? AND COALESCE(execution.agent_instance_id::text, '') > ?)))",
			cursor.TotalCostMicros, cursor.TotalCostMicros, cursor.AgentKey, cursor.AgentKey, cursor.InstanceID,
		)
	}
	return query.Order("SUM(execution.total_cost_micros) DESC, execution.agent_key ASC, COALESCE(execution.agent_instance_id::text, '') ASC").Limit(limit)
}

func applyAutomationCostAgentInstanceSnapshot(query *gorm.DB, snapshotAt time.Time, filters automationCostQuery) *gorm.DB {
	return applyAutomationCostTimeWindow(query, snapshotAt, filters)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
