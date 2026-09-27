package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var automationTraceQueryNames = []string{
	"organization_id", "client_id", "project_id", "epic_id", "work_item_id", "step_id", "step_key",
	"agent_key", "worker_id", "machine_id", "agent_instance_id", "operation", "tool",
	"status", "provider", "model", "run_id", "from", "to", "limit", "cursor", "snapshot_at", "q",
}

var automationTraceSnapshotPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T`)
var automationTraceModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,127}$`)
var automationTraceSearchPattern = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,120}$`)

type automationTraceItem struct {
	ID                uuid.UUID                               `json:"id"`
	Kind              string                                  `json:"kind"`
	OccurredAt        time.Time                               `json:"occurred_at"`
	AutomationTaskID  *uuid.UUID                              `json:"automation_task_id,omitempty"`
	RunID             string                                  `json:"run_id,omitempty"`
	AgentKey          string                                  `json:"agent_key"`
	WorkerID          string                                  `json:"worker_id,omitempty"`
	MachineID         string                                  `json:"machine_id,omitempty"`
	AgentInstanceID   *uuid.UUID                              `json:"agent_instance_id,omitempty"`
	ClientID          *uuid.UUID                              `json:"client_id,omitempty"`
	ClientName        string                                  `json:"client_name,omitempty"`
	ProjectID         *uuid.UUID                              `json:"project_id,omitempty"`
	ProjectName       string                                  `json:"project_name,omitempty"`
	EpicID            *uuid.UUID                              `json:"epic_id,omitempty"`
	EpicTitle         string                                  `json:"epic_title,omitempty"`
	WorkItemID        *uuid.UUID                              `json:"work_item_id,omitempty"`
	WorkItemTitle     string                                  `json:"work_item_title,omitempty"`
	StepID            *uuid.UUID                              `json:"step_id,omitempty"`
	StepKey           string                                  `json:"step_key,omitempty"`
	Operation         string                                  `json:"operation"`
	Tool              string                                  `json:"tool,omitempty"`
	Status            string                                  `json:"status"`
	Provider          string                                  `json:"provider,omitempty"`
	Model             string                                  `json:"model,omitempty"`
	InputTokens       *int64                                  `json:"input_tokens,omitempty"`
	OutputTokens      *int64                                  `json:"output_tokens,omitempty"`
	CachedInputTokens *int64                                  `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens  *int64                                  `json:"cache_write_tokens,omitempty"`
	LatencyMillis     *int64                                  `json:"latency_ms,omitempty"`
	TotalCostMicros   *int64                                  `json:"total_cost_microusd,omitempty"`
	CostPricingStatus string                                  `json:"cost_pricing_status,omitempty"`
	Summary           string                                  `json:"summary"`
	EventType         string                                  `json:"event_type,omitempty"`
	PreviousStatus    string                                  `json:"previous_status,omitempty"`
	PreviousAgentKey  string                                  `json:"previous_agent_key,omitempty"`
	PreviousMachineID string                                  `json:"previous_machine_id,omitempty"`
	ActivityAction    string                                  `json:"activity_action,omitempty"`
	ActivityDetails   *models.DeliveryPlanStepActivityDetails `json:"activity_details,omitempty"`
}

type automationTraceResponse struct {
	Items        []automationTraceItem    `json:"items"`
	HasMore      bool                     `json:"has_more"`
	NextCursor   string                   `json:"next_cursor"`
	SnapshotAt   time.Time                `json:"snapshot_at"`
	Limit        int                      `json:"limit"`
	CostCoverage agentHistoryCostCoverage `json:"cost_coverage"`
}

// GetAutomationTraces is the organization-wide, server-side merged event
// timeline. It performs one SQL UNION query so the database, not the client,
// owns global ordering and stable keyset pagination across all event sources.
// Its projection is deliberately allow-listed and does not include prompt,
// model request/response bodies, raw output, credentials, or private reasoning.
func GetAutomationTraces(c echo.Context) error {
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Trace timeline unavailable", "Database is unavailable")
	}
	c.Response().Header().Set(echo.HeaderCacheControl, "private, no-store")
	filters, explicitSnapshot, err := parseAutomationTraceFilters(c)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid trace filters", err.Error())
	}
	if err := validateAutomationTraceOrganizationFilter(c, filters); err != nil {
		return err
	}
	if err := authorizeAgentHistoryWorkspace(c, user, &filters); err != nil {
		return err
	}
	filters.ActorSub, _ = c.Get("cognito_sub").(string)
	filters.TenantCode, _ = c.Get("tenant_code").(string)
	now := time.Now().UTC().Add(time.Minute)
	if filters.Cursor != nil {
		cursorSnapshot := filters.Cursor.SnapshotAt.UTC()
		if explicitSnapshot != nil && !explicitSnapshot.Equal(cursorSnapshot) {
			return utils.Error(c, http.StatusBadRequest, "Invalid trace cursor", "snapshot_at does not match cursor")
		}
		filters.SnapshotAt = &cursorSnapshot
	} else if explicitSnapshot != nil {
		if explicitSnapshot.After(now) {
			return utils.Error(c, http.StatusBadRequest, "Invalid snapshot_at", "snapshot_at cannot be in the future")
		}
		filters.SnapshotAt = explicitSnapshot
	} else {
		snapshotAt := time.Now().UTC().Truncate(time.Microsecond)
		filters.SnapshotAt = &snapshotAt
	}
	if filters.Cursor != nil && filters.Cursor.Scope != automationTraceCursorScope(filters) {
		return utils.Error(c, http.StatusBadRequest, "Invalid trace cursor", "cursor does not belong to this workspace, actor, snapshot, and filter set")
	}

	page, err := queryAgentHistory(c, configuration.DB, "", filters)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Trace timeline unavailable", "")
	}
	response := automationTraceResponse{
		Items: make([]automationTraceItem, 0, len(page.Items)), HasMore: page.HasMore,
		SnapshotAt: page.SnapshotAt.UTC(), Limit: page.Limit, CostCoverage: page.CostCoverage,
	}
	for _, row := range page.Items {
		item := automationTraceItem{
			ID: row.ID, Kind: safeAutomationTraceKind(row), OccurredAt: row.OccurredAt.UTC(),
			AutomationTaskID: safeAutomationTraceTaskID(row), RunID: safeAgentHistoryRunID(row.RunID),
			AgentKey: safeAutomationTraceAgentKey(row.AgentKey), WorkerID: row.WorkerID,
			MachineID: row.MachineID, AgentInstanceID: row.AgentInstanceID,
			ClientID: row.ClientID, ClientName: row.ClientName, ProjectID: row.ProjectID,
			ProjectName: row.ProjectName, EpicID: row.EpicID, EpicTitle: row.EpicTitle,
			WorkItemID: row.WorkItemID, WorkItemTitle: row.WorkItemTitle, StepID: row.StepID,
			StepKey: row.StepKey, Operation: row.Operation, Tool: row.Tool, Status: row.Status,
			Provider: row.Provider, Model: row.Model,
			InputTokens:       safeAutomationTraceUsageValue(row.Kind, row.InputTokens),
			OutputTokens:      safeAutomationTraceUsageValue(row.Kind, row.OutputTokens),
			CachedInputTokens: safeAutomationTraceUsageValue(row.Kind, row.CachedInputTokens),
			CacheWriteTokens:  safeAutomationTraceUsageValue(row.Kind, row.CacheWriteTokens),
			LatencyMillis:     safeAutomationTraceUsageValue(row.Kind, row.LatencyMillis),
			TotalCostMicros:   safeAutomationTraceCost(row), CostPricingStatus: row.CostPricingStatus,
			Summary: row.Summary, EventType: row.EventType, PreviousStatus: row.PreviousStatus,
			PreviousAgentKey: row.PreviousAgentKey, PreviousMachineID: row.PreviousMachineID,
			ActivityAction: row.ActivityAction, ActivityDetails: row.ActivityDetails,
		}
		response.Items = append(response.Items, item)
	}
	if response.HasMore && len(response.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		response.NextCursor = encodeAgentHistoryCursor(agentHistoryCursor{
			Version: 2, Scope: automationTraceCursorScope(filters), SnapshotAt: *filters.SnapshotAt,
			OccurredAt: last.OccurredAt, ID: last.ID, Kind: last.Kind,
		})
	}
	return utils.Success(c, http.StatusOK, "Automation trace timeline", response)
}

// Only immutable provider-call ledger rows have authoritative usage and cost.
// Lifecycle, assignment, evidence, and other events must not look like free
// calls merely because their union projection fills numeric columns with zero.
func safeAutomationTraceUsageValue(kind string, value *int64) *int64 {
	if kind != "inference" && kind != "tool_call" {
		return nil
	}
	if value == nil {
		return nil
	}
	if *value < 0 {
		zero := int64(0)
		return &zero
	}
	return value
}

func safeAutomationTraceCost(row agentHistoryItem) *int64 {
	if !strings.EqualFold(strings.TrimSpace(row.Currency), "USD") {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(row.PricingBasis)) {
	case "", "legacy", "unpriced":
		return nil
	default:
		return safeAutomationTraceUsageValue(row.Kind, row.TotalCostMicros)
	}
}

// safeAutomationTraceTaskID omits task IDs for synthetic gate-decision rows:
// those rows carry the latest task for the work item only to satisfy the
// shared timeline projection, not a causal task association.
func safeAutomationTraceTaskID(row agentHistoryItem) *uuid.UUID {
	if row.Kind == "gate_decision" || row.TaskID == uuid.Nil {
		return nil
	}
	taskID := row.TaskID
	return &taskID
}

func parseAutomationTraceFilters(c echo.Context) (agentHistoryFilters, *time.Time, error) {
	query := c.QueryParams()
	for _, name := range automationTraceQueryNames {
		if values, exists := query[name]; exists && len(values) > 1 {
			return agentHistoryFilters{}, nil, fmt.Errorf("%s must appear at most once", name)
		}
	}
	filters, err := parseAgentHistoryFilters(c)
	if err != nil {
		return agentHistoryFilters{}, nil, err
	}
	if raw := strings.TrimSpace(c.QueryParam("organization_id")); raw != "" {
		organizationID, parseErr := uuid.FromString(raw)
		if parseErr != nil || organizationID == uuid.Nil {
			return agentHistoryFilters{}, nil, fmt.Errorf("organization_id must be a UUID")
		}
		filters.OrganizationID = &organizationID
	}
	// Model identifiers are compared exactly in SQL. Trim surrounding
	// whitespace, preserve case, and accept only bounded printable model IDs;
	// this avoids surprising case-folding of provider-specific identifiers.
	filters.Model = strings.TrimSpace(c.QueryParam("model"))
	if filters.Model != "" && !automationTraceModelPattern.MatchString(filters.Model) {
		return agentHistoryFilters{}, nil, fmt.Errorf("model is invalid")
	}
	var snapshotAt *time.Time
	if raw := strings.TrimSpace(c.QueryParam("snapshot_at")); raw != "" {
		if !automationTraceSnapshotPattern.MatchString(raw) {
			return agentHistoryFilters{}, nil, fmt.Errorf("snapshot_at must be RFC3339")
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil {
			return agentHistoryFilters{}, nil, fmt.Errorf("snapshot_at must be RFC3339")
		}
		parsed = parsed.UTC().Truncate(time.Microsecond)
		snapshotAt = &parsed
	}
	return filters, snapshotAt, nil
}

// validateAutomationTraceOrganizationFilter permits platform operators to
// narrow the trace explorer to one organization. In an organization workspace,
// the middleware-selected organization remains authoritative; a query parameter
// can confirm that scope but cannot switch it.
func validateAutomationTraceOrganizationFilter(c echo.Context, filters agentHistoryFilters) error {
	workspaceMode, _ := c.Get("workspace_mode").(string)
	if !strings.EqualFold(strings.TrimSpace(workspaceMode), "organization") || filters.OrganizationID == nil {
		return nil
	}
	selectedOrganizationID, ok := c.Get("organization_id").(uuid.UUID)
	if !ok || selectedOrganizationID == uuid.Nil || *filters.OrganizationID != selectedOrganizationID {
		return utils.Error(c, http.StatusNotFound, "Trace timeline unavailable", "")
	}
	return nil
}

func automationTraceCursorScope(filters agentHistoryFilters) string {
	base := agentHistoryCursorScope("", filters)
	payload, _ := json.Marshal(struct {
		Base       string    `json:"base"`
		SnapshotAt time.Time `json:"snapshot_at"`
	}{Base: base, SnapshotAt: filters.SnapshotAt.UTC()})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func safeAutomationTraceAgentKey(value string) string {
	value = strings.TrimSpace(value)
	if !agentProfileKeyPattern.MatchString(value) {
		return "unknown"
	}
	return value
}

func safeAutomationTraceKind(row agentHistoryItem) string {
	switch row.Kind {
	case "task", "task_event", "inference", "tool_call", "step_event", "step_activity", "assignment_event", "gate_decision", "step_evidence":
		return row.Kind
	default:
		return "recorded"
	}
}
