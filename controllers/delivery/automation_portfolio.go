package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"
	"net/http"
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
	automationPortfolioSchemaVersion          = 2
	automationPortfolioMaxWorkItemsPerProject = 24
	automationPortfolioMaxTasksPerWorkItem    = 12
)

// automationPortfolioSnapshot is safe to poll. It deliberately omits prompts,
// object-storage references, agent output, error messages, human identities,
// evidence references and other private implementation details.
type automationPortfolioSnapshot struct {
	SchemaVersion             int                          `json:"schema_version"`
	GeneratedAt               time.Time                    `json:"generated_at"`
	Revision                  string                       `json:"revision"`
	Totals                    automationPortfolioTotals    `json:"totals"`
	Projects                  []automationPortfolioProject `json:"projects"`
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
		SummarySourcesUnavailable []string                     `json:"summary_sources_unavailable,omitempty"`
	}{SchemaVersion: snapshot.SchemaVersion, Totals: snapshot.Totals, Projects: snapshot.Projects, SummarySourcesUnavailable: snapshot.SummarySourcesUnavailable}
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
