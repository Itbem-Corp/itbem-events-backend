package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"
	"github.com/gofrs/uuid"
)

const (
	maxPlanStepCallbackRequestBytes  = 64 << 10
	maxPlanStepCallbackResponseBytes = 1 << 20
	maxDependencyPatchManifestBytes  = 64 << 10
	minPlanStepLeaseSeconds          = 15
	maxPlanStepLeaseSeconds          = 300
	maxPlanStepFencingTokenBytes     = 256
	maxPlanStepEvidenceUploadBytes   = 1 << 20
	planStepCallbackTimeout          = 15 * time.Second
)

// Keep the plan-step key contract aligned with services/deliveryplansteps.
var planStepKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var planStepAgentKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

// ErrPlanStepLeaseLost is returned when the server's fence no longer matches
// the step's current owner. The caller must stop work rather than retrying the
// same mutation with a stale token.
var ErrPlanStepLeaseLost = errors.New("automation plan-step lease is no longer owned")

// PlanStepClaimRequest asks the control plane to atomically claim one ready
// step whose dependencies are complete. A retry with the same task and run
// identity is safe; the server is responsible for returning the existing
// claim, rather than allocating a second step to that run.
type PlanStepClaimRequest struct {
	TaskID       string `json:"task_id"`
	PlanID       string `json:"plan_id"`
	RunID        string `json:"run_id"`
	WorkerID     string `json:"worker_id"`
	AgentKey     string `json:"agent_key"`
	MachineID    string `json:"machine_id,omitempty"`
	StepID       string `json:"step_id,omitempty"`
	LeaseSeconds int    `json:"lease_seconds"`
}

// PlanStepDTO mirrors the explicit safe step projection returned by the API.
// It excludes the fencing token, machine identity, private prompts, and
// execution payloads; the token is carried separately in PlanStepClaim.
type PlanStepDTO struct {
	ID                    string                                  `json:"id"`
	PlanID                string                                  `json:"plan_id"`
	PlanVersion           int                                     `json:"plan_version"`
	StepKey               string                                  `json:"step_key"`
	Role                  string                                  `json:"role"`
	Order                 int                                     `json:"order"`
	Title                 string                                  `json:"title"`
	Objective             string                                  `json:"objective"`
	AcceptanceCriteria    []string                                `json:"acceptance_criteria"`
	EvidenceRequirements  []deliveryplansteps.EvidenceRequirement `json:"evidence_requirements,omitempty"`
	DependsOn             []string                                `json:"depends_on"`
	Status                string                                  `json:"status"`
	AgentKey              string                                  `json:"agent_key,omitempty"`
	AutomationTaskID      string                                  `json:"automation_task_id,omitempty"`
	AutomationExecutionID string                                  `json:"automation_execution_id,omitempty"`
	StartedAt             *time.Time                              `json:"started_at,omitempty"`
	CompletedAt           *time.Time                              `json:"completed_at,omitempty"`
	CreatedAt             time.Time                               `json:"created_at"`
	UpdatedAt             time.Time                               `json:"updated_at"`
}

// PlanStepEvidenceUploadReceipt is the safe metadata-only acknowledgement for
// a worker evidence upload. It deliberately has no storage bucket, object key,
// presigned URL, or credential field.
type PlanStepEvidenceUploadReceipt struct {
	ID             string    `json:"id"`
	StepID         string    `json:"step_id"`
	RequirementKey string    `json:"requirement_key"`
	FileName       string    `json:"file_name"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	SHA256         string    `json:"sha256"`
	CreatedAt      time.Time `json:"created_at"`
	Idempotent     bool      `json:"idempotent"`
}

type PlanStepClaim struct {
	Available      bool         `json:"available"`
	Step           *PlanStepDTO `json:"step,omitempty"`
	FencingToken   string       `json:"fencing_token"`
	LeaseExpiresAt time.Time    `json:"lease_expires_at"`
}

type PlanStepLeaseRequest struct {
	StepID       string `json:"-"`
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	WorkerID     string `json:"worker_id"`
	AgentKey     string `json:"agent_key"`
	MachineID    string `json:"machine_id,omitempty"`
	LeaseSeconds int    `json:"lease_seconds,omitempty"`
	FencingToken string `json:"fencing_token"`
}

type PlanStepLease struct {
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type PlanStepStatusRequest struct {
	StepID       string `json:"-"`
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	WorkerID     string `json:"worker_id"`
	AgentKey     string `json:"agent_key"`
	MachineID    string `json:"machine_id,omitempty"`
	FencingToken string `json:"fencing_token"`
	Status       string `json:"status"`
}

// PlanStepDependencyPatchManifest contains only the bounded, safe metadata
// needed to fetch direct-dependency patch bytes through the signed callback.
type PlanStepDependencyPatchManifest struct {
	ManifestSHA256 string                                            `json:"manifest_sha256"`
	PatchCount     int                                               `json:"patch_count"`
	TotalSizeBytes int64                                             `json:"total_size_bytes"`
	Patches        []models.DeliveryPlanStepDependencyPatchReference `json:"patches"`
}

// ClaimPlanStep claims one dependency-ready step. The server response is
// deliberately limited to a safe step DTO plus the run-scoped fence and lease.
func (c *HTTPCallback) ClaimPlanStep(ctx context.Context, claim PlanStepClaimRequest) (PlanStepClaim, error) {
	if err := validatePlanStepClaimRequest(claim); err != nil {
		return PlanStepClaim{}, err
	}
	var response PlanStepClaim
	if err := c.doPlanStepRequest(ctx, http.MethodPost, "/api/internal/automation/steps/claim", claim, "claim", &response); err != nil {
		return PlanStepClaim{}, err
	}
	if !response.Available {
		if response.Step != nil || response.FencingToken != "" || !response.LeaseExpiresAt.IsZero() {
			return PlanStepClaim{}, fmt.Errorf("automation step claim response is invalid")
		}
		return response, nil
	}
	if response.Step == nil || validatePlanStepDTO(*response.Step) != nil || response.Step.Status != "running" ||
		!samePlanStepCallbackUUID(response.Step.PlanID, claim.PlanID) ||
		!samePlanStepCallbackUUID(response.Step.AutomationTaskID, claim.TaskID) ||
		strings.TrimSpace(response.Step.AgentKey) != strings.TrimSpace(claim.AgentKey) ||
		!validPlanStepFencingToken(response.FencingToken) || response.LeaseExpiresAt.IsZero() {
		return PlanStepClaim{}, fmt.Errorf("automation step claim response is invalid")
	}
	return response, nil
}

// RenewPlanStepLease renews the current step lease using the exact same
// task/run/worker tuple and fencing token. A 409 means this process no longer
// owns the step; response bodies are intentionally not surfaced.
func (c *HTTPCallback) RenewPlanStepLease(ctx context.Context, lease PlanStepLeaseRequest) (PlanStepLease, error) {
	if err := validatePlanStepLeaseRequest(lease); err != nil {
		return PlanStepLease{}, err
	}
	var response PlanStepLease
	path := "/api/internal/automation/steps/" + lease.StepID + "/lease"
	if err := c.doPlanStepRequest(ctx, http.MethodPut, path, lease, "lease renewal", &response); err != nil {
		return PlanStepLease{}, err
	}
	if response.LeaseExpiresAt.IsZero() {
		return PlanStepLease{}, fmt.Errorf("automation step lease response is invalid")
	}
	return response, nil
}

// UpdatePlanStepStatus writes an idempotent state update fenced to the lease
// that claimed the step. The server returns the safe DTO as the envelope data.
func (c *HTTPCallback) UpdatePlanStepStatus(ctx context.Context, update PlanStepStatusRequest) (PlanStepDTO, error) {
	if err := validatePlanStepStatusRequest(update); err != nil {
		return PlanStepDTO{}, err
	}
	var response PlanStepDTO
	path := "/api/internal/automation/steps/" + update.StepID
	if err := c.doPlanStepRequest(ctx, http.MethodPut, path, update, "status update", &response); err != nil {
		return PlanStepDTO{}, err
	}
	if err := validatePlanStepDTO(response); err != nil || response.ID != update.StepID || response.Status != strings.TrimSpace(update.Status) {
		return PlanStepDTO{}, fmt.Errorf("automation step status response is invalid")
	}
	return response, nil
}

// GetPlanStepDependencyPatchManifest returns the API-authoritative manifest
// for direct, completed dependencies after this step has been claimed.
func (c *HTTPCallback) GetPlanStepDependencyPatchManifest(ctx context.Context, lease PlanStepLeaseRequest) (PlanStepDependencyPatchManifest, error) {
	if err := validatePlanStepLeaseRequest(lease); err != nil {
		return PlanStepDependencyPatchManifest{}, err
	}
	path := "/api/internal/automation/steps/" + lease.StepID + "/dependency-patches/manifest"
	var manifest PlanStepDependencyPatchManifest
	if err := c.doPlanStepRequestWithLimit(ctx, http.MethodPost, path, lease, "dependency patch manifest", maxDependencyPatchManifestBytes, &manifest); err != nil {
		return PlanStepDependencyPatchManifest{}, err
	}
	if err := validatePlanStepDependencyPatchManifest(manifest); err != nil {
		return PlanStepDependencyPatchManifest{}, err
	}
	return manifest, nil
}

// GetPlanStepDependencyPatch retrieves raw patch bytes over the authenticated
// control-plane callback. It intentionally never consumes storage references.
func (c *HTTPCallback) GetPlanStepDependencyPatch(ctx context.Context, lease PlanStepLeaseRequest, digest string) ([]byte, error) {
	if err := validatePlanStepLeaseRequest(lease); err != nil || !validPlanStepSHA256(digest) {
		return nil, fmt.Errorf("automation dependency patch identity is invalid")
	}
	body, err := json.Marshal(lease)
	if err != nil || len(body) == 0 || len(body) > maxPlanStepCallbackRequestBytes {
		return nil, fmt.Errorf("automation dependency patch request is invalid")
	}
	requestContext, cancel := context.WithTimeout(ctx, planStepCallbackTimeout)
	defer cancel()
	path := "/api/internal/automation/steps/" + lease.StepID + "/dependency-patches/" + digest
	req, err := c.newSignedRequest(requestContext, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("automation dependency patch request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusConflict {
			return nil, ErrPlanStepLeaseLost
		}
		return nil, fmt.Errorf("automation dependency patch request rejected (%d)", response.StatusCode)
	}
	if response.Header.Get("Content-Type") != "application/vnd.git-patch" || response.Header.Get("Cache-Control") != "no-store" || !strings.EqualFold(response.Header.Get("X-Content-Type-Options"), "nosniff") || hasDependencyPatchStorageHeaders(response.Header) {
		return nil, fmt.Errorf("automation dependency patch response headers are invalid")
	}
	patch, err := io.ReadAll(io.LimitReader(response.Body, models.MaxDeliveryPlanStepPatchArtifactBytes+1))
	if err != nil {
		wipeBytes(patch)
		return nil, fmt.Errorf("automation dependency patch response could not be read")
	}
	if len(patch) == 0 || len(patch) > models.MaxDeliveryPlanStepPatchArtifactBytes {
		wipeBytes(patch)
		return nil, fmt.Errorf("automation dependency patch response exceeds the allowed size")
	}
	digestBytes := sha256.Sum256(patch)
	if hex.EncodeToString(digestBytes[:]) != digest {
		wipeBytes(patch)
		return nil, fmt.Errorf("automation dependency patch digest is invalid")
	}
	return patch, nil
}

// UploadPlanStepEvidence sends one bounded raw artifact to the signed control
// plane callback. Both the exact MIME type and raw bytes are covered by the
// Ed25519 signature; no credentials or object-storage references are returned.
func (c *HTTPCallback) UploadPlanStepEvidence(ctx context.Context, lease PlanStepLeaseRequest, requirementKey, fileName, contentType, eventID string, body []byte) (PlanStepEvidenceUploadReceipt, error) {
	if err := validatePlanStepLeaseRequest(lease); err != nil || !validPlanEvidenceKey(requirementKey) || !validPlanEvidenceFileName(fileName) || !validPlanEvidenceEventID(eventID) || !validPlanEvidenceContentType(contentType) || len(body) == 0 || len(body) > maxPlanStepEvidenceUploadBytes || scanPlanStepEvidenceBytes(body) != nil {
		return PlanStepEvidenceUploadReceipt{}, fmt.Errorf("automation step evidence identity or size is invalid")
	}
	if c == nil || c.client == nil || c.baseURL == "" || len(c.identity.privateKey) == 0 || c.instanceID == "" {
		return PlanStepEvidenceUploadReceipt{}, fmt.Errorf("automation step callback is unavailable")
	}
	if lease.MachineID != c.identity.machineID {
		return PlanStepEvidenceUploadReceipt{}, fmt.Errorf("automation step evidence machine identity is invalid")
	}
	query := url.Values{}
	query.Set("task_id", lease.TaskID)
	query.Set("run_id", lease.RunID)
	query.Set("worker_id", lease.WorkerID)
	query.Set("agent_key", lease.AgentKey)
	query.Set("machine_id", lease.MachineID)
	query.Set("agent_instance_id", c.instanceID)
	query.Set("fencing_token", lease.FencingToken)
	query.Set("event_id", eventID)
	query.Set("requirement_key", requirementKey)
	query.Set("file_name", fileName)
	query.Set("content_type", contentType)
	path := "/api/internal/automation/steps/" + lease.StepID + "/evidence?" + query.Encode()
	requestContext, cancel := context.WithTimeout(ctx, planStepCallbackTimeout)
	defer cancel()
	req, err := c.newSignedRequest(requestContext, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return PlanStepEvidenceUploadReceipt{}, err
	}
	req.Header.Set("Content-Type", contentType)
	digest := sha256.Sum256(body)
	req.Header.Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
	response, err := c.client.Do(req)
	if err != nil {
		return PlanStepEvidenceUploadReceipt{}, fmt.Errorf("automation step evidence upload failed")
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxPlanStepCallbackRequestBytes+1))
	if err != nil || len(responseBody) > maxPlanStepCallbackRequestBytes {
		return PlanStepEvidenceUploadReceipt{}, fmt.Errorf("automation step evidence response is invalid")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		if response.StatusCode == http.StatusConflict {
			return PlanStepEvidenceUploadReceipt{}, ErrPlanStepLeaseLost
		}
		return PlanStepEvidenceUploadReceipt{}, fmt.Errorf("automation step evidence upload rejected (%d)", response.StatusCode)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return PlanStepEvidenceUploadReceipt{}, fmt.Errorf("automation step evidence response envelope is invalid")
	}
	var receipt PlanStepEvidenceUploadReceipt
	decoder := json.NewDecoder(strings.NewReader(string(envelope.Data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !validStepCallbackUUID(receipt.ID) || receipt.StepID != lease.StepID || receipt.RequirementKey != requirementKey || receipt.FileName != fileName || receipt.ContentType != contentType || receipt.SizeBytes != int64(len(body)) || receipt.SHA256 != hex.EncodeToString(digest[:]) || receipt.CreatedAt.IsZero() {
		return PlanStepEvidenceUploadReceipt{}, fmt.Errorf("automation step evidence response data is invalid")
	}
	return receipt, nil
}

func validPlanEvidenceKey(value string) bool {
	return len(value) > 0 && len(value) <= deliveryplansteps.MaxEvidenceRequirementKeyBytes && planEvidenceRequirementKeyPattern.MatchString(value)
}

var planEvidenceRequirementKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
var planEvidenceFileNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,119}$`)

func validPlanEvidenceFileName(value string) bool {
	return len(value) > 0 && planEvidenceFileNamePattern.MatchString(value) && value != "." && value != ".."
}

func validPlanEvidenceEventID(value string) bool { return validStepCallbackUUID(value) }

func validPlanEvidenceContentType(value string) bool {
	for _, allowed := range []string{"application/json", "image/jpeg", "image/png", "text/csv", "text/markdown", "text/plain"} {
		if value == allowed {
			return true
		}
	}
	return false
}

func hasDependencyPatchStorageHeaders(header http.Header) bool {
	if header.Get("Location") != "" || header.Get("Content-Location") != "" {
		return true
	}
	for name := range header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") || strings.HasPrefix(lower, "x-s3-") || strings.Contains(lower, "storage") || strings.Contains(lower, "bucket") || strings.Contains(lower, "object-key") {
			return true
		}
	}
	return false
}

func (c *HTTPCallback) doPlanStepRequest(ctx context.Context, method, path string, payload any, operation string, destination any) error {
	return c.doPlanStepRequestWithLimit(ctx, method, path, payload, operation, maxPlanStepCallbackResponseBytes, destination)
}

func (c *HTTPCallback) doPlanStepRequestWithLimit(ctx context.Context, method, path string, payload any, operation string, responseLimit int64, destination any) error {
	if c == nil || c.client == nil || c.baseURL == "" || len(c.identity.privateKey) == 0 || c.instanceID == "" {
		return fmt.Errorf("automation step callback is unavailable")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("automation step %s request could not be encoded", operation)
	}
	if len(body) == 0 || len(body) > maxPlanStepCallbackRequestBytes {
		return fmt.Errorf("automation step %s request exceeds the allowed size", operation)
	}
	requestContext, cancel := context.WithTimeout(ctx, planStepCallbackTimeout)
	defer cancel()
	req, err := c.newSignedRequest(requestContext, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("automation step %s request failed", operation)
	}
	defer response.Body.Close()
	if responseLimit < 1 || responseLimit > maxPlanStepCallbackResponseBytes {
		return fmt.Errorf("automation step %s response limit is invalid", operation)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return fmt.Errorf("automation step %s response could not be read", operation)
	}
	if int64(len(responseBody)) > responseLimit {
		return fmt.Errorf("automation step %s response exceeds the allowed size", operation)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		// Do not include the response body: proxies and application errors can
		// contain secrets, private plan details, or echoed request data.
		if response.StatusCode == http.StatusConflict && operation != "claim" {
			return ErrPlanStepLeaseLost
		}
		return fmt.Errorf("automation step %s rejected (%d)", operation, response.StatusCode)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return fmt.Errorf("automation step %s response envelope is invalid", operation)
	}
	if err := json.Unmarshal(envelope.Data, destination); err != nil {
		return fmt.Errorf("automation step %s response data is invalid", operation)
	}
	return nil
}

func validatePlanStepDependencyPatchManifest(manifest PlanStepDependencyPatchManifest) error {
	if manifest.PatchCount != len(manifest.Patches) || manifest.PatchCount < 0 || manifest.PatchCount > models.MaxDeliveryPlanStepDependencyPatchReferences || manifest.TotalSizeBytes < 0 || manifest.TotalSizeBytes > models.MaxDeliveryPlanStepDependencyPatchBytes {
		return fmt.Errorf("automation dependency patch manifest is invalid")
	}
	var total int64
	for _, patch := range manifest.Patches {
		if patch.SizeBytes < 1 || patch.SizeBytes > models.MaxDeliveryPlanStepPatchArtifactBytes || total > models.MaxDeliveryPlanStepDependencyPatchBytes-patch.SizeBytes {
			return fmt.Errorf("automation dependency patch manifest is invalid")
		}
		total += patch.SizeBytes
	}
	if total != manifest.TotalSizeBytes {
		return fmt.Errorf("automation dependency patch manifest is invalid")
	}
	digest, err := models.DeliveryPlanStepDependencyPatchManifestSHA256(manifest.Patches)
	if err != nil || digest != manifest.ManifestSHA256 {
		return fmt.Errorf("automation dependency patch manifest digest is invalid")
	}
	return nil
}

func validPlanStepSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func validatePlanStepClaimRequest(request PlanStepClaimRequest) error {
	if !validStepCallbackUUID(request.TaskID) || !validStepCallbackUUID(request.PlanID) || !validStepCallbackUUID(request.RunID) || !validStepCallbackUUID(request.WorkerID) {
		return fmt.Errorf("automation step claim identity is invalid")
	}
	if !validPlanStepWorkerIdentity(request.AgentKey, request.MachineID) {
		return fmt.Errorf("automation step claim worker profile is invalid")
	}
	if strings.TrimSpace(request.StepID) != "" && !validStepCallbackUUID(request.StepID) {
		return fmt.Errorf("automation step claim target is invalid")
	}
	if request.LeaseSeconds < minPlanStepLeaseSeconds || request.LeaseSeconds > maxPlanStepLeaseSeconds {
		return fmt.Errorf("automation step lease duration must be between %d and %d seconds", minPlanStepLeaseSeconds, maxPlanStepLeaseSeconds)
	}
	return nil
}

func validatePlanStepLeaseRequest(request PlanStepLeaseRequest) error {
	if !validStepCallbackUUID(request.StepID) || !validStepCallbackUUID(request.TaskID) || !validStepCallbackUUID(request.RunID) || !validStepCallbackUUID(request.WorkerID) || !validPlanStepFencingToken(request.FencingToken) {
		return fmt.Errorf("automation step lease identity is invalid")
	}
	if !validPlanStepWorkerIdentity(request.AgentKey, request.MachineID) {
		return fmt.Errorf("automation step lease worker profile is invalid")
	}
	if request.LeaseSeconds != 0 && (request.LeaseSeconds < minPlanStepLeaseSeconds || request.LeaseSeconds > maxPlanStepLeaseSeconds) {
		return fmt.Errorf("automation step lease duration must be between %d and %d seconds", minPlanStepLeaseSeconds, maxPlanStepLeaseSeconds)
	}
	return nil
}

func validatePlanStepStatusRequest(request PlanStepStatusRequest) error {
	if err := validatePlanStepLeaseRequest(PlanStepLeaseRequest{
		StepID: request.StepID, TaskID: request.TaskID, RunID: request.RunID, WorkerID: request.WorkerID,
		AgentKey: request.AgentKey, MachineID: request.MachineID, FencingToken: request.FencingToken,
	}); err != nil {
		return err
	}
	switch strings.TrimSpace(request.Status) {
	case "running", "blocked", "completed", "failed":
	default:
		return fmt.Errorf("automation step status is invalid")
	}
	return nil
}

func validPlanStepWorkerIdentity(agentKey, machineID string) bool {
	return planStepAgentKeyPattern.MatchString(strings.TrimSpace(agentKey)) &&
		(strings.TrimSpace(machineID) == "" || validStepCallbackUUID(machineID))
}

func validatePlanStepDTO(step PlanStepDTO) error {
	if !validStepCallbackUUID(step.ID) || !validStepCallbackUUID(step.PlanID) || step.PlanVersion < 1 || !planStepKeyPattern.MatchString(step.StepKey) || step.Order < 0 {
		return fmt.Errorf("invalid step identity")
	}
	if step.Role != models.DeliveryPlanStepRoleImplementation && step.Role != models.DeliveryPlanStepRoleIntegration {
		return fmt.Errorf("invalid step role")
	}
	if strings.TrimSpace(step.Title) == "" || len(step.Title) > 240 || len(step.Objective) > 5000 || len(step.AcceptanceCriteria) > 12 || len(step.DependsOn) > 100 {
		return fmt.Errorf("invalid step content")
	}
	for _, criterion := range step.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" || len(criterion) > 400 {
			return fmt.Errorf("invalid step acceptance criteria")
		}
	}
	if len(step.EvidenceRequirements) > deliveryplansteps.MaxEvidenceRequirements {
		return fmt.Errorf("invalid step evidence requirements")
	}
	encodedRequirements, err := json.Marshal(step.EvidenceRequirements)
	if err != nil {
		return fmt.Errorf("invalid step evidence requirements")
	}
	normalizedRequirements, err := deliveryplansteps.EvidenceRequirementsFromJSON(string(encodedRequirements))
	if err != nil || !samePlanStepEvidenceRequirements(step.EvidenceRequirements, normalizedRequirements) {
		return fmt.Errorf("invalid step evidence requirements")
	}
	switch step.Status {
	case "planned", "ready", "running", "blocked", "completed", "failed", "skipped":
		return nil
	default:
		return fmt.Errorf("invalid step status")
	}
}

func samePlanStepEvidenceRequirements(left, right []deliveryplansteps.EvidenceRequirement) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Key != right[index].Key || left[index].Title != right[index].Title || left[index].Description != right[index].Description || left[index].Required != right[index].Required || left[index].MaxBytes != right[index].MaxBytes || !sameStrings(left[index].ContentTypes, right[index].ContentTypes) {
			return false
		}
	}
	return true
}

func validStepCallbackUUID(value string) bool {
	parsed, err := uuid.FromString(strings.TrimSpace(value))
	return err == nil && parsed != uuid.Nil
}

func samePlanStepCallbackUUID(left, right string) bool {
	parsedLeft, leftErr := uuid.FromString(strings.TrimSpace(left))
	parsedRight, rightErr := uuid.FromString(strings.TrimSpace(right))
	return leftErr == nil && rightErr == nil && parsedLeft != uuid.Nil && parsedLeft == parsedRight
}

func validFencingToken(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxPlanStepFencingTokenBytes && !strings.ContainsAny(value, "\r\n\x00")
}

// Plan-step callback fences are the positive, canonical decimal int64 values
// emitted by the control plane. Keep the broader validator above for legacy
// activity payloads, but fail closed here so a malformed claim cannot start
// work that every subsequent lease/status callback would reject.
func validPlanStepFencingToken(value string) bool {
	if value == "" || len(value) > maxPlanStepFencingTokenBytes {
		return false
	}
	fence, err := strconv.ParseInt(value, 10, 64)
	return err == nil && fence > 0 && strconv.FormatInt(fence, 10) == value
}
