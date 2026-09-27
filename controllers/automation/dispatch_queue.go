package automation

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	deliverycontroller "events-stocks/controllers/delivery"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const (
	dispatchQueueDefaultPageSize = 25
	dispatchQueueMaxPageSize     = 100
	dispatchQueueMaxCursorBytes  = 2048
	dispatchQueueHeartbeatStale  = 90 * time.Second
)

type dispatchQueueFilters struct {
	PageSize             int
	Status               string
	Project              *uuid.UUID
	AgentKey             string
	IncludeWorkerSignals bool
}

type dispatchQueueItem struct {
	AssignmentID         uuid.UUID  `json:"assignment_id"`
	ExecutionID          uuid.UUID  `json:"execution_id"`
	StepID               uuid.UUID  `json:"step_id"`
	StepKey              string     `json:"step_key"`
	StepTitle            string     `json:"step_title"`
	StepStatus           string     `json:"step_status"`
	AssignmentStatus     string     `json:"assignment_status"`
	WorkItemID           uuid.UUID  `json:"work_item_id"`
	WorkItemTitle        string     `json:"work_item_title"`
	WorkItemState        string     `json:"work_item_state"`
	ProjectID            uuid.UUID  `json:"project_id"`
	ProjectName          string     `json:"project_name"`
	ClientID             uuid.UUID  `json:"client_id"`
	ClientName           string     `json:"client_name"`
	TargetAgentKey       *string    `json:"target_agent_key"`
	TargetMachineID      *string    `json:"target_machine_id"`
	TargetAvailability   string     `json:"target_availability"`
	TargetConcurrency    int        `json:"target_concurrency"`
	TargetActiveRuns     int        `json:"target_active_runs"`
	TargetAvailableSlots int        `json:"target_available_slots"`
	TargetLastSeenAt     *time.Time `json:"target_last_seen_at"`
	QueuedAt             *time.Time `json:"queued_at"`
	DispatchedAt         *time.Time `json:"dispatched_at"`
	StartedAt            *time.Time `json:"started_at"`
	CreatedAt            time.Time  `json:"created_at"`
	IsReady              bool       `json:"is_ready"`
}

type dispatchQueuePage struct {
	SchemaVersion         int                 `json:"schema_version"`
	GeneratedAt           time.Time           `json:"generated_at"`
	BlockedAssignments    int64               `json:"blocked_assignments"`
	ExpiredPlanStepLeases int64               `json:"expired_plan_step_leases"`
	Items                 []dispatchQueueItem `json:"items"`
	NextCursor            *string             `json:"next_cursor,omitempty"`
}

type dispatchQueueSnapshot struct {
	BlockedAssignments    int64 `gorm:"column:blocked_assignments"`
	ExpiredPlanStepLeases int64 `gorm:"column:expired_plan_step_leases"`
}

type dispatchQueueRow struct {
	AssignmentID      uuid.UUID  `gorm:"column:assignment_id"`
	ExecutionID       uuid.UUID  `gorm:"column:execution_id"`
	StepID            uuid.UUID  `gorm:"column:step_id"`
	StepKey           string     `gorm:"column:step_key"`
	StepTitle         string     `gorm:"column:step_title"`
	StepStatus        string     `gorm:"column:step_status"`
	AssignmentStatus  string     `gorm:"column:assignment_status"`
	WorkItemID        uuid.UUID  `gorm:"column:work_item_id"`
	WorkItemTitle     string     `gorm:"column:work_item_title"`
	WorkItemState     string     `gorm:"column:work_item_state"`
	ProjectID         uuid.UUID  `gorm:"column:project_id"`
	ProjectName       string     `gorm:"column:project_name"`
	ClientID          uuid.UUID  `gorm:"column:client_id"`
	ClientName        string     `gorm:"column:client_name"`
	TargetAgentKey    string     `gorm:"column:target_agent_key"`
	TargetMachineID   *string    `gorm:"column:target_machine_id"`
	TargetConcurrency int        `gorm:"column:target_concurrency"`
	TargetActiveRuns  int        `gorm:"column:target_active_runs"`
	TargetDraining    bool       `gorm:"column:target_draining"`
	TargetLastSeenAt  *time.Time `gorm:"column:target_last_seen_at"`
	QueuedAt          *time.Time `gorm:"column:queued_at"`
	DispatchedAt      *time.Time `gorm:"column:dispatched_at"`
	StartedAt         *time.Time `gorm:"column:started_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	IsReady           bool       `gorm:"column:is_ready"`
}

type dispatchQueueCursor struct {
	Version   int       `json:"v"`
	Scope     string    `json:"s"`
	Snapshot  time.Time `json:"snapshot"`
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

// GetDispatchQueue returns real, nonterminal delivery-plan assignments.
// Platform operators may inspect the global queue; organization workspaces
// must select one project and receive no machine/worker-wide heartbeat data.
// This endpoint is intentionally read-only: assignment, lease and scheduler
// state can only be changed by the dispatcher/runtime.
func GetDispatchQueue(c echo.Context) error {
	workspaceMode, _ := c.Get("workspace_mode").(string)
	workspaceMode = strings.ToLower(strings.TrimSpace(workspaceMode))
	if workspaceMode == "organization" {
		organizationID, ok := c.Get("organization_id").(uuid.UUID)
		if !ok || organizationID == uuid.Nil {
			return utils.Error(c, http.StatusNotFound, "Dispatch queue unavailable", "")
		}
	} else if err := requireAgentPlatformWorkspace(c, false); err != nil {
		return authz.Respond(c, err)
	}

	filters, err := parseDispatchQueueFilters(c)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid dispatch queue query", err.Error())
	}
	if workspaceMode == "organization" && filters.Project == nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid dispatch queue query", "project_id is required in an organization workspace")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Dispatch queue unavailable", "Database is unavailable")
	}
	if filters.Project != nil {
		allowed, authErr := deliverycontroller.AuthorizeProjectView(c, *filters.Project)
		if authErr != nil {
			return authErr
		}
		if !allowed {
			return utils.Error(c, http.StatusNotFound, "Dispatch queue unavailable", "")
		}
	}
	filters.IncludeWorkerSignals = workspaceMode == "platform"

	actorSub, _ := c.Get("cognito_sub").(string)
	organizationID, _ := c.Get("organization_id").(uuid.UUID)
	scope := dispatchQueueCursorScope(filters, actorSub, workspaceMode, organizationID)
	cursor, err := decodeDispatchQueueCursor(c.QueryParam("cursor"), scope)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid dispatch queue cursor", err.Error())
	}

	generatedAt := time.Now().UTC()
	if cursor != nil {
		generatedAt = cursor.Snapshot.UTC()
	}
	snapshotQuery, snapshotArgs := dispatchQueueSnapshotQuery(filters, generatedAt)
	var snapshot dispatchQueueSnapshot
	if err := configuration.DB.WithContext(c.Request().Context()).Raw(snapshotQuery, snapshotArgs...).Scan(&snapshot).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Dispatch queue unavailable", "")
	}
	query, args := dispatchQueueQuery(filters, cursor, generatedAt)
	var rows []dispatchQueueRow
	if err := configuration.DB.WithContext(c.Request().Context()).Raw(query, args...).Scan(&rows).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Dispatch queue unavailable", "")
	}

	page := dispatchQueuePage{
		SchemaVersion: 1, GeneratedAt: generatedAt,
		BlockedAssignments:    max(int64(0), snapshot.BlockedAssignments),
		ExpiredPlanStepLeases: max(int64(0), snapshot.ExpiredPlanStepLeases),
		Items:                 make([]dispatchQueueItem, 0, filters.PageSize),
	}
	if len(rows) > filters.PageSize {
		rows = rows[:filters.PageSize]
		last := rows[len(rows)-1]
		nextCursor := encodeDispatchQueueCursor(dispatchQueueCursor{
			Version: 1, Scope: scope, Snapshot: generatedAt, CreatedAt: last.CreatedAt.UTC(), ID: last.AssignmentID.String(),
		})
		page.NextCursor = &nextCursor
	}
	for _, row := range rows {
		page.Items = append(page.Items, projectDispatchQueueItem(row, generatedAt))
	}
	return utils.Success(c, http.StatusOK, "Dispatch queue", page)
}

func parseDispatchQueueFilters(c echo.Context) (dispatchQueueFilters, error) {
	filters := dispatchQueueFilters{PageSize: dispatchQueueDefaultPageSize}
	if raw := strings.TrimSpace(c.QueryParam("page_size")); raw != "" {
		pageSize, err := strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > dispatchQueueMaxPageSize {
			return dispatchQueueFilters{}, errors.New("page_size must be an integer between 1 and 100")
		}
		filters.PageSize = pageSize
	}
	filters.Status = strings.ToLower(strings.TrimSpace(c.QueryParam("status")))
	if filters.Status != "" {
		switch filters.Status {
		case models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
			models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning,
			models.DeliveryPlanStepAssignmentBlocked:
		default:
			return dispatchQueueFilters{}, errors.New("status is not a supported nonterminal assignment status")
		}
	}
	if raw := strings.TrimSpace(c.QueryParam("project_id")); raw != "" {
		projectID, err := uuid.FromString(raw)
		if err != nil || projectID == uuid.Nil || projectID.String() != strings.ToLower(raw) {
			return dispatchQueueFilters{}, errors.New("project_id must be a non-zero UUID")
		}
		filters.Project = &projectID
	}
	filters.AgentKey = strings.TrimSpace(c.QueryParam("agent_key"))
	if filters.AgentKey != "" && !agentProfileKeyPattern.MatchString(filters.AgentKey) {
		return dispatchQueueFilters{}, errors.New("agent_key is invalid")
	}
	return filters, nil
}

func dispatchQueueQuery(filters dispatchQueueFilters, cursor *dispatchQueueCursor, snapshot time.Time) (string, []any) {
	workerProjection := `NULL::text AS target_machine_id,
		0 AS target_concurrency,
		0 AS target_active_runs,
		FALSE AS target_draining,
		NULL::timestamptz AS target_last_seen_at`
	workerJoin := ""
	if filters.IncludeWorkerSignals {
		workerProjection = `NULLIF(a.target_machine_id, '') AS target_machine_id,
		COALESCE(target.concurrency, 0) AS target_concurrency,
		COALESCE(target.active_runs, 0) AS target_active_runs,
		COALESCE(target.draining, FALSE) AS target_draining,
		target.last_seen_at AS target_last_seen_at`
		workerJoin = `LEFT JOIN LATERAL (
		SELECT h.concurrency, h.draining, h.last_seen_at,
			(SELECT COUNT(*) FROM automation_tasks active
			 WHERE active.worker_id = h.worker_id AND active.status IN ('running', 'cancel_requested')) AS active_runs
		FROM automation_agent_heartbeats h
		WHERE a.target_machine_id <> ''
			AND h.agent_key = a.target_agent_key
			AND h.machine_id = a.target_machine_id
		ORDER BY h.last_seen_at DESC, h.updated_at DESC
		LIMIT 1
	) target ON TRUE`
	}
	query := `SELECT
		a.id AS assignment_id,
		e.id AS execution_id,
		s.id AS step_id,
		s.step_key AS step_key,
		s.title AS step_title,
		s.status AS step_status,
		a.status AS assignment_status,
		wi.id AS work_item_id,
		wi.title AS work_item_title,
		wi.state AS work_item_state,
		pr.id AS project_id,
		pr.name AS project_name,
		cl.id AS client_id,
		cl.name AS client_name,
		a.target_agent_key AS target_agent_key,
		` + workerProjection + `,
		a.queued_at AS queued_at,
		a.dispatched_at AS dispatched_at,
		a.started_at AS started_at,
		a.created_at AS created_at,
		CASE WHEN s.status = 'ready'
			AND a.status IN ('pending', 'queued', 'dispatched')
			AND NOT EXISTS (
				SELECT 1
				FROM delivery_plan_step_dependencies d
				JOIN delivery_plan_steps dependency_step ON dependency_step.id = d.depends_on_step_id
				WHERE d.plan_id = dp.id AND d.step_id = s.id AND dependency_step.status <> 'completed'
			)
		THEN TRUE ELSE FALSE END AS is_ready
	FROM delivery_plan_step_assignments a
	JOIN delivery_plan_executions e ON e.id = a.execution_id
	JOIN delivery_plans dp ON dp.id = e.plan_id AND dp.status = 'approved'
	JOIN delivery_plan_steps s ON s.id = a.delivery_plan_step_id AND s.plan_id = dp.id
	JOIN automation_tasks parent_task ON parent_task.id = e.automation_task_id
	JOIN delivery_work_items wi ON wi.id = parent_task.delivery_work_item_id
	JOIN delivery_projects pr ON pr.id = wi.project_id
	JOIN clients cl ON cl.id = pr.client_id
	` + workerJoin + `
	WHERE a.status NOT IN ('completed', 'failed', 'cancelled')
		AND e.status NOT IN ('completed', 'failed', 'cancelled')
		AND wi.deleted_at IS NULL AND pr.deleted_at IS NULL AND cl.deleted_at IS NULL
		AND a.created_at <= ?`
	args := []any{snapshot}
	if filters.Status != "" {
		query += "\n\t\tAND a.status = ?"
		args = append(args, filters.Status)
	}
	if filters.Project != nil {
		query += "\n\t\tAND pr.id = ?"
		args = append(args, *filters.Project)
	}
	if filters.AgentKey != "" {
		query += "\n\t\tAND a.target_agent_key = ?"
		args = append(args, filters.AgentKey)
	}
	if cursor != nil {
		query += "\n\t\tAND (a.created_at, a.id) > (?, ?::uuid)"
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += "\n\tORDER BY a.created_at ASC, a.id ASC\n\tLIMIT ?"
	args = append(args, filters.PageSize+1)
	return query, args
}

// dispatchQueueSnapshotQuery derives organization/project-visible queue
// counters from the same approved, nonterminal assignments used by the page.
// It intentionally excludes worker heartbeats and cursor position: counters
// summarize the whole filtered result set at the cursor's fixed snapshot.
func dispatchQueueSnapshotQuery(filters dispatchQueueFilters, snapshot time.Time) (string, []any) {
	query := `SELECT
		COUNT(*) FILTER (WHERE a.status = 'blocked') AS blocked_assignments,
		COUNT(DISTINCT s.id) FILTER (
			WHERE s.status = 'running'
				AND s.lease_expires_at IS NOT NULL
				AND s.lease_expires_at <= ?
		) AS expired_plan_step_leases
	FROM delivery_plan_step_assignments a
	JOIN delivery_plan_executions e ON e.id = a.execution_id
	JOIN delivery_plans dp ON dp.id = e.plan_id AND dp.status = 'approved'
	JOIN delivery_plan_steps s ON s.id = a.delivery_plan_step_id AND s.plan_id = dp.id
	JOIN automation_tasks parent_task ON parent_task.id = e.automation_task_id
	JOIN delivery_work_items wi ON wi.id = parent_task.delivery_work_item_id
	JOIN delivery_projects pr ON pr.id = wi.project_id
	JOIN clients cl ON cl.id = pr.client_id
	WHERE a.status NOT IN ('completed', 'failed', 'cancelled')
		AND e.status NOT IN ('completed', 'failed', 'cancelled')
		AND wi.deleted_at IS NULL AND pr.deleted_at IS NULL AND cl.deleted_at IS NULL
		AND a.created_at <= ?`
	args := []any{snapshot, snapshot}
	if filters.Status != "" {
		query += "\n\t\tAND a.status = ?"
		args = append(args, filters.Status)
	}
	if filters.Project != nil {
		query += "\n\t\tAND pr.id = ?"
		args = append(args, *filters.Project)
	}
	if filters.AgentKey != "" {
		query += "\n\t\tAND a.target_agent_key = ?"
		args = append(args, filters.AgentKey)
	}
	return query, args
}

func dispatchQueueCursorScope(filters dispatchQueueFilters, actorSub, workspaceMode string, organizationID uuid.UUID) string {
	identity := struct {
		ActorSub      string `json:"actor_sub"`
		WorkspaceMode string `json:"workspace_mode"`
		Organization  string `json:"organization_id,omitempty"`
		PageSize      int    `json:"page_size"`
		Status        string `json:"status,omitempty"`
		ProjectID     string `json:"project_id,omitempty"`
		AgentKey      string `json:"agent_key,omitempty"`
	}{ActorSub: actorSub, WorkspaceMode: strings.ToLower(strings.TrimSpace(workspaceMode)), PageSize: filters.PageSize, Status: filters.Status, AgentKey: filters.AgentKey}
	if organizationID != uuid.Nil {
		identity.Organization = organizationID.String()
	}
	if filters.Project != nil {
		identity.ProjectID = filters.Project.String()
	}
	encoded, _ := json.Marshal(identity)
	scopeHash := sha256.Sum256(encoded)
	return hex.EncodeToString(scopeHash[:])
}

func encodeDispatchQueueCursor(cursor dispatchQueueCursor) string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeDispatchQueueCursor(raw, scope string) (*dispatchQueueCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > dispatchQueueMaxCursorBytes {
		return nil, errors.New("cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("cursor encoding is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var cursor dispatchQueueCursor
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Scope != scope || cursor.Snapshot.IsZero() || cursor.CreatedAt.IsZero() {
		return nil, errors.New("cursor is invalid or belongs to another filter set")
	}
	id, err := uuid.FromString(cursor.ID)
	if err != nil || id == uuid.Nil || id.String() != cursor.ID || cursor.CreatedAt.After(cursor.Snapshot) {
		return nil, errors.New("cursor position is invalid")
	}
	cursor.Snapshot = cursor.Snapshot.UTC()
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	return &cursor, nil
}

func projectDispatchQueueItem(row dispatchQueueRow, observedAt time.Time) dispatchQueueItem {
	item := dispatchQueueItem{
		AssignmentID: row.AssignmentID, ExecutionID: row.ExecutionID, StepID: row.StepID,
		StepKey: safeDispatchQueueStepKey(row.StepKey), StepTitle: safeAgentDirectoryLabel(row.StepTitle, 240),
		StepStatus: safeDispatchQueueStepStatus(row.StepStatus), AssignmentStatus: safeDispatchQueueAssignmentStatus(row.AssignmentStatus),
		WorkItemID: row.WorkItemID, WorkItemTitle: safeAgentDirectoryLabel(row.WorkItemTitle, 240),
		WorkItemState: safeDispatchQueueWorkItemState(row.WorkItemState), ProjectID: row.ProjectID,
		ProjectName: safeAgentDirectoryLabel(row.ProjectName, 180), ClientID: row.ClientID,
		ClientName: safeAgentDirectoryLabel(row.ClientName, 180),
		QueuedAt:   row.QueuedAt, DispatchedAt: row.DispatchedAt, StartedAt: row.StartedAt,
		CreatedAt: row.CreatedAt.UTC(), IsReady: row.IsReady,
		TargetConcurrency: max(0, row.TargetConcurrency), TargetActiveRuns: max(0, row.TargetActiveRuns),
	}
	if agentKey := safeDispatchQueueAgentKey(row.TargetAgentKey); agentKey != "" {
		item.TargetAgentKey = &agentKey
	}
	if row.TargetMachineID != nil {
		if machineID := canonicalOpaqueMachineID(*row.TargetMachineID); machineID != "" {
			item.TargetMachineID = &machineID
		}
	}
	if row.TargetLastSeenAt != nil {
		lastSeen := row.TargetLastSeenAt.UTC()
		item.TargetLastSeenAt = &lastSeen
	}
	item.TargetAvailability, item.TargetAvailableSlots = dispatchQueueTargetAvailability(row, observedAt)
	if item.QueuedAt != nil {
		value := item.QueuedAt.UTC()
		item.QueuedAt = &value
	}
	if item.DispatchedAt != nil {
		value := item.DispatchedAt.UTC()
		item.DispatchedAt = &value
	}
	if item.StartedAt != nil {
		value := item.StartedAt.UTC()
		item.StartedAt = &value
	}
	return item
}

func dispatchQueueTargetAvailability(row dispatchQueueRow, observedAt time.Time) (string, int) {
	if row.TargetLastSeenAt == nil || row.TargetLastSeenAt.After(observedAt) {
		return "unknown", 0
	}
	if !row.TargetLastSeenAt.After(observedAt.Add(-dispatchQueueHeartbeatStale)) {
		return "offline", 0
	}
	if row.TargetDraining {
		return "draining", 0
	}
	concurrency, activeRuns := max(0, row.TargetConcurrency), max(0, row.TargetActiveRuns)
	if concurrency == 0 {
		return "no_capacity", 0
	}
	if activeRuns >= concurrency {
		return "saturated", 0
	}
	if activeRuns > 0 {
		return "working", concurrency - activeRuns
	}
	return "available", concurrency
}

func safeDispatchQueueAssignmentStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
		models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning,
		models.DeliveryPlanStepAssignmentBlocked:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func safeDispatchQueueStepStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case models.DeliveryPlanStepPlanned, models.DeliveryPlanStepReady, models.DeliveryPlanStepRunning,
		models.DeliveryPlanStepBlocked, models.DeliveryPlanStepCompleted, models.DeliveryPlanStepFailed,
		models.DeliveryPlanStepSkipped:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func safeDispatchQueueWorkItemState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "planning", "plan_review", "implementation", "code_review", "preview_pending", "qa_running",
		"qa_review", "release_review", "released", "blocked", "cancelled":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func safeDispatchQueueAgentKey(value string) string {
	value = strings.TrimSpace(value)
	if agentProfileKeyPattern.MatchString(value) {
		return value
	}
	return ""
}

func safeDispatchQueueStepKey(value string) string {
	value = strings.TrimSpace(value)
	if agentHistoryStepKeyPattern.MatchString(value) {
		return value
	}
	return ""
}
