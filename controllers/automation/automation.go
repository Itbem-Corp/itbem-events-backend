package automation

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/environmentevidence"
	"events-stocks/internal/projectvault"
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
	"events-stocks/internal/releasegatecontrol"
	"events-stocks/internal/securityevidence"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	awsrepository "events-stocks/repositories/awsrepository"
	"events-stocks/services/automationcost"
	outboxService "events-stocks/services/outbox"
	"events-stocks/utils"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var operationPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,95}$`)
var artifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,180}$`)
var artifactDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var agentBranchPattern = regexp.MustCompile(`^itbem-agent/[a-f0-9-]{36}$`)
var gitCommitSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)
var githubRepositoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)
var githubOrganizationPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
var toolCallKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
var workerWorkspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)

const maxGitHubReviewWebhookBytes = 1 << 20

var allowedOperations = map[string]struct{}{
	"ai.chat":                   {},
	"document.analyze":          {},
	"code.review":               {},
	"product.ideate":            {},
	"delivery.plan":             {},
	"delivery.implementation":   {},
	"delivery.onboarding_probe": {},
	"delivery.publish":          {},
	"delivery.release_gate":     {},
	"delivery.qa":               {},
	"delivery.summary":          {},
}

type githubPullRequestWebhook struct {
	Action       string `json:"action"`
	Number       int    `json:"number"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Draft bool `json:"draft"`
		Base  struct {
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

type githubReviewTaskView struct {
	ID           uuid.UUID  `json:"id"`
	Status       string     `json:"status"`
	AttemptCount int        `json:"attempt_count"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// GitHubPullRequestReviewWebhook converts an explicitly permitted, signed PR
// event into the same durable private code.review task used everywhere else.
// It cannot comment, approve, merge, publish or run code. The App API is used
// only to download a bounded immutable patch for the exact delivered head SHA.
func GitHubPullRequestReviewWebhook(c echo.Context) error {
	cfg, _ := c.Get("config").(*models.Config)
	if cfg == nil || !automationqueue.IsConfigured() || strings.TrimSpace(cfg.AutomationInputBucket) == "" || !githubReviewWebhookConfigured(cfg) {
		return utils.Error(c, http.StatusNotFound, "Not found", "")
	}
	if !strings.EqualFold(strings.TrimSpace(c.Request().Header.Get("X-GitHub-Event")), "pull_request") {
		return utils.Error(c, http.StatusBadRequest, "Invalid GitHub event", "")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxGitHubReviewWebhookBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxGitHubReviewWebhookBytes || !validGitHubWebhookSignature(body, c.Request().Header.Get("X-Hub-Signature-256"), cfg.GitHubReviewWebhookSecret) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	var event githubPullRequestWebhook
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid GitHub event", "")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return utils.Error(c, http.StatusBadRequest, "Invalid GitHub event", "")
	}
	repository := strings.ToLower(strings.TrimSpace(event.Repository.FullName))
	if event.PullRequest.Draft || !githubReviewActionAllowed(event.Action) || !githubReviewRepositoryAllowed(cfg.GitHubReviewRepositories, repository) || event.Installation.ID < 1 || event.Number < 1 || !gitCommitSHA.MatchString(strings.ToLower(strings.TrimSpace(event.PullRequest.Base.SHA))) || !gitCommitSHA.MatchString(strings.ToLower(strings.TrimSpace(event.PullRequest.Head.SHA))) || event.PullRequest.Base.SHA == event.PullRequest.Head.SHA {
		return utils.Error(c, http.StatusBadRequest, "GitHub review ignored", "")
	}
	// A deterministic task id makes GitHub redelivery idempotent before a
	// provider call or a second outbox record can exist.
	prIdentity := repository + ":" + strconv.Itoa(event.Number)
	identity := prIdentity + ":" + strings.ToLower(event.PullRequest.Head.SHA)
	taskID := uuid.NewV5(uuid.NamespaceURL, "itbem/github-review/"+identity)
	if taskID == uuid.Nil {
		return utils.Error(c, http.StatusInternalServerError, "GitHub review failed", "")
	}
	var existing models.AutomationTask
	if err := configuration.DB.First(&existing, taskID).Error; err == nil {
		return utils.Success(c, http.StatusAccepted, "GitHub review already queued", githubReviewTaskProjection(existing))
	} else if err != gorm.ErrRecordNotFound {
		return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
	}
	appConfig, err := automationagent.LoadGitHubAppConfig(os.Getenv)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
	}
	appConfig, err = appConfig.WithInstallationID(event.Installation.ID)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	installation, err := automationagent.MintGitHubInstallationToken(c.Request().Context(), appConfig, nil, time.Now().UTC())
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
	}
	currentPR, err := automationagent.ReadGitHubPullRequestState(c.Request().Context(), appConfig, installation.Token, repository, event.Number)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
	}
	if !currentPR.Open || currentPR.Draft || currentPR.Merged || subtle.ConstantTimeCompare([]byte(currentPR.HeadSHA), []byte(strings.ToLower(event.PullRequest.Head.SHA))) != 1 {
		return utils.Success(c, http.StatusAccepted, "GitHub review ignored", map[string]string{"status": "stale_delivery"})
	}
	patch, err := automationagent.ReadGitHubPullRequestPatch(c.Request().Context(), appConfig, installation.Token, repository, event.PullRequest.Base.SHA, event.PullRequest.Head.SHA)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "Could not freeze the pull request diff")
	}
	review, err := automationagent.NewCodeReviewInput("github://"+repository, event.PullRequest.Base.SHA, event.PullRequest.Head.SHA, patch)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "The pull request diff is not reviewable within the configured safety limits")
	}
	review, err = automationagent.BindCodeReviewRemoteTarget(review, event.Number, event.Installation.ID)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "The remote review target is invalid")
	}
	reviewSubject, err := automationagent.CodeReviewPublicationSubjectSHA256(review)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "The remote review subject could not be sealed")
	}
	input, err := json.Marshal(automationagent.TaskInput{Prompt: "Review this immutable pull request diff. Report only reproducible issues grounded in the supplied patch.", Delivery: mustJSON(review)})
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "GitHub review failed", "")
	}
	inputKey := "automation/inputs/" + taskID.String() + "/input.json"
	if err := awsrepository.UploadEncryptedJSON(c.Request().Context(), input, inputKey, cfg.AutomationInputBucket); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
	}
	jobID := uuid.NewV5(uuid.NamespaceURL, "itbem/github-review-job/"+identity)
	task := &models.AutomationTask{ID: taskID, JobID: jobID, RequestedBy: "github-app-review", CorrelationID: "github-pr:" + identity, Operation: "code.review", EvidenceSubjectDigest: reviewSubject, MaxCompletionTokens: automationagent.CompletionTokensForOperation("code.review"), InputRef: "s3://" + cfg.AutomationInputBucket + "/" + inputKey, Status: "queued"}
	message := automationqueue.Message{SchemaVersion: 1, JobID: jobID.String(), TenantCode: "itbem", CorrelationID: task.CorrelationID, Type: "ai.local.process"}
	message.Payload.TaskID, message.Payload.Operation, message.Payload.MaxCompletionTokens, message.Payload.InputRef, message.Payload.Attempt = taskID.String(), task.Operation, task.MaxCompletionTokens, task.InputRef, 1
	created := false
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		// A synchronize event supersedes any queued review for the same pull
		// request. Do not interrupt a run already holding a lease: it may be
		// about to persist a valid historical result and cancellation is an
		// operator action. Queued tasks, however, cannot provide useful feedback
		// after their head SHA is stale, so release their budget before inserting
		// the immutable review for the new commit.
		if err := supersedeQueuedGitHubReviews(tx, prIdentity, task.ID, time.Now().UTC()); err != nil {
			return err
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(task)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		created = true
		queued, queueErr := outboxService.EnqueueAutomationProcess(c.Request().Context(), tx, message)
		if queueErr != nil || !queued {
			if queueErr != nil {
				return queueErr
			}
			return fmt.Errorf("GitHub review delivery was not enqueued")
		}
		return nil
	}); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "GitHub review failed", "")
	}
	if !created {
		if err := configuration.DB.First(&existing, taskID).Error; err == nil {
			return utils.Success(c, http.StatusAccepted, "GitHub review already queued", githubReviewTaskProjection(existing))
		}
		return utils.Error(c, http.StatusConflict, "GitHub review already queued", "")
	}
	return utils.Success(c, http.StatusAccepted, "GitHub pull request review queued", githubReviewTaskProjection(*task))
}

func githubReviewTaskProjection(task models.AutomationTask) githubReviewTaskView {
	return githubReviewTaskView{ID: task.ID, Status: task.Status, AttemptCount: task.AttemptCount, CompletedAt: task.CompletedAt, CreatedAt: task.CreatedAt}
}

func supersedeQueuedGitHubReviews(tx *gorm.DB, prIdentity string, replacementID uuid.UUID, now time.Time) error {
	if tx == nil || strings.TrimSpace(prIdentity) == "" || replacementID == uuid.Nil {
		return fmt.Errorf("GitHub review supersession is invalid")
	}
	result := tx.Model(&models.AutomationTask{}).
		Where("operation = ? AND requested_by = ? AND status = ? AND correlation_id LIKE ? AND id <> ?", "code.review", "github-app-review", "queued", "github-pr:"+prIdentity+":%", replacementID).
		Updates(map[string]any{
			"status":                        "cancelled",
			"completed_at":                  now,
			"lease_expires_at":              nil,
			"budget_reservation_micros":     0,
			"budget_reservation_expires_at": nil,
			"error_message":                 "Superseded by a newer pull-request commit before review began",
		})
	return result.Error
}

func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

func githubReviewWebhookConfigured(cfg *models.Config) bool {
	return strings.TrimSpace(cfg.GitHubReviewWebhookSecret) != "" && len(githubReviewRepositories(cfg.GitHubReviewRepositories)) > 0
}
func githubReviewActionAllowed(action string) bool {
	_, ok := map[string]struct{}{"opened": {}, "reopened": {}, "ready_for_review": {}, "synchronize": {}}[strings.ToLower(strings.TrimSpace(action))]
	return ok
}
func githubReviewRepositories(raw string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if githubReviewRepositoryAllowListItem(item) {
			result[item] = struct{}{}
		}
	}
	return result
}
func githubReviewRepositoryAllowed(raw, repository string) bool {
	repository = strings.ToLower(strings.TrimSpace(repository))
	allowed := githubReviewRepositories(raw)
	if _, ok := allowed[repository]; ok {
		return true
	}
	owner, _, found := strings.Cut(repository, "/")
	if !found {
		return false
	}
	_, ok := allowed[owner+"/*"]
	return ok
}

// githubReviewRepositoryAllowListItem permits an organization wildcard only
// when an operator explicitly grants it. The GitHub App installation and the
// configured installation-ID allow-list still constrain the actual access.
func githubReviewRepositoryAllowListItem(value string) bool {
	if githubRepositoryPattern.MatchString(value) {
		return true
	}
	owner, wildcard, found := strings.Cut(value, "/")
	return found && wildcard == "*" && githubOrganizationPattern.MatchString(owner)
}
func validGitHubWebhookSignature(body []byte, value, secret string) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "sha256=")
	if len(value) != 64 || strings.TrimSpace(secret) == "" {
		return false
	}
	expected := hmac.New(sha256.New, []byte(secret))
	_, _ = expected.Write(body)
	return subtle.ConstantTimeCompare([]byte(value), []byte(fmt.Sprintf("%x", expected.Sum(nil)))) == 1
}

// Providers are an explicit allow-list because callback metadata is part of
// the audit trail shown to a human reviewer. Do not accept a free-form name.
var allowedProviders = map[string]struct{}{
	"minimax":   {},
	"openai":    {},
	"anthropic": {},
}

// genericTaskOperationAllowed keeps the quick, non-Delivery console useful
// without creating a second path around the Delivery workflow. Every delivery
// operation must originate from StartAgentRun, which validates the work-item
// state, context snapshot, human gate, budget and (where relevant) publication
// grant before a message reaches the worker.
func genericTaskOperationAllowed(operation string) bool {
	if strings.HasPrefix(operation, "delivery.") {
		return false
	}
	_, allowed := allowedOperations[operation]
	return allowed
}

const automationRunLeaseDuration = 20 * time.Minute

// retryReservationHeader is deliberately an internal, response-only signal.
// It distinguishes a recoverable expired budget hold from ordinary callback
// conflicts such as a newer worker owning the task. The worker retains only
// the former for a fresh lease; it must never retry every 409 blindly.
const retryReservationHeader = "X-ITBEM-Automation-Retry-Reservation"

type createTaskRequest struct {
	Operation string `json:"operation"`
	InputRef  string `json:"input_ref"`
}

type inputUploadResponse struct {
	InputRef             string `json:"input_ref"`
	UploadURL            string `json:"upload_url,omitempty"`
	ExpiresIn            int    `json:"expires_in_seconds"`
	LocalProxyUploadSafe bool   `json:"local_proxy_upload_safe"`
}

// inputUploadRequest intentionally has one bounded JSON field. In deployed
// environments the browser writes directly to the private bucket through a
// short-lived signed URL. LocalStack commonly runs outside the browser's
// network namespace, so ENV=local may use this authenticated, transient proxy
// instead. The API streams the value directly into the dedicated private
// bucket; it never places the prompt in the database, logs or SQS payload.
type inputUploadRequest struct {
	Content json.RawMessage `json:"content"`
}

type cancelTaskRequest struct {
	Reason string `json:"reason"`
}

const maxLocalAutomationInputBytes = 256 * 1024

type outputDownloadResponse struct {
	DownloadURL string `json:"download_url"`
	ExpiresIn   int    `json:"expires_in_seconds"`
}

type automationCostSummary struct {
	Executions           int64 `json:"executions"`
	Tasks                int64 `json:"tasks"`
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
	TotalCostMicros      int64 `json:"total_cost_microusd"`
}

type automationCostBreakdown struct {
	Key                  string `json:"key"`
	ExecutionKind        string `json:"execution_kind,omitempty"`
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
	TotalCostMicros      int64  `json:"total_cost_microusd"`
}

// automationCostProject and automationCostModel keep the global ledger
// useful for portfolio decisions without leaking any task content. A missing
// project is an intentional "general automation" bucket, not an error.
type automationCostProject struct {
	ProjectID            *uuid.UUID `json:"project_id,omitempty"`
	ProjectName          string     `json:"project_name"`
	Executions           int64      `json:"executions"`
	InputTokens          int64      `json:"input_tokens"`
	OutputTokens         int64      `json:"output_tokens"`
	CachedInputTokens    int64      `json:"cached_input_tokens"`
	CacheWriteTokens     int64      `json:"cache_write_tokens"`
	ReasoningTokens      int64      `json:"reasoning_tokens"`
	TotalTokens          int64      `json:"total_tokens"`
	InputCostMicros      int64      `json:"input_cost_microusd"`
	OutputCostMicros     int64      `json:"output_cost_microusd"`
	CachedCostMicros     int64      `json:"cached_cost_microusd"`
	CacheWriteCostMicros int64      `json:"cache_write_cost_microusd"`
	TotalCostMicros      int64      `json:"total_cost_microusd"`
}

type automationCostModel struct {
	Provider             string `json:"provider"`
	Model                string `json:"model"`
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
	TotalCostMicros      int64  `json:"total_cost_microusd"`
}

// automationCostBudgetWatch gives platform operators a small, current-month
// portfolio view of the hard project budgets. It intentionally contains no
// prompts, task titles, repository references or execution payloads.
type automationCostBudgetWatch struct {
	ProjectID           uuid.UUID `json:"project_id"`
	ProjectName         string    `json:"project_name"`
	MonthlyBudgetMicros int64     `json:"monthly_budget_microusd"`
	AlertPercent        int       `json:"alert_percent"`
	SpentMicros         int64     `json:"spent_microusd"`
	ReservedMicros      int64     `json:"reserved_microusd"`
	AllocatedMicros     int64     `json:"allocated_microusd"`
	RemainingMicros     int64     `json:"remaining_microusd"`
	UsagePercent        int       `json:"usage_percent"`
	Status              string    `json:"status"`
}

// automationCostTaskBudgetWatch exposes the all-time hard cap that belongs to
// an individual delivery task. Project budgets reset each month; task budgets
// do not. Keeping both shapes explicit prevents the UI from implying that a
// task cap will replenish at the start of a new billing period.
type automationCostTaskBudgetWatch struct {
	ProjectID       uuid.UUID `json:"project_id"`
	ProjectName     string    `json:"project_name"`
	WorkItemID      uuid.UUID `json:"work_item_id"`
	WorkItemTitle   string    `json:"work_item_title"`
	BudgetMicros    int64     `json:"budget_microusd"`
	AlertPercent    int       `json:"alert_percent"`
	SpentMicros     int64     `json:"spent_microusd"`
	ReservedMicros  int64     `json:"reserved_microusd"`
	AllocatedMicros int64     `json:"allocated_microusd"`
	RemainingMicros int64     `json:"remaining_microusd"`
	UsagePercent    int       `json:"usage_percent"`
	Status          string    `json:"status"`
}

// automationCostExecution is a deliberately summary-only ledger row for the
// global cost view. Request and response bodies stay private behind the
// task-scoped inspector; this record supplies the secure route back to them.
type automationCostExecution struct {
	ID                   uuid.UUID  `json:"id"`
	AutomationTaskID     uuid.UUID  `json:"automation_task_id"`
	DeliveryWorkItemID   *uuid.UUID `json:"delivery_work_item_id,omitempty"`
	Operation            string     `json:"operation"`
	TaskStatus           string     `json:"task_status"`
	ExecutionKind        string     `json:"execution_kind"`
	Tool                 string     `json:"tool,omitempty"`
	CallKey              string     `json:"call_key,omitempty"`
	CallStatus           string     `json:"call_status,omitempty"`
	StepKey              string     `json:"step_key"`
	Provider             string     `json:"provider"`
	Model                string     `json:"model"`
	InputTokens          int64      `json:"input_tokens"`
	OutputTokens         int64      `json:"output_tokens"`
	CachedInputTokens    int64      `json:"cached_input_tokens"`
	CacheWriteTokens     int64      `json:"cache_write_tokens"`
	ReasoningTokens      int64      `json:"reasoning_tokens"`
	TotalTokens          int64      `json:"total_tokens"`
	InputCostMicros      int64      `json:"input_cost_microusd"`
	OutputCostMicros     int64      `json:"output_cost_microusd"`
	CachedCostMicros     int64      `json:"cached_cost_microusd"`
	CacheWriteCostMicros int64      `json:"cache_write_cost_microusd"`
	TotalCostMicros      int64      `json:"total_cost_microusd"`
	PricingBasis         string     `json:"pricing_basis"`
	// ProviderOutcome is derived from an allow-listed slice of UsageJSON by
	// the task-scoped trace endpoint. It is not a database relation and must
	// stay invisible to GORM when the cost overview scans summary rows.
	ProviderOutcome *automationProviderOutcome `gorm:"-" json:"provider_outcome,omitempty"`
	CompletedAt     time.Time                  `json:"completed_at"`
}

// automationProviderOutcome is intentionally a tiny, allow-listed projection
// from the provider usage object. It lets an authorized reviewer understand
// why a call ended without accidentally making raw provider extensions,
// request content or reasoning traces part of the browser-facing trace API.
type automationProviderOutcome struct {
	FinishReason    string `json:"finish_reason,omitempty"`
	InputSensitive  bool   `json:"input_sensitive,omitempty"`
	OutputSensitive bool   `json:"output_sensitive,omitempty"`
	StatusCode      int    `json:"status_code,omitempty"`
}

var providerOutcomeFinishReasonPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func providerOutcomeFromUsage(usageJSON string) *automationProviderOutcome {
	var usage map[string]any
	if err := json.Unmarshal([]byte(usageJSON), &usage); err != nil || usage == nil {
		return nil
	}
	raw, ok := usage["_itbem_provider"].(map[string]any)
	if !ok {
		return nil
	}
	outcome := &automationProviderOutcome{}
	if finishReason, ok := raw["finish_reason"].(string); ok {
		finishReason = strings.TrimSpace(finishReason)
		if providerOutcomeFinishReasonPattern.MatchString(finishReason) {
			outcome.FinishReason = finishReason
		}
	}
	if inputSensitive, ok := raw["input_sensitive"].(bool); ok {
		outcome.InputSensitive = inputSensitive
	}
	if outputSensitive, ok := raw["output_sensitive"].(bool); ok {
		outcome.OutputSensitive = outputSensitive
	}
	if rawStatus, ok := raw["status_code"].(float64); ok {
		statusCode := int(rawStatus)
		if rawStatus == float64(statusCode) && statusCode >= 100 && statusCode <= 999 {
			outcome.StatusCode = statusCode
		}
	}
	if outcome.FinishReason == "" && !outcome.InputSensitive && !outcome.OutputSensitive && outcome.StatusCode == 0 {
		return nil
	}
	return outcome
}

// automationCostExecutionPage bounds the global ledger response while making
// every retained execution reachable. It deliberately contains no private
// request/response references; the existing task-scoped inspector enforces
// authorization before either object is read.
type automationCostExecutionPage struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// automationCostRecentExecutionSelect stays explicit rather than selecting a
// model wholesale: the portfolio view needs every billable component, but must
// never receive private object references or task payloads.
const automationCostRecentExecutionSelect = "execution.id, execution.automation_task_id, execution.delivery_work_item_id, task.operation, task.status AS task_status, execution.execution_kind, execution.tool, execution.call_key, execution.call_status, execution.step_key, execution.provider, execution.model, execution.input_tokens, execution.output_tokens, execution.cached_input_tokens, execution.cache_write_tokens, execution.reasoning_tokens, execution.total_tokens, execution.input_cost_micros, execution.output_cost_micros, execution.cached_cost_micros, execution.cache_write_cost_micros, execution.total_cost_micros, execution.pricing_basis, execution.completed_at"

// automationCostLedgerUnion gives the portfolio a single accounting view over
// the primary agent call and independently billable runtime tools. Keep the
// column list explicit: private request/result references never enter cost
// aggregation, pagination or the dashboard response.
const automationCostLedgerUnion = `SELECT id, automation_task_id, delivery_work_item_id, step_key, provider, model, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, input_cost_micros, output_cost_micros, cached_cost_micros, cache_write_cost_micros, total_cost_micros, pricing_basis, completed_at, 'agent' AS execution_kind, '' AS tool, '' AS call_key, 'completed' AS call_status FROM automation_executions UNION ALL SELECT id, automation_task_id, delivery_work_item_id, step_key, provider, model, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, input_cost_micros, output_cost_micros, cached_cost_micros, cache_write_cost_micros, total_cost_micros, pricing_basis, completed_at, 'tool' AS execution_kind, tool, call_key, call_status FROM automation_tool_executions`

const (
	automationExecutionLedgerTable     = "automation_executions"
	automationToolExecutionLedgerTable = "automation_tool_executions"
)

// automationCostLedgerCoverage makes an older, partially migrated local
// database explicit to API clients. Totals always come from persisted ledger
// values; a missing optional tool ledger is never represented as a made-up
// zero-cost tool call.
type automationCostLedgerCoverage struct {
	State             string   `json:"state"`
	AgentLedger       bool     `json:"agent_ledger"`
	ToolLedger        bool     `json:"tool_ledger"`
	UnknownDimensions []string `json:"unknown_dimensions,omitempty"`
}

type automationCostLedgerColumn struct {
	TableName  string `gorm:"column:table_name"`
	ColumnName string `gorm:"column:column_name"`
}

type automationCostLedgerField struct {
	Name     string
	Fallback string
	Required bool
}

// These fields are deliberately projected in one stable order for both
// ledgers. The primary identifiers, completion time and authoritative total
// cost are required; the newer accounting dimensions safely degrade to a
// marked legacy projection when an already-running local control plane has
// not applied its additive migration yet.
var automationCostLedgerFields = []automationCostLedgerField{
	{Name: "id", Required: true},
	{Name: "automation_task_id", Required: true},
	{Name: "delivery_work_item_id", Fallback: "NULL::uuid"},
	{Name: "step_key", Fallback: "''::text"},
	{Name: "provider", Fallback: "''::text"},
	{Name: "model", Fallback: "''::text"},
	{Name: "input_tokens", Fallback: "0::bigint"},
	{Name: "output_tokens", Fallback: "0::bigint"},
	{Name: "cached_input_tokens", Fallback: "0::bigint"},
	{Name: "cache_write_tokens", Fallback: "0::bigint"},
	{Name: "reasoning_tokens", Fallback: "0::bigint"},
	{Name: "total_tokens", Fallback: "0::bigint"},
	{Name: "input_cost_micros", Fallback: "0::bigint"},
	{Name: "output_cost_micros", Fallback: "0::bigint"},
	{Name: "cached_cost_micros", Fallback: "0::bigint"},
	{Name: "cache_write_cost_micros", Fallback: "0::bigint"},
	{Name: "total_cost_micros", Required: true},
	{Name: "pricing_basis", Fallback: "'legacy'::text"},
	{Name: "completed_at", Required: true},
}

func automationCostLedgerProjection(table string, columns map[string]struct{}, executionKind string) (string, []string, bool) {
	selects := make([]string, 0, len(automationCostLedgerFields)+4)
	missing := make([]string, 0)
	for _, field := range automationCostLedgerFields {
		if _, ok := columns[field.Name]; ok {
			selects = append(selects, table+"."+field.Name)
			continue
		}
		missing = append(missing, field.Name)
		if field.Required {
			return "", missing, false
		}
		selects = append(selects, field.Fallback+" AS "+field.Name)
	}

	selects = append(selects, "'"+executionKind+"'::text AS execution_kind")
	if executionKind == "tool" {
		for _, field := range []automationCostLedgerField{
			{Name: "tool", Fallback: "''::text"},
			{Name: "call_key", Fallback: "''::text"},
			{Name: "call_status", Fallback: "'completed'::text"},
		} {
			if _, ok := columns[field.Name]; ok {
				selects = append(selects, table+"."+field.Name)
				continue
			}
			missing = append(missing, field.Name)
			selects = append(selects, field.Fallback+" AS "+field.Name)
		}
	} else {
		selects = append(selects, "''::text AS tool", "''::text AS call_key", "'completed'::text AS call_status")
	}
	return "SELECT " + strings.Join(selects, ", ") + " FROM " + table, missing, true
}

// automationCostLedgerSource keeps the cost overview available while an
// additive local migration catches up. It performs one metadata query rather
// than probing every column individually, and only uses static table and
// column identifiers owned by this package.
func automationCostLedgerSource(db *gorm.DB) (string, automationCostLedgerCoverage, error) {
	coverage := automationCostLedgerCoverage{State: "unavailable"}
	if db == nil {
		return "", coverage, fmt.Errorf("automation cost ledger database is unavailable")
	}

	var rows []automationCostLedgerColumn
	if err := db.Raw(`SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name IN (?, ?)`, automationExecutionLedgerTable, automationToolExecutionLedgerTable).Scan(&rows).Error; err != nil {
		return "", coverage, fmt.Errorf("read automation cost ledger schema: %w", err)
	}

	columnsByTable := map[string]map[string]struct{}{
		automationExecutionLedgerTable:     {},
		automationToolExecutionLedgerTable: {},
	}
	for _, row := range rows {
		columns, ok := columnsByTable[row.TableName]
		if !ok {
			continue
		}
		columns[row.ColumnName] = struct{}{}
	}

	agentProjection, agentMissing, agentOK := automationCostLedgerProjection(automationExecutionLedgerTable, columnsByTable[automationExecutionLedgerTable], "agent")
	if !agentOK {
		coverage.UnknownDimensions = agentMissing
		return "", coverage, fmt.Errorf("automation execution ledger is missing required columns")
	}
	coverage.AgentLedger = true
	coverage.UnknownDimensions = append(coverage.UnknownDimensions, agentMissing...)

	parts := []string{agentProjection}
	if len(columnsByTable[automationToolExecutionLedgerTable]) > 0 {
		toolProjection, toolMissing, toolOK := automationCostLedgerProjection(automationToolExecutionLedgerTable, columnsByTable[automationToolExecutionLedgerTable], "tool")
		if toolOK {
			parts = append(parts, toolProjection)
			coverage.ToolLedger = true
			coverage.UnknownDimensions = append(coverage.UnknownDimensions, toolMissing...)
		} else {
			coverage.UnknownDimensions = append(coverage.UnknownDimensions, "tool_ledger")
		}
	} else {
		coverage.UnknownDimensions = append(coverage.UnknownDimensions, "tool_ledger")
	}

	coverage.State = "complete"
	if !coverage.ToolLedger || len(coverage.UnknownDimensions) > 0 {
		coverage.State = "partial"
	}
	return strings.Join(parts, " UNION ALL "), coverage, nil
}

// automationWorkerHealth is a deliberately small, anonymized view of a live
// worker. It lets an operator distinguish a configured runtime from a merely
// non-empty queue without exposing worker IDs, hostnames, queue locations,
// prompts, results or credentials.
type automationWorkerHealth struct {
	Provider               string                      `json:"provider"`
	Model                  string                      `json:"model"`
	Role                   string                      `json:"role,omitempty"`
	Lane                   string                      `json:"lane,omitempty"`
	Concurrency            int                         `json:"concurrency"`
	StartedAt              time.Time                   `json:"started_at"`
	LastSeenAt             time.Time                   `json:"last_seen_at"`
	WorkspaceReadiness     []automationWorkspaceHealth `gorm:"-" json:"workspace_readiness,omitempty"`
	WorkspaceReadinessJSON string                      `gorm:"column:workspace_readiness" json:"-"`
}

// automationWorkspaceHealth is the only workspace-level information that
// crosses the isolated worker boundary. It intentionally excludes a local
// path, repository name, Git SHA, branch, command line, error output and any
// source material.
type automationWorkspaceHealth struct {
	ID                          string `json:"id"`
	Ready                       bool   `json:"ready"`
	QAReady                     bool   `json:"qa_ready"`
	VisualQAReady               bool   `json:"visual_qa_ready"`
	PublicationReady            bool   `json:"publication_ready"`
	ValidationCommandCount      int    `json:"validation_command_count"`
	NamedValidationCommandCount int    `json:"named_validation_command_count"`
	QACommandCount              int    `json:"qa_command_count"`
	NamedQACommandCount         int    `json:"named_qa_command_count"`
}

// automationHealth gives operators a continuous safety signal without
// exposing private prompts, results or lease identifiers outside the
// task-scoped inspector. Workers contains only current, anonymous runtime
// metadata so that readiness claims remain auditable in the dashboard.
type automationHealth struct {
	Queued                        int64                                 `json:"queued"`
	Running                       int64                                 `json:"running"`
	FailedLastDay                 int64                                 `json:"failed_last_day"`
	ExpiredLeases                 int64                                 `json:"expired_leases"`
	SpendLastDay                  int64                                 `json:"spend_last_day_microusd"`
	ActiveWorkers                 int64                                 `json:"active_workers"`
	WorkerCapacity                int64                                 `json:"worker_capacity"`
	QueueTelemetry                bool                                  `json:"queue_telemetry_available"`
	QueueLanes                    map[string]automationqueue.LaneHealth `json:"queue_lanes,omitempty"`
	QueueVisible                  int64                                 `json:"queue_visible_approximate"`
	QueueInFlight                 int64                                 `json:"queue_in_flight_approximate"`
	QueueDelayed                  int64                                 `json:"queue_delayed_approximate"`
	DeadLetterTelemetry           bool                                  `json:"dead_letter_telemetry_available"`
	DeadLetterVisible             int64                                 `json:"dead_letter_visible_approximate"`
	OperationalTelemetryAvailable bool                                  `json:"operational_telemetry_available"`
	LastWorkerSeenAt              *time.Time                            `json:"last_worker_seen_at,omitempty"`
	Workers                       []automationWorkerHealth              `json:"workers"`
	ReviewIngress                 automationReviewIngressHealth         `json:"review_ingress"`
}

// automationReviewIngressHealth makes automatic PR review operationally
// visible without disclosing the webhook secret, GitHub App identity, or the
// repositories themselves. "ready" means the ingress is configured and at
// least one agent heartbeat can consume the private queue; it does not claim
// that a particular webhook delivery has succeeded.
type automationReviewIngressHealth struct {
	Enabled                bool `json:"enabled"`
	GitHubAppConfigured    bool `json:"github_app_configured"`
	AllowedRepositoryCount int  `json:"allowed_repository_count"`
	WorkerAvailable        bool `json:"worker_available"`
	Ready                  bool `json:"ready"`
}

type agentHeartbeatRequest struct {
	WorkerID           string                      `json:"worker_id"`
	Provider           string                      `json:"provider"`
	Model              string                      `json:"model"`
	Role               string                      `json:"role"`
	Lane               string                      `json:"lane"`
	Concurrency        int                         `json:"concurrency"`
	StartedAt          string                      `json:"started_at"`
	WorkspaceReadiness []automationWorkspaceHealth `json:"workspace_readiness"`
}

// CreateInputUploadURL keeps prompt/document inputs in ITBEM storage before a
// task is accepted. The agent receives only this private object reference.
func CreateInputUploadURL(c echo.Context) error {
	var request inputUploadRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation input", "input content must be valid JSON")
	}
	cfg, _ := c.Get("config").(*models.Config)
	bucket := ""
	if cfg != nil {
		bucket = strings.TrimSpace(cfg.AutomationInputBucket)
	}
	if strings.TrimSpace(bucket) == "" {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "ITBEM storage is not configured")
	}
	key := "automation/inputs/" + uuid.Must(uuid.NewV4()).String() + "/input.json"
	if len(request.Content) > 0 {
		if !localAutomationInputProxyAllowed() {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation input", "direct input upload is available only through the private upload URL")
		}
		if len(request.Content) > maxLocalAutomationInputBytes || !json.Valid(request.Content) {
			return utils.Error(c, http.StatusRequestEntityTooLarge, "Automation input too large", "local automation input must be valid JSON up to 256 KB")
		}
		if err := awsrepository.UploadEncryptedJSON(c.Request().Context(), request.Content, key, bucket); err != nil {
			return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Could not store the private automation input")
		}
		return utils.Success(c, http.StatusOK, "Automation input stored", inputUploadResponse{InputRef: "s3://" + bucket + "/" + key, ExpiresIn: 0, LocalProxyUploadSafe: true})
	}
	uploadURL, err := awsrepository.GeneratePresignedPutURL(c.Request().Context(), key, bucket, "application/json", 15)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Could not prepare private input upload")
	}
	return utils.Success(c, http.StatusOK, "Automation input upload URL generated", inputUploadResponse{InputRef: "s3://" + bucket + "/" + key, UploadURL: uploadURL, ExpiresIn: 900, LocalProxyUploadSafe: localAutomationInputProxyAllowed()})
}

func localAutomationInputProxyAllowed() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("ENV")), "local")
}

// Create accepts private object references only. Heavy payloads remain on the
// local agent and in ITBEM storage, never in the API or SQS message body.
func Create(c echo.Context) error {
	var request createTaskRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation task", err.Error())
	}
	request.Operation = strings.ToLower(strings.TrimSpace(request.Operation))
	request.InputRef = strings.TrimSpace(request.InputRef)
	cfg, _ := c.Get("config").(*models.Config)
	if !operationPattern.MatchString(request.Operation) || !inputReferenceMatches(cfg, request.InputRef) {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation task", "operation or input_ref is invalid")
	}
	if !genericTaskOperationAllowed(request.Operation) {
		if strings.HasPrefix(request.Operation, "delivery.") {
			return utils.Error(c, http.StatusConflict, "Delivery operation is gated", "Start this phase from its Delivery work item after the required human gate")
		}
		return utils.Error(c, http.StatusBadRequest, "Invalid automation task", "operation is not enabled")
	}
	if configuration.DB == nil || !automationqueue.IsConfigured() {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "The ITBEM local agent queue is not configured")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if requestedBy == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	taskID := uuid.Must(uuid.NewV4())
	jobID := uuid.Must(uuid.NewV4())
	correlationID := taskID.String()
	maxCompletionTokens := automationagent.CompletionTokensForOperation(request.Operation)
	task := &models.AutomationTask{ID: taskID, JobID: jobID, RequestedBy: requestedBy, CorrelationID: correlationID, Operation: request.Operation, MaxCompletionTokens: maxCompletionTokens, InputRef: request.InputRef, Status: "queued"}
	message := automationqueue.Message{SchemaVersion: 1, JobID: jobID.String(), TenantCode: "itbem", CorrelationID: correlationID, Type: "ai.local.process"}
	message.Payload.TaskID, message.Payload.Operation, message.Payload.MaxCompletionTokens, message.Payload.InputRef, message.Payload.Attempt = taskID.String(), task.Operation, task.MaxCompletionTokens, task.InputRef, 1
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		enqueued, err := outboxService.EnqueueAutomationProcess(c.Request().Context(), tx, message)
		if err != nil {
			return err
		}
		if !enqueued {
			return fmt.Errorf("automation task delivery was not enqueued")
		}
		return nil
	}); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation task failed", "Could not persist task delivery")
	}
	return utils.Success(c, http.StatusAccepted, "Automation task queued", task)
}

func List(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	var tasks []models.AutomationTask
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	// GitHub-originated reviews have a technical requester, so a platform
	// administrator must be able to see their lifecycle in the same list as
	// manually submitted automation. Non-administrators remain strictly scoped
	// to their own generic tasks; Delivery task reads continue through project
	// membership checks in their dedicated surfaces.
	query := configuration.DB
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if !user.IsPlatformAdmin() {
		query = query.Where("requested_by = ?", requestedBy)
	}
	if err := query.Order("created_at DESC").Limit(100).Find(&tasks).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation tasks unavailable", "")
	}
	return utils.Success(c, http.StatusOK, "Automation tasks", tasks)
}

// Cancel stops a queued task immediately or records a cancellation request for
// the exact worker lease already in flight. A provider call cannot be revoked
// once it has left the machine, so an in-flight completion remains allowed to
// persist its immutable usage ledger, but it can no longer advance delivery,
// attach QA evidence, or create change-set records.
func Cancel(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	taskID, err := uuid.FromString(c.Param("id"))
	if err != nil || taskID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation task", "task id must be a UUID")
	}
	request := cancelTaskRequest{}
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation cancellation", err.Error())
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" || len(reason) > 600 {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation cancellation", "reason must contain between 1 and 600 characters")
	}
	var task models.AutomationTask
	if err := configuration.DB.First(&task, taskID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation cancellation unavailable", "")
	}
	if !mayCancelTask(c, &task, requestedBy) {
		return utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot cancel this automation task")
	}
	now := time.Now().UTC()
	message := "Cancellation requested by an authorized operator: " + reason
	updates := map[string]any{"error_message": message}
	statusCode := http.StatusAccepted
	responseMessage := "Automation cancellation requested"
	switch task.Status {
	case "queued":
		updates["status"] = "cancelled"
		updates["completed_at"] = now
		updates["lease_expires_at"] = nil
		updates["budget_reservation_micros"] = 0
		updates["budget_reservation_expires_at"] = nil
		statusCode = http.StatusOK
		responseMessage = "Queued automation task cancelled"
	case "running":
		updates["status"] = "cancel_requested"
	case "cancel_requested":
		return utils.Success(c, http.StatusAccepted, "Automation cancellation already requested", task)
	default:
		return utils.Error(c, http.StatusConflict, "Automation cancellation rejected", "Only queued or running tasks can be cancelled")
	}
	result := configuration.DB.Model(&models.AutomationTask{}).Where("id = ? AND status = ?", task.ID, task.Status).Updates(updates)
	if result.Error != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation cancellation unavailable", "")
	}
	if result.RowsAffected != 1 {
		return utils.Error(c, http.StatusConflict, "Automation cancellation rejected", "Task state changed; refresh and review it again")
	}
	for key, value := range updates {
		switch key {
		case "status":
			task.Status, _ = value.(string)
		case "error_message":
			task.ErrorMessage, _ = value.(string)
		}
	}
	return utils.Success(c, statusCode, responseMessage, task)
}

// RetryCodeReview creates a fresh, explicitly authorized execution for the
// exact immutable input of a terminal failed review. GitHub redelivery is
// deliberately not used as a retry mechanism: a delivery identifies a PR
// revision, while this action makes the additional provider call visible to
// the operator and keeps the original failed result auditable.
func RetryCodeReview(c echo.Context) error {
	if configuration.DB == nil || !automationqueue.IsConfigured() {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "The ITBEM local agent queue is not configured")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	taskID, err := uuid.FromString(c.Param("id"))
	if err != nil || taskID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation task", "task id must be a UUID")
	}
	var original models.AutomationTask
	if err := configuration.DB.First(&original, taskID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation retry unavailable", "")
	}
	if !retryableCodeReviewTask(&original) {
		return utils.Error(c, http.StatusConflict, "Automation retry rejected", "Only a failed code review with its immutable input can be retried")
	}
	if !mayRetryAutomationTask(c, &original, requestedBy) {
		return utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot retry this automation task")
	}

	newTaskID, newJobID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	retry := &models.AutomationTask{
		ID:                  newTaskID,
		JobID:               newJobID,
		RequestedBy:         original.RequestedBy,
		DeliveryWorkItemID:  original.DeliveryWorkItemID,
		CorrelationID:       original.CorrelationID,
		Operation:           original.Operation,
		MaxCompletionTokens: original.MaxCompletionTokens,
		InputRef:            original.InputRef,
		Status:              "queued",
	}
	message := automationqueue.Message{SchemaVersion: 1, JobID: newJobID.String(), TenantCode: "itbem", CorrelationID: retry.CorrelationID, Type: "ai.local.process"}
	message.Payload.TaskID, message.Payload.Operation, message.Payload.MaxCompletionTokens, message.Payload.InputRef, message.Payload.Attempt = newTaskID.String(), retry.Operation, retry.MaxCompletionTokens, retry.InputRef, 1
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(retry).Error; err != nil {
			return err
		}
		queued, enqueueErr := outboxService.EnqueueAutomationProcess(c.Request().Context(), tx, message)
		if enqueueErr != nil {
			return enqueueErr
		}
		if !queued {
			return fmt.Errorf("automation retry delivery was not enqueued")
		}
		return nil
	}); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation retry failed", "Could not persist review retry delivery")
	}
	return utils.Success(c, http.StatusAccepted, "Code review retry queued", retry)
}

// taskResultIsInspectable deliberately includes a cancelled in-flight task
// when the worker has already returned a bounded private result. Cancellation
// blocks workflow progression; it never erases paid work or its audit trail.
func taskResultIsInspectable(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// A cancellation request remains potentially chargeable until its worker run
// settles, so it retains its admission reservation during that short window.
func activeAutomationBudgetStatuses() []string {
	return []string{"queued", "running", "cancel_requested"}
}

// GetOutput issues a short-lived result URL to the task requester, a platform
// administrator, or an authorized reader of the linked Delivery project.
// Result objects remain private and never become dashboard-accessible bucket
// paths.
func GetOutput(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid task ID", "")
	}
	var task models.AutomationTask
	if err := configuration.DB.First(&task, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation output unavailable", "")
	}
	if !mayAccessTask(c, &task, requestedBy) {
		return utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot access this automation task")
	}
	cfg, _ := c.Get("config").(*models.Config)
	if !taskResultIsInspectable(task.Status) || !outputReferenceMatches(cfg, task.ID, task.OutputRef) {
		return utils.Error(c, http.StatusConflict, "Automation output unavailable", "Task has no private result")
	}
	bucket, key, err := privateReference(task.OutputRef)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "Automation output unavailable", "Task has no private result")
	}
	url, err := awsrepository.GeneratePresignedURL(c.Request().Context(), key, bucket, 10)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation output unavailable", "Could not prepare private result download")
	}
	return utils.Success(c, http.StatusOK, "Automation output URL generated", outputDownloadResponse{DownloadURL: url, ExpiresIn: 600})
}

// GetOutputContent streams the small, structured agent result through the
// authenticated API. This keeps the dashboard reliable on localhost (where a
// browser may not be able to follow a LocalStack presigned URL) while retaining
// the same task-level access check and deterministic private-object reference.
// Larger QA artifacts continue to use short-lived object URLs.
func GetOutputContent(c echo.Context) error {
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation task", "")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	var task models.AutomationTask
	if err := configuration.DB.First(&task, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation output unavailable", "")
	}
	if !mayAccessTask(c, &task, requestedBy) {
		return utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot access this automation task")
	}
	cfg, _ := c.Get("config").(*models.Config)
	if !taskResultIsInspectable(task.Status) || !outputReferenceMatches(cfg, task.ID, task.OutputRef) {
		return utils.Error(c, http.StatusConflict, "Automation output unavailable", "Task has no private result")
	}
	bucket, key, referenceErr := privateReference(task.OutputRef)
	if referenceErr != nil {
		return utils.Error(c, http.StatusConflict, "Automation output unavailable", "Task has no private result")
	}
	body, err := awsrepository.GetS3Object(c.Request().Context(), key, bucket)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation output unavailable", "Could not read the private result")
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, 256*1024))
	if err != nil || len(content) == 0 {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation output unavailable", "Could not read the private result")
	}
	var output map[string]any
	if err := json.Unmarshal(content, &output); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation output unavailable", "Private result is not valid JSON")
	}
	return utils.Success(c, http.StatusOK, "Automation result loaded", output)
}

// GetInputContent lets an authorized Delivery reviewer inspect exactly what
// was sent to the agent. It intentionally reads the task's immutable S3
// reference, never a caller-supplied key, and returns a bounded JSON object.
func GetInputContent(c echo.Context) error {
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation task", "")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	var task models.AutomationTask
	if err := configuration.DB.First(&task, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation input unavailable", "")
	}
	if !mayAccessTask(c, &task, requestedBy) {
		return utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot access this automation task")
	}
	cfg, _ := c.Get("config").(*models.Config)
	if !inputReferenceMatches(cfg, task.InputRef) {
		return utils.Error(c, http.StatusConflict, "Automation input unavailable", "Task input is not a valid private reference")
	}
	bucket, key, err := privateReference(task.InputRef)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "Automation input unavailable", "Task input is not a valid private reference")
	}
	body, err := awsrepository.GetS3Object(c.Request().Context(), key, bucket)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation input unavailable", "Could not read the private input")
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, 256*1024))
	if err != nil || len(content) == 0 || !json.Valid(content) {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation input unavailable", "Could not read the private input")
	}
	return utils.Success(c, http.StatusOK, "Automation input loaded", json.RawMessage(content))
}

// GetExecutionInputContent exposes the exact bounded request retained for one
// immutable model call. A task can have retries or multiple model calls, so
// inspecting the task-level input is not enough for financial or technical
// audit. The execution itself never supplies a caller-controlled object key.
func GetExecutionInputContent(c echo.Context) error {
	return getExecutionContent(c, "input")
}

// GetExecutionResultContent exposes the exact private response retained for
// one immutable model call. It intentionally shares the same access boundary
// as its parent task and does not expose raw object storage references.
func GetExecutionResultContent(c echo.Context) error {
	return getExecutionContent(c, "result")
}

// GetExecutionInputDownload gives an authorized reviewer a short-lived URL
// for the complete immutable request. The inline inspector remains bounded so
// a large project context cannot destabilize the dashboard.
func GetExecutionInputDownload(c echo.Context) error {
	return getExecutionDownload(c, "input")
}

// GetExecutionResultDownload is the response counterpart of
// GetExecutionInputDownload. It is intentionally execution-scoped: retries
// and multi-step tasks must never be mistaken for the latest task result.
func GetExecutionResultDownload(c echo.Context) error {
	return getExecutionDownload(c, "result")
}

func authorizedExecutionObject(c echo.Context, kind string) (bucket, key string, err error) {
	if configuration.DB == nil {
		return "", "", utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	executionID, err := uuid.FromString(c.Param("id"))
	if err != nil || executionID == uuid.Nil {
		return "", "", utils.Error(c, http.StatusBadRequest, "Invalid automation execution", "")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	var execution models.AutomationExecution
	if err := configuration.DB.First(&execution, executionID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", "", utils.Error(c, http.StatusNotFound, "Automation execution not found", "")
		}
		return "", "", utils.Error(c, http.StatusInternalServerError, "Automation execution unavailable", "")
	}
	var task models.AutomationTask
	if err := configuration.DB.First(&task, execution.AutomationTaskID).Error; err != nil {
		return "", "", utils.Error(c, http.StatusInternalServerError, "Automation execution unavailable", "Could not resolve the parent task")
	}
	if !mayAccessTask(c, &task, requestedBy) {
		return "", "", utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot access this automation execution")
	}
	cfg, _ := c.Get("config").(*models.Config)
	reference := execution.RequestRef
	valid := inputReferenceMatches(cfg, reference) || executionRequestReferenceMatches(cfg, task.ID, execution.RunID, reference)
	if kind == "result" {
		reference = execution.ResponseRef
		valid = outputReferenceMatches(cfg, task.ID, reference)
	}
	if !valid {
		return "", "", utils.Error(c, http.StatusConflict, "Automation execution unavailable", "This execution does not retain a valid private "+kind+" reference")
	}
	bucket, key, err = privateReference(reference)
	if err != nil {
		return "", "", utils.Error(c, http.StatusConflict, "Automation execution unavailable", "This execution does not retain a valid private "+kind+" reference")
	}
	return bucket, key, nil
}

func getExecutionContent(c echo.Context, kind string) error {
	bucket, key, err := authorizedExecutionObject(c, kind)
	if err != nil {
		return err
	}
	body, err := awsrepository.GetS3Object(c.Request().Context(), key, bucket)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation execution unavailable", "Could not read the private "+kind)
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, 256*1024))
	if err != nil || len(content) == 0 || !json.Valid(content) {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation execution unavailable", "Could not read the private "+kind)
	}
	message := "Automation execution input loaded"
	if kind == "result" {
		message = "Automation execution result loaded"
	}
	return utils.Success(c, http.StatusOK, message, json.RawMessage(content))
}

func getExecutionDownload(c echo.Context, kind string) error {
	bucket, key, err := authorizedExecutionObject(c, kind)
	if err != nil {
		return err
	}
	url, err := awsrepository.GeneratePresignedURL(c.Request().Context(), key, bucket, 10)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation execution unavailable", "Could not prepare the private "+kind+" download")
	}
	return utils.Success(c, http.StatusOK, "Automation execution "+kind+" download URL generated", outputDownloadResponse{DownloadURL: url, ExpiresIn: 600})
}

// GetToolExecutionReportContent exposes the one private report retained for a
// billable tool call. Stagehand's bounded request, provider response excerpt,
// browser cases and evidence metadata live together in this immutable report,
// so there is no caller-supplied object key or second unrestricted endpoint.
func GetToolExecutionReportContent(c echo.Context) error {
	bucket, key, err := authorizedToolExecutionReport(c)
	if err != nil {
		return err
	}
	body, err := awsrepository.GetS3Object(c.Request().Context(), key, bucket)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation tool execution unavailable", "Could not read the private tool report")
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, 256*1024))
	if err != nil || len(content) == 0 || !json.Valid(content) {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation tool execution unavailable", "Could not read the private tool report")
	}
	return utils.Success(c, http.StatusOK, "Automation tool execution report loaded", json.RawMessage(content))
}

// GetToolExecutionReportDownload provides the full report for an authorized
// reviewer. It is deliberately separate from agent execution downloads: a
// tool report contains an evidence-specific request/response contract.
func GetToolExecutionReportDownload(c echo.Context) error {
	bucket, key, err := authorizedToolExecutionReport(c)
	if err != nil {
		return err
	}
	url, err := awsrepository.GeneratePresignedURL(c.Request().Context(), key, bucket, 10)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation tool execution unavailable", "Could not prepare the private tool report download")
	}
	return utils.Success(c, http.StatusOK, "Automation tool execution report download URL generated", outputDownloadResponse{DownloadURL: url, ExpiresIn: 600})
}

func authorizedToolExecutionReport(c echo.Context) (bucket, key string, err error) {
	if configuration.DB == nil {
		return "", "", utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	executionID, err := uuid.FromString(c.Param("id"))
	if err != nil || executionID == uuid.Nil {
		return "", "", utils.Error(c, http.StatusBadRequest, "Invalid automation tool execution", "")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	var execution models.AutomationToolExecution
	if err := configuration.DB.First(&execution, executionID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", "", utils.Error(c, http.StatusNotFound, "Automation tool execution not found", "")
		}
		return "", "", utils.Error(c, http.StatusInternalServerError, "Automation tool execution unavailable", "")
	}
	var task models.AutomationTask
	if err := configuration.DB.First(&task, execution.AutomationTaskID).Error; err != nil {
		return "", "", utils.Error(c, http.StatusInternalServerError, "Automation tool execution unavailable", "Could not resolve the parent task")
	}
	if !mayAccessTask(c, &task, requestedBy) {
		return "", "", utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot access this automation tool execution")
	}
	cfg, _ := c.Get("config").(*models.Config)
	if execution.Tool != "stagehand" || execution.RequestRef != execution.ResponseRef || !toolReportReferenceMatches(cfg, task.ID, execution.ResponseRef) {
		return "", "", utils.Error(c, http.StatusConflict, "Automation tool execution unavailable", "This tool report does not retain a valid private reference")
	}
	bucket, key, err = privateReference(execution.ResponseRef)
	if err != nil {
		return "", "", utils.Error(c, http.StatusConflict, "Automation tool execution unavailable", "This tool report does not retain a valid private reference")
	}
	return bucket, key, nil
}

// GetTrace is the detail source for the execution drawer: one workflow task
// and every model call attached to it. Private object locations are omitted;
// the dedicated inspector endpoints authorize each content read instead.
func GetTrace(c echo.Context) error {
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation task", "")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	var task models.AutomationTask
	if err := configuration.DB.First(&task, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation trace unavailable", "")
	}
	if !mayAccessTask(c, &task, requestedBy) {
		return utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot access this automation task")
	}
	var executions []models.AutomationExecution
	if err := configuration.DB.Where("automation_task_id = ?", task.ID).Order("completed_at ASC").Find(&executions).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation trace unavailable", "")
	}
	var toolExecutions []models.AutomationToolExecution
	if err := configuration.DB.Where("automation_task_id = ?", task.ID).Order("completed_at ASC").Find(&toolExecutions).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation trace unavailable", "")
	}
	// `executions` and `tool_executions` remain for backwards compatibility,
	// while entries is the canonical, chronological cross-runtime ledger used by
	// delivery UI and external integrations. It deliberately contains no object
	// references: the scoped inspector endpoints authorize every private read.
	entries := make([]automationCostExecution, 0, len(executions)+len(toolExecutions))
	for _, execution := range executions {
		entries = append(entries, traceEntryFromAgentExecution(execution))
	}
	for _, execution := range toolExecutions {
		entries = append(entries, traceEntryFromToolExecution(execution))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CompletedAt.Equal(entries[j].CompletedAt) {
			return entries[i].ID.String() < entries[j].ID.String()
		}
		return entries[i].CompletedAt.Before(entries[j].CompletedAt)
	})
	return utils.Success(c, http.StatusOK, "Automation execution trace", map[string]any{
		"task":            task,
		"executions":      executions,
		"tool_executions": toolExecutions,
		"entries":         entries,
	})
}

func traceEntryFromAgentExecution(execution models.AutomationExecution) automationCostExecution {
	return automationCostExecution{
		ID: execution.ID, AutomationTaskID: execution.AutomationTaskID, DeliveryWorkItemID: execution.DeliveryWorkItemID,
		ExecutionKind: "agent", StepKey: execution.StepKey, Provider: execution.Provider, Model: execution.Model,
		InputTokens: execution.InputTokens, OutputTokens: execution.OutputTokens, CachedInputTokens: execution.CachedInputTokens,
		CacheWriteTokens: execution.CacheWriteTokens, ReasoningTokens: execution.ReasoningTokens, TotalTokens: execution.TotalTokens,
		InputCostMicros: execution.InputCostMicros, OutputCostMicros: execution.OutputCostMicros, CachedCostMicros: execution.CachedCostMicros,
		CacheWriteCostMicros: execution.CacheWriteCostMicros, TotalCostMicros: execution.TotalCostMicros,
		PricingBasis: execution.PricingBasis, ProviderOutcome: providerOutcomeFromUsage(execution.UsageJSON), CompletedAt: execution.CompletedAt,
	}
}

func traceEntryFromToolExecution(execution models.AutomationToolExecution) automationCostExecution {
	return automationCostExecution{
		ID: execution.ID, AutomationTaskID: execution.AutomationTaskID, DeliveryWorkItemID: execution.DeliveryWorkItemID,
		ExecutionKind: "tool", Tool: execution.Tool, CallKey: execution.CallKey, CallStatus: execution.CallStatus, StepKey: execution.StepKey, Provider: execution.Provider, Model: execution.Model,
		InputTokens: execution.InputTokens, OutputTokens: execution.OutputTokens, CachedInputTokens: execution.CachedInputTokens,
		CacheWriteTokens: execution.CacheWriteTokens, ReasoningTokens: execution.ReasoningTokens, TotalTokens: execution.TotalTokens,
		InputCostMicros: execution.InputCostMicros, OutputCostMicros: execution.OutputCostMicros, CachedCostMicros: execution.CachedCostMicros,
		CacheWriteCostMicros: execution.CacheWriteCostMicros, TotalCostMicros: execution.TotalCostMicros,
		PricingBasis: execution.PricingBasis, ProviderOutcome: providerOutcomeFromUsage(execution.UsageJSON), CompletedAt: execution.CompletedAt,
	}
}

// CostOverview is intentionally aggregated server-side, so a dashboard never
// has to download prompts, results, or all execution rows just to show spend.
func CostOverview(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation costs unavailable", "Database is unavailable")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	days := 30
	if raw := strings.TrimSpace(c.QueryParam("days")); raw != "" {
		if _, err := fmt.Sscan(raw, &days); err != nil || days < 1 || days > 365 {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation cost range", "days must be from 1 to 365")
		}
	}
	page, pageSize := 1, 40
	if raw := strings.TrimSpace(c.QueryParam("page")); raw != "" {
		if _, err := fmt.Sscan(raw, &page); err != nil || page < 1 {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation cost page", "page must be a positive integer")
		}
	}
	if raw := strings.TrimSpace(c.QueryParam("page_size")); raw != "" {
		if _, err := fmt.Sscan(raw, &pageSize); err != nil || pageSize < 1 || pageSize > 100 {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation cost page size", "page_size must be from 1 to 100")
		}
	}
	// Resolve the actor once. A cost overview is available to a task owner and
	// to project members that can view the linked Delivery work item. A code or
	// QA reviewer therefore sees the spend behind the gates they are asked to
	// approve, without gaining visibility into unrelated generic automation.
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	ledgerSource, ledgerCoverage, ledgerErr := automationCostLedgerSource(configuration.DB)
	if ledgerErr != nil {
		return utils.ErrorWithData(c, http.StatusServiceUnavailable, "Automation costs unavailable", "Cost ledger is initializing", map[string]any{"ledger_coverage": ledgerCoverage})
	}
	baseQuery := configuration.DB.Table("("+ledgerSource+") AS execution").
		Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id").
		Joins("LEFT JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id").
		Joins("LEFT JOIN delivery_projects AS project ON project.id = work_item.project_id").
		Where("execution.completed_at >= ?", time.Now().UTC().AddDate(0, 0, -days))
	if !user.IsPlatformAdmin() {
		projectIDs, membershipErr := deliveryReadableProjectIDs(user.CognitoSub)
		if membershipErr != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not resolve delivery project access")
		}
		if len(projectIDs) > 0 {
			baseQuery = baseQuery.Where("task.requested_by = ? OR work_item.project_id IN ?", requestedBy, projectIDs)
		} else {
			baseQuery = baseQuery.Where("task.requested_by = ?", requestedBy)
		}
	}
	summary := automationCostSummary{}
	// Keep the statements independent: the aggregate and the breakdown have
	// different select/group shapes and must never leak state into one another.
	if err := baseQuery.Session(&gorm.Session{}).Select("COUNT(*) AS executions, COUNT(DISTINCT execution.automation_task_id) AS tasks, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Scan(&summary).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var byOperation []automationCostBreakdown
	if err := baseQuery.Session(&gorm.Session{}).Select("task.operation AS key, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("task.operation").Order("SUM(execution.total_cost_micros) DESC").Scan(&byOperation).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	// Operations describe the requested capability; step keys describe the
	// delivery phase that actually spent the budget. Keep both dimensions so an
	// operator can distinguish, for example, plan generation from browser QA.
	var byStep []automationCostBreakdown
	if err := baseQuery.Session(&gorm.Session{}).Select("COALESCE(NULLIF(execution.step_key, ''), 'execution') AS key, execution.execution_kind, execution.tool, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("execution.step_key, execution.execution_kind, execution.tool").Order("SUM(execution.total_cost_micros) DESC").Scan(&byStep).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var byProject []automationCostProject
	if err := baseQuery.Session(&gorm.Session{}).Select("project.id AS project_id, COALESCE(NULLIF(project.name, ''), 'Automatización general') AS project_name, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("project.id, project.name").Order("SUM(execution.total_cost_micros) DESC").Scan(&byProject).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var byModel []automationCostModel
	if err := baseQuery.Session(&gorm.Session{}).Select("COALESCE(NULLIF(execution.provider, ''), 'sin proveedor') AS provider, COALESCE(NULLIF(execution.model, ''), 'sin modelo') AS model, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("execution.provider, execution.model").Order("SUM(execution.total_cost_micros) DESC").Scan(&byModel).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var recentTotal int64
	if err := baseQuery.Session(&gorm.Session{}).Count(&recentTotal).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	recentPage := automationCostExecutionPage{Page: page, PageSize: pageSize, Total: recentTotal}
	if recentTotal > 0 {
		recentPage.TotalPages = int((recentTotal + int64(pageSize) - 1) / int64(pageSize))
	}
	var recentExecutions []automationCostExecution
	if err := baseQuery.Session(&gorm.Session{}).
		Select(automationCostRecentExecutionSelect).
		Order("execution.completed_at DESC, execution.id DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).Scan(&recentExecutions).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	budgetWatch := make([]automationCostBudgetWatch, 0)
	taskBudgetWatch := make([]automationCostTaskBudgetWatch, 0)
	if user.IsPlatformAdmin() {
		monthStart := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
		var currentMonthBudgets []automationCostBudgetWatch
		if err := configuration.DB.Table("delivery_projects AS project").
			Select("project.id AS project_id, project.name AS project_name, project.monthly_budget_micros, project.budget_alert_percent AS alert_percent, COALESCE(SUM(execution.total_cost_micros), 0) AS spent_micros").
			Joins("LEFT JOIN delivery_work_items AS work_item ON work_item.project_id = project.id AND work_item.deleted_at IS NULL").
			Joins("LEFT JOIN ("+ledgerSource+") AS execution ON execution.delivery_work_item_id = work_item.id AND execution.completed_at >= ?", monthStart).
			Where("project.deleted_at IS NULL AND project.monthly_budget_micros > 0").
			Group("project.id, project.name, project.monthly_budget_micros, project.budget_alert_percent").
			Scan(&currentMonthBudgets).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
		}
		var reservations []struct {
			ProjectID      uuid.UUID `gorm:"column:project_id"`
			ReservedMicros int64     `gorm:"column:reserved_micros"`
		}
		if err := configuration.DB.Table("automation_tasks AS task").
			Select("work_item.project_id AS project_id, COALESCE(SUM(task.budget_reservation_micros), 0) AS reserved_micros").
			Joins("JOIN delivery_work_items AS work_item ON work_item.id = task.delivery_work_item_id AND work_item.deleted_at IS NULL").
			Where("task.created_at >= ? AND task.status IN ? AND task.budget_reservation_micros > 0 AND (task.budget_reservation_expires_at IS NULL OR task.budget_reservation_expires_at > ?)", monthStart, activeAutomationBudgetStatuses(), time.Now().UTC()).
			Group("work_item.project_id").Scan(&reservations).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
		}
		reservedByProject := make(map[uuid.UUID]int64, len(reservations))
		for _, reservation := range reservations {
			reservedByProject[reservation.ProjectID] = reservation.ReservedMicros
		}
		for _, project := range currentMonthBudgets {
			project.ReservedMicros = reservedByProject[project.ProjectID]
			budgetWatch = append(budgetWatch, finalizeBudgetWatch(project))
		}

		// A task budget is an all-time cap. Do not scope execution spend by the
		// overview range or calendar month: doing so would advertise capacity the
		// task is not actually allowed to spend.
		var currentTaskBudgets []automationCostTaskBudgetWatch
		if err := configuration.DB.Table("delivery_work_items AS work_item").
			Select("project.id AS project_id, project.name AS project_name, work_item.id AS work_item_id, work_item.title AS work_item_title, work_item.budget_micros, work_item.budget_alert_percent AS alert_percent, COALESCE(SUM(execution.total_cost_micros), 0) AS spent_micros").
			Joins("JOIN delivery_projects AS project ON project.id = work_item.project_id AND project.deleted_at IS NULL").
			Joins("LEFT JOIN (" + ledgerSource + ") AS execution ON execution.delivery_work_item_id = work_item.id").
			Where("work_item.deleted_at IS NULL AND work_item.budget_micros > 0").
			Group("project.id, project.name, work_item.id, work_item.title, work_item.budget_micros, work_item.budget_alert_percent").
			Order("work_item.updated_at DESC").
			Scan(&currentTaskBudgets).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
		}
		var taskReservations []struct {
			WorkItemID     uuid.UUID `gorm:"column:work_item_id"`
			ReservedMicros int64     `gorm:"column:reserved_micros"`
		}
		if err := configuration.DB.Table("automation_tasks AS task").
			Select("task.delivery_work_item_id AS work_item_id, COALESCE(SUM(task.budget_reservation_micros), 0) AS reserved_micros").
			Where("task.delivery_work_item_id IS NOT NULL AND task.status IN ? AND task.budget_reservation_micros > 0 AND (task.budget_reservation_expires_at IS NULL OR task.budget_reservation_expires_at > ?)", activeAutomationBudgetStatuses(), time.Now().UTC()).
			Group("task.delivery_work_item_id").Scan(&taskReservations).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
		}
		reservedByWorkItem := make(map[uuid.UUID]int64, len(taskReservations))
		for _, reservation := range taskReservations {
			reservedByWorkItem[reservation.WorkItemID] = reservation.ReservedMicros
		}
		for _, workItem := range currentTaskBudgets {
			workItem.ReservedMicros = reservedByWorkItem[workItem.WorkItemID]
			taskBudgetWatch = append(taskBudgetWatch, finalizeTaskBudgetWatch(workItem))
		}
	}
	return utils.Success(c, http.StatusOK, "Automation cost overview", map[string]any{"range_days": days, "summary": summary, "by_operation": byOperation, "by_step": byStep, "by_project": byProject, "by_model": byModel, "budget_watch": budgetWatch, "task_budget_watch": taskBudgetWatch, "recent_execution_page": recentPage, "recent_executions": recentExecutions, "ledger_coverage": ledgerCoverage})
}

func finalizeBudgetWatch(watch automationCostBudgetWatch) automationCostBudgetWatch {
	if watch.MonthlyBudgetMicros <= 0 {
		return watch
	}
	if watch.SpentMicros < 0 {
		watch.SpentMicros = 0
	}
	if watch.ReservedMicros < 0 {
		watch.ReservedMicros = 0
	}
	watch.AllocatedMicros = watch.SpentMicros + watch.ReservedMicros
	if watch.AllocatedMicros >= watch.MonthlyBudgetMicros {
		watch.RemainingMicros = 0
		watch.UsagePercent = 100
		watch.Status = "exceeded"
		return watch
	}
	watch.RemainingMicros = watch.MonthlyBudgetMicros - watch.AllocatedMicros
	watch.UsagePercent = int((watch.AllocatedMicros * 100) / watch.MonthlyBudgetMicros)
	if watch.UsagePercent >= watch.AlertPercent {
		watch.Status = "attention"
	} else {
		watch.Status = "healthy"
	}
	return watch
}

func finalizeTaskBudgetWatch(watch automationCostTaskBudgetWatch) automationCostTaskBudgetWatch {
	if watch.BudgetMicros <= 0 {
		return watch
	}
	if watch.SpentMicros < 0 {
		watch.SpentMicros = 0
	}
	if watch.ReservedMicros < 0 {
		watch.ReservedMicros = 0
	}
	watch.AllocatedMicros = watch.SpentMicros + watch.ReservedMicros
	if watch.AllocatedMicros >= watch.BudgetMicros {
		watch.RemainingMicros = 0
		watch.UsagePercent = 100
		watch.Status = "exceeded"
		return watch
	}
	watch.RemainingMicros = watch.BudgetMicros - watch.AllocatedMicros
	watch.UsagePercent = int((watch.AllocatedMicros * 100) / watch.BudgetMicros)
	if watch.UsagePercent >= watch.AlertPercent {
		watch.Status = "attention"
	} else {
		watch.Status = "healthy"
	}
	return watch
}

func Health(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation health unavailable", "Database is unavailable")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	query := configuration.DB.Model(&models.AutomationTask{})
	if !user.IsPlatformAdmin() {
		query = query.Where("requested_by = ?", requestedBy)
	}
	now := time.Now().UTC()
	result := automationHealth{}
	if err := query.Session(&gorm.Session{}).Where("status = ?", "queued").Count(&result.Queued).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if err := query.Session(&gorm.Session{}).Where("status = ?", "running").Count(&result.Running).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if err := query.Session(&gorm.Session{}).Where("status = ? AND completed_at >= ?", "failed", now.Add(-24*time.Hour)).Count(&result.FailedLastDay).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if err := query.Session(&gorm.Session{}).Where("status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?", "running", now).Count(&result.ExpiredLeases).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	// Queue depth spans every tenant-scoped task on the shared local agent.
	// It is operational telemetry for platform admins only; a regular
	// requester must not infer another project's activity from it.
	if user.IsPlatformAdmin() {
		queueHealth := automationqueue.QueueHealth(c.Request().Context())
		result.QueueTelemetry = queueHealth.Available
		result.QueueLanes = queueHealth.Lanes
		result.QueueVisible, result.QueueInFlight, result.QueueDelayed = queueHealth.Visible, queueHealth.InFlight, queueHealth.Delayed
		result.DeadLetterTelemetry, result.DeadLetterVisible = queueHealth.DeadLetterAvailable, queueHealth.DeadLetterVisible
	}
	// Health must use the same schema-compatible accounting projection as the
	// cost screen. Otherwise an optional tool-ledger migration can make a
	// decorative health read fail even though the primary execution ledger is
	// available and authoritative.
	ledgerSource, _, ledgerErr := automationCostLedgerSource(configuration.DB)
	if ledgerErr != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation health unavailable", "Cost ledger is initializing")
	}
	spendQuery := configuration.DB.Table("("+ledgerSource+") AS execution").Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id").Where("execution.completed_at >= ?", now.Add(-24*time.Hour))
	if !user.IsPlatformAdmin() {
		spendQuery = spendQuery.Where("task.requested_by = ?", requestedBy)
	}
	if err := spendQuery.Select("COALESCE(SUM(execution.total_cost_micros), 0) AS spend_last_day").Scan(&result.SpendLastDay).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if user.IsPlatformAdmin() {
		result.OperationalTelemetryAvailable = true
		workerQuery := configuration.DB.Model(&models.AutomationAgentHeartbeat{}).Where("last_seen_at >= ?", now.Add(-90*time.Second))
		if err := workerQuery.Session(&gorm.Session{}).Count(&result.ActiveWorkers).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
		}
		if err := workerQuery.Session(&gorm.Session{}).Select("COALESCE(SUM(concurrency), 0) AS worker_capacity").Scan(&result.WorkerCapacity).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
		}
		lastSeen, err := automationWorkerLastSeen(workerQuery)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
		}
		if lastSeen != nil {
			result.LastWorkerSeenAt = lastSeen
		}
		if err := workerQuery.Session(&gorm.Session{}).
			Select("provider, model, role, lane, concurrency, started_at, last_seen_at, workspace_readiness").
			Order("last_seen_at DESC").
			Limit(8).
			Find(&result.Workers).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
		}
		for index := range result.Workers {
			// Legacy heartbeats predate workspace readiness. Keep them visible as
			// live workers, but omit their preflight data instead of fabricating a
			// ready state.
			if strings.TrimSpace(result.Workers[index].WorkspaceReadinessJSON) == "" {
				continue
			}
			if err := json.Unmarshal([]byte(result.Workers[index].WorkspaceReadinessJSON), &result.Workers[index].WorkspaceReadiness); err != nil {
				return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
			}
		}
		var reviewWorkers int64
		if err := workerQuery.Session(&gorm.Session{}).
			Where("(role = '' AND lane = '') OR (role = ? AND lane = ?)", agentwork.RoleReviewer, agentwork.LaneReview).
			Count(&reviewWorkers).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
		}
		cfg, _ := c.Get("config").(*models.Config)
		result.ReviewIngress = automationReviewIngressStatus(cfg, reviewWorkers)
	}
	return utils.Success(c, http.StatusOK, "Automation health", result)
}

func automationReviewIngressStatus(cfg *models.Config, activeWorkers int64) automationReviewIngressHealth {
	status := automationReviewIngressHealth{WorkerAvailable: activeWorkers > 0}
	if cfg == nil {
		return status
	}
	status.Enabled = githubReviewWebhookConfigured(cfg)
	status.AllowedRepositoryCount = len(githubReviewRepositories(cfg.GitHubReviewRepositories))
	if _, err := automationagent.LoadGitHubAppConfig(os.Getenv); err == nil {
		status.GitHubAppConfigured = true
	}
	status.Ready = status.Enabled && status.GitHubAppConfigured && status.WorkerAvailable
	return status
}

// automationWorkerLastSeen accepts an empty heartbeat table as a healthy
// local state. PostgreSQL returns NULL for MAX() in that case, which must not
// be scanned into time.Time because database/sql correctly rejects NULL there.
func automationWorkerLastSeen(query *gorm.DB) (*time.Time, error) {
	var lastSeen sql.NullTime
	if err := query.Session(&gorm.Session{}).Select("MAX(last_seen_at)").Scan(&lastSeen).Error; err != nil {
		return nil, err
	}
	if !lastSeen.Valid {
		return nil, nil
	}
	value := lastSeen.Time
	return &value, nil
}

func validateWorkerWorkspaceReadiness(readiness []automationWorkspaceHealth) error {
	if len(readiness) > 32 {
		return fmt.Errorf("too many workspaces")
	}
	seen := make(map[string]struct{}, len(readiness))
	for _, workspace := range readiness {
		if !workerWorkspaceIDPattern.MatchString(workspace.ID) {
			return fmt.Errorf("invalid workspace id")
		}
		if _, exists := seen[workspace.ID]; exists {
			return fmt.Errorf("duplicate workspace id")
		}
		seen[workspace.ID] = struct{}{}
		if workspace.ValidationCommandCount < 0 || workspace.ValidationCommandCount > 64 || workspace.QACommandCount < 0 || workspace.QACommandCount > 64 {
			return fmt.Errorf("invalid workspace command count")
		}
		if workspace.NamedValidationCommandCount < 0 || workspace.NamedValidationCommandCount > workspace.ValidationCommandCount || workspace.NamedQACommandCount < 0 || workspace.NamedQACommandCount > workspace.QACommandCount {
			return fmt.Errorf("invalid named workspace command count")
		}
		if workspace.QAReady && !workspace.Ready {
			return fmt.Errorf("qa readiness requires workspace readiness")
		}
		if workspace.VisualQAReady && !workspace.Ready {
			return fmt.Errorf("visual qa readiness requires workspace readiness")
		}
		if workspace.PublicationReady && !workspace.Ready {
			return fmt.Errorf("publication readiness requires workspace readiness")
		}
	}
	return nil
}

func normalizeWorkerRoleLane(role, lane string) (string, string, error) {
	role, lane = strings.TrimSpace(role), strings.TrimSpace(lane)
	if role == "" && lane == "" {
		return "", "", nil
	}
	if !agentwork.IsKnownRoleLane(agentwork.Role(role), agentwork.Lane(lane)) {
		return "", "", fmt.Errorf("invalid worker role and queue lane")
	}
	return role, lane, nil
}

func validWorkerProvider(role, lane, provider, model string) bool {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if role == string(agentwork.RoleReleaseManager) && lane == string(agentwork.LaneRelease) {
		return provider == "" && model == ""
	}
	return providerAllowed(provider) && model != "" && len(model) <= 128
}

// AgentHeartbeat gives the dashboard a real liveness signal from the isolated
// worker. It is callback-secret authenticated and stores no host, queue or
// credential information.
func AgentHeartbeat(c echo.Context) error {
	if !validCallbackSecret(c.Request().Header.Get("X-Automation-Secret")) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "")
	}
	var request agentHeartbeatRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	workerID := strings.TrimSpace(request.WorkerID)
	if _, err := uuid.FromString(workerID); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	if request.Concurrency < 1 || request.Concurrency > 8 {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	role, lane, err := normalizeWorkerRoleLane(request.Role, request.Lane)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	if !validWorkerProvider(role, lane, request.Provider, request.Model) {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	startedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(request.StartedAt))
	if err != nil || startedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	if err := validateWorkerWorkspaceReadiness(request.WorkspaceReadiness); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	workspaceReadiness, err := json.Marshal(request.WorkspaceReadiness)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	now := time.Now().UTC()
	heartbeat := models.AutomationAgentHeartbeat{WorkerID: workerID, Provider: strings.ToLower(strings.TrimSpace(request.Provider)), Model: strings.TrimSpace(request.Model), Role: role, Lane: lane, Concurrency: request.Concurrency, WorkspaceReadiness: string(workspaceReadiness), StartedAt: startedAt.UTC(), LastSeenAt: now}
	if err := configuration.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "worker_id"}}, DoUpdates: clause.Assignments(map[string]any{"provider": heartbeat.Provider, "model": heartbeat.Model, "role": heartbeat.Role, "lane": heartbeat.Lane, "concurrency": heartbeat.Concurrency, "workspace_readiness": heartbeat.WorkspaceReadiness, "started_at": heartbeat.StartedAt, "last_seen_at": heartbeat.LastSeenAt, "updated_at": now})}).Create(&heartbeat).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation heartbeat unavailable", "")
	}
	return utils.Success(c, http.StatusOK, "Automation agent heartbeat accepted", map[string]any{"accepted_at": now})
}

// GetArtifact issues a short-lived URL for a task-scoped QA artifact. The
// object key is derived from the authenticated owner's task ID, so the caller
// cannot choose a bucket, prefix, or another task's evidence.
func GetArtifact(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid task ID", "")
	}
	name := strings.TrimSpace(c.Param("name"))
	if !artifactNamePattern.MatchString(name) {
		return utils.Error(c, http.StatusBadRequest, "Invalid artifact", "Artifact name is invalid")
	}
	var task models.AutomationTask
	if err := configuration.DB.Where("id = ? AND status = ?", id, "completed").First(&task).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation artifact unavailable", "")
	}
	if !mayAccessTask(c, &task, requestedBy) {
		return utils.Error(c, http.StatusForbidden, "Forbidden", "You cannot access this automation task")
	}
	cfg, _ := c.Get("config").(*models.Config)
	if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation artifact unavailable", "Private output storage is not configured")
	}
	key := "automation/" + task.ID.String() + "/artifacts/" + name
	url, err := awsrepository.GeneratePresignedURL(c.Request().Context(), key, cfg.AutomationOutputBucket, 10)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation artifact unavailable", "Could not prepare private artifact download")
	}
	return utils.Success(c, http.StatusOK, "Automation artifact URL generated", outputDownloadResponse{DownloadURL: url, ExpiresIn: 600})
}

type callbackRequest struct {
	Status             string                  `json:"status"`
	RunID              string                  `json:"run_id"`
	RecoveryRunID      string                  `json:"recovery_run_id"`
	RequestRef         string                  `json:"request_ref"`
	OutputRef          string                  `json:"output_ref"`
	ErrorMessage       string                  `json:"error_message"`
	Provider           string                  `json:"provider"`
	Model              string                  `json:"model"`
	ProviderResponseID string                  `json:"provider_response_id"`
	Usage              json.RawMessage         `json:"usage"`
	Artifacts          []callbackArtifact      `json:"artifacts"`
	ToolExecutions     []callbackToolExecution `json:"tool_executions"`
	Execution          json.RawMessage         `json:"execution"`
	Deterministic      bool                    `json:"deterministic"`
}

// callbackToolExecution is an independently billable model call from a
// pinned runtime tool. It carries only usage and private object references;
// the controller derives all financial fields from its own price catalog.
type callbackToolExecution struct {
	Tool        string          `json:"tool"`
	CallKey     string          `json:"call_key"`
	CallStatus  string          `json:"call_status"`
	StepKey     string          `json:"step_key"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	Usage       json.RawMessage `json:"usage"`
	RequestRef  string          `json:"request_ref"`
	ResponseRef string          `json:"response_ref"`
}

// callbackArtifact is intentionally small: it lets the API make QA assets
// visible in the delivery timeline without passing their contents through the
// queue or persistence layer.
type callbackArtifact struct {
	Name        string `json:"name"`
	Reference   string `json:"reference"`
	ContentType string `json:"content_type"`
	SizeBytes   int    `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

func Complete(c echo.Context) error {
	if !validCallbackSecret(c.Request().Header.Get("X-Automation-Secret")) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid task ID", "")
	}
	var request callbackRequest
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", err.Error())
	}
	request.Status = strings.ToLower(strings.TrimSpace(request.Status))
	if request.Status != "running" && request.Status != "completed" && request.Status != "failed" {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "status must be completed or failed")
	}
	cfg, _ := c.Get("config").(*models.Config)
	if (request.Status == "completed" || (request.Status == "failed" && strings.TrimSpace(request.OutputRef) != "")) && !outputReferenceMatches(cfg, id, request.OutputRef) {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "output_ref must be an ITBEM private object reference")
	}
	var task models.AutomationTask
	if err := configuration.DB.First(&task, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation result failed", "")
	}
	request.RunID = strings.TrimSpace(request.RunID)
	request.RecoveryRunID = strings.TrimSpace(request.RecoveryRunID)
	if request.Status == "running" {
		if _, err := uuid.FromString(request.RunID); err != nil || request.RunID == "" {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "running tasks require a valid run lease ID")
		}
		return claimAutomationTaskRun(c, id, request.RunID)
	}
	cancellationRequested := task.Status == "cancel_requested" && request.RunID != "" && task.RunID == request.RunID
	if request.RunID == "" || (task.Status != "running" && !cancellationRequested) || task.RunID != request.RunID {
		return utils.Error(c, http.StatusConflict, "Automation result ignored", "Task is not held by this worker run")
	}
	request.RequestRef = strings.TrimSpace(request.RequestRef)
	ledgerRunID := request.RunID
	if request.RecoveryRunID != "" {
		if request.Status == "running" {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "a recovery run may only complete a stored result")
		}
		if _, err := uuid.FromString(request.RecoveryRunID); err != nil || request.RecoveryRunID == request.RunID {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "recovery_run_id must identify a prior immutable run")
		}
		if !executionRequestReferenceMatches(cfg, task.ID, request.RecoveryRunID, request.RequestRef) || !executionResultReferenceMatches(cfg, task.ID, request.RecoveryRunID, request.OutputRef) {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "recovery evidence must match the original private run")
		}
		ledgerRunID = request.RecoveryRunID
	}
	// Admission reserves the maximum possible spend before the provider is
	// called. A late callback must not convert an expired hold into ledger spend:
	// the agent will retain its immutable private result, receive this narrow
	// retry signal, reclaim a fresh lease (and reservation), then publish the
	// same result without another model call.
	if !cancellationRequested && task.BudgetReservationMicros > 0 && task.BudgetReservationExpiresAt != nil && !task.BudgetReservationExpiresAt.After(time.Now().UTC()) {
		c.Response().Header().Set(retryReservationHeader, "1")
		return utils.Error(c, http.StatusConflict, "Automation budget reservation expired", "The worker will safely retry this stored result with a renewed reservation")
	}
	if err := validateCallbackArtifacts(cfg, &task, id, request.Artifacts); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation evidence", err.Error())
	}
	if len(request.Execution) > 0 {
		limit := 32 * 1024
		if task.Operation == "delivery.release_gate" {
			limit = 256 * 1024
		}
		if request.Status != "completed" || (task.Operation != "code.review" && task.Operation != "delivery.implementation" && task.Operation != "delivery.onboarding_probe" && task.Operation != "delivery.publish" && task.Operation != "delivery.release_gate" && task.Operation != "delivery.qa") || len(request.Execution) > limit || !json.Valid(request.Execution) {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation execution", "only a bounded completed review or delivery execution may register execution metadata")
		}
	}
	if request.Deterministic && (request.Status != "completed" || (task.Operation != "delivery.onboarding_probe" && task.Operation != "delivery.publish" && task.Operation != "delivery.release_gate")) {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "only onboarding probes, delivery publication, or release Gatekeeper may be deterministic")
	}
	if task.Operation == "delivery.release_gate" && request.Status == "completed" && (!request.Deterministic || len(request.Execution) == 0) {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "release Gatekeeper completion requires deterministic execution evidence")
	}
	if task.Operation == "delivery.onboarding_probe" && request.Status == "completed" && (!request.Deterministic || len(request.Execution) == 0) {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "onboarding probe completion requires deterministic execution evidence")
	}
	if task.Operation == "code.review" && task.RequestedBy == "github-app-review" && request.Status == "completed" && len(request.Execution) == 0 {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "remote code review completion requires exact GitHub publication evidence")
	}
	toolExecutionRows, toolExecutionErr := buildToolExecutionLedger(cfg, &task, ledgerRunID, request.Status, request.ToolExecutions, request.Artifacts, time.Now().UTC())
	if toolExecutionErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation tool execution", toolExecutionErr.Error())
	}
	updates := map[string]interface{}{"status": request.Status}
	expectedStatus := "running"
	if cancellationRequested {
		updates["status"] = "cancelled"
		expectedStatus = "cancel_requested"
	}
	where := "id = ? AND status = ? AND run_id = ?"
	args := []interface{}{id, expectedStatus, request.RunID}
	completedAt := time.Time{}
	var execution *models.AutomationExecution
	if request.Status != "running" {
		completedAt = time.Now().UTC()
		updates["output_ref"] = strings.TrimSpace(request.OutputRef)
		if cancellationRequested {
			// Preserve the human cancellation rationale. The worker's terminal
			// error is still retained in the private result/ledger when present,
			// but it must not make a cancelled task look self-initiated.
			updates["error_message"] = task.ErrorMessage
		} else {
			updates["error_message"] = strings.TrimSpace(request.ErrorMessage)
		}
		updates["completed_at"] = completedAt
		updates["budget_reservation_expires_at"] = nil
		// A failed task that reports provider accounting did receive a real model
		// answer, whether its private result reached storage or not. Cost it like
		// any other call; failures without usage remain transport/input failures.
		// This prevents a storage outage after a provider response from erasing
		// spend or causing the worker to repeat a billable call.
		providerCallReported := len(request.Usage) > 0 || strings.TrimSpace(request.ProviderResponseID) != ""
		if (request.Status == "completed" || (request.Status == "failed" && providerCallReported)) && !request.Deterministic {
			// New workers store the canonical provider request before making a
			// billable call. The empty fallback supports records produced before
			// that immutable execution audit object existed.
			if request.RequestRef != "" && !executionRequestReferenceMatches(cfg, task.ID, ledgerRunID, request.RequestRef) {
				return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "request_ref must be this execution's private request object")
			}
			request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
			if !providerAllowed(request.Provider) || strings.TrimSpace(request.Model) == "" || len(request.Usage) == 0 || !json.Valid(request.Usage) {
				return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "completed tasks require valid approved-provider execution metadata")
			}
			var usage map[string]any
			if err := json.Unmarshal(request.Usage, &usage); err != nil || usage == nil {
				return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "completed tasks require a usage object")
			}
			ledger, err := automationcost.Build(request.Provider, strings.TrimSpace(request.Model), usage, pricingCatalog(cfg))
			if err != nil {
				return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "provider usage could not be costed: "+err.Error())
			}
			updates["provider"] = request.Provider
			updates["model"] = strings.TrimSpace(request.Model)
			updates["provider_response_id"] = strings.TrimSpace(request.ProviderResponseID)
			updates["usage_json"] = string(request.Usage)
			requestReference := task.InputRef
			if request.RequestRef != "" {
				requestReference = request.RequestRef
			}
			execution = &models.AutomationExecution{
				AutomationTaskID: task.ID, DeliveryWorkItemID: task.DeliveryWorkItemID, RunID: ledgerRunID, StepKey: executionStepKey(task.Operation),
				Provider: request.Provider, Model: strings.TrimSpace(request.Model), ProviderResponseID: strings.TrimSpace(request.ProviderResponseID),
				InputTokens: ledger.InputTokens, OutputTokens: ledger.OutputTokens, CachedInputTokens: ledger.CachedInputTokens,
				CacheWriteTokens: ledger.CacheWriteTokens, ReasoningTokens: ledger.ReasoningTokens, TotalTokens: ledger.TotalTokens,
				InputCostMicros: ledger.InputCostMicros, OutputCostMicros: ledger.OutputCostMicros, CachedCostMicros: ledger.CachedCostMicros,
				CacheWriteCostMicros: ledger.CacheWriteCostMicros, TotalCostMicros: ledger.TotalCostMicros, Currency: "USD",
				PricingBasis: ledger.PricingBasis, PricingSnapshotJSON: ledger.PricingSnapshot, UsageJSON: string(request.Usage),
				RequestRef: requestReference, ResponseRef: strings.TrimSpace(request.OutputRef), CompletedAt: completedAt,
			}
		}
	}
	rowsAffected := int64(0)
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.AutomationTask{}).Where(where, args...).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		rowsAffected = result.RowsAffected
		if result.RowsAffected == 0 {
			return nil
		}
		if execution != nil {
			if err := tx.Create(execution).Error; err != nil {
				return err
			}
		}
		for _, toolExecution := range toolExecutionRows {
			if err := tx.Create(&toolExecution).Error; err != nil {
				return err
			}
		}
		if !cancellationRequested && request.Status == "completed" && len(request.Artifacts) > 0 {
			if err := persistDeliveryQAEvidence(tx, &task, request.Artifacts, completedAt); err != nil {
				return err
			}
		}
		if !cancellationRequested && request.Status == "completed" && len(request.Execution) > 0 {
			if task.Operation == "code.review" {
				if err := persistCodeReviewPublication(tx, &task, request.Execution, completedAt); err != nil {
					return err
				}
			}
			if task.Operation == "delivery.onboarding_probe" {
				if err := persistOnboardingCapabilityProbes(tx, &task, request.Execution, completedAt); err != nil {
					return err
				}
			}
			if task.Operation == "delivery.implementation" {
				if err := persistImplementationChangeSet(tx, &task, request.Execution, completedAt); err != nil {
					return err
				}
			}
			if task.Operation == "delivery.publish" {
				if err := persistPublicationChangeSet(tx, &task, request.Execution, completedAt); err != nil {
					return err
				}
			}
			if task.Operation == "delivery.qa" {
				if err := persistQAObservation(tx, &task, request.Execution, completedAt); err != nil {
					return err
				}
			}
			if task.Operation == "delivery.release_gate" {
				if err := persistReleaseGateEvaluation(tx, &task, request.Execution, completedAt); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation result failed", "")
	}
	if rowsAffected == 0 {
		return utils.Error(c, http.StatusConflict, "Automation result ignored", "Task is not awaiting a result")
	}
	return c.NoContent(http.StatusNoContent)
}

func persistCodeReviewPublication(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, completedAt time.Time) error {
	publication, err := codeReviewPublicationForTask(task, raw)
	if err != nil {
		return err
	}
	publication.AutomationTaskID, publication.PublishedAt = task.ID, publication.PublishedAt.UTC()
	if publication.PublishedAt.After(completedAt.Add(time.Minute)) {
		return fmt.Errorf("code review publication time is invalid")
	}
	return tx.Create(&publication).Error
}

func codeReviewPublicationForTask(task *models.AutomationTask, raw json.RawMessage) (models.AutomationCodeReviewPublication, error) {
	if task == nil || task.ID == uuid.Nil || task.Operation != "code.review" || task.RequestedBy != "github-app-review" || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))) {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("only an exact webhook review task may publish GitHub review evidence")
	}
	var execution automationagent.GitHubCodeReviewPublication
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&execution); err != nil {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review publication evidence is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review publication evidence is invalid")
	}
	repository := strings.ToLower(strings.TrimSpace(execution.Repository))
	verdict := strings.ToLower(strings.TrimSpace(execution.Verdict))
	event := strings.ToUpper(strings.TrimSpace(execution.Event))
	actor := strings.ToLower(strings.TrimSpace(execution.ReviewerActor))
	author := strings.ToLower(strings.TrimSpace(execution.AuthorActor))
	if execution.SchemaVersion != 1 || !githubRepositoryPattern.MatchString(repository) || execution.PullRequest < 1 || !gitCommitSHA.MatchString(strings.ToLower(strings.TrimSpace(execution.HeadSHA))) || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(execution.PatchSHA256))) || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(execution.SubjectSHA256))) || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(execution.PayloadSHA256))) || !strings.EqualFold(execution.SubjectSHA256, task.EvidenceSubjectDigest) || execution.ReviewID < 1 || actor == "" || execution.PublishedAt.IsZero() {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review publication evidence is invalid")
	}
	if task.CorrelationID != "github-pr:"+repository+":"+strconv.Itoa(execution.PullRequest)+":"+strings.ToLower(execution.HeadSHA) || !validGitHubReviewURL(execution.ReviewURL, repository, execution.PullRequest, execution.ReviewID) {
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review publication does not match its queued pull request")
	}
	switch event {
	case "APPROVE":
		if verdict != "approve" || author == "" || strings.EqualFold(actor, author) {
			return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review approval is not independent")
		}
	case "REQUEST_CHANGES":
		if verdict != "request_changes" {
			return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review event contradicts its verdict")
		}
	case "COMMENT":
		if verdict != "comment" && verdict != "blocked" && (verdict != "approve" || author == "" || !strings.EqualFold(actor, author)) {
			return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review comment contradicts its verdict")
		}
	default:
		return models.AutomationCodeReviewPublication{}, fmt.Errorf("code review event is invalid")
	}
	return models.AutomationCodeReviewPublication{
		Repository: repository, PullRequest: execution.PullRequest, HeadSHA: strings.ToLower(execution.HeadSHA), PatchSHA256: strings.ToLower(execution.PatchSHA256),
		SubjectSHA256: strings.ToLower(execution.SubjectSHA256), PayloadSHA256: strings.ToLower(execution.PayloadSHA256), Verdict: verdict, Event: event,
		ReviewID: execution.ReviewID, ReviewURL: strings.TrimSpace(execution.ReviewURL), ReviewerActor: actor, AuthorActor: author, PublishedAt: execution.PublishedAt,
	}, nil
}

func validGitHubReviewURL(value, repository string, pullRequest int, reviewID int64) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || pullRequest < 1 || reviewID < 1 {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[0]+"/"+parts[1], repository) || parts[2] != "pull" || parts[3] != strconv.Itoa(pullRequest) {
		return false
	}
	return parsed.Fragment == "pullrequestreview-"+strconv.FormatInt(reviewID, 10)
}

func persistOnboardingCapabilityProbes(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, completedAt time.Time) error {
	if tx == nil || completedAt.IsZero() {
		return fmt.Errorf("only a bounded onboarding task may append capability probes")
	}
	execution, queuedSubject, err := onboardingCapabilityProbeForTask(task, raw)
	if err != nil {
		return err
	}
	var onboarding models.DeliveryRepositoryOnboarding
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", *task.DeliveryOnboardingID).First(&onboarding).Error; err != nil {
		return err
	}
	if onboarding.Status != "proposed" || !strings.EqualFold(onboarding.ProposalSHA256, queuedSubject) || onboarding.RepositoryReference != execution.RepositoryReference || onboarding.DefaultBranch != execution.DefaultBranch || !strings.EqualFold(onboarding.Revision, execution.Revision) {
		return fmt.Errorf("onboarding capability probe subject is stale or mismatched")
	}
	proposal, err := projectvault.ValidateStoredProposal(
		onboarding.ProposalJSON, onboarding.RepositoryReference, onboarding.DefaultBranch,
		onboarding.Revision, onboarding.Readiness, onboarding.ProposalSHA256, onboarding.VaultSHA256,
	)
	if err != nil {
		return err
	}
	updated, err := projectvault.ApplyCapabilityProbes(proposal, execution.Probes)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	proposalDigest, err := projectvault.ProposalSHA256(updated)
	if err != nil {
		return err
	}
	for _, probe := range execution.Probes {
		row := models.DeliveryRepositoryCapabilityProbe{
			ProjectID: onboarding.ProjectID, OnboardingID: onboarding.ID, AutomationTaskID: task.ID,
			RepositoryReference: execution.RepositoryReference, Revision: execution.Revision,
			Capability: probe.Name, State: probe.State, ExecutorRole: execution.ExecutorRole,
			EvidenceSHA256: strings.ToLower(probe.EvidenceSHA256), SubjectSHA256: strings.ToLower(probe.SubjectSHA256),
			Reason: probe.Reason, ObservedAt: completedAt.UTC(),
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	result := tx.Model(&models.DeliveryRepositoryOnboarding{}).
		Where("id = ? AND status = ? AND proposal_sha256 = ?", onboarding.ID, "proposed", onboarding.ProposalSHA256).
		Updates(map[string]any{"proposal_json": string(encoded), "proposal_sha256": proposalDigest, "readiness": updated.Readiness})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("onboarding capability probe lost its proposal checkpoint")
	}
	return nil
}

func onboardingCapabilityProbeForTask(task *models.AutomationTask, raw json.RawMessage) (automationagent.OnboardingProbeExecution, string, error) {
	if task == nil || task.ID == uuid.Nil || task.Operation != "delivery.onboarding_probe" || task.DeliveryOnboardingID == nil || *task.DeliveryOnboardingID == uuid.Nil {
		return automationagent.OnboardingProbeExecution{}, "", fmt.Errorf("only a bounded onboarding task may append capability probes")
	}
	execution, err := automationagent.DecodeOnboardingProbeExecution(raw)
	if err != nil || execution.TaskID != task.ID.String() {
		return automationagent.OnboardingProbeExecution{}, "", fmt.Errorf("onboarding capability probes do not match their automation task")
	}
	queuedSubject := strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))
	if !artifactDigestPattern.MatchString(queuedSubject) {
		return automationagent.OnboardingProbeExecution{}, "", fmt.Errorf("onboarding capability probe task has no exact proposal subject")
	}
	return execution, queuedSubject, nil
}

func persistReleaseGateEvaluation(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, completedAt time.Time) error {
	if tx == nil || completedAt.IsZero() {
		return fmt.Errorf("only a bounded release Gatekeeper task may append an evaluation")
	}
	workItemID, actor, input, environment, err := releaseGateCandidateForTask(task, raw)
	if err != nil {
		return err
	}
	if environment != nil {
		if _, _, err := deliveryledger.RecordEnvironmentObservation(tx, workItemID, *environment, completedAt.UTC()); err != nil {
			return err
		}
	}
	input, err = releasegatecontrol.Resolve(tx, workItemID, input, completedAt.UTC())
	if err != nil {
		return err
	}
	preApproval := releasegate.Evaluate(input)
	if preApproval.SubjectDigest == "" {
		return fmt.Errorf("release Gatekeeper execution has no exact subject")
	}
	input.HumanApproval = &releasegate.HumanApproval{Actor: actor, ActorType: "human", SubjectDigest: preApproval.SubjectDigest, Approved: true}
	if _, _, err := deliveryledger.RecordGateEvaluation(tx, workItemID, input, completedAt.UTC()); err != nil {
		return err
	}
	return nil
}

func persistQAObservation(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, completedAt time.Time) error {
	if tx == nil || completedAt.IsZero() {
		return fmt.Errorf("only a bounded QA task may append an observation")
	}
	workItemID, observation, err := qaObservationForTask(task, raw)
	if err != nil {
		return err
	}
	if _, _, err := deliveryledger.RecordQAObservation(tx, workItemID, observation, completedAt.UTC()); err != nil {
		return err
	}
	security, complete, err := securityObservationFromQA(observation)
	if err != nil {
		return err
	}
	if complete {
		if _, _, err := deliveryledger.RecordSecurityObservation(tx, workItemID, security, completedAt.UTC()); err != nil {
			return err
		}
	}
	return nil
}

const (
	securitySecretsTestKind      = "security:secrets"
	securityHighCriticalTestKind = "security:high-critical"
)

// securityObservationFromQA promotes only operator-owned reserved command
// identities. Missing scanners do not become an event, so the release gate
// remains explicitly missing instead of treating an unavailable tool as pass.
func securityObservationFromQA(observation qaevidence.Observation) (securityevidence.Observation, bool, error) {
	if err := qaevidence.Validate(observation); err != nil {
		return securityevidence.Observation{}, false, err
	}
	repositories := make([]securityevidence.Repository, 0, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		commands := make(map[string]qaevidence.Command, len(repository.Commands))
		for _, command := range repository.Commands {
			commands[strings.ToLower(command.Kind)] = command
		}
		secretScan, hasSecretScan := commands[securitySecretsTestKind]
		highCritical, hasHighCritical := commands[securityHighCriticalTestKind]
		if !hasSecretScan || !hasHighCritical {
			return securityevidence.Observation{}, false, nil
		}
		highFindings := 0
		if !highCritical.Passed {
			// The bounded command contract exposes pass/fail, not scanner details.
			// One means at least one high-or-critical finding was observed.
			highFindings = 1
		}
		repositories = append(repositories, securityevidence.Repository{
			Reference: repository.Reference, Branch: repository.Branch, SecretScanPassed: secretScan.Passed,
			HighFindings: highFindings, CriticalFindings: 0,
		})
	}
	security := securityevidence.Observation{
		SchemaVersion: securityevidence.SchemaVersion, TaskID: observation.TaskID, MatrixDigest: observation.MatrixDigest, Repositories: repositories,
	}
	if err := securityevidence.Validate(security); err != nil {
		return securityevidence.Observation{}, false, err
	}
	return security, true, nil
}

func qaObservationForTask(task *models.AutomationTask, raw json.RawMessage) (uuid.UUID, qaevidence.Observation, error) {
	if task == nil || task.ID == uuid.Nil || task.Operation != "delivery.qa" || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID == uuid.Nil {
		return uuid.Nil, qaevidence.Observation{}, fmt.Errorf("only a bounded QA task may append an observation")
	}
	observation, err := qaevidence.Decode(raw)
	if err != nil {
		return uuid.Nil, qaevidence.Observation{}, err
	}
	subject := strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))
	if observation.TaskID != task.ID.String() || !strings.EqualFold(observation.MatrixDigest, subject) || !artifactDigestPattern.MatchString(subject) {
		return uuid.Nil, qaevidence.Observation{}, fmt.Errorf("QA observation does not match its exact queued subject")
	}
	return *task.DeliveryWorkItemID, observation, nil
}

func releaseGateCandidateForTask(task *models.AutomationTask, raw json.RawMessage) (uuid.UUID, string, releasegate.Input, *environmentevidence.Observation, error) {
	if task == nil || task.Operation != "delivery.release_gate" || task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID == uuid.Nil {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("only a bounded release Gatekeeper task may append an evaluation")
	}
	actor := strings.TrimSpace(task.RequestedBy)
	if actor == "" || actor == "github-app-review" || actor == "itbem-local-agent" || actor == "itbem-github-app" {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper task does not have an authenticated human requester")
	}
	var handoff map[string]json.RawMessage
	if err := json.Unmarshal(raw, &handoff); err != nil || (len(handoff) != 2 && len(handoff) != 3) {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper execution metadata is invalid")
	}
	var schemaVersion int
	if err := json.Unmarshal(handoff["schema_version"], &schemaVersion); err != nil || (schemaVersion != 1 && schemaVersion != 2) || (schemaVersion == 1 && len(handoff) != 2) || (schemaVersion == 2 && len(handoff) != 3) {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper execution schema is invalid")
	}
	input, err := releasegate.DecodeInput(handoff["gatekeeper_input"])
	if err != nil || input.SchemaVersion != releasegate.SchemaVersion || input.Action != releasegate.ActionRelease || input.ChangeSetID != task.DeliveryWorkItemID.String() || input.HumanApproval != nil {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release Gatekeeper execution candidate is invalid")
	}
	if schemaVersion == 1 {
		// Rolling upgrades may finish a task on an old release worker. Accept its
		// exact GitHub PR/check candidate but attach no environment event, so the
		// deterministic Gatekeeper remains blocked until a schema-v2 rerun.
		return *task.DeliveryWorkItemID, actor, input, nil, nil
	}
	environment, err := environmentevidence.Decode(handoff["environment_observation"])
	subject := strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))
	if err != nil || environment.TaskID != task.ID.String() || !strings.EqualFold(environment.MatrixDigest, subject) || !artifactDigestPattern.MatchString(subject) {
		return uuid.Nil, "", releasegate.Input{}, nil, fmt.Errorf("release environment observation does not match its exact queued subject")
	}
	return *task.DeliveryWorkItemID, actor, input, &environment, nil
}

func buildToolExecutionLedger(cfg *models.Config, task *models.AutomationTask, runID, status string, reported []callbackToolExecution, artifacts []callbackArtifact, completedAt time.Time) ([]models.AutomationToolExecution, error) {
	if len(reported) == 0 {
		return nil, nil
	}
	if task == nil || strings.TrimSpace(status) != "completed" || task.Operation != "delivery.qa" || task.DeliveryWorkItemID == nil || len(reported) > 6 {
		return nil, fmt.Errorf("only bounded completed delivery QA tool calls are allowed")
	}
	artifactReferences := make(map[string]callbackArtifact, len(artifacts))
	for _, artifact := range artifacts {
		artifactReferences[strings.TrimSpace(artifact.Reference)] = artifact
	}
	rows := make([]models.AutomationToolExecution, 0, len(reported))
	seenCallKeys := make(map[string]struct{}, len(reported))
	for _, reportedExecution := range reported {
		tool := strings.ToLower(strings.TrimSpace(reportedExecution.Tool))
		stepKey := strings.TrimSpace(reportedExecution.StepKey)
		if tool != "stagehand" || stepKey != "qa.semantic_browser" {
			return nil, fmt.Errorf("unapproved automation tool execution")
		}
		callKey := strings.ToLower(strings.TrimSpace(reportedExecution.CallKey))
		if callKey == "" {
			callKey = "semantic-assessment"
		}
		if !toolCallKeyPattern.MatchString(callKey) {
			return nil, fmt.Errorf("tool call key is invalid")
		}
		if _, duplicate := seenCallKeys[callKey]; duplicate {
			return nil, fmt.Errorf("tool call key is duplicated")
		}
		seenCallKeys[callKey] = struct{}{}
		callStatus := strings.ToLower(strings.TrimSpace(reportedExecution.CallStatus))
		if callStatus == "" {
			callStatus = "completed"
		}
		if callStatus != "completed" && callStatus != "failed" {
			return nil, fmt.Errorf("tool call status is invalid")
		}
		provider := strings.ToLower(strings.TrimSpace(reportedExecution.Provider))
		model := strings.TrimSpace(reportedExecution.Model)
		if !providerAllowed(provider) || model == "" || len(reportedExecution.Usage) == 0 || !json.Valid(reportedExecution.Usage) {
			return nil, fmt.Errorf("tool usage requires an approved provider, model and JSON usage")
		}
		requestRef, responseRef := strings.TrimSpace(reportedExecution.RequestRef), strings.TrimSpace(reportedExecution.ResponseRef)
		artifact, exists := artifactReferences[responseRef]
		if !exists || requestRef != responseRef || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(artifact.Name)), "semantic-qa.json") || !strings.EqualFold(strings.TrimSpace(artifact.ContentType), "application/json") {
			return nil, fmt.Errorf("tool request and response must use the uploaded Stagehand report")
		}
		var usage map[string]any
		if err := json.Unmarshal(reportedExecution.Usage, &usage); err != nil || usage == nil {
			return nil, fmt.Errorf("tool usage is invalid")
		}
		ledger, err := automationcost.Build(provider, model, usage, pricingCatalog(cfg))
		if err != nil {
			return nil, fmt.Errorf("tool usage could not be costed: %w", err)
		}
		rows = append(rows, models.AutomationToolExecution{
			AutomationTaskID: task.ID, DeliveryWorkItemID: task.DeliveryWorkItemID, RunID: runID, Tool: tool, CallKey: callKey, CallStatus: callStatus, StepKey: stepKey,
			Provider: provider, Model: model, InputTokens: ledger.InputTokens, OutputTokens: ledger.OutputTokens, CachedInputTokens: ledger.CachedInputTokens,
			CacheWriteTokens: ledger.CacheWriteTokens, ReasoningTokens: ledger.ReasoningTokens, TotalTokens: ledger.TotalTokens,
			InputCostMicros: ledger.InputCostMicros, OutputCostMicros: ledger.OutputCostMicros, CachedCostMicros: ledger.CachedCostMicros,
			CacheWriteCostMicros: ledger.CacheWriteCostMicros, TotalCostMicros: ledger.TotalCostMicros, Currency: "USD", PricingBasis: ledger.PricingBasis,
			PricingSnapshotJSON: ledger.PricingSnapshot, UsageJSON: string(reportedExecution.Usage), RequestRef: requestRef, ResponseRef: responseRef, CompletedAt: completedAt,
		})
	}
	return rows, nil
}

// claimAutomationTaskRun gives at most one worker a renewable, opaque lease.
// A redelivered SQS message is therefore harmless while the original worker is
// active; a genuinely abandoned run becomes recoverable after the lease.
func claimAutomationTaskRun(c echo.Context, id uuid.UUID, runID string) error {
	now := time.Now().UTC()
	expiresAt := now.Add(automationRunLeaseDuration)
	reservationExpiresAt := now.Add(2 * automationRunLeaseDuration)
	claimed := false
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		// The budget hold intentionally outlives the execution lease. A worker
		// may finish uploading a private result shortly after its 20-minute
		// lease, while the queue needs time to redeliver a genuinely abandoned
		// run. Keeping the admission hold for 40 minutes prevents that narrow
		// recovery window from becoming unreserved spend.
		updates := map[string]any{"status": "running", "run_id": runID, "lease_expires_at": expiresAt, "budget_reservation_expires_at": reservationExpiresAt, "attempt_count": gorm.Expr("attempt_count + ?", 1)}
		result := tx.Model(&models.AutomationTask{}).Where("id = ? AND status = ?", id, "queued").Updates(updates)
		if result.Error != nil || result.RowsAffected > 0 {
			claimed = result.RowsAffected > 0
			return result.Error
		}
		var current models.AutomationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
			return err
		}
		if current.Status != "running" {
			return nil
		}
		if current.RunID == runID {
			result := tx.Model(&models.AutomationTask{}).Where("id = ? AND status = ? AND run_id = ?", id, "running", runID).Updates(map[string]any{"lease_expires_at": expiresAt, "budget_reservation_expires_at": reservationExpiresAt})
			claimed = result.RowsAffected > 0
			return result.Error
		}
		if current.LeaseExpiresAt != nil && current.LeaseExpiresAt.After(now) {
			return nil
		}
		result = tx.Model(&models.AutomationTask{}).
			Where("id = ? AND status = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)", id, "running", now).
			Updates(updates)
		claimed = result.RowsAffected > 0
		return result.Error
	}); err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation result failed", "")
	}
	if !claimed {
		return utils.Error(c, http.StatusConflict, "Automation run is already leased", "Another active worker owns this execution; no provider call will be duplicated")
	}
	return c.NoContent(http.StatusNoContent)
}

type implementationExecutionHandoff struct {
	Workspace        string `json:"workspace"`
	Worktree         string `json:"worktree"`
	Branch           string `json:"branch"`
	BaseSHA          string `json:"base_sha"`
	GitHubRepository string `json:"github_repository"`
	ReviewDiffSHA256 string `json:"review_diff_sha256"`
	DiffCheckPassed  bool   `json:"diff_check_passed"`
	Validations      []struct {
		Passed bool `json:"passed"`
	} `json:"validations"`
	ChangeSets []implementationExecutionHandoff `json:"change_sets"`
}

// persistImplementationChangeSet turns the authenticated, bounded worker
// handoff into a review record. The record is intentionally local_worktree:
// no PR, commit, push or CI run is fabricated by this callback. A reviewer
// sees which validations passed and still controls the next gate.
func persistImplementationChangeSet(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, createdAt time.Time) error {
	changes, err := implementationChangeSetsForHandoff(task, raw, createdAt)
	if err != nil {
		return err
	}
	for _, change := range changes {
		var existing models.DeliveryChangeSet
		err = tx.Where("work_item_id = ? AND repository_ref = ? AND branch = ?", change.WorkItemID, change.RepositoryRef, change.Branch).First(&existing).Error
		if err == nil {
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if err := tx.Create(&change).Error; err != nil {
			return err
		}
	}
	return nil
}

func implementationChangeSetForHandoff(task *models.AutomationTask, raw json.RawMessage, createdAt time.Time) (models.DeliveryChangeSet, error) {
	if task == nil || task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
		return models.DeliveryChangeSet{}, fmt.Errorf("only a delivery implementation may create a change set")
	}
	var handoff implementationExecutionHandoff
	if err := json.Unmarshal(raw, &handoff); err != nil {
		return models.DeliveryChangeSet{}, fmt.Errorf("implementation execution metadata is invalid")
	}
	if len(handoff.ChangeSets) != 0 {
		return models.DeliveryChangeSet{}, fmt.Errorf("implementation execution contains multiple change sets")
	}
	return implementationChangeSetFromHandoff(task, handoff, createdAt)
}

// implementationChangeSetsForHandoff accepts one legacy change set or the
// explicit per-repository array emitted by the multi-repository worker. The
// entire callback is rejected if any declared repository is malformed, so an
// agent cannot create a partial review gate by omitting one result.
func implementationChangeSetsForHandoff(task *models.AutomationTask, raw json.RawMessage, createdAt time.Time) ([]models.DeliveryChangeSet, error) {
	if task == nil || task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
		return nil, fmt.Errorf("only a delivery implementation may create a change set")
	}
	var handoff implementationExecutionHandoff
	if err := json.Unmarshal(raw, &handoff); err != nil {
		return nil, fmt.Errorf("implementation execution metadata is invalid")
	}
	if len(handoff.ChangeSets) == 0 {
		change, err := implementationChangeSetFromHandoff(task, handoff, createdAt)
		if err != nil {
			return nil, err
		}
		return []models.DeliveryChangeSet{change}, nil
	}
	if strings.TrimSpace(handoff.Workspace) != "" || strings.TrimSpace(handoff.Worktree) != "" || strings.TrimSpace(handoff.Branch) != "" {
		return nil, fmt.Errorf("multi-repository implementation execution must not mix aggregate and individual change sets")
	}
	changes := make([]models.DeliveryChangeSet, 0, len(handoff.ChangeSets))
	seen := make(map[string]struct{}, len(handoff.ChangeSets))
	for _, entry := range handoff.ChangeSets {
		if len(entry.ChangeSets) != 0 {
			return nil, fmt.Errorf("implementation execution change sets must not be nested")
		}
		change, err := implementationChangeSetFromHandoff(task, entry, createdAt)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[change.RepositoryRef]; duplicate {
			return nil, fmt.Errorf("implementation execution repeats a repository change set")
		}
		seen[change.RepositoryRef] = struct{}{}
		changes = append(changes, change)
	}
	return changes, nil
}

func implementationChangeSetFromHandoff(task *models.AutomationTask, handoff implementationExecutionHandoff, createdAt time.Time) (models.DeliveryChangeSet, error) {
	handoff.Workspace = strings.TrimSpace(handoff.Workspace)
	handoff.Worktree = strings.TrimSpace(handoff.Worktree)
	handoff.Branch = strings.TrimSpace(handoff.Branch)
	handoff.BaseSHA = strings.ToLower(strings.TrimSpace(handoff.BaseSHA))
	handoff.GitHubRepository = strings.ToLower(strings.TrimSpace(handoff.GitHubRepository))
	handoff.ReviewDiffSHA256 = strings.ToLower(strings.TrimSpace(handoff.ReviewDiffSHA256))
	if !strings.HasPrefix(handoff.Workspace, "workspace://") || !strings.HasPrefix(handoff.Worktree, handoff.Workspace+"#") || !agentBranchPattern.MatchString(handoff.Branch) || !gitCommitSHA.MatchString(handoff.BaseSHA) || !artifactDigestPattern.MatchString(handoff.ReviewDiffSHA256) {
		return models.DeliveryChangeSet{}, fmt.Errorf("implementation execution workspace or branch is invalid")
	}
	if handoff.Worktree != handoff.Workspace+"#"+handoff.Branch {
		return models.DeliveryChangeSet{}, fmt.Errorf("implementation execution worktree does not match its branch")
	}
	passedValidations := 0
	for _, validation := range handoff.Validations {
		if validation.Passed {
			passedValidations++
		}
	}
	// "passed" here means the bounded local verification completed, not that
	// an external CI provider passed. The review type preserves that distinction
	// in the UI and downstream policy.
	status := "pending"
	if handoff.DiffCheckPassed && len(handoff.Validations) > 0 && passedValidations == len(handoff.Validations) {
		status = "passed"
	}
	metadata, err := json.Marshal(map[string]any{
		"automation_task_id":      task.ID.String(),
		"worktree":                handoff.Worktree,
		"base_sha":                handoff.BaseSHA,
		"github_repository":       handoff.GitHubRepository,
		"review_diff_sha256":      handoff.ReviewDiffSHA256,
		"diff_check_passed":       handoff.DiffCheckPassed,
		"validation_count":        len(handoff.Validations),
		"validation_passed_count": passedValidations,
		"verification_source":     "itbem-local-agent",
	})
	if err != nil {
		return models.DeliveryChangeSet{}, err
	}
	return models.DeliveryChangeSet{
		WorkItemID: *task.DeliveryWorkItemID, RepositoryRef: handoff.Workspace, Branch: handoff.Branch,
		ReviewType: "local_worktree", CIStatus: status, Environment: "local", MetadataJSON: string(metadata),
		CreatedBy: "itbem-local-agent", CreatedAt: createdAt,
	}, nil
}

type publicationExecutionHandoff struct {
	GrantID            string `json:"grant_id"`
	Workspace          string `json:"workspace"`
	Worktree           string `json:"worktree"`
	RepositoryRef      string `json:"repository_ref"`
	Branch             string `json:"branch"`
	TargetBranch       string `json:"target_branch"`
	BaseSHA            string `json:"base_sha"`
	CommitSHA          string `json:"commit_sha"`
	RemoteRepository   string `json:"remote_repository"`
	BranchPublished    bool   `json:"branch_published"`
	PullRequestURL     string `json:"pull_request_url"`
	PullRequestCreated bool   `json:"pull_request_created"`
}

// persistPublicationChangeSet revalidates the grant in the control plane
// before representing a remote branch or PR. The worker is authenticated but
// does not become the authority for grant scope or expiry.
func persistPublicationChangeSet(tx *gorm.DB, task *models.AutomationTask, raw json.RawMessage, createdAt time.Time) error {
	if task == nil || task.Operation != "delivery.publish" || task.DeliveryWorkItemID == nil {
		return fmt.Errorf("only a delivery publication may register a remote change set")
	}
	var handoff publicationExecutionHandoff
	if err := json.Unmarshal(raw, &handoff); err != nil {
		return fmt.Errorf("publication execution metadata is invalid")
	}
	grantID, err := uuid.FromString(strings.TrimSpace(handoff.GrantID))
	if err != nil || grantID == uuid.Nil || !handoff.BranchPublished {
		return fmt.Errorf("publication execution grant or branch evidence is invalid")
	}
	handoff.Workspace, handoff.Worktree, handoff.RepositoryRef, handoff.Branch, handoff.TargetBranch = strings.TrimSpace(handoff.Workspace), strings.TrimSpace(handoff.Worktree), strings.TrimSpace(handoff.RepositoryRef), strings.TrimSpace(handoff.Branch), strings.TrimSpace(handoff.TargetBranch)
	handoff.BaseSHA, handoff.CommitSHA, handoff.RemoteRepository = strings.ToLower(strings.TrimSpace(handoff.BaseSHA)), strings.ToLower(strings.TrimSpace(handoff.CommitSHA)), strings.ToLower(strings.TrimSpace(handoff.RemoteRepository))
	if !strings.HasPrefix(handoff.Workspace, "workspace://") || handoff.RepositoryRef != handoff.Workspace || handoff.Worktree != handoff.Workspace+"#"+handoff.Branch || !agentBranchPattern.MatchString(handoff.Branch) || !validReleaseTargetBranch(handoff.TargetBranch) || !gitCommitSHA.MatchString(handoff.BaseSHA) || !gitCommitSHA.MatchString(handoff.CommitSHA) || !githubRepositoryPattern.MatchString(handoff.RemoteRepository) {
		return fmt.Errorf("publication execution workspace or revision is invalid")
	}
	var workItem models.DeliveryWorkItem
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "state").First(&workItem, *task.DeliveryWorkItemID).Error; err != nil {
		return err
	}
	if workItem.State != "preview_pending" {
		return fmt.Errorf("publication result arrived after the approved preview window closed")
	}
	var grant models.DeliveryPublicationGrant
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND work_item_id = ? AND revoked_at IS NULL AND expires_at > ?", grantID, *task.DeliveryWorkItemID, createdAt).First(&grant).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("publication grant is no longer active")
		}
		return err
	}
	if grant.RepositoryRef != handoff.RepositoryRef || grant.Branch != handoff.Branch || strings.ToLower(grant.BaseSHA) != handoff.BaseSHA || !strings.EqualFold(grant.GitHubRepository, handoff.RemoteRepository) {
		return fmt.Errorf("publication execution does not match its grant")
	}
	var capabilities []string
	if err := json.Unmarshal([]byte(grant.CapabilitiesJSON), &capabilities); err != nil || !containsCapability(capabilities, "branch:publish") || !containsCapability(capabilities, "commit:stage") {
		return fmt.Errorf("publication grant capabilities are invalid")
	}
	if handoff.PullRequestURL != "" && (!containsCapability(capabilities, "pull_request:create") || !validPublicationPRURL(handoff.PullRequestURL, grant.GitHubRepository)) {
		return fmt.Errorf("publication pull request is outside grant scope")
	}
	metadata, err := json.Marshal(map[string]any{
		"automation_task_id": task.ID.String(), "publication_grant_id": grant.ID.String(), "base_sha": handoff.BaseSHA,
		"remote_repository": strings.TrimSpace(handoff.RemoteRepository), "target_branch": handoff.TargetBranch,
		"branch_published": true, "pull_request_created": handoff.PullRequestCreated,
		"verification_source": "itbem-github-app",
	})
	if err != nil {
		return err
	}
	change := models.DeliveryChangeSet{WorkItemID: *task.DeliveryWorkItemID, RepositoryRef: handoff.RepositoryRef, Branch: handoff.Branch, CommitSHA: handoff.CommitSHA, ReviewType: "pull_request", PullRequestURL: strings.TrimSpace(handoff.PullRequestURL), CIStatus: "pending", Environment: "preview", MetadataJSON: string(metadata), CreatedBy: "itbem-github-app", CreatedAt: createdAt}
	var existing models.DeliveryChangeSet
	err = tx.Where("work_item_id = ? AND repository_ref = ? AND branch = ? AND review_type = ?", change.WorkItemID, change.RepositoryRef, change.Branch, change.ReviewType).First(&existing).Error
	if err == nil {
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	if err := tx.Create(&change).Error; err != nil {
		return err
	}
	// A publication grant is deliberately one-shot. Once the control plane
	// accepts immutable branch evidence, the same authorization cannot be used
	// to create another commit or PR by replaying a completed run.
	return tx.Model(&grant).Updates(map[string]any{
		"revoked_by":        "itbem-github-app",
		"revoked_at":        createdAt.UTC(),
		"revocation_reason": "Consumed after the approved branch publication was recorded.",
	}).Error
}

func containsCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if strings.TrimSpace(capability) == expected {
			return true
		}
	}
	return false
}

// validPublicationPRURL keeps the control-plane record bound to the exact
// GitHub repository approved in the one-shot publication grant. The worker
// already validates GitHub App responses, but this second boundary prevents a
// forged or confused handoff from attaching an unrelated repository's PR as
// apparently valid delivery evidence.
func validPublicationPRURL(value, repository string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" || !githubRepositoryPattern.MatchString(strings.ToLower(parts[0]+"/"+parts[1])) {
		return false
	}
	for _, character := range parts[3] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return parts[3] != "" && parts[3] != "0" && strings.EqualFold(parts[0]+"/"+parts[1], strings.TrimSpace(repository))
}

func validReleaseTargetBranch(value string) bool {
	_, err := releasegate.RevisionMatrixDigest([]releasegate.Revision{{
		Repository: "validation/repository",
		Branch:     strings.TrimSpace(value),
		SHA:        strings.Repeat("0", 40),
	}})
	return err == nil && value == strings.TrimSpace(value)
}

func pricingCatalog(cfg *models.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.AutomationPricingJSON
}

func executionStepKey(operation string) string {
	if strings.HasPrefix(operation, "delivery.") {
		return strings.TrimPrefix(operation, "delivery.")
	}
	return operation
}

func validateCallbackArtifacts(cfg *models.Config, task *models.AutomationTask, taskID uuid.UUID, artifacts []callbackArtifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	if task == nil || task.Operation != "delivery.qa" || task.DeliveryWorkItemID == nil {
		return fmt.Errorf("only a delivery QA run may register artifacts")
	}
	if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return fmt.Errorf("private output storage is not configured")
	}
	if len(artifacts) > 12 {
		return fmt.Errorf("too many QA artifacts")
	}
	seen := map[string]struct{}{}
	for _, artifact := range artifacts {
		name := strings.TrimSpace(artifact.Name)
		if !artifactNamePattern.MatchString(name) || artifact.SizeBytes < 1 || artifact.SizeBytes > 25<<20 || strings.TrimSpace(artifact.ContentType) == "" || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(artifact.SHA256))) {
			return fmt.Errorf("QA artifact metadata is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("QA artifact names must be unique")
		}
		seen[name] = struct{}{}
		expected := "s3://" + strings.TrimSpace(cfg.AutomationOutputBucket) + "/automation/" + taskID.String() + "/artifacts/" + name
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(artifact.Reference)), []byte(expected)) != 1 {
			return fmt.Errorf("QA artifact reference is outside the task private prefix")
		}
	}
	return nil
}

func persistDeliveryQAEvidence(tx *gorm.DB, task *models.AutomationTask, artifacts []callbackArtifact, capturedAt time.Time) error {
	if task == nil || task.DeliveryWorkItemID == nil {
		return nil
	}
	for _, artifact := range artifacts {
		var existing models.DeliveryEvidence
		err := tx.Where("work_item_id = ? AND reference = ?", *task.DeliveryWorkItemID, artifact.Reference).First(&existing).Error
		if err == nil {
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		kind := "artifact"
		if strings.HasPrefix(strings.ToLower(artifact.ContentType), "image/") {
			kind = "screenshot"
		} else if strings.HasPrefix(strings.ToLower(artifact.ContentType), "video/") {
			kind = "video"
		}
		metadata, _ := json.Marshal(map[string]any{
			"automation_task_id": task.ID.String(),
			"content_type":       artifact.ContentType,
			"size_bytes":         artifact.SizeBytes,
			"artifact_name":      artifact.Name,
			"sha256":             strings.ToLower(strings.TrimSpace(artifact.SHA256)),
		})
		if comparisonKey, comparisonRole := deliveryQAEvidenceComparison(artifact.Name); comparisonKey != "" {
			metadata, _ = json.Marshal(map[string]any{
				"automation_task_id": task.ID.String(),
				"content_type":       artifact.ContentType,
				"size_bytes":         artifact.SizeBytes,
				"artifact_name":      artifact.Name,
				"sha256":             strings.ToLower(strings.TrimSpace(artifact.SHA256)),
				"qa_comparison_key":  comparisonKey,
				"qa_comparison_role": comparisonRole,
			})
		}
		evidence := models.DeliveryEvidence{
			WorkItemID: *task.DeliveryWorkItemID, Kind: kind, Phase: "qa",
			Title:     deliveryQAEvidenceTitle(artifact.Name, kind),
			Reference: artifact.Reference, MetadataJSON: string(metadata),
			CapturedBy: "itbem-local-agent", CapturedAt: &capturedAt,
		}
		if err := tx.Create(&evidence).Error; err != nil {
			return err
		}
	}
	return nil
}

// deliveryQAEvidenceTitle turns the bounded harness artifact names into a
// meaningful gallery label. The source object key remains immutable in the
// metadata; this is strictly a human-facing presentation improvement.
func deliveryQAEvidenceTitle(name, kind string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if kind == "screenshot" {
		if comparisonKey, comparisonRole := deliveryQAEvidenceComparison(name); comparisonKey != "" {
			role := "Antes"
			if comparisonRole == "after" {
				role = "Después"
			}
			return "QA visual · Caso " + strings.TrimPrefix(comparisonKey, "case-") + " · " + role
		}
		switch {
		case strings.Contains(lower, "preview-desktop"):
			return "QA visual · Escritorio"
		case strings.Contains(lower, "preview-mobile"):
			return "QA visual · Móvil"
		case strings.Contains(lower, "preview"):
			return "QA visual · Preview"
		}
	}
	return "Evidencia QA: " + strings.TrimSpace(name)
}

// deliveryQAEvidenceComparison recognizes only the runner's bounded artifact
// convention. A comparison key is display metadata, never a client-supplied
// object lookup; the immutable evidence reference and SHA-256 remain the
// authorization and integrity boundaries.
func deliveryQAEvidenceComparison(name string) (key, role string) {
	lower := strings.ToLower(strings.TrimSpace(name))
	const marker = "semantic-qa-case-"
	index := strings.Index(lower, marker)
	if index < 0 {
		return "", ""
	}
	value := lower[index+len(marker):]
	for suffix, candidateRole := range map[string]string{"-before.png": "before", "-after.png": "after"} {
		if !strings.HasSuffix(value, suffix) {
			continue
		}
		caseID := strings.TrimSuffix(value, suffix)
		if caseID == "" || len(caseID) > 3 {
			return "", ""
		}
		for _, character := range caseID {
			if character < '0' || character > '9' {
				return "", ""
			}
		}
		return "case-" + caseID, candidateRole
	}
	return "", ""
}

func providerAllowed(provider string) bool {
	_, allowed := allowedProviders[strings.ToLower(strings.TrimSpace(provider))]
	return allowed
}

func mayAccessTask(c echo.Context, task *models.AutomationTask, requestedBy string) bool {
	if task != nil && strings.TrimSpace(requestedBy) != "" && task.RequestedBy == requestedBy {
		return true
	}
	if mayAccessDeliveryTask(c, task) {
		return true
	}
	_, err := authz.RequireRoot(c)
	return err == nil
}

// mayCancelTask is narrower than read access. A requester can stop their
// generic job; a Delivery job requires a project owner/manager (or platform
// administrator) because cancellation can alter another reviewer’s workflow.
func mayCancelTask(c echo.Context, task *models.AutomationTask, requestedBy string) bool {
	if task == nil || strings.TrimSpace(requestedBy) == "" {
		return false
	}
	if task.DeliveryWorkItemID == nil {
		if task.RequestedBy == requestedBy {
			return true
		}
		_, err := authz.RequireRoot(c)
		return err == nil
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return false
	}
	if user.IsPlatformAdmin() {
		return true
	}
	var item models.DeliveryWorkItem
	if configuration.DB == nil || configuration.DB.Select("project_id").First(&item, *task.DeliveryWorkItemID).Error != nil {
		return false
	}
	var member models.DeliveryProjectMember
	if configuration.DB.Where("project_id = ? AND cognito_sub = ?", item.ProjectID, user.CognitoSub).First(&member).Error != nil {
		return false
	}
	return deliveryTaskMemberCanManage(member)
}

func retryableCodeReviewTask(task *models.AutomationTask) bool {
	return task != nil && task.Operation == "code.review" && task.Status == "failed" && strings.TrimSpace(task.InputRef) != ""
}

// mayRetryAutomationTask is intentionally no broader than cancellation. A
// retry can cause a second billable provider request, so a GitHub-originated
// review is platform-admin only unless it belongs to a Delivery project whose
// manager is already authorized to control its tasks.
func mayRetryAutomationTask(c echo.Context, task *models.AutomationTask, requestedBy string) bool {
	return mayCancelTask(c, task, requestedBy)
}

func deliveryTaskMemberCanManage(member models.DeliveryProjectMember) bool {
	role := strings.ToLower(strings.TrimSpace(member.Role))
	if role == "owner" || role == "delivery_manager" {
		return true
	}
	var permissions []string
	if json.Unmarshal([]byte(member.Permissions), &permissions) != nil {
		return false
	}
	for _, permission := range permissions {
		value := strings.ToLower(strings.TrimSpace(permission))
		if value == "manage" || value == "delivery:manage" {
			return true
		}
	}
	return false
}

// mayAccessDeliveryTask gives project members enough visibility to perform
// their human gate. Without this, the person responsible for plan/code/QA
// review could see the task but not the exact bounded prompt, response or QA
// artifact when a different teammate originally queued the agent run.
// Generic automation remains requester-or-platform-admin only.
func mayAccessDeliveryTask(c echo.Context, task *models.AutomationTask) bool {
	if configuration.DB == nil || task == nil || task.DeliveryWorkItemID == nil {
		return false
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return false
	}
	if user.IsPlatformAdmin() {
		return true
	}
	var item models.DeliveryWorkItem
	if err := configuration.DB.Select("project_id").First(&item, *task.DeliveryWorkItemID).Error; err != nil {
		return false
	}
	var member models.DeliveryProjectMember
	if err := configuration.DB.Where("project_id = ? AND cognito_sub = ?", item.ProjectID, user.CognitoSub).First(&member).Error; err != nil {
		return false
	}
	return deliveryTaskMemberCanView(member)
}

func deliveryTaskMemberCanView(member models.DeliveryProjectMember) bool {
	role := strings.ToLower(strings.TrimSpace(member.Role))
	if role == "owner" || role == "delivery_manager" || role == "reviewer" || role == "qa_reviewer" || role == "requester" || role == "viewer" {
		return true
	}
	var permissions []string
	if json.Unmarshal([]byte(member.Permissions), &permissions) != nil {
		return false
	}
	for _, permission := range permissions {
		value := strings.ToLower(strings.TrimSpace(permission))
		if value == "view" || value == "delivery:view" {
			return true
		}
	}
	return false
}

func deliveryReadableProjectIDs(cognitoSub string) ([]uuid.UUID, error) {
	if configuration.DB == nil || strings.TrimSpace(cognitoSub) == "" {
		return nil, nil
	}
	var memberships []models.DeliveryProjectMember
	if err := configuration.DB.Where("cognito_sub = ?", cognitoSub).Find(&memberships).Error; err != nil {
		return nil, err
	}
	projectIDs := make([]uuid.UUID, 0, len(memberships))
	for _, member := range memberships {
		if deliveryTaskMemberCanView(member) {
			projectIDs = append(projectIDs, member.ProjectID)
		}
	}
	return projectIDs, nil
}

func inputReferenceMatches(cfg *models.Config, reference string) bool {
	if cfg == nil || strings.TrimSpace(cfg.AutomationInputBucket) == "" {
		return false
	}
	prefix := "s3://" + strings.TrimSpace(cfg.AutomationInputBucket) + "/automation/inputs/"
	reference = strings.TrimSpace(reference)
	return strings.HasPrefix(reference, prefix) && strings.HasSuffix(reference, "/input.json")
}

func privateReference(reference string) (string, string, error) {
	value := strings.TrimPrefix(strings.TrimSpace(reference), "s3://")
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid private reference")
	}
	return parts[0], parts[1], nil
}

func outputReferenceMatches(cfg *models.Config, taskID uuid.UUID, reference string) bool {
	if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return false
	}
	bucket, key, err := privateReference(reference)
	if err != nil || subtle.ConstantTimeCompare([]byte(bucket), []byte(strings.TrimSpace(cfg.AutomationOutputBucket))) != 1 {
		return false
	}
	legacy := "automation/" + taskID.String() + "/result.json"
	if subtle.ConstantTimeCompare([]byte(key), []byte(legacy)) == 1 {
		return true
	}
	prefix := "automation/" + taskID.String() + "/runs/"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, "/result.json") {
		return false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "/result.json")
	if strings.Contains(runID, "/") {
		return false
	}
	parsed, parseErr := uuid.FromString(runID)
	return parseErr == nil && parsed != uuid.Nil
}

// toolReportReferenceMatches keeps tool request/response inspection inside the
// same task artifact namespace that was validated at callback time. It does
// not accept a generic JSON object from the output bucket: only the bounded
// Stagehand report name can be dereferenced by the tool inspector.
func toolReportReferenceMatches(cfg *models.Config, taskID uuid.UUID, reference string) bool {
	if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" || taskID == uuid.Nil {
		return false
	}
	bucket, key, err := privateReference(reference)
	if err != nil || subtle.ConstantTimeCompare([]byte(bucket), []byte(strings.TrimSpace(cfg.AutomationOutputBucket))) != 1 {
		return false
	}
	prefix := "automation/" + taskID.String() + "/artifacts/"
	name := strings.TrimPrefix(key, prefix)
	if name == key || strings.Contains(name, "/") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(name), "semantic-qa.json")
}

// executionRequestReferenceMatches accepts only the encrypted canonical
// request generated for this task's exact worker lease. It prevents a callback
// from attaching another task's prompt or another execution's context to a
// ledger row.
func executionRequestReferenceMatches(cfg *models.Config, taskID uuid.UUID, runID, reference string) bool {
	if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return false
	}
	parsedRunID, err := uuid.FromString(strings.TrimSpace(runID))
	if err != nil || parsedRunID == uuid.Nil {
		return false
	}
	bucket, key, err := privateReference(reference)
	if err != nil || subtle.ConstantTimeCompare([]byte(bucket), []byte(strings.TrimSpace(cfg.AutomationOutputBucket))) != 1 {
		return false
	}
	expected := "automation/" + taskID.String() + "/runs/" + parsedRunID.String() + "/request.json"
	return subtle.ConstantTimeCompare([]byte(key), []byte(expected)) == 1
}

// executionResultReferenceMatches binds a recovered callback to the exact
// immutable response written by its original provider run. It deliberately
// rejects the compatibility result pointer so a new lease cannot relabel a
// different response as recovered evidence.
func executionResultReferenceMatches(cfg *models.Config, taskID uuid.UUID, runID, reference string) bool {
	if cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return false
	}
	parsedRunID, err := uuid.FromString(strings.TrimSpace(runID))
	if err != nil || parsedRunID == uuid.Nil {
		return false
	}
	bucket, key, err := privateReference(reference)
	if err != nil || subtle.ConstantTimeCompare([]byte(bucket), []byte(strings.TrimSpace(cfg.AutomationOutputBucket))) != 1 {
		return false
	}
	expected := "automation/" + taskID.String() + "/runs/" + parsedRunID.String() + "/result.json"
	return subtle.ConstantTimeCompare([]byte(key), []byte(expected)) == 1
}

func validCallbackSecret(provided string) bool {
	if provided == "" {
		return false
	}
	valid := 0
	for _, name := range []string{"AUTOMATION_CALLBACK_SECRET", "AUTOMATION_CALLBACK_SECRET_PREVIOUS"} {
		if expected := os.Getenv(name); expected != "" {
			valid |= subtle.ConstantTimeCompare([]byte(provided), []byte(expected))
		}
	}
	return valid == 1
}
