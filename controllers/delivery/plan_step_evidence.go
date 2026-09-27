package delivery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/awsrepository"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const (
	defaultPlanStepEvidencePageSize = 25
	maxPlanStepEvidencePageSize     = 100
	maxPlanStepEvidenceCursorBytes  = 2048
)

type deliveryPlanStepEvidenceDTO struct {
	ID               uuid.UUID `json:"id"`
	RequirementKey   string    `json:"requirement_key"`
	FileName         string    `json:"file_name"`
	ContentType      string    `json:"content_type"`
	SizeBytes        int64     `json:"size_bytes"`
	SHA256           string    `json:"sha256"`
	Source           string    `json:"source"`
	AutomationTaskID uuid.UUID `json:"automation_task_id"`
	RunID            string    `json:"run_id"`
	AgentKey         string    `json:"agent_key"`
	AgentInstanceID  uuid.UUID `json:"agent_instance_id"`
	FencingToken     int64     `json:"fencing_token"`
	CreatedAt        time.Time `json:"created_at"`
}

type deliveryPlanStepEvidencePage struct {
	PlanID      uuid.UUID                     `json:"plan_id"`
	PlanVersion int                           `json:"plan_version"`
	StepID      uuid.UUID                     `json:"step_id"`
	Items       []deliveryPlanStepEvidenceDTO `json:"items"`
	NextCursor  string                        `json:"next_cursor"`
}

type deliveryPlanStepEvidenceCursor struct {
	Version   int       `json:"v"`
	Scope     string    `json:"s"`
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
}

// ListPlanStepEvidence returns only safe metadata for artifacts attached to
// one authorized versioned plan step. Storage bucket, key and signed URLs are
// intentionally excluded from the DTO.
func ListPlanStepEvidence(c echo.Context) error {
	planID, err := id(c, "delivery plan")
	if err != nil {
		return err
	}
	stepID, err := parseDeliveryPlanStepEventID(c.Param("stepId"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery plan step", "stepId must be a UUID")
	}
	plan, _, err := loadDeliveryPlanForPermission(c, planID, deliveryView)
	if err != nil || plan == nil {
		return err
	}
	var step models.DeliveryPlanStep
	if err := configuration.DB.Select("id", "plan_id").Where("plan_id = ? AND id = ?", plan.ID, stepID).First(&step).Error; err != nil {
		return lookup(c, "Delivery plan step", err)
	}
	limit, err := planStepEvidencePageSize(c.QueryParam("limit"))
	if err != nil {
		return badRequest(c, "Invalid plan step evidence page size", err.Error())
	}
	requirementKey := strings.TrimSpace(c.QueryParam("requirement_key"))
	if requirementKey != "" && !validPlanStepEvidenceRequirementKey(requirementKey) {
		return badRequest(c, "Invalid plan step evidence filter", "requirement_key is invalid")
	}
	runID := strings.TrimSpace(c.QueryParam("run_id"))
	if runID != "" {
		parsed, parseErr := uuid.FromString(runID)
		if parseErr != nil || parsed == uuid.Nil || parsed.String() != runID {
			return badRequest(c, "Invalid plan step evidence filter", "run_id must be a UUID")
		}
	}
	scope := planStepEvidenceCursorScope(plan.ID, step.ID, requirementKey, runID)
	cursor, err := decodePlanStepEvidenceCursor(c.QueryParam("cursor"), scope)
	if err != nil {
		return badRequest(c, "Invalid plan step evidence cursor", err.Error())
	}
	query := configuration.DB.Model(&models.DeliveryPlanStepEvidence{}).
		Select("id", "plan_id", "plan_version", "step_id", "requirement_key", "automation_task_id", "run_id", "agent_key", "machine_id", "agent_instance_id", "fencing_token", "file_name", "content_type", "sha256", "size_bytes", "created_at").
		Where("plan_id = ? AND plan_version = ? AND step_id = ?", plan.ID, plan.Version, step.ID)
	if requirementKey != "" {
		query = query.Where("requirement_key = ?", requirementKey)
	}
	if runID != "" {
		query = query.Where("run_id = ?", runID)
	}
	if cursor != nil {
		cursorID, _ := uuid.FromString(cursor.ID)
		query = query.Where("(created_at < ? OR (created_at = ? AND id < ?))", cursor.CreatedAt, cursor.CreatedAt, cursorID)
	}
	var rows []models.DeliveryPlanStepEvidence
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return utilsError(c, err)
	}
	page := deliveryPlanStepEvidencePage{PlanID: plan.ID, PlanVersion: plan.Version, StepID: step.ID, Items: make([]deliveryPlanStepEvidenceDTO, 0, limit)}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodePlanStepEvidenceCursor(deliveryPlanStepEvidenceCursor{Version: 1, Scope: scope, CreatedAt: last.CreatedAt.UTC(), ID: last.ID.String()})
	}
	for _, row := range rows {
		page.Items = append(page.Items, safePlanStepEvidenceDTO(row))
	}
	return success(c, "Delivery plan step evidence", page)
}

// DownloadPlanStepEvidence is an authorized proxy download. It rechecks the
// private object's size and digest before sending bytes and never redirects to
// S3 or returns storage coordinates.
func DownloadPlanStepEvidence(c echo.Context) error {
	planID, err := id(c, "delivery plan")
	if err != nil {
		return err
	}
	stepID, err := parseDeliveryPlanStepEventID(c.Param("stepId"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery plan step", "stepId must be a UUID")
	}
	evidenceID, err := parseDeliveryPlanStepEventID(c.Param("evidenceId"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery plan step evidence", "evidenceId must be a UUID")
	}
	plan, _, err := loadDeliveryPlanForPermission(c, planID, deliveryView)
	if err != nil || plan == nil {
		return err
	}
	var record models.DeliveryPlanStepEvidence
	if err := configuration.DB.Select("id", "plan_id", "plan_version", "step_id", "file_name", "content_type", "bucket", "object_key", "sha256", "size_bytes").
		Where("id = ? AND plan_id = ? AND plan_version = ? AND step_id = ?", evidenceID, plan.ID, plan.Version, stepID).First(&record).Error; err != nil {
		return lookup(c, "Delivery plan step evidence", err)
	}
	if record.Bucket == "" || record.ObjectKey == "" || record.SizeBytes < 1 || record.SizeBytes > 1<<20 {
		return utils.Error(c, http.StatusServiceUnavailable, "Evidence unavailable", "The private evidence object could not be verified")
	}
	object, err := awsrepository.GetS3Object(c.Request().Context(), record.ObjectKey, record.Bucket)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Evidence unavailable", "The private evidence object could not be read")
	}
	body, readErr := io.ReadAll(io.LimitReader(object, (1<<20)+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil || int64(len(body)) != record.SizeBytes {
		return utils.Error(c, http.StatusServiceUnavailable, "Evidence unavailable", "The private evidence object could not be verified")
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != record.SHA256 {
		return utils.Error(c, http.StatusServiceUnavailable, "Evidence unavailable", "The private evidence object could not be verified")
	}
	if !validPlanStepEvidenceFilename(record.FileName) || !validPlanStepEvidenceContentType(record.ContentType) {
		return utils.Error(c, http.StatusServiceUnavailable, "Evidence unavailable", "The private evidence metadata is invalid")
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": record.FileName})
	c.Response().Header().Set(echo.HeaderContentDisposition, disposition)
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	c.Response().Header().Set(echo.HeaderCacheControl, "private, no-store")
	c.Response().Header().Set("Content-Security-Policy", "sandbox")
	return c.Blob(http.StatusOK, record.ContentType, body)
}

func safePlanStepEvidenceDTO(record models.DeliveryPlanStepEvidence) deliveryPlanStepEvidenceDTO {
	return deliveryPlanStepEvidenceDTO{
		ID: record.ID, RequirementKey: record.RequirementKey, FileName: record.FileName, ContentType: record.ContentType,
		SizeBytes: record.SizeBytes, SHA256: record.SHA256, Source: "agent", AutomationTaskID: record.AutomationTaskID,
		RunID: record.RunID, AgentKey: record.AgentKey,
		AgentInstanceID: record.AgentInstanceID, FencingToken: record.FencingToken, CreatedAt: record.CreatedAt.UTC(),
	}
}

func planStepEvidencePageSize(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultPlanStepEvidencePageSize, nil
	}
	size, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || size < 1 || size > maxPlanStepEvidencePageSize || strings.TrimSpace(raw) != strconv.Itoa(size) {
		return 0, errors.New("limit must be an integer from 1 to 100")
	}
	return size, nil
}

func validPlanStepEvidenceRequirementKey(value string) bool {
	if len(value) == 0 || len(value) > 48 {
		return false
	}
	for index, char := range value {
		if !(char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9' || index > 0 && (char == '_' || char == '-')) {
			return false
		}
	}
	return true
}

func validPlanStepEvidenceFilename(value string) bool {
	if len(value) == 0 || len(value) > 160 || strings.Contains(value, "..") || strings.ContainsAny(value, "/\\\r\n\x00") {
		return false
	}
	for _, char := range value {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._ -", char)) {
			return false
		}
	}
	return value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z' || value[0] >= '0' && value[0] <= '9'
}

func validPlanStepEvidenceContentType(value string) bool {
	switch value {
	case "application/json", "image/jpeg", "image/png", "text/csv", "text/markdown", "text/plain":
		return true
	default:
		return false
	}
}

func planStepEvidenceCursorScope(planID, stepID uuid.UUID, requirementKey, runID string) string {
	return planID.String() + "\x00" + stepID.String() + "\x00" + requirementKey + "\x00" + runID
}

func encodePlanStepEvidenceCursor(cursor deliveryPlanStepEvidenceCursor) string {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodePlanStepEvidenceCursor(raw, scope string) (*deliveryPlanStepEvidenceCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxPlanStepEvidenceCursorBytes {
		return nil, errors.New("cursor exceeds its maximum length")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("cursor is malformed")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var cursor deliveryPlanStepEvidenceCursor
	if err := decoder.Decode(&cursor); err != nil {
		return nil, errors.New("cursor is malformed")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("cursor is malformed")
	}
	if cursor.Version != 1 || cursor.Scope != scope || cursor.CreatedAt.IsZero() {
		return nil, errors.New("cursor does not match this evidence query")
	}
	id, err := uuid.FromString(cursor.ID)
	if err != nil || id == uuid.Nil || id.String() != cursor.ID {
		return nil, errors.New("cursor identifier is invalid")
	}
	return &cursor, nil
}
