package automation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"events-stocks/configuration"
	deliverycontroller "events-stocks/controllers/delivery"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/organizationscope"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const agentDirectorySchemaVersion = 1

func isOpaqueMachineID(raw string) bool {
	parsed, err := uuid.FromString(strings.TrimSpace(raw))
	return err == nil && parsed != uuid.Nil
}

func requireAgentPlatformWorkspace(c echo.Context, primaryRoot bool) error {
	if primaryRoot {
		if _, err := authz.RequirePrimaryRoot(c); err != nil {
			return err
		}
	} else if _, err := authz.RequireRoot(c); err != nil {
		return err
	}

	workspaceMode, _ := c.Get("workspace_mode").(string)
	if !strings.EqualFold(strings.TrimSpace(workspaceMode), "platform") {
		return &authz.Failure{Status: http.StatusNotFound, Message: "Agent operations unavailable"}
	}
	if rawOrganization := c.Get("organization_id"); rawOrganization != nil {
		organizationID, ok := rawOrganization.(uuid.UUID)
		if !ok || organizationID != uuid.Nil {
			return &authz.Failure{Status: http.StatusNotFound, Message: "Agent operations unavailable"}
		}
	}
	return nil
}

func safeAgentDirectoryLabel(value string, maxRunes int) string {
	safe, _ := automationagent.RedactSourceExcerpt(strings.TrimSpace(value))
	if maxRunes <= 0 {
		return safe
	}
	runes := []rune(safe)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return safe
}

func canonicalOpaqueMachineID(value string) string {
	parsed, err := uuid.FromString(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil {
		return ""
	}
	return parsed.String()
}

func findActiveAgentProfile(db *gorm.DB, agentKey string) (*models.AutomationAgentProfile, error) {
	if db == nil || !agentProfileKeyPattern.MatchString(strings.TrimSpace(agentKey)) {
		return nil, gorm.ErrRecordNotFound
	}
	var profile models.AutomationAgentProfile
	if err := db.Where("agent_key = ? AND active = ?", strings.TrimSpace(agentKey), true).First(&profile).Error; err != nil {
		return nil, err
	}
	return &profile, nil
}

func profileOperations(profile *models.AutomationAgentProfile) map[string]struct{} {
	var values []string
	if profile == nil || json.Unmarshal([]byte(profile.OperationsJSON), &values) != nil {
		return map[string]struct{}{}
	}
	allowed := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, valid := allowedOperations[value]; valid {
			allowed[value] = struct{}{}
		}
	}
	return allowed
}

func profileSupportsCapabilities(profile *models.AutomationAgentProfile, capabilities []string) bool {
	allowed := profileOperations(profile)
	for _, capability := range capabilities {
		if _, ok := allowed[strings.TrimSpace(capability)]; !ok {
			return false
		}
	}
	return true
}

func profileSupportsOperation(profile *models.AutomationAgentProfile, operation string) bool {
	_, ok := profileOperations(profile)[strings.TrimSpace(operation)]
	return ok
}

func profileOperationList(profile *models.AutomationAgentProfile) []string {
	allowed := profileOperations(profile)
	operations := make([]string, 0, len(allowed))
	for operation := range allowed {
		operations = append(operations, operation)
	}
	sort.Strings(operations)
	return operations
}

func requestWorkerIdentity(request callbackRequest) automationagent.AgentIdentity {
	return automationagent.AgentIdentity{
		WorkerID: strings.TrimSpace(request.WorkerID), AgentKey: strings.TrimSpace(request.AgentKey), MachineID: strings.TrimSpace(request.MachineID),
	}
}

func validateAgentIdentity(identity automationagent.AgentIdentity, requireWorker bool) error {
	if requireWorker && identity.WorkerID == "" {
		return gorm.ErrRecordNotFound
	}
	if identity.WorkerID != "" {
		if workerID, err := uuid.FromString(identity.WorkerID); err != nil || workerID == uuid.Nil {
			return gorm.ErrRecordNotFound
		}
	}
	if identity.AgentKey != "" && !agentProfileKeyPattern.MatchString(identity.AgentKey) {
		return gorm.ErrRecordNotFound
	}
	if identity.MachineID != "" && !isOpaqueMachineID(identity.MachineID) {
		return gorm.ErrRecordNotFound
	}
	if identity.WorkerID == "" && (identity.AgentKey != "" || identity.MachineID != "") {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func validateAgentClaimIdentity(db *gorm.DB, taskID uuid.UUID, request callbackRequest) (automationagent.AgentIdentity, error) {
	identity := requestWorkerIdentity(request)
	if identity.WorkerID == "" && identity.AgentKey == "" && identity.MachineID == "" {
		return identity, nil // legacy worker callback; retain compatibility without attribution
	}
	if err := validateAgentIdentity(identity, true); err != nil {
		return automationagent.AgentIdentity{}, err
	}
	if identity.AgentKey == "" {
		identity.AgentKey = "generalist"
	}
	var task models.AutomationTask
	if err := db.Select("id", "operation").First(&task, taskID).Error; err != nil {
		return automationagent.AgentIdentity{}, err
	}
	profile, err := findActiveAgentProfile(db, identity.AgentKey)
	if err != nil || !profileSupportsOperation(profile, task.Operation) {
		return automationagent.AgentIdentity{}, gorm.ErrRecordNotFound
	}
	return identity, nil
}

func ledgerAgentIdentity(task *models.AutomationTask, request callbackRequest) (automationagent.AgentIdentity, error) {
	identity := automationagent.AgentIdentity{}
	if task != nil {
		identity = automationagent.AgentIdentity{WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID}
	}
	callbackIdentity := requestWorkerIdentity(request)
	if callbackIdentity.WorkerID != "" {
		if err := validateAgentIdentity(callbackIdentity, true); err != nil {
			return automationagent.AgentIdentity{}, err
		}
		if identity.WorkerID != "" && (identity.WorkerID != callbackIdentity.WorkerID || identity.AgentKey != callbackIdentity.AgentKey || identity.MachineID != callbackIdentity.MachineID) {
			return automationagent.AgentIdentity{}, gorm.ErrRecordNotFound
		}
		identity = callbackIdentity
	}
	if request.ExecutionIdentity != nil {
		origin := *request.ExecutionIdentity
		origin.WorkerID = strings.TrimSpace(origin.WorkerID)
		origin.AgentKey = strings.TrimSpace(origin.AgentKey)
		origin.MachineID = strings.TrimSpace(origin.MachineID)
		if err := validateAgentIdentity(origin, false); err != nil {
			return automationagent.AgentIdentity{}, err
		}
		if origin.WorkerID != "" {
			identity = origin
		}
	}
	return identity, nil
}

type agentDirectorySummary struct {
	ProfileCount      int   `json:"profile_count"`
	LiveInstances     int   `json:"live_instances"`
	ActiveRuns        int   `json:"active_runs"`
	AvailableSlots    int   `json:"available_slots"`
	AvailabilityKnown bool  `json:"availability_known"`
	QueuedTasks       int   `json:"queued_tasks"`
	Spend30dMicros    int64 `json:"spend_30d_microusd"`
}

type agentDirectoryScope struct {
	ClientID                 *uuid.UUID
	ClientIDs                []uuid.UUID
	ProjectID                *uuid.UUID
	AuthorizedProjectIDs     []uuid.UUID
	AuthorizedProjectsScoped bool
}

type agentDirectoryRun struct {
	TaskID        string     `json:"task_id"`
	RunID         string     `json:"run_id"`
	Operation     string     `json:"operation"`
	Status        string     `json:"status"`
	ClientID      string     `json:"client_id,omitempty"`
	ClientName    string     `json:"client_name,omitempty"`
	ProjectID     string     `json:"project_id,omitempty"`
	ProjectName   string     `json:"project_name,omitempty"`
	WorkItemID    string     `json:"work_item_id,omitempty"`
	WorkItemTitle string     `json:"work_item_title,omitempty"`
	EpicID        string     `json:"epic_id,omitempty"`
	EpicTitle     string     `json:"epic_title,omitempty"`
	StepKey       string     `json:"step_key,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
}

type agentDirectoryInstance struct {
	WorkerID        string              `json:"worker_id"`
	MachineID       string              `json:"machine_id,omitempty"`
	AgentInstanceID *uuid.UUID          `json:"agent_instance_id,omitempty"`
	Status          string              `json:"status"`
	Provider        string              `json:"provider"`
	Model           string              `json:"model"`
	Concurrency     int                 `json:"concurrency"`
	Draining        bool                `json:"draining"`
	Protocols       []string            `json:"protocols"`
	StartedAt       time.Time           `json:"started_at"`
	LastSeenAt      time.Time           `json:"last_seen_at"`
	ActiveRuns      []agentDirectoryRun `json:"active_runs"`
}

type agentDirectoryProfile struct {
	AgentKey       string                   `json:"agent_key"`
	Name           string                   `json:"name"`
	Specialty      string                   `json:"specialty"`
	Description    string                   `json:"description"`
	Capabilities   []string                 `json:"capabilities"`
	Operations     []string                 `json:"operations"`
	Active         bool                     `json:"active"`
	Status         string                   `json:"status"`
	InstanceCount  int                      `json:"instance_count"`
	ActiveRunCount int                      `json:"active_run_count"`
	TotalRuns30d   int                      `json:"total_runs_30d"`
	Spend30dMicros int64                    `json:"spend_30d_microusd"`
	Instances      []agentDirectoryInstance `json:"instances"`
}

type agentDirectoryQueueLane struct {
	Operation      string     `json:"operation"`
	QueuedTasks    int        `json:"queued_tasks"`
	OldestQueuedAt *time.Time `json:"oldest_queued_at,omitempty"`
}

type agentDirectoryResponse struct {
	SchemaVersion int                       `json:"schema_version"`
	GeneratedAt   time.Time                 `json:"generated_at"`
	Summary       agentDirectorySummary     `json:"summary"`
	Agents        []agentDirectoryProfile   `json:"agents"`
	QueueLanes    []agentDirectoryQueueLane `json:"queue_lanes"`
}

// GetAgentDirectory returns an operational snapshot in an authorized platform
// or organization scope. It never includes task inputs, prompts, object
// references, raw usage blobs, local hostnames or credentials.
func GetAgentDirectory(c echo.Context) error {
	workspaceMode, _ := c.Get("workspace_mode").(string)
	workspaceMode = strings.ToLower(strings.TrimSpace(workspaceMode))
	if workspaceMode == "platform" {
		if err := requireAgentPlatformWorkspace(c, false); err != nil {
			return authz.Respond(c, err)
		}
	} else if workspaceMode != "organization" {
		return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Agent directory unavailable", "Database is unavailable")
	}
	scope, status, err := parseAgentDirectoryScope(c, configuration.DB)
	if err != nil {
		return utils.Error(c, status, "Agent directory unavailable", "The selected client or project is unavailable")
	}
	if err := authorizeAgentDirectoryScope(c, configuration.DB, user, workspaceMode, &scope); err != nil {
		return err
	}
	response, err := buildAgentDirectory(c.Request().Context(), configuration.DB, time.Now().UTC(), scope)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Agent directory unavailable", "")
	}
	return utils.Success(c, http.StatusOK, "Automation agent directory", response)
}

func authorizeAgentDirectoryScope(c echo.Context, db *gorm.DB, user *models.User, workspaceMode string, scope *agentDirectoryScope) error {
	if scope == nil {
		return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
	}
	if workspaceMode == "platform" {
		if user == nil || !user.IsPlatformAdmin() {
			return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
		}
		return nil
	}
	organizationID, ok := c.Get("organization_id").(uuid.UUID)
	if !ok || organizationID == uuid.Nil {
		return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
	}
	if user == nil || !user.IsPlatformAdmin() {
		if scope.ClientID == nil && scope.ProjectID == nil {
			return utils.Error(c, http.StatusBadRequest, "Agent scope required", "client_id or project_id is required")
		}
	}
	var organizationClientIDs []uuid.UUID
	if scope.ClientID != nil || (scope.ClientID == nil && scope.ProjectID == nil) {
		ids, err := organizationscope.ClientIDs(db, organizationID)
		if err != nil {
			if errors.Is(err, organizationscope.ErrOrganizationNotFound) {
				return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
			}
			return utils.Error(c, http.StatusInternalServerError, "Agent directory unavailable", "")
		}
		organizationClientIDs = ids
	}
	if scope.ClientID != nil {
		if !containsAgentDirectoryClient(organizationClientIDs, *scope.ClientID) {
			return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
		}
	}
	if scope.ProjectID != nil {
		allowed, err := deliverycontroller.AuthorizeProjectView(c, *scope.ProjectID)
		if err != nil {
			return err
		}
		if !allowed {
			return echo.NewHTTPError(http.StatusNotFound)
		}
		if scope.ClientID != nil {
			var project models.DeliveryProject
			if err := db.Select("id", "client_id").First(&project, "id = ?", *scope.ProjectID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
				}
				return utils.Error(c, http.StatusInternalServerError, "Agent directory unavailable", "")
			}
			if !containsAgentDirectoryClient(organizationClientIDs, project.ClientID) {
				return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
			}
		}
	} else if user == nil || !user.IsPlatformAdmin() {
		projectIDs, err := deliverycontroller.VisibleProjectIDsForActor(c, user, scope.ClientIDs)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Agent directory unavailable", "")
		}
		if len(projectIDs) == 0 {
			return utils.Error(c, http.StatusNotFound, "Agent directory unavailable", "")
		}
		scope.AuthorizedProjectIDs = projectIDs
		scope.AuthorizedProjectsScoped = true
	} else if scope.ClientID == nil && scope.ProjectID == nil {
		// A platform administrator using an organization workspace gets that
		// organization only, never the platform-wide agent/activity totals.
		scope.ClientIDs = organizationClientIDs
	}
	return nil
}

func parseAgentDirectoryScope(c echo.Context, db *gorm.DB) (agentDirectoryScope, int, error) {
	scope := agentDirectoryScope{}
	for _, filter := range []struct {
		name   string
		target **uuid.UUID
	}{{"client_id", &scope.ClientID}, {"project_id", &scope.ProjectID}} {
		raw := strings.TrimSpace(c.QueryParam(filter.name))
		if raw == "" {
			continue
		}
		id, err := uuid.FromString(raw)
		if err != nil || id == uuid.Nil {
			return agentDirectoryScope{}, http.StatusBadRequest, gorm.ErrRecordNotFound
		}
		*filter.target = &id
	}
	if scope.ClientID != nil {
		ids, err := organizationscope.ClientIDs(db, *scope.ClientID)
		if err != nil {
			if errors.Is(err, organizationscope.ErrOrganizationNotFound) {
				return agentDirectoryScope{}, http.StatusNotFound, err
			}
			return agentDirectoryScope{}, http.StatusInternalServerError, err
		}
		scope.ClientIDs = ids
	}
	if scope.ProjectID != nil {
		var project models.DeliveryProject
		if err := db.Select("id", "client_id").First(&project, "id = ?", *scope.ProjectID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return agentDirectoryScope{}, http.StatusNotFound, err
			}
			return agentDirectoryScope{}, http.StatusInternalServerError, err
		}
		if len(scope.ClientIDs) > 0 && !containsAgentDirectoryClient(scope.ClientIDs, project.ClientID) {
			return agentDirectoryScope{}, http.StatusNotFound, gorm.ErrRecordNotFound
		}
	}
	return scope, http.StatusOK, nil
}

func containsAgentDirectoryClient(ids []uuid.UUID, clientID uuid.UUID) bool {
	for _, id := range ids {
		if id == clientID {
			return true
		}
	}
	return false
}

type agentDirectoryActiveRecord struct {
	TaskID        uuid.UUID  `gorm:"column:task_id"`
	RunID         string     `gorm:"column:run_id"`
	Operation     string     `gorm:"column:operation"`
	Status        string     `gorm:"column:status"`
	WorkerID      string     `gorm:"column:worker_id"`
	AgentKey      string     `gorm:"column:agent_key"`
	StepKey       string     `gorm:"column:step_key"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	StartedAt     *time.Time `gorm:"column:started_at"`
	ClientID      *uuid.UUID `gorm:"column:client_id"`
	ClientName    *string    `gorm:"column:client_name"`
	ProjectID     *uuid.UUID `gorm:"column:project_id"`
	ProjectName   *string    `gorm:"column:project_name"`
	WorkItemID    *uuid.UUID `gorm:"column:work_item_id"`
	WorkItemTitle *string    `gorm:"column:work_item_title"`
	EpicID        *uuid.UUID `gorm:"column:epic_id"`
	EpicTitle     *string    `gorm:"column:epic_title"`
}

type agentDirectoryLedgerRecord struct {
	AgentKey string `gorm:"column:agent_key"`
	Runs     int    `gorm:"column:run_count"`
	Cost     int64  `gorm:"column:spend_micros"`
}

type agentDirectoryQueuedRecord struct {
	Operation string     `gorm:"column:operation"`
	Count     int        `gorm:"column:count"`
	Oldest    *time.Time `gorm:"column:oldest"`
}

func buildAgentDirectory(ctx context.Context, db *gorm.DB, now time.Time, scope agentDirectoryScope) (agentDirectoryResponse, error) {
	// A 30-day operational window keeps stale process records bounded while
	// long-term execution and cost history remains in the immutable ledgers.
	cutoff := now.Add(-30 * 24 * time.Hour)
	var profiles []models.AutomationAgentProfile
	if err := db.WithContext(ctx).Order("agent_key ASC").Find(&profiles).Error; err != nil {
		return agentDirectoryResponse{}, err
	}
	var activeRecords []agentDirectoryActiveRecord
	activeQuery := db.WithContext(ctx).Table("automation_tasks AS t").
		Select("t.id AS task_id, t.run_id, t.operation, t.status, t.worker_id, t.agent_key, t.progress_step AS step_key, t.created_at, current_claim.occurred_at AS started_at, c.id AS client_id, c.name AS client_name, p.id AS project_id, p.name AS project_name, w.id AS work_item_id, w.title AS work_item_title, current_epic.id AS epic_id, current_epic.title AS epic_title").
		Joins("LEFT JOIN delivery_work_items AS w ON w.id = t.delivery_work_item_id").
		Joins("LEFT JOIN delivery_projects AS p ON p.id = w.project_id").
		Joins("LEFT JOIN clients AS c ON c.id = p.client_id").
		Joins(`LEFT JOIN LATERAL (
			SELECT event.occurred_at
			FROM automation_task_events AS event
			WHERE event.automation_task_id = t.id AND event.run_id = t.run_id
				AND event.worker_id = t.worker_id AND event.status = 'running'
				AND event.event_type IN ('claimed', 'lease_reclaimed')
			ORDER BY event.sequence DESC
			LIMIT 1
		) AS current_claim ON TRUE`).
		Joins(`LEFT JOIN LATERAL (
			SELECT epic.id, epic.title
			FROM delivery_epic_work_items AS membership
			JOIN delivery_epics AS epic ON epic.id = membership.epic_id AND epic.project_id = membership.project_id
			WHERE membership.work_item_id = w.id AND membership.project_id = w.project_id
				AND membership.deleted_at IS NULL AND w.deleted_at IS NULL
			ORDER BY membership.created_at DESC, membership.id DESC
			LIMIT 1
		) AS current_epic ON TRUE`).
		Where("t.status IN ?", []string{"running", "cancel_requested"})
	activeQuery = applyAgentDirectoryScope(activeQuery, scope)
	if err := activeQuery.Order("t.created_at ASC").Find(&activeRecords).Error; err != nil {
		return agentDirectoryResponse{}, err
	}
	var heartbeats []models.AutomationAgentHeartbeat
	heartbeatQuery := db.WithContext(ctx).Where("last_seen_at >= ?", cutoff)
	if scope.isScoped() {
		// Heartbeats have no client/project owner. Only include a worker when it
		// has an active run in the selected scope; otherwise machine availability
		// would be global data presented as project-specific state.
		workerIDs := uniqueDirectoryWorkerIDs(activeRecords)
		if len(workerIDs) == 0 {
			heartbeatQuery = nil
		} else {
			heartbeatQuery = heartbeatQuery.Where("worker_id IN ?", workerIDs)
		}
	}
	if heartbeatQuery != nil {
		if err := heartbeatQuery.Order("agent_key ASC, last_seen_at DESC").Find(&heartbeats).Error; err != nil {
			return agentDirectoryResponse{}, err
		}
	}
	var queued []agentDirectoryQueuedRecord
	queuedQuery := db.WithContext(ctx).Table("automation_tasks AS t").
		Select("t.operation, COUNT(*) AS count, MIN(t.created_at) AS oldest").
		Where("t.status = ?", "queued")
	if scope.isScoped() {
		queuedQuery = queuedQuery.Joins("JOIN delivery_work_items AS w ON w.id = t.delivery_work_item_id AND w.deleted_at IS NULL").
			Joins("JOIN delivery_projects AS p ON p.id = w.project_id AND p.deleted_at IS NULL")
		queuedQuery = applyAgentDirectoryScope(queuedQuery, scope)
	}
	if err := queuedQuery.Group("t.operation").Find(&queued).Error; err != nil {
		return agentDirectoryResponse{}, err
	}
	spendByAgent, runsByAgent, totalSpend, err := agentDirectoryUsage(ctx, db, cutoff, scope)
	if err != nil {
		return agentDirectoryResponse{}, err
	}

	response := agentDirectoryResponse{SchemaVersion: agentDirectorySchemaVersion, GeneratedAt: now, Agents: make([]agentDirectoryProfile, 0, len(profiles)), QueueLanes: make([]agentDirectoryQueueLane, 0, len(allowedOperations))}
	response.Summary.ProfileCount = len(profiles)
	response.Summary.ActiveRuns = len(activeRecords)
	response.Summary.Spend30dMicros = totalSpend
	response.Summary.AvailabilityKnown = !scope.isScoped()
	profileIndex := make(map[string]int, len(profiles))
	for _, profile := range profiles {
		var capabilities []string
		if json.Unmarshal([]byte(profile.CapabilitiesJSON), &capabilities) != nil || capabilities == nil {
			capabilities = []string{}
		}
		for index := range capabilities {
			capabilities[index] = safeAgentDirectoryLabel(capabilities[index], 96)
		}
		capabilities = uniqueSortedStrings(capabilities)
		operations := profileOperationList(&profile)
		response.Agents = append(response.Agents, agentDirectoryProfile{
			AgentKey: profile.AgentKey, Name: safeAgentDirectoryLabel(profile.Name, 128), Specialty: safeAgentDirectoryLabel(profile.Specialty, 160), Description: safeAgentDirectoryLabel(profile.Description, 512),
			Capabilities: capabilities, Operations: operations, Active: profile.Active, Status: "offline", TotalRuns30d: runsByAgent[profile.AgentKey], Spend30dMicros: spendByAgent[profile.AgentKey],
			Instances: []agentDirectoryInstance{},
		})
		profileIndex[profile.AgentKey] = len(response.Agents) - 1
	}
	instanceIndex := make(map[string]struct {
		profile  int
		instance int
	}, len(heartbeats))
	activeByWorker := make(map[string]int)
	for _, record := range activeRecords {
		if record.WorkerID != "" {
			activeByWorker[record.WorkerID]++
		}
	}
	for _, heartbeat := range heartbeats {
		index, exists := profileIndex[heartbeat.AgentKey]
		if !exists {
			continue
		}
		machineID := canonicalOpaqueMachineID(heartbeat.MachineID)
		status := "available"
		if !heartbeat.LastSeenAt.After(now.Add(-90 * time.Second)) {
			status = "offline"
		} else if heartbeat.Draining {
			status = "draining"
		} else if activeByWorker[heartbeat.WorkerID] > 0 {
			status = "working"
		}
		response.Agents[index].Instances = append(response.Agents[index].Instances, agentDirectoryInstance{
			WorkerID: heartbeat.WorkerID, MachineID: machineID, AgentInstanceID: heartbeat.AgentInstanceID, Status: status, Provider: safeAgentDirectoryLabel(heartbeat.Provider, 48), Model: safeAgentDirectoryLabel(heartbeat.Model, 128),
			Concurrency: heartbeat.Concurrency, Draining: heartbeat.Draining, Protocols: safeWorkerProtocolsJSON(heartbeat.ProtocolsJSON), StartedAt: heartbeat.StartedAt, LastSeenAt: heartbeat.LastSeenAt, ActiveRuns: []agentDirectoryRun{},
		})
		instanceIndex[heartbeat.WorkerID] = struct {
			profile  int
			instance int
		}{profile: index, instance: len(response.Agents[index].Instances) - 1}
		if status != "offline" {
			response.Summary.LiveInstances++
			if response.Summary.AvailabilityKnown && !heartbeat.Draining && heartbeat.Concurrency > activeByWorker[heartbeat.WorkerID] {
				response.Summary.AvailableSlots += heartbeat.Concurrency - activeByWorker[heartbeat.WorkerID]
			}
		}
	}
	for _, record := range activeRecords {
		ref, exists := instanceIndex[record.WorkerID]
		if !exists {
			continue
		}
		run := agentDirectoryRun{
			TaskID: record.TaskID.String(), RunID: safeAgentDirectoryLabel(record.RunID, 64),
			Operation: safeAgentDirectoryLabel(record.Operation, 96), Status: safeAgentDirectoryLabel(record.Status, 16),
			StepKey: safeAgentDirectoryLabel(record.StepKey, 64), StartedAt: record.StartedAt,
		}
		if record.ClientID != nil {
			run.ClientID = record.ClientID.String()
		}
		if record.ClientName != nil {
			run.ClientName = safeAgentDirectoryLabel(*record.ClientName, 160)
		}
		if record.ProjectID != nil {
			run.ProjectID = record.ProjectID.String()
		}
		if record.ProjectName != nil {
			run.ProjectName = safeAgentDirectoryLabel(*record.ProjectName, 160)
		}
		if record.WorkItemID != nil {
			run.WorkItemID = record.WorkItemID.String()
		}
		if record.WorkItemTitle != nil {
			run.WorkItemTitle = safeAgentDirectoryLabel(*record.WorkItemTitle, 512)
		}
		if record.EpicID != nil {
			run.EpicID = record.EpicID.String()
		}
		if record.EpicTitle != nil {
			run.EpicTitle = safeAgentDirectoryLabel(*record.EpicTitle, 180)
		}
		response.Agents[ref.profile].Instances[ref.instance].ActiveRuns = append(response.Agents[ref.profile].Instances[ref.instance].ActiveRuns, run)
		response.Agents[ref.profile].ActiveRunCount++
	}
	for profileIndex := range response.Agents {
		profile := &response.Agents[profileIndex]
		profile.InstanceCount = len(profile.Instances)
		profile.Status = profileDirectoryStatus(profile.Instances, profile.ActiveRunCount)
		if !response.Summary.AvailabilityKnown && profile.ActiveRunCount == 0 {
			profile.Status = "not_in_scope"
		}
		if !profiles[profileIndex].Active {
			profile.Status = "offline"
		}
	}
	queuedByOperation := make(map[string]agentDirectoryQueuedRecord, len(queued))
	for _, item := range queued {
		queuedByOperation[item.Operation] = item
		response.Summary.QueuedTasks += item.Count
	}
	operations := make([]string, 0, len(allowedOperations))
	for operation := range allowedOperations {
		operations = append(operations, operation)
	}
	sort.Strings(operations)
	for _, operation := range operations {
		lane := agentDirectoryQueueLane{Operation: operation}
		if item, ok := queuedByOperation[operation]; ok {
			lane.QueuedTasks, lane.OldestQueuedAt = item.Count, item.Oldest
		}
		response.QueueLanes = append(response.QueueLanes, lane)
	}
	return response, nil
}

func applyAgentDirectoryScope(query *gorm.DB, scope agentDirectoryScope) *gorm.DB {
	if len(scope.ClientIDs) > 0 {
		query = query.Where("p.client_id IN ?", scope.ClientIDs)
	}
	if scope.ProjectID != nil {
		query = query.Where("p.id = ?", *scope.ProjectID)
	}
	if scope.AuthorizedProjectsScoped {
		if len(scope.AuthorizedProjectIDs) == 0 {
			query = query.Where("1 = 0")
		} else {
			query = query.Where("p.id IN ?", scope.AuthorizedProjectIDs)
		}
	}
	return query
}

func (scope agentDirectoryScope) isScoped() bool {
	return scope.ClientID != nil || len(scope.ClientIDs) > 0 || scope.ProjectID != nil || scope.AuthorizedProjectsScoped
}

func uniqueDirectoryWorkerIDs(records []agentDirectoryActiveRecord) []string {
	seen := make(map[string]struct{}, len(records))
	workerIDs := make([]string, 0, len(records))
	for _, record := range records {
		workerID := strings.TrimSpace(record.WorkerID)
		if workerID == "" {
			continue
		}
		if _, exists := seen[workerID]; exists {
			continue
		}
		seen[workerID] = struct{}{}
		workerIDs = append(workerIDs, workerID)
	}
	return workerIDs
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

func profileDirectoryStatus(instances []agentDirectoryInstance, activeRuns int) string {
	if activeRuns > 0 {
		return "working"
	}
	available, draining, live := false, false, false
	for _, instance := range instances {
		if instance.Status == "offline" {
			continue
		}
		live = true
		if instance.Status == "draining" {
			draining = true
		} else {
			available = true
		}
	}
	if available {
		return "available"
	}
	if draining {
		return "draining"
	}
	if !live {
		return "offline"
	}
	return "offline"
}

func agentDirectoryUsage(ctx context.Context, db *gorm.DB, cutoff time.Time, scope agentDirectoryScope) (map[string]int64, map[string]int, int64, error) {
	costs := make(map[string]int64)
	runs := make(map[string]int)
	var records []agentDirectoryLedgerRecord
	query := `SELECT agent_key, COUNT(DISTINCT (automation_task_id, run_id)) AS run_count, COALESCE(SUM(total_cost_micros), 0) AS spend_micros
	FROM (
		SELECT agent_key, automation_task_id, run_id, total_cost_micros FROM automation_executions WHERE completed_at >= ?
		UNION ALL
		SELECT agent_key, automation_task_id, run_id, total_cost_micros FROM automation_tool_executions WHERE completed_at >= ?
	) AS ledger
	GROUP BY agent_key`
	args := []any{cutoff, cutoff}
	if scope.isScoped() {
		query = `SELECT ledger.agent_key, COUNT(DISTINCT (ledger.automation_task_id, ledger.run_id)) AS run_count,
			COALESCE(SUM(ledger.total_cost_micros), 0) AS spend_micros
		FROM (
			SELECT agent_key, automation_task_id, run_id, total_cost_micros FROM automation_executions WHERE completed_at >= ?
			UNION ALL
			SELECT agent_key, automation_task_id, run_id, total_cost_micros FROM automation_tool_executions WHERE completed_at >= ?
		) AS ledger
		JOIN automation_tasks AS t ON t.id = ledger.automation_task_id
		JOIN delivery_work_items AS w ON w.id = t.delivery_work_item_id AND w.deleted_at IS NULL
		JOIN delivery_projects AS p ON p.id = w.project_id AND p.deleted_at IS NULL
		WHERE TRUE`
		args = []any{cutoff, cutoff}
		if len(scope.ClientIDs) > 0 {
			query += " AND p.client_id IN ?"
			args = append(args, scope.ClientIDs)
		}
		if scope.ProjectID != nil {
			query += " AND p.id = ?"
			args = append(args, *scope.ProjectID)
		}
		if scope.AuthorizedProjectsScoped {
			if len(scope.AuthorizedProjectIDs) == 0 {
				query += " AND 1 = 0"
			} else {
				query += " AND p.id IN ?"
				args = append(args, scope.AuthorizedProjectIDs)
			}
		}
		query += " GROUP BY ledger.agent_key"
	}
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&records).Error; err != nil {
		return nil, nil, 0, err
	}
	var total int64
	for _, record := range records {
		total += record.Cost
		if strings.TrimSpace(record.AgentKey) == "" {
			continue
		}
		costs[record.AgentKey] = record.Cost
		runs[record.AgentKey] = record.Runs
	}
	return costs, runs, total, nil
}
