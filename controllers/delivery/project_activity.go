package delivery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const (
	defaultProjectActivityPageSize = 50
	maxProjectActivityPageSize     = 100
	maxProjectActivityCursorBytes  = 2048
)

type deliveryProjectActivityFilters struct {
	EpicID          *uuid.UUID
	WorkItemID      *uuid.UUID
	AgentKey        string
	AgentInstanceID *uuid.UUID
	WorkerID        *uuid.UUID
	MachineID       *uuid.UUID
	RunID           *uuid.UUID
	Kind            string
	Action          string
	Status          string
	From            *time.Time
	Until           *time.Time
}

type deliveryProjectActivityCursor struct {
	Version    int       `json:"v"`
	Scope      string    `json:"s"`
	OccurredAt time.Time `json:"t"`
	EventID    string    `json:"i"`
	EventKind  string    `json:"k"`
}

type deliveryProjectActivityRow struct {
	EventID             uuid.UUID  `gorm:"column:event_id"`
	EventKind           string     `gorm:"column:event_kind"`
	ClientID            uuid.UUID  `gorm:"column:client_id"`
	ProjectID           uuid.UUID  `gorm:"column:project_id"`
	EpicID              *uuid.UUID `gorm:"column:epic_id"`
	WorkItemID          uuid.UUID  `gorm:"column:work_item_id"`
	PlanID              uuid.UUID  `gorm:"column:plan_id"`
	PlanVersion         int        `gorm:"column:plan_version"`
	StepID              uuid.UUID  `gorm:"column:step_id"`
	AutomationTaskID    uuid.UUID  `gorm:"column:automation_task_id"`
	RunID               string     `gorm:"column:run_id"`
	WorkerID            string     `gorm:"column:worker_id"`
	AgentKey            string     `gorm:"column:agent_key"`
	MachineID           string     `gorm:"column:machine_id"`
	AgentInstanceID     *uuid.UUID `gorm:"column:agent_instance_id"`
	EventType           string     `gorm:"column:event_type"`
	Action              string     `gorm:"column:action"`
	Phase               string     `gorm:"column:phase"`
	ToolName            string     `gorm:"column:tool_name"`
	FromStatus          string     `gorm:"column:from_status"`
	ToStatus            string     `gorm:"column:to_status"`
	TargetAgentKey      string     `gorm:"column:target_agent_key"`
	PreviousTargetAgent string     `gorm:"column:previous_target_agent_key"`
	TargetMachineID     string     `gorm:"column:target_machine_id"`
	PreviousMachineID   string     `gorm:"column:previous_target_machine_id"`
	OccurredAt          time.Time  `gorm:"column:occurred_at"`
}

type deliveryProjectActivityItem struct {
	ID                      uuid.UUID  `json:"id"`
	Kind                    string     `json:"kind"`
	EventType               string     `json:"event_type"`
	Action                  string     `json:"action,omitempty"`
	Phase                   string     `json:"phase,omitempty"`
	ToolName                string     `json:"tool_name,omitempty"`
	FromStatus              string     `json:"from_status,omitempty"`
	ToStatus                string     `json:"to_status,omitempty"`
	Summary                 string     `json:"summary"`
	ClientID                uuid.UUID  `json:"client_id"`
	ProjectID               uuid.UUID  `json:"project_id"`
	EpicID                  *uuid.UUID `json:"epic_id,omitempty"`
	WorkItemID              uuid.UUID  `json:"work_item_id"`
	PlanID                  uuid.UUID  `json:"plan_id"`
	PlanVersion             int        `json:"plan_version"`
	StepID                  uuid.UUID  `json:"step_id"`
	AutomationTaskID        uuid.UUID  `json:"automation_task_id"`
	RunID                   string     `json:"run_id,omitempty"`
	WorkerID                string     `json:"worker_id,omitempty"`
	AgentKey                string     `json:"agent_key,omitempty"`
	MachineID               string     `json:"machine_id,omitempty"`
	AgentInstanceID         *uuid.UUID `json:"agent_instance_id,omitempty"`
	TargetAgentKey          string     `json:"target_agent_key,omitempty"`
	PreviousTargetAgentKey  string     `json:"previous_target_agent_key,omitempty"`
	TargetMachineID         string     `json:"target_machine_id,omitempty"`
	PreviousTargetMachineID string     `json:"previous_target_machine_id,omitempty"`
	OccurredAt              time.Time  `json:"occurred_at"`
}

type deliveryProjectActivityPage struct {
	ProjectID  uuid.UUID                     `json:"project_id"`
	Items      []deliveryProjectActivityItem `json:"items"`
	NextCursor string                        `json:"next_cursor"`
}

// ListProjectActivity provides a project-scoped, cursor-paged timeline over
// durable step lifecycle, assignment, and worker activity events. The
// projection is allow-listed and intentionally excludes prompts, model output,
// command text/arguments, file contents, and arbitrary event JSON.
func ListProjectActivity(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryView)
	if err != nil {
		return err
	}
	if actor == nil {
		return nil
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	limit, err := projectActivityPageSize(c.QueryParam("limit"))
	if err != nil {
		return badRequest(c, "Invalid project activity page size", err.Error())
	}
	filters, err := parseProjectActivityFilters(c)
	if err != nil {
		return badRequest(c, "Invalid project activity filter", err.Error())
	}
	workspaceMode, _ := c.Get("workspace_mode").(string)
	organizationID, _ := c.Get("organization_id").(uuid.UUID)
	scope := projectActivityCursorScope(projectID, actor.CognitoSub, workspaceMode, organizationID, filters)
	cursor, err := decodeProjectActivityCursor(c.QueryParam("cursor"), scope)
	if err != nil {
		return badRequest(c, "Invalid project activity cursor", err.Error())
	}

	query, args := buildProjectActivityQuery(projectID, filters, cursor, limit+1)
	var rows []deliveryProjectActivityRow
	if err := configuration.DB.Raw(query, args...).Scan(&rows).Error; err != nil {
		return utilsError(c, err)
	}
	page := deliveryProjectActivityPage{ProjectID: projectID, Items: make([]deliveryProjectActivityItem, 0, limit)}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodeProjectActivityCursor(deliveryProjectActivityCursor{
			Version: 1, Scope: scope, OccurredAt: last.OccurredAt.UTC(), EventID: last.EventID.String(), EventKind: last.EventKind,
		})
	}
	for _, row := range rows {
		page.Items = append(page.Items, safeProjectActivityItem(row))
	}
	return success(c, "Delivery project activity", page)
}

func parseProjectActivityFilters(c echo.Context) (deliveryProjectActivityFilters, error) {
	var filters deliveryProjectActivityFilters
	for _, entry := range []struct {
		name   string
		target **uuid.UUID
	}{
		{name: "epic_id", target: &filters.EpicID},
		{name: "work_item_id", target: &filters.WorkItemID},
		{name: "agent_instance_id", target: &filters.AgentInstanceID},
		{name: "worker_id", target: &filters.WorkerID},
		{name: "machine_id", target: &filters.MachineID},
		{name: "run_id", target: &filters.RunID},
	} {
		raw := strings.TrimSpace(c.QueryParam(entry.name))
		if raw == "" {
			continue
		}
		parsed, err := uuid.FromString(raw)
		if err != nil || parsed == uuid.Nil {
			return filters, errors.New(entry.name + " must be a non-zero UUID")
		}
		*entry.target = &parsed
	}
	filters.AgentKey = strings.TrimSpace(c.QueryParam("agent_key"))
	if filters.AgentKey != "" && safeAgentEventKey(filters.AgentKey) != filters.AgentKey {
		return filters, errors.New("agent_key is invalid")
	}
	filters.Kind = strings.ToLower(strings.TrimSpace(c.QueryParam("kind")))
	if filters.Kind != "" && filters.Kind != "step" && filters.Kind != "assignment" && filters.Kind != "activity" {
		return filters, errors.New("kind is not supported")
	}
	filters.Action = strings.ToLower(strings.TrimSpace(c.QueryParam("action")))
	if filters.Action != "" && !validProjectActivityAction(filters.Action) {
		return filters, errors.New("action is not supported")
	}
	filters.Status = strings.ToLower(strings.TrimSpace(c.QueryParam("status")))
	if filters.Status != "" && !validProjectActivityStatus(filters.Status) {
		return filters, errors.New("status is not supported")
	}
	for _, entry := range []struct {
		name   string
		target **time.Time
	}{
		{name: "from", target: &filters.From},
		{name: "to", target: &filters.Until},
	} {
		raw := strings.TrimSpace(c.QueryParam(entry.name))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return filters, errors.New(entry.name + " must be RFC3339")
		}
		parsed = parsed.UTC()
		*entry.target = &parsed
	}
	if filters.From != nil && filters.Until != nil && !filters.From.Before(*filters.Until) {
		return filters, errors.New("from must be earlier than to")
	}
	return filters, nil
}

func validProjectActivityAction(value string) bool {
	switch value {
	case models.DeliveryPlanStepActivityInference, models.DeliveryPlanStepActivityTool,
		models.DeliveryPlanStepActivityFileRead, models.DeliveryPlanStepActivityFileChange,
		models.DeliveryPlanStepActivityCommand, models.DeliveryPlanStepActivityValidation,
		models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepEventReady,
		models.DeliveryPlanStepEventClaimed, models.DeliveryPlanStepEventLeaseReclaimed,
		models.DeliveryPlanStepEventLeaseRenewed, models.DeliveryPlanStepEventTransitioned,
		models.DeliveryPlanStepAssignmentEventCreated, models.DeliveryPlanStepAssignmentEventStatusChanged,
		models.DeliveryPlanStepAssignmentEventTargetChanged, models.DeliveryPlanStepAssignmentEventStatusAndTargetChanged:
		return true
	default:
		return false
	}
}

func validProjectActivityStatus(value string) bool {
	return safeDeliveryPlanStepStatus(value) != "" || validDeliveryPlanStepAssignmentStatus(value) ||
		value == models.DeliveryPlanStepActivityStarted || value == models.DeliveryPlanStepActivityCompleted || value == models.DeliveryPlanStepActivityFailed
}

func projectActivityPageSize(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultProjectActivityPageSize, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 1 || limit > maxProjectActivityPageSize {
		return 0, errors.New("limit must be an integer between 1 and 100")
	}
	return limit, nil
}

func projectActivityCursorScope(projectID uuid.UUID, actorSub, workspaceMode string, organizationID uuid.UUID, filters deliveryProjectActivityFilters) string {
	identity := struct {
		ProjectID       string     `json:"project_id"`
		ActorSub        string     `json:"actor_sub"`
		WorkspaceMode   string     `json:"workspace_mode"`
		OrganizationID  string     `json:"organization_id,omitempty"`
		EpicID          string     `json:"epic_id,omitempty"`
		WorkItemID      string     `json:"work_item_id,omitempty"`
		AgentKey        string     `json:"agent_key,omitempty"`
		AgentInstanceID string     `json:"agent_instance_id,omitempty"`
		WorkerID        string     `json:"worker_id,omitempty"`
		MachineID       string     `json:"machine_id,omitempty"`
		RunID           string     `json:"run_id,omitempty"`
		Kind            string     `json:"kind,omitempty"`
		Action          string     `json:"action,omitempty"`
		Status          string     `json:"status,omitempty"`
		From            *time.Time `json:"from,omitempty"`
		Until           *time.Time `json:"until,omitempty"`
	}{
		ProjectID: projectID.String(), ActorSub: actorSub,
		WorkspaceMode: strings.ToLower(strings.TrimSpace(workspaceMode)), AgentKey: filters.AgentKey,
		Kind: filters.Kind, Action: filters.Action, Status: filters.Status, From: filters.From, Until: filters.Until,
	}
	if organizationID != uuid.Nil {
		identity.OrganizationID = organizationID.String()
	}
	if filters.EpicID != nil {
		identity.EpicID = filters.EpicID.String()
	}
	if filters.WorkItemID != nil {
		identity.WorkItemID = filters.WorkItemID.String()
	}
	if filters.AgentInstanceID != nil {
		identity.AgentInstanceID = filters.AgentInstanceID.String()
	}
	if filters.WorkerID != nil {
		identity.WorkerID = filters.WorkerID.String()
	}
	if filters.MachineID != nil {
		identity.MachineID = filters.MachineID.String()
	}
	if filters.RunID != nil {
		identity.RunID = filters.RunID.String()
	}
	encoded, _ := json.Marshal(identity)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func encodeProjectActivityCursor(cursor deliveryProjectActivityCursor) string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeProjectActivityCursor(raw, scope string) (*deliveryProjectActivityCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxProjectActivityCursorBytes {
		return nil, errors.New("cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("cursor encoding is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var cursor deliveryProjectActivityCursor
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Scope != scope || cursor.OccurredAt.IsZero() {
		return nil, errors.New("cursor is invalid or belongs to another project or filter set")
	}
	id, err := uuid.FromString(cursor.EventID)
	if err != nil || id == uuid.Nil || id.String() != cursor.EventID || (cursor.EventKind != "step" && cursor.EventKind != "assignment" && cursor.EventKind != "activity") {
		return nil, errors.New("cursor event identity is invalid")
	}
	cursor.OccurredAt = cursor.OccurredAt.UTC()
	return &cursor, nil
}

func buildProjectActivityQuery(projectID uuid.UUID, filters deliveryProjectActivityFilters, cursor *deliveryProjectActivityCursor, limit int) (string, []any) {
	query := `WITH project_activity_events AS (
		SELECT e.id::uuid AS event_id, 'step'::text AS event_kind, e.plan_id::uuid, e.step_id::uuid, e.automation_task_id::uuid,
			e.run_id::text, e.worker_id::text, e.agent_key::text, e.machine_id::text, e.agent_instance_id::uuid, e.event_type::text AS event_type,
			''::text AS action, ''::text AS phase, ''::text AS tool_name, e.from_status::text, e.to_status::text,
			''::text AS target_agent_key, ''::text AS previous_target_agent_key,
			''::text AS target_machine_id, ''::text AS previous_target_machine_id, e.occurred_at::timestamptz
		FROM delivery_plan_step_events e
		UNION ALL
		SELECT e.id::uuid AS event_id, 'assignment'::text AS event_kind, e.plan_id::uuid, e.step_id::uuid, e.child_automation_task_id::uuid AS automation_task_id,
			''::text AS run_id, ''::text AS worker_id, e.target_agent_key::text AS agent_key, e.target_machine_id::text AS machine_id,
			NULL::uuid AS agent_instance_id, e.event_type::text AS event_type, ''::text AS action, ''::text AS phase, ''::text AS tool_name,
			e.previous_status::text AS from_status, e.status::text AS to_status, e.target_agent_key::text, e.previous_target_agent_key::text,
			e.target_machine_id::text, e.previous_target_machine_id::text, e.occurred_at::timestamptz
		FROM delivery_plan_step_assignment_events e
		UNION ALL
		SELECT e.id::uuid AS event_id, 'activity'::text AS event_kind, e.plan_id::uuid, e.step_id::uuid, e.automation_task_id::uuid,
			e.run_id::text, e.worker_id::text, e.agent_key::text, e.machine_id::text, e.agent_instance_id::uuid, e.action::text AS event_type,
			e.action::text AS action, e.phase::text AS phase, e.tool_name::text AS tool_name,
			''::text AS from_status, ''::text AS to_status, ''::text AS target_agent_key, ''::text AS previous_target_agent_key,
			''::text AS target_machine_id, ''::text AS previous_target_machine_id, e.occurred_at::timestamptz
		FROM delivery_plan_step_activity_events e
	)
	SELECT e.event_id, e.event_kind, p.client_id, p.id AS project_id, event_epic.epic_id,
		w.id AS work_item_id, plan.id AS plan_id, plan.version AS plan_version, step.id AS step_id,
		e.automation_task_id, e.run_id, e.worker_id, e.agent_key, e.machine_id, e.agent_instance_id,
		e.event_type, e.action, e.phase, e.tool_name, e.from_status, e.to_status,
		e.target_agent_key, e.previous_target_agent_key, e.target_machine_id, e.previous_target_machine_id, e.occurred_at
	FROM project_activity_events e
	JOIN delivery_plans plan ON plan.id = e.plan_id
	JOIN delivery_work_items w ON w.id = plan.work_item_id AND w.deleted_at IS NULL
	JOIN delivery_projects p ON p.id = w.project_id AND p.deleted_at IS NULL
	JOIN delivery_plan_steps step ON step.id = e.step_id AND step.plan_id = plan.id
	LEFT JOIN LATERAL (
		SELECT membership.epic_id
		FROM delivery_epic_work_items membership
		WHERE membership.work_item_id = w.id AND membership.project_id = p.id
			AND membership.created_at <= e.occurred_at
			AND (membership.deleted_at IS NULL OR membership.deleted_at > e.occurred_at)
		ORDER BY membership.created_at DESC, membership.id DESC
		LIMIT 1
	) event_epic ON TRUE
	WHERE p.id = ?`
	args := []any{projectID}
	if filters.EpicID != nil {
		query += ` AND event_epic.epic_id = ?`
		args = append(args, *filters.EpicID)
	}
	if filters.WorkItemID != nil {
		query += ` AND w.id = ?`
		args = append(args, *filters.WorkItemID)
	}
	if filters.AgentKey != "" {
		query += ` AND (e.agent_key = ? OR e.target_agent_key = ? OR e.previous_target_agent_key = ?)`
		args = append(args, filters.AgentKey, filters.AgentKey, filters.AgentKey)
	}
	if filters.AgentInstanceID != nil {
		query += ` AND e.agent_instance_id = ?`
		args = append(args, *filters.AgentInstanceID)
	}
	if filters.WorkerID != nil {
		query += ` AND e.worker_id = ?`
		args = append(args, filters.WorkerID.String())
	}
	if filters.MachineID != nil {
		query += ` AND (e.machine_id = ? OR e.target_machine_id = ? OR e.previous_target_machine_id = ?)`
		machineID := filters.MachineID.String()
		args = append(args, machineID, machineID, machineID)
	}
	if filters.RunID != nil {
		query += ` AND e.run_id = ?`
		args = append(args, filters.RunID.String())
	}
	if filters.Kind != "" {
		query += ` AND e.event_kind = ?`
		args = append(args, filters.Kind)
	}
	if filters.Action != "" {
		query += ` AND e.event_type = ?`
		args = append(args, filters.Action)
	}
	if filters.Status != "" {
		query += ` AND (e.from_status = ? OR e.to_status = ? OR e.phase = ?)`
		args = append(args, filters.Status, filters.Status, filters.Status)
	}
	if filters.From != nil {
		query += ` AND e.occurred_at >= ?`
		args = append(args, *filters.From)
	}
	if filters.Until != nil {
		query += ` AND e.occurred_at < ?`
		args = append(args, *filters.Until)
	}
	if cursor != nil {
		id, _ := uuid.FromString(cursor.EventID)
		query += ` AND (e.occurred_at < ? OR (e.occurred_at = ? AND (e.event_id < ? OR (e.event_id = ? AND e.event_kind < ?))))`
		args = append(args, cursor.OccurredAt, cursor.OccurredAt, id, id, cursor.EventKind)
	}
	query += ` ORDER BY e.occurred_at DESC, e.event_id DESC, e.event_kind DESC LIMIT ?`
	args = append(args, limit)
	return query, args
}

func safeProjectActivityItem(row deliveryProjectActivityRow) deliveryProjectActivityItem {
	item := deliveryProjectActivityItem{
		ID: row.EventID, Kind: row.EventKind, ClientID: row.ClientID, ProjectID: row.ProjectID,
		EpicID: row.EpicID, WorkItemID: row.WorkItemID, PlanID: row.PlanID, PlanVersion: row.PlanVersion,
		StepID: row.StepID, AutomationTaskID: row.AutomationTaskID,
		RunID: safeOpaqueEventID(row.RunID), WorkerID: safeOpaqueEventID(row.WorkerID),
		AgentKey: safeAgentEventKey(row.AgentKey), MachineID: safeOpaqueEventID(row.MachineID),
		AgentInstanceID: row.AgentInstanceID, TargetAgentKey: safeAgentEventKey(row.TargetAgentKey),
		PreviousTargetAgentKey: safeAgentEventKey(row.PreviousTargetAgent),
		TargetMachineID:        safeOpaqueEventID(row.TargetMachineID), OccurredAt: row.OccurredAt.UTC(),
		PreviousTargetMachineID: safeOpaqueEventID(row.PreviousMachineID),
	}
	switch row.EventKind {
	case "step":
		item.EventType, item.Summary = safeProjectStepEventLabel(row.EventType)
		item.FromStatus = safeDeliveryPlanStepStatus(row.FromStatus)
		item.ToStatus = safeDeliveryPlanStepStatus(row.ToStatus)
	case "assignment":
		item.EventType, item.Summary = safeProjectAssignmentEventLabel(row.EventType)
		item.FromStatus = safePlanStepAssignmentStatus(row.FromStatus)
		item.ToStatus = safePlanStepAssignmentStatus(row.ToStatus)
	case "activity":
		item.Action = row.Action
		if !validDeliveryPlanStepActivityAction(item.Action) {
			item.Action = ""
		}
		item.EventType = item.Action
		item.Phase = row.Phase
		if item.Phase != models.DeliveryPlanStepActivityStarted && item.Phase != models.DeliveryPlanStepActivityCompleted && item.Phase != models.DeliveryPlanStepActivityFailed {
			item.Phase = "recorded"
		}
		item.ToolName = safeDeliveryPlanStepActivityTool(row.ToolName)
		if item.Action != models.DeliveryPlanStepActivityTool {
			item.ToolName = ""
		}
		item.Summary = safeDeliveryPlanStepActivitySummary(item.Action, item.Phase)
	default:
		item.Kind = "activity"
		item.EventType = "event_recorded"
		item.Summary = "Activity recorded"
	}
	return item
}

func safeProjectStepEventLabel(eventType string) (string, string) {
	summary := map[string]string{
		models.DeliveryPlanStepEventReady:          "Step ready",
		models.DeliveryPlanStepEventClaimed:        "Step claimed",
		models.DeliveryPlanStepEventLeaseReclaimed: "Expired step lease reclaimed",
		models.DeliveryPlanStepEventLeaseRenewed:   "Step lease renewed",
		models.DeliveryPlanStepEventTransitioned:   "Step status changed",
	}[eventType]
	if summary == "" {
		return "step_event", "Step event recorded"
	}
	return safePlanStepEventLabel(eventType, summary)
}

func safeProjectAssignmentEventLabel(eventType string) (string, string) {
	switch eventType {
	case models.DeliveryPlanStepAssignmentEventCreated:
		return eventType, "Assignment created"
	case models.DeliveryPlanStepAssignmentEventStatusChanged:
		return eventType, "Assignment status changed"
	case models.DeliveryPlanStepAssignmentEventTargetChanged:
		return eventType, "Assignment target changed"
	case models.DeliveryPlanStepAssignmentEventStatusAndTargetChanged:
		return eventType, "Assignment status and target changed"
	default:
		return "assignment_event", "Assignment event recorded"
	}
}
