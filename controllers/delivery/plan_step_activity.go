package delivery

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	defaultPlanStepActivityPageSize = 25
	maxPlanStepActivityPageSize     = 100
	maxPlanStepActivityCursorBytes  = 2048
)

type deliveryPlanStepActivityDTO struct {
	ID               uuid.UUID                               `json:"id"`
	Sequence         int64                                   `json:"sequence"`
	Action           string                                  `json:"action"`
	Phase            string                                  `json:"phase"`
	ToolName         string                                  `json:"tool_name"`
	AutomationTaskID uuid.UUID                               `json:"automation_task_id"`
	RunID            string                                  `json:"run_id"`
	WorkerID         string                                  `json:"worker_id"`
	AgentKey         string                                  `json:"agent_key"`
	MachineID        string                                  `json:"machine_id"`
	AgentInstanceID  *uuid.UUID                              `json:"agent_instance_id,omitempty"`
	DurationMS       *int64                                  `json:"duration_ms"`
	Details          *models.DeliveryPlanStepActivityDetails `json:"details,omitempty"`
	Inference        *deliveryPlanStepInferenceDTO           `json:"inference,omitempty"`
	Summary          string                                  `json:"summary"`
	OccurredAt       time.Time                               `json:"occurred_at"`
}

// deliveryPlanStepInferenceDTO intentionally contains only a bounded,
// server-derived accounting projection from the canonical gateway receipt.
// It has no prompt, completion, credential, hidden reasoning or raw provider
// response fields.
type deliveryPlanStepInferenceDTO struct {
	ReceiptID         uuid.UUID `json:"receipt_id"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	Status            string    `json:"status"`
	InputTokens       int64     `json:"input_tokens"`
	OutputTokens      int64     `json:"output_tokens"`
	CachedInputTokens int64     `json:"cached_input_tokens"`
	CacheWriteTokens  int64     `json:"cache_write_tokens"`
	ReasoningTokens   int64     `json:"reasoning_tokens"`
	TotalTokens       int64     `json:"total_tokens"`
	TotalCostMicrousd int64     `json:"total_cost_microusd"`
	Currency          string    `json:"currency"`
	PricingBasis      string    `json:"pricing_basis"`
}

type planStepActivityInferenceReceiptRow struct {
	ID                uuid.UUID  `gorm:"column:id"`
	PlanStepID        *uuid.UUID `gorm:"column:plan_step_id"`
	CallID            uuid.UUID  `gorm:"column:call_id"`
	AutomationTaskID  uuid.UUID  `gorm:"column:automation_task_id"`
	RunID             string     `gorm:"column:run_id"`
	WorkerID          string     `gorm:"column:worker_id"`
	AgentKey          string     `gorm:"column:agent_key"`
	MachineID         string     `gorm:"column:machine_id"`
	Status            string     `gorm:"column:status"`
	Provider          string     `gorm:"column:provider"`
	Model             string     `gorm:"column:model"`
	InputTokens       int64      `gorm:"column:input_tokens"`
	OutputTokens      int64      `gorm:"column:output_tokens"`
	CachedInputTokens int64      `gorm:"column:cached_input_tokens"`
	CacheWriteTokens  int64      `gorm:"column:cache_write_tokens"`
	ReasoningTokens   int64      `gorm:"column:reasoning_tokens"`
	TotalTokens       int64      `gorm:"column:total_tokens"`
	TotalCostMicros   int64      `gorm:"column:total_cost_micros"`
	Currency          string     `gorm:"column:currency"`
	PricingBasis      string     `gorm:"column:pricing_basis"`
}

var planStepInferenceLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+\-]{0,127}$`)

type deliveryPlanStepActivityPage struct {
	PlanID      uuid.UUID                     `json:"plan_id"`
	PlanVersion int                           `json:"plan_version"`
	StepID      uuid.UUID                     `json:"step_id"`
	Items       []deliveryPlanStepActivityDTO `json:"items"`
	NextCursor  string                        `json:"next_cursor"`
}

type deliveryPlanStepActivityCursor struct {
	Version    int       `json:"v"`
	Scope      string    `json:"s"`
	OccurredAt time.Time `json:"t"`
	ID         string    `json:"i"`
}

// ListPlanStepActivity returns the authorized, allow-listed observable
// activity timeline for a plan step, ordered by (occurred_at, id) descending.
func ListPlanStepActivity(c echo.Context) error {
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
		return nil
	}
	var step models.DeliveryPlanStep
	if err := configuration.DB.Select("id", "plan_id").Where("plan_id = ? AND id = ?", plan.ID, stepID).First(&step).Error; err != nil {
		return lookup(c, "Delivery plan step", err)
	}
	limit, err := planStepActivityPageSize(c.QueryParam("limit"))
	if err != nil {
		return badRequest(c, "Invalid plan step activity page size", err.Error())
	}
	scope := planStepActivityCursorScope(plan.ID, step.ID)
	cursor, err := decodePlanStepActivityCursor(c.QueryParam("cursor"), scope)
	if err != nil {
		return badRequest(c, "Invalid plan step activity cursor", err.Error())
	}
	query := configuration.DB.Model(&models.DeliveryPlanStepActivityEvent{}).
		Select("id", "step_id", "automation_task_id", "run_id", "worker_id", "agent_key", "machine_id", "agent_instance_id", "sequence", "action", "phase", "tool_name", "duration_ms", "summary", "details_json", "occurred_at").
		Where("plan_id = ? AND step_id = ?", plan.ID, step.ID)
	if cursor != nil {
		query = query.Where("(occurred_at < ? OR (occurred_at = ? AND id < ?))", cursor.OccurredAt, cursor.OccurredAt, uuid.Must(uuid.FromString(cursor.ID)))
	}
	var rows []models.DeliveryPlanStepActivityEvent
	if err := query.Order("occurred_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return utilsError(c, err)
	}
	page := deliveryPlanStepActivityPage{PlanID: plan.ID, PlanVersion: plan.Version, StepID: step.ID, Items: make([]deliveryPlanStepActivityDTO, 0, limit)}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodePlanStepActivityCursor(deliveryPlanStepActivityCursor{Version: 1, Scope: scope, OccurredAt: last.OccurredAt.UTC(), ID: last.ID.String()})
	}
	inferenceByEventID, err := loadPlanStepActivityInferenceProjections(configuration.DB, rows)
	if err != nil {
		return utilsError(c, err)
	}
	for _, row := range rows {
		item := safePlanStepActivityDTO(row)
		item.Inference = inferenceByEventID[row.ID]
		page.Items = append(page.Items, item)
	}
	return success(c, "Delivery plan step activity", page)
}

func loadPlanStepActivityInferenceProjections(db *gorm.DB, events []models.DeliveryPlanStepActivityEvent) (map[uuid.UUID]*deliveryPlanStepInferenceDTO, error) {
	projections := make(map[uuid.UUID]*deliveryPlanStepInferenceDTO)
	if db == nil || len(events) == 0 {
		return projections, nil
	}
	activityIDs := make([]uuid.UUID, 0, len(events))
	for _, event := range events {
		if event.Action != models.DeliveryPlanStepActivityInference || event.Phase == models.DeliveryPlanStepActivityStarted {
			continue
		}
		activityIDs = append(activityIDs, event.ID)
	}
	if len(activityIDs) == 0 {
		return projections, nil
	}
	type activityReceiptLink struct {
		ID                 uuid.UUID  `gorm:"column:id"`
		InferenceCallID    *uuid.UUID `gorm:"column:inference_call_id"`
		InferenceReceiptID *uuid.UUID `gorm:"column:inference_receipt_id"`
	}
	var links []activityReceiptLink
	if err := db.Table("delivery_plan_step_activity_events").Select("id, inference_call_id, inference_receipt_id").Where("id IN ?", activityIDs).Scan(&links).Error; err != nil {
		return nil, err
	}
	linkByEventID := make(map[uuid.UUID]activityReceiptLink, len(links))
	ids := make([]uuid.UUID, 0, len(links))
	seenReceiptIDs := make(map[uuid.UUID]struct{}, len(links))
	for _, link := range links {
		linkByEventID[link.ID] = link
		if link.InferenceReceiptID == nil {
			continue
		}
		if _, found := seenReceiptIDs[*link.InferenceReceiptID]; found {
			continue
		}
		seenReceiptIDs[*link.InferenceReceiptID] = struct{}{}
		ids = append(ids, *link.InferenceReceiptID)
	}
	if len(ids) == 0 {
		return projections, nil
	}
	var receipts []planStepActivityInferenceReceiptRow
	if err := db.Table("automation_inference_receipts").Select(
		"id, plan_step_id, call_id, automation_task_id, run_id, worker_id, agent_key, machine_id, status, provider, model, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, total_cost_micros, currency, pricing_basis",
	).Where("id IN ?", ids).Scan(&receipts).Error; err != nil {
		return nil, err
	}
	byReceiptID := make(map[uuid.UUID]planStepActivityInferenceReceiptRow, len(receipts))
	for _, receipt := range receipts {
		byReceiptID[receipt.ID] = receipt
	}
	for _, event := range events {
		if event.Action != models.DeliveryPlanStepActivityInference || event.Phase == models.DeliveryPlanStepActivityStarted {
			continue
		}
		link, found := linkByEventID[event.ID]
		if !found || link.InferenceCallID == nil || link.InferenceReceiptID == nil {
			continue
		}
		receipt, found := byReceiptID[*link.InferenceReceiptID]
		if !found {
			continue
		}
		event.InferenceCallID, event.InferenceReceiptID = link.InferenceCallID, link.InferenceReceiptID
		projection, ok := safePlanStepActivityInferenceProjection(event, receipt)
		if ok {
			projections[event.ID] = projection
		}
	}
	return projections, nil
}

func safePlanStepActivityInferenceProjection(event models.DeliveryPlanStepActivityEvent, receipt planStepActivityInferenceReceiptRow) (*deliveryPlanStepInferenceDTO, bool) {
	if event.ID == uuid.Nil || event.InferenceReceiptID == nil || event.InferenceCallID == nil || *event.InferenceReceiptID != receipt.ID || *event.InferenceCallID != receipt.CallID ||
		event.StepID == uuid.Nil || receipt.PlanStepID == nil || *receipt.PlanStepID == uuid.Nil || event.StepID != *receipt.PlanStepID ||
		event.Action != models.DeliveryPlanStepActivityInference || event.Phase == models.DeliveryPlanStepActivityStarted ||
		event.AutomationTaskID != receipt.AutomationTaskID || event.RunID != receipt.RunID || event.WorkerID != receipt.WorkerID || event.AgentKey != receipt.AgentKey || event.MachineID != receipt.MachineID ||
		(receipt.Status != "accepted" && receipt.Status != "rejected") || !planStepInferenceLabelPattern.MatchString(receipt.Provider) || !planStepInferenceLabelPattern.MatchString(receipt.Model) ||
		receipt.InputTokens < 0 || receipt.OutputTokens < 0 || receipt.CachedInputTokens < 0 || receipt.CacheWriteTokens < 0 || receipt.ReasoningTokens < 0 || receipt.TotalTokens < 0 || receipt.TotalCostMicros < 0 ||
		len(receipt.Currency) != 3 || receipt.Currency[0] < 'A' || receipt.Currency[0] > 'Z' || receipt.Currency[1] < 'A' || receipt.Currency[1] > 'Z' || receipt.Currency[2] < 'A' || receipt.Currency[2] > 'Z' ||
		!validPlanStepInferencePricingBasis(receipt.PricingBasis) || (event.Phase == models.DeliveryPlanStepActivityCompleted && receipt.Status != "accepted") {
		return nil, false
	}
	return &deliveryPlanStepInferenceDTO{
		ReceiptID: receipt.ID, Provider: receipt.Provider, Model: receipt.Model, Status: receipt.Status,
		InputTokens: receipt.InputTokens, OutputTokens: receipt.OutputTokens, CachedInputTokens: receipt.CachedInputTokens,
		CacheWriteTokens: receipt.CacheWriteTokens, ReasoningTokens: receipt.ReasoningTokens, TotalTokens: receipt.TotalTokens,
		TotalCostMicrousd: receipt.TotalCostMicros, Currency: receipt.Currency, PricingBasis: receipt.PricingBasis,
	}, true
}

func validPlanStepInferencePricingBasis(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func safePlanStepActivityDTO(event models.DeliveryPlanStepActivityEvent) deliveryPlanStepActivityDTO {
	action := event.Action
	if !validDeliveryPlanStepActivityAction(action) {
		action = "activity"
	}
	phase := event.Phase
	if phase != models.DeliveryPlanStepActivityStarted && phase != models.DeliveryPlanStepActivityCompleted && phase != models.DeliveryPlanStepActivityFailed {
		phase = "recorded"
	}
	toolName := safeDeliveryPlanStepActivityTool(event.ToolName)
	if action != models.DeliveryPlanStepActivityTool {
		toolName = ""
	}
	duration := event.DurationMS
	if duration != nil && (*duration < 0 || *duration > 24*60*60*1000 || phase == models.DeliveryPlanStepActivityStarted) {
		duration = nil
	}
	details, _ := safePlanStepActivityDetails(event.Action, event.Phase, event.DetailsJSON)
	return deliveryPlanStepActivityDTO{
		ID: event.ID, Sequence: event.Sequence, Action: action, Phase: phase, ToolName: toolName,
		AutomationTaskID: event.AutomationTaskID, RunID: safeOpaqueEventID(event.RunID),
		WorkerID: safeOpaqueEventID(event.WorkerID), AgentKey: safeAgentEventKey(event.AgentKey), MachineID: safeOpaqueEventID(event.MachineID), AgentInstanceID: event.AgentInstanceID,
		DurationMS: duration, Details: details, Summary: safeDeliveryPlanStepActivitySummary(action, phase), OccurredAt: event.OccurredAt.UTC(),
	}
}

func safePlanStepActivityDetails(action, phase, raw string) (*models.DeliveryPlanStepActivityDetails, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return nil, true
	}
	if trimmed == "null" || len(trimmed) > models.MaxDeliveryPlanStepActivityDetailsBytes {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var details models.DeliveryPlanStepActivityDetails
	if err := decoder.Decode(&details); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	if err := models.ValidateDeliveryPlanStepActivityDetails(action, phase, &details); err != nil {
		return nil, false
	}
	return &details, true
}

func validDeliveryPlanStepActivityAction(action string) bool {
	switch action {
	case models.DeliveryPlanStepActivityInference, models.DeliveryPlanStepActivityTool,
		models.DeliveryPlanStepActivityFileRead, models.DeliveryPlanStepActivityFileChange,
		models.DeliveryPlanStepActivityCommand, models.DeliveryPlanStepActivityValidation,
		models.DeliveryPlanStepActivityEvidence:
		return true
	default:
		return false
	}
}

func safeDeliveryPlanStepActivityTool(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) > 48 || value == "" {
		return ""
	}
	for index, char := range value {
		if (index == 0 && char >= 'a' && char <= 'z') || (index > 0 && char >= 'a' && char <= 'z') || (index > 0 && char >= '0' && char <= '9') || (index > 0 && char == '_') {
			continue
		}
		return ""
	}
	return value
}

func safeDeliveryPlanStepActivitySummary(action, phase string) string {
	labels := map[string]string{
		models.DeliveryPlanStepActivityInference:  "Inference",
		models.DeliveryPlanStepActivityTool:       "Tool",
		models.DeliveryPlanStepActivityFileRead:   "File read",
		models.DeliveryPlanStepActivityFileChange: "File change",
		models.DeliveryPlanStepActivityCommand:    "Command",
		models.DeliveryPlanStepActivityValidation: "Validation",
		models.DeliveryPlanStepActivityEvidence:   "Evidence",
	}
	label, ok := labels[action]
	if !ok || phase != models.DeliveryPlanStepActivityStarted && phase != models.DeliveryPlanStepActivityCompleted && phase != models.DeliveryPlanStepActivityFailed {
		return "Step activity recorded"
	}
	return label + " " + phase
}

func planStepActivityPageSize(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultPlanStepActivityPageSize, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 1 || limit > maxPlanStepActivityPageSize {
		return 0, errors.New("limit must be an integer between 1 and 100")
	}
	return limit, nil
}

func planStepActivityCursorScope(planID, stepID uuid.UUID) string {
	return planID.String() + ":" + stepID.String()
}

func encodePlanStepActivityCursor(cursor deliveryPlanStepActivityCursor) string {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodePlanStepActivityCursor(raw, scope string) (*deliveryPlanStepActivityCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxPlanStepActivityCursorBytes {
		return nil, errors.New("cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("cursor encoding is invalid")
	}
	var cursor deliveryPlanStepActivityCursor
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
