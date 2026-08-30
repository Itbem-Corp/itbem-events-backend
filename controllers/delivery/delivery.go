// Package delivery exposes the private ITBEM delivery control plane.
package delivery

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	awsrepository "events-stocks/repositories/awsrepository"
	"events-stocks/services/deliveryworkflow"
	"events-stocks/utils"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var contextKinds = map[string]struct{}{"repository": {}, "document": {}, "design": {}, "client_conversation": {}, "decision": {}, "runbook": {}, "environment": {}}
var evidenceKinds = map[string]struct{}{"screenshot": {}, "video": {}, "test_result": {}, "diff": {}, "report": {}, "log": {}, "artifact": {}}
var deliveryArtifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,180}$`)
var deliveryArtifactDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var deliveryGitHubRepositoryReference = regexp.MustCompile(`^github://[A-Za-z0-9][A-Za-z0-9_.-]{0,38}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

const releaseGateAuthorizationMaxAge = 10 * time.Minute

type projectRequest struct {
	ClientID string `json:"client_id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Summary  string `json:"summary"`
	// Intent is the human's plain-language description of the first outcome.
	// It lets the UI avoid asking people to duplicate a title and summary before
	// there is enough project context for the agent to refine them.
	Intent string `json:"intent"`
}
type contextRequest struct {
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Reference string         `json:"reference"`
	Revision  string         `json:"revision"`
	Metadata  map[string]any `json:"metadata"`
}

// contextMetadataUpdateRequest deliberately excludes reference, revision,
// status and automation-owned fields. Updating an architecture label must not
// silently move a repository checkpoint or mutate a task's frozen evidence.
type contextMetadataUpdateRequest struct {
	Metadata map[string]any `json:"metadata"`
}
type workItemRequest struct {
	RequestID            string   `json:"request_id"`
	ContextSourceIDs     []string `json:"context_source_ids"`
	DependsOnWorkItemIDs []string `json:"depends_on_work_item_ids"`
	Title                string   `json:"title"`
	Description          string   `json:"description"`
	ExpectedOutcome      string   `json:"expected_outcome"`
	AssignedAgent        string   `json:"assigned_agent"`
	IncludedScope        []string `json:"included_scope"`
	ExcludedScope        []string `json:"excluded_scope"`
	AcceptanceCriteria   []string `json:"acceptance_criteria"`
	BudgetMicros         int64    `json:"budget_microusd"`
	BudgetAlertPercent   int      `json:"budget_alert_percent"`
}
type deliveryCostStep struct {
	StepKey              string `json:"step_key"`
	ExecutionKind        string `json:"execution_kind"`
	Tool                 string `json:"tool,omitempty"`
	Executions           int64  `json:"executions"`
	InputTokens          int64  `json:"input_tokens"`
	OutputTokens         int64  `json:"output_tokens"`
	CachedInputTokens    int64  `json:"cached_input_tokens"`
	CacheWriteTokens     int64  `json:"cache_write_tokens"`
	ReasoningTokens      int64  `json:"reasoning_tokens"`
	TotalTokens          int64  `json:"total_tokens"`
	InputCostMicros      int64  `json:"input_cost_microusd"`
	OutputCostMicros     int64  `json:"output_cost_microusd"`
	CachedCostMicros     int64  `json:"cached_cost_microusd"`
	CacheWriteCostMicros int64  `json:"cache_write_cost_microusd"`
	TotalCostMicrousd    int64  `json:"total_cost_microusd"`
}
type deliveryCostSummary struct {
	Executions           int64              `json:"executions"`
	InputTokens          int64              `json:"input_tokens"`
	OutputTokens         int64              `json:"output_tokens"`
	CachedInputTokens    int64              `json:"cached_input_tokens"`
	CacheWriteTokens     int64              `json:"cache_write_tokens"`
	ReasoningTokens      int64              `json:"reasoning_tokens"`
	TotalTokens          int64              `json:"total_tokens"`
	InputCostMicros      int64              `json:"input_cost_microusd"`
	OutputCostMicros     int64              `json:"output_cost_microusd"`
	CachedCostMicros     int64              `json:"cached_cost_microusd"`
	CacheWriteCostMicros int64              `json:"cache_write_cost_microusd"`
	TotalCostMicrousd    int64              `json:"total_cost_microusd"`
	Steps                []deliveryCostStep `json:"steps"`
}

// deliveryCostTotals intentionally excludes nested steps. GORM treats a slice
// on a scan target as a relationship, so using deliveryCostSummary directly
// turns an otherwise simple aggregate into a relation error.
type deliveryCostTotals struct {
	Executions           int64 `json:"executions"`
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CachedInputTokens    int64 `json:"cached_input_tokens"`
	CacheWriteTokens     int64 `json:"cache_write_tokens"`
	ReasoningTokens      int64 `json:"reasoning_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
	InputCostMicros      int64 `json:"input_cost_microusd"`
	OutputCostMicros     int64 `json:"output_cost_microusd"`
	CachedCostMicros     int64 `json:"cached_cost_microusd"`
	CacheWriteCostMicros int64 `json:"cache_write_cost_microusd"`
	TotalCostMicrousd    int64 `json:"total_cost_microusd"`
}
type deliveryWorkItemResponse struct {
	*models.DeliveryWorkItem
	CostSummary deliveryCostSummary `json:"cost_summary"`
}
type transitionRequest struct {
	Action             string   `json:"action"`
	Comment            string   `json:"comment"`
	PullRequestURL     string   `json:"pull_request_url"`
	PreviewURL         string   `json:"preview_url"`
	EvidenceChecklist  []string `json:"evidence_checklist"`
	ReleaseGateEventID string   `json:"release_gate_event_id,omitempty"`
}
type evidenceRequest struct {
	Kind      string         `json:"kind"`
	Phase     string         `json:"phase"`
	Title     string         `json:"title"`
	Reference string         `json:"reference"`
	Metadata  map[string]any `json:"metadata"`
}
type messageRequest struct {
	Phase string `json:"phase"`
	Body  string `json:"body"`
}

func admin(c echo.Context) (*models.User, error) {
	if configuration.DB == nil {
		return nil, utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	user, err := authz.RequireRoot(c)
	if err != nil {
		return nil, authz.Respond(c, err)
	}
	return user, nil
}

// deliveryPermission is intentionally local to the ITBEM control plane. A
// project membership can narrow access for a non-root operator, but it can
// never grant access outside the ITBEM automation application middleware.
type deliveryPermission string

const (
	deliveryView    deliveryPermission = "view"
	deliveryRequest deliveryPermission = "request"
	deliveryManage  deliveryPermission = "manage"
	deliveryReview  deliveryPermission = "review"
	deliveryQA      deliveryPermission = "qa"
	deliveryRelease deliveryPermission = "release"
)

func projectActor(c echo.Context, projectID uuid.UUID, permission deliveryPermission) (*models.User, error) {
	if configuration.DB == nil {
		return nil, utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return nil, authz.Respond(c, err)
	}
	if user.IsPlatformAdmin() {
		return user, nil
	}
	var member models.DeliveryProjectMember
	if err := configuration.DB.Where("project_id = ? AND cognito_sub = ?", projectID, user.CognitoSub).First(&member).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, utils.Error(c, http.StatusForbidden, "Delivery access denied", "You are not assigned to this delivery project")
		}
		return nil, utils.Error(c, http.StatusInternalServerError, "Delivery access unavailable", "Could not load project membership")
	}
	if !memberAllows(member, permission) {
		return nil, utils.Error(c, http.StatusForbidden, "Delivery access denied", "Your project role cannot perform this action")
	}
	return user, nil
}

func workItemActor(c echo.Context, workItemID uuid.UUID, permission deliveryPermission) (*models.User, *models.DeliveryWorkItem, error) {
	var item models.DeliveryWorkItem
	if configuration.DB == nil {
		return nil, nil, utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	if err := configuration.DB.Select("id", "project_id").First(&item, workItemID).Error; err != nil {
		return nil, nil, lookup(c, "Delivery work item", err)
	}
	user, err := projectActor(c, item.ProjectID, permission)
	return user, &item, err
}

func memberAllows(member models.DeliveryProjectMember, permission deliveryPermission) bool {
	role := strings.ToLower(strings.TrimSpace(member.Role))
	if role == "owner" || role == "delivery_manager" {
		return true
	}
	var explicit []string
	_ = json.Unmarshal([]byte(member.Permissions), &explicit)
	for _, value := range explicit {
		if strings.EqualFold(strings.TrimSpace(value), string(permission)) || strings.EqualFold(strings.TrimSpace(value), "delivery:"+string(permission)) {
			return true
		}
	}
	switch permission {
	case deliveryView:
		return role == "reviewer" || role == "qa_reviewer" || role == "requester" || role == "viewer"
	case deliveryRequest:
		return role == "requester"
	case deliveryReview:
		return role == "reviewer"
	case deliveryQA:
		return role == "qa_reviewer" || role == "reviewer"
	case deliveryRelease:
		return role == "reviewer"
	default:
		return false
	}
}

func transitionPermission(action deliveryworkflow.Action) deliveryPermission {
	switch action {
	case deliveryworkflow.ActionApproveQA, deliveryworkflow.ActionRequestQAChanges:
		return deliveryQA
	case deliveryworkflow.ActionApproveRelease:
		return deliveryRelease
	case deliveryworkflow.ActionApprovePlan, deliveryworkflow.ActionRequestPlanChanges, deliveryworkflow.ActionApproveCodeReview, deliveryworkflow.ActionRequestCodeChanges:
		return deliveryReview
	default:
		return deliveryManage
	}
}

func ListProjects(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	var projects []models.DeliveryProject
	query := configuration.DB.Preload("Client").Order("updated_at DESC")
	if !user.IsPlatformAdmin() {
		query = query.Joins("JOIN delivery_project_members ON delivery_project_members.project_id = delivery_projects.id AND delivery_project_members.cognito_sub = ?", user.CognitoSub)
	}
	if err := query.Find(&projects).Error; err != nil {
		return utils.Error(c, 500, "Delivery projects unavailable", "Could not load projects")
	}
	return utils.Success(c, 200, "Delivery projects", projects)
}

func CreateProject(c echo.Context) error {
	actor, err := admin(c)
	if err != nil {
		return err
	}
	var request projectRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, 400, "Invalid delivery project", err.Error())
	}
	clientID, err := uuid.FromString(strings.TrimSpace(request.ClientID))
	if err != nil || clientID == uuid.Nil {
		return utils.Error(c, 400, "Invalid delivery project", "client_id must be a UUID")
	}
	intent := strings.TrimSpace(request.Intent)
	name := strings.TrimSpace(request.Name)
	if name == "" && intent != "" {
		name = deliveryProjectTitle(intent)
	}
	slug := strings.ToLower(strings.TrimSpace(request.Slug))
	if slug == "" && name != "" {
		// The short UUID keeps an intent-created project unique without forcing a
		// human to think about a technical identifier.
		slug = deliveryProjectSlug(name) + "-" + strings.ToLower(uuid.Must(uuid.NewV4()).String()[:8])
	}
	if name == "" || slug == "" || len(name) > 180 || len(slug) > 180 {
		return utils.Error(c, 400, "Invalid delivery project", "client and a project intent or name are required")
	}
	var client models.Client
	if err := configuration.DB.First(&client, clientID).Error; err != nil {
		return lookup(c, "Client", err)
	}
	summary := strings.TrimSpace(request.Summary)
	if summary == "" {
		summary = intent
	}
	project := models.DeliveryProject{ClientID: clientID, Name: name, Slug: slug, Summary: summary, Status: "active", CreatedBy: actor.CognitoSub, Client: client}
	if err := configuration.DB.Create(&project).Error; err != nil {
		return utils.Error(c, 409, "Delivery project failed", "Project slug must be unique")
	}
	return utils.Success(c, 201, "Delivery project created", project)
}

var deliveryProjectSlugSeparators = regexp.MustCompile(`[^a-z0-9]+`)

func deliveryProjectSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = deliveryProjectSlugSeparators.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "delivery"
	}
	if len(value) > 160 {
		return value[:160]
	}
	return value
}

func deliveryProjectTitle(intent string) string {
	intent = strings.TrimSpace(strings.Split(intent, "\n")[0])
	if index := strings.IndexAny(intent, ".!?;"); index > 16 {
		intent = intent[:index]
	}
	words := strings.Fields(intent)
	if len(words) > 9 {
		words = words[:9]
	}
	value := strings.Join(words, " ")
	if len(value) > 120 {
		value = value[:120]
	}
	if value == "" {
		return "Nuevo proyecto Delivery"
	}
	return value
}

func GetProject(c echo.Context) error {
	id, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, id, deliveryView); err != nil {
		return err
	}
	var project models.DeliveryProject
	if err := configuration.DB.
		Preload("Client").
		Preload("Members").
		Preload("Context").
		Preload("Requests").
		Preload("Releases").
		Preload("WorkItems").
		Preload("WorkItems.Gates").
		Preload("WorkItems.ChangeSets").
		Preload("WorkItems.Dependencies.DependsOn").
		First(&project, id).Error; err != nil {
		return lookup(c, "Delivery project", err)
	}
	return utils.Success(c, 200, "Delivery project", project)
}

func CreateContext(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryManage); err != nil {
		return err
	}
	if err := projectPresent(projectID); err != nil {
		return lookup(c, "Delivery project", err)
	}
	var request contextRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, 400, "Invalid context source", err.Error())
	}
	kind := strings.ToLower(strings.TrimSpace(request.Kind))
	_, valid := contextKinds[kind]
	if !valid || strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.Reference) == "" {
		return utils.Error(c, 400, "Invalid context source", "kind, name and reference are required")
	}
	reference := strings.TrimSpace(request.Reference)
	if kind == "repository" {
		reference = canonicalDeliveryRepositoryReference(reference)
	}
	revision := strings.TrimSpace(request.Revision)
	metadataValue := request.Metadata
	status := "ready"
	var syncedAt *time.Time
	if kind == "repository" {
		var validationErr error
		metadataValue, validationErr = normalizeRepositoryContextMetadata(reference, metadataValue)
		if validationErr != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid repository context", validationErr.Error())
		}
		if strings.HasPrefix(reference, "workspace://") {
			workspace, workspaceErr := automationagent.RegisteredWorkspace(reference, os.Getenv)
			if workspaceErr != nil {
				return utils.Error(c, http.StatusConflict, "Local workspace unavailable", "Register the workspace on this control-plane host before attaching it to a project")
			}
			gitState := automationagent.ReadWorkspaceGitState(workspace)
			if !gitState.Available || strings.TrimSpace(gitState.HeadSHA) == "" {
				return utils.Error(c, http.StatusConflict, "Local workspace unavailable", "The registered workspace must be a readable Git repository with a checked-out revision")
			}
			if gitState.HasLocalChanges {
				return utils.Error(c, http.StatusConflict, "Local workspace not ready", "Commit or stash local changes before attaching this workspace as Delivery context")
			}
			if gitState.RemoteAhead > 0 {
				return utils.Error(c, http.StatusConflict, "Local workspace not ready", "Synchronize the workspace with its known tracking branch before attaching it as Delivery context")
			}
			if revision == "" {
				revision = gitState.HeadSHA
			} else if !strings.EqualFold(revision, gitState.HeadSHA) {
				// A workspace source is a concrete local checkpoint, never an
				// arbitrary label. Requiring its declared revision to equal HEAD
				// prevents a task from freezing metadata for code the agent cannot
				// actually inspect or implement against.
				return utils.Error(c, http.StatusConflict, "Local workspace revision mismatch", "The registered workspace HEAD must match the declared context revision")
			}
			applyWorkspaceDeliveryMetadata(metadataValue, workspace)
			metadataValue["local_git_branch"] = gitState.Branch
			metadataValue["local_workspace_dirty"] = gitState.HasLocalChanges
			metadataValue["local_change_count"] = gitState.LocalChangeCount
			metadataValue["local_tracking_branch"] = gitState.TrackingBranch
			metadataValue["local_ahead"] = gitState.LocalAhead
			metadataValue["remote_ahead"] = gitState.RemoteAhead
			if gitState.GitHubRepository != "" {
				metadataValue["github_repository"] = gitState.GitHubRepository
			}
		} else if revision == "" {
			// A remote reference is not yet usable as task context until a person
			// refreshes it through the GitHub App or records a concrete revision.
			status = "pending_sync"
		}
	}
	metadata, err := json.Marshal(metadataValue)
	if err != nil {
		return utils.Error(c, 400, "Invalid context source", "metadata is invalid")
	}
	if len(metadata) > 16*1024 {
		return utils.Error(c, 400, "Invalid context source", "metadata must be at most 16 KiB")
	}
	if status == "ready" {
		now := time.Now().UTC()
		syncedAt = &now
	}
	source := models.DeliveryContextSource{ProjectID: projectID, Kind: kind, Name: strings.TrimSpace(request.Name), Reference: reference, Revision: revision, Status: status, MetadataJSON: string(metadata), SyncedAt: syncedAt}
	if err := configuration.DB.Create(&source).Error; err != nil {
		return utils.Error(c, 500, "Context source failed", "Could not save source")
	}
	return utils.Success(c, 201, "Context source created", source)
}

// UpdateContextMetadata lets an operator refine the human-owned project map
// (role, runtime kind, responsibility and dependency edges) without having to
// duplicate an existing repository context. Existing work items are unchanged:
// they retain their immutable context snapshots.
func UpdateContextMetadata(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryManage); err != nil {
		return err
	}
	sourceID, err := uuid.FromString(c.Param("sourceID"))
	if err != nil || sourceID == uuid.Nil {
		return badRequest(c, "Invalid context source", "source ID must be a UUID")
	}
	var request contextMetadataUpdateRequest
	if err := c.Bind(&request); err != nil || request.Metadata == nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid context update", "metadata is required")
	}
	var source models.DeliveryContextSource
	if err := configuration.DB.Where("id = ? AND project_id = ?", sourceID, projectID).First(&source).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Delivery context source not found", "")
		}
		return utilsError(c, err)
	}
	if source.Kind != "repository" {
		return conflict(c, "Context update rejected", "Only repository architecture metadata can be updated here")
	}
	metadata, err := mergeDeliveryRepositoryMetadata(source.Reference, source.MetadataJSON, request.Metadata)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid repository context", err.Error())
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || len(encoded) > 16*1024 {
		return utils.Error(c, http.StatusBadRequest, "Invalid repository context", "metadata must be valid and at most 16 KiB")
	}
	if err := configuration.DB.Model(&source).Update("metadata_json", string(encoded)).Error; err != nil {
		return utilsError(c, err)
	}
	source.MetadataJSON = string(encoded)
	return success(c, "Repository architecture updated", source)
}

var editableRepositoryMetadataKeys = map[string]struct{}{
	"repository_role": {}, "repository_kind": {}, "repository_responsibility": {}, "depends_on_repositories": {}, "excerpt": {},
}

func mergeDeliveryRepositoryMetadata(reference, stored string, updates map[string]any) (map[string]any, error) {
	metadata := map[string]any{}
	if strings.TrimSpace(stored) != "" && json.Unmarshal([]byte(stored), &metadata) != nil {
		return nil, fmt.Errorf("stored repository metadata is invalid")
	}
	for key, value := range updates {
		if _, editable := editableRepositoryMetadataKeys[key]; !editable {
			return nil, fmt.Errorf("%s is managed by the control plane and cannot be changed", key)
		}
		if key == "repository_responsibility" || key == "excerpt" {
			text, ok := value.(string)
			if !ok || len(text) > 12_000 {
				return nil, fmt.Errorf("%s must be text up to 12,000 characters", key)
			}
			if strings.TrimSpace(text) == "" {
				delete(metadata, key)
				continue
			}
		}
		metadata[key] = value
	}
	return normalizeRepositoryContextMetadata(reference, metadata)
}

// normalizeRepositoryContextMetadata makes the project topology valid before
// it can ever be frozen onto a task. This is intentionally stricter than a
// generic metadata blob: repository role and dependency mistakes should be
// corrected in the project workspace, not discovered after an agent run.
func normalizeRepositoryContextMetadata(reference string, source map[string]any) (map[string]any, error) {
	if !isDeliveryRepositoryReference(reference) {
		return nil, fmt.Errorf("repository reference must use workspace://id or github://owner/repository")
	}
	metadata := make(map[string]any, len(source)+2)
	for key, value := range source {
		metadata[key] = value
	}
	if rawRole, exists := metadata["repository_role"]; exists {
		role, ok := rawRole.(string)
		role = strings.ToLower(strings.TrimSpace(role))
		if !ok || (role != "primary" && role != "supporting") {
			return nil, fmt.Errorf("repository_role must be primary or supporting")
		}
		metadata["repository_role"] = role
	}
	if rawResponsibility, exists := metadata["repository_responsibility"]; exists {
		responsibility, ok := rawResponsibility.(string)
		responsibility = strings.TrimSpace(responsibility)
		if !ok || len(responsibility) > 2000 {
			return nil, fmt.Errorf("repository_responsibility must be text up to 2000 characters")
		}
		if responsibility == "" {
			delete(metadata, "repository_responsibility")
		} else {
			metadata["repository_responsibility"] = responsibility
		}
	}
	if rawKind, exists := metadata["repository_kind"]; exists {
		kind, ok := rawKind.(string)
		kind = strings.ToLower(strings.TrimSpace(kind))
		if !ok || !isDeliveryRepositoryKind(kind) {
			return nil, fmt.Errorf("repository_kind must be one of frontend, backend_api, worker, lambda, infrastructure, shared_package, data, automation, or unclassified")
		}
		metadata["repository_kind"] = kind
	}
	if rawDependencies, exists := metadata["depends_on_repositories"]; exists {
		values, ok := rawDependencies.([]any)
		if !ok {
			return nil, fmt.Errorf("depends_on_repositories must be a list of repository references")
		}
		seen := make(map[string]struct{}, len(values))
		dependencies := make([]string, 0, len(values))
		for _, rawDependency := range values {
			dependency, ok := rawDependency.(string)
			dependency = canonicalDeliveryRepositoryReference(strings.TrimSpace(dependency))
			if !ok || !isDeliveryRepositoryReference(dependency) || dependency == reference {
				return nil, fmt.Errorf("each repository dependency must be a distinct workspace:// or github:// reference other than itself")
			}
			if _, duplicate := seen[dependency]; duplicate {
				return nil, fmt.Errorf("repository dependencies must not repeat a reference")
			}
			seen[dependency] = struct{}{}
			dependencies = append(dependencies, dependency)
		}
		metadata["depends_on_repositories"] = dependencies
	}
	return metadata, nil
}

// isDeliveryRepositoryKind names the operational role of one repository in a
// composed product. It is intentionally separate from repository_role:
// `primary` chooses the reviewed worktree for a bounded delivery task, while
// this classification tells the planner whether the repository is a frontend,
// API, worker, Lambda or shared operational dependency.
func isDeliveryRepositoryKind(kind string) bool {
	switch kind {
	case "frontend", "backend_api", "worker", "lambda", "infrastructure", "shared_package", "data", "automation", "unclassified":
		return true
	default:
		return false
	}
}

func isDeliveryRepositoryReference(reference string) bool {
	reference = strings.TrimSpace(reference)
	if strings.HasPrefix(reference, "workspace://") {
		id := strings.Trim(strings.TrimPrefix(reference, "workspace://"), "/ ")
		return id != "" && !strings.ContainsAny(id, "#/\\")
	}
	return deliveryGitHubRepositoryReference.MatchString(reference)
}

func canonicalDeliveryRepositoryReference(reference string) string {
	reference = strings.TrimSpace(reference)
	if strings.HasPrefix(reference, "workspace://") {
		id := strings.Trim(strings.TrimPrefix(reference, "workspace://"), "/ ")
		return "workspace://" + id
	}
	return reference
}

func CreateWorkItem(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryRequest)
	if err != nil {
		return err
	}
	if err := projectPresent(projectID); err != nil {
		return lookup(c, "Delivery project", err)
	}
	var request workItemRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, 400, "Invalid delivery work item", err.Error())
	}
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.ExpectedOutcome) == "" {
		return utils.Error(c, 400, "Invalid delivery work item", "title and expected_outcome are required")
	}
	if request.BudgetMicros < 0 || request.BudgetMicros > maxDeliveryTaskBudgetMicros || (request.BudgetAlertPercent != 0 && (request.BudgetAlertPercent < 50 || request.BudgetAlertPercent > 100)) {
		return utils.Error(c, 400, "Invalid delivery work item", "task budget must be between 0 and 100,000 USD; alert percent must be between 50 and 100")
	}
	if request.BudgetAlertPercent == 0 {
		request.BudgetAlertPercent = defaultTaskBudgetAlertPercent
	}
	contextSourceIDs := make([]uuid.UUID, 0, len(request.ContextSourceIDs))
	seenSources := make(map[uuid.UUID]struct{}, len(request.ContextSourceIDs))
	for _, rawID := range request.ContextSourceIDs {
		parsed, parseErr := uuid.FromString(strings.TrimSpace(rawID))
		if parseErr != nil || parsed == uuid.Nil {
			return utils.Error(c, 400, "Invalid delivery work item", "context_source_ids must contain UUIDs")
		}
		if _, exists := seenSources[parsed]; !exists {
			seenSources[parsed] = struct{}{}
			contextSourceIDs = append(contextSourceIDs, parsed)
		}
	}
	if len(contextSourceIDs) == 0 {
		return utils.Error(c, 400, "Invalid delivery work item", "select at least one relevant context source")
	}
	dependencyIDs := make([]uuid.UUID, 0, len(request.DependsOnWorkItemIDs))
	seenDependencies := make(map[uuid.UUID]struct{}, len(request.DependsOnWorkItemIDs))
	for _, rawID := range request.DependsOnWorkItemIDs {
		parsed, parseErr := uuid.FromString(strings.TrimSpace(rawID))
		if parseErr != nil || parsed == uuid.Nil {
			return utils.Error(c, 400, "Invalid delivery work item", "depends_on_work_item_ids must contain UUIDs")
		}
		if _, exists := seenDependencies[parsed]; !exists {
			seenDependencies[parsed] = struct{}{}
			dependencyIDs = append(dependencyIDs, parsed)
		}
	}
	included, _ := json.Marshal(request.IncludedScope)
	excluded, _ := json.Marshal(request.ExcludedScope)
	acceptance, _ := json.Marshal(request.AcceptanceCriteria)
	item := models.DeliveryWorkItem{ProjectID: projectID, RequestedBy: actor.CognitoSub, AssignedAgent: strings.TrimSpace(request.AssignedAgent), Title: strings.TrimSpace(request.Title), Description: strings.TrimSpace(request.Description), ExpectedOutcome: strings.TrimSpace(request.ExpectedOutcome), IncludedScopeJSON: string(included), ExcludedScopeJSON: string(excluded), AcceptanceJSON: string(acceptance), BudgetMicros: request.BudgetMicros, BudgetAlertPercent: request.BudgetAlertPercent, State: deliveryworkflow.StatePlanning}
	if requestID := strings.TrimSpace(request.RequestID); requestID != "" {
		parsed, parseErr := uuid.FromString(requestID)
		if parseErr != nil || parsed == uuid.Nil {
			return utils.Error(c, 400, "Invalid delivery work item", "request_id must be a UUID")
		}
		var sourceRequest models.DeliveryRequest
		if err := configuration.DB.Where("id = ? AND project_id = ?", parsed, projectID).First(&sourceRequest).Error; err != nil {
			return lookup(c, "Delivery request", err)
		}
		item.RequestID = &parsed
	}
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		clientContext, err := snapshotClientContext(tx, projectID)
		if err != nil {
			return err
		}
		item.ClientContextJSON = clientContext
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		var sources []models.DeliveryContextSource
		if err := tx.Where("project_id = ? AND status = ? AND id IN ?", projectID, "ready", contextSourceIDs).Find(&sources).Error; err != nil {
			return err
		}
		if len(sources) != len(contextSourceIDs) {
			return fmt.Errorf("every selected context source must belong to this project and be ready")
		}
		now := time.Now().UTC()
		snapshots := make([]models.DeliveryContextSnapshot, 0, len(sources))
		for _, source := range sources {
			snapshots = append(snapshots, models.DeliveryContextSnapshot{WorkItemID: item.ID, SourceID: source.ID, Kind: source.Kind, Name: source.Name, Reference: source.Reference, Revision: source.Revision, MetadataJSON: source.MetadataJSON, CapturedAt: now})
		}
		if len(snapshots) > 0 {
			if err := tx.Create(&snapshots).Error; err != nil {
				return err
			}
		}
		if len(dependencyIDs) > 0 {
			var dependencies []models.DeliveryWorkItem
			if err := tx.Where("project_id = ? AND id IN ?", projectID, dependencyIDs).Find(&dependencies).Error; err != nil {
				return err
			}
			if len(dependencies) != len(dependencyIDs) {
				return fmt.Errorf("every dependency must belong to this delivery project")
			}
			links := make([]models.DeliveryWorkItemDependency, 0, len(dependencyIDs))
			for _, dependencyID := range dependencyIDs {
				links = append(links, models.DeliveryWorkItemDependency{WorkItemID: item.ID, DependsOnWorkItemID: dependencyID})
			}
			if err := tx.Create(&links).Error; err != nil {
				return err
			}
		}
		if item.RequestID != nil {
			return tx.Model(&models.DeliveryRequest{}).Where("id = ?", *item.RequestID).Update("status", "planned").Error
		}
		return nil
	}); err != nil {
		return utils.Error(c, 500, "Delivery work item failed", "Could not persist work item")
	}
	return utils.Success(c, 201, "Delivery work item created", item)
}

func GetWorkItem(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	var item models.DeliveryWorkItem
	if err := configuration.DB.Preload("Project").Preload("Request").Preload("ContextSnapshots").Preload("Dependencies.DependsOn").Preload("Plans", func(db *gorm.DB) *gorm.DB {
		return db.Order("version DESC")
	}).Preload("ChangeSets", func(db *gorm.DB) *gorm.DB {
		return db.Order("created_at DESC")
	}).Preload("PublicationGrants", func(db *gorm.DB) *gorm.DB {
		return db.Order("granted_at DESC")
	}).Preload("Gates").Preload("Evidence").Preload("Messages").Preload("AutomationTasks", func(db *gorm.DB) *gorm.DB {
		return db.Order("created_at DESC")
	}).First(&item, workItemID).Error; err != nil {
		return lookup(c, "Delivery work item", err)
	}
	var totals deliveryCostTotals
	if err := configuration.DB.Table("("+deliveryCostLedgerUnion+") AS execution").Where("execution.delivery_work_item_id = ?", item.ID).Select("COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_microusd, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_microusd, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_microusd, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_microusd, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_microusd").Scan(&totals).Error; err != nil {
		return utils.Error(c, 500, "Delivery cost summary unavailable", "Could not aggregate execution costs")
	}
	summary := deliveryCostSummary{
		Executions:           totals.Executions,
		InputTokens:          totals.InputTokens,
		OutputTokens:         totals.OutputTokens,
		CachedInputTokens:    totals.CachedInputTokens,
		CacheWriteTokens:     totals.CacheWriteTokens,
		ReasoningTokens:      totals.ReasoningTokens,
		TotalTokens:          totals.TotalTokens,
		InputCostMicros:      totals.InputCostMicros,
		OutputCostMicros:     totals.OutputCostMicros,
		CachedCostMicros:     totals.CachedCostMicros,
		CacheWriteCostMicros: totals.CacheWriteCostMicros,
		TotalCostMicrousd:    totals.TotalCostMicrousd,
		Steps:                []deliveryCostStep{},
	}
	if err := configuration.DB.Table("("+deliveryCostLedgerUnion+") AS execution").Where("execution.delivery_work_item_id = ?", item.ID).Select("execution.step_key, execution.execution_kind, execution.tool, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_microusd, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_microusd, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_microusd, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_microusd, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_microusd").Group("execution.step_key, execution.execution_kind, execution.tool").Order("MIN(execution.completed_at) ASC").Scan(&summary.Steps).Error; err != nil {
		return utils.Error(c, 500, "Delivery cost summary unavailable", "Could not aggregate execution steps")
	}
	return utils.Success(c, 200, "Delivery work item", deliveryWorkItemResponse{DeliveryWorkItem: &item, CostSummary: summary})
}

func TransitionWorkItem(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	var request transitionRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, 400, "Invalid delivery transition", err.Error())
	}
	action := deliveryworkflow.Action(strings.TrimSpace(request.Action))
	comment := strings.TrimSpace(request.Comment)
	checklistItems, validationErr := validateHumanGateInput(action, comment, request.EvidenceChecklist)
	if validationErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery gate", validationErr.Error())
	}
	checklist, marshalErr := json.Marshal(checklistItems)
	if marshalErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery gate", "evidence checklist is invalid")
	}
	var requestedReleaseGateEventID uuid.UUID
	if action == deliveryworkflow.ActionApproveRelease {
		parsed, parseErr := uuid.FromString(strings.TrimSpace(request.ReleaseGateEventID))
		if parseErr != nil || parsed == uuid.Nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid delivery gate", "release_gate_event_id is required for release approval")
		}
		requestedReleaseGateEventID = parsed
	}
	actor, _, err := workItemActor(c, workItemID, transitionPermission(action))
	if err != nil {
		return err
	}
	var item models.DeliveryWorkItem
	transitionedAt := time.Now().UTC()
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		// Serialise state changes. A gate is a human decision with operational
		// consequences, so two reviewers must never be able to advance the same
		// work item from an out-of-date state at the same time.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, workItemID).Error; err != nil {
			return err
		}
		if action == deliveryworkflow.ActionSubmitPlan || action == deliveryworkflow.ActionApprovePlan {
			if err := requiresPlan(tx, item.ID); err != nil {
				return err
			}
		}
		if action == deliveryworkflow.ActionSubmitPlan {
			if err := requireReleasedDependencies(tx, item.ID); err != nil {
				return err
			}
		}
		if action == deliveryworkflow.ActionApproveRelease {
			var gateEvent models.DeliveryEvent
			if err := tx.Where("id = ? AND work_item_id = ? AND event_type = ?", requestedReleaseGateEventID, item.ID, deliveryledger.EventTypeReleaseGateEvaluated).First(&gateEvent).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return fmt.Errorf("the selected release Gatekeeper evaluation does not exist for this work item")
				}
				return err
			}
			var latestSequence int64
			if err := tx.Model(&models.DeliveryEvent{}).
				Where("work_item_id = ? AND event_type = ?", item.ID, deliveryledger.EventTypeReleaseGateEvaluated).
				Select("COALESCE(MAX(sequence), 0)").Scan(&latestSequence).Error; err != nil {
				return err
			}
			if gateEvent.Sequence != latestSequence {
				return fmt.Errorf("release approval requires the newest Gatekeeper evaluation")
			}
			projection, err := deliveryledger.AuthorizeGateEvaluation(gateEvent, item.ID, releasegate.ActionRelease, actor.CognitoSub, transitionedAt, releaseGateAuthorizationMaxAge)
			if err != nil {
				return fmt.Errorf("release Gatekeeper authorization rejected: %w", err)
			}
			checklistItems = append(checklistItems,
				"release_gate_event:"+gateEvent.ID.String(),
				"release_gate_subject:"+projection.SubjectDigest,
			)
			checklist, marshalErr = json.Marshal(checklistItems)
			if marshalErr != nil {
				return fmt.Errorf("release Gatekeeper evidence could not be attached")
			}
			var release models.DeliveryRelease
			if err := tx.Where("work_item_id = ? AND status = ?", item.ID, "ready").First(&release).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return fmt.Errorf("a prepared delivery report is required before release approval")
				}
				return err
			}
		}
		if err := requireCompletedAgentArtifact(tx, &item, action); err != nil {
			return err
		}
		if action == deliveryworkflow.ActionPreviewReady {
			var changes []models.DeliveryChangeSet
			if err := tx.Where("work_item_id = ?", item.ID).Order("created_at DESC").Find(&changes).Error; err != nil {
				return err
			}
			requiredRepositories, err := codeReviewRequiredRepositories(item.PlanJSON)
			if err != nil {
				return err
			}
			var preview *models.DeliveryChangeSet
			for index := range changes {
				if !validPublishedChangeRecord(changes[index]) {
					continue
				}
				if preview == nil && validPreviewURL(changes[index].PreviewURL) {
					preview = &changes[index]
				}
			}
			// Legacy work items predate the repository impact matrix and may
			// already contain a human-recorded preview without GitHub App
			// provenance. New multi-repository plans always take the strict path
			// above; retaining this branch avoids stranding older local-only work.
			if preview == nil && len(requiredRepositories) == 0 {
				for index := range changes {
					if validPreviewURL(changes[index].PreviewURL) {
						preview = &changes[index]
						break
					}
				}
			}
			if len(requiredRepositories) > 0 {
				missing := missingPublishedRepositories(requiredRepositories, changes)
				if len(missing) > 0 {
					return fmt.Errorf("every repository marked as changed must have recorded branch publication before preview: %s", strings.Join(missing, ", "))
				}
			}
			if preview == nil {
				return fmt.Errorf("a traceable published change set with a valid preview is required before QA")
			}
			item.PreviewURL = preview.PreviewURL
		}
		if action == deliveryworkflow.ActionSubmitCodeReview {
			var changes []models.DeliveryChangeSet
			if err := tx.Where("work_item_id = ? AND ci_status = ?", item.ID, "passed").Order("created_at DESC").Find(&changes).Error; err != nil {
				return err
			}
			requiredRepositories, err := codeReviewRequiredRepositories(item.PlanJSON)
			if err != nil {
				return err
			}
			var review *models.DeliveryChangeSet
			coveredRepositories := make(map[string]struct{}, len(requiredRepositories))
			for index := range changes {
				if validCodeReviewRecord(changes[index]) {
					if _, required := requiredRepositories[changes[index].RepositoryRef]; required {
						coveredRepositories[changes[index].RepositoryRef] = struct{}{}
					}
					if review == nil {
						review = &changes[index]
					}
					// Keep looking: a single human submission must cover every
					// repository that the approved plan declared as changed.
				}
			}
			if review == nil {
				return fmt.Errorf("a traceable pull request or local worktree review with passed CI is required before code review")
			}
			if len(requiredRepositories) > 0 {
				missing := make([]string, 0, len(requiredRepositories))
				for reference := range requiredRepositories {
					if _, covered := coveredRepositories[reference]; !covered {
						missing = append(missing, reference)
					}
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					return fmt.Errorf("a passed code review is required for every repository marked as changed in the approved plan: %s", strings.Join(missing, ", "))
				}
			}
			if review.ReviewType == "pull_request" {
				item.PullRequestURL = review.PullRequestURL
			}
		}
		gate := gate(action, item.ID, actor.CognitoSub, comment, string(checklist))
		if err := deliveryworkflow.Advance(&item, action, gate, transitionedAt); err != nil {
			return err
		}
		if gate != nil {
			if err := tx.Create(gate).Error; err != nil {
				return err
			}
			if action == deliveryworkflow.ActionApprovePlan {
				if err := markPlan(tx, item.ID, "approved", &gate.ID); err != nil {
					return err
				}
			}
			if action == deliveryworkflow.ActionRequestPlanChanges {
				if err := markPlan(tx, item.ID, "changes_requested", nil); err != nil {
					return err
				}
			}
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		if err := invalidatePublicationGrantsForTransition(tx, item.ID, actor.CognitoSub, action, transitionedAt); err != nil {
			return err
		}
		if action == deliveryworkflow.ActionApproveRelease {
			return tx.Model(&models.DeliveryRelease{}).Where("work_item_id = ?", item.ID).Updates(map[string]any{"status": "released", "released_by": actor.CognitoSub, "released_at": &transitionedAt}).Error
		}
		return nil
	}); err != nil {
		if err == gorm.ErrRecordNotFound {
			return lookup(c, "Delivery work item", err)
		}
		return utils.Error(c, 409, "Delivery transition rejected", err.Error())
	}
	return utils.Success(c, 200, "Delivery transition applied", item)
}

// invalidatePublicationGrantsForTransition closes any remaining publication
// authority as soon as the work leaves the narrow preview window. A grant is
// already single-use when publication succeeds; this handles a human moving
// on, sending QA back for changes, blocking, or cancelling while a grant was
// still live. The original grant remains visible as an audit record.
func invalidatePublicationGrantsForTransition(tx *gorm.DB, workItemID uuid.UUID, actor string, action deliveryworkflow.Action, now time.Time) error {
	if !publicationGrantsInvalidatedBy(action) {
		return nil
	}
	result := tx.Model(&models.DeliveryPublicationGrant{}).
		Where("work_item_id = ? AND revoked_at IS NULL AND expires_at > ?", workItemID, now).
		Updates(map[string]any{
			"revoked_by":        actor,
			"revoked_at":        now,
			"revocation_reason": "Invalidated automatically because the delivery workflow moved beyond the approved publication scope.",
		})
	return result.Error
}

func publicationGrantsInvalidatedBy(action deliveryworkflow.Action) bool {
	switch action {
	case deliveryworkflow.ActionPreviewReady,
		deliveryworkflow.ActionRequestQAChanges,
		deliveryworkflow.ActionApproveQA,
		deliveryworkflow.ActionApproveRelease,
		deliveryworkflow.ActionBlock,
		deliveryworkflow.ActionCancel:
		return true
	default:
		return false
	}
}

func validPreviewURL(value string) bool {
	return validWebURL(value)
}

func validLocalWorktreeReview(repositoryRef, branch string) bool {
	return strings.HasPrefix(strings.TrimSpace(repositoryRef), "workspace://") && strings.HasPrefix(strings.TrimSpace(branch), "itbem-agent/")
}

func validCodeReviewRecord(change models.DeliveryChangeSet) bool {
	switch strings.ToLower(strings.TrimSpace(change.ReviewType)) {
	case "local_worktree":
		return validLocalWorktreeReview(change.RepositoryRef, change.Branch) && strings.TrimSpace(change.PullRequestURL) == ""
	case "", "pull_request":
		return validWebURL(change.PullRequestURL)
	default:
		return false
	}
}

// validPublishedChangeRecord verifies branch publication provenance persisted
// by the deterministic GitHub App callback. It deliberately does not trust a
// free-form PR URL: a preview gate must be tied to an actual one-shot grant
// consumption, not a manually typed link.
func validPublishedChangeRecord(change models.DeliveryChangeSet) bool {
	if strings.ToLower(strings.TrimSpace(change.ReviewType)) != "pull_request" || strings.TrimSpace(change.RepositoryRef) == "" || !strings.HasPrefix(strings.TrimSpace(change.Branch), "itbem-agent/") {
		return false
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(change.MetadataJSON), &metadata); err != nil {
		return false
	}
	published, _ := metadata["branch_published"].(bool)
	verificationSource, _ := metadata["verification_source"].(string)
	return published && verificationSource == "itbem-github-app"
}

func missingPublishedRepositories(required map[string]struct{}, changes []models.DeliveryChangeSet) []string {
	published := make(map[string]struct{}, len(required))
	for _, change := range changes {
		if validPublishedChangeRecord(change) {
			if _, expected := required[change.RepositoryRef]; expected {
				published[change.RepositoryRef] = struct{}{}
			}
		}
	}
	missing := make([]string, 0, len(required))
	for reference := range required {
		if _, present := published[reference]; !present {
			missing = append(missing, reference)
		}
	}
	sort.Strings(missing)
	return missing
}

// codeReviewRequiredRepositories extracts the immutable repository matrix
// from the approved plan. A plan may consult many repositories, but only the
// entries explicitly marked "changes" require their own passed review record.
// Older work items created before the matrix existed retain the historical
// single-review requirement, rather than becoming impossible to finish.
func codeReviewRequiredRepositories(planJSON string) (map[string]struct{}, error) {
	raw := strings.TrimSpace(planJSON)
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var plan struct {
		RepositoryImpact json.RawMessage `json:"repository_impact"`
	}
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, fmt.Errorf("the approved plan is invalid")
	}
	if len(plan.RepositoryImpact) == 0 {
		return nil, fmt.Errorf("the approved plan is missing its repository impact matrix")
	}
	var impact []struct {
		Reference string `json:"reference"`
		Impact    string `json:"impact"`
	}
	if err := json.Unmarshal(plan.RepositoryImpact, &impact); err != nil {
		return nil, fmt.Errorf("the approved plan repository impact matrix is invalid")
	}
	required := make(map[string]struct{}, len(impact))
	for _, entry := range impact {
		reference := strings.TrimSpace(entry.Reference)
		state := strings.ToLower(strings.TrimSpace(entry.Impact))
		if reference == "" || (state != "changes" && state != "consulted" && state != "untouched") {
			return nil, fmt.Errorf("the approved plan repository impact matrix is invalid")
		}
		if state == "changes" {
			required[reference] = struct{}{}
		}
	}
	return required, nil
}

func validWebURL(value string) bool {
	if len(value) > 2000 || value == "" {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

// requireCompletedAgentArtifact keeps the human submission action honest: a
// plan, implementation, or QA review cannot be opened until its matching
// local-agent run completed and produced a private result. The result is
// recorded once as report evidence without copying its contents into Postgres.
func requireCompletedAgentArtifact(tx *gorm.DB, item *models.DeliveryWorkItem, action deliveryworkflow.Action) error {
	operation, phase := agentOperationForSubmission(action)
	if operation == "" {
		return nil
	}
	var task models.AutomationTask
	if err := tx.Where("delivery_work_item_id = ? AND operation = ? AND status = ?", item.ID, operation, "completed").Order("completed_at DESC").First(&task).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("a completed %s agent run is required before this review", phase)
		}
		return err
	}
	var existing models.DeliveryEvidence
	err := tx.Where("work_item_id = ? AND reference = ?", item.ID, task.OutputRef).First(&existing).Error
	if err == nil {
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	capturedAt := task.CompletedAt
	evidence := models.DeliveryEvidence{
		WorkItemID: item.ID, Kind: "report", Phase: phase,
		Title: "Resultado del agente: " + phase, Reference: task.OutputRef,
		MetadataJSON: fmt.Sprintf(`{"automation_task_id":%q,"operation":%q,"provider":%q,"model":%q}`, task.ID.String(), task.Operation, task.Provider, task.Model),
		CapturedBy:   "itbem-local-agent", CapturedAt: capturedAt,
	}
	return tx.Create(&evidence).Error
}

// requireReleasedDependencies stops a downstream task at its first human
// submission gate until each declared prerequisite has been delivered.
// Planning can be drafted early, but a task cannot enter review or trigger
// implementation ahead of its dependencies.
func requireReleasedDependencies(tx *gorm.DB, workItemID uuid.UUID) error {
	var pending int64
	if err := tx.Model(&models.DeliveryWorkItemDependency{}).
		Joins("JOIN delivery_work_items AS dependency_items ON dependency_items.id = delivery_work_item_dependencies.depends_on_work_item_id").
		Where("delivery_work_item_dependencies.work_item_id = ? AND dependency_items.state <> ?", workItemID, deliveryworkflow.StateReleased).
		Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("all declared task dependencies must be released before plan review")
	}
	return nil
}

func agentOperationForSubmission(action deliveryworkflow.Action) (operation, phase string) {
	switch action {
	case deliveryworkflow.ActionSubmitPlan:
		return "delivery.plan", "plan"
	case deliveryworkflow.ActionSubmitCodeReview:
		return "delivery.implementation", "implementation"
	case deliveryworkflow.ActionSubmitQA:
		return "delivery.qa", "qa"
	case deliveryworkflow.ActionApproveRelease:
		return "delivery.summary", "summary"
	default:
		return "", ""
	}
}

func CreateEvidence(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	actor, _, err := workItemActor(c, workItemID, deliveryManage)
	if err != nil {
		return err
	}
	var request evidenceRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, 400, "Invalid delivery evidence", err.Error())
	}
	kind := strings.ToLower(strings.TrimSpace(request.Kind))
	_, valid := evidenceKinds[kind]
	if !valid || strings.TrimSpace(request.Phase) == "" || strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Reference) == "" {
		return utils.Error(c, 400, "Invalid delivery evidence", "kind, phase, title and reference are required")
	}
	metadata, _ := json.Marshal(request.Metadata)
	now := time.Now().UTC()
	evidence := models.DeliveryEvidence{WorkItemID: workItemID, Kind: kind, Phase: strings.TrimSpace(request.Phase), Title: strings.TrimSpace(request.Title), Reference: strings.TrimSpace(request.Reference), MetadataJSON: string(metadata), CapturedBy: actor.CognitoSub, CapturedAt: &now}
	if err := configuration.DB.Create(&evidence).Error; err != nil {
		return utils.Error(c, 500, "Delivery evidence failed", "Could not save evidence")
	}
	return utils.Success(c, 201, "Delivery evidence created", evidence)
}

// GetEvidenceAsset streams a task-scoped private QA artifact through the
// authenticated API. The browser never sees a bucket path or needs direct
// LocalStack/S3 access, which makes visual evidence work identically in local
// development and deployed environments.
func GetEvidenceAsset(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	evidenceID, err := uuid.FromString(c.Param("evidenceId"))
	if err != nil || evidenceID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery evidence", "evidence id must be a UUID")
	}
	var evidence models.DeliveryEvidence
	if err := configuration.DB.Where("id = ? AND work_item_id = ?", evidenceID, workItemID).First(&evidence).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Delivery evidence not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Delivery evidence unavailable", "")
	}
	cfg, _ := c.Get("config").(*models.Config)
	taskID, key, artifactName, ok := deliveryArtifactReference(cfg, evidence.Reference)
	if !ok {
		return utils.Error(c, http.StatusConflict, "Delivery evidence unavailable", "This evidence is not a private QA asset")
	}
	var task models.AutomationTask
	if err := configuration.DB.Where("id = ? AND delivery_work_item_id = ? AND operation = ? AND status = ?", taskID, workItemID, "delivery.qa", "completed").First(&task).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Delivery evidence not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Delivery evidence unavailable", "")
	}
	body, err := awsrepository.GetS3Object(c.Request().Context(), key, cfg.AutomationOutputBucket)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery evidence unavailable", "Could not read private QA evidence")
	}
	defer body.Close()
	expectedDigest, hasDigest, digestErr := deliveryEvidenceSHA256(evidence.MetadataJSON)
	if digestErr != nil {
		return utils.Error(c, http.StatusConflict, "Delivery evidence unavailable", "The QA evidence integrity metadata is invalid")
	}
	contentType := deliveryEvidenceContentType(evidence.MetadataJSON, artifactName)
	c.Response().Header().Set(echo.HeaderCacheControl, "private, max-age=60")
	c.Response().Header().Set(echo.HeaderContentDisposition, "inline")
	if hasDigest {
		// Worker-produced QA artifacts are capped at 25 MiB. Read the bounded
		// object before serving it so the visual shown to a reviewer is exactly
		// the immutable evidence that the worker attested to in its callback.
		content, readErr := io.ReadAll(io.LimitReader(body, 25<<20+1))
		if readErr != nil || len(content) > 25<<20 {
			return utils.Error(c, http.StatusServiceUnavailable, "Delivery evidence unavailable", "Could not read bounded QA evidence")
		}
		actual := sha256.Sum256(content)
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(actual[:])), []byte(expectedDigest)) != 1 {
			return utils.Error(c, http.StatusConflict, "Delivery evidence unavailable", "The private QA evidence no longer matches its recorded integrity digest")
		}
		c.Response().Header().Set("X-ITBEM-Evidence-SHA256", expectedDigest)
		return c.Blob(http.StatusOK, contentType, content)
	}
	return c.Stream(http.StatusOK, contentType, body)
}

// deliveryEvidenceSHA256 accepts only the canonical worker integrity field.
// Legacy evidence remains readable without it; malformed new metadata fails
// closed instead of silently bypassing an explicit integrity assertion.
func deliveryEvidenceSHA256(metadataJSON string) (string, bool, error) {
	if strings.TrimSpace(metadataJSON) == "" || strings.TrimSpace(metadataJSON) == "{}" {
		return "", false, nil
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return "", false, err
	}
	raw, exists := metadata["sha256"]
	if !exists {
		return "", false, nil
	}
	digest, ok := raw.(string)
	digest = strings.ToLower(strings.TrimSpace(digest))
	if !ok || !deliveryArtifactDigestPattern.MatchString(digest) {
		return "", false, fmt.Errorf("evidence SHA-256 digest is invalid")
	}
	return digest, true, nil
}

func deliveryArtifactReference(cfg *models.Config, reference string) (uuid.UUID, string, string, bool) {
	if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return uuid.Nil, "", "", false
	}
	prefix := "s3://" + strings.TrimSpace(cfg.AutomationOutputBucket) + "/automation/"
	key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reference), "s3://"+strings.TrimSpace(cfg.AutomationOutputBucket)+"/"))
	if !strings.HasPrefix(strings.TrimSpace(reference), prefix) {
		return uuid.Nil, "", "", false
	}
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "automation" || parts[2] != "artifacts" || !deliveryArtifactNamePattern.MatchString(parts[3]) {
		return uuid.Nil, "", "", false
	}
	taskID, err := uuid.FromString(parts[1])
	if err != nil || taskID == uuid.Nil {
		return uuid.Nil, "", "", false
	}
	return taskID, key, parts[3], true
}

func deliveryEvidenceContentType(metadataJSON, artifactName string) string {
	var metadata map[string]any
	if json.Unmarshal([]byte(metadataJSON), &metadata) == nil {
		if contentType, ok := metadata["content_type"].(string); ok {
			contentType = strings.ToLower(strings.TrimSpace(contentType))
			if strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "video/") || contentType == "application/json" || strings.HasPrefix(contentType, "text/") {
				return contentType
			}
		}
	}
	if strings.HasSuffix(strings.ToLower(artifactName), ".png") {
		return "image/png"
	}
	if strings.HasSuffix(strings.ToLower(artifactName), ".jpg") || strings.HasSuffix(strings.ToLower(artifactName), ".jpeg") {
		return "image/jpeg"
	}
	if strings.HasSuffix(strings.ToLower(artifactName), ".webp") {
		return "image/webp"
	}
	return "application/octet-stream"
}

func CreateMessage(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	actor, _, err := workItemActor(c, workItemID, deliveryRequest)
	if err != nil {
		return err
	}
	var request messageRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, 400, "Invalid delivery message", err.Error())
	}
	if strings.TrimSpace(request.Phase) == "" || strings.TrimSpace(request.Body) == "" || len(request.Body) > 12000 {
		return utils.Error(c, 400, "Invalid delivery message", "phase and body are required")
	}
	message := models.DeliveryMessage{WorkItemID: workItemID, Phase: strings.TrimSpace(request.Phase), AuthorType: "human", AuthorID: actor.CognitoSub, Body: strings.TrimSpace(request.Body)}
	if err := configuration.DB.Create(&message).Error; err != nil {
		return utils.Error(c, 500, "Delivery message failed", "Could not save message")
	}
	return utils.Success(c, 201, "Delivery message created", message)
}

func id(c echo.Context, kind string) (uuid.UUID, error) {
	value, err := uuid.FromString(c.Param("id"))
	if err != nil || value == uuid.Nil {
		return uuid.Nil, utils.Error(c, 400, "Invalid "+kind, "id must be a UUID")
	}
	return value, nil
}
func projectPresent(projectID uuid.UUID) error {
	var project models.DeliveryProject
	return configuration.DB.Select("id").First(&project, projectID).Error
}

// snapshotClientContext freezes the minimum client context that can inform a
// task. Contact details are intentionally excluded; a later profile edit must
// not silently change the plan a human is reviewing.
func snapshotClientContext(tx *gorm.DB, projectID uuid.UUID) (string, error) {
	var project models.DeliveryProject
	if err := tx.Select("client_id").First(&project, projectID).Error; err != nil {
		return "", err
	}
	var profile models.DeliveryClientProfile
	if err := tx.Where("client_id = ?", project.ClientID).First(&profile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "{}", nil
		}
		return "", err
	}
	var rules []string
	if err := json.Unmarshal([]byte(profile.RulesJSON), &rules); err != nil {
		return "", fmt.Errorf("delivery client rules are invalid")
	}
	updatedAt := profile.UpdatedAt.UTC().Format(time.RFC3339Nano)
	snapshot, err := json.Marshal(map[string]any{
		"health":               profile.Health,
		"rules":                cleanStrings(rules),
		"conversation_summary": profile.ConversationSummary,
		"profile_updated_at":   updatedAt,
	})
	if err != nil {
		return "", err
	}
	return string(snapshot), nil
}
func lookup(c echo.Context, kind string, err error) error {
	if err == gorm.ErrRecordNotFound {
		return utils.Error(c, 404, kind+" not found", "")
	}
	return utils.Error(c, 500, kind+" unavailable", "Could not load resource")
}

func gate(action deliveryworkflow.Action, workItemID uuid.UUID, decidedBy, comment, checklist string) *models.DeliveryGate {
	value := &models.DeliveryGate{WorkItemID: workItemID, DecidedBy: decidedBy, Comment: comment, EvidenceChecklist: checklist}
	switch action {
	case deliveryworkflow.ActionApprovePlan:
		value.Kind, value.Decision = deliveryworkflow.GatePlan, deliveryworkflow.DecisionApproved
	case deliveryworkflow.ActionRequestPlanChanges:
		value.Kind, value.Decision = deliveryworkflow.GatePlan, deliveryworkflow.DecisionChangesRequested
	case deliveryworkflow.ActionApproveCodeReview:
		value.Kind, value.Decision = deliveryworkflow.GateCodeReview, deliveryworkflow.DecisionApproved
	case deliveryworkflow.ActionRequestCodeChanges:
		value.Kind, value.Decision = deliveryworkflow.GateCodeReview, deliveryworkflow.DecisionChangesRequested
	case deliveryworkflow.ActionApproveQA:
		value.Kind, value.Decision = deliveryworkflow.GateQAReview, deliveryworkflow.DecisionApproved
	case deliveryworkflow.ActionRequestQAChanges:
		value.Kind, value.Decision = deliveryworkflow.GateQAReview, deliveryworkflow.DecisionChangesRequested
	case deliveryworkflow.ActionApproveRelease:
		value.Kind, value.Decision = deliveryworkflow.GateRelease, deliveryworkflow.DecisionApproved
	default:
		return nil
	}
	return value
}

func isHumanGateAction(action deliveryworkflow.Action) bool {
	switch action {
	case deliveryworkflow.ActionApprovePlan,
		deliveryworkflow.ActionRequestPlanChanges,
		deliveryworkflow.ActionApproveCodeReview,
		deliveryworkflow.ActionRequestCodeChanges,
		deliveryworkflow.ActionApproveQA,
		deliveryworkflow.ActionRequestQAChanges,
		deliveryworkflow.ActionApproveRelease:
		return true
	default:
		return false
	}
}

func validateHumanGateInput(action deliveryworkflow.Action, comment string, checklist []string) ([]string, error) {
	items := cleanStrings(checklist)
	if !isHumanGateAction(action) {
		return items, nil
	}
	if strings.TrimSpace(comment) == "" {
		return nil, fmt.Errorf("a human decision comment is required")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("at least one reviewed evidence item is required")
	}
	return items, nil
}
