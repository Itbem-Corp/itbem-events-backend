package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/repositories/awsrepository"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/utils"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxDeliveryPlanStepEvidenceBody = 1 << 20

var (
	stepEvidenceFilenamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,159}$`)
	stepEvidenceJSONSecretPattern = regexp.MustCompile(`(?i)["']?(?:api[_-]?key|access[_-]?(?:key|token)|client[_-]?secret|password|secret[_-]?(?:access[_-]?)?key|bearer[_-]?token)["']?\s*:\s*["']?[A-Za-z0-9/+_=-]{12,}["']?`)
)

var errPlanStepEvidenceObjectUnavailable = errors.New("plan-step evidence object unavailable")
var errPlanStepRequiredEvidenceMissing = errors.New("required plan-step evidence is missing")

type deliveryPlanStepEvidenceUploadIdentity struct {
	StepID         uuid.UUID
	EventID        uuid.UUID
	TaskID         uuid.UUID
	RunID          string
	WorkerID       string
	AgentKey       string
	MachineID      string
	InstanceID     uuid.UUID
	Fence          int64
	RequirementKey string
	FileName       string
	ContentType    string
	SHA256         string
	SizeBytes      int64
}

type deliveryPlanStepEvidenceUploadResponse struct {
	ID             uuid.UUID `json:"id"`
	StepID         uuid.UUID `json:"step_id"`
	RequirementKey string    `json:"requirement_key"`
	FileName       string    `json:"file_name"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	SHA256         string    `json:"sha256"`
	CreatedAt      time.Time `json:"created_at"`
	Idempotent     bool      `json:"idempotent"`
}

// UploadDeliveryPlanStepEvidence is a deliberately narrow binary callback:
// the existing signed callback layer still caps the complete body at 1 MiB.
// It validates the current assignment/lease before and after private object
// storage, scans bounded content for known credential patterns, then records
// only safe metadata in an append-only ledger.
func UploadDeliveryPlanStepEvidence(c echo.Context) error {
	stepID, err := uuid.FromString(strings.TrimSpace(c.Param("id")))
	if err != nil || stepID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan step", "The step identifier is invalid")
	}
	callbackIdentity, ok := requireAgentCallbackIdentity(c)
	if !ok {
		return nil
	}
	query := c.Request().URL.Query()
	workerID := strings.TrimSpace(query.Get("worker_id"))
	agentKey := strings.TrimSpace(query.Get("agent_key"))
	machineID := strings.TrimSpace(query.Get("machine_id"))
	identity, ok := bindCallbackProfileIdentity(c, &agentKey, &machineID)
	if !ok {
		return nil
	}
	taskID, runID, workerIdentity, _, tupleErr := parsePlanStepRuntimeTuple(
		query.Get("task_id"), query.Get("run_id"), workerID, agentKey, machineID, 0,
	)
	fence, fenceErr := parsePlanStepFence(query.Get("fencing_token"))
	eventID, eventErr := parseCanonicalStepEvidenceUUID(query.Get("event_id"))
	if tupleErr != nil || fenceErr != nil || eventErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid step evidence upload", "The task, run, worker, event, or fencing identity is invalid")
	}
	contentType, _, typeErr := mime.ParseMediaType(c.Request().Header.Get(echo.HeaderContentType))
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	signedContentType := strings.ToLower(strings.TrimSpace(query.Get("content_type")))
	fileName := strings.TrimSpace(query.Get("file_name"))
	requirementKey := strings.TrimSpace(query.Get("requirement_key"))
	if typeErr != nil || contentType != signedContentType || !validStepEvidenceContentType(contentType) || !validStepEvidenceFilename(fileName) || !validStepEvidenceRequirementKey(requirementKey) {
		return utils.Error(c, http.StatusBadRequest, "Invalid step evidence upload", "The file metadata does not match the supported evidence format")
	}
	if c.Request().ContentLength > maxDeliveryPlanStepEvidenceBody {
		return utils.Error(c, http.StatusRequestEntityTooLarge, "Step evidence rejected", "The evidence file exceeds the 1 MiB signed callback limit")
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, maxDeliveryPlanStepEvidenceBody+1))
	if err != nil || len(body) == 0 || len(body) > maxDeliveryPlanStepEvidenceBody {
		return utils.Error(c, http.StatusRequestEntityTooLarge, "Step evidence rejected", "The evidence file must be between 1 byte and 1 MiB")
	}
	digest := sha256.Sum256(body)
	sha256Hex := hex.EncodeToString(digest[:])
	if supplied := strings.TrimSpace(strings.ToLower(c.Request().Header.Get("X-Content-SHA256"))); supplied != "" && supplied != sha256Hex {
		return utils.Error(c, http.StatusBadRequest, "Step evidence rejected", "The declared digest does not match the file")
	}
	if !validStepEvidenceContent(body, contentType) || containsHighConfidencePatchSecret(body) || stepEvidenceJSONSecretPattern.Match(body) {
		return utils.Error(c, http.StatusUnprocessableEntity, "Step evidence rejected", "The file type is invalid or the content contains a sensitive credential pattern")
	}
	upload := deliveryPlanStepEvidenceUploadIdentity{
		StepID: stepID, EventID: eventID, TaskID: taskID, RunID: runID,
		WorkerID: workerIdentity.WorkerID, AgentKey: workerIdentity.AgentKey, MachineID: workerIdentity.MachineID,
		InstanceID: callbackIdentity.InstanceID, Fence: fence, RequirementKey: requirementKey,
		FileName: fileName, ContentType: contentType, SHA256: sha256Hex, SizeBytes: int64(len(body)),
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	now := time.Now().UTC()
	var existing *models.DeliveryPlanStepEvidence
	var planVersion int
	err = configuration.DB.WithContext(planStepCallbackContext(c, identity.InstanceID)).Transaction(func(tx *gorm.DB) error {
		if err := validatePlanStepEvidenceLease(tx, upload, now, &planVersion); err != nil {
			return err
		}
		var lookup models.DeliveryPlanStepEvidence
		lookupErr := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&lookup, "event_id = ?", upload.EventID).Error
		if lookupErr == nil {
			if lookup.PlanVersion != planVersion || !samePlanStepEvidenceUpload(lookup, upload) {
				return deliveryplansteps.ErrStepLeaseConflict
			}
			existing = &lookup
			return nil
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		return nil
	})
	if err != nil {
		return planStepRuntimeError(c, "Step evidence rejected", err)
	}
	if existing != nil {
		return utils.Success(c, http.StatusOK, "Step evidence already recorded", safeStepEvidenceUploadResponse(*existing, true))
	}
	cfg, ok := c.Get("config").(*models.Config)
	if !ok || cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" || strings.TrimSpace(cfg.AutomationOutputBucket) != cfg.AutomationOutputBucket {
		return utils.Error(c, http.StatusServiceUnavailable, "Step evidence unavailable", "Private evidence storage is not configured")
	}
	key := planStepEvidenceObjectKey(upload)
	if err := uploadPrivateStepEvidence(c.Request().Context(), body, key, upload.ContentType, cfg.AutomationOutputBucket); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Step evidence unavailable", "The private evidence object could not be stored")
	}
	createdAt := time.Now().UTC()
	record := models.DeliveryPlanStepEvidence{
		EventID: upload.EventID, PlanID: uuid.Nil, PlanVersion: planVersion, StepID: upload.StepID,
		RequirementKey: upload.RequirementKey, AutomationTaskID: upload.TaskID, RunID: upload.RunID,
		WorkerID: upload.WorkerID, AgentKey: upload.AgentKey, MachineID: upload.MachineID,
		AgentInstanceID: upload.InstanceID, FencingToken: upload.Fence,
		FileName: upload.FileName, ContentType: upload.ContentType, Bucket: cfg.AutomationOutputBucket,
		ObjectKey: key, SHA256: upload.SHA256, SizeBytes: upload.SizeBytes, CreatedAt: createdAt,
	}
	var response *models.DeliveryPlanStepEvidence
	idempotent := false
	err = configuration.DB.WithContext(planStepCallbackContext(c, identity.InstanceID)).Transaction(func(tx *gorm.DB) error {
		if err := validatePlanStepEvidenceLease(tx, upload, time.Now().UTC(), &planVersion); err != nil {
			return err
		}
		var lookup models.DeliveryPlanStepEvidence
		lookupErr := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&lookup, "event_id = ?", upload.EventID).Error
		if lookupErr == nil {
			if lookup.PlanVersion != planVersion || !samePlanStepEvidenceUpload(lookup, upload) {
				return deliveryplansteps.ErrStepLeaseConflict
			}
			response = &lookup
			idempotent = true
			return nil
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		var step models.DeliveryPlanStep
		if err := tx.Select("plan_id").First(&step, "id = ?", upload.StepID).Error; err != nil {
			return err
		}
		record.PlanID = step.PlanID
		record.PlanVersion = planVersion
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 1 {
			response = &record
			return nil
		}
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&lookup, "event_id = ?", upload.EventID).Error; err != nil {
			return err
		}
		if lookup.PlanVersion != planVersion || !samePlanStepEvidenceUpload(lookup, upload) {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		response = &lookup
		idempotent = true
		return nil
	})
	if err != nil {
		// A deterministic authorization/fence rejection rolled the transaction
		// back and can safely discard the staged object. For infrastructure or
		// commit errors, leave it in private storage: the commit outcome may be
		// ambiguous, and deleting could break a record that actually committed.
		if errors.Is(err, deliveryplansteps.ErrStepLeaseConflict) || errors.Is(err, deliveryplansteps.ErrStepInputInvalid) || errors.Is(err, gorm.ErrRecordNotFound) {
			_ = awsrepository.DeleteS3Object(context.Background(), key, cfg.AutomationOutputBucket)
		}
		return planStepRuntimeError(c, "Step evidence rejected", err)
	}
	status := http.StatusCreated
	message := "Step evidence recorded"
	if idempotent {
		status = http.StatusOK
		message = "Step evidence already recorded"
	}
	return utils.Success(c, status, message, safeStepEvidenceUploadResponse(*response, idempotent))
}

func validatePlanStepEvidenceLease(tx *gorm.DB, upload deliveryPlanStepEvidenceUploadIdentity, now time.Time, planVersion *int) error {
	tuple := planStepRuntimeTuple{TaskID: upload.TaskID, RunID: upload.RunID, WorkerID: upload.WorkerID, AgentKey: upload.AgentKey, MachineID: upload.MachineID}
	task, err := validatePlanStepRuntimeTask(tx, tuple, now)
	if err != nil {
		return err
	}
	if task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil || task.AgentInstanceID == nil || *task.AgentInstanceID != upload.InstanceID {
		return deliveryplansteps.ErrStepLeaseConflict
	}
	var step models.DeliveryPlanStep
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&step, "id = ?", upload.StepID).Error; err != nil {
		return err
	}
	now = time.Now().UTC()
	if !planStepActivityLeaseMatches(step, *task, upload.RunID, upload.WorkerID, upload.AgentKey, upload.MachineID, upload.Fence, now) {
		return deliveryplansteps.ErrStepLeaseConflict
	}
	var plan models.DeliveryPlan
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&plan, "id = ?", step.PlanID).Error; err != nil {
		return err
	}
	if plan.WorkItemID != *task.DeliveryWorkItemID || plan.Status != "approved" || plan.ApprovedGateID == nil || plan.Version < 1 {
		return deliveryplansteps.ErrStepLeaseConflict
	}
	requirements, err := deliveryplansteps.EvidenceRequirementsFromJSON(step.EvidenceRequirementsJSON)
	if err != nil {
		return deliveryplansteps.ErrStepInputInvalid
	}
	if _, found := findStepEvidenceRequirement(requirements, upload.RequirementKey, upload.ContentType, upload.SizeBytes); !found {
		return deliveryplansteps.ErrStepInputInvalid
	}
	if _, err := validateAssignedPlanStepCallback(tx, *task, upload.StepID); err != nil {
		return err
	}
	if planVersion != nil {
		*planVersion = plan.Version
	}
	return nil
}

func findStepEvidenceRequirement(requirements []deliveryplansteps.EvidenceRequirement, key, contentType string, size int64) (deliveryplansteps.EvidenceRequirement, bool) {
	for _, requirement := range requirements {
		if requirement.Key != key || size < 1 || size > requirement.MaxBytes {
			continue
		}
		for _, allowed := range requirement.ContentTypes {
			if allowed == contentType {
				return requirement, true
			}
		}
	}
	return deliveryplansteps.EvidenceRequirement{}, false
}

func samePlanStepEvidenceUpload(existing models.DeliveryPlanStepEvidence, incoming deliveryPlanStepEvidenceUploadIdentity) bool {
	return existing.StepID == incoming.StepID && existing.EventID == incoming.EventID && existing.AutomationTaskID == incoming.TaskID &&
		existing.RunID == incoming.RunID && existing.WorkerID == incoming.WorkerID && existing.AgentKey == incoming.AgentKey &&
		existing.MachineID == incoming.MachineID && existing.AgentInstanceID == incoming.InstanceID && existing.FencingToken == incoming.Fence &&
		existing.RequirementKey == incoming.RequirementKey && existing.FileName == incoming.FileName && existing.ContentType == incoming.ContentType &&
		existing.SHA256 == incoming.SHA256 && existing.SizeBytes == incoming.SizeBytes
}

func safeStepEvidenceUploadResponse(record models.DeliveryPlanStepEvidence, idempotent bool) deliveryPlanStepEvidenceUploadResponse {
	return deliveryPlanStepEvidenceUploadResponse{
		ID: record.ID, StepID: record.StepID, RequirementKey: record.RequirementKey, FileName: record.FileName,
		ContentType: record.ContentType, SizeBytes: record.SizeBytes, SHA256: record.SHA256, CreatedAt: record.CreatedAt.UTC(), Idempotent: idempotent,
	}
}

func parseCanonicalStepEvidenceUUID(raw string) (uuid.UUID, error) {
	value, err := uuid.FromString(strings.TrimSpace(raw))
	if err != nil || value == uuid.Nil || value.String() != strings.TrimSpace(raw) {
		return uuid.Nil, deliveryplansteps.ErrStepInputInvalid
	}
	return value, nil
}

func validStepEvidenceRequirementKey(value string) bool {
	if len(value) == 0 || len(value) > deliveryplansteps.MaxEvidenceRequirementKeyBytes {
		return false
	}
	for index, char := range value {
		if !(char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9' || index > 0 && (char == '_' || char == '-')) {
			return false
		}
	}
	return true
}

func validStepEvidenceFilename(value string) bool {
	return len(value) > 0 && len(value) <= 160 && !strings.Contains(value, "..") && !strings.ContainsAny(value, "/\\\r\n\x00") && stepEvidenceFilenamePattern.MatchString(value)
}

func validStepEvidenceContentType(value string) bool {
	switch value {
	case "application/json", "image/jpeg", "image/png", "text/csv", "text/markdown", "text/plain":
		return true
	default:
		return false
	}
}

func validStepEvidenceContent(body []byte, contentType string) bool {
	if len(body) == 0 || len(body) > maxDeliveryPlanStepEvidenceBody {
		return false
	}
	switch contentType {
	case "application/json":
		return json.Valid(body)
	case "text/plain", "text/markdown", "text/csv":
		return utf8.Valid(body) && !bytes.ContainsRune(body, 0)
	case "image/png":
		return bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	case "image/jpeg":
		return len(body) >= 3 && body[0] == 0xff && body[1] == 0xd8 && body[2] == 0xff
	default:
		return false
	}
}

func planStepEvidenceObjectKey(upload deliveryPlanStepEvidenceUploadIdentity) string {
	return "delivery-step-evidence/" + upload.TaskID.String() + "/runs/" + upload.RunID + "/steps/" + upload.StepID.String() + "/evidence/" + upload.EventID.String() + "-" + upload.SHA256
}

var putPrivateStepEvidenceObject = func(ctx context.Context, body []byte, key, contentType, bucket string) error {
	client := configuration.GetS3Client(nil)
	if client == nil {
		return errPlanStepEvidenceObjectUnavailable
	}
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body))),
		ContentType: aws.String(contentType), ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	})
	return err
}

func uploadPrivateStepEvidence(ctx context.Context, body []byte, key, contentType, bucket string) error {
	if len(body) == 0 || len(body) > maxDeliveryPlanStepEvidenceBody || strings.TrimSpace(bucket) == "" || strings.TrimSpace(key) == "" {
		return errPlanStepEvidenceObjectUnavailable
	}
	return putPrivateStepEvidenceObject(ctx, body, key, contentType, bucket)
}

func requirePlanStepEvidenceRequirements(tx *gorm.DB, step models.DeliveryPlanStep, planVersion int, task models.AutomationTask, runID string, identity automationagent.AgentIdentity, fence int64, instanceID uuid.UUID) error {
	if step.ID == uuid.Nil {
		return errPlanStepRequiredEvidenceMissing
	}
	requirements, err := deliveryplansteps.EvidenceRequirementsFromJSON(step.EvidenceRequirementsJSON)
	if err != nil {
		return errPlanStepRequiredEvidenceMissing
	}
	for _, requirement := range requirements {
		if !requirement.Required {
			continue
		}
		if tx == nil || step.PlanID == uuid.Nil || planVersion < 1 || task.ID == uuid.Nil || strings.TrimSpace(runID) == "" || fence < 1 || instanceID == uuid.Nil ||
			strings.TrimSpace(identity.WorkerID) == "" || strings.TrimSpace(identity.AgentKey) == "" || strings.TrimSpace(identity.MachineID) == "" {
			return errPlanStepRequiredEvidenceMissing
		}
		var count int64
		if err := tx.Model(&models.DeliveryPlanStepEvidence{}).
			Where(`plan_id = ? AND plan_version = ? AND step_id = ? AND requirement_key = ? AND automation_task_id = ? AND run_id = ? AND worker_id = ? AND agent_key = ? AND machine_id = ? AND agent_instance_id = ? AND fencing_token = ?`,
				step.PlanID, planVersion, step.ID, requirement.Key, task.ID, runID, identity.WorkerID, identity.AgentKey, identity.MachineID, instanceID, fence).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return errPlanStepRequiredEvidenceMissing
		}
	}
	return nil
}
