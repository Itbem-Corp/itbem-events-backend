package delivery

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	defaultPlanStepEventPageSize = 25
	maxPlanStepEventPageSize     = 100
	maxPlanStepEventCursorBytes  = 2048
)

type deliveryPlanStepEventDTO struct {
	ID               uuid.UUID  `json:"id"`
	EventType        string     `json:"event_type"`
	FromStatus       string     `json:"from_status"`
	ToStatus         string     `json:"to_status"`
	AutomationTaskID uuid.UUID  `json:"automation_task_id"`
	RunID            string     `json:"run_id"`
	WorkerID         string     `json:"worker_id"`
	AgentKey         string     `json:"agent_key"`
	MachineID        string     `json:"machine_id"`
	AgentInstanceID  *uuid.UUID `json:"agent_instance_id,omitempty"`
	Summary          string     `json:"summary"`
	OccurredAt       time.Time  `json:"occurred_at"`
}

type deliveryPlanStepEventPage struct {
	PlanID      uuid.UUID                  `json:"plan_id"`
	PlanVersion int                        `json:"plan_version"`
	StepID      uuid.UUID                  `json:"step_id"`
	Items       []deliveryPlanStepEventDTO `json:"items"`
	NextCursor  string                     `json:"next_cursor"`
}

type deliveryPlanStepEventCursor struct {
	Version    int       `json:"v"`
	Scope      string    `json:"s"`
	OccurredAt time.Time `json:"t"`
	ID         string    `json:"i"`
}

// ListPlanStepEvents returns an authorized, allow-listed event timeline for a
// single step. Cursor pagination is stable on (occurred_at, id), descending.
func ListPlanStepEvents(c echo.Context) error {
	planID, err := id(c, "delivery plan")
	if err != nil {
		return err
	}
	stepID, err := parseDeliveryPlanStepEventID(c.Param("stepId"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery plan step", "stepId must be a UUID")
	}
	if err := authorizeDeliveryPlanStepEventOrganization(c, planID); err != nil {
		return err
	}
	plan, _, err := loadDeliveryPlanForPermission(c, planID, deliveryView)
	if err != nil {
		return err
	}
	if plan == nil {
		// Authorization and lookup helpers may have already written a response.
		return nil
	}

	var step models.DeliveryPlanStep
	if err := configuration.DB.Select("id", "plan_id").Where("plan_id = ? AND id = ?", plan.ID, stepID).First(&step).Error; err != nil {
		return lookup(c, "Delivery plan step", err)
	}

	limit, err := planStepEventPageSize(c.QueryParam("limit"))
	if err != nil {
		return badRequest(c, "Invalid plan step event page size", err.Error())
	}
	scope := planStepEventCursorScope(plan.ID, step.ID)
	cursor, err := decodePlanStepEventCursor(c.QueryParam("cursor"), scope)
	if err != nil {
		return badRequest(c, "Invalid plan step event cursor", err.Error())
	}

	query := configuration.DB.Model(&models.DeliveryPlanStepEvent{}).
		Select("id", "event_type", "from_status", "to_status", "automation_task_id", "run_id", "worker_id", "agent_key", "machine_id", "agent_instance_id", "summary", "occurred_at").
		Where("plan_id = ? AND step_id = ?", plan.ID, step.ID)
	if cursor != nil {
		query = query.Where("(occurred_at < ? OR (occurred_at = ? AND id < ?))", cursor.OccurredAt, cursor.OccurredAt, uuid.Must(uuid.FromString(cursor.ID)))
	}
	var rows []models.DeliveryPlanStepEvent
	if err := query.Order("occurred_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return utilsError(c, err)
	}

	page := deliveryPlanStepEventPage{
		PlanID: plan.ID, PlanVersion: plan.Version, StepID: step.ID,
		Items: make([]deliveryPlanStepEventDTO, 0, limit), NextCursor: "",
	}
	hasNextPage := len(rows) > limit
	if hasNextPage {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodePlanStepEventCursor(deliveryPlanStepEventCursor{
			Version: 1, Scope: scope, OccurredAt: last.OccurredAt.UTC(), ID: last.ID.String(),
		})
	}
	for _, row := range rows {
		page.Items = append(page.Items, safePlanStepEventDTO(row))
	}
	return success(c, "Delivery plan step events", page)
}

// authorizeDeliveryPlanStepEventOrganization intersects the organization
// selected by authenticated middleware with the client that owns this
// project's plan. A missing/mismatched organization gets the same generic 404
// to avoid disclosing plan or project existence. The only context-free path is
// an explicit platform workspace already admitted by middleware, rechecked
// here against the authenticated user's platform-admin identity.
func authorizeDeliveryPlanStepEventOrganization(c echo.Context, planID uuid.UUID) error {
	if configuration.DB == nil {
		return deliveryRespondAndStop(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	workspaceMode, _ := c.Get("workspace_mode").(string)
	organizationID, hasOrganizationID := c.Get("organization_id").(uuid.UUID)
	if strings.EqualFold(strings.TrimSpace(workspaceMode), "platform") {
		actor, err := authz.CurrentUser(c)
		if err != nil {
			return deliveryRespondAuthzAndStop(c, err)
		}
		if actor != nil && actor.IsPlatformAdmin() {
			return nil
		}
		return deliveryPlanStepEventNotFound(c)
	}
	if !strings.EqualFold(strings.TrimSpace(workspaceMode), "organization") || !hasOrganizationID || organizationID == uuid.Nil {
		return deliveryPlanStepEventNotFound(c)
	}

	var plan models.DeliveryPlan
	if err := configuration.DB.Select("id", "work_item_id").First(&plan, "id = ?", planID).Error; err != nil {
		return deliveryPlanStepEventScopeLookupError(c, err)
	}
	var item models.DeliveryWorkItem
	if err := configuration.DB.Select("id", "project_id").First(&item, "id = ?", plan.WorkItemID).Error; err != nil {
		return deliveryPlanStepEventScopeLookupError(c, err)
	}
	var project models.DeliveryProject
	if err := configuration.DB.Select("id", "client_id").First(&project, "id = ?", item.ProjectID).Error; err != nil {
		return deliveryPlanStepEventScopeLookupError(c, err)
	}
	clientIDs, err := deliveryOrganizationClientIDs(organizationID)
	if errors.Is(err, errDeliveryOrganizationNotFound) {
		return deliveryPlanStepEventNotFound(c)
	}
	if err != nil {
		return deliveryRespondAndStop(c, http.StatusInternalServerError, "Delivery access unavailable", "Could not validate project organization")
	}
	if !deliveryOrganizationContainsClient(clientIDs, project.ClientID) {
		return deliveryPlanStepEventNotFound(c)
	}
	return nil
}

func deliveryPlanStepEventScopeLookupError(c echo.Context, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return deliveryPlanStepEventNotFound(c)
	}
	return deliveryRespondAndStop(c, http.StatusInternalServerError, "Delivery access unavailable", "Could not validate the requested plan scope")
}

func deliveryPlanStepEventNotFound(c echo.Context) error {
	return deliveryResourceNotFound(c)
}

func parseDeliveryPlanStepEventID(raw string) (uuid.UUID, error) {
	parsed, err := uuid.FromString(strings.TrimSpace(raw))
	if err != nil || parsed == uuid.Nil {
		return uuid.Nil, errors.New("step id must be a non-zero UUID")
	}
	return parsed, nil
}

func planStepEventPageSize(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultPlanStepEventPageSize, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 1 || limit > maxPlanStepEventPageSize {
		return 0, errors.New("limit must be an integer between 1 and 100")
	}
	return limit, nil
}

func planStepEventCursorScope(planID, stepID uuid.UUID) string {
	return planID.String() + ":" + stepID.String()
}

func encodePlanStepEventCursor(cursor deliveryPlanStepEventCursor) string {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodePlanStepEventCursor(raw, scope string) (*deliveryPlanStepEventCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxPlanStepEventCursorBytes {
		return nil, errors.New("cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("cursor encoding is invalid")
	}
	var cursor deliveryPlanStepEventCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 1 || cursor.Scope != scope || cursor.OccurredAt.IsZero() {
		return nil, errors.New("cursor is invalid or belongs to another step")
	}
	id, err := uuid.FromString(cursor.ID)
	if err != nil || id == uuid.Nil {
		return nil, errors.New("cursor id must be a non-zero UUID")
	}
	cursor.ID = id.String()
	cursor.OccurredAt = cursor.OccurredAt.UTC()
	return &cursor, nil
}

// Project only known lifecycle labels and fixed server-authored summaries.
// Unknown persisted strings degrade to generic safe labels instead of being
// reflected to users as arbitrary database content.
func safePlanStepEventDTO(event models.DeliveryPlanStepEvent) deliveryPlanStepEventDTO {
	eventType, fixedSummary := safePlanStepEventLabel(event.EventType, event.Summary)
	return deliveryPlanStepEventDTO{
		ID: event.ID, EventType: eventType,
		FromStatus: safeDeliveryPlanStepStatus(event.FromStatus), ToStatus: safeDeliveryPlanStepStatus(event.ToStatus),
		AutomationTaskID: event.AutomationTaskID, RunID: safeOpaqueEventID(event.RunID),
		WorkerID: safeOpaqueEventID(event.WorkerID),
		AgentKey: safeAgentEventKey(event.AgentKey), MachineID: safeOpaqueEventID(event.MachineID), AgentInstanceID: event.AgentInstanceID,
		Summary: fixedSummary, OccurredAt: event.OccurredAt.UTC(),
	}
}

func safePlanStepEventLabel(eventType, summary string) (string, string) {
	typeAndSummary := map[string]string{
		models.DeliveryPlanStepEventReady:          "Step ready",
		models.DeliveryPlanStepEventClaimed:        "Step claimed",
		models.DeliveryPlanStepEventLeaseReclaimed: "Expired step lease reclaimed",
		models.DeliveryPlanStepEventLeaseRenewed:   "Step lease renewed",
		models.DeliveryPlanStepEventTransitioned:   "Step status changed",
	}
	fixedSummary, ok := typeAndSummary[eventType]
	if !ok {
		return "step_event", "Step event recorded"
	}
	// The summary is validated against known generated text. Status transition
	// summaries vary by destination status; the API uses a generic fixed label.
	if eventType == models.DeliveryPlanStepEventTransitioned {
		switch strings.TrimSpace(summary) {
		case "Step made ready", "Step started", "Step blocked", "Step completed", "Step failed", "Step skipped", "Step status changed":
		default:
			return "step_event", "Step event recorded"
		}
	}
	if eventType != models.DeliveryPlanStepEventTransitioned && strings.TrimSpace(summary) != fixedSummary {
		return "step_event", "Step event recorded"
	}
	return eventType, fixedSummary
}

func safeDeliveryPlanStepStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case models.DeliveryPlanStepPlanned, models.DeliveryPlanStepReady, models.DeliveryPlanStepRunning,
		models.DeliveryPlanStepBlocked, models.DeliveryPlanStepCompleted, models.DeliveryPlanStepFailed, models.DeliveryPlanStepSkipped:
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return ""
	}
}

func safeOpaqueEventID(raw string) string {
	parsed, err := uuid.FromString(strings.TrimSpace(raw))
	if err != nil || parsed == uuid.Nil {
		return ""
	}
	return parsed.String()
}

func safeAgentEventKey(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) < 2 || len(value) > 64 {
		return ""
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (index > 0 && char >= '0' && char <= '9') || (index > 0 && (char == '_' || char == '-')) {
			continue
		}
		return ""
	}
	return value
}
