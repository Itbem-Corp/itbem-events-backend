package automation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/agentprotocol"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/inferencecapability"
	"events-stocks/internal/organizationscope"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	awsrepository "events-stocks/repositories/awsrepository"
	"events-stocks/services/automationcost"
	"events-stocks/services/deliveryplansteps"
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
var agentProfileKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
var executionReportURLPattern = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)
var executionReportProviderKeyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\bya29\.[0-9A-Za-z_-]{20,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
}

const maxGitHubReviewWebhookBytes = 1 << 20

// A systemd restart deliberately assigns a new worker ID. Keep enough recent
// heartbeat rows for diagnostics, but project only the newest heartbeat for a
// configured role/lane into operational health. Otherwise a healthy restart
// looks like duplicate capacity and can combine old, failing workspace
// readiness with the current successful preflight.
const maxAutomationHealthWorkerRows = 64
const workspaceAttestationTTL = 2 * time.Minute

var allowedOperations = map[string]struct{}{
	"ai.chat":                   {},
	"document.analyze":          {},
	"code.review":               {},
	"product.ideate":            {},
	"delivery.chat":             {},
	"delivery.plan":             {},
	"delivery.implementation":   {},
	"delivery.assessment":       {},
	"delivery.onboarding_probe": {},
	"delivery.publish":          {},
	"delivery.release_gate":     {},
	"delivery.qa":               {},
	"delivery.summary":          {},
	"delivery.workflow":         {},
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

// automationTaskListView is the only task shape exposed by the broad polling
// endpoint. Object keys, prompts, outputs, errors, requesters, leases,
// provider response IDs and evidence digests remain behind task-scoped reads.
type automationTaskListView struct {
	ID                 uuid.UUID  `json:"id"`
	DeliveryWorkItemID *uuid.UUID `json:"delivery_work_item_id,omitempty"`
	Operation          string     `json:"operation"`
	Status             string     `json:"status"`
	Provider           string     `json:"provider,omitempty"`
	Model              string     `json:"model,omitempty"`
	AttemptCount       int        `json:"attempt_count"`
	ResultAvailable    bool       `json:"result_available"`
	HasError           bool       `json:"has_error"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
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
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxGitHubReviewWebhookBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxGitHubReviewWebhookBytes || !validGitHubWebhookSignature(body, c.Request().Header.Get("X-Hub-Signature-256"), cfg.GitHubReviewWebhookSecret) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	eventName := strings.TrimSpace(c.Request().Header.Get("X-GitHub-Event"))
	if githubReviewWebhookPing(eventName, body) {
		return utils.Success(c, http.StatusOK, "GitHub webhook ready", map[string]string{"status": "ready"})
	}
	if !strings.EqualFold(eventName, "pull_request") {
		// The App can be subscribed to events which do not create a review
		// task (for example check_suite). This delivery is authenticated but
		// intentionally irrelevant, so acknowledge it without decoding,
		// queuing or persisting anything. Returning a client error would only
		// cause GitHub to retry a task the reviewer must never execute.
		return utils.Success(c, http.StatusAccepted, "GitHub event ignored", map[string]string{"status": "unsupported_event"})
	}
	var event githubPullRequestWebhook
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid GitHub event", "")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return utils.Error(c, http.StatusBadRequest, "Invalid GitHub event", "")
	}
	if reason := githubReviewWebhookIgnoreReason(event, cfg); reason != "" {
		return utils.Success(c, http.StatusAccepted, "GitHub review ignored", map[string]string{"status": reason})
	}
	repository := strings.ToLower(strings.TrimSpace(event.Repository.FullName))
	// A deterministic task id makes GitHub redelivery idempotent before a
	// provider call or a second outbox record can exist.
	prIdentity := repository + ":" + strconv.Itoa(event.Number)
	identity := prIdentity + ":" + strings.ToLower(event.PullRequest.Head.SHA)
	taskID := uuid.NewV5(uuid.NamespaceURL, "itbem/github-review/"+identity)
	if taskID == uuid.Nil {
		return utils.Error(c, http.StatusInternalServerError, "GitHub review failed", "")
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
	var existing models.AutomationTask
	if err := configuration.DB.First(&existing, taskID).Error; err == nil {
		current, currentErr := latestGitHubReviewAttempt(&existing)
		if currentErr != nil {
			return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
		}
		recovered, recoveryErr := recoverStrandedGitHubReview(c.Request().Context(), &current, time.Now().UTC())
		if recoveryErr != nil {
			return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
		}
		if recovered != nil {
			return utils.Success(c, http.StatusAccepted, "GitHub pull request review recovery queued", githubReviewTaskProjection(*recovered))
		}
		return utils.Success(c, http.StatusAccepted, "GitHub review already queued", githubReviewTaskProjection(current))
	} else if err != gorm.ErrRecordNotFound {
		return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
	}
	patch, err := automationagent.ReadGitHubPullRequestPatch(c.Request().Context(), appConfig, installation.Token, repository, event.PullRequest.Base.SHA, event.PullRequest.Head.SHA)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "Could not freeze the pull request diff")
	}
	review, err := automationagent.NewCodeReviewInput("github://"+repository, event.PullRequest.Base.SHA, event.PullRequest.Head.SHA, patch)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "The pull request diff is not reviewable within the configured safety limits")
	}
	sourceContext, err := automationagent.ReadGitHubCodeReviewContext(c.Request().Context(), appConfig, installation.Token, review)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "Exact-revision surrounding source context could not be frozen")
	}
	review, err = automationagent.BindCodeReviewContext(review, sourceContext)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "Exact-revision surrounding source context is invalid")
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
	correlationID, err := githubReviewCorrelationID(repository, event.Number, event.PullRequest.Head.SHA)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub review rejected", "The remote review target is invalid")
	}
	task := &models.AutomationTask{ID: taskID, JobID: jobID, RequestedBy: "github-app-review", CorrelationID: correlationID, Operation: "code.review", EvidenceSubjectDigest: reviewSubject, MaxCompletionTokens: automationagent.CompletionTokensForOperation("code.review"), InputRef: "s3://" + cfg.AutomationInputBucket + "/" + inputKey, Status: "queued"}
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
		if err := supersedeQueuedGitHubReviews(tx, repository, event.Number, task.ID, time.Now().UTC()); err != nil {
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
			current, currentErr := latestGitHubReviewAttempt(&existing)
			if currentErr != nil {
				return utils.Error(c, http.StatusServiceUnavailable, "GitHub review unavailable", "")
			}
			return utils.Success(c, http.StatusAccepted, "GitHub review already queued", githubReviewTaskProjection(current))
		}
		return utils.Error(c, http.StatusConflict, "GitHub review already queued", "")
	}
	return utils.Success(c, http.StatusAccepted, "GitHub pull request review queued", githubReviewTaskProjection(*task))
}

func githubReviewWebhookPing(eventName string, body []byte) bool {
	return strings.EqualFold(strings.TrimSpace(eventName), "ping") && json.Valid(body)
}

func githubReviewTaskProjection(task models.AutomationTask) githubReviewTaskView {
	return githubReviewTaskView{ID: task.ID, Status: task.Status, AttemptCount: task.AttemptCount, CompletedAt: task.CompletedAt, CreatedAt: task.CreatedAt}
}

// latestGitHubReviewAttempt follows recovery and operator-retry tasks that
// preserve the original immutable input. The deterministic webhook task can
// become cancelled audit evidence after a lost queue handoff; treating that
// historical row as the current attempt would prevent a later signed GitHub
// redelivery from observing or recovering the replacement.
func latestGitHubReviewAttempt(original *models.AutomationTask) (models.AutomationTask, error) {
	if configuration.DB == nil || original == nil || original.ID == uuid.Nil || original.JobID == uuid.Nil || original.Operation != "code.review" || original.RequestedBy != "github-app-review" || strings.TrimSpace(original.CorrelationID) == "" || strings.TrimSpace(original.InputRef) == "" || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(original.EvidenceSubjectDigest))) {
		return models.AutomationTask{}, fmt.Errorf("GitHub review attempt boundary is invalid")
	}
	var current models.AutomationTask
	err := configuration.DB.
		Where("operation = ? AND requested_by = ? AND correlation_id = ? AND input_ref = ? AND evidence_subject_digest = ?", original.Operation, original.RequestedBy, original.CorrelationID, original.InputRef, original.EvidenceSubjectDigest).
		Order("created_at DESC, id DESC").
		First(&current).Error
	if err != nil {
		return models.AutomationTask{}, err
	}
	return current, nil
}

func supersedeQueuedGitHubReviews(tx *gorm.DB, repository string, pullRequest int, replacementID uuid.UUID, now time.Time) error {
	prefix, err := githubReviewCorrelationPrefix(repository, pullRequest)
	if tx == nil || err != nil || replacementID == uuid.Nil {
		return fmt.Errorf("GitHub review supersession is invalid")
	}
	// Keep one rolling-upgrade compatibility selector for review tasks queued
	// before correlation IDs became bounded. Only queued tasks for the exact
	// repository/PR are affected; running and terminal historical evidence is
	// deliberately retained.
	legacyPrefix := "github-pr:" + strings.ToLower(strings.TrimSpace(repository)) + ":" + strconv.Itoa(pullRequest)
	result := tx.Model(&models.AutomationTask{}).
		Where("operation = ? AND requested_by = ? AND status = ? AND (correlation_id LIKE ? OR correlation_id LIKE ?) AND id <> ?", "code.review", "github-app-review", "queued", prefix+":%", legacyPrefix+":%", replacementID).
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

// githubReviewCorrelationID keeps observability and supersession identifiers
// inside the shared 64-character persistence boundary even for the longest
// valid GitHub owner/repository names. The immutable task ID, input and
// evidence subject still bind the complete repository, PR and 40-character
// head SHA; this compact label is never release authority.
func githubReviewCorrelationID(repository string, pullRequest int, headSHA string) (string, error) {
	prefix, err := githubReviewCorrelationPrefix(repository, pullRequest)
	head := strings.ToLower(strings.TrimSpace(headSHA))
	if err != nil || !gitCommitSHA.MatchString(head) {
		return "", fmt.Errorf("GitHub review correlation is invalid")
	}
	return prefix + ":" + head[:20], nil
}

func githubReviewCorrelationPrefix(repository string, pullRequest int) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(repository))
	if !githubRepositoryPattern.MatchString(normalized) || pullRequest < 1 {
		return "", fmt.Errorf("GitHub review correlation is invalid")
	}
	subject := normalized + ":" + strconv.Itoa(pullRequest)
	digest := sha256.Sum256([]byte(subject))
	return "github-pr:" + fmt.Sprintf("%x", digest[:10]), nil
}

func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

func githubReviewWebhookConfigured(cfg *models.Config) bool {
	return strings.TrimSpace(cfg.GitHubReviewWebhookSecret) != "" && len(githubReviewRepositories(cfg.GitHubReviewRepositories)) > 0
}
func githubReviewActionAllowed(action string) bool {
	_, ok := map[string]struct{}{"opened": {}, "reopened": {}, "ready_for_review": {}, "synchronize": {}}[strings.ToLower(strings.TrimSpace(action))]
	return ok
}

// githubReviewWebhookIgnoreReason identifies signed pull-request deliveries
// that cannot create a review. They are terminal for the webhook, but they
// never create a task or alter an existing review. Keep the public status
// fixed; repository and pull-request details stay out of the response.
func githubReviewWebhookIgnoreReason(event githubPullRequestWebhook, cfg *models.Config) string {
	if cfg == nil {
		return "ineligible_pull_request"
	}
	repository := strings.ToLower(strings.TrimSpace(event.Repository.FullName))
	baseSHA := strings.ToLower(strings.TrimSpace(event.PullRequest.Base.SHA))
	headSHA := strings.ToLower(strings.TrimSpace(event.PullRequest.Head.SHA))
	if event.PullRequest.Draft || !githubReviewActionAllowed(event.Action) || !githubReviewRepositoryAllowed(cfg.GitHubReviewRepositories, repository) || event.Installation.ID < 1 || event.Number < 1 || !gitCommitSHA.MatchString(baseSHA) || !gitCommitSHA.MatchString(headSHA) || baseSHA == headSHA {
		return "ineligible_pull_request"
	}
	return ""
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
	"minimax":     {},
	"openai":      {},
	"deepseek":    {},
	"openrouter":  {},
	"anthropic":   {},
	"opencode-go": {},
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

// githubReviewRecoveryDelay is deliberately longer than the normal outbox
// dispatch and worker-poll intervals. A signed GitHub redelivery may recover
// only a task that was never claimed, so it cannot create a second provider
// call for a live reviewer execution.
const githubReviewRecoveryDelay = 15 * time.Minute

// githubReviewLeaseReconciliationBatchSize deliberately limits the amount of
// historical work a polling Reviewer can repair. A live webhook remains the
// normal ingress; this is only the narrowly-scoped escape hatch for a worker
// lease that was lost after GitHub successfully delivered that webhook.
const githubReviewLeaseReconciliationBatchSize = 1

// The webhook's patch and bounded source context are stored together in this
// immutable input. Keep the reconciliation read bounded as well: a corrupt
// object must never turn a reviewer poll into an unbounded S3 download.
const maxGitHubReviewRecoveryInputBytes = 2 << 20

// githubReviewLeaseRecoveryMaximumAttempts is deliberately small. A current
// signed GitHub redelivery may repair one abandoned reviewer execution, but a
// repeated worker outage must become visible to an operator rather than spend
// an unbounded number of provider calls against the same immutable SHA.
const githubReviewLeaseRecoveryMaximumAttempts = 2

// retryReservationHeader is deliberately an internal, response-only signal.
// It distinguishes a recoverable expired budget hold from ordinary callback
// conflicts such as a newer worker owning the task. The worker retains only
// the former for a fresh lease; it must never retry every 409 blindly.
const retryReservationHeader = "X-ITBEM-Automation-Retry-Reservation"

// retryLeaseHeader carries the remaining active execution lease to a worker
// that received the same at-least-once queue delivery. Without this explicit
// signal the worker would treat the 409 as terminal and acknowledge the only
// durable message before the original run becomes recoverable.
const retryLeaseHeader = "X-ITBEM-Automation-Retry-Lease"

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
	UnpricedExecutions   int64 `json:"unpriced_executions"`
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

const automationCostUnpricedPricingBases = "('', 'legacy', 'unpriced')"

func automationCostPricingBasisCountsAsUnpriced(basis string) bool {
	switch strings.ToLower(strings.TrimSpace(basis)) {
	case "", "legacy", "unpriced":
		return true
	default:
		return false
	}
}

func automationCostUnpricedPricingBasisPredicate(column string) string {
	return "LOWER(BTRIM(COALESCE(" + column + ", ''))) IN " + automationCostUnpricedPricingBases
}

func automationCostSummarySelect() string {
	return "COUNT(*) AS executions, COUNT(DISTINCT execution.automation_task_id) AS tasks, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros, COUNT(*) FILTER (WHERE " + automationCostUnpricedPricingBasisPredicate("execution.pricing_basis") + ") AS unpriced_executions"
}

func aggregateAutomationCostSummary(filteredQuery *gorm.DB) (automationCostSummary, error) {
	var summary automationCostSummary
	err := filteredQuery.Session(&gorm.Session{}).Select(automationCostSummarySelect()).Scan(&summary).Error
	return summary, err
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

type automationCostAgent struct {
	AgentKey        string `json:"agent_key"`
	Executions      int64  `json:"executions"`
	TotalTokens     int64  `json:"total_tokens"`
	TotalCostMicros int64  `json:"total_cost_microusd"`
}

type automationCostWorkItem struct {
	ProjectID       *uuid.UUID `json:"project_id,omitempty"`
	ProjectName     string     `json:"project_name"`
	WorkItemID      uuid.UUID  `json:"work_item_id"`
	WorkItemTitle   string     `json:"work_item_title"`
	Executions      int64      `json:"executions"`
	TotalTokens     int64      `json:"total_tokens"`
	TotalCostMicros int64      `json:"total_cost_microusd"`
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
	ProjectID            *uuid.UUID `json:"project_id,omitempty"`
	ProjectName          string     `json:"project_name,omitempty"`
	WorkItemTitle        string     `json:"work_item_title,omitempty"`
	AgentKey             string     `json:"agent_key,omitempty"`
	AgentInstanceID      *uuid.UUID `json:"agent_instance_id,omitempty"`
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
	Page       int       `json:"page"`
	PageSize   int       `json:"page_size"`
	Total      int64     `json:"total"`
	TotalPages int       `json:"total_pages"`
	Mode       string    `json:"mode"`
	SnapshotAt time.Time `json:"snapshot_at"`
	HasMore    bool      `json:"has_more"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// automationCostRecentExecutionSelect stays explicit rather than selecting a
// model wholesale: the portfolio view needs every billable component, but must
// never receive private object references or task payloads.
const automationCostRecentExecutionSelect = "execution.id, execution.automation_task_id, execution.delivery_work_item_id, work_item.project_id, project.name AS project_name, work_item.title AS work_item_title, execution.agent_key AS agent_key, execution.agent_instance_id AS agent_instance_id, task.operation, task.status AS task_status, execution.execution_kind, execution.tool, execution.call_key, execution.call_status, execution.step_key, execution.provider, execution.model, execution.input_tokens, execution.output_tokens, execution.cached_input_tokens, execution.cache_write_tokens, execution.reasoning_tokens, execution.total_tokens, execution.input_cost_micros, execution.output_cost_micros, execution.cached_cost_micros, execution.cache_write_cost_micros, execution.total_cost_micros, execution.pricing_basis, execution.completed_at"

// automationCostLedgerUnion gives the portfolio a single accounting view over
// the primary agent call and independently billable runtime tools. Keep the
// column list explicit: private request/result references never enter cost
// aggregation, pagination or the dashboard response.
const automationCostLedgerUnion = `SELECT id, automation_task_id, delivery_work_item_id, step_key, agent_key, agent_instance_id, provider, model, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, input_cost_micros, output_cost_micros, cached_cost_micros, cache_write_cost_micros, total_cost_micros, pricing_basis, completed_at, 'agent' AS execution_kind, '' AS tool, '' AS call_key, 'completed' AS call_status FROM automation_executions UNION ALL SELECT id, automation_task_id, delivery_work_item_id, step_key, agent_key, agent_instance_id, provider, model, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, input_cost_micros, output_cost_micros, cached_cost_micros, cache_write_cost_micros, total_cost_micros, pricing_basis, completed_at, 'tool' AS execution_kind, tool, call_key, call_status FROM automation_tool_executions`

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
	{Name: "agent_key", Fallback: "''::text"},
	{Name: "agent_instance_id", Fallback: "NULL::uuid"},
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
	// Older ledgers may not have a separate creation timestamp. In that case
	// completion time is the safest available immutable snapshot boundary.
	{Name: "created_at", Fallback: "completed_at"},
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
	Draining               bool                        `json:"draining"`
	Capabilities           []string                    `json:"capabilities,omitempty" gorm:"-"`
	CapabilitiesJSON       string                      `json:"-" gorm:"column:capabilities_json"`
	Protocols              []string                    `json:"protocols,omitempty" gorm:"-"`
	ProtocolsJSON          string                      `json:"-" gorm:"column:protocols_json"`
	StartedAt              time.Time                   `json:"started_at"`
	LastSeenAt             time.Time                   `json:"last_seen_at"`
	WorkspaceReadiness     []automationWorkspaceHealth `gorm:"-" json:"workspace_readiness,omitempty"`
	WorkspaceReadinessJSON string                      `gorm:"column:workspace_readiness" json:"-"`
}

type automationOperationReadiness struct {
	Operation      string `json:"operation"`
	WorkerCount    int    `json:"worker_count"`
	WorkerCapacity int    `json:"worker_capacity"`
	Ready          bool   `json:"ready"`
}

func operationReadiness(workers []automationWorkerHealth) []automationOperationReadiness {
	operations := make([]string, 0, len(allowedOperations))
	for operation := range allowedOperations {
		operations = append(operations, operation)
	}
	sort.Strings(operations)
	result := make([]automationOperationReadiness, 0, len(operations))
	for _, operation := range operations {
		readiness := automationOperationReadiness{Operation: operation}
		for _, worker := range workers {
			if worker.Draining {
				continue
			}
			if len(worker.Capabilities) != 0 && !containsCapability(worker.Capabilities, operation) {
				continue
			}
			readiness.WorkerCount++
			if worker.Concurrency > 0 {
				readiness.WorkerCapacity += worker.Concurrency
			}
		}
		readiness.Ready = readiness.WorkerCount > 0 && readiness.WorkerCapacity > 0
		result = append(result, readiness)
	}
	return result
}

func effectiveWorkerCapacity(workers []automationWorkerHealth) int64 {
	var total int64
	for _, worker := range workers {
		if !worker.Draining && worker.Concurrency > 0 {
			total += int64(worker.Concurrency)
		}
	}
	return total
}
func effectiveWorkerCount(active, draining int64) int64 {
	if active <= 0 || draining >= active {
		return 0
	}
	if draining < 0 {
		draining = 0
	}
	return active - draining
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
	Queued              int64                                 `json:"queued"`
	Running             int64                                 `json:"running"`
	ActiveTasks         int64                                 `json:"active_tasks"`
	FailedLastDay       int64                                 `json:"failed_last_day"`
	ExpiredLeases       int64                                 `json:"expired_leases"`
	SpendLastDay        int64                                 `json:"spend_last_day_microusd"`
	ActiveWorkers       int64                                 `json:"active_workers"`
	WorkerCapacity      int64                                 `json:"worker_capacity"`
	QueueTelemetry      bool                                  `json:"queue_telemetry_available"`
	QueueLanes          map[string]automationqueue.LaneHealth `json:"queue_lanes,omitempty"`
	QueueVisible        int64                                 `json:"queue_visible_approximate"`
	QueueInFlight       int64                                 `json:"queue_in_flight_approximate"`
	QueueDelayed        int64                                 `json:"queue_delayed_approximate"`
	DeadLetterTelemetry bool                                  `json:"dead_letter_telemetry_available"`
	DeadLetterVisible   int64                                 `json:"dead_letter_visible_approximate"`
	// Outbox is the durable boundary before a task reaches a lane. It is
	// intentionally aggregate-only: task IDs, payloads, queue URLs and delivery
	// errors remain private to the task inspector and server logs.
	OutboxTelemetryAvailable      bool                          `json:"outbox_telemetry_available"`
	OutboxPending                 int64                         `json:"outbox_pending"`
	OutboxProcessing              int64                         `json:"outbox_processing"`
	OutboxRetrying                int64                         `json:"outbox_retrying"`
	OutboxOldestPendingAt         *time.Time                    `json:"outbox_oldest_pending_at,omitempty"`
	OperationalTelemetryAvailable bool                          `json:"operational_telemetry_available"`
	LastWorkerSeenAt              *time.Time                    `json:"last_worker_seen_at,omitempty"`
	Workers                       []automationWorkerHealth      `json:"workers"`
	ReviewIngress                 automationReviewIngressHealth `json:"review_ingress"`
	Scaling                       automationScalingHealth       `json:"scaling"`
	GlobalActiveLimit             int                           `json:"global_active_limit"`
	ProjectActiveLimit            int                           `json:"project_active_limit"`
	QueueDepthLimit               int                           `json:"queue_depth_limit"`
	AdmissionSaturated            bool                          `json:"admission_saturated"`
}

type automationScalingHealth struct {
	Mode                    string `json:"mode"`
	Reason                  string `json:"reason,omitempty"`
	QueueDepth              int64  `json:"queue_depth"`
	ActiveWorkers           int64  `json:"active_workers"`
	AvailableWorkers        int64  `json:"available_workers"`
	DesiredWorkers          int64  `json:"desired_workers"`
	TargetMessagesPerWorker int    `json:"target_messages_per_worker"`
	MaxWorkers              int    `json:"max_workers"`
	WorkerGap               int64  `json:"worker_gap"`
}

type automationOutboxStateCount struct {
	State    string `gorm:"column:state"`
	Count    int64  `gorm:"column:count"`
	Retrying int64  `gorm:"column:retrying"`
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
	AgentKey           string                      `json:"agent_key"`
	MachineID          string                      `json:"machine_id"`
	Provider           string                      `json:"provider"`
	Model              string                      `json:"model"`
	Role               string                      `json:"role"`
	Lane               string                      `json:"lane"`
	Concurrency        int                         `json:"concurrency"`
	Draining           bool                        `json:"draining"`
	Capabilities       []string                    `json:"capabilities"`
	Protocols          []string                    `json:"protocols"`
	StartedAt          string                      `json:"started_at"`
	WorkspaceReadiness []automationWorkspaceHealth `json:"workspace_readiness"`
}

// workspaceAttestationRequest is accepted only over the worker callback
// channel after a fresh heartbeat for the same immutable worker identity. It
// is not a path to GitHub, source code, shell commands or credentials.
type workspaceAttestationRequest struct {
	WorkerID     string                          `json:"worker_id"`
	Attestations []workspaceAttestationStatement `json:"attestations"`
}

type workspaceAttestationStatement struct {
	ID               string   `json:"id"`
	Available        bool     `json:"available"`
	GitHubRepository string   `json:"github_repository,omitempty"`
	HeadSHA          string   `json:"head_sha,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	Clean            bool     `json:"clean"`
	ChangeCount      int      `json:"change_count"`
	TrackingBranch   string   `json:"tracking_branch,omitempty"`
	LocalAhead       int      `json:"local_ahead"`
	RemoteAhead      int      `json:"remote_ahead"`
	Capabilities     []string `json:"capabilities,omitempty"`
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
	var tasks []automationTaskListView
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	// GitHub-originated reviews have a technical requester, so a platform
	// administrator must be able to see their lifecycle in the same list as
	// manually submitted automation. Non-administrators remain strictly scoped
	// to their own generic tasks; Delivery task reads continue through project
	// membership checks in their dedicated surfaces.
	query := configuration.DB.Table("automation_tasks AS task").Select(`
		task.id, task.delivery_work_item_id, task.operation, task.status, task.provider, task.model,
		task.attempt_count, task.completed_at, task.created_at, task.updated_at,
		CASE WHEN task.output_ref <> '' THEN TRUE ELSE FALSE END AS result_available,
		CASE WHEN task.error_message <> '' THEN TRUE ELSE FALSE END AS has_error
	`)
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if !user.IsPlatformAdmin() {
		query = query.Where("task.requested_by = ?", requestedBy)
	}
	if err := query.Order("task.created_at DESC").Limit(100).Scan(&tasks).Error; err != nil {
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
	updates, statusCode, responseMessage, transitionErr := automationCancellationTransition(task, now, reason)
	if transitionErr != nil {
		if task.Status == "cancel_requested" {
			return utils.Success(c, http.StatusAccepted, "Automation cancellation already requested", task)
		}
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

// automationCancellationTransition settles an abandoned execution immediately
// once its renewable lease has expired. There is no live worker left to
// acknowledge cancel_requested in that state; retaining it forever would block
// a safe operator retry and keep a stale budget reservation active.
func automationCancellationTransition(task models.AutomationTask, now time.Time, reason string) (map[string]any, int, string, error) {
	updates := map[string]any{"error_message": "Cancellation requested by an authorized operator: " + reason}
	settle := func(message string) (map[string]any, int, string, error) {
		updates["status"] = "cancelled"
		updates["completed_at"] = now
		updates["lease_expires_at"] = nil
		updates["budget_reservation_micros"] = int64(0)
		updates["budget_reservation_expires_at"] = nil
		return updates, http.StatusOK, message, nil
	}
	switch task.Status {
	case "queued":
		return settle("Queued automation task cancelled")
	case "running":
		// A running task without a renewable lease has no live execution
		// authority. Settle it like an expired lease so it cannot retain a stale
		// budget reservation or block an operator retry indefinitely.
		if task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
			return settle("Expired automation task cancelled")
		}
		updates["status"] = "cancel_requested"
		return updates, http.StatusAccepted, "Automation cancellation requested", nil
	case "cancel_requested":
		return nil, 0, "", fmt.Errorf("automation cancellation already requested")
	default:
		return nil, 0, "", fmt.Errorf("automation task is not cancellable")
	}
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

	retry, err := newCodeReviewRetryTask(&original)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "Automation retry rejected", "The failed review no longer has a valid immutable evidence boundary")
	}
	message := codeReviewRetryQueueMessage(&original, retry)
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
	content, err := readAuthorizedToolExecutionReport(c)
	if err != nil {
		return err
	}
	return utils.Success(c, http.StatusOK, "Automation tool execution report loaded", json.RawMessage(content))
}

// GetToolExecutionReportDownload provides the sanitized report for an
// authorized reviewer. It is deliberately separate from agent execution
// downloads: a tool report contains an evidence-specific request/response
// contract.
func GetToolExecutionReportDownload(c echo.Context) error {
	content, err := readAuthorizedToolExecutionReport(c)
	if err != nil {
		return err
	}
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="automation-tool-execution-report.json"`)
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSON, content)
}

func readAuthorizedToolExecutionReport(c echo.Context) ([]byte, error) {
	bucket, key, err := authorizedToolExecutionReport(c)
	if err != nil {
		return nil, err
	}
	body, err := awsrepository.GetS3Object(c.Request().Context(), key, bucket)
	if err != nil {
		return nil, utils.Error(c, http.StatusServiceUnavailable, "Automation tool execution unavailable", "Could not read the private tool report")
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, 256*1024+1))
	if err != nil || len(content) == 0 || len(content) > 256*1024 || !json.Valid(content) {
		return nil, utils.Error(c, http.StatusServiceUnavailable, "Automation tool execution unavailable", "Could not read the private tool report")
	}
	sanitized, err := sanitizePrivateExecutionReport(content)
	if err != nil {
		return nil, utils.Error(c, http.StatusServiceUnavailable, "Automation tool execution unavailable", "Could not read the private tool report")
	}
	return sanitized, nil
}

// sanitizePrivateExecutionReport is a final server-side boundary for reports
// whose object-store producer may be older or may include untrusted browser
// output. It preserves operational data and summaries while removing secret-
// bearing fields, provider keys in text, and private reasoning payloads.
func sanitizePrivateExecutionReport(content []byte) ([]byte, error) {
	var report any
	if err := json.Unmarshal(content, &report); err != nil {
		return nil, err
	}
	if _, ok := report.(map[string]any); !ok {
		return nil, fmt.Errorf("tool report must be a JSON object")
	}
	sanitized := sanitizePrivateExecutionReportValue(report)
	return json.Marshal(sanitized)
}

func sanitizePrivateExecutionReportValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if executionReportPrivateReasoningNode(typed) {
			return map[string]any{}
		}
		result := make(map[string]any, len(typed))
		for key, entry := range typed {
			if executionReportSensitiveKey(key) || executionReportPrivateReasoningKey(key) || executionReportPrivateReasoningNode(entry) {
				continue
			}
			result[key] = sanitizePrivateExecutionReportValue(entry)
		}
		return result
	case []any:
		result := make([]any, 0, len(typed))
		for _, entry := range typed {
			if executionReportPrivateReasoningNode(entry) {
				continue
			}
			result = append(result, sanitizePrivateExecutionReportValue(entry))
		}
		return result
	case string:
		return sanitizePrivateExecutionReportText(typed)
	default:
		return value
	}
}

func executionReportPrivateReasoningNode(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, field := range []string{"type", "kind", "channel", "role"} {
		candidate, ok := object[field].(string)
		if ok && executionReportPrivateReasoningKey(candidate) {
			return true
		}
	}
	return false
}

func executionReportSensitiveKey(key string) bool {
	normalized := strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			return character
		}
		return -1
	}, strings.ToLower(strings.TrimSpace(key)))
	if executionReportOperationalTokenKey(normalized) {
		return false
	}
	if normalized == "key" {
		return true
	}
	for _, marker := range []string{"apikey", "accesskey", "clientsecret", "privatekey", "providerkey", "password", "passphrase", "secret", "token", "authorization", "credential", "cookie", "session"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return normalized == "auth"
}

func executionReportPrivateReasoningKey(key string) bool {
	normalized := strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' && character != '_' && character != '-' || character >= '0' && character <= '9' {
			return character
		}
		return -1
	}, strings.ToLower(strings.TrimSpace(key)))
	if executionReportOperationalTokenKey(normalized) {
		return false
	}
	return strings.Contains(normalized, "reasoning") || strings.Contains(normalized, "chainofthought") ||
		normalized == "analysis" || normalized == "analysiscontent" || normalized == "analysistext" ||
		normalized == "thought" || normalized == "thoughts" || normalized == "cot" ||
		strings.Contains(normalized, "privatecot") || strings.Contains(normalized, "privateanalysis") ||
		strings.Contains(normalized, "hiddenanalysis") || strings.Contains(normalized, "privatethought") || strings.Contains(normalized, "hiddenthought")
}

func executionReportOperationalTokenKey(key string) bool {
	switch key {
	case "inputtokens", "outputtokens", "cachedinputtokens", "cachewritetokens", "reasoningtokens", "totaltokens",
		"prompttokens", "completiontokens", "maxcompletiontokens", "maxoutputtokens", "tokencount", "totalinputtokens", "totaloutputtokens":
		return true
	default:
		return false
	}
}

func sanitizePrivateExecutionReportText(value string) string {
	sanitized, _ := automationagent.RedactSourceExcerpt(value)
	for _, pattern := range executionReportProviderKeyPatterns {
		sanitized = pattern.ReplaceAllString(sanitized, "[REDACTED]")
	}
	return executionReportURLPattern.ReplaceAllStringFunc(sanitized, func(candidate string) string {
		trimmed := strings.TrimRight(candidate, ".,;:!?)]}")
		trailing := candidate[len(trimmed):]
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return candidate
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		parsed.RawFragment = ""
		return parsed.String() + trailing
	})
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
		if err == gorm.ErrRecordNotFound {
			return "", "", utils.Error(c, http.StatusNotFound, "Automation tool execution not found", "")
		}
		return "", "", utils.Error(c, http.StatusInternalServerError, "Automation tool execution unavailable", "Could not resolve the parent task")
	}
	if !mayAccessTask(c, &task, requestedBy) {
		return "", "", utils.Error(c, http.StatusNotFound, "Automation tool execution not found", "")
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
	safeExecutions := make([]automationCostExecution, 0, len(executions))
	entries := make([]automationCostExecution, 0, len(executions)+len(toolExecutions))
	for _, execution := range executions {
		projected := traceEntryFromAgentExecution(execution)
		safeExecutions = append(safeExecutions, projected)
		entries = append(entries, projected)
	}
	safeToolExecutions := make([]automationCostExecution, 0, len(toolExecutions))
	for _, execution := range toolExecutions {
		projected := traceEntryFromToolExecution(execution)
		safeToolExecutions = append(safeToolExecutions, projected)
		entries = append(entries, projected)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CompletedAt.Equal(entries[j].CompletedAt) {
			return entries[i].ID.String() < entries[j].ID.String()
		}
		return entries[i].CompletedAt.Before(entries[j].CompletedAt)
	})
	return utils.Success(c, http.StatusOK, "Automation execution trace", map[string]any{
		"task":            automationTraceTaskFrom(task),
		"executions":      safeExecutions,
		"tool_executions": safeToolExecutions,
		"entries":         entries,
	})
}

func traceEntryFromAgentExecution(execution models.AutomationExecution) automationCostExecution {
	return automationCostExecution{
		ID: execution.ID, AutomationTaskID: execution.AutomationTaskID, DeliveryWorkItemID: execution.DeliveryWorkItemID,
		AgentInstanceID: execution.AgentInstanceID,
		ExecutionKind:   "agent", StepKey: execution.StepKey, Provider: execution.Provider, Model: execution.Model,
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
		AgentInstanceID: execution.AgentInstanceID,
		ExecutionKind:   "tool", Tool: execution.Tool, CallKey: execution.CallKey, CallStatus: execution.CallStatus, StepKey: execution.StepKey, Provider: execution.Provider, Model: execution.Model,
		InputTokens: execution.InputTokens, OutputTokens: execution.OutputTokens, CachedInputTokens: execution.CachedInputTokens,
		CacheWriteTokens: execution.CacheWriteTokens, ReasoningTokens: execution.ReasoningTokens, TotalTokens: execution.TotalTokens,
		InputCostMicros: execution.InputCostMicros, OutputCostMicros: execution.OutputCostMicros, CachedCostMicros: execution.CachedCostMicros,
		CacheWriteCostMicros: execution.CacheWriteCostMicros, TotalCostMicros: execution.TotalCostMicros,
		PricingBasis: execution.PricingBasis, ProviderOutcome: providerOutcomeFromUsage(execution.UsageJSON), CompletedAt: execution.CompletedAt,
	}
}

func automationLedgerInstancePointer(instanceID uuid.UUID) *uuid.UUID {
	if instanceID == uuid.Nil {
		return nil
	}
	return &instanceID
}

func attributeAutomationCostRowsToCallbackIdentity(c echo.Context, execution *models.AutomationExecution, toolExecutions []models.AutomationToolExecution) bool {
	identity, ok := currentAgentCallbackIdentity(c)
	if !ok {
		return false
	}
	instanceID := automationLedgerInstancePointer(identity.InstanceID)
	if execution != nil {
		execution.AgentInstanceID = instanceID
	}
	for index := range toolExecutions {
		toolExecutions[index].AgentInstanceID = instanceID
	}
	return true
}

// CostOverview is intentionally aggregated server-side, so a dashboard never
// has to download prompts, results, or all execution rows just to show spend.
func CostOverview(c echo.Context) error {
	setAutomationCostNoStoreHeaders(c)
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation costs unavailable", "Database is unavailable")
	}
	requestedBy, _ := c.Get("cognito_sub").(string)
	if strings.TrimSpace(requestedBy) == "" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	costQuery, queryErr := parseAutomationCostQuery(c)
	if queryErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation cost query", queryErr.Error())
	}
	days, page, pageSize := costQuery.Days, costQuery.Page, costQuery.PageSize
	// Resolve the actor once. A cost overview is available to a task owner and
	// to project members that can view the linked Delivery work item. A code or
	// QA reviewer therefore sees the spend behind the gates they are asked to
	// approve, without gaining visibility into unrelated generic automation.
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	workspaceMode, _ := c.Get("workspace_mode").(string)
	organizationID, hasOrganizationID := c.Get("organization_id").(uuid.UUID)
	workspaceClientIDs, clientScopeErr := automationCostWorkspaceClientIDs(configuration.DB, workspaceMode, organizationID, hasOrganizationID)
	if clientScopeErr != nil {
		if errors.Is(clientScopeErr, organizationscope.ErrOrganizationNotFound) {
			return utils.Error(c, http.StatusNotFound, "Automation cost workspace not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not resolve automation cost workspace")
	}
	costQuery.WorkspaceClientIDs = workspaceClientIDs
	snapshotAt := costQuery.SnapshotAt
	if costQuery.Cursor != nil {
		if !snapshotAt.IsZero() && !snapshotAt.Equal(costQuery.Cursor.SnapshotAt) {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation cost cursor", "Cursor snapshot does not match the requested snapshot")
		}
		snapshotAt = costQuery.Cursor.SnapshotAt
	}
	if costQuery.WorkItemCursor != nil {
		if !snapshotAt.IsZero() && !snapshotAt.Equal(costQuery.WorkItemCursor.SnapshotAt) {
			return utils.Error(c, http.StatusBadRequest, "Invalid work-item cost cursor", "Cursor snapshot does not match the requested snapshot")
		}
		snapshotAt = costQuery.WorkItemCursor.SnapshotAt
	}
	if snapshotAt.IsZero() {
		snapshotAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	costQuery.SnapshotAt = snapshotAt
	cursorScope := automationCostCursorScope(costQuery, workspaceMode, organizationID, hasOrganizationID, user.CognitoSub)
	if costQuery.Cursor != nil && costQuery.Cursor.ScopeHash != cursorScope {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation cost cursor", "Cursor does not match the selected filters or workspace")
	}
	workItemCursorScope := automationCostWorkItemCursorScope(costQuery, workspaceMode, organizationID, hasOrganizationID, user.CognitoSub)
	if costQuery.WorkItemCursor != nil && costQuery.WorkItemCursor.ScopeHash != workItemCursorScope {
		return utils.Error(c, http.StatusBadRequest, "Invalid work-item cost cursor", "Cursor does not match the selected filters or workspace")
	}
	ledgerSource, ledgerCoverage, ledgerErr := automationCostLedgerSource(configuration.DB)
	if ledgerErr != nil {
		return utils.ErrorWithData(c, http.StatusServiceUnavailable, "Automation costs unavailable", "Cost ledger is initializing", map[string]any{"ledger_coverage": ledgerCoverage})
	}
	baseQuery := configuration.DB.Table("(" + ledgerSource + ") AS execution").
		Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id").
		Joins("LEFT JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id").
		Joins("LEFT JOIN delivery_projects AS project ON project.id = work_item.project_id").
		Session(&gorm.Session{})
	baseQuery = applyAutomationCostTimeWindow(baseQuery, snapshotAt, costQuery)
	baseQuery, scopeErr := applyAutomationCostWorkspaceScope(baseQuery, workspaceMode, organizationID, hasOrganizationID, user.IsPlatformAdmin(), workspaceClientIDs)
	if scopeErr != nil {
		return utils.Error(c, http.StatusNotFound, "Automation cost workspace not found", "")
	}
	if !user.IsPlatformAdmin() {
		projectIDs, membershipErr := deliveryReadableProjectIDs(user.CognitoSub)
		if membershipErr != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not resolve delivery project access")
		}
		baseQuery = applyAutomationCostActorScope(baseQuery, requestedBy, projectIDs, false)
	}
	filteredQuery := applyAutomationCostFilters(baseQuery.Session(&gorm.Session{}), costQuery)
	// Keep the statements independent: the aggregate and the breakdown have
	// different select/group shapes and must never leak state into one another.
	summary, err := aggregateAutomationCostSummary(filteredQuery)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var byOperation []automationCostBreakdown
	if err := filteredQuery.Session(&gorm.Session{}).Select("task.operation AS key, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("task.operation").Order("SUM(execution.total_cost_micros) DESC").Scan(&byOperation).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	// Operations describe the requested capability; step keys describe the
	// delivery phase that actually spent the budget. Keep both dimensions so an
	// operator can distinguish, for example, plan generation from browser QA.
	var byStep []automationCostBreakdown
	if err := filteredQuery.Session(&gorm.Session{}).Select("COALESCE(NULLIF(execution.step_key, ''), 'execution') AS key, execution.execution_kind, execution.tool, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("execution.step_key, execution.execution_kind, execution.tool").Order("SUM(execution.total_cost_micros) DESC").Scan(&byStep).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var byProject []automationCostProject
	if err := filteredQuery.Session(&gorm.Session{}).Select("project.id AS project_id, COALESCE(NULLIF(project.name, ''), 'Automatización general') AS project_name, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("project.id, project.name").Order("SUM(execution.total_cost_micros) DESC").Scan(&byProject).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var byAgent []automationCostAgent
	if err := filteredQuery.Session(&gorm.Session{}).
		Select("execution.agent_key AS agent_key, COUNT(*) AS executions, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").
		Where("execution.agent_key <> ''").Group("execution.agent_key").Order("SUM(execution.total_cost_micros) DESC").Scan(&byAgent).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var byWorkItem []automationCostWorkItem
	workItemQuery := filteredQuery.Session(&gorm.Session{}).
		Select("project.id AS project_id, COALESCE(NULLIF(project.name, ''), 'Automatización general') AS project_name, work_item.id AS work_item_id, work_item.title AS work_item_title, COUNT(*) AS executions, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").
		Where("work_item.id IS NOT NULL").Group("project.id, project.name, work_item.id, work_item.title")
	if costQuery.WorkItemCursor != nil {
		workItemQuery = applyAutomationCostWorkItemCursor(workItemQuery, costQuery.WorkItemCursor)
	}
	if err := workItemQuery.Order("SUM(execution.total_cost_micros) DESC, work_item.id ASC").Limit(costQuery.WorkItemLimit + 1).Scan(&byWorkItem).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	workItemNextCursor := ""
	if len(byWorkItem) > costQuery.WorkItemLimit {
		byWorkItem = byWorkItem[:costQuery.WorkItemLimit]
		last := byWorkItem[len(byWorkItem)-1]
		workItemNextCursor, err = encodeAutomationCostWorkItemCursor(automationCostWorkItemCursor{
			SnapshotAt:      snapshotAt,
			TotalCostMicros: last.TotalCostMicros,
			WorkItemID:      last.WorkItemID,
			ScopeHash:       workItemCursorScope,
		})
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not prepare the next work-item page")
		}
	}
	var byModel []automationCostModel
	if err := filteredQuery.Session(&gorm.Session{}).Select("COALESCE(NULLIF(execution.provider, ''), 'sin proveedor') AS provider, COALESCE(NULLIF(execution.model, ''), 'sin modelo') AS model, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("execution.provider, execution.model").Order("SUM(execution.total_cost_micros) DESC").Scan(&byModel).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	var recentTotal int64
	if err := filteredQuery.Session(&gorm.Session{}).Count(&recentTotal).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	mode := "offset"
	if costQuery.Cursor != nil {
		mode = "cursor"
	}
	recentPage := automationCostExecutionPage{Page: page, PageSize: pageSize, Total: recentTotal, Mode: mode, SnapshotAt: snapshotAt}
	if recentTotal > 0 {
		recentPage.TotalPages = int((recentTotal + int64(pageSize) - 1) / int64(pageSize))
	}
	recentQuery := applyAutomationCostCursor(filteredQuery.Session(&gorm.Session{}), costQuery.Cursor).
		Select(automationCostRecentExecutionSelect).
		Order("execution.completed_at DESC, execution.id DESC")
	var recentExecutions []automationCostExecution
	if costQuery.Cursor != nil {
		if err := recentQuery.Limit(pageSize + 1).Scan(&recentExecutions).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
		}
		if len(recentExecutions) > pageSize {
			recentPage.HasMore = true
			recentExecutions = recentExecutions[:pageSize]
		}
	} else if err := recentQuery.Limit(pageSize).Offset((page - 1) * pageSize).Scan(&recentExecutions).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "")
	}
	if costQuery.Cursor == nil {
		recentPage.HasMore = int64(page*pageSize) < recentTotal
	}
	if recentPage.HasMore && len(recentExecutions) > 0 {
		cursor, cursorErr := encodeAutomationCostCursor(recentExecutions[len(recentExecutions)-1], cursorScope, snapshotAt)
		if cursorErr != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation costs unavailable", "Could not prepare the next cost page")
		}
		recentPage.NextCursor = cursor
	}
	budgetWatch := make([]automationCostBudgetWatch, 0)
	taskBudgetWatch := make([]automationCostTaskBudgetWatch, 0)
	if user.IsPlatformAdmin() {
		monthStart := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
		budgetQuery := configuration.DB.Table("delivery_projects AS project").
			Select("project.id AS project_id, project.name AS project_name, project.monthly_budget_micros, project.budget_alert_percent AS alert_percent, COALESCE(SUM(execution.total_cost_micros), 0) AS spent_micros").
			Joins("LEFT JOIN delivery_work_items AS work_item ON work_item.project_id = project.id AND work_item.deleted_at IS NULL").
			Joins("LEFT JOIN ("+ledgerSource+") AS execution ON execution.delivery_work_item_id = work_item.id AND execution.completed_at >= ?", monthStart).
			Where("project.deleted_at IS NULL AND project.monthly_budget_micros > 0").
			Group("project.id, project.name, project.monthly_budget_micros, project.budget_alert_percent")
		if strings.EqualFold(strings.TrimSpace(workspaceMode), "organization") {
			budgetQuery = budgetQuery.Where("project.client_id IN ?", workspaceClientIDs)
		}
		if costQuery.ClientID != nil {
			budgetQuery = budgetQuery.Where("project.client_id = ?", *costQuery.ClientID)
		}
		var currentMonthBudgets []automationCostBudgetWatch
		if err := budgetQuery.Scan(&currentMonthBudgets).Error; err != nil {
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
		taskBudgetQuery := configuration.DB.Table("delivery_work_items AS work_item").
			Select("project.id AS project_id, project.name AS project_name, work_item.id AS work_item_id, work_item.title AS work_item_title, work_item.budget_micros, work_item.budget_alert_percent AS alert_percent, COALESCE(SUM(execution.total_cost_micros), 0) AS spent_micros").
			Joins("JOIN delivery_projects AS project ON project.id = work_item.project_id AND project.deleted_at IS NULL").
			Joins("LEFT JOIN (" + ledgerSource + ") AS execution ON execution.delivery_work_item_id = work_item.id").
			Where("work_item.deleted_at IS NULL AND work_item.budget_micros > 0").
			Group("project.id, project.name, work_item.id, work_item.title, work_item.budget_micros, work_item.budget_alert_percent").
			Order("work_item.updated_at DESC")
		if strings.EqualFold(strings.TrimSpace(workspaceMode), "organization") {
			taskBudgetQuery = taskBudgetQuery.Where("project.client_id IN ?", workspaceClientIDs)
		}
		if costQuery.ClientID != nil {
			taskBudgetQuery = taskBudgetQuery.Where("project.client_id = ?", *costQuery.ClientID)
		}
		var currentTaskBudgets []automationCostTaskBudgetWatch
		if err := taskBudgetQuery.Scan(&currentTaskBudgets).Error; err != nil {
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
	return utils.Success(c, http.StatusOK, "Automation cost overview", map[string]any{
		"range_days": days, "snapshot_at": snapshotAt,
		"applied_filters": map[string]any{
			"client_id": costQuery.ClientID, "project_id": costQuery.ProjectID, "epic_id": costQuery.EpicID, "work_item_id": costQuery.WorkItemID,
			"agent_instance_id": costQuery.AgentInstanceID,
			"agent_key":         costQuery.AgentKey, "step_key": costQuery.StepKey, "provider": costQuery.Provider, "model": costQuery.Model,
			"from_at": automationCostAppliedTimestamp(costQuery.FromAt), "to_at": automationCostAppliedTimestamp(costQuery.ToAt),
		},
		"summary": summary, "by_operation": byOperation, "by_step": byStep,
		"by_project": byProject, "by_work_item": byWorkItem, "by_work_item_limit": costQuery.WorkItemLimit,
		"by_work_item_cursor": costQuery.WorkItemCursorToken, "by_work_item_next_cursor": workItemNextCursor,
		"by_agent": byAgent, "by_model": byModel,
		"budget_watch": budgetWatch, "task_budget_watch": taskBudgetWatch,
		"recent_execution_page": recentPage, "recent_executions": recentExecutions, "ledger_coverage": ledgerCoverage,
	})
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
	workspace, workspaceErr := resolveAutomationHealthWorkspace(c, user)
	if workspaceErr != nil {
		if errors.Is(workspaceErr, errAutomationHealthWorkspaceUnavailable) || errors.Is(workspaceErr, organizationscope.ErrOrganizationNotFound) {
			return utils.Error(c, http.StatusNotFound, "Automation health unavailable", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "Could not resolve automation workspace")
	}
	query, scopeErr := applyAutomationHealthTaskWorkspaceScope(configuration.DB.Model(&models.AutomationTask{}), workspace)
	if scopeErr != nil {
		return utils.Error(c, http.StatusNotFound, "Automation health unavailable", "")
	}
	now := time.Now().UTC()
	result := automationHealth{
		Workers: []automationWorkerHealth{},
	}
	if workspace.PlatformTelemetry {
		result.Scaling = automationScalingHealth{Mode: "disabled", Reason: "scaling_policy_not_configured"}
	} else {
		result.Scaling = automationScalingHealth{Mode: "unavailable", Reason: "platform_workspace_required"}
	}
	cfg, _ := c.Get("config").(*models.Config)
	if err := query.Session(&gorm.Session{}).Where("automation_tasks.status = ?", "queued").Count(&result.Queued).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if err := query.Session(&gorm.Session{}).Where("automation_tasks.status = ?", "running").Count(&result.Running).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if err := query.Session(&gorm.Session{}).Where("automation_tasks.status IN ?", []string{"queued", "running", "cancel_requested"}).Count(&result.ActiveTasks).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if workspace.PlatformTelemetry && cfg != nil {
		result.GlobalActiveLimit = cfg.AutomationGlobalActiveLimit
		result.ProjectActiveLimit = cfg.AutomationProjectActiveLimit
		result.QueueDepthLimit = cfg.AutomationQueueDepthLimit
		result.AdmissionSaturated = (result.GlobalActiveLimit > 0 && result.ActiveTasks >= int64(result.GlobalActiveLimit)) ||
			(result.QueueDepthLimit > 0 && result.ActiveTasks >= int64(result.QueueDepthLimit))
	}
	if err := query.Session(&gorm.Session{}).Where("automation_tasks.status = ? AND automation_tasks.completed_at >= ?", "failed", now.Add(-24*time.Hour)).Count(&result.FailedLastDay).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if err := query.Session(&gorm.Session{}).Where("automation_tasks.status = ? AND automation_tasks.lease_expires_at IS NOT NULL AND automation_tasks.lease_expires_at <= ?", "running", now).Count(&result.ExpiredLeases).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	// Queue depth spans every tenant-scoped task on the shared local agent.
	// It is operational telemetry for platform admins only; a regular
	// requester must not infer another project's activity from it.
	if workspace.PlatformTelemetry {
		queueHealth := automationqueue.QueueHealth(c.Request().Context())
		result.QueueTelemetry = queueHealth.Available
		result.QueueLanes = queueHealth.Lanes
		result.QueueVisible, result.QueueInFlight, result.QueueDelayed = queueHealth.Visible, queueHealth.InFlight, queueHealth.Delayed
		result.DeadLetterTelemetry, result.DeadLetterVisible = queueHealth.DeadLetterAvailable, queueHealth.DeadLetterVisible
		populateAutomationOutboxHealth(configuration.DB, &result)
	}
	// Health must use the same schema-compatible accounting projection as the
	// cost screen. Otherwise an optional tool-ledger migration can make a
	// decorative health read fail even though the primary execution ledger is
	// available and authoritative.
	ledgerSource, _, ledgerErr := automationCostLedgerSource(configuration.DB)
	if ledgerErr != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation health unavailable", "Cost ledger is initializing")
	}
	spendQuery, scopeErr := applyAutomationHealthSpendWorkspaceScope(
		configuration.DB.Table("("+ledgerSource+") AS execution").
			Joins("JOIN automation_tasks AS task ON task.id = execution.automation_task_id").
			Where("execution.completed_at >= ?", now.Add(-24*time.Hour)),
		workspace,
	)
	if scopeErr != nil {
		return utils.Error(c, http.StatusNotFound, "Automation health unavailable", "")
	}
	if err := spendQuery.Select("COALESCE(SUM(execution.total_cost_micros), 0) AS spend_last_day").Scan(&result.SpendLastDay).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
	}
	if workspace.PlatformTelemetry {
		result.OperationalTelemetryAvailable = true
		workerQuery := configuration.DB.Model(&models.AutomationAgentHeartbeat{}).Where("last_seen_at >= ?", now.Add(-90*time.Second))
		var heartbeatRows []automationWorkerHealth
		if err := workerQuery.Session(&gorm.Session{}).
			Select("provider, model, concurrency, draining, started_at, last_seen_at, capabilities_json, protocols_json, workspace_readiness").
			Order("last_seen_at DESC").
			Limit(maxAutomationHealthWorkerRows).
			Find(&heartbeatRows).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
		}
		result.Workers = currentAutomationWorkers(heartbeatRows)
		result.ActiveWorkers = int64(len(result.Workers))
		for _, worker := range result.Workers {
			result.WorkerCapacity += int64(worker.Concurrency)
		}
		result.LastWorkerSeenAt = newestAutomationWorkerLastSeen(result.Workers)
		for index := range result.Workers {
			if strings.TrimSpace(result.Workers[index].CapabilitiesJSON) != "" {
				if err := json.Unmarshal([]byte(result.Workers[index].CapabilitiesJSON), &result.Workers[index].Capabilities); err != nil {
					return utils.Error(c, http.StatusInternalServerError, "Automation health unavailable", "")
				}
			}
			result.Workers[index].Protocols = safeWorkerProtocolsJSON(result.Workers[index].ProtocolsJSON)
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
		for _, worker := range result.Workers {
			if (worker.Role == "" && worker.Lane == "") || (worker.Role == string(agentwork.RoleReviewer) && worker.Lane == string(agentwork.LaneReview)) {
				reviewWorkers++
			}
		}
		cfg, _ := c.Get("config").(*models.Config)
		result.ReviewIngress = automationReviewIngressStatus(cfg, reviewWorkers)
	}
	return utils.Success(c, http.StatusOK, "Automation health", result)
}

// populateAutomationOutboxHealth keeps the control-plane handoff observable
// without turning a temporarily unavailable optional projection into a false
// claim that no work is pending. The dispatcher remains the only component
// that can publish an event; this read model never retries, edits, or exposes
// a durable payload.
func populateAutomationOutboxHealth(db *gorm.DB, health *automationHealth) {
	if db == nil || health == nil {
		return
	}
	var rows []automationOutboxStateCount
	if err := db.Model(&models.OutboxEvent{}).
		Select("state, COUNT(*) AS count, COALESCE(SUM(CASE WHEN attempts > 0 THEN 1 ELSE 0 END), 0) AS retrying").
		Where("target_runtime = ?", string(outboxService.RuntimeLocalAgent)).
		Group("state").
		Scan(&rows).Error; err != nil {
		return
	}
	var oldest sql.NullTime
	if err := db.Model(&models.OutboxEvent{}).
		Select("MIN(created_at)").
		Where("target_runtime = ? AND state = ?", string(outboxService.RuntimeLocalAgent), "pending").
		Scan(&oldest).Error; err != nil {
		return
	}
	health.OutboxTelemetryAvailable = true
	for _, row := range rows {
		health.OutboxRetrying += row.Retrying
		switch row.State {
		case "pending":
			health.OutboxPending = row.Count
		case "processing":
			health.OutboxProcessing = row.Count
		}
	}
	if oldest.Valid {
		value := oldest.Time.UTC()
		health.OutboxOldestPendingAt = &value
	}
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

// currentAutomationWorkers returns the live operational projection while
// retaining the heartbeat rows themselves in storage for audit and diagnosis.
// V1 intentionally permits one local worker per declared role/lane; a restart
// must replace that lane's presentation rather than inflate capacity. Legacy
// workers without a role/lane retain their individual entries for migration
// compatibility because they cannot be grouped safely.
func currentAutomationWorkers(candidates []automationWorkerHealth) []automationWorkerHealth {
	newestByLane := make(map[string]automationWorkerHealth)
	legacy := make([]automationWorkerHealth, 0, len(candidates))
	for _, worker := range candidates {
		role, lane := strings.TrimSpace(worker.Role), strings.TrimSpace(worker.Lane)
		if role == "" && lane == "" {
			legacy = append(legacy, worker)
			continue
		}
		key := role + "\x00" + lane
		current, exists := newestByLane[key]
		if !exists || worker.LastSeenAt.After(current.LastSeenAt) || (worker.LastSeenAt.Equal(current.LastSeenAt) && worker.StartedAt.After(current.StartedAt)) {
			newestByLane[key] = worker
		}
	}

	result := make([]automationWorkerHealth, 0, len(legacy)+len(newestByLane))
	result = append(result, legacy...)
	for _, worker := range newestByLane {
		result = append(result, worker)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].LastSeenAt.Equal(result[j].LastSeenAt) {
			return result[i].LastSeenAt.After(result[j].LastSeenAt)
		}
		if result[i].Role != result[j].Role {
			return result[i].Role < result[j].Role
		}
		if result[i].Lane != result[j].Lane {
			return result[i].Lane < result[j].Lane
		}
		return result[i].StartedAt.After(result[j].StartedAt)
	})
	return result
}

func newestAutomationWorkerLastSeen(workers []automationWorkerHealth) *time.Time {
	if len(workers) == 0 {
		return nil
	}
	newest := workers[0].LastSeenAt
	for _, worker := range workers[1:] {
		if worker.LastSeenAt.After(newest) {
			newest = worker.LastSeenAt
		}
	}
	return &newest
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

// normalizeWorkerProtocols validates the deliberately small protocol
// allow-list and canonicalizes a heartbeat for stable persistence. Missing
// protocol metadata is represented as an empty list for pre-protocol agents.
func normalizeWorkerProtocols(protocols []string) ([]string, error) {
	if len(protocols) > 1 {
		return nil, fmt.Errorf("too many worker protocols")
	}
	seen := make(map[string]struct{}, len(protocols))
	normalized := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		protocol = strings.TrimSpace(protocol)
		if protocol != agentprotocol.ProtocolDeliveryPlanStepsV1 {
			return nil, fmt.Errorf("worker protocol is not allowlisted")
		}
		if _, duplicate := seen[protocol]; duplicate {
			return nil, fmt.Errorf("worker protocol is duplicated")
		}
		seen[protocol] = struct{}{}
		normalized = append(normalized, protocol)
	}
	sort.Strings(normalized)
	return normalized, nil
}

// safeWorkerProtocolsJSON prevents corrupted or pre-allow-list database values
// from becoming arbitrary metadata in health responses.
func safeWorkerProtocolsJSON(raw string) []string {
	protocols := []string{}
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &protocols) != nil {
		return []string{}
	}
	normalized, err := normalizeWorkerProtocols(protocols)
	if err != nil {
		return []string{}
	}
	return normalized
}

// AgentHeartbeat gives the dashboard a real liveness signal from an enrolled
// worker instance. Profile and machine identity are derived from the signed
// callback rather than trusted from the request body.
func AgentHeartbeat(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "")
	}
	var request agentHeartbeatRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	callbackIdentity, ok := bindCallbackProfileIdentity(c, &request.AgentKey, &request.MachineID)
	if !ok {
		return nil
	}
	workerID := strings.TrimSpace(request.WorkerID)
	if _, err := uuid.FromString(workerID); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	agentKey := strings.TrimSpace(request.AgentKey)
	if agentKey == "" {
		agentKey = "generalist"
	}
	machineID := strings.TrimSpace(request.MachineID)
	if !agentProfileKeyPattern.MatchString(agentKey) || (machineID != "" && !isOpaqueMachineID(machineID)) {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	profile, profileErr := findActiveAgentProfile(configuration.DB, agentKey)
	if profileErr != nil || !profileSupportsCapabilities(profile, request.Capabilities) {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	if !providerAllowed(request.Provider) || len(strings.TrimSpace(request.Model)) == 0 || len(strings.TrimSpace(request.Model)) > 128 || request.Concurrency < 1 || request.Concurrency > 8 {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	role, lane, err := normalizeWorkerRoleLane(request.Role, request.Lane)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	if roleHeader, laneHeader := strings.TrimSpace(c.Request().Header.Get("X-Agent-Role")), strings.TrimSpace(c.Request().Header.Get("X-Agent-Lane")); roleHeader != "" || laneHeader != "" {
		if roleHeader != role || laneHeader != lane {
			return utils.Error(c, http.StatusForbidden, "Worker identity does not match heartbeat", "")
		}
	}
	if !validWorkerProvider(role, lane, request.Provider, request.Model) {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	protocolList, err := normalizeWorkerProtocols(request.Protocols)
	if err != nil {
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
	capabilityList := request.Capabilities
	if capabilityList == nil {
		capabilityList = []string{}
	}
	capabilities, err := json.Marshal(capabilityList)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	protocols, err := json.Marshal(protocolList)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid agent heartbeat", "")
	}
	now := time.Now().UTC()
	instanceID := callbackIdentity.InstanceID
	heartbeat := models.AutomationAgentHeartbeat{WorkerID: workerID, AgentKey: agentKey, MachineID: machineID, AgentInstanceID: &instanceID, Provider: strings.ToLower(strings.TrimSpace(request.Provider)), Model: strings.TrimSpace(request.Model), Concurrency: request.Concurrency, Draining: request.Draining, CapabilitiesJSON: string(capabilities), ProtocolsJSON: string(protocols), WorkspaceReadiness: string(workspaceReadiness), StartedAt: startedAt.UTC(), LastSeenAt: now}
	if err := configuration.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "worker_id"}}, DoUpdates: clause.Assignments(map[string]any{"agent_key": heartbeat.AgentKey, "machine_id": heartbeat.MachineID, "agent_instance_id": heartbeat.AgentInstanceID, "provider": heartbeat.Provider, "model": heartbeat.Model, "concurrency": heartbeat.Concurrency, "draining": heartbeat.Draining, "capabilities_json": heartbeat.CapabilitiesJSON, "protocols_json": heartbeat.ProtocolsJSON, "workspace_readiness": heartbeat.WorkspaceReadiness, "started_at": heartbeat.StartedAt, "last_seen_at": heartbeat.LastSeenAt, "updated_at": now})}).Create(&heartbeat).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation heartbeat unavailable", "")
	}
	return utils.Success(c, http.StatusOK, "Automation agent heartbeat accepted", map[string]any{"accepted_at": now})
}

// AgentWorkspaceAttestations records short-lived workspace metadata from a
// live local agent. The later Delivery refresh path validates repository and
// SHA against the independently obtained GitHub checkpoint, so an attestation
// never creates workspace, merge, publish or deployment authority on its own.
func AgentWorkspaceAttestations(c echo.Context) error {
	if !validWorkerCallbackCredential(c) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "")
	}
	var request workspaceAttestationRequest
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid workspace attestation", "")
	}
	workerID := strings.TrimSpace(request.WorkerID)
	if _, err := uuid.FromString(workerID); err != nil || len(request.Attestations) == 0 || len(request.Attestations) > 32 {
		return utils.Error(c, http.StatusBadRequest, "Invalid workspace attestation", "")
	}
	role, lane, err := normalizeWorkerRoleLane(strings.TrimSpace(c.Request().Header.Get("X-Agent-Role")), strings.TrimSpace(c.Request().Header.Get("X-Agent-Lane")))
	if err != nil || role == "" || lane == "" {
		return utils.Error(c, http.StatusForbidden, "Worker identity is required for workspace attestation", "")
	}
	now := time.Now().UTC()
	var heartbeat models.AutomationAgentHeartbeat
	if err := configuration.DB.Where("worker_id = ? AND role = ? AND lane = ? AND last_seen_at >= ?", workerID, role, lane, now.Add(-workspaceAttestationTTL)).First(&heartbeat).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusConflict, "Workspace attestation rejected", "A fresh heartbeat from the same worker is required before reporting workspace state")
		}
		return utils.Error(c, http.StatusInternalServerError, "Workspace attestation unavailable", "")
	}
	if err := validateWorkspaceAttestations(request.Attestations); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid workspace attestation", "")
	}
	for _, statement := range request.Attestations {
		capabilities, marshalErr := json.Marshal(statement.Capabilities)
		if marshalErr != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid workspace attestation", "")
		}
		attestation := models.AutomationWorkspaceAttestation{
			WorkerID: workerID, Role: role, Lane: lane, WorkspaceID: statement.ID, Available: statement.Available,
			GitHubRepository: strings.ToLower(strings.TrimSpace(statement.GitHubRepository)), HeadSHA: strings.ToLower(strings.TrimSpace(statement.HeadSHA)),
			Branch: strings.TrimSpace(statement.Branch), Clean: statement.Clean, ChangeCount: statement.ChangeCount,
			TrackingBranch: strings.TrimSpace(statement.TrackingBranch), LocalAhead: statement.LocalAhead, RemoteAhead: statement.RemoteAhead,
			CapabilitiesJSON: string(capabilities), AttestedAt: now,
		}
		if err := configuration.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "worker_id"}, {Name: "workspace_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"role", "lane", "available", "github_repository", "head_sha", "branch", "clean", "change_count", "tracking_branch", "local_ahead", "remote_ahead", "capabilities_json", "attested_at", "updated_at"}),
		}).Create(&attestation).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Workspace attestation unavailable", "")
		}
	}
	return utils.Success(c, http.StatusOK, "Workspace attestation accepted", map[string]any{"accepted_at": now, "workspace_count": len(request.Attestations)})
}

func validateWorkspaceAttestations(attestations []workspaceAttestationStatement) error {
	seen := make(map[string]struct{}, len(attestations))
	for _, statement := range attestations {
		if !workerWorkspaceIDPattern.MatchString(statement.ID) {
			return fmt.Errorf("invalid workspace id")
		}
		if _, exists := seen[statement.ID]; exists {
			return fmt.Errorf("duplicate workspace id")
		}
		seen[statement.ID] = struct{}{}
		if statement.ChangeCount < 0 || statement.ChangeCount > 1000000 || statement.LocalAhead < 0 || statement.LocalAhead > 1000000 || statement.RemoteAhead < 0 || statement.RemoteAhead > 1000000 || len(statement.Capabilities) > 8 {
			return fmt.Errorf("invalid workspace counters")
		}
		if !statement.Available {
			if statement.GitHubRepository != "" || statement.HeadSHA != "" || statement.Branch != "" || statement.TrackingBranch != "" || statement.Clean || statement.ChangeCount != 0 || statement.LocalAhead != 0 || statement.RemoteAhead != 0 {
				return fmt.Errorf("unavailable workspace included git state")
			}
			continue
		}
		repository := strings.ToLower(strings.TrimSpace(statement.GitHubRepository))
		if !githubRepositoryPattern.MatchString(repository) || !gitCommitSHA.MatchString(strings.ToLower(strings.TrimSpace(statement.HeadSHA))) || !validWorkspaceAttestationBranch(statement.Branch) || (statement.TrackingBranch != "" && !validWorkspaceAttestationBranch(statement.TrackingBranch)) {
			return fmt.Errorf("invalid workspace git identity")
		}
		if !statement.Clean && statement.ChangeCount == 0 {
			return fmt.Errorf("dirty workspace missing change count")
		}
		if statement.Clean && statement.ChangeCount != 0 {
			return fmt.Errorf("clean workspace has changes")
		}
		capabilities := make(map[string]struct{}, len(statement.Capabilities))
		for _, capability := range statement.Capabilities {
			if _, supported := map[string]struct{}{
				automationagent.WorkspaceCapabilityReadRepository: {}, automationagent.WorkspaceCapabilityFetchRemote: {}, automationagent.WorkspaceCapabilityCreateWorktree: {}, automationagent.WorkspaceCapabilityApplyPatch: {}, automationagent.WorkspaceCapabilityStageCommit: {}, automationagent.WorkspaceCapabilityPublishBranch: {}, automationagent.WorkspaceCapabilityCreatePullReq: {},
			}[capability]; !supported {
				return fmt.Errorf("unsupported workspace capability")
			}
			if _, duplicate := capabilities[capability]; duplicate {
				return fmt.Errorf("duplicate workspace capability")
			}
			capabilities[capability] = struct{}{}
		}
		if _, readable := capabilities[automationagent.WorkspaceCapabilityReadRepository]; !readable {
			return fmt.Errorf("available workspace is not readable")
		}
	}
	return nil
}

func validWorkspaceAttestationBranch(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value == strings.TrimSpace(value) && workspaceAttestationBranchPattern.MatchString(value) && !strings.Contains(value, "..") && !strings.Contains(value, "//") && !strings.HasSuffix(value, ".") && !strings.HasSuffix(value, "/")
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
	ProgressStep       string                         `json:"progress_step"`
	ProgressCall       int                            `json:"progress_call"`
	Status             string                         `json:"status"`
	RunID              string                         `json:"run_id"`
	WorkerID           string                         `json:"worker_id"`
	AgentKey           string                         `json:"agent_key"`
	MachineID          string                         `json:"machine_id"`
	ExecutionIdentity  *automationagent.AgentIdentity `json:"execution_identity"`
	RecoveryRunID      string                         `json:"recovery_run_id"`
	RequestRef         string                         `json:"request_ref"`
	OutputRef          string                         `json:"output_ref"`
	ErrorMessage       string                         `json:"error_message"`
	Provider           string                         `json:"provider"`
	Model              string                         `json:"model"`
	CallID             string                         `json:"call_id"`
	ReceiptID          string                         `json:"receipt_id"`
	ProviderResponseID string                         `json:"provider_response_id"`
	Usage              json.RawMessage                `json:"usage"`
	Artifacts          []callbackArtifact             `json:"artifacts"`
	ToolExecutions     []callbackToolExecution        `json:"tool_executions"`
	Execution          json.RawMessage                `json:"execution"`
	Deterministic      bool                           `json:"deterministic"`
}

// callbackToolExecution is an independently billable model call from a
// pinned runtime tool. It carries only usage and private object references;
// the controller derives all financial fields from its own price catalog.
type callbackToolExecution struct {
	Tool        string          `json:"tool"`
	CallKey     string          `json:"call_key"`
	CallID      string          `json:"call_id"`
	ReceiptID   string          `json:"receipt_id"`
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

type planIntegrationFanInProof struct {
	SchemaVersion      int    `json:"schema_version"`
	ParentTaskID       string `json:"parent_task_id"`
	ExecutionID        string `json:"execution_id"`
	PlanID             string `json:"plan_id"`
	PlanVersion        int    `json:"plan_version"`
	PlanHash           string `json:"plan_hash"`
	IntegrationStepID  string `json:"integration_step_id"`
	IntegrationStepKey string `json:"integration_step_key"`
	IntegrationTaskID  string `json:"integration_task_id"`
	RunID              string `json:"run_id"`
	FencingToken       int64  `json:"fencing_token"`
	WorkerID           string `json:"worker_id"`
	AgentKey           string `json:"agent_key"`
	MachineID          string `json:"machine_id"`
}

func Complete(c echo.Context) error {
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
	request.OutputRef = strings.TrimSpace(request.OutputRef)
	_, ok := bindCallbackProfileIdentity(c, &request.AgentKey, &request.MachineID)
	if !ok {
		return nil
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
	if roleHeader, laneHeader := strings.TrimSpace(c.Request().Header.Get("X-Agent-Role")), strings.TrimSpace(c.Request().Header.Get("X-Agent-Lane")); roleHeader != "" || laneHeader != "" {
		assignment, ok := agentwork.AssignmentForOperation(task.Operation)
		if !ok || roleHeader != string(assignment.Role) || laneHeader != string(assignment.Lane) {
			return utils.Error(c, http.StatusForbidden, "Worker identity does not own this task", "")
		}
	}
	request.RunID = strings.TrimSpace(request.RunID)
	request.RecoveryRunID = strings.TrimSpace(request.RecoveryRunID)
	if request.Status == "running" {
		if _, err := uuid.FromString(request.RunID); err != nil || request.RunID == "" {
			return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "running tasks require a valid run lease ID")
		}
		return claimAutomationTaskRun(c, id, request.RunID, request)
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
	ledgerIdentity, identityErr := ledgerAgentIdentity(&task, request)
	if identityErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "worker identity is invalid for this run")
	}
	if request.Status == "completed" && task.Status == "completed" {
		replayed, replayErr := verifiedPlanFanInCompletionReplay(configuration.DB, cfg, task, request, ledgerRunID, ledgerIdentity)
		if replayErr == nil && replayed {
			return c.NoContent(http.StatusNoContent)
		}
		return utils.Error(c, http.StatusConflict, "Automation result ignored", "completed task callback does not match a committed verified plan fan-in")
	}
	cancellationRequested := task.Status == "cancel_requested" && request.RunID != "" && task.RunID == request.RunID
	if request.RunID == "" || (task.Status != "running" && !cancellationRequested) || task.RunID != request.RunID {
		return utils.Error(c, http.StatusConflict, "Automation result ignored", "Task is not held by this worker run")
	}
	request.CallID = strings.TrimSpace(request.CallID)
	request.ReceiptID = strings.TrimSpace(request.ReceiptID)
	var primaryReceipt *models.AutomationInferenceReceipt
	if request.ReceiptID != "" {
		receipt, receiptErr := resolveAutomationInferenceReceipt(configuration.DB, request.ReceiptID, request.CallID, task.ID, ledgerRunID, ledgerIdentity)
		if receiptErr != nil || !inferenceReceiptStatusAllowsCallback(receipt, request.Status) {
			return utils.Error(c, http.StatusBadRequest, "Invalid inference receipt", "receipt must match this task, run, call and authenticated worker identity")
		}
		primaryReceipt = &receipt
	} else if hasCallbackProviderAccounting(request.Provider, request.Model, request.Usage, request.ProviderResponseID) {
		return utils.Error(c, http.StatusBadRequest, "Invalid inference receipt", "provider accounting requires a gateway receipt")
	}
	if request.Deterministic && primaryReceipt != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "deterministic tasks cannot report an inference receipt")
	}
	if request.Status == "completed" && !request.Deterministic && primaryReceipt == nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid inference receipt", "completed model tasks require an accepted gateway receipt")
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
		if request.Status != "completed" || (task.Operation != "code.review" && task.Operation != "delivery.implementation" && task.Operation != "delivery.assessment" && task.Operation != "delivery.onboarding_probe" && task.Operation != "delivery.publish" && task.Operation != "delivery.release_gate" && task.Operation != "delivery.qa") || len(request.Execution) > limit || !json.Valid(request.Execution) {
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
	primaryReceiptID := uuid.Nil
	if primaryReceipt != nil {
		primaryReceiptID = primaryReceipt.ID
	}
	toolExecutionRows, toolExecutionErr := buildVerifiedToolExecutionLedger(configuration.DB, cfg, &task, ledgerRunID, request.Status, request.ToolExecutions, request.Artifacts, ledgerIdentity, primaryReceiptID, time.Now().UTC())
	if toolExecutionErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation tool execution", toolExecutionErr.Error())
	}
	for index := range toolExecutionRows {
		toolExecutionRows[index].WorkerID = ledgerIdentity.WorkerID
		toolExecutionRows[index].AgentKey = ledgerIdentity.AgentKey
		toolExecutionRows[index].MachineID = ledgerIdentity.MachineID
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
		providerCallReported := primaryReceipt != nil
		if (request.Status == "completed" || (request.Status == "failed" && providerCallReported)) && !request.Deterministic {
			// New workers store the canonical provider request before making a
			// billable call. The empty fallback supports records produced before
			// that immutable execution audit object existed.
			if request.RequestRef != "" && !executionRequestReferenceMatches(cfg, task.ID, ledgerRunID, request.RequestRef) {
				return utils.Error(c, http.StatusBadRequest, "Invalid automation result", "request_ref must be this execution's private request object")
			}
			if primaryReceipt == nil {
				return utils.Error(c, http.StatusBadRequest, "Invalid inference receipt", "billable provider outcomes require a gateway receipt")
			}
			updates["provider"] = primaryReceipt.Provider
			updates["model"] = primaryReceipt.Model
			updates["provider_response_id"] = primaryReceipt.ProviderResponseID
			updates["usage_json"] = primaryReceipt.UsageJSON
			requestReference := task.InputRef
			if request.RequestRef != "" {
				requestReference = request.RequestRef
			}
			execution = &models.AutomationExecution{
				AutomationTaskID: task.ID, DeliveryWorkItemID: task.DeliveryWorkItemID, RunID: ledgerRunID, StepKey: executionStepKey(task.Operation),
				InferenceReceiptID: &primaryReceipt.ID,
				WorkerID:           ledgerIdentity.WorkerID, AgentKey: ledgerIdentity.AgentKey, MachineID: ledgerIdentity.MachineID,
				Provider: primaryReceipt.Provider, Model: primaryReceipt.Model, ProviderResponseID: primaryReceipt.ProviderResponseID,
				InputTokens: primaryReceipt.InputTokens, OutputTokens: primaryReceipt.OutputTokens, CachedInputTokens: primaryReceipt.CachedInputTokens,
				CacheWriteTokens: primaryReceipt.CacheWriteTokens, ReasoningTokens: primaryReceipt.ReasoningTokens, TotalTokens: primaryReceipt.TotalTokens,
				InputCostMicros: primaryReceipt.InputCostMicros, OutputCostMicros: primaryReceipt.OutputCostMicros, CachedCostMicros: primaryReceipt.CachedCostMicros,
				CacheWriteCostMicros: primaryReceipt.CacheWriteCostMicros, TotalCostMicros: primaryReceipt.TotalCostMicros, Currency: primaryReceipt.Currency,
				PricingBasis: primaryReceipt.PricingBasis, PricingSnapshotJSON: primaryReceipt.PricingSnapshotJSON, UsageJSON: primaryReceipt.UsageJSON,
				RequestRef: requestReference, ResponseRef: strings.TrimSpace(request.OutputRef), CompletedAt: completedAt,
			}
		}
	}
	if !attributeAutomationCostRowsToCallbackIdentity(c, execution, toolExecutionRows) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	rowsAffected := int64(0)
	callbackContext := configuration.WithConfig(c.Request().Context(), cfg)
	err = configuration.DB.WithContext(callbackContext).Transaction(func(tx *gorm.DB) error {
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
				var assignedChildCount int64
				if err := tx.Model(&models.DeliveryPlanStepAssignment{}).Where("child_automation_task_id = ?", task.ID).Count(&assignedChildCount).Error; err != nil {
					return err
				}
				if assignedChildCount == 0 {
					if err := persistImplementationChangeSet(tx, &task, request.Execution, completedAt); err != nil {
						return err
					}
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
		if !cancellationRequested && request.Status == "completed" && len(request.Execution) > 0 {
			if err := advanceDelegatedDeliverySubmission(tx, &task, completedAt); err != nil {
				return err
			}
		}
		if task.Operation == "delivery.implementation" && task.DeliveryWorkItemID != nil {
			if err := reconcilePlanStepChildTerminalInTransaction(tx, task, request, cancellationRequested, completedAt, ledgerRunID, ledgerIdentity, cfg); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Automation result failed", "")
	}
	if rowsAffected == 0 {
		if request.Status == "completed" {
			var currentTask models.AutomationTask
			if reloadErr := configuration.DB.First(&currentTask, id).Error; reloadErr == nil && currentTask.Status == "completed" {
				currentIdentity, identityErr := ledgerAgentIdentity(&currentTask, request)
				if identityErr == nil {
					replayed, replayErr := verifiedPlanFanInCompletionReplay(configuration.DB, cfg, currentTask, request, ledgerRunID, currentIdentity)
					if replayErr == nil && replayed {
						return c.NoContent(http.StatusNoContent)
					}
				}
			}
		}
		return utils.Error(c, http.StatusConflict, "Automation result ignored", "Task is not awaiting a result")
	}
	return c.NoContent(http.StatusNoContent)
}

// verifiedPlanFanInCompletionReplay acknowledges a lost callback response only
// when the previously committed parent result carries the exact integration
// receipt and the child, assignment, step, execution, identities, and immutable
// run references still agree. It is read-only and never creates another cost
// row or advances a workflow gate.
func verifiedPlanFanInCompletionReplay(
	db *gorm.DB,
	cfg *models.Config,
	child models.AutomationTask,
	request callbackRequest,
	ledgerRunID string,
	identity automationagent.AgentIdentity,
) (bool, error) {
	if db == nil || cfg == nil || child.Status != "completed" || child.Operation != "delivery.implementation" || child.DeliveryWorkItemID == nil ||
		request.Status != "completed" || request.RunID == "" || child.RunID != request.RunID || request.OutputRef == "" || child.OutputRef != request.OutputRef ||
		ledgerRunID == "" || !outputReferenceMatches(cfg, child.ID, request.OutputRef) ||
		child.WorkerID != identity.WorkerID || child.AgentKey != identity.AgentKey || child.MachineID != identity.MachineID {
		return false, nil
	}
	var handoff struct {
		FanInReceipt *planIntegrationCallbackReceipt `json:"fan_in_receipt"`
	}
	if len(request.Execution) == 0 || len(request.Execution) > 32*1024 || json.Unmarshal(request.Execution, &handoff) != nil || handoff.FanInReceipt == nil {
		return false, nil
	}
	var assignment models.DeliveryPlanStepAssignment
	if err := db.Where("child_automation_task_id = ?", child.ID).Take(&assignment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if assignment.ChildAutomationTaskID != child.ID || assignment.Status != models.DeliveryPlanStepAssignmentCompleted {
		return false, nil
	}
	var execution models.DeliveryPlanExecution
	if err := db.Where("id = ?", assignment.ExecutionID).Take(&execution).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if execution.Status != models.DeliveryPlanExecutionCompleted || execution.PlanID == uuid.Nil || execution.PlanVersion < 1 ||
		!artifactDigestPattern.MatchString(execution.PlanHash) {
		return false, nil
	}
	var parent models.AutomationTask
	if err := db.Where("id = ?", execution.AutomationTaskID).Take(&parent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if parent.Status != "completed" || parent.Operation != "delivery.implementation" || parent.DeliveryWorkItemID == nil || *parent.DeliveryWorkItemID != *child.DeliveryWorkItemID ||
		!executionResultReferenceMatches(cfg, parent.ID, ledgerRunID, parent.OutputRef) {
		return false, nil
	}
	var step models.DeliveryPlanStep
	if err := db.Where("id = ? AND plan_id = ?", assignment.DeliveryPlanStepID, execution.PlanID).Take(&step).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if step.Role != models.DeliveryPlanStepRoleIntegration || step.AutomationTaskID == nil || *step.AutomationTaskID != child.ID || step.RunID != ledgerRunID || step.LeaseFence < 1 ||
		step.WorkerID != identity.WorkerID || step.AgentKey != identity.AgentKey || step.MachineID != identity.MachineID {
		return false, nil
	}
	receipt := handoff.FanInReceipt
	if receipt.ParentTaskID != parent.ID.String() || receipt.PlanID != execution.PlanID.String() || receipt.PlanVersion != execution.PlanVersion || receipt.PlanHash != execution.PlanHash ||
		receipt.StepID != step.ID.String() || receipt.ChildTaskID != child.ID.String() || receipt.RunID != ledgerRunID {
		return false, nil
	}
	bucket, key, err := privateReference(parent.OutputRef)
	if err != nil {
		return false, nil
	}
	object, err := getPlanStepPatchObject(db.Statement.Context, key, bucket)
	if err != nil {
		return false, err
	}
	content, readErr := io.ReadAll(io.LimitReader(object, 1<<20+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil || len(content) == 0 || len(content) > 1<<20 {
		return false, fmt.Errorf("read committed plan fan-in replay proof")
	}
	var output struct {
		TaskID             string                          `json:"task_id"`
		IntegrationTaskID  string                          `json:"integration_task_id"`
		RunID              string                          `json:"run_id"`
		IntegrationReceipt *planIntegrationCallbackReceipt `json:"integration_receipt"`
		Execution          struct {
			FanInReceipt *planIntegrationCallbackReceipt `json:"fan_in_receipt"`
		} `json:"execution"`
		FanIn planIntegrationFanInProof `json:"fan_in"`
	}
	if json.Unmarshal(content, &output) != nil || output.TaskID != parent.ID.String() || output.IntegrationTaskID != child.ID.String() || output.RunID != ledgerRunID ||
		output.IntegrationReceipt == nil || *output.IntegrationReceipt != *receipt || output.Execution.FanInReceipt == nil || *output.Execution.FanInReceipt != *receipt {
		return false, nil
	}
	proof := output.FanIn
	if proof.SchemaVersion != 1 || proof.ParentTaskID != parent.ID.String() || proof.ExecutionID != execution.ID.String() || proof.PlanID != execution.PlanID.String() ||
		proof.PlanVersion != execution.PlanVersion || proof.PlanHash != execution.PlanHash || proof.IntegrationStepID != step.ID.String() || proof.IntegrationStepKey != step.StepKey ||
		proof.IntegrationTaskID != child.ID.String() || proof.RunID != ledgerRunID || proof.FencingToken != step.LeaseFence ||
		proof.WorkerID != identity.WorkerID || proof.AgentKey != identity.AgentKey || proof.MachineID != identity.MachineID {
		return false, nil
	}
	return true, nil
}

// reconcilePlanStepChildTerminalInTransaction is the only fan-in point for
// implementation child tasks. It may dispatch more independent roots, but it
// never releases dependencies or promotes a parent result: child worktrees are
// isolated and no verified merge artifact exists yet.
func reconcilePlanStepChildTerminalInTransaction(tx *gorm.DB, child models.AutomationTask, request callbackRequest, cancelled bool, now time.Time, ledgerRunID string, identity automationagent.AgentIdentity, cfg *models.Config) error {
	if tx == nil || child.ID == uuid.Nil || child.DeliveryWorkItemID == nil {
		return fmt.Errorf("plan-step child callback is missing its task scope")
	}
	var assignmentIdentity models.DeliveryPlanStepAssignment
	err := tx.Select("id", "execution_id").Where("child_automation_task_id = ?", child.ID).Take(&assignmentIdentity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Legacy sequential implementation tasks have no execution assignment.
		return nil
	}
	if err != nil {
		return err
	}
	var executionIdentity models.DeliveryPlanExecution
	if err := tx.Select("id", "automation_task_id").First(&executionIdentity, "id = ?", assignmentIdentity.ExecutionID).Error; err != nil {
		return err
	}
	// Keep the same parent -> execution -> assignment lock order as the
	// reservation/scheduler service to avoid fan-in deadlocks under parallel
	// child callbacks.
	var parent models.AutomationTask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "id = ?", executionIdentity.AutomationTaskID).Error; err != nil {
		return err
	}
	var execution models.DeliveryPlanExecution
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", executionIdentity.ID).Error; err != nil {
		return err
	}
	var assignment models.DeliveryPlanStepAssignment
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND execution_id = ? AND child_automation_task_id = ?", assignmentIdentity.ID, execution.ID, child.ID).Take(&assignment).Error; err != nil {
		return err
	}
	if parent.Operation != "delivery.implementation" || parent.DeliveryWorkItemID == nil || *parent.DeliveryWorkItemID != *child.DeliveryWorkItemID || parent.Status != "queued" {
		return fmt.Errorf("plan-step execution parent no longer matches its queued implementation task")
	}
	var item models.DeliveryWorkItem
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, "id = ?", *parent.DeliveryWorkItemID).Error; err != nil {
		return err
	}
	if item.ProjectID == uuid.Nil {
		return fmt.Errorf("plan-step execution work item has no project scope")
	}

	// A successful AutomationTask callback is not proof that its fenced plan
	// step transition committed. Require that second durable signal; otherwise
	// stop fan-out and audit the inconsistency as blocked aggregation.
	if cancelled || request.Status == "failed" {
		terminalAssignment := models.DeliveryPlanStepAssignmentFailed
		if cancelled {
			terminalAssignment = models.DeliveryPlanStepAssignmentCancelled
		}
		if assignment.Status != models.DeliveryPlanStepAssignmentCompleted && assignment.Status != models.DeliveryPlanStepAssignmentFailed && assignment.Status != models.DeliveryPlanStepAssignmentBlocked && assignment.Status != models.DeliveryPlanStepAssignmentCancelled {
			if err := tx.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ? AND status IN ?", assignment.ID, activePlanStepAssignmentStatuses()).Updates(map[string]any{
				"status": terminalAssignment, "completed_at": now, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			assignment.Status = terminalAssignment
		}
	} else if request.Status == "completed" && assignment.Status != models.DeliveryPlanStepAssignmentCompleted {
		if assignment.Status == models.DeliveryPlanStepAssignmentPending || assignment.Status == models.DeliveryPlanStepAssignmentQueued || assignment.Status == models.DeliveryPlanStepAssignmentDispatched || assignment.Status == models.DeliveryPlanStepAssignmentRunning {
			if err := tx.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ? AND status IN ?", assignment.ID, activePlanStepAssignmentStatuses()).Updates(map[string]any{
				"status": models.DeliveryPlanStepAssignmentBlocked, "completed_at": now, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			assignment.Status = models.DeliveryPlanStepAssignmentBlocked
		}
	}

	var plan models.DeliveryPlan
	if err := tx.First(&plan, "id = ? AND work_item_id = ?", execution.PlanID, item.ID).Error; err != nil {
		return err
	}
	var stepRows []models.DeliveryPlanStep
	if err := tx.Where("plan_id = ?", plan.ID).Order("display_order ASC, id ASC").Find(&stepRows).Error; err != nil {
		return err
	}
	if len(stepRows) == 0 {
		return fmt.Errorf("frozen plan has no steps for fan-in")
	}
	stepIDs := make([]uuid.UUID, 0, len(stepRows))
	for _, step := range stepRows {
		stepIDs = append(stepIDs, step.ID)
	}

	var allAssignments []models.DeliveryPlanStepAssignment
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("execution_id = ?", execution.ID).Find(&allAssignments).Error; err != nil {
		return err
	}
	assessmentAssignments, err := persistAndOverlayPlanChildTaskStates(tx, allAssignments, now)
	if err != nil {
		return err
	}
	beforeScheduling := deliveryplansteps.AssessFanIn(stepIDs, assessmentAssignments)
	machineBlockReason := ""
	if !beforeScheduling.HasChildFailure {
		if _, err := enqueueReadyPlanStepsInTransaction(tx, execution.ID, now); err != nil {
			if !errors.Is(err, deliveryplansteps.ErrNoEligibleAgentMachine) {
				return fmt.Errorf("dispatch next ready plan steps: %w", err)
			}
			machineBlockReason = retryableMachineDispatchReason(err)
			if err := tx.Model(&models.DeliveryWorkItem{}).Where("id = ?", item.ID).Updates(map[string]any{
				"agent_progress": "blocked", "blocked_reason": machineBlockReason, "updated_at": now,
			}).Error; err != nil {
				return err
			}
		}
	}
	if err := tx.Where("execution_id = ?", execution.ID).Find(&allAssignments).Error; err != nil {
		return err
	}
	assessmentAssignments, err = persistAndOverlayPlanChildTaskStates(tx, allAssignments, now)
	if err != nil {
		return err
	}
	decision := deliveryplansteps.AssessFanIn(stepIDs, assessmentAssignments)
	if machineBlockReason != "" {
		// Keep the parent task and approved work item retryable. A continuation
		// polls for a new heartbeat and repeats the same server-side selection;
		// no unassigned child is emitted into the queue.
		if parent.ContinuationID != nil {
			return tx.Model(&models.DeliveryContinuation{}).Where("id = ? AND status IN ?", *parent.ContinuationID, []string{"dispatched", "claimed"}).Updates(map[string]any{
				"status": "dispatched", "available_at": now.Add(time.Minute), "updated_at": now,
			}).Error
		}
		return nil
	}
	if decision.WaitingForChildren {
		// Preserve the parent reservation while any targeted child can still
		// consume the aggregate budget. Continuation polling stays observational.
		if parent.ContinuationID != nil {
			return tx.Model(&models.DeliveryContinuation{}).Where("id = ? AND status IN ?", *parent.ContinuationID, []string{"dispatched", "claimed"}).Updates(map[string]any{
				"status": "dispatched", "available_at": now.Add(time.Minute), "updated_at": now,
			}).Error
		}
		return nil
	}
	if !decision.AggregationPending {
		return fmt.Errorf("fan-in returned an unsupported non-terminal decision")
	}
	if !decision.HasChildFailure && request.Status == "completed" {
		completed, reason, err := completeVerifiedPlanFanInInTransaction(
			tx, parent, child, item, execution, plan, stepRows, allAssignments,
			request, ledgerRunID, identity, cfg, now,
		)
		if err != nil {
			return err
		}
		if completed {
			return nil
		}
		if reason != "" {
			return persistPlanAggregationPendingInTransaction(tx, parent, item, execution, reason, now)
		}
	}
	return persistPlanAggregationPendingInTransaction(tx, parent, item, execution, decision.Reason, now)
}

type planIntegrationCallbackReceipt struct {
	ParentTaskID string `json:"parent_task_id"`
	PlanID       string `json:"plan_id"`
	PlanVersion  int    `json:"plan_version"`
	PlanHash     string `json:"plan_hash"`
	StepID       string `json:"step_id"`
	ChildTaskID  string `json:"child_task_id"`
	RunID        string `json:"run_id"`
}

type planIntegrationOutputEvidence struct {
	PlanID             string                       `json:"plan_id"`
	PlanVersion        int                          `json:"plan_version"`
	StepID             string                       `json:"step_id"`
	StepKey            string                       `json:"step_key"`
	AcceptanceCriteria []string                     `json:"acceptance_criteria"`
	Checks             []planIntegrationOutputCheck `json:"checks"`
	VerifiedAt         time.Time                    `json:"verified_at"`
}

type planIntegrationOutputCheck struct {
	Criterion string `json:"criterion"`
	Passed    bool   `json:"passed"`
}

// completeVerifiedPlanFanInInTransaction is the only successful aggregate
// transition. It verifies the frozen integration child, its exact lease-run
// evidence and every repository artifact before promoting the parent.
func completeVerifiedPlanFanInInTransaction(
	tx *gorm.DB,
	parent, child models.AutomationTask,
	item models.DeliveryWorkItem,
	execution models.DeliveryPlanExecution,
	plan models.DeliveryPlan,
	steps []models.DeliveryPlanStep,
	assignments []models.DeliveryPlanStepAssignment,
	request callbackRequest,
	ledgerRunID string,
	identity automationagent.AgentIdentity,
	cfg *models.Config,
	now time.Time,
) (bool, string, error) {
	if tx == nil || child.DeliveryWorkItemID == nil || *child.DeliveryWorkItemID != item.ID || len(steps) == 0 {
		return false, deliveryplansteps.AggregationPendingReason + ": frozen plan scope is missing", nil
	}
	var integration *models.DeliveryPlanStep
	for index := range steps {
		if steps[index].Role != models.DeliveryPlanStepRoleIntegration {
			continue
		}
		if integration != nil {
			return false, deliveryplansteps.AggregationPendingReason + ": frozen plan has multiple integration nodes", nil
		}
		integration = &steps[index]
	}
	if integration == nil || integration.AutomationTaskID == nil || *integration.AutomationTaskID != child.ID {
		return false, "", nil
	}
	if request.Status != "completed" || strings.TrimSpace(ledgerRunID) == "" || cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" {
		return false, deliveryplansteps.AggregationPendingReason + ": integration callback scope is incomplete", nil
	}
	if integration.Status != models.DeliveryPlanStepCompleted || integration.RunID != ledgerRunID || integration.LeaseFence < 1 || integration.WorkerID != identity.WorkerID || integration.AgentKey != identity.AgentKey || integration.MachineID != identity.MachineID {
		return false, deliveryplansteps.AggregationPendingReason + ": integration step completion does not match its callback fence", nil
	}
	var integrationAssignment *models.DeliveryPlanStepAssignment
	for index := range assignments {
		if assignments[index].DeliveryPlanStepID == integration.ID {
			if integrationAssignment != nil {
				return false, deliveryplansteps.AggregationPendingReason + ": integration step has duplicate assignments", nil
			}
			integrationAssignment = &assignments[index]
		}
	}
	if integrationAssignment == nil || integrationAssignment.ChildAutomationTaskID != child.ID || integrationAssignment.Status != models.DeliveryPlanStepAssignmentCompleted {
		return false, deliveryplansteps.AggregationPendingReason + ": integration assignment is not terminal", nil
	}

	var dependencies []models.DeliveryPlanStepDependency
	if err := tx.Where("plan_id = ?", plan.ID).Order("step_id ASC, depends_on_step_id ASC").Find(&dependencies).Error; err != nil {
		return false, "", err
	}
	planHash, err := deliveryplansteps.ApprovedPlanContentHash(plan, steps, dependencies)
	if err != nil || planHash != execution.PlanHash || execution.PlanID != plan.ID || execution.PlanVersion != plan.Version || execution.AutomationTaskID != parent.ID {
		return false, deliveryplansteps.AggregationPendingReason + ": frozen plan hash no longer matches its execution", nil
	}
	var handoff struct {
		FanInReceipt *planIntegrationCallbackReceipt `json:"fan_in_receipt"`
	}
	if len(request.Execution) == 0 || len(request.Execution) > 32*1024 || json.Unmarshal(request.Execution, &handoff) != nil || handoff.FanInReceipt == nil {
		return false, deliveryplansteps.AggregationPendingReason + ": integration callback omitted its frozen-plan receipt", nil
	}
	receipt := handoff.FanInReceipt
	if receipt.ParentTaskID != parent.ID.String() || receipt.PlanID != plan.ID.String() || receipt.PlanVersion != plan.Version || receipt.PlanHash != execution.PlanHash || receipt.StepID != integration.ID.String() || receipt.ChildTaskID != child.ID.String() || receipt.RunID != ledgerRunID {
		return false, deliveryplansteps.AggregationPendingReason + ": integration receipt does not match the current parent, plan, child, and run", nil
	}

	var gatePlan struct {
		RepositoryImpact []struct {
			Reference string `json:"reference"`
			Impact    string `json:"impact"`
		} `json:"repository_impact"`
	}
	if json.Unmarshal([]byte(plan.StructuredJSON), &gatePlan) != nil {
		return false, deliveryplansteps.AggregationPendingReason + ": approved repository impact is invalid", nil
	}
	expectedRepositories := make(map[string]struct{})
	for _, repository := range gatePlan.RepositoryImpact {
		if !strings.EqualFold(strings.TrimSpace(repository.Impact), "changes") {
			continue
		}
		reference := strings.TrimSpace(repository.Reference)
		if !strings.HasPrefix(reference, "workspace://") {
			return false, deliveryplansteps.AggregationPendingReason + ": approved changed repository reference is invalid", nil
		}
		if _, duplicate := expectedRepositories[reference]; duplicate {
			return false, deliveryplansteps.AggregationPendingReason + ": approved repository impact contains duplicates", nil
		}
		expectedRepositories[reference] = struct{}{}
	}
	if len(expectedRepositories) == 0 || len(expectedRepositories) > models.MaxDeliveryPlanStepPatchArtifacts {
		return false, deliveryplansteps.AggregationPendingReason + ": approved plan has no bounded changed-repository set", nil
	}

	var events []models.DeliveryPlanStepActivityEvent
	if err := tx.Where(`step_id = ? AND plan_id = ? AND automation_task_id = ? AND run_id = ? AND worker_id = ?
		AND agent_key = ? AND machine_id = ? AND fencing_token = ? AND action = ? AND phase = ?`,
		integration.ID, plan.ID, child.ID, ledgerRunID, identity.WorkerID, identity.AgentKey, identity.MachineID, integration.LeaseFence,
		models.DeliveryPlanStepActivityEvidence, models.DeliveryPlanStepActivityCompleted).Order("sequence DESC").Limit(1).Find(&events).Error; err != nil {
		return false, "", err
	}
	if len(events) != 1 || events[0].AgentInstanceID == nil || *events[0].AgentInstanceID == uuid.Nil {
		return false, deliveryplansteps.AggregationPendingReason + ": integration acceptance event is missing", nil
	}
	if err := requirePlanStepAcceptanceEvidence(tx, *integration, child.ID, ledgerRunID, identity, integration.LeaseFence, *events[0].AgentInstanceID); err != nil {
		return false, deliveryplansteps.AggregationPendingReason + ": integration acceptance or dependency-merge evidence is incomplete", nil
	}
	if err := validatePlanStepActivityAcceptanceCriteria(integration.AcceptanceCriteriaJSON, events[0]); err != nil {
		return false, deliveryplansteps.AggregationPendingReason + ": integration acceptance criteria do not match the frozen step", nil
	}
	var details models.DeliveryPlanStepActivityDetails
	if json.Unmarshal([]byte(events[0].DetailsJSON), &details) != nil || len(details.PatchArtifacts) != len(expectedRepositories) {
		return false, deliveryplansteps.AggregationPendingReason + ": integration artifact manifest is incomplete", nil
	}
	var artifactRows []models.DeliveryPlanStepPatchArtifact
	if err := tx.Where("plan_id = ? AND automation_task_id = ? AND run_id = ? AND step_id = ?", plan.ID, child.ID, ledgerRunID, integration.ID).Order("repository_ref ASC").Find(&artifactRows).Error; err != nil {
		return false, "", err
	}
	if len(artifactRows) != len(expectedRepositories) || len(artifactRows) != len(details.PatchArtifacts) {
		return false, deliveryplansteps.AggregationPendingReason + ": final artifact rows do not cover every changed repository", nil
	}
	refsByRepository := make(map[string]models.DeliveryPlanStepPatchArtifactReference, len(details.PatchArtifacts))
	for _, reference := range details.PatchArtifacts {
		if _, expected := expectedRepositories[reference.RepositoryRef]; !expected {
			return false, deliveryplansteps.AggregationPendingReason + ": artifact manifest contains an unapproved repository", nil
		}
		if _, duplicate := refsByRepository[reference.RepositoryRef]; duplicate || reference.SizeBytes < 1 || reference.SizeBytes > models.MaxDeliveryPlanStepPatchArtifactBytes || !artifactDigestPattern.MatchString(reference.SHA256) || !gitCommitSHA.MatchString(reference.BaseSHA) {
			return false, deliveryplansteps.AggregationPendingReason + ": artifact manifest is malformed", nil
		}
		refsByRepository[reference.RepositoryRef] = reference
	}
	for _, artifact := range artifactRows {
		reference, found := refsByRepository[artifact.RepositoryRef]
		if !found || artifact.PlanID != plan.ID || artifact.AutomationTaskID != child.ID || artifact.RunID != ledgerRunID || artifact.StepID != integration.ID ||
			artifact.WorkerID != identity.WorkerID || artifact.AgentKey != identity.AgentKey || artifact.MachineID != identity.MachineID ||
			artifact.AgentInstanceID == nil || *artifact.AgentInstanceID != *events[0].AgentInstanceID || artifact.FencingToken != integration.LeaseFence ||
			artifact.Bucket != cfg.AutomationOutputBucket || artifact.ObjectKey != planStepPatchObjectKey(child.ID, ledgerRunID, integration.ID, artifact.SHA256) ||
			artifact.BaseSHA != reference.BaseSHA || artifact.SHA256 != reference.SHA256 || artifact.SizeBytes != reference.SizeBytes {
			return false, deliveryplansteps.AggregationPendingReason + ": final repository artifact does not match its authenticated evidence", nil
		}
		object, err := getPlanStepPatchObject(tx.Statement.Context, artifact.ObjectKey, artifact.Bucket)
		if err != nil {
			return false, "", fmt.Errorf("read final integration patch artifact: %w", err)
		}
		patch, readErr := io.ReadAll(io.LimitReader(object, int64(models.MaxDeliveryPlanStepPatchArtifactBytes)+1))
		closeErr := object.Close()
		if readErr != nil || closeErr != nil {
			for index := range patch {
				patch[index] = 0
			}
			return false, "", fmt.Errorf("read final integration patch artifact")
		}
		digest := sha256.Sum256(patch)
		validBytes := int64(len(patch)) == artifact.SizeBytes && hex.EncodeToString(digest[:]) == artifact.SHA256 && validateUnifiedGitPatch(patch) == nil && !containsHighConfidencePatchSecret(patch)
		for index := range patch {
			patch[index] = 0
		}
		if !validBytes {
			return false, deliveryplansteps.AggregationPendingReason + ": final integration patch bytes no longer match their immutable reference", nil
		}
	}
	if manifestSHA256, manifestErr := models.DeliveryPlanStepPatchArtifactManifestSHA256(details.PatchArtifacts); manifestErr != nil || details.ReviewDiffSHA256 != manifestSHA256 {
		return false, deliveryplansteps.AggregationPendingReason + ": integration artifact manifest digest is inconsistent", nil
	}

	if !executionResultReferenceMatches(cfg, child.ID, ledgerRunID, request.OutputRef) {
		return false, deliveryplansteps.AggregationPendingReason + ": integration result reference is not bound to its child run", nil
	}
	bucket, key, err := privateReference(request.OutputRef)
	if err != nil {
		return false, deliveryplansteps.AggregationPendingReason + ": integration result reference is invalid", nil
	}
	body, err := awsrepository.GetS3Object(tx.Statement.Context, key, bucket)
	if err != nil {
		return false, "", fmt.Errorf("read integration result for fan-in: %w", err)
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, 1<<20+1))
	if err != nil {
		return false, "", fmt.Errorf("read integration result for fan-in: %w", err)
	}
	if len(content) == 0 || len(content) > 1<<20 {
		return false, deliveryplansteps.AggregationPendingReason + ": integration result is empty or oversized", nil
	}
	var output struct {
		TaskID             string                          `json:"task_id"`
		RunID              string                          `json:"run_id"`
		PlanID             string                          `json:"plan_id"`
		TargetPlanStepID   string                          `json:"target_plan_step_id"`
		CompletedPlanSteps []string                        `json:"completed_plan_steps"`
		PlanStepEvidence   []planIntegrationOutputEvidence `json:"plan_step_evidence"`
		IntegrationReceipt *planIntegrationCallbackReceipt `json:"integration_receipt"`
		Execution          struct {
			FanInReceipt *planIntegrationCallbackReceipt `json:"fan_in_receipt"`
		} `json:"execution"`
	}
	if json.Unmarshal(content, &output) != nil || output.TaskID != child.ID.String() || output.RunID != ledgerRunID || output.PlanID != plan.ID.String() || output.TargetPlanStepID != integration.ID.String() || !containsAutomationString(output.CompletedPlanSteps, integration.StepKey) || output.IntegrationReceipt == nil || *output.IntegrationReceipt != *receipt || output.Execution.FanInReceipt == nil || *output.Execution.FanInReceipt != *receipt {
		return false, deliveryplansteps.AggregationPendingReason + ": integration result does not match its receipt", nil
	}
	criteria := []string{}
	if json.Unmarshal([]byte(integration.AcceptanceCriteriaJSON), &criteria) != nil || len(criteria) == 0 || len(output.PlanStepEvidence) != 1 {
		return false, deliveryplansteps.AggregationPendingReason + ": integration output omitted final acceptance evidence", nil
	}
	outputEvidence := output.PlanStepEvidence[0]
	if outputEvidence.PlanID != plan.ID.String() || outputEvidence.PlanVersion != plan.Version || outputEvidence.StepID != integration.ID.String() || outputEvidence.StepKey != integration.StepKey || !sameAutomationStrings(outputEvidence.AcceptanceCriteria, criteria) || outputEvidence.VerifiedAt.IsZero() || !validPlanIntegrationOutputChecks(criteria, outputEvidence.Checks) {
		return false, deliveryplansteps.AggregationPendingReason + ": integration output acceptance evidence is not exact", nil
	}

	parentResult := map[string]json.RawMessage{}
	if json.Unmarshal(content, &parentResult) != nil {
		return false, deliveryplansteps.AggregationPendingReason + ": integration result JSON is invalid", nil
	}
	parentResult["task_id"], _ = json.Marshal(parent.ID.String())
	parentResult["integration_task_id"], _ = json.Marshal(child.ID.String())
	parentResult["plan_hash"], _ = json.Marshal(execution.PlanHash)
	parentResult["plan_execution_id"], _ = json.Marshal(execution.ID.String())
	proof := map[string]any{
		"schema_version": 1, "parent_task_id": parent.ID.String(), "execution_id": execution.ID.String(),
		"plan_id": plan.ID.String(), "plan_version": plan.Version, "plan_hash": execution.PlanHash,
		"integration_step_id": integration.ID.String(), "integration_step_key": integration.StepKey,
		"integration_task_id": child.ID.String(), "run_id": integration.RunID, "fencing_token": integration.LeaseFence,
		"worker_id": identity.WorkerID, "agent_key": identity.AgentKey, "machine_id": identity.MachineID,
		"evidence_event_id": events[0].ID.String(), "evidence_sequence": events[0].Sequence,
		"evidence_verified_at": outputEvidence.VerifiedAt.UTC(), "acceptance_checks": details.AcceptanceChecks,
		"applied_dependency_manifest_sha256": details.AppliedDependencyManifestSHA256,
		"applied_dependency_patch_count":     details.AppliedDependencyPatchCount,
		"final_artifacts":                    details.PatchArtifacts,
	}
	parentResult["fan_in"], _ = json.Marshal(proof)
	parentOutput, err := json.Marshal(parentResult)
	if err != nil {
		return false, "", err
	}
	parentKey := "automation/" + parent.ID.String() + "/runs/" + ledgerRunID + "/result.json"
	if err := awsrepository.UploadEncryptedJSON(tx.Statement.Context, parentOutput, parentKey, cfg.AutomationOutputBucket); err != nil {
		return false, "", fmt.Errorf("store verified parent fan-in result: %w", err)
	}
	parent.OutputRef = "s3://" + cfg.AutomationOutputBucket + "/" + parentKey
	changes, err := implementationChangeSetsForHandoff(&parent, request.Execution, now)
	if err != nil || len(changes) != len(expectedRepositories) {
		return false, deliveryplansteps.AggregationPendingReason + ": final review handoff does not cover every repository", nil
	}
	changesByRepository := make(map[string]models.DeliveryChangeSet, len(changes))
	for _, change := range changes {
		if _, duplicate := changesByRepository[change.RepositoryRef]; duplicate || change.CIStatus != "passed" {
			return false, deliveryplansteps.AggregationPendingReason + ": final repository review handoff is duplicated or unverified", nil
		}
		changesByRepository[change.RepositoryRef] = change
	}
	for _, artifact := range artifactRows {
		change, found := changesByRepository[artifact.RepositoryRef]
		var metadata struct {
			ReviewDiffSHA256 string `json:"review_diff_sha256"`
		}
		if !found || json.Unmarshal([]byte(change.MetadataJSON), &metadata) != nil || metadata.ReviewDiffSHA256 != artifact.SHA256 {
			return false, deliveryplansteps.AggregationPendingReason + ": final review handoff does not match its repository artifact", nil
		}
	}
	if err := persistVerifiedPlanFanInChangeSets(tx, changes, now); err != nil {
		return false, "", err
	}
	parentResultUpdate := tx.Model(&models.AutomationTask{}).Where("id = ? AND status = ?", parent.ID, "queued").Updates(map[string]any{
		"status": "completed", "output_ref": parent.OutputRef, "error_message": "", "progress_step": "completed",
		"completed_at": now, "budget_reservation_micros": 0, "budget_reservation_expires_at": nil, "updated_at": now,
	})
	if parentResultUpdate.Error != nil {
		return false, "", parentResultUpdate.Error
	}
	if parentResultUpdate.RowsAffected != 1 {
		return false, "", fmt.Errorf("parent task changed during locked verified fan-in")
	}
	executionUpdate := tx.Model(&models.DeliveryPlanExecution{}).
		Where("id = ? AND automation_task_id = ? AND plan_hash = ? AND status IN ?", execution.ID, parent.ID, execution.PlanHash, []string{models.DeliveryPlanExecutionDispatching, models.DeliveryPlanExecutionRunning}).
		Updates(map[string]any{"status": models.DeliveryPlanExecutionCompleted, "completed_at": now, "updated_at": now})
	if executionUpdate.Error != nil {
		return false, "", executionUpdate.Error
	}
	if executionUpdate.RowsAffected != 1 {
		return false, "", fmt.Errorf("plan execution changed during locked verified fan-in")
	}
	return true, "", nil
}

func sameAutomationStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func containsAutomationString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validPlanIntegrationOutputChecks(criteria []string, checks []planIntegrationOutputCheck) bool {
	if len(criteria) == 0 || len(checks) != len(criteria) {
		return false
	}
	expected := make(map[string]struct{}, len(criteria))
	for _, criterion := range criteria {
		expected[criterion] = struct{}{}
	}
	for _, check := range checks {
		if !check.Passed {
			return false
		}
		if _, found := expected[check.Criterion]; !found {
			return false
		}
		delete(expected, check.Criterion)
	}
	return len(expected) == 0
}

func persistVerifiedPlanFanInChangeSets(tx *gorm.DB, changes []models.DeliveryChangeSet, now time.Time) error {
	for _, change := range changes {
		var existing models.DeliveryChangeSet
		err := tx.Where("work_item_id = ? AND repository_ref = ? AND branch = ?", change.WorkItemID, change.RepositoryRef, change.Branch).First(&existing).Error
		if err == nil {
			if existing.ReviewType != change.ReviewType || existing.CIStatus != change.CIStatus || existing.Environment != change.Environment || existing.MetadataJSON != change.MetadataJSON || existing.CreatedBy != change.CreatedBy {
				return fmt.Errorf("fan-in review record conflicts with existing repository evidence")
			}
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		change.CreatedAt = now
		change.UpdatedAt = now
		if err := tx.Create(&change).Error; err != nil {
			return err
		}
	}
	return nil
}

// Assignment completion is recorded by the fenced plan-step callback before
// the child stores its final output and submits its AutomationTask callback.
// Treat a still-active child row as active even if its step is already marked
// completed, so fan-in cannot release the parent budget or block continuation
// while a sibling still has a terminal callback in flight.
func persistAndOverlayPlanChildTaskStates(tx *gorm.DB, assignments []models.DeliveryPlanStepAssignment, now time.Time) ([]models.DeliveryPlanStepAssignment, error) {
	if len(assignments) == 0 {
		return []models.DeliveryPlanStepAssignment{}, nil
	}
	childIDs := make([]uuid.UUID, 0, len(assignments))
	indexByTaskID := make(map[uuid.UUID]int, len(assignments))
	for index, assignment := range assignments {
		childIDs = append(childIDs, assignment.ChildAutomationTaskID)
		indexByTaskID[assignment.ChildAutomationTaskID] = index
	}
	var tasks []models.AutomationTask
	if err := tx.Select("id", "status").Where("id IN ?", childIDs).Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) != len(assignments) {
		return nil, fmt.Errorf("one or more plan-step child tasks are missing during fan-in")
	}
	result := append([]models.DeliveryPlanStepAssignment(nil), assignments...)
	for _, task := range tasks {
		index, exists := indexByTaskID[task.ID]
		if !exists {
			return nil, fmt.Errorf("fan-in returned a task outside the assignment set")
		}
		status, err := effectivePlanStepAssignmentStatus(result[index].Status, task.Status)
		if err != nil {
			return nil, err
		}
		if status != result[index].Status && (task.Status == "failed" || task.Status == "cancelled" || (task.Status == "completed" && status == models.DeliveryPlanStepAssignmentBlocked)) {
			update := tx.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ?", result[index].ID).Updates(map[string]any{
				"status": status, "completed_at": now, "updated_at": now,
			})
			if update.Error != nil {
				return nil, update.Error
			}
			if update.RowsAffected != 1 {
				return nil, fmt.Errorf("child task terminal state could not be synchronized to its assignment")
			}
		}
		result[index].Status = status
	}
	return result, nil
}

func effectivePlanStepAssignmentStatus(assignmentStatus, childTaskStatus string) (string, error) {
	switch childTaskStatus {
	case "queued", "running", "cancel_requested":
		return models.DeliveryPlanStepAssignmentRunning, nil
	case "failed":
		return models.DeliveryPlanStepAssignmentFailed, nil
	case "cancelled":
		return models.DeliveryPlanStepAssignmentCancelled, nil
	case "completed":
		if assignmentStatus == models.DeliveryPlanStepAssignmentPending || assignmentStatus == models.DeliveryPlanStepAssignmentQueued || assignmentStatus == models.DeliveryPlanStepAssignmentDispatched || assignmentStatus == models.DeliveryPlanStepAssignmentRunning {
			return models.DeliveryPlanStepAssignmentBlocked, nil
		}
		return assignmentStatus, nil
	default:
		return "", fmt.Errorf("plan-step child task has an unsupported terminal state")
	}
}

func enqueueReadyPlanStepsInTransaction(tx *gorm.DB, executionID uuid.UUID, now time.Time) (int, error) {
	execution, readySteps, err := deliveryplansteps.ReadyPlanStepsInTransaction(tx, executionID, now)
	if err != nil {
		return 0, err
	}
	if len(readySteps) == 0 {
		return 0, nil
	}
	var parent models.AutomationTask
	if err := tx.First(&parent, "id = ?", execution.AutomationTaskID).Error; err != nil {
		return 0, err
	}
	if parent.DeliveryWorkItemID == nil || parent.Operation != "delivery.implementation" || parent.Status != "queued" {
		return 0, fmt.Errorf("approved-plan parent is not eligible for child dispatch")
	}
	var item models.DeliveryWorkItem
	if err := tx.First(&item, "id = ?", *parent.DeliveryWorkItemID).Error; err != nil {
		return 0, err
	}
	var plan models.DeliveryPlan
	if err := tx.First(&plan, "id = ?", execution.PlanID).Error; err != nil {
		return 0, err
	}
	var steps []models.DeliveryPlanStep
	if err := tx.Where("plan_id = ?", plan.ID).Order("display_order ASC, id ASC").Find(&steps).Error; err != nil {
		return 0, err
	}
	var dependencies []models.DeliveryPlanStepDependency
	if err := tx.Where("plan_id = ?", plan.ID).Order("step_id ASC, depends_on_step_id ASC").Find(&dependencies).Error; err != nil {
		return 0, err
	}
	dtos, err := deliveryplansteps.DTOs(steps, dependencies, plan.Version)
	if err != nil {
		return 0, err
	}
	stepDTOs := make(map[uuid.UUID]deliveryplansteps.StepDTO, len(dtos))
	for _, step := range dtos {
		stepID, parseErr := uuid.FromString(step.ID)
		if parseErr != nil || stepID == uuid.Nil || step.PlanID != plan.ID.String() || step.PlanVersion != execution.PlanVersion {
			return 0, fmt.Errorf("current plan DTO does not match the frozen execution")
		}
		stepDTOs[stepID] = step
	}
	childTaskIDs := make(map[uuid.UUID]uuid.UUID, len(readySteps))
	for _, step := range readySteps {
		if _, found := stepDTOs[step.ID]; !found {
			return 0, fmt.Errorf("ready step is not present in the frozen plan snapshot")
		}
		childID := deliveryplansteps.ChildAutomationTaskID(execution.ID, step.ID)
		var child models.AutomationTask
		if err := tx.First(&child, "id = ?", childID).Error; err != nil {
			return 0, fmt.Errorf("precreated child task is missing: %w", err)
		}
		if child.JobID != deliveryplansteps.ChildAutomationJobID(execution.ID, step.ID) || child.DeliveryWorkItemID == nil || *child.DeliveryWorkItemID != item.ID || child.Operation != parent.Operation || child.InputRef != parent.InputRef {
			return 0, fmt.Errorf("precreated child task does not match its execution parent")
		}
		childTaskIDs[step.ID] = childID
	}
	assignments, err := deliveryplansteps.ReserveReadyAssignmentsInTransaction(tx, execution.ID, execution.MaxConcurrency, childTaskIDs, now)
	if err != nil {
		return 0, err
	}
	if len(assignments) != len(childTaskIDs) {
		return 0, fmt.Errorf("reserved assignments differ from ready plan steps")
	}
	for _, assignment := range assignments {
		step, found := stepDTOs[assignment.DeliveryPlanStepID]
		childID, mapped := childTaskIDs[assignment.DeliveryPlanStepID]
		if !found || !mapped || childID != assignment.ChildAutomationTaskID {
			return 0, fmt.Errorf("reserved assignment does not match its targeted child")
		}
		result := tx.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ? AND status IN ?", assignment.ID, []string{models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued}).Updates(map[string]any{
			"status": models.DeliveryPlanStepAssignmentQueued, "queued_at": now, "updated_at": now,
		})
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected != 1 {
			return 0, fmt.Errorf("new plan-step assignment could not be queued")
		}
		var child models.AutomationTask
		if err := tx.First(&child, "id = ?", childID).Error; err != nil {
			return 0, err
		}
		queuedChild := tx.Model(&models.AutomationTask{}).Where("id = ? AND status IN ?", child.ID, []string{"pending", "queued"}).Updates(map[string]any{"status": "queued", "updated_at": now})
		if queuedChild.Error != nil {
			return 0, queuedChild.Error
		}
		if queuedChild.RowsAffected != 1 {
			return 0, fmt.Errorf("precreated child task is not eligible for first dispatch")
		}
		message := planStepChildQueueMessage(item, child, step)
		message.Payload.AgentKey = assignment.TargetAgentKey
		message.Payload.TargetMachineID = assignment.TargetMachineID
		if _, err := outboxService.EnqueueAutomationProcess(tx.Statement.Context, tx, message); err != nil {
			return 0, err
		}
	}
	result := tx.Model(&models.DeliveryPlanExecution{}).Where("id = ? AND status IN ?", execution.ID, []string{models.DeliveryPlanExecutionPending, models.DeliveryPlanExecutionDispatching}).Updates(map[string]any{
		"status": models.DeliveryPlanExecutionDispatching, "dispatched_at": now, "updated_at": now,
	})
	if result.Error != nil {
		return 0, result.Error
	}
	if err := tx.Model(&models.DeliveryWorkItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"agent_progress": "queued", "blocked_reason": "", "updated_at": now,
	}).Error; err != nil {
		return 0, err
	}
	return len(assignments), nil
}

func planStepChildQueueMessage(item models.DeliveryWorkItem, child models.AutomationTask, step deliveryplansteps.StepDTO) automationqueue.Message {
	message := automationqueue.Message{SchemaVersion: 1, JobID: child.JobID.String(), TenantCode: "itbem", CorrelationID: child.CorrelationID, Type: "ai.local.process"}
	message.Payload.TaskID = child.ID.String()
	message.Payload.ProjectID = item.ProjectID.String()
	message.Payload.AgentKey = strings.TrimSpace(step.AgentKey)
	message.Payload.PlanStepID = strings.TrimSpace(step.ID)
	message.Payload.Operation = child.Operation
	message.Payload.MaxCompletionTokens = child.MaxCompletionTokens
	message.Payload.InputRef = child.InputRef
	message.Payload.Attempt = 1
	return message
}

func activePlanStepAssignmentStatuses() []string {
	return []string{models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued, models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning}
}

func retryableMachineDispatchReason(err error) string {
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		reason = "No hay un agente local reciente con el perfil y todos los workspaces congelados listos en Docker. Se reintentará cuando llegue un heartbeat compatible."
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return reason
}

func persistPlanAggregationPendingInTransaction(tx *gorm.DB, parent models.AutomationTask, item models.DeliveryWorkItem, execution models.DeliveryPlanExecution, reason string, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = deliveryplansteps.AggregationPendingReason
	}
	updates := map[string]any{"agent_progress": "blocked", "blocked_reason": reason, "updated_at": now}
	if err := tx.Model(&models.DeliveryWorkItem{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
		return err
	}
	// Keep the parent queued and the execution running: neither a failed nor a
	// completed aggregate exists. Stop its continuation from interpreting the
	// parent's intentionally empty result as a normal implementation result.
	if err := tx.Model(&models.AutomationTask{}).Where("id = ? AND status = ?", parent.ID, "queued").Updates(map[string]any{
		"progress_step":             "aggregation_pending",
		"budget_reservation_micros": 0, "budget_reservation_expires_at": nil, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.DeliveryPlanExecution{}).Where("id = ? AND status IN ?", execution.ID, []string{models.DeliveryPlanExecutionPending, models.DeliveryPlanExecutionDispatching, models.DeliveryPlanExecutionRunning}).Updates(map[string]any{
		"status": models.DeliveryPlanExecutionRunning, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if parent.ContinuationID != nil {
		if err := tx.Model(&models.DeliveryContinuation{}).Where("id = ? AND status NOT IN ?", *parent.ContinuationID, []string{"done", "superseded"}).Updates(map[string]any{
			"status": "blocked", "available_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
	} else {
		if err := tx.Model(&models.DeliveryContinuation{}).Where("work_item_id = ? AND epoch = ? AND phase = ? AND status IN ?", item.ID, item.AutomationEpoch, "implementation", []string{"pending", "claimed", "dispatched"}).Updates(map[string]any{
			"status": "blocked", "available_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
	}
	message := planAggregationPendingMessage(item, execution, reason, now)
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&message).Error
}

func planAggregationPendingMessage(item models.DeliveryWorkItem, execution models.DeliveryPlanExecution, reason string, now time.Time) models.DeliveryMessage {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = deliveryplansteps.AggregationPendingReason
	}
	receipt, _ := json.Marshal(map[string]any{
		"version": 1, "status": "aggregation_pending", "reason": reason,
		"required_artifact": "verified_parent_workspace_merge_and_full_plan_acceptance_receipt",
	})
	return models.DeliveryMessage{
		ID:         uuid.NewV5(uuid.NamespaceURL, "delivery-plan-aggregation-pending/"+execution.ID.String()),
		WorkItemID: item.ID, Phase: "implementation", AuthorType: "agent", AuthorID: "itbem-runtime",
		Body: reason, Intent: "agent_update", Effect: "workflow_observation", ReceiptJSON: string(receipt), CreatedAt: now,
	}
}

func buildToolExecutionLedger(cfg *models.Config, task *models.AutomationTask, runID, status string, reported []callbackToolExecution, artifacts []callbackArtifact, completedAt time.Time) ([]models.AutomationToolExecution, error) {
	if len(reported) == 0 {
		return nil, nil
	}
	agentLoop := task != nil && task.Operation == "delivery.implementation" && (status == "completed" || status == "failed")
	if task == nil || (!agentLoop && ((strings.TrimSpace(status) != "completed" && strings.TrimSpace(status) != "failed") || task.Operation != "delivery.qa")) || task.DeliveryWorkItemID == nil || len(reported) > 6 {
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
			WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
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
func claimAutomationTaskRun(c echo.Context, id uuid.UUID, runID string, progress ...callbackRequest) error {
	var now, expiresAt, reservationExpiresAt time.Time
	claimIdentity := automationagent.AgentIdentity{}
	if len(progress) == 0 {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation claim", "a worker identity is required")
	}
	var identityErr error
	claimIdentity, identityErr = validateAgentClaimIdentity(configuration.DB, id, progress[0])
	if identityErr != nil || claimIdentity.WorkerID == "" || claimIdentity.AgentKey == "" || claimIdentity.MachineID == "" {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation claim", "worker identity or profile is invalid")
	}
	callbackIdentity, callbackAuthenticated := currentAgentCallbackIdentity(c)
	if !callbackAuthenticated || callbackIdentity.AgentKey != claimIdentity.AgentKey || callbackIdentity.MachineID != claimIdentity.MachineID {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	if !validAgentProgress(progress[0].ProgressStep, progress[0].ProgressCall) {
		return utils.Error(c, http.StatusBadRequest, "Invalid automation claim", "progress label is invalid")
	}
	claimed := false
	busy := false
	var retryAfterSeconds int64
	inferenceToken := ""
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var assignment models.DeliveryPlanStepAssignment
		assignmentErr := tx.Where("child_automation_task_id = ?", id).Take(&assignment).Error
		if assignmentErr == nil {
			var child models.AutomationTask
			if err := tx.Select("id", "delivery_work_item_id", "status").First(&child, "id = ?", id).Error; err != nil {
				return err
			}
			// A terminal child may still have its immutable assignment row. Do
			// not classify its redelivery as busy merely because the assignment is
			// now terminal; the queue can safely acknowledge it.
			switch child.Status {
			case "completed", "failed", "cancelled", "cancel_requested":
				return nil
			}
			if !planStepAssignmentTargetProfileMatches(assignment, claimIdentity) {
				busy = true
				return nil
			}
			if child.DeliveryWorkItemID == nil {
				busy = true
				return nil
			}
			var validationErr error
			now, validationErr = deliveryplansteps.ValidateAssignmentWorker(tx, &assignment, *child.DeliveryWorkItemID,
				claimIdentity.WorkerID, claimIdentity.AgentKey, claimIdentity.MachineID, time.Time{})
			if validationErr != nil {
				if errors.Is(validationErr, deliveryplansteps.ErrNoEligibleAgentMachine) {
					busy = true
					return nil
				}
				return validationErr
			}
		} else if !errors.Is(assignmentErr, gorm.ErrRecordNotFound) {
			return assignmentErr
		}
		if now.IsZero() {
			now = time.Now().UTC()
		}
		expiresAt = now.Add(automationRunLeaseDuration)
		reservationExpiresAt = now.Add(2 * automationRunLeaseDuration)
		// The budget hold intentionally outlives the execution lease. A worker
		// may finish uploading a private result shortly after its 20-minute
		// lease, while the queue needs time to redeliver a genuinely abandoned
		// run. Keeping the admission hold for 40 minutes prevents that narrow
		// recovery window from becoming unreserved spend.
		updates := map[string]any{"status": "running", "run_id": runID, "lease_expires_at": expiresAt, "budget_reservation_expires_at": reservationExpiresAt, "attempt_count": gorm.Expr("attempt_count + ?", 1)}
		renewal := map[string]any{"lease_expires_at": expiresAt, "budget_reservation_expires_at": reservationExpiresAt}
		if claimIdentity.WorkerID != "" {
			updates["worker_id"], updates["agent_key"], updates["machine_id"], updates["agent_instance_id"] = claimIdentity.WorkerID, claimIdentity.AgentKey, claimIdentity.MachineID, callbackIdentity.InstanceID
			renewal["worker_id"], renewal["agent_key"], renewal["machine_id"], renewal["agent_instance_id"] = claimIdentity.WorkerID, claimIdentity.AgentKey, claimIdentity.MachineID, callbackIdentity.InstanceID
		}
		if len(progress) > 0 && progress[0].ProgressStep != "" {
			updates["progress_step"], updates["progress_call"] = progress[0].ProgressStep, progress[0].ProgressCall
			renewal["progress_step"], renewal["progress_call"] = progress[0].ProgressStep, progress[0].ProgressCall
		}
		result := tx.Model(&models.AutomationTask{}).Where("id = ? AND status = ?", id, "queued").Updates(updates)
		if result.Error != nil || result.RowsAffected > 0 {
			claimed = result.RowsAffected > 0
			if result.Error != nil || !claimed {
				return result.Error
			}
			var claimedTask models.AutomationTask
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claimedTask, "id = ?", id).Error; err != nil {
				return err
			}
			if _, err := freezeAutomationInferenceAttemptPolicy(tx, claimedTask, runID, now); err != nil {
				return err
			}
			token, mintErr := mintAutomationInferenceCapability(claimedTask, now)
			if mintErr != nil {
				return mintErr
			}
			inferenceToken = token
			return renewPlanExecutionParentReservationInTransaction(tx, id, reservationExpiresAt, now)
		}
		var current models.AutomationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
			return err
		}
		if current.Status != "running" {
			return nil
		}
		if current.RunID == runID {
			if claimIdentity.WorkerID != "" && current.WorkerID != "" && (current.WorkerID != claimIdentity.WorkerID || current.AgentKey != claimIdentity.AgentKey || current.MachineID != claimIdentity.MachineID) {
				busy = true
				return nil
			}
			snapshot, err := readAutomationInferenceAttemptPolicy(tx, current.ID, runID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					// Do not extend a pre-migration run's lease without an immutable
					// inference policy snapshot for that exact attempt. Retain the
					// queue delivery so it can be retried with a fresh RunID after
					// the legacy lease expires.
					busy = true
					return nil
				}
				return err
			}
			if _, _, valid := validateAutomationInferenceAttemptPolicy(snapshot); !valid {
				return errors.New("automation inference attempt policy snapshot is invalid")
			}
			var projectID *uuid.UUID
			if current.DeliveryWorkItemID != nil {
				var item models.DeliveryWorkItem
				if err := tx.Select("project_id").First(&item, *current.DeliveryWorkItemID).Error; err != nil || item.ProjectID == uuid.Nil {
					return errors.New("automation inference attempt project scope is invalid")
				}
				projectID = &item.ProjectID
			}
			if snapshot.AutomationTaskID != current.ID || snapshot.RunID != current.RunID ||
				snapshot.Operation != current.Operation || snapshot.MaxCompletionTokens != current.MaxCompletionTokens ||
				!sameInferenceProject(snapshot.ProjectID, projectID) {
				return errors.New("automation inference attempt policy scope is invalid")
			}
			result := tx.Model(&models.AutomationTask{}).Where("id = ? AND status = ? AND run_id = ?", id, "running", runID).Updates(renewal)
			claimed = result.RowsAffected > 0
			if result.Error != nil || !claimed {
				return result.Error
			}
			current.LeaseExpiresAt = &expiresAt
			current.WorkerID, current.AgentKey, current.MachineID = claimIdentity.WorkerID, claimIdentity.AgentKey, claimIdentity.MachineID
			token, mintErr := mintAutomationInferenceCapability(current, now)
			if mintErr != nil {
				return mintErr
			}
			inferenceToken = token
			return renewPlanExecutionParentReservationInTransaction(tx, id, reservationExpiresAt, now)
		}
		if current.LeaseExpiresAt != nil && current.LeaseExpiresAt.After(now) {
			retryAfterSeconds = automationLeaseRetryAfterSeconds(*current.LeaseExpiresAt, now)
			return nil
		}
		result = tx.Model(&models.AutomationTask{}).
			Where("id = ? AND status = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)", id, "running", now).
			Updates(updates)
		claimed = result.RowsAffected > 0
		if result.Error != nil || !claimed {
			return result.Error
		}
		var claimedTask models.AutomationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claimedTask, "id = ?", id).Error; err != nil {
			return err
		}
		if _, err := freezeAutomationInferenceAttemptPolicy(tx, claimedTask, runID, now); err != nil {
			return err
		}
		token, mintErr := mintAutomationInferenceCapability(claimedTask, now)
		if mintErr != nil {
			return mintErr
		}
		inferenceToken = token
		return renewPlanExecutionParentReservationInTransaction(tx, id, reservationExpiresAt, now)
	}); err != nil {
		if err == gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusNotFound, "Automation task not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Automation result failed", "")
	}
	if !claimed {
		if retryAfterSeconds > 0 {
			c.Response().Header().Set(retryLeaseHeader, strconv.FormatInt(retryAfterSeconds, 10))
		}
		if busy || retryAfterSeconds > 0 {
			c.Response().Header().Set("X-ITBEM-Automation-Run-Busy", "1")
		}
		if busy {
			return utils.Error(c, http.StatusConflict, "Automation run is not eligible for this worker", "The assigned worker identity or plan-step eligibility does not match")
		}
		return utils.Error(c, http.StatusConflict, "Automation run is already leased", "Another active worker owns this execution; no provider call will be duplicated")
	}
	c.Response().Header().Set(inferencecapability.HeaderName, inferenceToken)
	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	return c.NoContent(http.StatusNoContent)
}

func mintAutomationInferenceCapability(task models.AutomationTask, now time.Time) (string, error) {
	key, _, ok := activeAttemptPolicySigningKey()
	if !ok || task.ID == uuid.Nil || task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) {
		return "", errors.New("automation inference capability signing or lease is unavailable")
	}
	return inferencecapability.Mint(string(key), inferencecapability.Scope{
		TaskID: task.ID.String(), RunID: task.RunID, Operation: task.Operation,
		WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
	}, inferencecapability.MaxTTL)
}

func planStepAssignmentTargetIdentityMatches(assignment models.DeliveryPlanStepAssignment, identity automationagent.AgentIdentity) bool {
	if assignment.ID == uuid.Nil || identity.WorkerID == "" || identity.AgentKey == "" || identity.MachineID == "" ||
		assignment.TargetMachineID == "" || assignment.TargetAgentKey == "" ||
		identity.MachineID != assignment.TargetMachineID || identity.AgentKey != assignment.TargetAgentKey {
		return false
	}
	for _, status := range activePlanStepAssignmentStatuses() {
		if assignment.Status == status {
			return true
		}
	}
	return false
}

// planStepAssignmentTargetProfileMatches leaves machine eligibility to the
// transactional delivery-plan validator. A redelivered queue message may be
// claimed by another machine only after that validator proves the existing
// task and step leases plus the old target heartbeat have expired.
func planStepAssignmentTargetProfileMatches(assignment models.DeliveryPlanStepAssignment, identity automationagent.AgentIdentity) bool {
	if assignment.ID == uuid.Nil || identity.WorkerID == "" || identity.AgentKey == "" || identity.MachineID == "" ||
		assignment.TargetAgentKey == "" || identity.AgentKey != assignment.TargetAgentKey {
		return false
	}
	for _, status := range activePlanStepAssignmentStatuses() {
		if assignment.Status == status {
			return true
		}
	}
	return false
}

func renewPlanExecutionParentReservationInTransaction(tx *gorm.DB, childTaskID uuid.UUID, expiresAt, now time.Time) error {
	if tx == nil || childTaskID == uuid.Nil {
		return fmt.Errorf("plan-step reservation renewal is missing its transaction or child task")
	}
	var scope struct {
		ParentTaskID uuid.UUID `gorm:"column:parent_task_id"`
	}
	err := tx.Table("delivery_plan_step_assignments AS assignment").
		Select("execution.automation_task_id AS parent_task_id").
		Joins("JOIN delivery_plan_executions AS execution ON execution.id = assignment.execution_id").
		Where("assignment.child_automation_task_id = ?", childTaskID).Take(&scope).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil // legacy, non-fan-out implementation task
	}
	if err != nil {
		return err
	}
	return tx.Model(&models.AutomationTask{}).
		Where("id = ? AND status = ? AND budget_reservation_micros > 0", scope.ParentTaskID, "queued").
		Updates(map[string]any{"budget_reservation_expires_at": expiresAt, "updated_at": now}).Error
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
	if task == nil {
		return false
	}
	// A delivery requester is not a permanent grant. Every read of its private
	// execution evidence must still be authorized by current project membership
	// (or platform-admin authority) so removing a member revokes access.
	if task.DeliveryWorkItemID != nil {
		return mayAccessDeliveryTask(c, task)
	}
	if strings.TrimSpace(requestedBy) != "" && task.RequestedBy == requestedBy {
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
	return task != nil && task.Operation == "code.review" && task.Status == "failed" && strings.TrimSpace(task.InputRef) != "" && artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest)))
}

// recoverableQueuedGitHubReview is intentionally narrower than a normal
// retry. It describes a durable handoff that was accepted by the control
// plane but was never claimed by any worker. A lease or an execution attempt
// makes recovery ineligible, leaving the task to the normal at-least-once
// worker protocol instead.
func recoverableQueuedGitHubReview(task *models.AutomationTask, now time.Time) bool {
	if task == nil || task.ID == uuid.Nil || task.JobID == uuid.Nil || task.Operation != "code.review" || task.RequestedBy != "github-app-review" || task.Status != "queued" || task.AttemptCount != 0 || strings.TrimSpace(task.InputRef) == "" || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))) || task.CreatedAt.IsZero() {
		return false
	}
	return !task.CreatedAt.After(now.UTC().Add(-githubReviewRecoveryDelay))
}

// recoverableExpiredGitHubReviewLease is the second, narrower recovery path.
// The task was claimed, but its worker lease has expired and no terminal
// callback was recorded. A fresh task is necessary because the original
// outbox event is already deduplicated; the new task carries RetryOfTaskID so
// publication remains bound to the same immutable review subject.
func recoverableExpiredGitHubReviewLease(task *models.AutomationTask, now time.Time) bool {
	if task == nil || task.ID == uuid.Nil || task.JobID == uuid.Nil || task.Operation != "code.review" || task.RequestedBy != "github-app-review" || task.Status != "running" || task.AttemptCount < 1 || task.AttemptCount >= githubReviewLeaseRecoveryMaximumAttempts || strings.TrimSpace(task.InputRef) == "" || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))) || task.LeaseExpiresAt == nil || task.LeaseExpiresAt.After(now.UTC()) || task.CompletedAt != nil {
		return false
	}
	return true
}

func exhaustedExpiredGitHubReviewLease(task *models.AutomationTask, now time.Time) bool {
	if task == nil || task.ID == uuid.Nil || task.JobID == uuid.Nil || task.Operation != "code.review" || task.RequestedBy != "github-app-review" || task.Status != "running" || task.AttemptCount < githubReviewLeaseRecoveryMaximumAttempts || strings.TrimSpace(task.InputRef) == "" || !artifactDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))) || task.LeaseExpiresAt == nil || task.LeaseExpiresAt.After(now.UTC()) || task.CompletedAt != nil {
		return false
	}
	return true
}

type githubReviewRecoverySubject struct {
	Repository     string
	PullRequest    int
	InstallationID int64
	HeadSHA        string
}

// parseGitHubReviewRecoverySubject re-establishes every side-effect boundary
// from the encrypted, immutable input before a stale task may be retried. In
// particular, neither a database correlation id nor a worker-provided value
// is enough to select a GitHub PR for a recovery.
func parseGitHubReviewRecoverySubject(task *models.AutomationTask, cfg *models.Config, raw []byte, now time.Time) (githubReviewRecoverySubject, error) {
	if !recoverableExpiredGitHubReviewLease(task, now) || !inputReferenceMatches(cfg, task.InputRef) || len(raw) == 0 || len(raw) > maxGitHubReviewRecoveryInputBytes {
		return githubReviewRecoverySubject{}, fmt.Errorf("GitHub review recovery input boundary is invalid")
	}
	var input automationagent.TaskInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return githubReviewRecoverySubject{}, fmt.Errorf("GitHub review recovery input is invalid")
	}
	review, err := automationagent.ParseCodeReviewInput(input.Delivery)
	if err != nil || review.Remote == nil {
		return githubReviewRecoverySubject{}, fmt.Errorf("GitHub review recovery subject is invalid")
	}
	digest, err := automationagent.CodeReviewPublicationSubjectSHA256(review)
	if err != nil || subtle.ConstantTimeCompare([]byte(strings.ToLower(strings.TrimSpace(task.EvidenceSubjectDigest))), []byte(digest)) != 1 {
		return githubReviewRecoverySubject{}, fmt.Errorf("GitHub review recovery subject digest is invalid")
	}
	repository := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(review.RepositoryRef), "github://"))
	if !githubRepositoryPattern.MatchString(repository) {
		return githubReviewRecoverySubject{}, fmt.Errorf("GitHub review recovery repository is invalid")
	}
	expectedCorrelationID, err := githubReviewCorrelationID(repository, review.Remote.PullRequestNumber, review.HeadSHA)
	if err != nil || subtle.ConstantTimeCompare([]byte(task.CorrelationID), []byte(expectedCorrelationID)) != 1 {
		return githubReviewRecoverySubject{}, fmt.Errorf("GitHub review recovery correlation is invalid")
	}
	return githubReviewRecoverySubject{Repository: repository, PullRequest: review.Remote.PullRequestNumber, InstallationID: review.Remote.InstallationID, HeadSHA: review.HeadSHA}, nil
}

func loadGitHubReviewRecoverySubject(ctx context.Context, task *models.AutomationTask, cfg *models.Config, now time.Time) (githubReviewRecoverySubject, error) {
	if task == nil || !inputReferenceMatches(cfg, task.InputRef) {
		return githubReviewRecoverySubject{}, fmt.Errorf("GitHub review recovery input reference is invalid")
	}
	bucket, key, err := privateReference(task.InputRef)
	if err != nil {
		return githubReviewRecoverySubject{}, err
	}
	body, err := awsrepository.GetS3Object(ctx, key, bucket)
	if err != nil {
		return githubReviewRecoverySubject{}, err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, maxGitHubReviewRecoveryInputBytes+1))
	if err != nil {
		return githubReviewRecoverySubject{}, err
	}
	return parseGitHubReviewRecoverySubject(task, cfg, raw, now)
}

// cancelObsoleteExpiredGitHubReviewLease retires a historical attempt only
// after GitHub has positively reported that its sealed revision is no longer
// reviewable. A transport failure deliberately does not enter this path. A
// publication is always terminal, even if its callback raced a worker crash.
func cancelObsoleteExpiredGitHubReviewLease(original *models.AutomationTask, now time.Time) error {
	if configuration.DB == nil || original == nil || original.ID == uuid.Nil {
		return fmt.Errorf("GitHub review recovery is unavailable")
	}
	return configuration.DB.Transaction(func(tx *gorm.DB) error {
		var current models.AutomationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, original.ID).Error; err != nil {
			return err
		}
		if !recoverableExpiredGitHubReviewLease(&current, now) {
			return nil
		}
		var publications int64
		if err := tx.Model(&models.AutomationCodeReviewPublication{}).Where("automation_task_id = ?", current.ID).Count(&publications).Error; err != nil {
			return err
		}
		if publications != 0 {
			return nil
		}
		return tx.Model(&models.AutomationTask{}).
			Where("id = ? AND status = ? AND lease_expires_at <= ?", current.ID, "running", now.UTC()).
			Updates(map[string]any{
				"status":                        "cancelled",
				"completed_at":                  now.UTC(),
				"lease_expires_at":              nil,
				"budget_reservation_expires_at": nil,
				"error_message":                 "Cancelled during reviewer lease reconciliation because GitHub no longer exposes the sealed pull-request revision",
			}).Error
	})
}

// failExhaustedExpiredGitHubReviewLease makes a bounded recovery policy
// observable. It never retries or publishes: after the one permitted repair
// path has itself expired, the only safe action is to release the stale lease
// and preserve the failure for an operator.
func failExhaustedExpiredGitHubReviewLease(original *models.AutomationTask, now time.Time) error {
	if configuration.DB == nil || original == nil || original.ID == uuid.Nil {
		return fmt.Errorf("GitHub review recovery is unavailable")
	}
	return configuration.DB.Transaction(func(tx *gorm.DB) error {
		var current models.AutomationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, original.ID).Error; err != nil {
			return err
		}
		if !exhaustedExpiredGitHubReviewLease(&current, now) {
			return nil
		}
		var publications int64
		if err := tx.Model(&models.AutomationCodeReviewPublication{}).Where("automation_task_id = ?", current.ID).Count(&publications).Error; err != nil {
			return err
		}
		if publications != 0 {
			return nil
		}
		return tx.Model(&models.AutomationTask{}).
			Where("id = ? AND status = ? AND lease_expires_at <= ?", current.ID, "running", now.UTC()).
			Updates(map[string]any{
				"status":                        "failed",
				"completed_at":                  now.UTC(),
				"lease_expires_at":              nil,
				"budget_reservation_expires_at": nil,
				"error_message":                 "Reviewer lease expired after the maximum permitted recovery attempts; no additional provider retry was created",
			}).Error
	})
}

// reconcileOneExpiredGitHubReviewLease is invoked only by the authenticated
// Review lane before it asks SQS for more work. It does not block ordinary
// queue delivery: any storage, GitHub or configuration failure simply leaves
// the historical task untouched for a later poll. At most one exact stale
// task is inspected, and it can be requeued only after a fresh GitHub App
// read proves the same open PR head still exists.
func reconcileOneExpiredGitHubReviewLease(ctx context.Context, cfg *models.Config, now time.Time) (bool, error) {
	if configuration.DB == nil || cfg == nil || !githubReviewWebhookConfigured(cfg) {
		return false, nil
	}
	var candidates []models.AutomationTask
	if err := configuration.DB.Where("operation = ? AND requested_by = ? AND status = ? AND attempt_count >= ? AND lease_expires_at <= ? AND completed_at IS NULL", "code.review", "github-app-review", "running", 1, now.UTC()).Order("lease_expires_at ASC").Limit(githubReviewLeaseReconciliationBatchSize).Find(&candidates).Error; err != nil {
		return false, err
	}
	if len(candidates) == 0 {
		return false, nil
	}
	candidate := &candidates[0]
	// An exhausted lease is deliberately terminal: it must be released before
	// attempting to reopen its immutable input. parseGitHubReviewRecoverySubject
	// correctly rejects an exhausted task (only a recoverable lease may supply
	// a subject), so doing the input read first would leave this row running
	// forever and starve every later review recovery behind it.
	if exhaustedExpiredGitHubReviewLease(candidate, now) {
		if err := failExhaustedExpiredGitHubReviewLease(candidate, now); err != nil {
			return false, err
		}
		return true, nil
	}
	subject, err := loadGitHubReviewRecoverySubject(ctx, candidate, cfg, now)
	if err != nil {
		return false, nil
	}
	appConfig, err := automationagent.LoadGitHubAppConfig(os.Getenv)
	if err != nil {
		return false, nil
	}
	appConfig, err = appConfig.WithInstallationID(subject.InstallationID)
	if err != nil {
		return false, nil
	}
	installation, err := automationagent.MintGitHubInstallationToken(ctx, appConfig, nil, now.UTC())
	if err != nil {
		return false, nil
	}
	currentPR, err := automationagent.ReadGitHubPullRequestState(ctx, appConfig, installation.Token, subject.Repository, subject.PullRequest)
	if err != nil {
		return false, nil
	}
	if !currentPR.Open || currentPR.Draft || currentPR.Merged || subtle.ConstantTimeCompare([]byte(currentPR.HeadSHA), []byte(subject.HeadSHA)) != 1 {
		return false, cancelObsoleteExpiredGitHubReviewLease(candidate, now)
	}
	recovered, err := recoverStrandedGitHubReview(ctx, candidate, now)
	if err != nil {
		return false, err
	}
	return recovered != nil, nil
}

// newStrandedGitHubReviewRecovery preserves the immutable review boundary but
// deliberately has a new task and job identity. The original has no worker
// attempt, so it is retained as cancelled audit evidence instead of being
// relabelled as a failed provider execution. RetryOfTaskID stays empty: this
// recovery must never authorize replacing a prior Reviewer check.
func newStrandedGitHubReviewRecovery(original *models.AutomationTask, now time.Time) (*models.AutomationTask, error) {
	if !recoverableQueuedGitHubReview(original, now) {
		return nil, fmt.Errorf("queued GitHub review recovery boundary is invalid")
	}
	return &models.AutomationTask{
		ID:                    uuid.Must(uuid.NewV4()),
		JobID:                 uuid.Must(uuid.NewV4()),
		RequestedBy:           original.RequestedBy,
		DeliveryWorkItemID:    original.DeliveryWorkItemID,
		DeliveryOnboardingID:  original.DeliveryOnboardingID,
		CorrelationID:         original.CorrelationID,
		Operation:             original.Operation,
		EvidenceSubjectDigest: strings.ToLower(strings.TrimSpace(original.EvidenceSubjectDigest)),
		MaxCompletionTokens:   original.MaxCompletionTokens,
		InputRef:              original.InputRef,
		Status:                "queued",
	}, nil
}

func newExpiredGitHubReviewLeaseRecovery(original *models.AutomationTask, now time.Time) (*models.AutomationTask, error) {
	if !recoverableExpiredGitHubReviewLease(original, now) {
		return nil, fmt.Errorf("expired GitHub review lease recovery boundary is invalid")
	}
	// Reuse the existing retry contract so the worker is explicitly told that
	// this task can supersede only the prior, same-subject reviewer attempt.
	failed := *original
	failed.Status = "failed"
	next, err := newCodeReviewRetryTask(&failed)
	if err != nil {
		return nil, err
	}
	// A retry is a new task identity, not a new retry budget. Carry the number
	// of completed claims forward so its next lease becomes the final allowed
	// attempt instead of opening an unbounded chain of fresh task rows.
	next.AttemptCount = original.AttemptCount
	return next, nil
}

// recoverStrandedGitHubReview gives an authenticated, current-head GitHub
// redelivery one bounded repair path for a lost queue handoff. The transaction
// re-reads and locks the source task, so a concurrent worker claim or another
// redelivery cannot create duplicate provider work.
func recoverStrandedGitHubReview(ctx context.Context, original *models.AutomationTask, now time.Time) (*models.AutomationTask, error) {
	if configuration.DB == nil || original == nil || original.ID == uuid.Nil {
		return nil, fmt.Errorf("queued GitHub review recovery is unavailable")
	}
	var recovered *models.AutomationTask
	err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var current models.AutomationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, original.ID).Error; err != nil {
			return err
		}
		var next *models.AutomationTask
		var message automationqueue.Message
		var updates map[string]any
		var where string
		var args []any
		switch {
		case recoverableQueuedGitHubReview(&current, now):
			var recoveryErr error
			next, recoveryErr = newStrandedGitHubReviewRecovery(&current, now)
			if recoveryErr != nil {
				return recoveryErr
			}
			updates = map[string]any{
				"status":                        "cancelled",
				"completed_at":                  now.UTC(),
				"budget_reservation_expires_at": nil,
				"error_message":                 "Recovered after an authenticated GitHub redelivery found no worker execution attempt",
			}
			where, args = "id = ? AND status = ? AND attempt_count = ?", []any{current.ID, "queued", 0}
			message = automationqueue.Message{SchemaVersion: 1, JobID: next.JobID.String(), TenantCode: "itbem", CorrelationID: next.CorrelationID, Type: "ai.local.process"}
			message.Payload.TaskID, message.Payload.Operation, message.Payload.MaxCompletionTokens, message.Payload.InputRef, message.Payload.Attempt = next.ID.String(), next.Operation, next.MaxCompletionTokens, next.InputRef, 1
		case recoverableExpiredGitHubReviewLease(&current, now):
			// A published reviewer result is terminal even if a late callback
			// failed to update its task row. Never publish or infer a duplicate.
			var publications int64
			if err := tx.Model(&models.AutomationCodeReviewPublication{}).Where("automation_task_id = ?", current.ID).Count(&publications).Error; err != nil {
				return err
			}
			if publications != 0 {
				return nil
			}
			var recoveryErr error
			next, recoveryErr = newExpiredGitHubReviewLeaseRecovery(&current, now)
			if recoveryErr != nil {
				return recoveryErr
			}
			updates = map[string]any{
				"status":                        "failed",
				"completed_at":                  now.UTC(),
				"lease_expires_at":              nil,
				"budget_reservation_expires_at": nil,
				"error_message":                 "Recovered after an authenticated GitHub redelivery found an expired reviewer execution lease without a publication",
			}
			where, args = "id = ? AND status = ? AND lease_expires_at <= ?", []any{current.ID, "running", now.UTC()}
			message = codeReviewRetryQueueMessage(&current, next)
		default:
			return nil
		}
		result := tx.Model(&models.AutomationTask{}).Where(where, args...).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		if err := tx.Create(next).Error; err != nil {
			return err
		}
		queued, enqueueErr := outboxService.EnqueueAutomationProcess(ctx, tx, message)
		if enqueueErr != nil {
			return enqueueErr
		}
		if !queued {
			return fmt.Errorf("GitHub review recovery delivery was not enqueued")
		}
		recovered = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return recovered, nil
}

// newCodeReviewRetryTask preserves the frozen input and its evidence subject.
// A retry is a new billable execution, not a new review target; dropping the
// subject digest would make an otherwise successful exact-SHA publication
// unverifiable at callback time.
func newCodeReviewRetryTask(original *models.AutomationTask) (*models.AutomationTask, error) {
	if !retryableCodeReviewTask(original) {
		return nil, fmt.Errorf("code review retry boundary is invalid")
	}
	return &models.AutomationTask{
		ID:                    uuid.Must(uuid.NewV4()),
		JobID:                 uuid.Must(uuid.NewV4()),
		RequestedBy:           original.RequestedBy,
		DeliveryWorkItemID:    original.DeliveryWorkItemID,
		CorrelationID:         original.CorrelationID,
		Operation:             original.Operation,
		EvidenceSubjectDigest: strings.ToLower(strings.TrimSpace(original.EvidenceSubjectDigest)),
		MaxCompletionTokens:   original.MaxCompletionTokens,
		InputRef:              original.InputRef,
		Status:                "queued",
	}, nil
}

// codeReviewRetryQueueMessage carries the explicit authorization for a Reviewer
// retry to supersede its earlier failed exact-SHA check. It is emitted by a
// manually authorized retry, or by the bounded authenticated-redelivery repair
// of an expired, unpublished GitHub review lease; ordinary delivery leaves
// RetryOfTaskID empty.
func codeReviewRetryQueueMessage(original, retry *models.AutomationTask) automationqueue.Message {
	message := automationqueue.Message{SchemaVersion: 1, JobID: retry.JobID.String(), TenantCode: "itbem", CorrelationID: retry.CorrelationID, Type: "ai.local.process"}
	message.Payload.TaskID = retry.ID.String()
	message.Payload.Operation = retry.Operation
	message.Payload.MaxCompletionTokens = retry.MaxCompletionTokens
	message.Payload.InputRef = retry.InputRef
	message.Payload.Attempt = retry.AttemptCount + 1
	message.Payload.RetryOfTaskID = original.ID.String()
	return message
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

// validWorkerCallbackCredential accepts the root callback secret only for the
// retained direct-AWS migration transport. Physical gateway workers receive a
// derived role/lane token, so compromise of one host identity cannot operate
// another queue lane or recover the server-owned root secret.
func validWorkerCallbackCredential(c echo.Context) bool {
	provided := strings.TrimSpace(c.Request().Header.Get("X-Automation-Secret"))
	if validCallbackSecret(provided) {
		return true
	}
	identity := gatewayIdentity{Role: agentwork.Role(strings.TrimSpace(c.Request().Header.Get("X-Agent-Role"))), Lane: agentwork.Lane(strings.TrimSpace(c.Request().Header.Get("X-Agent-Lane")))}
	if provided == "" || !agentwork.IsKnownRoleLane(identity.Role, identity.Lane) {
		return false
	}
	valid := 0
	for _, name := range []string{"AUTOMATION_CALLBACK_SECRET", "AUTOMATION_CALLBACK_SECRET_PREVIOUS"} {
		if root := strings.TrimSpace(os.Getenv(name)); root != "" {
			valid |= subtle.ConstantTimeCompare([]byte(provided), []byte(deriveGatewayToken(root, identity)))
		}
	}
	return valid == 1
}
