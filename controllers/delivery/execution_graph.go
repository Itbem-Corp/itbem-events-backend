package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/models"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// The graph is intentionally a read model over immutable task, execution,
// tool, gate and evidence records. It never becomes a second workflow source
// of truth: mutations still go through the existing delivery and automation
// endpoints, whose authorization and audit boundaries remain authoritative.
const (
	defaultExecutionGraphActivityLimit = 96
	maxExecutionGraphActivityLimit     = 240
)

// executionGraphSnapshot is the stable browser contract for a bounded
// delivery process graph. Nodes keep an opaque but deterministic graph ID and
// separately identify their source entity, so a reusable graph client can
// render, inspect and act without being coupled to Delivery table layouts.
//
// The response deliberately omits prompts, agent output, S3 references,
// provider usage blobs, human identities and message bodies. Those details
// remain behind their existing resource-specific, authorized endpoints.
type executionGraphSnapshot struct {
	SchemaVersion int                  `json:"schema_version"`
	WorkItemID    uuid.UUID            `json:"work_item_id"`
	Revision      string               `json:"revision"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Live          bool                 `json:"live"`
	Truncated     bool                 `json:"truncated"`
	Nodes         []executionGraphNode `json:"nodes"`
	Edges         []executionGraphEdge `json:"edges"`
}

// executionGraphNode contains presentation-safe metadata required by a
// generic execution graph. summary/detail are deliberately compact labels;
// an inspector can use entity_type/entity_id or action metadata to request
// more detail through a separately authorized endpoint.
type executionGraphNode struct {
	ID         string                   `json:"id"`
	Kind       string                   `json:"kind"`
	Status     string                   `json:"status"`
	Summary    string                   `json:"summary"`
	Detail     string                   `json:"detail,omitempty"`
	ParentID   string                   `json:"parent_id,omitempty"`
	TrackID    string                   `json:"track_id"`
	OccurredAt time.Time                `json:"occurred_at"`
	Entity     executionGraphNodeEntity `json:"entity"`
	Metadata   map[string]any           `json:"metadata,omitempty"`
	Actions    []executionGraphAction   `json:"actions"`
}

// executionGraphNodeEntity is intentionally resource-oriented instead of
// exposing a storage table name. It leaves room for other graph producers
// (incidents, costs or future agent runtime events) to use the same module.
type executionGraphNodeEntity struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// executionGraphAction tells the client which existing operation is relevant
// for a node. It is metadata, never a bypass: every mutation is reauthorized
// by the target endpoint when the action is actually invoked.
type executionGraphAction struct {
	ID                   string `json:"id"`
	TargetType           string `json:"target_type"`
	TargetID             string `json:"target_id"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

type executionGraphEdge struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Kind     string `json:"kind"`
	Status   string `json:"status,omitempty"`
}

type executionGraphBuildInput struct {
	WorkItem     models.DeliveryWorkItem
	Dependencies []models.DeliveryWorkItemDependency
	Tasks        []models.AutomationTask
	Executions   []models.AutomationExecution
	ToolCalls    []models.AutomationToolExecution
	Gates        []models.DeliveryGate
	Evidence     []models.DeliveryEvidence
	Messages     []models.DeliveryMessage
	ViewerID     string
	CanManage    bool
	Truncated    bool
	GeneratedAt  time.Time
}

// GetExecutionGraph exposes the graph projection for one Delivery work item.
// It shares GetWorkItem's deliveryView boundary, while action availability is
// tailored to the current viewer and remains subject to endpoint re-checks.
func GetExecutionGraph(c echo.Context) error {
	workItemID, err := id(c, "work item")
	if err != nil {
		return err
	}
	viewer, accessibleItem, err := workItemActor(c, workItemID, deliveryView)
	if err != nil {
		return err
	}
	activityLimit, err := executionGraphActivityLimit(c)
	if err != nil {
		return err
	}

	var item models.DeliveryWorkItem
	if err := configuration.DB.Select("id", "project_id", "title", "state", "created_at", "updated_at").First(&item, workItemID).Error; err != nil {
		return lookup(c, "Delivery work item", err)
	}

	canManage := viewer != nil && viewer.IsPlatformAdmin()
	if !canManage {
		if _, manageErr := projectActor(c, accessibleItem.ProjectID, deliveryManage); manageErr == nil {
			canManage = true
		}
	}

	input := executionGraphBuildInput{
		WorkItem:    item,
		ViewerID:    viewer.CognitoSub,
		CanManage:   canManage,
		GeneratedAt: time.Now().UTC(),
	}
	if err := loadExecutionGraphInput(workItemID, activityLimit, &input); err != nil {
		return utilsError(c, err)
	}
	snapshot := buildExecutionGraph(input)

	// The snapshot is frequently polled or streamed by a live graph client.
	// A content hash lets it avoid re-rendering an unchanged graph while the
	// private response remains revalidated for every request.
	etag := `"` + snapshot.Revision + `"`
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set(echo.HeaderCacheControl, "private, max-age=0, must-revalidate")
	if executionGraphETagMatches(c.Request().Header.Get("If-None-Match"), etag) {
		return c.NoContent(http.StatusNotModified)
	}
	return success(c, "Delivery execution graph", snapshot)
}

func executionGraphActivityLimit(c echo.Context) (int, error) {
	limit := defaultExecutionGraphActivityLimit
	raw := strings.TrimSpace(c.QueryParam("activity_limit"))
	if raw == "" {
		return limit, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 || parsed > maxExecutionGraphActivityLimit {
		return 0, badRequest(c, "Invalid execution graph limit", "activity_limit must be between 1 and 240")
	}
	return parsed, nil
}

// loadExecutionGraphInput reads only indexed, graph-safe columns. Each
// activity collection has the same bounded limit, so an unusually active task
// cannot make a live dashboard download an unbounded execution history.
func loadExecutionGraphInput(workItemID uuid.UUID, limit int, input *executionGraphBuildInput) error {
	if input == nil {
		return nil
	}
	limitPlusOne := limit + 1
	if err := configuration.DB.Where("work_item_id = ?", workItemID).
		Preload("DependsOn", func(db *gorm.DB) *gorm.DB { return db.Select("id", "title", "state", "created_at", "updated_at") }).
		Order("created_at DESC").Limit(limitPlusOne).Find(&input.Dependencies).Error; err != nil {
		return err
	}
	input.Dependencies, input.Truncated = trimExecutionGraphRows(input.Dependencies, limit, input.Truncated)

	if err := configuration.DB.Select("id", "requested_by", "delivery_work_item_id", "operation", "provider", "model", "status", "attempt_count", "completed_at", "created_at", "updated_at").Where("delivery_work_item_id = ?", workItemID).
		Order("created_at DESC").Limit(limitPlusOne).Find(&input.Tasks).Error; err != nil {
		return err
	}
	input.Tasks, input.Truncated = trimExecutionGraphRows(input.Tasks, limit, input.Truncated)

	if err := configuration.DB.Select("id", "automation_task_id", "delivery_work_item_id", "step_key", "provider", "model", "total_tokens", "total_cost_micros", "pricing_basis", "completed_at").Where("delivery_work_item_id = ?", workItemID).
		Order("completed_at DESC").Limit(limitPlusOne).Find(&input.Executions).Error; err != nil {
		return err
	}
	input.Executions, input.Truncated = trimExecutionGraphRows(input.Executions, limit, input.Truncated)

	if err := configuration.DB.Select("id", "automation_task_id", "delivery_work_item_id", "tool", "call_key", "call_status", "step_key", "provider", "model", "total_tokens", "total_cost_micros", "pricing_basis", "completed_at").Where("delivery_work_item_id = ?", workItemID).
		Order("completed_at DESC").Limit(limitPlusOne).Find(&input.ToolCalls).Error; err != nil {
		return err
	}
	input.ToolCalls, input.Truncated = trimExecutionGraphRows(input.ToolCalls, limit, input.Truncated)

	if err := configuration.DB.Select("id", "work_item_id", "kind", "decision", "decided_at", "created_at").Where("work_item_id = ?", workItemID).
		Order("decided_at DESC").Limit(limitPlusOne).Find(&input.Gates).Error; err != nil {
		return err
	}
	input.Gates, input.Truncated = trimExecutionGraphRows(input.Gates, limit, input.Truncated)

	if err := configuration.DB.Select("id", "work_item_id", "kind", "phase", "title", "metadata_json", "captured_at", "created_at").Where("work_item_id = ?", workItemID).
		Order("created_at DESC").Limit(limitPlusOne).Find(&input.Evidence).Error; err != nil {
		return err
	}
	input.Evidence, input.Truncated = trimExecutionGraphRows(input.Evidence, limit, input.Truncated)

	if err := configuration.DB.Select("id", "work_item_id", "phase", "author_type", "created_at").Where("work_item_id = ?", workItemID).
		Order("created_at DESC").Limit(limitPlusOne).Find(&input.Messages).Error; err != nil {
		return err
	}
	input.Messages, input.Truncated = trimExecutionGraphRows(input.Messages, limit, input.Truncated)
	return nil
}

func trimExecutionGraphRows[T any](values []T, limit int, alreadyTruncated bool) ([]T, bool) {
	if len(values) <= limit {
		return values, alreadyTruncated
	}
	return values[:limit], true
}

func buildExecutionGraph(input executionGraphBuildInput) executionGraphSnapshot {
	generatedAt := input.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	rootID := executionGraphWorkItemNodeID(input.WorkItem.ID)
	live := executionGraphIsLive(input.WorkItem.State, input.Tasks)
	nodes := []executionGraphNode{{
		ID:         rootID,
		Kind:       "work_item",
		Status:     executionGraphWorkItemStatus(input.WorkItem.State, live),
		Summary:    executionGraphText(input.WorkItem.Title, "Delivery"),
		Detail:     executionGraphWorkItemDetail(input.WorkItem.State),
		TrackID:    "workflow",
		OccurredAt: executionGraphOccurredAt(input.WorkItem.CreatedAt, input.WorkItem.UpdatedAt),
		Entity:     executionGraphNodeEntity{Type: "delivery_work_item", ID: input.WorkItem.ID.String()},
		Metadata: map[string]any{
			"state": strings.TrimSpace(input.WorkItem.State),
		},
		Actions: []executionGraphAction{{ID: "inspect", TargetType: "delivery_work_item", TargetID: input.WorkItem.ID.String()}},
	}}
	edges := make([]executionGraphEdge, 0, len(input.Dependencies)+len(input.Tasks)+len(input.Executions)+len(input.ToolCalls)+len(input.Gates)+len(input.Evidence)+len(input.Messages))
	taskByID := make(map[uuid.UUID]models.AutomationTask, len(input.Tasks))

	for _, dependency := range input.Dependencies {
		dependencyID := executionGraphDependencyNodeID(dependency.DependsOnWorkItemID)
		state := strings.TrimSpace(dependency.DependsOn.State)
		nodes = append(nodes, executionGraphNode{
			ID:         dependencyID,
			Kind:       "dependency",
			Status:     executionGraphDependencyStatus(state),
			Summary:    executionGraphText(dependency.DependsOn.Title, "Dependencia"),
			Detail:     executionGraphWorkItemDetail(state),
			ParentID:   rootID,
			TrackID:    "dependencies",
			OccurredAt: executionGraphOccurredAt(dependency.DependsOn.CreatedAt, dependency.CreatedAt),
			Entity:     executionGraphNodeEntity{Type: "delivery_work_item", ID: dependency.DependsOnWorkItemID.String()},
			Metadata:   map[string]any{"state": state},
			Actions:    []executionGraphAction{{ID: "open_work_item", TargetType: "delivery_work_item", TargetID: dependency.DependsOnWorkItemID.String()}},
		})
		edges = append(edges, executionGraphEdge{ID: executionGraphEdgeID(dependencyID, rootID, "depends_on"), SourceID: dependencyID, TargetID: rootID, Kind: "depends_on", Status: executionGraphDependencyStatus(state)})
	}

	for _, task := range input.Tasks {
		taskByID[task.ID] = task
		taskID := executionGraphTaskNodeID(task.ID)
		status := executionGraphTaskStatus(task.Status)
		metadata := map[string]any{
			"operation":     strings.TrimSpace(task.Operation),
			"attempt_count": task.AttemptCount,
		}
		if provider := strings.TrimSpace(task.Provider); provider != "" {
			metadata["provider"] = provider
		}
		if model := strings.TrimSpace(task.Model); model != "" {
			metadata["model"] = model
		}
		nodes = append(nodes, executionGraphNode{
			ID:         taskID,
			Kind:       "task",
			Status:     status,
			Summary:    executionGraphOperationSummary(task.Operation),
			Detail:     executionGraphTaskDetail(task.AttemptCount, task.Status),
			ParentID:   rootID,
			TrackID:    executionGraphTaskTrack(task.Operation),
			OccurredAt: executionGraphOccurredAt(task.CreatedAt, task.UpdatedAt, executionGraphOptionalTime(task.CompletedAt)),
			Entity:     executionGraphNodeEntity{Type: "automation_task", ID: task.ID.String()},
			Metadata:   metadata,
			Actions:    executionGraphTaskActions(task, input.ViewerID, input.CanManage),
		})
		edges = append(edges, executionGraphEdge{ID: executionGraphEdgeID(rootID, taskID, "starts"), SourceID: rootID, TargetID: taskID, Kind: "starts", Status: status})
	}

	for _, execution := range input.Executions {
		parentID, trackID, status := rootID, "agent", "completed"
		if task, ok := taskByID[execution.AutomationTaskID]; ok {
			parentID, trackID = executionGraphTaskNodeID(task.ID), executionGraphTaskTrack(task.Operation)
			status = executionGraphExecutionStatus(task.Status)
		}
		nodeID := executionGraphAgentExecutionNodeID(execution.ID)
		nodes = append(nodes, executionGraphNode{
			ID:         nodeID,
			Kind:       "execution",
			Status:     status,
			Summary:    executionGraphStepSummary(execution.StepKey, "Ejecución del agente"),
			Detail:     executionGraphRuntimeDetail(execution.Provider, execution.Model),
			ParentID:   parentID,
			TrackID:    trackID,
			OccurredAt: execution.CompletedAt.UTC(),
			Entity:     executionGraphNodeEntity{Type: "automation_execution", ID: execution.ID.String()},
			Metadata: map[string]any{
				"step_key":            strings.TrimSpace(execution.StepKey),
				"provider":            strings.TrimSpace(execution.Provider),
				"model":               strings.TrimSpace(execution.Model),
				"total_tokens":        execution.TotalTokens,
				"total_cost_microusd": execution.TotalCostMicros,
				"pricing_basis":       strings.TrimSpace(execution.PricingBasis),
			},
			Actions: []executionGraphAction{
				{ID: "inspect", TargetType: "automation_execution", TargetID: execution.ID.String()},
				{ID: "open_execution", TargetType: "automation_execution", TargetID: execution.ID.String()},
			},
		})
		edges = append(edges, executionGraphEdge{ID: executionGraphEdgeID(parentID, nodeID, "executes"), SourceID: parentID, TargetID: nodeID, Kind: "executes", Status: status})
	}

	for _, toolCall := range input.ToolCalls {
		parentID, trackID := rootID, "tools"
		if task, ok := taskByID[toolCall.AutomationTaskID]; ok {
			parentID, trackID = executionGraphTaskNodeID(task.ID), executionGraphTaskTrack(task.Operation)
		}
		status := executionGraphToolCallStatus(toolCall.CallStatus)
		nodeID := executionGraphToolExecutionNodeID(toolCall.ID)
		actions := []executionGraphAction{{ID: "inspect", TargetType: "automation_tool_execution", TargetID: toolCall.ID.String()}}
		if strings.EqualFold(strings.TrimSpace(toolCall.Tool), "stagehand") {
			actions = append(actions, executionGraphAction{ID: "open_tool_report", TargetType: "automation_tool_execution", TargetID: toolCall.ID.String()})
		}
		nodes = append(nodes, executionGraphNode{
			ID:         nodeID,
			Kind:       "tool_call",
			Status:     status,
			Summary:    executionGraphText(toolCall.Tool, "Herramienta"),
			Detail:     executionGraphText(toolCall.CallKey, executionGraphStepSummary(toolCall.StepKey, "Llamada de herramienta")),
			ParentID:   parentID,
			TrackID:    trackID,
			OccurredAt: toolCall.CompletedAt.UTC(),
			Entity:     executionGraphNodeEntity{Type: "automation_tool_execution", ID: toolCall.ID.String()},
			Metadata: map[string]any{
				"tool":                strings.TrimSpace(toolCall.Tool),
				"call_key":            strings.TrimSpace(toolCall.CallKey),
				"call_status":         strings.TrimSpace(toolCall.CallStatus),
				"step_key":            strings.TrimSpace(toolCall.StepKey),
				"provider":            strings.TrimSpace(toolCall.Provider),
				"model":               strings.TrimSpace(toolCall.Model),
				"total_tokens":        toolCall.TotalTokens,
				"total_cost_microusd": toolCall.TotalCostMicros,
				"pricing_basis":       strings.TrimSpace(toolCall.PricingBasis),
			},
			Actions: actions,
		})
		edges = append(edges, executionGraphEdge{ID: executionGraphEdgeID(parentID, nodeID, "uses_tool"), SourceID: parentID, TargetID: nodeID, Kind: "uses_tool", Status: status})
	}

	for _, gate := range input.Gates {
		nodeID := executionGraphGateNodeID(gate.ID)
		status := executionGraphGateStatus(gate.Decision)
		nodes = append(nodes, executionGraphNode{
			ID:         nodeID,
			Kind:       "gate",
			Status:     status,
			Summary:    "Gate · " + executionGraphText(gate.Kind, "Revisión"),
			Detail:     executionGraphGateDetail(gate.Decision),
			ParentID:   rootID,
			TrackID:    "decisions",
			OccurredAt: executionGraphOccurredAt(gate.DecidedAt, gate.CreatedAt),
			Entity:     executionGraphNodeEntity{Type: "delivery_gate", ID: gate.ID.String()},
			Metadata: map[string]any{
				"kind":     strings.TrimSpace(gate.Kind),
				"decision": strings.TrimSpace(gate.Decision),
			},
			Actions: []executionGraphAction{{ID: "inspect", TargetType: "delivery_gate", TargetID: gate.ID.String()}},
		})
		edges = append(edges, executionGraphEdge{ID: executionGraphEdgeID(rootID, nodeID, "decision"), SourceID: rootID, TargetID: nodeID, Kind: "decision", Status: status})
	}

	for _, evidence := range input.Evidence {
		parentID, trackID := rootID, "evidence"
		if taskID, ok := executionGraphEvidenceTaskID(evidence.MetadataJSON); ok {
			if _, exists := taskByID[taskID]; exists {
				parentID = executionGraphTaskNodeID(taskID)
				trackID = executionGraphTaskTrack(taskByID[taskID].Operation)
			}
		}
		nodeID := executionGraphEvidenceNodeID(evidence.ID)
		nodes = append(nodes, executionGraphNode{
			ID:         nodeID,
			Kind:       "evidence",
			Status:     "completed",
			Summary:    executionGraphText(evidence.Title, "Evidencia"),
			Detail:     executionGraphEvidenceDetail(evidence.Phase, evidence.Kind),
			ParentID:   parentID,
			TrackID:    trackID,
			OccurredAt: executionGraphOccurredAt(executionGraphOptionalTime(evidence.CapturedAt), evidence.CreatedAt),
			Entity:     executionGraphNodeEntity{Type: "delivery_evidence", ID: evidence.ID.String()},
			Metadata: map[string]any{
				"kind":  strings.TrimSpace(evidence.Kind),
				"phase": strings.TrimSpace(evidence.Phase),
			},
			Actions: []executionGraphAction{{ID: "inspect", TargetType: "delivery_evidence", TargetID: evidence.ID.String()}},
		})
		edges = append(edges, executionGraphEdge{ID: executionGraphEdgeID(parentID, nodeID, "produces_evidence"), SourceID: parentID, TargetID: nodeID, Kind: "produces_evidence", Status: "completed"})
	}

	for _, message := range input.Messages {
		nodeID := executionGraphMessageNodeID(message.ID)
		nodes = append(nodes, executionGraphNode{
			ID:         nodeID,
			Kind:       "message",
			Status:     "decision",
			Summary:    executionGraphMessageSummary(message.AuthorType),
			Detail:     executionGraphText(message.Phase, "Contexto"),
			ParentID:   rootID,
			TrackID:    "context",
			OccurredAt: message.CreatedAt.UTC(),
			Entity:     executionGraphNodeEntity{Type: "delivery_message", ID: message.ID.String()},
			Metadata: map[string]any{
				"phase":       strings.TrimSpace(message.Phase),
				"author_type": strings.TrimSpace(message.AuthorType),
			},
			Actions: []executionGraphAction{{ID: "inspect", TargetType: "delivery_message", TargetID: message.ID.String()}},
		})
		edges = append(edges, executionGraphEdge{ID: executionGraphEdgeID(rootID, nodeID, "adds_context"), SourceID: rootID, TargetID: nodeID, Kind: "adds_context", Status: "decision"})
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].OccurredAt.Equal(nodes[j].OccurredAt) {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].OccurredAt.Before(nodes[j].OccurredAt)
	})
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	revision := executionGraphRevision(nodes, edges, input.Truncated)
	return executionGraphSnapshot{
		SchemaVersion: 1,
		WorkItemID:    input.WorkItem.ID,
		Revision:      revision,
		GeneratedAt:   generatedAt,
		Live:          live,
		Truncated:     input.Truncated,
		Nodes:         nodes,
		Edges:         edges,
	}
}

func executionGraphWorkItemNodeID(id uuid.UUID) string       { return "work-item:" + id.String() }
func executionGraphDependencyNodeID(id uuid.UUID) string     { return "dependency:" + id.String() }
func executionGraphTaskNodeID(id uuid.UUID) string           { return "task:" + id.String() }
func executionGraphAgentExecutionNodeID(id uuid.UUID) string { return "execution:" + id.String() }
func executionGraphToolExecutionNodeID(id uuid.UUID) string  { return "tool-call:" + id.String() }
func executionGraphGateNodeID(id uuid.UUID) string           { return "gate:" + id.String() }
func executionGraphEvidenceNodeID(id uuid.UUID) string       { return "evidence:" + id.String() }
func executionGraphMessageNodeID(id uuid.UUID) string        { return "message:" + id.String() }

func executionGraphEdgeID(sourceID, targetID, kind string) string {
	return kind + ":" + sourceID + ":" + targetID
}

func executionGraphTaskTrack(operation string) string {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return "agent"
	}
	return operation
}

func executionGraphTaskActions(task models.AutomationTask, viewerID string, canManage bool) []executionGraphAction {
	actions := []executionGraphAction{
		{ID: "inspect", TargetType: "automation_task", TargetID: task.ID.String()},
		{ID: "open_trace", TargetType: "automation_task", TargetID: task.ID.String()},
	}
	status := strings.ToLower(strings.TrimSpace(task.Status))
	if status == "completed" || status == "failed" || status == "cancelled" {
		actions = append(actions, executionGraphAction{ID: "open_result", TargetType: "automation_task", TargetID: task.ID.String()})
	}
	if (status == "queued" || status == "running") && (canManage || (viewerID != "" && viewerID == task.RequestedBy)) {
		actions = append(actions, executionGraphAction{ID: "cancel", TargetType: "automation_task", TargetID: task.ID.String(), RequiresConfirmation: true})
	}
	return actions
}

func executionGraphTaskStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued":
		return "queued"
	case "running":
		return "running"
	case "completed":
		return "completed"
	case "failed", "dispatch_failed":
		return "failed"
	case "cancel_requested":
		return "attention"
	case "cancelled":
		return "cancelled"
	default:
		return "unknown"
	}
}

// An immutable execution represents a completed provider call even while its
// parent task later continues to another step. Only a terminal/cancelling task
// status changes the visual outcome of that particular ledger node.
func executionGraphExecutionStatus(taskStatus string) string {
	switch executionGraphTaskStatus(taskStatus) {
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	case "attention":
		return "attention"
	default:
		return "completed"
	}
}

func executionGraphToolCallStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}

func executionGraphGateStatus(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "approved":
		return "completed"
	case "changes_requested":
		return "attention"
	default:
		return "decision"
	}
}

func executionGraphDependencyStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "released":
		return "completed"
	case "blocked":
		return "blocked"
	case "cancelled":
		return "cancelled"
	default:
		return "waiting"
	}
}

func executionGraphWorkItemStatus(state string, live bool) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "released":
		return "completed"
	case "blocked":
		return "blocked"
	case "cancelled":
		return "cancelled"
	case "plan_review", "code_review", "qa_review", "release_review":
		return "decision"
	case "planning", "implementation", "preview_pending", "qa_running":
		if live {
			return "running"
		}
		return "waiting"
	default:
		return "unknown"
	}
}

func executionGraphIsLive(state string, tasks []models.AutomationTask) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "released", "blocked", "cancelled":
		return false
	}
	for _, task := range tasks {
		switch strings.ToLower(strings.TrimSpace(task.Status)) {
		case "queued", "running", "cancel_requested":
			return true
		}
	}
	return false
}

func executionGraphWorkItemDetail(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "planning":
		return "Preparando"
	case "plan_review":
		return "Esperando revisión"
	case "implementation":
		return "Implementando"
	case "code_review":
		return "Esperando revisión de código"
	case "preview_pending":
		return "Esperando preview"
	case "qa_running":
		return "Validando"
	case "qa_review":
		return "Esperando revisión QA"
	case "release_review":
		return "Esperando liberación"
	case "released":
		return "Liberado"
	case "blocked":
		return "Bloqueado"
	case "cancelled":
		return "Cancelado"
	default:
		return "Estado pendiente"
	}
}

func executionGraphOperationSummary(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "delivery.plan":
		return "Plan"
	case "delivery.implementation":
		return "Implementación"
	case "delivery.qa":
		return "QA"
	case "delivery.publish":
		return "Publicación"
	case "delivery.summary":
		return "Resumen de entrega"
	default:
		return executionGraphText(operation, "Automatización")
	}
}

func executionGraphStepSummary(stepKey, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(stepKey)) {
	case "plan":
		return "Plan"
	case "implementation":
		return "Implementación"
	case "qa", "qa.semantic_browser":
		return "QA"
	case "publish":
		return "Publicación"
	case "summary":
		return "Resumen de entrega"
	default:
		return executionGraphText(stepKey, fallback)
	}
}

func executionGraphTaskDetail(attemptCount int, status string) string {
	if attemptCount > 1 {
		return strconv.Itoa(attemptCount) + " intentos"
	}
	switch executionGraphTaskStatus(status) {
	case "queued":
		return "En cola"
	case "running":
		return "En curso"
	case "completed":
		return "Completado"
	case "failed":
		return "Requiere atención"
	case "attention":
		return "Cancelación solicitada"
	case "cancelled":
		return "Cancelado"
	default:
		return "Estado pendiente"
	}
}

func executionGraphRuntimeDetail(provider, model string) string {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if provider == "" && model == "" {
		return "Ejecución registrada"
	}
	if provider == "" {
		return executionGraphText(model, "Ejecución registrada")
	}
	if model == "" {
		return executionGraphText(provider, "Ejecución registrada")
	}
	return executionGraphText(provider+" · "+model, "Ejecución registrada")
}

func executionGraphGateDetail(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "approved":
		return "Aprobado"
	case "changes_requested":
		return "Cambios solicitados"
	default:
		return "Decisión registrada"
	}
}

func executionGraphEvidenceDetail(phase, kind string) string {
	phase, kind = strings.TrimSpace(phase), strings.TrimSpace(kind)
	if phase == "" {
		return executionGraphText(kind, "Evidencia")
	}
	if kind == "" {
		return executionGraphText(phase, "Evidencia")
	}
	return executionGraphText(phase+" · "+kind, "Evidencia")
}

func executionGraphMessageSummary(authorType string) string {
	switch strings.ToLower(strings.TrimSpace(authorType)) {
	case "human":
		return "Contexto humano"
	case "agent":
		return "Contexto del agente"
	default:
		return "Contexto"
	}
}

func executionGraphText(value, fallback string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return fallback
	}
	const maxRunes = 240
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

func executionGraphOccurredAt(candidates ...time.Time) time.Time {
	for _, candidate := range candidates {
		if !candidate.IsZero() {
			return candidate.UTC()
		}
	}
	return time.Unix(0, 0).UTC()
}

func executionGraphOptionalTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

// executionGraphEvidenceTaskID reads one allow-listed linkage that the
// automation callback already persists with its QA artifacts. It ignores all
// other evidence metadata (including object refs, notes and arbitrary user
// metadata) so a generic graph never becomes a metadata exfiltration path.
func executionGraphEvidenceTaskID(raw string) (uuid.UUID, bool) {
	var metadata struct {
		AutomationTaskID string `json:"automation_task_id"`
	}
	if json.Unmarshal([]byte(raw), &metadata) != nil {
		return uuid.Nil, false
	}
	id, err := uuid.FromString(strings.TrimSpace(metadata.AutomationTaskID))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

func executionGraphRevision(nodes []executionGraphNode, edges []executionGraphEdge, truncated bool) string {
	// Do not hash generated_at or per-viewer action availability: a graph that
	// has not changed in storage should retain the same revision for every
	// eligible viewer, allowing a client to avoid layout work on 304 responses.
	parts := make([]string, 0, len(nodes)+len(edges)+1)
	for _, node := range nodes {
		parts = append(parts, strings.Join([]string{
			node.ID,
			node.Kind,
			node.Status,
			node.Summary,
			node.Detail,
			node.ParentID,
			node.TrackID,
			node.OccurredAt.UTC().Format(time.RFC3339Nano),
		}, "\x00"))
	}
	for _, edge := range edges {
		parts = append(parts, strings.Join([]string{"edge", edge.ID, edge.SourceID, edge.TargetID, edge.Kind, edge.Status}, "\x00"))
	}
	if truncated {
		parts = append(parts, "truncated")
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func executionGraphETagMatches(header, expected string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == expected || candidate == "W/"+expected {
			return true
		}
	}
	return false
}
