package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// The portfolio is intentionally a compact read model, not a replacement for
// the Delivery workflow. It gives an automation console enough state to render
// a living portfolio in one request, while every detailed resource keeps its
// existing, separately-authorized endpoint.
const (
	automationPortfolioSchemaVersion          = 3
	automationPortfolioMaxWorkItemsPerProject = 24
	automationPortfolioMaxTasksPerWorkItem    = 12
	automationPortfolioMaxReviewTasks         = 50
)

var automationPortfolioReviewCorrelation = regexp.MustCompile(`^github-pr:([a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*):([1-9][0-9]*):([a-f0-9]{40})$`)

// automationPortfolioSnapshot is safe to poll. It deliberately omits prompts,
// object-storage references, agent output, error messages, human identities,
// evidence references and other private implementation details.
type automationPortfolioSnapshot struct {
	SchemaVersion             int                          `json:"schema_version"`
	GeneratedAt               time.Time                    `json:"generated_at"`
	Revision                  string                       `json:"revision"`
	Totals                    automationPortfolioTotals    `json:"totals"`
	Projects                  []automationPortfolioProject `json:"projects"`
	ReviewQueue               []automationPortfolioReview  `json:"review_queue"`
	SummarySourcesUnavailable []string                     `json:"summary_sources_unavailable,omitempty"`
}

type automationPortfolioTotals struct {
	Projects          int64 `json:"projects"`
	WorkItems         int64 `json:"work_items"`
	ActiveWorkItems   int64 `json:"active_work_items"`
	DecisionsRequired int64 `json:"decisions_required"`
	BlockedWorkItems  int64 `json:"blocked_work_items"`
	AutomationTasks   int64 `json:"automation_tasks"`
	QueuedTasks       int64 `json:"queued_tasks"`
	RunningTasks      int64 `json:"running_tasks"`
	AttentionTasks    int64 `json:"attention_tasks"`
	ReviewTasks       int64 `json:"review_tasks"`
	QueuedReviews     int64 `json:"queued_reviews"`
	RunningReviews    int64 `json:"running_reviews"`
	AttentionReviews  int64 `json:"attention_reviews"`
	PublishedReviews  int64 `json:"published_reviews"`
}

// automationPortfolioReview is the public, platform-admin-only lifecycle of
// one webhook review. It contains no prompt, patch, finding, object reference,
// error prose, installation ID or credential material.
type automationPortfolioReview struct {
	TaskID        uuid.UUID  `json:"task_id"`
	Repository    string     `json:"repository"`
	PullRequest   int        `json:"pull_request"`
	HeadSHA       string     `json:"head_sha"`
	Status        string     `json:"status"`
	AttemptCount  int        `json:"attempt_count"`
	Verdict       string     `json:"verdict,omitempty"`
	Event         string     `json:"event,omitempty"`
	ReviewURL     string     `json:"review_url,omitempty"`
	ReviewerActor string     `json:"reviewer_actor,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	PublishedAt   *time.Time `json:"published_at,omitempty"`
}

type automationPortfolioProject struct {
	ID                 uuid.UUID                     `json:"id"`
	ClientID           uuid.UUID                     `json:"client_id"`
	Name               string                        `json:"name"`
	Status             string                        `json:"status"`
	UpdatedAt          time.Time                     `json:"updated_at"`
	Client             automationPortfolioClient     `json:"client"`
	WorkItemCount      int64                         `json:"work_item_count"`
	ActiveWorkItems    int64                         `json:"active_work_items"`
	DecisionsRequired  int64                         `json:"decisions_required"`
	BlockedWorkItems   int64                         `json:"blocked_work_items"`
	AutomationTasks    int64                         `json:"automation_tasks"`
	QueuedTasks        int64                         `json:"queued_tasks"`
	RunningTasks       int64                         `json:"running_tasks"`
	AttentionTasks     int64                         `json:"attention_tasks"`
	WorkItemsTruncated bool                          `json:"work_items_truncated"`
	WorkItems          []automationPortfolioWorkItem `json:"work_items"`
}

type automationPortfolioClient struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type automationPortfolioWorkItem struct {
	ID                       uuid.UUID                      `json:"id"`
	ProjectID                uuid.UUID                      `json:"project_id"`
	Title                    string                         `json:"title"`
	State                    string                         `json:"state"`
	CreatedAt                time.Time                      `json:"created_at"`
	UpdatedAt                time.Time                      `json:"updated_at"`
	AutomationTaskCount      int64                          `json:"automation_task_count"`
	AutomationTasksTruncated bool                           `json:"automation_tasks_truncated"`
	AutomationTasks          []automationPortfolioTask      `json:"automation_tasks"`
	GateSummary              automationPortfolioGateSummary `json:"gate_summary"`
	EvidenceCount            int64                          `json:"evidence_count"`
}

type automationPortfolioTask struct {
	ID           uuid.UUID  `json:"id"`
	Operation    string     `json:"operation"`
	Status       string     `json:"status"`
	AttemptCount int        `json:"attempt_count"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

type automationPortfolioGateSummary struct {
	Total            int64 `json:"total"`
	Approved         int64 `json:"approved"`
	ChangesRequested int64 `json:"changes_requested"`
}

// These rows are intentionally narrower than their domain models. Keeping the
// SQL selection explicit prevents a future model field (such as an object
// reference) from accidentally becoming part of this high-frequency response.
type automationPortfolioWorkItemRow struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Title     string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type automationPortfolioTaskRow struct {
	ID                 uuid.UUID
	DeliveryWorkItemID uuid.UUID
	Operation          string
	Status             string
	AttemptCount       int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

type automationPortfolioWorkItemTotals struct {
	ProjectID         uuid.UUID
	WorkItemCount     int64
	ActiveWorkItems   int64
	DecisionsRequired int64
	BlockedWorkItems  int64
}

type automationPortfolioProjectTaskTotals struct {
	ProjectID       uuid.UUID
	AutomationTasks int64
	QueuedTasks     int64
	RunningTasks    int64
	AttentionTasks  int64
}

type automationPortfolioWorkItemTaskTotals struct {
	WorkItemID      uuid.UUID
	AutomationTasks int64
}

type automationPortfolioGateTotals struct {
	WorkItemID       uuid.UUID
	Total            int64
	Approved         int64
	ChangesRequested int64
}

type automationPortfolioEvidenceTotals struct {
	WorkItemID    uuid.UUID
	EvidenceCount int64
}

type automationPortfolioReviewRow struct {
	TaskID                 uuid.UUID
	CorrelationID          string
	Status                 string
	AttemptCount           int
	CreatedAt              time.Time
	UpdatedAt              time.Time
	CompletedAt            *time.Time
	PublicationRepository  string
	PublicationPullRequest int
	PublicationHeadSHA     string
	Verdict                string
	Event                  string
	ReviewID               int64
	ReviewURL              string
	ReviewerActor          string
	PublishedAt            *time.Time
}

type automationPortfolioBuildInput struct {
	GeneratedAt               time.Time
	Projects                  []models.DeliveryProject
	WorkItems                 []automationPortfolioWorkItemRow
	Tasks                     []automationPortfolioTaskRow
	WorkItemTotals            []automationPortfolioWorkItemTotals
	ProjectTaskTotals         []automationPortfolioProjectTaskTotals
	WorkItemTaskTotals        []automationPortfolioWorkItemTaskTotals
	GateTotals                []automationPortfolioGateTotals
	EvidenceTotals            []automationPortfolioEvidenceTotals
	ReviewQueue               []automationPortfolioReview
	SummarySourcesUnavailable []string
}

// GetAutomationPortfolio returns the authenticated viewer's compact Delivery
// portfolio. Memberships are filtered through deliveryView rather than merely
// joined, so a custom project role never gains screen-level visibility it could
// not use to open the individual work item.
func GetAutomationPortfolio(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	viewer, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}

	input, err := loadAutomationPortfolio(viewer)
	if err != nil {
		return utilsError(c, err)
	}
	snapshot := buildAutomationPortfolio(input)
	etag := `"` + snapshot.Revision + `"`
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set(echo.HeaderCacheControl, "private, max-age=0, must-revalidate")
	if automationPortfolioETagMatches(c.Request().Header.Get("If-None-Match"), etag) {
		return c.NoContent(http.StatusNotModified)
	}
	return success(c, "Automation portfolio", snapshot)
}

func loadAutomationPortfolio(viewer *models.User) (automationPortfolioBuildInput, error) {
	input := automationPortfolioBuildInput{GeneratedAt: time.Now().UTC()}
	if viewer == nil {
		return input, gorm.ErrRecordNotFound
	}
	input.ReviewQueue = []automationPortfolioReview{}
	if viewer.IsPlatformAdmin() {
		if err := loadAutomationPortfolioReviewQueue(&input); err != nil {
			return input, err
		}
	}

	projectQuery := configuration.DB.Model(&models.DeliveryProject{}).
		Select("delivery_projects.id", "delivery_projects.client_id", "delivery_projects.name", "delivery_projects.status", "delivery_projects.updated_at").
		Preload("Client", func(db *gorm.DB) *gorm.DB { return db.Select("id", "name") }).
		Order("delivery_projects.updated_at DESC, delivery_projects.id DESC")

	if !viewer.IsPlatformAdmin() {
		projectIDs, err := automationPortfolioViewableProjectIDs(viewer.CognitoSub)
		if err != nil {
			return input, err
		}
		if len(projectIDs) == 0 {
			input.Projects = []models.DeliveryProject{}
			return input, nil
		}
		projectQuery = projectQuery.Where("delivery_projects.id IN ?", projectIDs)
	}
	if err := projectQuery.Find(&input.Projects).Error; err != nil {
		return input, err
	}
	if len(input.Projects) == 0 {
		input.Projects = []models.DeliveryProject{}
		return input, nil
	}

	projectIDs := make([]uuid.UUID, 0, len(input.Projects))
	for _, project := range input.Projects {
		projectIDs = append(projectIDs, project.ID)
	}
	if err := loadAutomationPortfolioWorkItemTotals(projectIDs, &input); err != nil {
		return input, err
	}
	if err := loadAutomationPortfolioProjectTaskTotals(projectIDs, &input); err != nil {
		return input, err
	}
	if err := loadAutomationPortfolioWorkItems(projectIDs, &input); err != nil {
		return input, err
	}

	workItemIDs := make([]uuid.UUID, 0, len(input.WorkItems))
	for _, workItem := range input.WorkItems {
		workItemIDs = append(workItemIDs, workItem.ID)
	}
	if len(workItemIDs) == 0 {
		input.WorkItems = []automationPortfolioWorkItemRow{}
		return input, nil
	}
	if err := loadAutomationPortfolioWorkItemTaskTotals(workItemIDs, &input); err != nil {
		return input, err
	}
	if err := loadAutomationPortfolioTasks(workItemIDs, &input); err != nil {
		return input, err
	}
	if err := loadAutomationPortfolioGateTotals(workItemIDs, &input); err != nil && !automationPortfolioOptionalSummaryUnavailable(err, "delivery_gates") {
		return input, err
	} else if err != nil {
		input.SummarySourcesUnavailable = append(input.SummarySourcesUnavailable, "gates")
	}
	if err := loadAutomationPortfolioEvidenceTotals(workItemIDs, &input); err != nil && !automationPortfolioOptionalSummaryUnavailable(err, "delivery_evidences") {
		return input, err
	} else if err != nil {
		input.SummarySourcesUnavailable = append(input.SummarySourcesUnavailable, "evidence")
	}
	return input, nil
}

func loadAutomationPortfolioReviewQueue(input *automationPortfolioBuildInput) error {
	var rows []automationPortfolioReviewRow
	if err := configuration.DB.Raw(`
		SELECT task.id AS task_id, task.correlation_id, task.status, task.attempt_count,
			task.created_at, task.updated_at, task.completed_at,
			COALESCE(publication.repository, '') AS publication_repository,
			COALESCE(publication.pull_request, 0) AS publication_pull_request,
			COALESCE(publication.head_sha, '') AS publication_head_sha,
			COALESCE(publication.verdict, '') AS verdict,
			COALESCE(publication.event, '') AS event,
			COALESCE(publication.review_id, 0) AS review_id,
			COALESCE(publication.review_url, '') AS review_url,
			COALESCE(publication.reviewer_actor, '') AS reviewer_actor,
			publication.published_at
		FROM automation_tasks AS task
		LEFT JOIN automation_code_review_publications AS publication ON publication.automation_task_id = task.id
		WHERE task.operation = 'code.review' AND task.requested_by = 'github-app-review'
		ORDER BY task.created_at DESC, task.id DESC
		LIMIT ?
	`, automationPortfolioMaxReviewTasks).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		review, ok := automationPortfolioReviewFromRow(row)
		if !ok {
			return fmt.Errorf("automation review queue contains invalid public identity")
		}
		input.ReviewQueue = append(input.ReviewQueue, review)
	}
	return nil
}

func automationPortfolioReviewFromRow(row automationPortfolioReviewRow) (automationPortfolioReview, bool) {
	matches := automationPortfolioReviewCorrelation.FindStringSubmatch(strings.ToLower(strings.TrimSpace(row.CorrelationID)))
	if row.TaskID == uuid.Nil || len(matches) != 4 || !automationPortfolioTaskStatus(strings.TrimSpace(row.Status)) || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		return automationPortfolioReview{}, false
	}
	pullRequest, err := strconv.Atoi(matches[2])
	if err != nil || pullRequest < 1 || !githubRepositoryPattern.MatchString(matches[1]) {
		return automationPortfolioReview{}, false
	}
	review := automationPortfolioReview{
		TaskID: row.TaskID, Repository: matches[1], PullRequest: pullRequest, HeadSHA: matches[3],
		Status: strings.TrimSpace(row.Status), AttemptCount: row.AttemptCount, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(), CompletedAt: utcTimePointer(row.CompletedAt),
	}
	if row.ReviewID == 0 {
		return review, true
	}
	verdict := strings.ToLower(strings.TrimSpace(row.Verdict))
	event := strings.ToUpper(strings.TrimSpace(row.Event))
	actor := strings.ToLower(strings.TrimSpace(row.ReviewerActor))
	if !strings.EqualFold(row.PublicationRepository, review.Repository) || row.PublicationPullRequest != review.PullRequest || !strings.EqualFold(row.PublicationHeadSHA, review.HeadSHA) || !automationPortfolioReviewVerdictEvent(verdict, event) || actor == "" || row.PublishedAt == nil || !automationPortfolioReviewURL(row.ReviewURL, review.Repository, review.PullRequest, row.ReviewID) {
		return automationPortfolioReview{}, false
	}
	review.Verdict, review.Event, review.ReviewURL, review.ReviewerActor = verdict, event, strings.TrimSpace(row.ReviewURL), actor
	review.PublishedAt = utcTimePointer(row.PublishedAt)
	return review, true
}

func automationPortfolioTaskStatus(value string) bool {
	switch value {
	case "queued", "running", "cancel_requested", "cancelled", "completed", "failed", "dispatch_failed":
		return true
	default:
		return false
	}
}

func automationPortfolioReviewVerdictEvent(verdict, event string) bool {
	return (verdict == "approve" && (event == "APPROVE" || event == "COMMENT")) ||
		(verdict == "request_changes" && event == "REQUEST_CHANGES") ||
		((verdict == "comment" || verdict == "blocked") && event == "COMMENT")
}

func automationPortfolioReviewURL(value, repository string, pullRequest int, reviewID int64) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "pullrequestreview-"+strconv.FormatInt(reviewID, 10) {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return len(parts) == 4 && strings.EqualFold(parts[0]+"/"+parts[1], repository) && parts[2] == "pull" && parts[3] == strconv.Itoa(pullRequest)
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func automationPortfolioViewableProjectIDs(cognitoSub string) ([]uuid.UUID, error) {
	var memberships []models.DeliveryProjectMember
	if err := configuration.DB.Select("project_id", "role", "permissions").
		Where("cognito_sub = ?", strings.TrimSpace(cognitoSub)).
		Find(&memberships).Error; err != nil {
		return nil, err
	}
	return automationPortfolioProjectIDsFromMemberships(memberships), nil
}

func automationPortfolioProjectIDsFromMemberships(memberships []models.DeliveryProjectMember) []uuid.UUID {
	projectIDs := make([]uuid.UUID, 0, len(memberships))
	for _, membership := range memberships {
		if membership.ProjectID != uuid.Nil && memberAllows(membership, deliveryView) {
			projectIDs = append(projectIDs, membership.ProjectID)
		}
	}
	return projectIDs
}

func loadAutomationPortfolioWorkItemTotals(projectIDs []uuid.UUID, input *automationPortfolioBuildInput) error {
	return configuration.DB.Raw(automationPortfolioWorkItemTotalsQuery(), projectIDs).Scan(&input.WorkItemTotals).Error
}

func automationPortfolioWorkItemTotalsQuery() string {
	return `
		SELECT work_item.project_id,
			COUNT(*) AS work_item_count,
			COALESCE(SUM(CASE
				WHEN work_item.state IN ('planning', 'implementation', 'preview_pending', 'qa_running')
					AND NOT EXISTS (
						SELECT 1 FROM automation_tasks AS stopping_task
						WHERE stopping_task.delivery_work_item_id = work_item.id
							AND stopping_task.status = 'cancel_requested'
					)
				THEN 1 ELSE 0 END), 0) AS active_work_items,
			COALESCE(SUM(CASE WHEN work_item.state IN ('plan_review', 'code_review', 'qa_review', 'release_review') THEN 1 ELSE 0 END), 0) AS decisions_required,
			COALESCE(SUM(CASE WHEN work_item.state = 'blocked' THEN 1 ELSE 0 END), 0) AS blocked_work_items
		FROM delivery_work_items AS work_item
		WHERE work_item.project_id IN ? AND work_item.deleted_at IS NULL
		GROUP BY work_item.project_id
	`
}

func loadAutomationPortfolioProjectTaskTotals(projectIDs []uuid.UUID, input *automationPortfolioBuildInput) error {
	// Only the newest attempt for each work-item operation can require
	// intervention. Earlier failed attempts remain in the audit trail but must
	// not keep the portfolio red after a later retry succeeded. A cancellation
	// request is likewise a neutral stop-in-progress, not an incident.
	return configuration.DB.Raw(automationPortfolioProjectTaskTotalsQuery(), projectIDs, projectIDs).Scan(&input.ProjectTaskTotals).Error
}

func automationPortfolioProjectTaskTotalsQuery() string {
	return `
		WITH ranked_tasks AS (
			SELECT task.delivery_work_item_id, task.operation, task.status, task.updated_at, task.id,
				ROW_NUMBER() OVER (
					PARTITION BY task.delivery_work_item_id, task.operation
					ORDER BY task.updated_at DESC, task.id DESC
				) AS operation_attempt_rank
			FROM automation_tasks AS task
		), latest_operation_tasks AS (
			SELECT delivery_work_item_id, status
			FROM ranked_tasks
			WHERE operation_attempt_rank = 1
		)
		, task_totals AS (
			SELECT work_item.project_id,
				COUNT(task.id) AS automation_tasks,
				COALESCE(SUM(CASE
					WHEN task.status = 'queued'
						AND NOT EXISTS (
							SELECT 1 FROM automation_tasks AS stopping_task
							WHERE stopping_task.delivery_work_item_id = work_item.id
								AND stopping_task.status = 'cancel_requested'
						)
					THEN 1 ELSE 0 END), 0) AS queued_tasks,
				COALESCE(SUM(CASE
					WHEN task.status = 'running'
						AND NOT EXISTS (
							SELECT 1 FROM automation_tasks AS stopping_task
							WHERE stopping_task.delivery_work_item_id = work_item.id
								AND stopping_task.status = 'cancel_requested'
						)
					THEN 1 ELSE 0 END), 0) AS running_tasks
			FROM delivery_work_items AS work_item
			LEFT JOIN automation_tasks AS task ON task.delivery_work_item_id = work_item.id
			WHERE work_item.project_id IN ? AND work_item.deleted_at IS NULL
			GROUP BY work_item.project_id
		), attention_totals AS (
			SELECT work_item.project_id,
				COALESCE(SUM(CASE WHEN latest_task.status IN ('failed', 'dispatch_failed') THEN 1 ELSE 0 END), 0) AS attention_tasks
			FROM delivery_work_items AS work_item
			LEFT JOIN latest_operation_tasks AS latest_task ON latest_task.delivery_work_item_id = work_item.id
			WHERE work_item.project_id IN ? AND work_item.deleted_at IS NULL
			GROUP BY work_item.project_id
		)
		SELECT task_totals.project_id, task_totals.automation_tasks, task_totals.queued_tasks, task_totals.running_tasks,
			COALESCE(attention_totals.attention_tasks, 0) AS attention_tasks
		FROM task_totals
		LEFT JOIN attention_totals ON attention_totals.project_id = task_totals.project_id
	`
}

func loadAutomationPortfolioWorkItems(projectIDs []uuid.UUID, input *automationPortfolioBuildInput) error {
	return configuration.DB.Raw(`
		SELECT id, project_id, title, state, created_at, updated_at
		FROM (
			SELECT id, project_id, title, state, created_at, updated_at,
				ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY updated_at DESC, id DESC) AS work_item_rank
			FROM delivery_work_items
			WHERE project_id IN ? AND deleted_at IS NULL
		) AS ranked_work_items
		WHERE work_item_rank <= ?
		ORDER BY updated_at DESC, id DESC
	`, projectIDs, automationPortfolioMaxWorkItemsPerProject).Scan(&input.WorkItems).Error
}

func loadAutomationPortfolioWorkItemTaskTotals(workItemIDs []uuid.UUID, input *automationPortfolioBuildInput) error {
	return configuration.DB.Table("automation_tasks").
		Select("delivery_work_item_id AS work_item_id, COUNT(*) AS automation_tasks").
		Where("delivery_work_item_id IN ?", workItemIDs).
		Group("delivery_work_item_id").
		Scan(&input.WorkItemTaskTotals).Error
}

func loadAutomationPortfolioTasks(workItemIDs []uuid.UUID, input *automationPortfolioBuildInput) error {
	return configuration.DB.Raw(`
		SELECT id, delivery_work_item_id, operation, status, attempt_count, created_at, updated_at, completed_at
		FROM (
			SELECT id, delivery_work_item_id, operation, status, attempt_count, created_at, updated_at, completed_at,
				ROW_NUMBER() OVER (PARTITION BY delivery_work_item_id ORDER BY updated_at DESC, id DESC) AS task_rank
			FROM automation_tasks
			WHERE delivery_work_item_id IN ?
		) AS ranked_tasks
		WHERE task_rank <= ?
		ORDER BY delivery_work_item_id, updated_at DESC, id DESC
	`, workItemIDs, automationPortfolioMaxTasksPerWorkItem).Scan(&input.Tasks).Error
}

func loadAutomationPortfolioGateTotals(workItemIDs []uuid.UUID, input *automationPortfolioBuildInput) error {
	return configuration.DB.Table("delivery_gates").
		Select(`work_item_id,
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN decision = 'approved' THEN 1 ELSE 0 END), 0) AS approved,
			COALESCE(SUM(CASE WHEN decision = 'changes_requested' THEN 1 ELSE 0 END), 0) AS changes_requested`).
		Where("work_item_id IN ?", workItemIDs).
		Group("work_item_id").
		Scan(&input.GateTotals).Error
}

func loadAutomationPortfolioEvidenceTotals(workItemIDs []uuid.UUID, input *automationPortfolioBuildInput) error {
	return configuration.DB.Table("delivery_evidences").
		Select("work_item_id, COUNT(*) AS evidence_count").
		Where("work_item_id IN ?", workItemIDs).
		Group("work_item_id").
		Scan(&input.EvidenceTotals).Error
}

func buildAutomationPortfolio(input automationPortfolioBuildInput) automationPortfolioSnapshot {
	generatedAt := input.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}

	workItemTotals := make(map[uuid.UUID]automationPortfolioWorkItemTotals, len(input.WorkItemTotals))
	for _, total := range input.WorkItemTotals {
		workItemTotals[total.ProjectID] = total
	}
	projectTaskTotals := make(map[uuid.UUID]automationPortfolioProjectTaskTotals, len(input.ProjectTaskTotals))
	for _, total := range input.ProjectTaskTotals {
		projectTaskTotals[total.ProjectID] = total
	}
	workItemTaskTotals := make(map[uuid.UUID]automationPortfolioWorkItemTaskTotals, len(input.WorkItemTaskTotals))
	for _, total := range input.WorkItemTaskTotals {
		workItemTaskTotals[total.WorkItemID] = total
	}
	gateTotals := make(map[uuid.UUID]automationPortfolioGateTotals, len(input.GateTotals))
	for _, total := range input.GateTotals {
		gateTotals[total.WorkItemID] = total
	}
	evidenceTotals := make(map[uuid.UUID]automationPortfolioEvidenceTotals, len(input.EvidenceTotals))
	for _, total := range input.EvidenceTotals {
		evidenceTotals[total.WorkItemID] = total
	}
	tasksByWorkItem := make(map[uuid.UUID][]automationPortfolioTask, len(input.Tasks))
	for _, task := range input.Tasks {
		tasksByWorkItem[task.DeliveryWorkItemID] = append(tasksByWorkItem[task.DeliveryWorkItemID], automationPortfolioTask{
			ID: task.ID, Operation: strings.TrimSpace(task.Operation), Status: strings.TrimSpace(task.Status), AttemptCount: task.AttemptCount,
			CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, CompletedAt: task.CompletedAt,
		})
	}
	workItemsByProject := make(map[uuid.UUID][]automationPortfolioWorkItem, len(input.WorkItems))
	for _, workItem := range input.WorkItems {
		taskTotal := workItemTaskTotals[workItem.ID]
		gates := gateTotals[workItem.ID]
		evidence := evidenceTotals[workItem.ID]
		tasks := tasksByWorkItem[workItem.ID]
		if tasks == nil {
			tasks = []automationPortfolioTask{}
		}
		workItemsByProject[workItem.ProjectID] = append(workItemsByProject[workItem.ProjectID], automationPortfolioWorkItem{
			ID: workItem.ID, ProjectID: workItem.ProjectID, Title: strings.TrimSpace(workItem.Title), State: strings.TrimSpace(workItem.State),
			CreatedAt: workItem.CreatedAt, UpdatedAt: workItem.UpdatedAt, AutomationTaskCount: taskTotal.AutomationTasks,
			AutomationTasksTruncated: taskTotal.AutomationTasks > int64(len(tasks)), AutomationTasks: tasks,
			GateSummary:   automationPortfolioGateSummary{Total: gates.Total, Approved: gates.Approved, ChangesRequested: gates.ChangesRequested},
			EvidenceCount: evidence.EvidenceCount,
		})
	}

	projects := make([]automationPortfolioProject, 0, len(input.Projects))
	totals := automationPortfolioTotals{Projects: int64(len(input.Projects))}
	reviewQueue := input.ReviewQueue
	if reviewQueue == nil {
		reviewQueue = []automationPortfolioReview{}
	}
	for _, review := range reviewQueue {
		totals.ReviewTasks++
		switch review.Status {
		case "queued":
			totals.QueuedReviews++
		case "running":
			totals.RunningReviews++
		case "failed", "dispatch_failed":
			totals.AttentionReviews++
		}
		if review.ReviewURL != "" {
			totals.PublishedReviews++
		}
	}
	for _, project := range input.Projects {
		workItemTotal := workItemTotals[project.ID]
		taskTotal := projectTaskTotals[project.ID]
		workItems := workItemsByProject[project.ID]
		if workItems == nil {
			workItems = []automationPortfolioWorkItem{}
		}
		projects = append(projects, automationPortfolioProject{
			ID: project.ID, ClientID: project.ClientID, Name: strings.TrimSpace(project.Name), Status: strings.TrimSpace(project.Status), UpdatedAt: project.UpdatedAt,
			Client:        automationPortfolioClient{ID: project.Client.ID, Name: strings.TrimSpace(project.Client.Name)},
			WorkItemCount: workItemTotal.WorkItemCount, ActiveWorkItems: workItemTotal.ActiveWorkItems, DecisionsRequired: workItemTotal.DecisionsRequired,
			BlockedWorkItems: workItemTotal.BlockedWorkItems, AutomationTasks: taskTotal.AutomationTasks, QueuedTasks: taskTotal.QueuedTasks,
			RunningTasks: taskTotal.RunningTasks, AttentionTasks: taskTotal.AttentionTasks,
			WorkItemsTruncated: workItemTotal.WorkItemCount > int64(len(workItems)), WorkItems: workItems,
		})
		totals.WorkItems += workItemTotal.WorkItemCount
		totals.ActiveWorkItems += workItemTotal.ActiveWorkItems
		totals.DecisionsRequired += workItemTotal.DecisionsRequired
		totals.BlockedWorkItems += workItemTotal.BlockedWorkItems
		totals.AutomationTasks += taskTotal.AutomationTasks
		totals.QueuedTasks += taskTotal.QueuedTasks
		totals.RunningTasks += taskTotal.RunningTasks
		totals.AttentionTasks += taskTotal.AttentionTasks
	}

	snapshot := automationPortfolioSnapshot{
		SchemaVersion:             automationPortfolioSchemaVersion,
		GeneratedAt:               generatedAt,
		Totals:                    totals,
		Projects:                  projects,
		ReviewQueue:               reviewQueue,
		SummarySourcesUnavailable: input.SummarySourcesUnavailable,
	}
	snapshot.Revision = automationPortfolioRevision(snapshot)
	return snapshot
}

func automationPortfolioRevision(snapshot automationPortfolioSnapshot) string {
	// generated_at is intentionally excluded: an unchanged response must keep a
	// stable revision so live clients can skip animation/layout work after 304.
	payload := struct {
		SchemaVersion             int                          `json:"schema_version"`
		Totals                    automationPortfolioTotals    `json:"totals"`
		Projects                  []automationPortfolioProject `json:"projects"`
		ReviewQueue               []automationPortfolioReview  `json:"review_queue"`
		SummarySourcesUnavailable []string                     `json:"summary_sources_unavailable,omitempty"`
	}{SchemaVersion: snapshot.SchemaVersion, Totals: snapshot.Totals, Projects: snapshot.Projects, ReviewQueue: snapshot.ReviewQueue, SummarySourcesUnavailable: snapshot.SummarySourcesUnavailable}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func automationPortfolioETagMatches(header, expected string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == expected || candidate == "W/"+expected {
			return true
		}
	}
	return false
}

// Older local databases may be booted before optional Delivery evidence or
// gate tables have migrated. The portfolio still renders its core work-item
// and task state, and explicitly tells clients which compact summaries are not
// available. Any other query failure remains visible as a real server error.
func automationPortfolioOptionalSummaryUnavailable(err error, table string) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, strings.ToLower(table)) &&
		(strings.Contains(message, "does not exist") || strings.Contains(message, "sqlstate 42p01"))
}
