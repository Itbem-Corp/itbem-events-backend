package automation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	attemptPolicySigningKeyEnv         = "AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY"
	attemptPolicyPreviousSigningKeyEnv = "AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY_PREVIOUS"
	maxAutomationInferenceCallsPerRun  = 6
)

// inferenceAttemptCallQuota mirrors deliveryRunBudgetReservationForRoutes:
// implementation admission holds six calls, QA holds one primary plus one
// Stagehand call, ordinary operations hold one, and publish is deterministic.
func inferenceAttemptCallQuota(operation string) int {
	switch strings.TrimSpace(operation) {
	case "delivery.implementation":
		return 6
	case "delivery.qa":
		return 2
	case "delivery.publish":
		return 0
	default:
		return 1
	}
}

// freezeAutomationInferenceAttemptPolicy binds the current credential-free AI
// route policy to one task lease. It is called in the same transaction that
// claims or recovers a task. Existing rows are verified and reused, never
// replaced; a different RunID therefore receives a fresh policy snapshot.
func freezeAutomationInferenceAttemptPolicy(tx *gorm.DB, task models.AutomationTask, runID string, now time.Time) (models.AutomationInferenceAttemptPolicy, error) {
	var empty models.AutomationInferenceAttemptPolicy
	if tx == nil || task.ID == uuid.Nil || strings.TrimSpace(runID) == "" || len(runID) > 64 || strings.TrimSpace(task.Operation) == "" || len(task.Operation) > 96 || task.MaxCompletionTokens < 0 {
		return empty, errors.New("automation inference attempt identity is invalid")
	}
	runID = strings.TrimSpace(runID)
	projectID, err := inferenceAttemptProjectID(tx, task.DeliveryWorkItemID)
	if err != nil {
		return empty, err
	}

	var existing models.AutomationInferenceAttemptPolicy
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("automation_task_id = ? AND run_id = ?", task.ID, runID).Take(&existing).Error
	if err == nil {
		if !inferenceAttemptPolicyMatches(existing, task, runID, projectID) {
			return empty, errors.New("automation inference attempt binding is immutable")
		}
		if _, _, valid := validateAutomationInferenceAttemptPolicy(existing); !valid || !verifyAutomationInferenceAttemptPolicySignature(existing) {
			return empty, errors.New("automation inference attempt snapshot is invalid")
		}
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return empty, err
	}

	routesJSON := "[]"
	routesHash := sha256Hex([]byte(routesJSON))
	var revision int64
	if policy, configured := actionPolicyForOperation(tx, task.Operation); configured {
		routes, routesErr := policy.Routes()
		if routesErr != nil {
			return empty, fmt.Errorf("automation inference action policy is invalid: %w", routesErr)
		}
		routesJSON, routesHash, err = canonicalInferenceRoutes(routes)
		if err != nil {
			return empty, err
		}
		revision = policy.Revision
	}

	snapshot := models.AutomationInferenceAttemptPolicy{
		AutomationTaskID:    task.ID,
		RunID:               runID,
		Operation:           task.Operation,
		ProjectID:           projectID,
		PolicyRevision:      revision,
		RoutesJSON:          routesJSON,
		RoutesHash:          routesHash,
		MaxCompletionTokens: task.MaxCompletionTokens,
		MaxInferenceCalls:   inferenceAttemptCallQuota(task.Operation),
		CreatedAt:           now.UTC(),
	}
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
		return empty, err
	}

	// A unique conflict is expected only if another claim froze this same
	// attempt concurrently. DO NOTHING preserves append-only semantics; reload
	// below then proves the winning record has the same binding.
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "automation_task_id"}, {Name: "run_id"}},
		DoNothing: true,
	}).Create(&snapshot).Error; err != nil {
		return empty, err
	}
	var persisted models.AutomationInferenceAttemptPolicy
	if err := tx.Where("automation_task_id = ? AND run_id = ?", task.ID, runID).Take(&persisted).Error; err != nil {
		return empty, err
	}
	if !inferenceAttemptPolicyMatches(persisted, task, runID, projectID) {
		return empty, errors.New("automation inference attempt binding is immutable")
	}
	if _, _, valid := validateAutomationInferenceAttemptPolicy(persisted); !valid || !verifyAutomationInferenceAttemptPolicySignature(persisted) {
		return empty, errors.New("automation inference attempt snapshot is invalid")
	}
	return persisted, nil
}

func readAutomationInferenceAttemptPolicy(tx *gorm.DB, taskID uuid.UUID, runID string) (models.AutomationInferenceAttemptPolicy, error) {
	var snapshot models.AutomationInferenceAttemptPolicy
	if tx == nil || taskID == uuid.Nil || strings.TrimSpace(runID) == "" || len(runID) > 64 {
		return snapshot, errors.New("automation inference attempt identity is invalid")
	}
	err := tx.Where("automation_task_id = ? AND run_id = ?", taskID, strings.TrimSpace(runID)).Take(&snapshot).Error
	return snapshot, err
}

// validateAutomationInferenceAttemptPolicy checks both the canonical route
// list and the complete attempt binding hash before any credential is read.
func validateAutomationInferenceAttemptPolicy(snapshot models.AutomationInferenceAttemptPolicy) ([]models.AutomationAIActionRoute, string, bool) {
	if snapshot.ID == uuid.Nil || snapshot.AutomationTaskID == uuid.Nil || strings.TrimSpace(snapshot.RunID) == "" || len(snapshot.RunID) > 64 ||
		strings.TrimSpace(snapshot.Operation) == "" || len(snapshot.Operation) > 96 || snapshot.MaxCompletionTokens < 0 || snapshot.MaxInferenceCalls < 0 || snapshot.MaxInferenceCalls > maxAutomationInferenceCallsPerRun || snapshot.MaxInferenceCalls != inferenceAttemptCallQuota(snapshot.Operation) ||
		snapshot.CreatedAt.IsZero() || strings.TrimSpace(snapshot.SnapshotHash) == "" {
		return nil, "", false
	}
	routesJSON, routesHash, err := canonicalStoredInferenceRoutes(snapshot.RoutesJSON)
	if err != nil || routesHash != snapshot.RoutesHash || inferenceAttemptSnapshotHash(snapshot) != snapshot.SnapshotHash {
		return nil, "", false
	}
	var routes []models.AutomationAIActionRoute
	if err := json.Unmarshal([]byte(routesJSON), &routes); err != nil {
		return nil, "", false
	}
	return routes, routesHash, true
}

// PostgreSQL jsonb rewrites whitespace and object-key order on read. Parse the
// stored routing value strictly, then hash and sign its canonical typed form
// so those harmless database rewrites do not invalidate a frozen attempt.
func canonicalStoredInferenceRoutes(raw string) (string, string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var routes []models.AutomationAIActionRoute
	if err := decoder.Decode(&routes); err != nil || routes == nil || decoder.Decode(&struct{}{}) != io.EOF || len(routes) > models.MaxAutomationAIActionRoutes {
		return "", "", errors.New("automation inference attempt routes are invalid")
	}
	if len(routes) == 0 {
		canonical := "[]"
		return canonical, sha256Hex([]byte(canonical)), nil
	}
	return canonicalInferenceRoutes(routes)
}

func inferenceAttemptPolicyMatches(snapshot models.AutomationInferenceAttemptPolicy, task models.AutomationTask, runID string, projectID *uuid.UUID) bool {
	if snapshot.AutomationTaskID != task.ID || snapshot.RunID != strings.TrimSpace(runID) || snapshot.Operation != task.Operation ||
		snapshot.MaxCompletionTokens != task.MaxCompletionTokens || snapshot.MaxInferenceCalls != inferenceAttemptCallQuota(task.Operation) {
		return false
	}
	if snapshot.ProjectID == nil || projectID == nil {
		return snapshot.ProjectID == nil && projectID == nil
	}
	return *snapshot.ProjectID == *projectID
}

func inferenceAttemptProjectID(tx *gorm.DB, workItemID *uuid.UUID) (*uuid.UUID, error) {
	if workItemID == nil {
		return nil, nil
	}
	if *workItemID == uuid.Nil {
		return nil, errors.New("automation inference project scope is invalid")
	}
	var item struct {
		ProjectID uuid.UUID `gorm:"column:project_id"`
	}
	if err := tx.Table("delivery_work_items").Select("project_id").Where("id = ?", *workItemID).Take(&item).Error; err != nil || item.ProjectID == uuid.Nil {
		return nil, errors.New("automation inference project scope is invalid")
	}
	projectID := item.ProjectID
	return &projectID, nil
}

func inferenceAttemptSnapshotHash(snapshot models.AutomationInferenceAttemptPolicy) string {
	projectID := ""
	if snapshot.ProjectID != nil {
		projectID = snapshot.ProjectID.String()
	}
	routesJSON := snapshot.RoutesJSON
	if canonical, _, err := canonicalStoredInferenceRoutes(snapshot.RoutesJSON); err == nil {
		routesJSON = canonical
	}
	payload := struct {
		TaskID              string `json:"automation_task_id"`
		RunID               string `json:"run_id"`
		Operation           string `json:"operation"`
		ProjectID           string `json:"project_id"`
		PolicyRevision      int64  `json:"policy_revision"`
		RoutesJSON          string `json:"routes_json"`
		RoutesHash          string `json:"routes_hash"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		MaxInferenceCalls   int    `json:"max_inference_calls"`
	}{
		TaskID: snapshot.AutomationTaskID.String(), RunID: snapshot.RunID, Operation: snapshot.Operation,
		ProjectID: projectID, PolicyRevision: snapshot.PolicyRevision, RoutesJSON: routesJSON,
		RoutesHash: snapshot.RoutesHash, MaxCompletionTokens: snapshot.MaxCompletionTokens, MaxInferenceCalls: snapshot.MaxInferenceCalls,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return sha256Hex(encoded)
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// signAutomationInferenceAttemptPolicy adds an HMAC generated only by the
// control plane. The callback secret is intentionally not reused here because
// it is present on local workers. A keyed signature makes a frozen route
// recipe tamper-evident even if a database writer recomputes its SHA-256 hash.
func signAutomationInferenceAttemptPolicy(snapshot *models.AutomationInferenceAttemptPolicy) error {
	if snapshot == nil {
		return errors.New("automation inference attempt snapshot is invalid")
	}
	key, keyID, ok := activeAttemptPolicySigningKey()
	if !ok {
		return errors.New("automation inference attempt signing is unavailable")
	}
	snapshot.SignatureKeyID = keyID
	snapshot.SnapshotSignature = attemptPolicyMAC(key, snapshot.SnapshotHash)
	return nil
}

func verifyAutomationInferenceAttemptPolicySignature(snapshot models.AutomationInferenceAttemptPolicy) bool {
	if snapshot.SignatureKeyID == "" || len(snapshot.SnapshotSignature) != sha256.Size*2 {
		return false
	}
	provided, err := hex.DecodeString(snapshot.SnapshotSignature)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	current, currentID, currentOK := activeAttemptPolicySigningKey()
	if currentOK && snapshot.SignatureKeyID == currentID && hmac.Equal(provided, attemptPolicyMACBytes(current, snapshot.SnapshotHash)) {
		return true
	}
	previous, previousID, previousOK := previousAttemptPolicySigningKey()
	return previousOK && snapshot.SignatureKeyID == previousID && hmac.Equal(provided, attemptPolicyMACBytes(previous, snapshot.SnapshotHash))
}

func activeAttemptPolicySigningKey() ([]byte, string, bool) {
	return parseAttemptPolicySigningKey(os.Getenv(attemptPolicySigningKeyEnv))
}

func previousAttemptPolicySigningKey() ([]byte, string, bool) {
	return parseAttemptPolicySigningKey(os.Getenv(attemptPolicyPreviousSigningKeyEnv))
}

func parseAttemptPolicySigningKey(raw string) ([]byte, string, bool) {
	key := []byte(strings.TrimSpace(raw))
	if len(key) < 32 {
		return nil, "", false
	}
	digest := sha256.Sum256(key)
	return key, hex.EncodeToString(digest[:8]), true
}

func attemptPolicyMAC(key []byte, snapshotHash string) string {
	return hex.EncodeToString(attemptPolicyMACBytes(key, snapshotHash))
}

func attemptPolicyMACBytes(key []byte, snapshotHash string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("itbem/automation/inference-attempt-policy/v1." + snapshotHash))
	return mac.Sum(nil)
}
