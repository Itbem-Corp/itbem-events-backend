package automation

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	deliverycontroller "events-stocks/controllers/delivery"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/organizationscope"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	agentHistoryDefaultLimit = 50
	agentHistoryMaxLimit     = 100
)

var (
	agentHistoryRunPattern     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	agentHistoryStepKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,63}$`)
)

var agentHistoryTools = map[string]struct{}{"agent_loop": {}, "stagehand": {}}

var agentHistoryStatuses = map[string]struct{}{
	"queued": {}, "running": {}, "cancel_requested": {}, "completed": {}, "failed": {}, "cancelled": {},
	"pending": {}, "dispatched": {}, "planned": {}, "ready": {}, "blocked": {}, "started": {}, "skipped": {},
	"approved": {}, "changes_requested": {},
}

type agentHistoryCursor struct {
	Version    int       `json:"version"`
	Scope      string    `json:"scope"`
	SnapshotAt time.Time `json:"snapshot_at,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	ID         uuid.UUID `json:"id"`
	Kind       string    `json:"kind"`
}

type agentHistoryFilters struct {
	Limit                    int
	Cursor                   *agentHistoryCursor
	ActorSub                 string
	WorkspaceMode            string
	TenantCode               string
	OrganizationID           *uuid.UUID
	SnapshotAt               *time.Time
	From                     *time.Time
	To                       *time.Time
	ClientID                 *uuid.UUID
	ClientScopeIDs           []uuid.UUID
	ProjectID                *uuid.UUID
	AuthorizedProjectIDs     []uuid.UUID
	AuthorizedProjectsScoped bool
	EpicID                   *uuid.UUID
	WorkItemID               *uuid.UUID
	StepID                   *uuid.UUID
	StepKey                  string
	WorkerID                 *uuid.UUID
	MachineID                *uuid.UUID
	AgentInstanceID          *uuid.UUID
	RunID                    string
	AgentKey                 string
	Operation                string
	Tool                     string
	Status                   string
	Provider                 string
	Model                    string
	Search                   string
}

type agentHistoryItem struct {
	ID                      uuid.UUID                               `json:"id"`
	Kind                    string                                  `json:"kind"`
	OccurredAt              time.Time                               `json:"occurred_at"`
	TaskID                  uuid.UUID                               `json:"task_id"`
	RunID                   string                                  `json:"run_id,omitempty"`
	WorkerID                string                                  `json:"worker_id,omitempty"`
	MachineID               string                                  `json:"machine_id,omitempty"`
	AgentInstanceID         *uuid.UUID                              `json:"agent_instance_id,omitempty"`
	Operation               string                                  `json:"operation"`
	Status                  string                                  `json:"status"`
	EventType               string                                  `json:"event_type,omitempty"`
	PreviousStatus          string                                  `json:"previous_status,omitempty"`
	AttemptCount            *int                                    `json:"attempt_count,omitempty"`
	EventSequence           int64                                   `json:"event_sequence,omitempty"`
	CurrentAgentKey         string                                  `json:"current_agent_key,omitempty"`
	PreviousRunID           string                                  `json:"previous_run_id,omitempty"`
	PreviousWorkerID        string                                  `json:"previous_worker_id,omitempty"`
	PreviousAgentKey        string                                  `json:"previous_agent_key,omitempty"`
	PreviousMachineID       string                                  `json:"previous_machine_id,omitempty"`
	PreviousAgentInstanceID *uuid.UUID                              `json:"previous_agent_instance_id,omitempty"`
	ClientID                *uuid.UUID                              `json:"client_id,omitempty"`
	ClientName              string                                  `json:"client_name,omitempty"`
	ProjectID               *uuid.UUID                              `json:"project_id,omitempty"`
	ProjectName             string                                  `json:"project_name,omitempty"`
	EpicID                  *uuid.UUID                              `json:"epic_id,omitempty"`
	EpicTitle               string                                  `json:"epic_title,omitempty"`
	WorkItemID              *uuid.UUID                              `json:"work_item_id,omitempty"`
	WorkItemTitle           string                                  `json:"work_item_title,omitempty"`
	StepID                  *uuid.UUID                              `json:"step_id,omitempty"`
	StepKey                 string                                  `json:"step_key,omitempty"`
	Provider                string                                  `json:"provider,omitempty"`
	Model                   string                                  `json:"model,omitempty"`
	ActivityAction          string                                  `json:"activity_action,omitempty"`
	AgentKey                string                                  `json:"-" gorm:"-"`
	Tool                    string                                  `json:"-" gorm:"-"`
	ActivityDetails         *models.DeliveryPlanStepActivityDetails `json:"activity_details,omitempty" gorm:"-"`
	ActivityDetailsJSON     string                                  `json:"-"`
	InputTokens             *int64                                  `json:"input_tokens,omitempty"`
	OutputTokens            *int64                                  `json:"output_tokens,omitempty"`
	CachedInputTokens       *int64                                  `json:"-"`
	CacheWriteTokens        *int64                                  `json:"-"`
	LatencyMillis           *int64                                  `json:"-"`
	TotalCostMicros         *int64                                  `json:"total_cost_microusd,omitempty"`
	CostPricingStatus       string                                  `json:"cost_pricing_status,omitempty"`
	Currency                string                                  `json:"-"`
	PricingBasis            string                                  `json:"-"`
	Summary                 string                                  `json:"summary"`
}

type agentHistoryResponse struct {
	AgentKey     string                   `json:"agent_key"`
	Items        []agentHistoryItem       `json:"items"`
	CostCoverage agentHistoryCostCoverage `json:"cost_coverage"`
	Limit        int                      `json:"limit"`
	HasMore      bool                     `json:"has_more"`
	NextCursor   string                   `json:"next_cursor,omitempty"`
	SnapshotAt   time.Time                `json:"snapshot_at"`
}

// Cost coverage counts provider calls returned on this cursor page only; it
// is not a whole-filter or historical aggregate.
type agentHistoryCostCoverage struct {
	Scope                 string `json:"scope"`
	VerifiedUSDExecutions int64  `json:"verified_usd_executions"`
	UnpricedExecutions    int64  `json:"unpriced_executions"`
}

// GetAgentHistory returns an allow-listed, cursor-paginated event timeline for
// one agent profile. Platform administrators may query globally; organization
// callers are constrained to the selected tenant and, at client scope, to
// projects their membership role permits them to view. It deliberately
// excludes prompts, provider payloads, private object references, raw errors,
// tool output, and hidden reasoning.
func GetAgentHistory(c echo.Context) error {
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Agent history unavailable", "Database is unavailable")
	}
	filters, err := parseAgentHistoryFilters(c)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid history filters", err.Error())
	}
	if err := authorizeAgentHistoryWorkspace(c, user, &filters); err != nil {
		return err
	}
	filters.ActorSub, _ = c.Get("cognito_sub").(string)
	filters.TenantCode, _ = c.Get("tenant_code").(string)
	if filters.Cursor != nil && !filters.Cursor.SnapshotAt.IsZero() {
		snapshotAt := filters.Cursor.SnapshotAt.UTC()
		filters.SnapshotAt = &snapshotAt
	}
	agentKey := strings.TrimSpace(c.Param("agentKey"))
	if !agentProfileKeyPattern.MatchString(agentKey) {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent", "Agent key is invalid")
	}
	if filters.Cursor != nil && filters.Cursor.Scope != agentHistoryCursorScope(agentKey, filters) {
		return utils.Error(c, http.StatusBadRequest, "Invalid history cursor", "cursor does not belong to this agent and filter set")
	}
	response, err := queryAgentHistory(c, configuration.DB, agentKey, filters)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Agent history unavailable", "")
	}
	return utils.Success(c, http.StatusOK, "Automation agent history", response)
}

func authorizeAgentHistoryWorkspace(c echo.Context, user *models.User, filters *agentHistoryFilters) error {
	workspaceMode, _ := c.Get("workspace_mode").(string)
	workspaceMode = strings.ToLower(strings.TrimSpace(workspaceMode))
	filters.WorkspaceMode = workspaceMode
	switch workspaceMode {
	case "platform":
		if user == nil || !user.IsPlatformAdmin() {
			return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
		}
		if filters.ClientID != nil {
			clientIDs, err := organizationscope.ClientIDs(configuration.DB, *filters.ClientID)
			if err != nil {
				if errors.Is(err, organizationscope.ErrOrganizationNotFound) {
					return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
				}
				return utils.Error(c, http.StatusInternalServerError, "Agent history unavailable", "")
			}
			filters.ClientScopeIDs = clientIDs
		}
		if filters.ProjectID != nil {
			projectClientID, err := loadAgentHistoryProjectClient(*filters.ProjectID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
				}
				return utils.Error(c, http.StatusInternalServerError, "Agent history unavailable", "")
			}
			if len(filters.ClientScopeIDs) > 0 && !containsAgentDirectoryClient(filters.ClientScopeIDs, projectClientID) {
				return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
			}
		}
		return nil
	case "organization":
		organizationID, ok := c.Get("organization_id").(uuid.UUID)
		if !ok || organizationID == uuid.Nil {
			return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
		}
		filters.OrganizationID = &organizationID
		if (user == nil || !user.IsPlatformAdmin()) && filters.ProjectID == nil && filters.ClientID == nil {
			return utils.Error(c, http.StatusBadRequest, "Project scope required", "project_id or client_id is required")
		}
		var organizationClientIDs []uuid.UUID
		if filters.ClientID != nil {
			var err error
			organizationClientIDs, err = organizationscope.ClientIDs(configuration.DB, organizationID)
			if err != nil {
				if errors.Is(err, organizationscope.ErrOrganizationNotFound) {
					return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
				}
				return utils.Error(c, http.StatusInternalServerError, "Agent history unavailable", "")
			}
			clientIDs, clientErr := organizationscope.ClientIDs(configuration.DB, *filters.ClientID)
			if clientErr != nil {
				if errors.Is(clientErr, organizationscope.ErrOrganizationNotFound) {
					return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
				}
				return utils.Error(c, http.StatusInternalServerError, "Agent history unavailable", "")
			}
			if !containsAgentDirectoryClient(organizationClientIDs, *filters.ClientID) {
				return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
			}
			filters.ClientScopeIDs = clientIDs
		}
		if filters.ProjectID != nil {
			allowed, authErr := deliverycontroller.AuthorizeProjectView(c, *filters.ProjectID)
			if authErr != nil {
				return authErr
			}
			if !allowed {
				return echo.NewHTTPError(http.StatusNotFound)
			}
			if filters.ClientID != nil {
				projectClientID, projectErr := loadAgentHistoryProjectClient(*filters.ProjectID)
				if projectErr != nil {
					if errors.Is(projectErr, gorm.ErrRecordNotFound) {
						return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
					}
					return utils.Error(c, http.StatusInternalServerError, "Agent history unavailable", "")
				}
				if !containsAgentDirectoryClient(organizationClientIDs, projectClientID) || !containsAgentDirectoryClient(filters.ClientScopeIDs, projectClientID) {
					return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
				}
			}
		} else if user == nil || !user.IsPlatformAdmin() {
			// A client-level view is the union of projects the current actor can
			// actually view, not every project under the selected organization.
			projectIDs, projectErr := deliverycontroller.VisibleProjectIDsForActor(c, user, filters.ClientScopeIDs)
			if projectErr != nil {
				return utils.Error(c, http.StatusInternalServerError, "Agent history unavailable", "")
			}
			filters.AuthorizedProjectIDs = projectIDs
			filters.AuthorizedProjectsScoped = true
		}
		return nil
	default:
		return utils.Error(c, http.StatusNotFound, "Agent history unavailable", "")
	}
}

func loadAgentHistoryProjectClient(projectID uuid.UUID) (uuid.UUID, error) {
	var project models.DeliveryProject
	if err := configuration.DB.Select("id", "client_id").First(&project, "id = ?", projectID).Error; err != nil {
		return uuid.Nil, err
	}
	return project.ClientID, nil
}

func parseAgentHistoryFilters(c echo.Context) (agentHistoryFilters, error) {
	filters := agentHistoryFilters{Limit: agentHistoryDefaultLimit}
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > agentHistoryMaxLimit {
			return agentHistoryFilters{}, fmt.Errorf("limit must be between 1 and %d", agentHistoryMaxLimit)
		}
		filters.Limit = limit
	}
	if raw := strings.TrimSpace(c.QueryParam("cursor")); raw != "" {
		cursor, err := decodeAgentHistoryCursor(raw)
		if err != nil {
			return agentHistoryFilters{}, fmt.Errorf("cursor is invalid")
		}
		filters.Cursor = &cursor
	}
	parseTime := func(name string) (*time.Time, error) {
		raw := strings.TrimSpace(c.QueryParam(name))
		if raw == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("%s must be RFC3339", name)
		}
		parsed = parsed.UTC()
		return &parsed, nil
	}
	var err error
	if filters.From, err = parseTime("from"); err != nil {
		return agentHistoryFilters{}, err
	}
	if filters.To, err = parseTime("to"); err != nil {
		return agentHistoryFilters{}, err
	}
	if filters.From != nil && filters.To != nil && !filters.From.Before(*filters.To) {
		return agentHistoryFilters{}, fmt.Errorf("from must be before to")
	}
	for _, filter := range []struct {
		name   string
		target **uuid.UUID
	}{
		{name: "client_id", target: &filters.ClientID},
		{name: "project_id", target: &filters.ProjectID},
		{name: "epic_id", target: &filters.EpicID},
		{name: "work_item_id", target: &filters.WorkItemID},
		{name: "step_id", target: &filters.StepID},
		{name: "worker_id", target: &filters.WorkerID},
		{name: "machine_id", target: &filters.MachineID},
		{name: "agent_instance_id", target: &filters.AgentInstanceID},
	} {
		raw := strings.TrimSpace(c.QueryParam(filter.name))
		if raw == "" {
			continue
		}
		id, parseErr := uuid.FromString(raw)
		if parseErr != nil || id == uuid.Nil {
			return agentHistoryFilters{}, fmt.Errorf("%s must be a UUID", filter.name)
		}
		*filter.target = &id
	}
	filters.StepKey = strings.TrimSpace(c.QueryParam("step_key"))
	if filters.StepKey != "" && !agentHistoryStepKeyPattern.MatchString(filters.StepKey) {
		return agentHistoryFilters{}, fmt.Errorf("step_key is invalid")
	}
	filters.RunID = strings.TrimSpace(c.QueryParam("run_id"))
	if filters.RunID != "" && !agentHistoryRunPattern.MatchString(filters.RunID) {
		return agentHistoryFilters{}, fmt.Errorf("run_id is invalid")
	}
	filters.AgentKey = strings.TrimSpace(c.QueryParam("agent_key"))
	if filters.AgentKey != "" && !agentProfileKeyPattern.MatchString(filters.AgentKey) {
		return agentHistoryFilters{}, fmt.Errorf("agent_key is invalid")
	}
	filters.Operation = strings.TrimSpace(c.QueryParam("operation"))
	if filters.Operation != "" {
		if _, ok := allowedOperations[filters.Operation]; !ok {
			return agentHistoryFilters{}, fmt.Errorf("operation is not supported")
		}
	}
	filters.Tool = strings.ToLower(strings.TrimSpace(c.QueryParam("tool")))
	if filters.Tool != "" && !agentHistoryToolSupported(filters.Tool) {
		return agentHistoryFilters{}, fmt.Errorf("tool is invalid")
	}
	filters.Status = strings.TrimSpace(c.QueryParam("status"))
	if filters.Status != "" {
		filters.Status = strings.ToLower(filters.Status)
		if _, ok := agentHistoryStatuses[filters.Status]; !ok {
			return agentHistoryFilters{}, fmt.Errorf("status is not supported")
		}
	}
	filters.Provider = strings.ToLower(strings.TrimSpace(c.QueryParam("provider")))
	if filters.Provider != "" && !agentHistoryProviderSupported(filters.Provider) {
		return agentHistoryFilters{}, fmt.Errorf("provider is not supported")
	}
	filters.Search = strings.TrimSpace(c.QueryParam("q"))
	if filters.Search != "" && !automationTraceSearchPattern.MatchString(filters.Search) {
		return agentHistoryFilters{}, fmt.Errorf("q must be at most 120 printable characters")
	}
	return filters, nil
}

func encodeAgentHistoryCursor(cursor agentHistoryCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeAgentHistoryCursor(raw string) (agentHistoryCursor, error) {
	var cursor agentHistoryCursor
	if len(raw) > 1024 {
		return cursor, fmt.Errorf("cursor is too long")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return agentHistoryCursor{}, fmt.Errorf("cursor is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 2 || len(cursor.Scope) != 64 || cursor.ID == uuid.Nil || cursor.OccurredAt.IsZero() || cursor.SnapshotAt.IsZero() {
		return agentHistoryCursor{}, fmt.Errorf("cursor is malformed")
	}
	if _, err := hex.DecodeString(cursor.Scope); err != nil {
		return agentHistoryCursor{}, fmt.Errorf("cursor scope is malformed")
	}
	if cursor.Kind != "task" && cursor.Kind != "task_event" && cursor.Kind != "inference" && cursor.Kind != "tool_call" && cursor.Kind != "step_event" && cursor.Kind != "step_activity" && cursor.Kind != "assignment_event" && cursor.Kind != "gate_decision" && cursor.Kind != "step_evidence" {
		return agentHistoryCursor{}, fmt.Errorf("cursor kind is invalid")
	}
	cursor.OccurredAt = cursor.OccurredAt.UTC()
	cursor.SnapshotAt = cursor.SnapshotAt.UTC()
	if cursor.SnapshotAt.Before(cursor.OccurredAt) || cursor.SnapshotAt.After(time.Now().UTC().Add(time.Minute)) {
		return agentHistoryCursor{}, fmt.Errorf("cursor snapshot is outside its position range")
	}
	return cursor, nil
}

// agentHistoryCursorScope binds pagination to the complete visible query so a
// cursor cannot silently continue a different agent or filter selection. The
// digest is a consistency boundary, not an authorization token; the request
// still repeats workspace/client/project authorization and SQL filtering.
func agentHistoryCursorScope(agentKey string, filters agentHistoryFilters) string {
	type scopeInput struct {
		AgentKey                 string `json:"agent_key"`
		Limit                    int    `json:"limit"`
		ActorSub                 string `json:"actor_sub,omitempty"`
		WorkspaceMode            string `json:"workspace_mode,omitempty"`
		TenantCode               string `json:"tenant_code,omitempty"`
		OrganizationID           string `json:"organization_id,omitempty"`
		From                     string `json:"from,omitempty"`
		To                       string `json:"to,omitempty"`
		ClientID                 string `json:"client_id,omitempty"`
		ClientScopeIDs           string `json:"client_scope_ids,omitempty"`
		ProjectID                string `json:"project_id,omitempty"`
		AuthorizedProjectIDs     string `json:"authorized_project_ids,omitempty"`
		AuthorizedProjectsScoped bool   `json:"authorized_projects_scoped,omitempty"`
		EpicID                   string `json:"epic_id,omitempty"`
		WorkItemID               string `json:"work_item_id,omitempty"`
		StepID                   string `json:"step_id,omitempty"`
		StepKey                  string `json:"step_key,omitempty"`
		WorkerID                 string `json:"worker_id,omitempty"`
		MachineID                string `json:"machine_id,omitempty"`
		AgentInstanceID          string `json:"agent_instance_id,omitempty"`
		RunID                    string `json:"run_id,omitempty"`
		Operation                string `json:"operation,omitempty"`
		Status                   string `json:"status,omitempty"`
		Provider                 string `json:"provider,omitempty"`
		Model                    string `json:"model,omitempty"`
		Search                   string `json:"search,omitempty"`
		AgentFilterKey           string `json:"agent_filter_key,omitempty"`
		Tool                     string `json:"tool,omitempty"`
	}
	input := scopeInput{
		AgentKey: agentKey, Limit: filters.Limit, ActorSub: filters.ActorSub,
		WorkspaceMode: filters.WorkspaceMode, TenantCode: strings.ToLower(strings.TrimSpace(filters.TenantCode)),
		Operation: filters.Operation, Status: filters.Status, Provider: filters.Provider, Model: filters.Model, Search: filters.Search,
		AgentFilterKey: filters.AgentKey, Tool: filters.Tool,
	}
	if filters.OrganizationID != nil {
		input.OrganizationID = filters.OrganizationID.String()
	}
	if filters.From != nil {
		input.From = filters.From.UTC().Format(time.RFC3339Nano)
	}
	if filters.To != nil {
		input.To = filters.To.UTC().Format(time.RFC3339Nano)
	}
	if filters.ClientID != nil {
		input.ClientID = filters.ClientID.String()
	}
	input.ClientScopeIDs = canonicalAgentHistoryUUIDs(filters.ClientScopeIDs)
	if filters.ProjectID != nil {
		input.ProjectID = filters.ProjectID.String()
	}
	input.AuthorizedProjectIDs = canonicalAgentHistoryUUIDs(filters.AuthorizedProjectIDs)
	input.AuthorizedProjectsScoped = filters.AuthorizedProjectsScoped
	if filters.EpicID != nil {
		input.EpicID = filters.EpicID.String()
	}
	if filters.WorkItemID != nil {
		input.WorkItemID = filters.WorkItemID.String()
	}
	if filters.StepID != nil {
		input.StepID = filters.StepID.String()
	}
	input.StepKey = filters.StepKey
	if filters.WorkerID != nil {
		input.WorkerID = filters.WorkerID.String()
	}
	if filters.MachineID != nil {
		input.MachineID = filters.MachineID.String()
	}
	if filters.AgentInstanceID != nil {
		input.AgentInstanceID = filters.AgentInstanceID.String()
	}
	input.RunID = filters.RunID
	encoded, _ := json.Marshal(input)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func canonicalAgentHistoryUUIDs(values []uuid.UUID) string {
	if len(values) == 0 {
		return ""
	}
	canonical := make([]string, 0, len(values))
	for _, value := range values {
		if value != uuid.Nil {
			canonical = append(canonical, value.String())
		}
	}
	sort.Strings(canonical)
	return strings.Join(canonical, ",")
}

func queryAgentHistory(c echo.Context, db *gorm.DB, agentKey string, filters agentHistoryFilters) (agentHistoryResponse, error) {
	if filters.SnapshotAt == nil {
		snapshotAt := time.Now().UTC().Truncate(time.Microsecond)
		if filters.Cursor != nil && !filters.Cursor.SnapshotAt.IsZero() {
			snapshotAt = filters.Cursor.SnapshotAt.UTC()
		}
		filters.SnapshotAt = &snapshotAt
	}
	timeline := `WITH activity AS (
		WITH requested_agent AS (SELECT ?::text AS agent_key)
		SELECT t.id, 'task'::text AS kind, t.updated_at AS occurred_at, t.updated_at AS recorded_at,
			t.id AS task_id, t.run_id::text, t.worker_id::text, t.machine_id::text, t.agent_instance_id, t.operation, t.status,
			'legacy_snapshot'::text AS event_type, ''::text AS previous_status, COALESCE(t.attempt_count, 0)::integer AS attempt_count, 0::bigint AS event_sequence, t.agent_key AS current_agent_key,
			''::text AS previous_run_id, ''::text AS previous_worker_id, ''::text AS previous_agent_key, ''::text AS previous_machine_id, NULL::uuid AS previous_agent_instance_id,
			c.id AS client_id, LEFT(c.name, 2048) AS client_name, p.id AS project_id, LEFT(p.name, 2048) AS project_name,
			w.id AS work_item_id, LEFT(w.title, 2048) AS work_item_title, active_epic.id AS epic_id, LEFT(active_epic.title, 2048) AS epic_title,
			task_step.id AS step_id, COALESCE(task_step.step_key, t.progress_step) AS step_key,
			t.provider, t.model, 0::bigint AS input_tokens, 0::bigint AS output_tokens,
			0::bigint AS total_cost_micros, 'Legacy task snapshot'::text AS summary, ''::text AS activity_action, '{}'::text AS activity_details_json
		FROM automation_tasks t
		LEFT JOIN delivery_plan_step_assignments task_assignment ON task_assignment.child_automation_task_id = t.id
		LEFT JOIN delivery_plan_steps task_step ON task_step.id = task_assignment.delivery_plan_step_id
		LEFT JOIN delivery_work_items w ON w.id = t.delivery_work_item_id
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE t.agent_key = (SELECT agent_key FROM requested_agent)
		  AND NOT EXISTS (SELECT 1 FROM automation_task_events prior_event WHERE prior_event.automation_task_id = t.id)
		UNION ALL
		SELECT e.id, 'task_event'::text, e.occurred_at, e.created_at, e.automation_task_id, e.run_id, e.worker_id, e.machine_id, e.agent_instance_id,
			COALESCE(t.operation, ''), e.status, e.event_type, e.previous_status, e.attempt_count, e.sequence, e.agent_key,
			e.previous_run_id, e.previous_worker_id, e.previous_agent_key, e.previous_machine_id, e.previous_agent_instance_id,
			c.id, LEFT(c.name, 2048), p.id, LEFT(p.name, 2048), w.id, LEFT(w.title, 2048), active_epic.id, LEFT(active_epic.title, 2048),
		task_step.id, COALESCE(task_step.step_key, t.progress_step),
			t.provider, t.model, 0::bigint, 0::bigint, 0::bigint, 'Task lifecycle event'::text, ''::text, '{}'::text
		FROM automation_task_events e
		LEFT JOIN automation_tasks t ON t.id = e.automation_task_id
		LEFT JOIN delivery_plan_step_assignments task_assignment ON task_assignment.child_automation_task_id = t.id
		LEFT JOIN delivery_plan_steps task_step ON task_step.id = task_assignment.delivery_plan_step_id
		LEFT JOIN delivery_work_items w ON w.id = t.delivery_work_item_id
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE e.agent_key = (SELECT agent_key FROM requested_agent)
		   OR e.previous_agent_key = (SELECT agent_key FROM requested_agent)
		UNION ALL
		SELECT e.id, 'inference'::text, COALESCE(e.completed_at, e.created_at), e.created_at, e.automation_task_id, e.run_id::text, e.worker_id::text, e.machine_id::text, e.agent_instance_id,
			COALESCE(t.operation, ''), 'completed'::text, ''::text, ''::text, 0::integer, 0::bigint, e.agent_key, ''::text, ''::text, ''::text, ''::text, NULL::uuid,
			c.id, LEFT(c.name, 2048), p.id, LEFT(p.name, 2048), w.id, LEFT(w.title, 2048), active_epic.id, LEFT(active_epic.title, 2048),
		inference_assignment.delivery_plan_step_id, COALESCE(inference_step.step_key, e.step_key),
			e.provider, e.model, e.input_tokens, e.output_tokens, e.total_cost_micros, 'Inference call'::text, ''::text, '{}'::text
		FROM automation_executions e
		LEFT JOIN automation_tasks t ON t.id = e.automation_task_id
		LEFT JOIN delivery_plan_step_assignments inference_assignment ON inference_assignment.child_automation_task_id = e.automation_task_id
		LEFT JOIN delivery_plan_steps inference_step ON inference_step.id = inference_assignment.delivery_plan_step_id
		LEFT JOIN delivery_work_items w ON w.id = COALESCE(e.delivery_work_item_id, t.delivery_work_item_id)
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE e.agent_key = ?
		UNION ALL
		SELECT e.id, 'tool_call'::text, COALESCE(e.completed_at, e.created_at), e.created_at, e.automation_task_id, e.run_id::text, e.worker_id::text, e.machine_id::text, e.agent_instance_id,
			COALESCE(t.operation, ''), e.call_status, ''::text, ''::text, 0::integer, 0::bigint, e.agent_key, ''::text, ''::text, ''::text, ''::text, NULL::uuid,
			c.id, LEFT(c.name, 2048), p.id, LEFT(p.name, 2048), w.id, LEFT(w.title, 2048), active_epic.id, LEFT(active_epic.title, 2048),
		tool_assignment.delivery_plan_step_id, COALESCE(tool_step.step_key, e.step_key),
			e.provider, e.model, e.input_tokens, e.output_tokens, e.total_cost_micros, 'AI tool call'::text, e.tool, '{}'::text
		FROM automation_tool_executions e
		LEFT JOIN automation_tasks t ON t.id = e.automation_task_id
		LEFT JOIN delivery_plan_step_assignments tool_assignment ON tool_assignment.child_automation_task_id = e.automation_task_id
		LEFT JOIN delivery_plan_steps tool_step ON tool_step.id = tool_assignment.delivery_plan_step_id
		LEFT JOIN delivery_work_items w ON w.id = COALESCE(e.delivery_work_item_id, t.delivery_work_item_id)
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE e.agent_key = ?
		UNION ALL
		SELECT e.id, 'step_event'::text, e.occurred_at, e.created_at, e.automation_task_id, e.run_id::text, e.worker_id::text, e.machine_id::text, e.agent_instance_id,
			COALESCE(t.operation, ''), e.to_status, ''::text, ''::text, 0::integer, 0::bigint, e.agent_key, ''::text, ''::text, ''::text, ''::text, NULL::uuid,
			c.id, LEFT(c.name, 2048), p.id, LEFT(p.name, 2048), w.id, LEFT(w.title, 2048), active_epic.id, LEFT(active_epic.title, 2048),
		s.id, s.step_key,
			''::text, ''::text, 0::bigint, 0::bigint, 0::bigint, 'Plan step event'::text, ''::text, '{}'::text
		FROM delivery_plan_step_events e
		LEFT JOIN automation_tasks t ON t.id = e.automation_task_id
		LEFT JOIN delivery_plan_steps s ON s.id = e.step_id
		LEFT JOIN delivery_plans event_plan ON event_plan.id = e.plan_id
		LEFT JOIN delivery_work_items w ON w.id = COALESCE(event_plan.work_item_id, t.delivery_work_item_id)
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE e.agent_key = ?
		UNION ALL
		SELECT e.id, 'step_activity'::text, e.occurred_at, e.created_at, e.automation_task_id, e.run_id::text, e.worker_id::text, e.machine_id::text, e.agent_instance_id,
			COALESCE(t.operation, ''), e.phase, ''::text, ''::text, 0::integer, 0::bigint, e.agent_key, ''::text, ''::text, ''::text, ''::text, NULL::uuid,
			c.id, LEFT(c.name, 2048), p.id, LEFT(p.name, 2048), w.id, LEFT(w.title, 2048), active_epic.id, LEFT(active_epic.title, 2048),
			s.id, s.step_key,
			''::text, ''::text, 0::bigint, 0::bigint, 0::bigint, 'Plan step activity'::text, e.action, e.details_json::text
		FROM delivery_plan_step_activity_events e
		LEFT JOIN automation_tasks t ON t.id = e.automation_task_id
		LEFT JOIN delivery_plan_steps s ON s.id = e.step_id
		LEFT JOIN delivery_plans activity_plan ON activity_plan.id = e.plan_id
		LEFT JOIN delivery_work_items w ON w.id = COALESCE(activity_plan.work_item_id, t.delivery_work_item_id)
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE e.agent_key = (SELECT agent_key FROM requested_agent)
		UNION ALL
		SELECT e.id, 'assignment_event'::text, e.occurred_at, e.created_at, e.child_automation_task_id, ''::text, ''::text,
			e.target_machine_id, NULL::uuid, COALESCE(NULLIF(child_task.operation, ''), 'delivery.plan'), e.status,
			e.event_type, e.previous_status, 0::integer, 0::bigint, e.target_agent_key, ''::text, ''::text,
		e.previous_target_agent_key, e.previous_target_machine_id, NULL::uuid,
		c.id, LEFT(c.name, 2048), p.id, LEFT(p.name, 2048), w.id, LEFT(w.title, 2048), active_epic.id, LEFT(active_epic.title, 2048),
		s.id, s.step_key, ''::text, ''::text, 0::bigint, 0::bigint, 0::bigint,
		CASE e.event_type
			WHEN 'assignment_created' THEN 'Plan step assignment created'
			WHEN 'status_changed' THEN 'Plan step assignment status changed'
			ELSE 'Plan step assignment target changed'
		END::text, ''::text, '{}'::text
		FROM delivery_plan_step_assignment_events e
		JOIN delivery_plans assignment_plan ON assignment_plan.id = e.plan_id
		JOIN delivery_work_items w ON w.id = assignment_plan.work_item_id
		LEFT JOIN automation_tasks child_task ON child_task.id = e.child_automation_task_id
		LEFT JOIN delivery_plan_steps s ON s.id = e.step_id
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE (SELECT agent_key FROM requested_agent) = ''
		   OR e.target_agent_key = (SELECT agent_key FROM requested_agent)
		   OR e.previous_target_agent_key = (SELECT agent_key FROM requested_agent)
		UNION ALL
		SELECT g.id, 'gate_decision'::text, g.decided_at, g.created_at, COALESCE((SELECT gate_task.id FROM automation_tasks gate_task
			WHERE gate_task.delivery_work_item_id = w.id ORDER BY gate_task.updated_at DESC, gate_task.id DESC LIMIT 1),
			'00000000-0000-0000-0000-000000000000'::uuid), ''::text, ''::text, ''::text, NULL::uuid,
		'delivery.workflow'::text, g.decision, g.kind, ''::text, 0::integer, 0::bigint, ''::text,
		''::text, ''::text, ''::text, ''::text, NULL::uuid,
		c.id, LEFT(c.name, 2048), p.id, LEFT(p.name, 2048), w.id, LEFT(w.title, 2048), active_epic.id, LEFT(active_epic.title, 2048),
		NULL::uuid, ''::text, ''::text, ''::text, 0::bigint, 0::bigint, 0::bigint,
		'Human gate decision recorded'::text, ''::text, '{}'::text
		FROM delivery_gates g
		JOIN delivery_work_items w ON w.id = g.work_item_id
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE (SELECT agent_key FROM requested_agent) = ''
		   OR EXISTS (SELECT 1 FROM automation_tasks gate_task
			WHERE gate_task.delivery_work_item_id = w.id AND gate_task.agent_key = (SELECT agent_key FROM requested_agent))
		UNION ALL
		SELECT e.event_id, 'step_evidence'::text, e.created_at, e.created_at, e.automation_task_id, e.run_id::text, e.worker_id::text, e.machine_id,
			e.agent_instance_id, COALESCE(NULLIF(t.operation, ''), 'delivery.implementation'), 'recorded'::text, ''::text, ''::text,
			0::integer, 0::bigint, e.agent_key, ''::text, ''::text, ''::text, ''::text, NULL::uuid,
			c.id, LEFT(c.name, 2048), p.id, LEFT(p.name, 2048), w.id, LEFT(w.title, 2048), active_epic.id, LEFT(active_epic.title, 2048),
			s.id, s.step_key, ''::text, ''::text, 0::bigint, 0::bigint, 0::bigint,
			'Plan step evidence recorded'::text, ''::text, '{}'::text
		FROM delivery_plan_step_evidences e
		JOIN delivery_plans evidence_plan ON evidence_plan.id = e.plan_id
		JOIN delivery_work_items w ON w.id = evidence_plan.work_item_id
		LEFT JOIN automation_tasks t ON t.id = e.automation_task_id
		LEFT JOIN delivery_plan_steps s ON s.id = e.step_id
		LEFT JOIN delivery_projects p ON p.id = w.project_id
		LEFT JOIN clients c ON c.id = p.client_id
		LEFT JOIN LATERAL (
			SELECT epic.id, epic.title FROM delivery_epic_work_items epic_work_item
			JOIN delivery_epics epic ON epic.id = epic_work_item.epic_id
			WHERE epic_work_item.work_item_id = w.id AND epic_work_item.deleted_at IS NULL
			ORDER BY epic_work_item.created_at DESC, epic_work_item.id DESC LIMIT 1
		) active_epic ON TRUE
		WHERE (SELECT agent_key FROM requested_agent) = '' OR e.agent_key = (SELECT agent_key FROM requested_agent)
	)`
	query := timeline + ` SELECT id, kind, occurred_at, task_id, run_id, worker_id, machine_id, agent_instance_id, operation, status,
		event_type, previous_status, attempt_count, event_sequence, current_agent_key, previous_run_id, previous_worker_id, previous_agent_key, previous_machine_id, previous_agent_instance_id,
		client_id, client_name, project_id, project_name, work_item_id, work_item_title,
		epic_id, epic_title, step_id, step_key, provider, model, input_tokens, output_tokens,
		CASE kind WHEN 'inference' THEN COALESCE((SELECT execution.cached_input_tokens FROM automation_executions execution WHERE execution.id = activity.id), 0)
			WHEN 'tool_call' THEN COALESCE((SELECT tool_execution.cached_input_tokens FROM automation_tool_executions tool_execution WHERE tool_execution.id = activity.id), 0) ELSE 0 END AS cached_input_tokens,
		CASE kind WHEN 'inference' THEN COALESCE((SELECT execution.cache_write_tokens FROM automation_executions execution WHERE execution.id = activity.id), 0)
			WHEN 'tool_call' THEN COALESCE((SELECT tool_execution.cache_write_tokens FROM automation_tool_executions tool_execution WHERE tool_execution.id = activity.id), 0) ELSE 0 END AS cache_write_tokens,
		CASE kind WHEN 'inference' THEN COALESCE((SELECT GREATEST((EXTRACT(EPOCH FROM (execution.completed_at - execution.created_at)) * 1000)::bigint, 0) FROM automation_executions execution WHERE execution.id = activity.id), 0)
			WHEN 'tool_call' THEN COALESCE((SELECT GREATEST((EXTRACT(EPOCH FROM (tool_execution.completed_at - tool_execution.created_at)) * 1000)::bigint, 0) FROM automation_tool_executions tool_execution WHERE tool_execution.id = activity.id), 0) ELSE 0 END AS latency_millis,
		total_cost_micros,
		CASE kind WHEN 'inference' THEN COALESCE((SELECT execution.pricing_basis FROM automation_executions execution WHERE execution.id = activity.id), 'unpriced')
			WHEN 'tool_call' THEN COALESCE((SELECT tool_execution.pricing_basis FROM automation_tool_executions tool_execution WHERE tool_execution.id = activity.id), 'unpriced') ELSE '' END AS pricing_basis,
		CASE kind WHEN 'inference' THEN COALESCE((SELECT UPPER(BTRIM(execution.currency)) FROM automation_executions execution WHERE execution.id = activity.id), '')
			WHEN 'tool_call' THEN COALESCE((SELECT UPPER(BTRIM(tool_execution.currency)) FROM automation_tool_executions tool_execution WHERE tool_execution.id = activity.id), '') ELSE '' END AS currency,
		summary, activity_action, activity_details_json
		FROM activity WHERE TRUE`
	args := []any{agentKey, agentKey, agentKey, agentKey}
	if agentKey == "" {
		// This path powers the global trace explorer. Keep the UNION as one
		// database query, but remove each per-profile predicate so the database
		// globally orders every source before applying its keyset cursor.
		query = strings.ReplaceAll(query, "WHERE t.agent_key = (SELECT agent_key FROM requested_agent)", "WHERE TRUE")
		query = strings.ReplaceAll(query, "WHERE e.agent_key = (SELECT agent_key FROM requested_agent)\n\t\t   OR e.previous_agent_key = (SELECT agent_key FROM requested_agent)", "WHERE TRUE")
		query = strings.ReplaceAll(query, "WHERE e.agent_key = ?", "WHERE TRUE")
		query = strings.ReplaceAll(query, "WHERE e.agent_key = (SELECT agent_key FROM requested_agent)", "WHERE TRUE")
		// The task-branch CTE retains its positional bind even though its value
		// is no longer a restriction in global mode.
		args = []any{""}
	}
	appendFilter := func(clause string, value any) {
		query += " AND " + clause + " ?"
		args = append(args, value)
	}
	if filters.From != nil {
		appendFilter("occurred_at >=", *filters.From)
	}
	if filters.To != nil {
		appendFilter("occurred_at <", *filters.To)
	}
	appendFilter("occurred_at <=", *filters.SnapshotAt)
	// Event occurred_at is operational chronology and can be backfilled. Also
	// bound by its persistence time so rows ingested after page one cannot drift
	// into a later keyset page merely because their event timestamp is old.
	appendFilter("recorded_at <=", *filters.SnapshotAt)
	if filters.OrganizationID != nil {
		query += ` AND client_id IN (
			WITH RECURSIVE organization_clients(id) AS (
				SELECT clients.id FROM clients WHERE clients.id = ? AND clients.deleted_at IS NULL
				UNION
				SELECT child.id FROM clients AS child
				JOIN organization_clients AS parent ON child.parent_id = parent.id
				WHERE child.deleted_at IS NULL
			)
			SELECT id FROM organization_clients
		)`
		args = append(args, *filters.OrganizationID)
	}
	if filters.ClientID != nil {
		if len(filters.ClientScopeIDs) > 0 {
			appendFilter("client_id IN", filters.ClientScopeIDs)
		} else {
			appendFilter("client_id =", *filters.ClientID)
		}
	}
	if filters.ProjectID != nil {
		appendFilter("project_id =", *filters.ProjectID)
	}
	if filters.AuthorizedProjectsScoped {
		if len(filters.AuthorizedProjectIDs) == 0 {
			query += " AND FALSE"
		} else {
			appendFilter("project_id IN", filters.AuthorizedProjectIDs)
		}
	}
	if filters.EpicID != nil {
		appendFilter("epic_id =", *filters.EpicID)
	}
	if filters.WorkItemID != nil {
		appendFilter("work_item_id =", *filters.WorkItemID)
	}
	if filters.StepID != nil {
		appendFilter("step_id =", *filters.StepID)
	}
	if filters.StepKey != "" {
		appendFilter("step_key =", filters.StepKey)
	}
	if filters.WorkerID != nil {
		query += " AND (worker_id = ? OR (kind = 'task_event' AND previous_worker_id = ?))"
		args = append(args, filters.WorkerID.String(), filters.WorkerID.String())
	}
	if filters.MachineID != nil {
		query += " AND (machine_id = ? OR (kind = 'task_event' AND previous_machine_id = ?))"
		args = append(args, filters.MachineID.String(), filters.MachineID.String())
	}
	if filters.AgentInstanceID != nil {
		query += " AND (agent_instance_id = ? OR (kind = 'task_event' AND previous_agent_instance_id = ?))"
		args = append(args, *filters.AgentInstanceID, *filters.AgentInstanceID)
	}
	if filters.AgentKey != "" {
		query += " AND (current_agent_key = ? OR (kind = 'task_event' AND previous_agent_key = ?))"
		args = append(args, filters.AgentKey, filters.AgentKey)
	}
	if filters.RunID != "" {
		query += " AND (run_id = ? OR (kind = 'task_event' AND previous_run_id = ?))"
		args = append(args, filters.RunID, filters.RunID)
	}
	if filters.Operation != "" {
		appendFilter("operation =", filters.Operation)
	}
	if filters.Tool != "" {
		query += " AND kind = 'tool_call' AND activity_action = ?"
		args = append(args, filters.Tool)
	}
	if filters.Status != "" {
		appendFilter("status =", filters.Status)
	}
	if filters.Provider != "" {
		appendFilter("provider =", filters.Provider)
	}
	if filters.Model != "" {
		appendFilter("model =", filters.Model)
	}
	if filters.Search != "" {
		query += ` AND strpos(lower(concat_ws(' ', kind, event_type, operation, status, current_agent_key, previous_agent_key,
			machine_id, previous_machine_id, provider, model, summary, client_name, project_name, epic_title, work_item_title,
			step_key, activity_action)), lower(?)) > 0`
		args = append(args, filters.Search)
	}
	if filters.Cursor != nil {
		query += " AND (occurred_at, id, kind) < (?, ?, ?)"
		args = append(args, filters.Cursor.OccurredAt, filters.Cursor.ID, filters.Cursor.Kind)
	}
	query += " ORDER BY occurred_at DESC, id DESC, kind DESC LIMIT ?"
	args = append(args, filters.Limit+1)
	var rows []agentHistoryItem
	if err := db.WithContext(c.Request().Context()).Raw(query, args...).Scan(&rows).Error; err != nil {
		return agentHistoryResponse{}, err
	}
	response := agentHistoryResponse{
		AgentKey: agentKey, Limit: filters.Limit, SnapshotAt: *filters.SnapshotAt,
		CostCoverage: agentHistoryCostCoverage{Scope: "returned_page"},
		Items:        make([]agentHistoryItem, 0, min(len(rows), filters.Limit)),
	}
	if len(rows) > filters.Limit {
		response.HasMore = true
		rows = rows[:filters.Limit]
	}
	for _, row := range rows {
		if agentProfileKeyPattern.MatchString(strings.TrimSpace(row.CurrentAgentKey)) {
			row.AgentKey = strings.TrimSpace(row.CurrentAgentKey)
		}
		if row.Kind == "tool_call" {
			row.Tool = safeAgentHistoryTool(row.ActivityAction)
		}
		row.Operation = safeAgentHistoryOperation(row.Operation)
		row.Status = safeAgentHistoryStatus(row.Kind, row.Status)
		switch row.Kind {
		case "task_event":
			row.EventType = safeAgentHistoryTaskEventType(row.EventType)
			if !agentProfileKeyPattern.MatchString(strings.TrimSpace(row.CurrentAgentKey)) {
				row.CurrentAgentKey = ""
			}
			if row.PreviousStatus != "" {
				row.PreviousStatus = safeAgentHistoryStatus("task_event", row.PreviousStatus)
			}
			if row.AttemptCount == nil {
				attemptCount := 0
				row.AttemptCount = &attemptCount
			} else if *row.AttemptCount < 0 {
				*row.AttemptCount = 0
			}
			if row.EventSequence < 0 {
				row.EventSequence = 0
			}
			row.PreviousRunID = safeAgentHistoryRunID(row.PreviousRunID)
			row.PreviousWorkerID = safeAgentHistoryUUID(row.PreviousWorkerID)
			row.PreviousMachineID = safeAgentHistoryUUID(row.PreviousMachineID)
			if !agentProfileKeyPattern.MatchString(strings.TrimSpace(row.PreviousAgentKey)) {
				row.PreviousAgentKey = ""
			}
			if row.AgentKey == "" {
				row.AgentKey = row.PreviousAgentKey
			}
			if row.PreviousAgentInstanceID != nil && *row.PreviousAgentInstanceID == uuid.Nil {
				row.PreviousAgentInstanceID = nil
			}
		case "assignment_event":
			row.EventType = safeAgentHistoryAssignmentEventType(row.EventType)
			if row.PreviousStatus != "" {
				row.PreviousStatus = safeAgentHistoryStatus("assignment_event", row.PreviousStatus)
			}
			if !agentProfileKeyPattern.MatchString(strings.TrimSpace(row.CurrentAgentKey)) {
				row.CurrentAgentKey = ""
			}
			if !agentProfileKeyPattern.MatchString(strings.TrimSpace(row.PreviousAgentKey)) {
				row.PreviousAgentKey = ""
			}
			row.PreviousMachineID = safeAgentHistoryUUID(row.PreviousMachineID)
			if row.AgentKey == "" {
				row.AgentKey = row.PreviousAgentKey
			}
			row.AttemptCount = nil
			row.EventSequence = 0
			row.PreviousRunID = ""
			row.PreviousWorkerID = ""
			row.PreviousAgentInstanceID = nil
		case "gate_decision":
			row.EventType = safeAgentHistoryGateKind(row.EventType)
			row.AttemptCount = nil
			row.EventSequence = 0
			row.CurrentAgentKey = ""
			row.PreviousRunID = ""
			row.PreviousWorkerID = ""
			row.PreviousAgentKey = ""
			row.PreviousMachineID = ""
			row.PreviousAgentInstanceID = nil
		default:
			legacyAttemptCount := int64(0)
			if row.AttemptCount != nil {
				legacyAttemptCount = int64(*row.AttemptCount)
			}
			legacyAgentKey := row.CurrentAgentKey
			row.EventType = ""
			row.PreviousStatus = ""
			row.AttemptCount = nil
			row.EventSequence = 0
			row.CurrentAgentKey = ""
			row.PreviousRunID = ""
			row.PreviousWorkerID = ""
			row.PreviousAgentKey = ""
			row.PreviousMachineID = ""
			row.PreviousAgentInstanceID = nil
			if row.Kind == "task" {
				row.EventType = "legacy_snapshot"
				if agentProfileKeyPattern.MatchString(strings.TrimSpace(legacyAgentKey)) {
					row.CurrentAgentKey = strings.TrimSpace(legacyAgentKey)
				}
				attemptCount := int(nonnegativeAgentHistoryCount(legacyAttemptCount))
				row.AttemptCount = &attemptCount
			}
		}
		if row.Kind == "step_activity" {
			row.ActivityAction = safeAgentHistoryActivityAction(row.ActivityAction)
			if details, err := parsePlanStepActivityDetailsJSON(row.ActivityDetailsJSON, row.ActivityAction, safeAgentHistoryActivityPhase(row.Status)); err == nil {
				row.ActivityDetails = details
			}
		} else {
			row.ActivityAction = ""
		}
		row.ActivityDetailsJSON = ""
		row.RunID = safeAgentHistoryRunID(row.RunID)
		row.WorkerID = safeAgentHistoryUUID(row.WorkerID)
		row.MachineID = safeAgentHistoryUUID(row.MachineID)
		if row.AgentInstanceID != nil && *row.AgentInstanceID == uuid.Nil {
			row.AgentInstanceID = nil
		}
		if row.EpicID != nil && *row.EpicID == uuid.Nil {
			row.EpicID = nil
		}
		if row.StepID != nil && *row.StepID == uuid.Nil {
			row.StepID = nil
		}
		row.StepKey = safeAgentHistoryStepKey(row.StepKey)
		row.ClientName = redactAgentHistoryText(row.ClientName, 160)
		row.ProjectName = redactAgentHistoryText(row.ProjectName, 160)
		row.EpicTitle = redactAgentHistoryText(row.EpicTitle, 160)
		row.WorkItemTitle = redactAgentHistoryText(row.WorkItemTitle, 512)
		row.Provider = safeAgentHistoryProvider(row.Provider)
		row.Model = redactAgentHistoryText(row.Model, 128)
		row.Summary = redactAgentHistoryText(row.Summary, 512)
		normalizeAgentHistoryUsage(&row)
		switch row.CostPricingStatus {
		case "verified_usd":
			response.CostCoverage.VerifiedUSDExecutions++
		case "unknown":
			response.CostCoverage.UnpricedExecutions++
		}
		response.Items = append(response.Items, row)
	}
	if response.HasMore && len(response.Items) > 0 {
		last := response.Items[len(response.Items)-1]
		response.NextCursor = encodeAgentHistoryCursor(agentHistoryCursor{
			Version: 2, Scope: agentHistoryCursorScope(agentKey, filters), SnapshotAt: *filters.SnapshotAt,
			OccurredAt: last.OccurredAt, ID: last.ID, Kind: last.Kind,
		})
	}
	return response, nil
}

func safeAgentHistoryActivityAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "inference", "tool", "file_read", "file_change", "command", "validation", "evidence":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "activity"
	}
}

func safeAgentHistoryActivityPhase(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "started", "completed", "failed":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "recorded"
	}
}

func safeAgentHistoryOperation(value string) string {
	value = strings.TrimSpace(value)
	if _, ok := allowedOperations[value]; ok {
		return value
	}
	return "unknown"
}

func safeAgentHistoryStatus(kind, value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if kind == "step_activity" {
		return safeAgentHistoryActivityPhase(value)
	}
	if kind == "gate_decision" {
		if value == "approved" || value == "changes_requested" {
			return value
		}
		return "recorded"
	}
	if _, ok := agentHistoryStatuses[value]; ok {
		return value
	}
	return "recorded"
}

func safeAgentHistoryAssignmentEventType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case models.DeliveryPlanStepAssignmentEventCreated, models.DeliveryPlanStepAssignmentEventStatusChanged,
		models.DeliveryPlanStepAssignmentEventTargetChanged, models.DeliveryPlanStepAssignmentEventStatusAndTargetChanged:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "recorded"
	}
}

func safeAgentHistoryGateKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "plan", "code_review", "qa", "release":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "recorded"
	}
}

func safeAgentHistoryTaskEventType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "created", "claimed", "status_transition", "lease_reclaimed", "assignment_changed", "attempt_updated":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "recorded"
	}
}

func safeAgentHistoryRunID(value string) string {
	value = strings.TrimSpace(value)
	if !agentHistoryRunPattern.MatchString(value) {
		return ""
	}
	return redactAgentHistoryText(value, 64)
}

func safeAgentHistoryUUID(value string) string {
	parsed, err := uuid.FromString(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil {
		return ""
	}
	return parsed.String()
}

func safeAgentHistoryStepKey(value string) string {
	value = strings.TrimSpace(value)
	if !agentHistoryStepKeyPattern.MatchString(value) {
		return ""
	}
	return redactAgentHistoryText(value, 64)
}

func safeAgentHistoryTool(value string) string {
	value = strings.TrimSpace(value)
	if !agentHistoryToolSupported(value) {
		return ""
	}
	return value
}

func agentHistoryToolSupported(value string) bool {
	_, ok := agentHistoryTools[strings.ToLower(strings.TrimSpace(value))]
	return ok
}

func agentHistoryProviderSupported(value string) bool {
	for _, provider := range cataloguedProviders {
		if strings.EqualFold(string(provider), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func safeAgentHistoryProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !agentHistoryProviderSupported(value) {
		return ""
	}
	return value
}

func nonnegativeAgentHistoryCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func nonnegativeAgentHistoryCountPointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	count := nonnegativeAgentHistoryCount(*value)
	return &count
}

func normalizeAgentHistoryUsage(row *agentHistoryItem) {
	if row == nil {
		return
	}
	row.InputTokens = nonnegativeAgentHistoryCountPointer(row.InputTokens)
	row.OutputTokens = nonnegativeAgentHistoryCountPointer(row.OutputTokens)
	row.CachedInputTokens = nonnegativeAgentHistoryCountPointer(row.CachedInputTokens)
	row.CacheWriteTokens = nonnegativeAgentHistoryCountPointer(row.CacheWriteTokens)
	row.LatencyMillis = nonnegativeAgentHistoryCountPointer(row.LatencyMillis)
	row.TotalCostMicros = nonnegativeAgentHistoryCountPointer(row.TotalCostMicros)
	row.Currency = strings.ToUpper(strings.TrimSpace(row.Currency))
	row.PricingBasis = strings.ToLower(strings.TrimSpace(row.PricingBasis))
	if row.Kind != "inference" && row.Kind != "tool_call" {
		row.InputTokens = nil
		row.OutputTokens = nil
		row.CachedInputTokens = nil
		row.CacheWriteTokens = nil
		row.LatencyMillis = nil
		row.TotalCostMicros = nil
		return
	}
	if row.Currency != "USD" || row.PricingBasis == "" || row.PricingBasis == "legacy" || row.PricingBasis == "unpriced" {
		row.TotalCostMicros = nil
		row.CostPricingStatus = "unknown"
		return
	}
	row.CostPricingStatus = "verified_usd"
}

func redactAgentHistoryText(value string, maxRunes int) string {
	safe, _ := automationagent.RedactSourceExcerpt(strings.TrimSpace(value))
	if maxRunes > 0 {
		runes := []rune(safe)
		if len(runes) > maxRunes {
			safe = string(runes[:maxRunes])
		}
	}
	return safe
}
