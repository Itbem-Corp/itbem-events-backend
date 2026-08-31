package delivery

// This file owns the durable delivery artifacts that sit around the gated
// state machine.  They are deliberately records, not webhook side-effects:
// a human can inspect a proposed plan, a change set, or a release before it
// becomes authoritative for the next phase.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	awsrepository "events-stocks/repositories/awsrepository"
	"events-stocks/services/deliveryworkflow"
	"events-stocks/utils"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type deliveryRequestInput struct {
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Priority        string   `json:"priority"`
	Constraints     []string `json:"constraints"`
	ExpectedOutcome string   `json:"expected_outcome"`
}

type deliveryPlanInput struct {
	Summary    string         `json:"summary"`
	Structured map[string]any `json:"structured"`
}

type changeSetInput struct {
	RepositoryRef  string         `json:"repository_ref"`
	Branch         string         `json:"branch"`
	CommitSHA      string         `json:"commit_sha"`
	ReviewType     string         `json:"review_type"`
	PullRequestURL string         `json:"pull_request_url"`
	CIStatus       string         `json:"ci_status"`
	CIURL          string         `json:"ci_url"`
	PreviewURL     string         `json:"preview_url"`
	Environment    string         `json:"environment"`
	Metadata       map[string]any `json:"metadata"`
}

type releaseInput struct {
	Executive map[string]any `json:"executive"`
	Technical map[string]any `json:"technical"`
	ReportRef string         `json:"report_ref"`
}

type memberInput struct {
	CognitoSub  string   `json:"cognito_sub"`
	UserEmail   string   `json:"user_email"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

var requestPriorities = map[string]struct{}{"low": {}, "normal": {}, "high": {}, "urgent": {}}
var projectRoles = map[string]struct{}{"owner": {}, "delivery_manager": {}, "reviewer": {}, "qa_reviewer": {}, "requester": {}, "viewer": {}}
var ciStatuses = map[string]struct{}{"pending": {}, "running": {}, "passed": {}, "failed": {}, "cancelled": {}}
var reviewTypes = map[string]struct{}{"pull_request": {}, "local_worktree": {}}
var requiredPlanLists = []string{"context_reviewed", "context_gaps", "assumptions", "human_decisions", "implementation_steps", "risks", "qa_plan", "evidence_plan", "acceptance_criteria", "files_impacted", "rollback_plan", "questions"}
var repositoryImpactStates = map[string]struct{}{"changes": {}, "consulted": {}, "untouched": {}}
var reservedChangeSetProvenanceKeys = map[string]struct{}{
	"automation_task_id": {}, "publication_grant_id": {}, "verification_source": {}, "branch_published": {},
	"review_diff_sha256": {}, "github_repository": {}, "base_sha": {}, "worktree": {}, "remote_repository": {}, "target_branch": {},
}

func containsReservedChangeSetProvenance(metadata map[string]any) bool {
	for key := range metadata {
		if _, reserved := reservedChangeSetProvenanceKeys[key]; reserved {
			return true
		}
	}
	return false
}

func validatePlanStructure(structured map[string]any) error {
	for _, field := range requiredPlanLists {
		values, ok := structured[field].([]any)
		if !ok {
			return fmt.Errorf("structured plan field %s must be a list", field)
		}
		for _, value := range values {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("structured plan field %s must contain only non-empty strings", field)
			}
		}
	}
	if err := validateRepositoryImpact(structured["repository_impact"]); err != nil {
		return err
	}
	if err := automationagent.ValidateApprovedBrowserQAPlan(structured); err != nil {
		return err
	}
	if estimate, ok := structured["estimate"].(string); !ok || strings.TrimSpace(estimate) == "" {
		return fmt.Errorf("structured plan field estimate is required")
	}
	for _, field := range []string{"goal_interpretation", "autonomy_boundary"} {
		if value, ok := structured[field].(string); !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("structured plan field %s is required", field)
		}
	}
	confidence, ok := structured["confidence"].(float64)
	if !ok || confidence < 0 || confidence > 1 {
		return fmt.Errorf("structured plan field confidence must be a number from 0 to 1")
	}
	return nil
}

// validateRepositoryImpact preserves the machine-readable repository matrix
// that a reviewer needs for multi-repository delivery. Its contents are later
// bound to frozen project context before the plan can pass a human gate.
func validateRepositoryImpact(value any) error {
	entries, ok := value.([]any)
	if !ok {
		return fmt.Errorf("structured plan field repository_impact must be a list")
	}
	seen := map[string]struct{}{}
	for _, value := range entries {
		entry, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("structured plan field repository_impact must contain objects")
		}
		name, reference := strings.TrimSpace(stringMapValue(entry, "name")), strings.TrimSpace(stringMapValue(entry, "reference"))
		revision, role := strings.TrimSpace(stringMapValue(entry, "revision")), strings.ToLower(strings.TrimSpace(stringMapValue(entry, "role")))
		impact, notes := strings.ToLower(strings.TrimSpace(stringMapValue(entry, "impact"))), strings.TrimSpace(stringMapValue(entry, "notes"))
		if name == "" || reference == "" || revision == "" || notes == "" || (role != "primary" && role != "supporting") {
			return fmt.Errorf("structured plan repository_impact entries require name, reference, revision, role and notes")
		}
		if _, allowed := repositoryImpactStates[impact]; !allowed {
			return fmt.Errorf("structured plan repository_impact impact must be changes, consulted or untouched")
		}
		if _, duplicate := seen[reference]; duplicate {
			return fmt.Errorf("structured plan repository_impact must not repeat a repository reference")
		}
		seen[reference] = struct{}{}
	}
	return nil
}

// validatePlanMatchesFrozenRepositoryTopology makes the repository impact
// matrix authoritative at persistence time too. A worker validates it before
// completion, but promotion and manually authored proposals must independently
// prove they reference the immutable snapshots attached to this work item.
func validatePlanMatchesFrozenRepositoryTopology(tx *gorm.DB, workItemID uuid.UUID, structured map[string]any) error {
	var snapshots []models.DeliveryContextSnapshot
	if err := tx.Where("work_item_id = ?", workItemID).Find(&snapshots).Error; err != nil {
		return err
	}
	topology, err := frozenRepositoryTopology(snapshots)
	if err != nil {
		return err
	}
	return validatePlanRepositoryTopology(structured, topology)
}

func validatePlanRepositoryTopology(structured map[string]any, topology []deliveryAgentRepository) error {
	entries, ok := structured["repository_impact"].([]any)
	if !ok || len(entries) != len(topology) {
		return fmt.Errorf("structured plan repository_impact must declare every frozen repository")
	}
	expected := map[string]deliveryAgentRepository{}
	for _, repository := range topology {
		expected[repository.Reference] = repository
	}
	for _, value := range entries {
		entry, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("structured plan repository_impact must contain objects")
		}
		reference := strings.TrimSpace(stringMapValue(entry, "reference"))
		repository, exists := expected[reference]
		if !exists || strings.TrimSpace(stringMapValue(entry, "name")) != repository.Name || strings.TrimSpace(stringMapValue(entry, "revision")) != repository.Revision || strings.ToLower(strings.TrimSpace(stringMapValue(entry, "role"))) != repository.Role {
			return fmt.Errorf("structured plan repository_impact does not match frozen repository topology")
		}
		if strings.HasPrefix(strings.ToLower(reference), "github://") && strings.EqualFold(strings.TrimSpace(stringMapValue(entry, "impact")), "changes") {
			return fmt.Errorf("structured plan cannot mark a github-only repository as changed; register a local workspace checkpoint before implementation")
		}
		delete(expected, reference)
	}
	if len(expected) != 0 {
		return fmt.Errorf("structured plan repository_impact omits a frozen repository")
	}
	// The persisted plan must obey the same Stagehand and per-repository QA
	// contract as a worker-produced plan. Otherwise a manual proposal could
	// bypass an E2E requirement that the agent itself was required to satisfy.
	payload, err := json.Marshal(map[string]any{"repository_topology": topology})
	if err != nil {
		return fmt.Errorf("could not encode frozen repository topology")
	}
	return automationagent.ValidateDeliveryPlanTopology(structured, payload)
}

func stringMapValue(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}

// validateReleaseStructure keeps the final delivery report readable and
// complete. It prevents an arbitrary JSON blob from being marked ready for a
// human release gate without the outcome, verification path, decisions and
// evidence that the reviewer needs.
func validateReleaseStructure(executive, technical map[string]any) error {
	for _, field := range []string{"what_changed", "how_to_test"} {
		value, ok := executive[field].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("executive summary field %s is required", field)
		}
	}
	if value, ok := executive["why"]; ok {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("executive summary field why must be text")
		}
	}
	for _, field := range []string{"risks"} {
		if err := validateStringList(executive, field, "executive summary"); err != nil {
			return err
		}
	}
	for _, field := range []string{"decisions", "evidence"} {
		if err := validateStringList(technical, field, "technical summary"); err != nil {
			return err
		}
	}
	return nil
}

func validateStringList(value map[string]any, field, scope string) error {
	items, ok := value[field].([]any)
	if !ok {
		return fmt.Errorf("%s field %s must be a list", scope, field)
	}
	for _, item := range items {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s field %s must contain only non-empty strings", scope, field)
		}
	}
	return nil
}

func ListRequests(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	var requests []models.DeliveryRequest
	if err := configuration.DB.Where("project_id = ?", projectID).Order("created_at DESC").Find(&requests).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery requests", requests)
}

func CreateRequest(c echo.Context) error {
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
	var input deliveryRequestInput
	if err := c.Bind(&input); err != nil {
		return badRequest(c, "Invalid delivery request", err.Error())
	}
	priority := strings.ToLower(strings.TrimSpace(input.Priority))
	if priority == "" {
		priority = "normal"
	}
	title := deliveryRequestTitle(input.Title, input.Body)
	expectedOutcome := strings.TrimSpace(input.ExpectedOutcome)
	if expectedOutcome == "" {
		expectedOutcome = strings.TrimSpace(input.Body)
	}
	if _, ok := requestPriorities[priority]; !ok || title == "" || expectedOutcome == "" {
		return badRequest(c, "Invalid delivery request", "describe the requested outcome and use a valid priority")
	}
	constraints, err := json.Marshal(cleanStrings(input.Constraints))
	if err != nil {
		return badRequest(c, "Invalid delivery request", "constraints are invalid")
	}
	request := models.DeliveryRequest{ProjectID: projectID, RequestedBy: actor.CognitoSub, Title: title, Body: strings.TrimSpace(input.Body), Priority: priority, ConstraintsJSON: string(constraints), ExpectedOutcome: expectedOutcome, Status: "open"}
	if err := configuration.DB.Create(&request).Error; err != nil {
		return utilsError(c, err)
	}
	return created(c, "Delivery request created", request)
}

// deliveryRequestTitle allows a human to state intent in plain language. The
// request keeps the full original body; this short label is only navigation
// metadata and can be refined by the plan before any implementation begins.
func deliveryRequestTitle(rawTitle, body string) string {
	title := strings.Join(strings.Fields(strings.TrimSpace(rawTitle)), " ")
	if title == "" {
		title = strings.Join(strings.Fields(strings.TrimSpace(body)), " ")
	}
	if len(title) > 120 {
		title = strings.TrimSpace(title[:117]) + "..."
	}
	return title
}

func ListPlans(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	var plans []models.DeliveryPlan
	if err := configuration.DB.Where("work_item_id = ?", workItemID).Order("version DESC").Find(&plans).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery plans", plans)
}

// CreatePlan persists a reviewable, versioned candidate. The subsequent
// submit_plan action still requires a completed agent artifact, and approval
// still requires a separate human decision.
func CreatePlan(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	actor, _, err := workItemActor(c, workItemID, deliveryManage)
	if err != nil {
		return err
	}
	var input deliveryPlanInput
	if err := c.Bind(&input); err != nil {
		return badRequest(c, "Invalid delivery plan", err.Error())
	}
	if strings.TrimSpace(input.Summary) == "" || len(input.Structured) == 0 {
		return badRequest(c, "Invalid delivery plan", "summary and structured plan are required")
	}
	if err := validatePlanStructure(input.Structured); err != nil {
		return badRequest(c, "Invalid delivery plan", err.Error())
	}
	structured, err := json.Marshal(input.Structured)
	if err != nil || len(structured) > 96*1024 {
		return badRequest(c, "Invalid delivery plan", "structured plan must be valid JSON up to 96 KiB")
	}
	var plan models.DeliveryPlan
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var item models.DeliveryWorkItem
		if err := tx.First(&item, workItemID).Error; err != nil {
			return err
		}
		if item.State != deliveryworkflow.StatePlanning {
			return fmt.Errorf("a plan can only be proposed while the task is planning")
		}
		if err := validatePlanMatchesFrozenRepositoryTopology(tx, item.ID, input.Structured); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.DeliveryPlan{}).Where("work_item_id = ?", item.ID).Count(&count).Error; err != nil {
			return err
		}
		plan = models.DeliveryPlan{WorkItemID: item.ID, Version: int(count) + 1, Status: "proposed", Summary: strings.TrimSpace(input.Summary), StructuredJSON: string(structured), ContextDigest: contextDigest(tx, item.ID), ProposedBy: actor.CognitoSub}
		if err := tx.Create(&plan).Error; err != nil {
			return err
		}
		item.PlanJSON = string(structured)
		return tx.Save(&item).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return lookup(c, "Delivery work item", err)
		}
		return conflict(c, "Delivery plan rejected", err.Error())
	}
	return created(c, "Delivery plan proposed", plan)
}

// PromoteLatestAgentPlan turns the structured result already produced by the
// isolated worker into a versioned candidate. It intentionally does not
// submit, approve, or transition the task; the human still owns each gate.
func PromoteLatestAgentPlan(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	actor, _, err := workItemActor(c, workItemID, deliveryManage)
	if err != nil {
		return err
	}
	var task models.AutomationTask
	if err := configuration.DB.Where("delivery_work_item_id = ? AND operation = ? AND status = ?", workItemID, "delivery.plan", "completed").Order("completed_at DESC").First(&task).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return conflict(c, "Delivery plan unavailable", "a completed delivery.plan agent run is required")
		}
		return utilsError(c, err)
	}
	cfg, _ := c.Get("config").(*models.Config)
	if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery plan unavailable", "Private automation output storage is not configured")
	}
	resultKey, validResult := deliveryPlanResultKey(cfg, task)
	if !validResult {
		return conflict(c, "Delivery plan unavailable", "agent output reference is not valid for this task")
	}
	body, err := awsrepository.GetS3Object(c.Request().Context(), resultKey, cfg.AutomationOutputBucket)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery plan unavailable", "Could not read the private agent result")
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 128*1024))
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery plan unavailable", "Could not read the private agent result")
	}
	var output struct {
		SchemaVersion int            `json:"schema_version"`
		TaskID        string         `json:"task_id"`
		Operation     string         `json:"operation"`
		Structured    map[string]any `json:"structured_result"`
	}
	if err := json.Unmarshal(raw, &output); err != nil || output.SchemaVersion != 1 || output.TaskID != task.ID.String() || output.Operation != "delivery.plan" {
		return conflict(c, "Delivery plan unavailable", "agent result is not a valid structured delivery plan")
	}
	summary, _ := output.Structured["summary"].(string)
	if strings.TrimSpace(summary) == "" {
		return conflict(c, "Delivery plan unavailable", "agent plan does not include a summary")
	}
	if err := validatePlanStructure(output.Structured); err != nil {
		return conflict(c, "Delivery plan unavailable", err.Error())
	}
	structured, err := json.Marshal(output.Structured)
	if err != nil || len(structured) > 96*1024 {
		return conflict(c, "Delivery plan unavailable", "agent plan exceeds the structured plan limit")
	}
	plan := models.DeliveryPlan{}
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var item models.DeliveryWorkItem
		if err := tx.First(&item, workItemID).Error; err != nil {
			return err
		}
		if item.State != deliveryworkflow.StatePlanning {
			return fmt.Errorf("an agent plan can only be promoted while the task is planning")
		}
		if err := validatePlanMatchesFrozenRepositoryTopology(tx, item.ID, output.Structured); err != nil {
			return err
		}
		var existing models.DeliveryPlan
		// StructuredJSON is persisted by GORM as structured_json.  Keep this
		// idempotency lookup aligned with the database column, rather than the
		// public JSON response key (structured_result).
		if err := tx.Where("work_item_id = ? AND proposed_by = ? AND structured_json = ?", item.ID, "agent:"+task.ID.String(), string(structured)).First(&existing).Error; err == nil {
			plan = existing
			return nil
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		var count int64
		if err := tx.Model(&models.DeliveryPlan{}).Where("work_item_id = ?", item.ID).Count(&count).Error; err != nil {
			return err
		}
		plan = models.DeliveryPlan{WorkItemID: item.ID, Version: int(count) + 1, Status: "proposed", Summary: strings.TrimSpace(summary), StructuredJSON: string(structured), ContextDigest: contextDigest(tx, item.ID), ProposedBy: "agent:" + task.ID.String()}
		if err := tx.Create(&plan).Error; err != nil {
			return err
		}
		item.PlanJSON = string(structured)
		return tx.Save(&item).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return lookup(c, "Delivery work item", err)
		}
		return conflict(c, "Delivery plan rejected", err.Error())
	}
	_ = actor // actor is deliberately resolved for authorization/audit context.
	return created(c, "Agent plan promoted for human review", plan)
}

func deliveryPlanResultKey(cfg *models.Config, task models.AutomationTask) (string, bool) {
	if cfg == nil || task.ID == uuid.Nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return "", false
	}
	prefix := "s3://" + strings.TrimSpace(cfg.AutomationOutputBucket) + "/"
	if !strings.HasPrefix(task.OutputRef, prefix) {
		return "", false
	}
	key := strings.TrimPrefix(task.OutputRef, prefix)
	legacy := "automation/" + task.ID.String() + "/result.json"
	if key == legacy {
		return key, true
	}
	runID, err := uuid.FromString(strings.TrimSpace(task.RunID))
	if err != nil || runID == uuid.Nil {
		return "", false
	}
	expected := "automation/" + task.ID.String() + "/runs/" + runID.String() + "/result.json"
	return key, key == expected
}

func ListChangeSets(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	var changes []models.DeliveryChangeSet
	if err := configuration.DB.Where("work_item_id = ?", workItemID).Order("created_at DESC").Find(&changes).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery change sets", changes)
}

func CreateChangeSet(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	actor, _, err := workItemActor(c, workItemID, deliveryManage)
	if err != nil {
		return err
	}
	var input changeSetInput
	if err := c.Bind(&input); err != nil {
		return badRequest(c, "Invalid delivery change set", err.Error())
	}
	status := strings.ToLower(strings.TrimSpace(input.CIStatus))
	if status == "" {
		status = "pending"
	}
	if _, ok := ciStatuses[status]; !ok || strings.TrimSpace(input.RepositoryRef) == "" {
		return badRequest(c, "Invalid delivery change set", "repository_ref and a valid ci_status are required")
	}
	reviewType := strings.ToLower(strings.TrimSpace(input.ReviewType))
	if reviewType == "" {
		reviewType = "pull_request"
	}
	if _, ok := reviewTypes[reviewType]; !ok {
		return badRequest(c, "Invalid delivery change set", "review_type must be pull_request or local_worktree")
	}
	if reviewType == "pull_request" && input.PullRequestURL != "" && !validWebURL(strings.TrimSpace(input.PullRequestURL)) {
		return badRequest(c, "Invalid delivery change set", "pull_request_url must be a valid http(s) URL")
	}
	if reviewType == "local_worktree" {
		if strings.TrimSpace(input.PullRequestURL) != "" || !validLocalWorktreeReview(strings.TrimSpace(input.RepositoryRef), strings.TrimSpace(input.Branch)) {
			return badRequest(c, "Invalid delivery change set", "local_worktree review requires a workspace:// repository, an itbem-agent branch, and no pull_request_url")
		}
	}
	if input.CIURL != "" && !validWebURL(strings.TrimSpace(input.CIURL)) {
		return badRequest(c, "Invalid delivery change set", "ci_url must be a valid http(s) URL")
	}
	if input.PreviewURL != "" && !validPreviewURL(strings.TrimSpace(input.PreviewURL)) {
		return badRequest(c, "Invalid delivery change set", "preview_url must be a valid http(s) URL")
	}
	if containsReservedChangeSetProvenance(input.Metadata) {
		return badRequest(c, "Invalid delivery change set", "metadata may not claim agent or GitHub App publication provenance")
	}
	metadata, err := json.Marshal(input.Metadata)
	if err != nil || len(metadata) > 16*1024 {
		return badRequest(c, "Invalid delivery change set", "metadata must be valid JSON up to 16 KiB")
	}
	change := models.DeliveryChangeSet{}
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var item models.DeliveryWorkItem
		if err := tx.First(&item, workItemID).Error; err != nil {
			return err
		}
		if !allowsChangeSet(item.State) {
			return fmt.Errorf("a change set cannot be recorded in state %q", item.State)
		}
		change = models.DeliveryChangeSet{WorkItemID: item.ID, RepositoryRef: strings.TrimSpace(input.RepositoryRef), Branch: strings.TrimSpace(input.Branch), CommitSHA: strings.TrimSpace(input.CommitSHA), ReviewType: reviewType, PullRequestURL: strings.TrimSpace(input.PullRequestURL), CIStatus: status, CIURL: strings.TrimSpace(input.CIURL), PreviewURL: strings.TrimSpace(input.PreviewURL), Environment: defaultString(strings.TrimSpace(input.Environment), "preview"), MetadataJSON: string(metadata), CreatedBy: actor.CognitoSub}
		if err := tx.Create(&change).Error; err != nil {
			return err
		}
		if change.PullRequestURL != "" {
			item.PullRequestURL = change.PullRequestURL
		}
		if change.PreviewURL != "" {
			item.PreviewURL = change.PreviewURL
		}
		return tx.Save(&item).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return lookup(c, "Delivery work item", err)
		}
		return conflict(c, "Delivery change set rejected", err.Error())
	}
	return created(c, "Delivery change set recorded", change)
}

func GetRelease(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	var release models.DeliveryRelease
	if err := configuration.DB.Where("work_item_id = ?", workItemID).First(&release).Error; err != nil {
		return lookup(c, "Delivery release", err)
	}
	return success(c, "Delivery release", release)
}

func UpsertRelease(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	actor, _, err := workItemActor(c, workItemID, deliveryRelease)
	if err != nil {
		return err
	}
	var input releaseInput
	if err := c.Bind(&input); err != nil {
		return badRequest(c, "Invalid delivery release", err.Error())
	}
	if err := validateReleaseStructure(input.Executive, input.Technical); err != nil {
		return badRequest(c, "Invalid delivery release", err.Error())
	}
	executive, eErr := json.Marshal(input.Executive)
	technical, tErr := json.Marshal(input.Technical)
	if eErr != nil || tErr != nil || len(executive) > 48*1024 || len(technical) > 64*1024 {
		return badRequest(c, "Invalid delivery release", "summaries must be valid JSON within the size limit")
	}
	var release models.DeliveryRelease
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var item models.DeliveryWorkItem
		if err := tx.First(&item, workItemID).Error; err != nil {
			return err
		}
		if item.State != deliveryworkflow.StateReleaseReview {
			return fmt.Errorf("a release can only be prepared after QA approval")
		}
		release = models.DeliveryRelease{ProjectID: item.ProjectID, WorkItemID: item.ID, Status: "ready", ExecutiveJSON: string(executive), TechnicalJSON: string(technical), ReportRef: strings.TrimSpace(input.ReportRef), ReleasedBy: actor.CognitoSub}
		return tx.Where("work_item_id = ?", item.ID).Assign(release).FirstOrCreate(&release).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return lookup(c, "Delivery work item", err)
		}
		return conflict(c, "Delivery release rejected", err.Error())
	}
	return success(c, "Delivery release prepared", release)
}

// DownloadReleaseReport renders the stored release as a portable, readable
// Markdown report. It performs no deployment and reveals no private object
// contents; evidence remains referenced rather than copied into the report.
func DownloadReleaseReport(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	if _, _, err := workItemActor(c, workItemID, deliveryView); err != nil {
		return err
	}
	var item models.DeliveryWorkItem
	if err := configuration.DB.Preload("Project").Preload("ChangeSets", func(db *gorm.DB) *gorm.DB { return db.Order("created_at DESC") }).Preload("Evidence", func(db *gorm.DB) *gorm.DB { return db.Order("captured_at DESC") }).Preload("Gates", func(db *gorm.DB) *gorm.DB { return db.Order("decided_at ASC") }).First(&item, workItemID).Error; err != nil {
		return lookup(c, "Delivery work item", err)
	}
	var release models.DeliveryRelease
	if err := configuration.DB.Where("work_item_id = ? AND status IN ?", workItemID, []string{"ready", "released"}).First(&release).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return conflict(c, "Delivery report unavailable", "prepare the delivery report before downloading it")
		}
		return utilsError(c, err)
	}
	report := renderDeliveryReport(item, release)
	filename := "itbem-delivery-" + reportSlug(item.Title) + ".md"
	c.Response().Header().Set(echo.HeaderContentType, "text/markdown; charset=utf-8")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+filename+`"`)
	return c.String(http.StatusOK, report)
}

func renderDeliveryReport(item models.DeliveryWorkItem, release models.DeliveryRelease) string {
	var builder strings.Builder
	builder.WriteString("# Entrega: " + strings.TrimSpace(item.Title) + "\n\n")
	builder.WriteString("**Estado:** " + release.Status + "  \n")
	builder.WriteString("**Proyecto:** " + strings.TrimSpace(item.Project.Name) + "  \n")
	if release.ReleasedAt != nil {
		builder.WriteString("**Liberada:** " + release.ReleasedAt.UTC().Format(time.RFC3339) + "  \n")
	}
	builder.WriteString("\n## Resultado esperado\n\n" + strings.TrimSpace(item.ExpectedOutcome) + "\n")
	builder.WriteString("\n## Resumen ejecutivo\n\n" + markdownSummary(release.ExecutiveJSON) + "\n")
	builder.WriteString("\n## Resumen técnico\n\n" + markdownSummary(release.TechnicalJSON) + "\n")
	if len(item.ChangeSets) > 0 {
		builder.WriteString("\n## Cambios, CI y previews\n")
		for _, change := range item.ChangeSets {
			builder.WriteString("\n- **" + safeInline(change.RepositoryRef) + "**")
			if change.Branch != "" {
				builder.WriteString(" · `" + safeInline(change.Branch) + "`")
			}
			builder.WriteString(" · CI: **" + safeInline(change.CIStatus) + "**")
			if change.PullRequestURL != "" {
				builder.WriteString(" · PR: " + change.PullRequestURL)
			}
			if change.PreviewURL != "" {
				builder.WriteString(" · Preview: " + change.PreviewURL)
			}
		}
		builder.WriteString("\n")
	}
	if len(item.Evidence) > 0 {
		builder.WriteString("\n## Evidencia\n")
		for _, evidence := range item.Evidence {
			builder.WriteString("\n- [" + safeInline(evidence.Title) + "](" + evidence.Reference + ") · " + safeInline(evidence.Phase))
		}
		builder.WriteString("\n")
	}
	if len(item.Gates) > 0 {
		builder.WriteString("\n## Decisiones humanas\n")
		for _, gate := range item.Gates {
			builder.WriteString("\n- **" + safeInline(gate.Kind) + "**: " + safeInline(gate.Decision) + " · " + gate.DecidedAt.UTC().Format(time.RFC3339))
			if gate.Comment != "" {
				builder.WriteString(" — " + safeInline(gate.Comment))
			}
		}
		builder.WriteString("\n")
	}
	if strings.TrimSpace(release.ReportRef) != "" {
		builder.WriteString("\n## Referencia del informe\n\n" + release.ReportRef + "\n")
	}
	return builder.String()
}

func markdownSummary(raw string) string {
	var value map[string]any
	if json.Unmarshal([]byte(raw), &value) != nil || len(value) == 0 {
		return "Sin resumen registrado."
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		encoded, _ := json.Marshal(value[key])
		text := strings.Trim(strings.TrimSpace(string(encoded)), `"`)
		builder.WriteString("- **" + strings.ReplaceAll(key, "_", " ") + "**: " + text + "\n")
	}
	return strings.TrimSpace(builder.String())
}

func safeInline(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "\n", " "), "\r", " ")
}
func reportSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			out.WriteRune(character)
		} else if out.Len() > 0 && !strings.HasSuffix(out.String(), "-") {
			out.WriteByte('-')
		}
	}
	result := strings.Trim(out.String(), "-")
	if result == "" {
		return "report"
	}
	return result
}

func ListMembers(c echo.Context) error {
	if _, err := admin(c); err != nil {
		return err
	}
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	var members []models.DeliveryProjectMember
	if err := configuration.DB.Where("project_id = ?", projectID).Order("created_at ASC").Find(&members).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery project members", members)
}

func UpsertMember(c echo.Context) error {
	actor, err := admin(c)
	if err != nil {
		return err
	}
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if err := projectPresent(projectID); err != nil {
		return lookup(c, "Delivery project", err)
	}
	var input memberInput
	if err := c.Bind(&input); err != nil {
		return badRequest(c, "Invalid project member", err.Error())
	}
	role := strings.ToLower(strings.TrimSpace(input.Role))
	if _, ok := projectRoles[role]; !ok {
		return badRequest(c, "Invalid project member", "a valid project role is required")
	}
	targetSub := strings.TrimSpace(input.CognitoSub)
	if email := strings.ToLower(strings.TrimSpace(input.UserEmail)); email != "" {
		var target models.User
		if err := configuration.DB.Where("LOWER(email) = ? AND is_active = ?", email, true).First(&target).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return badRequest(c, "Invalid project member", "the active user email was not found")
			}
			return utilsError(c, err)
		}
		if targetSub != "" && targetSub != target.CognitoSub {
			return badRequest(c, "Invalid project member", "user_email and cognito_sub identify different users")
		}
		targetSub = target.CognitoSub
	}
	if targetSub == "" {
		return badRequest(c, "Invalid project member", "user_email or cognito_sub is required")
	}
	permissions, err := json.Marshal(cleanStrings(input.Permissions))
	if err != nil {
		return badRequest(c, "Invalid project member", "permissions are invalid")
	}
	member := models.DeliveryProjectMember{ProjectID: projectID, CognitoSub: targetSub, Role: role, Permissions: string(permissions), CreatedBy: actor.CognitoSub}
	if err := configuration.DB.Where("project_id = ? AND cognito_sub = ?", projectID, member.CognitoSub).Assign(member).FirstOrCreate(&member).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery project member saved", member)
}

func requiresPlan(tx *gorm.DB, itemID uuid.UUID) error {
	var plan models.DeliveryPlan
	if err := tx.Where("work_item_id = ? AND status = ?", itemID, "proposed").Order("version DESC").First(&plan).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("a structured delivery plan is required before review")
		}
		return err
	}
	return nil
}
func markPlan(tx *gorm.DB, itemID uuid.UUID, status string, gateID *uuid.UUID) error {
	var plan models.DeliveryPlan
	if err := tx.Where("work_item_id = ? AND status = ?", itemID, "proposed").Order("version DESC").First(&plan).Error; err != nil {
		return err
	}
	plan.Status, plan.ApprovedGateID = status, gateID
	return tx.Save(&plan).Error
}
func allowsChangeSet(state string) bool {
	switch state {
	case deliveryworkflow.StateImplementation, deliveryworkflow.StateCodeReview, deliveryworkflow.StatePreviewPending, deliveryworkflow.StateQARunning, deliveryworkflow.StateQAReview, deliveryworkflow.StateReleaseReview:
		return true
	}
	return false
}
func contextDigest(tx *gorm.DB, itemID uuid.UUID) string {
	var snapshots []models.DeliveryContextSnapshot
	_ = tx.Where("work_item_id = ?", itemID).Order("id ASC").Find(&snapshots).Error
	payload, _ := json.Marshal(snapshots)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func success(c echo.Context, message string, data any) error {
	return utils.Success(c, http.StatusOK, message, data)
}
func created(c echo.Context, message string, data any) error {
	return utils.Success(c, http.StatusCreated, message, data)
}
func badRequest(c echo.Context, message, detail string) error {
	return utils.Error(c, http.StatusBadRequest, message, detail)
}
func conflict(c echo.Context, message, detail string) error {
	return utils.Error(c, http.StatusConflict, message, detail)
}
func utilsError(c echo.Context, err error) error {
	return utils.Error(c, http.StatusInternalServerError, "Delivery resource unavailable", err.Error())
}
