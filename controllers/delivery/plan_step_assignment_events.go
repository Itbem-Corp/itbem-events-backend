package delivery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const (
	defaultPlanStepAssignmentEventPageSize = 25
	maxPlanStepAssignmentEventPageSize     = 100
	maxPlanStepAssignmentEventCursorBytes  = 2048
)

type deliveryPlanStepAssignmentEventFilters struct {
	StepID       *uuid.UUID
	AssignmentID *uuid.UUID
	TaskID       *uuid.UUID
	Status       string
	AgentKey     string
	MachineID    *uuid.UUID
}

type deliveryPlanStepAssignmentEventDTO struct {
	ID                      uuid.UUID `json:"id"`
	AssignmentID            uuid.UUID `json:"assignment_id"`
	ExecutionID             uuid.UUID `json:"execution_id"`
	StepID                  uuid.UUID `json:"step_id"`
	ParentTaskID            uuid.UUID `json:"parent_task_id"`
	TaskID                  uuid.UUID `json:"task_id"`
	EventType               string    `json:"event_type"`
	PreviousStatus          string    `json:"previous_status"`
	Status                  string    `json:"status"`
	PreviousTargetAgentKey  string    `json:"previous_target_agent_key"`
	TargetAgentKey          string    `json:"target_agent_key"`
	PreviousTargetMachineID string    `json:"previous_target_machine_id"`
	TargetMachineID         string    `json:"target_machine_id"`
	OccurredAt              time.Time `json:"occurred_at"`
}

type deliveryPlanStepAssignmentEventPage struct {
	PlanID      uuid.UUID                            `json:"plan_id"`
	PlanVersion int                                  `json:"plan_version"`
	Items       []deliveryPlanStepAssignmentEventDTO `json:"items"`
	NextCursor  string                               `json:"next_cursor"`
}

type deliveryPlanStepAssignmentEventCursor struct {
	Version    int       `json:"v"`
	Scope      string    `json:"s"`
	OccurredAt time.Time `json:"t"`
	ID         string    `json:"i"`
}

// ListPlanStepAssignmentEvents returns a project-authorized, allow-listed
// assignment history for one immutable plan version. Keyset pagination is
// stable on (occurred_at, id), descending; all filters are cursor-scoped.
func ListPlanStepAssignmentEvents(c echo.Context) error {
	planID, err := id(c, "delivery plan")
	if err != nil {
		return err
	}
	plan, actor, err := loadDeliveryPlanForPermission(c, planID, deliveryView)
	if err != nil {
		return err
	}
	if plan == nil || actor == nil {
		return nil
	}
	limit, err := planStepAssignmentEventPageSize(c.QueryParam("limit"))
	if err != nil {
		return badRequest(c, "Invalid plan step assignment event page size", err.Error())
	}
	filters, err := parsePlanStepAssignmentEventFilters(c)
	if err != nil {
		return badRequest(c, "Invalid plan step assignment event filter", err.Error())
	}
	workspaceMode, _ := c.Get("workspace_mode").(string)
	organizationID, _ := c.Get("organization_id").(uuid.UUID)
	scope := planStepAssignmentEventCursorScope(plan.ID, actor.CognitoSub, workspaceMode, organizationID, filters)
	cursor, err := decodePlanStepAssignmentEventCursor(c.QueryParam("cursor"), scope)
	if err != nil {
		return badRequest(c, "Invalid plan step assignment event cursor", err.Error())
	}

	query := configuration.DB.Model(&models.DeliveryPlanStepAssignmentEvent{}).
		Select("id", "assignment_id", "execution_id", "step_id", "parent_automation_task_id", "child_automation_task_id", "event_type", "previous_status", "status", "previous_target_agent_key", "target_agent_key", "previous_target_machine_id", "target_machine_id", "occurred_at").
		Where("plan_id = ?", plan.ID)
	if filters.StepID != nil {
		query = query.Where("step_id = ?", *filters.StepID)
	}
	if filters.AssignmentID != nil {
		query = query.Where("assignment_id = ?", *filters.AssignmentID)
	}
	if filters.TaskID != nil {
		query = query.Where("(parent_automation_task_id = ? OR child_automation_task_id = ?)", *filters.TaskID, *filters.TaskID)
	}
	if filters.Status != "" {
		query = query.Where("status = ?", filters.Status)
	}
	if filters.AgentKey != "" {
		query = query.Where("(target_agent_key = ? OR previous_target_agent_key = ?)", filters.AgentKey, filters.AgentKey)
	}
	if filters.MachineID != nil {
		machineID := filters.MachineID.String()
		query = query.Where("(target_machine_id = ? OR previous_target_machine_id = ?)", machineID, machineID)
	}
	if cursor != nil {
		query = query.Where("(occurred_at < ? OR (occurred_at = ? AND id < ?))", cursor.OccurredAt, cursor.OccurredAt, uuid.Must(uuid.FromString(cursor.ID)))
	}
	var rows []models.DeliveryPlanStepAssignmentEvent
	if err := query.Order("occurred_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return utilsError(c, err)
	}

	page := deliveryPlanStepAssignmentEventPage{
		PlanID: plan.ID, PlanVersion: plan.Version,
		Items: make([]deliveryPlanStepAssignmentEventDTO, 0, limit),
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodePlanStepAssignmentEventCursor(deliveryPlanStepAssignmentEventCursor{
			Version: 1, Scope: scope, OccurredAt: last.OccurredAt.UTC(), ID: last.ID.String(),
		})
	}
	for _, row := range rows {
		page.Items = append(page.Items, safePlanStepAssignmentEventDTO(row))
	}
	return success(c, "Delivery plan step assignment events", page)
}

func parsePlanStepAssignmentEventFilters(c echo.Context) (deliveryPlanStepAssignmentEventFilters, error) {
	var filters deliveryPlanStepAssignmentEventFilters
	for _, entry := range []struct {
		name   string
		target **uuid.UUID
	}{
		{name: "step_id", target: &filters.StepID},
		{name: "assignment_id", target: &filters.AssignmentID},
		{name: "task_id", target: &filters.TaskID},
		{name: "machine_id", target: &filters.MachineID},
	} {
		raw := strings.TrimSpace(c.QueryParam(entry.name))
		if raw == "" {
			continue
		}
		id, err := uuid.FromString(raw)
		if err != nil || id == uuid.Nil {
			return filters, errors.New(entry.name + " must be a non-zero UUID")
		}
		*entry.target = &id
	}
	filters.Status = strings.ToLower(strings.TrimSpace(c.QueryParam("status")))
	if filters.Status != "" && !validDeliveryPlanStepAssignmentStatus(filters.Status) {
		return filters, errors.New("status is not supported")
	}
	filters.AgentKey = strings.TrimSpace(c.QueryParam("agent_key"))
	if filters.AgentKey != "" && safeAgentEventKey(filters.AgentKey) != filters.AgentKey {
		return filters, errors.New("agent_key is invalid")
	}
	return filters, nil
}

func validDeliveryPlanStepAssignmentStatus(status string) bool {
	switch status {
	case models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
		models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning,
		models.DeliveryPlanStepAssignmentBlocked, models.DeliveryPlanStepAssignmentCompleted,
		models.DeliveryPlanStepAssignmentFailed, models.DeliveryPlanStepAssignmentCancelled:
		return true
	default:
		return false
	}
}

func planStepAssignmentEventPageSize(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultPlanStepAssignmentEventPageSize, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 1 || limit > maxPlanStepAssignmentEventPageSize {
		return 0, errors.New("limit must be an integer between 1 and 100")
	}
	return limit, nil
}

func planStepAssignmentEventCursorScope(planID uuid.UUID, actorSub, workspaceMode string, organizationID uuid.UUID, filters deliveryPlanStepAssignmentEventFilters) string {
	identity := struct {
		PlanID        string `json:"plan_id"`
		ActorSub      string `json:"actor_sub"`
		WorkspaceMode string `json:"workspace_mode"`
		Organization  string `json:"organization_id,omitempty"`
		StepID        string `json:"step_id,omitempty"`
		AssignmentID  string `json:"assignment_id,omitempty"`
		TaskID        string `json:"task_id,omitempty"`
		Status        string `json:"status,omitempty"`
		AgentKey      string `json:"agent_key,omitempty"`
		MachineID     string `json:"machine_id,omitempty"`
	}{PlanID: planID.String(), ActorSub: actorSub, WorkspaceMode: strings.ToLower(strings.TrimSpace(workspaceMode)), Status: filters.Status, AgentKey: filters.AgentKey}
	if organizationID != uuid.Nil {
		identity.Organization = organizationID.String()
	}
	if filters.StepID != nil {
		identity.StepID = filters.StepID.String()
	}
	if filters.AssignmentID != nil {
		identity.AssignmentID = filters.AssignmentID.String()
	}
	if filters.TaskID != nil {
		identity.TaskID = filters.TaskID.String()
	}
	if filters.MachineID != nil {
		identity.MachineID = filters.MachineID.String()
	}
	encoded, _ := json.Marshal(identity)
	scopeHash := sha256.Sum256(encoded)
	return hex.EncodeToString(scopeHash[:])
}

func encodePlanStepAssignmentEventCursor(cursor deliveryPlanStepAssignmentEventCursor) string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodePlanStepAssignmentEventCursor(raw, scope string) (*deliveryPlanStepAssignmentEventCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxPlanStepAssignmentEventCursorBytes {
		return nil, errors.New("cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("cursor encoding is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var cursor deliveryPlanStepAssignmentEventCursor
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Scope != scope || cursor.OccurredAt.IsZero() {
		return nil, errors.New("cursor is invalid or belongs to another plan or filter set")
	}
	id, err := uuid.FromString(cursor.ID)
	if err != nil || id == uuid.Nil || id.String() != cursor.ID {
		return nil, errors.New("cursor id is invalid")
	}
	cursor.OccurredAt = cursor.OccurredAt.UTC()
	return &cursor, nil
}

func safePlanStepAssignmentEventDTO(event models.DeliveryPlanStepAssignmentEvent) deliveryPlanStepAssignmentEventDTO {
	eventType := event.EventType
	switch eventType {
	case models.DeliveryPlanStepAssignmentEventCreated, models.DeliveryPlanStepAssignmentEventStatusChanged,
		models.DeliveryPlanStepAssignmentEventTargetChanged, models.DeliveryPlanStepAssignmentEventStatusAndTargetChanged:
	default:
		eventType = "assignment_event"
	}
	return deliveryPlanStepAssignmentEventDTO{
		ID: event.ID, AssignmentID: event.AssignmentID, ExecutionID: event.ExecutionID,
		StepID: event.StepID, ParentTaskID: event.ParentAutomationTaskID, TaskID: event.ChildAutomationTaskID,
		EventType: eventType, PreviousStatus: safePlanStepAssignmentStatus(event.PreviousStatus),
		Status:                 safePlanStepAssignmentStatus(event.Status),
		PreviousTargetAgentKey: safeAgentEventKey(event.PreviousTargetAgentKey), TargetAgentKey: safeAgentEventKey(event.TargetAgentKey),
		PreviousTargetMachineID: safeOpaqueEventID(event.PreviousTargetMachineID), TargetMachineID: safeOpaqueEventID(event.TargetMachineID),
		OccurredAt: event.OccurredAt.UTC(),
	}
}

func safePlanStepAssignmentStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if !validDeliveryPlanStepAssignmentStatus(status) {
		return ""
	}
	return status
}
