package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type normalizedWorkItemRequest struct {
	ProjectID        uuid.UUID
	RequestedBy      string
	RequestID        *uuid.UUID
	EpicID           *uuid.UUID
	ContextSourceIDs []uuid.UUID
	DependencyIDs    []uuid.UUID
	Request          workItemRequest
}

// normalizeWorkItemRequest is shared by the authenticated work-item endpoint
// and the recurring scheduler. The recurring caller additionally requires a
// positive per-run budget; ordinary one-off work keeps its legacy unmetered
// option.
func normalizeWorkItemRequest(projectID uuid.UUID, requestedBy string, request workItemRequest, requireBudget bool) (normalizedWorkItemRequest, error) {
	if projectID == uuid.Nil || strings.TrimSpace(requestedBy) == "" {
		return normalizedWorkItemRequest{}, fmt.Errorf("project and requesting actor are required")
	}
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.ExpectedOutcome) == "" {
		return normalizedWorkItemRequest{}, fmt.Errorf("title and expected_outcome are required")
	}
	if request.BudgetMicros < 0 || request.BudgetMicros > maxDeliveryTaskBudgetMicros || (requireBudget && request.BudgetMicros == 0) || (request.BudgetAlertPercent != 0 && (request.BudgetAlertPercent < 50 || request.BudgetAlertPercent > 100)) {
		return normalizedWorkItemRequest{}, fmt.Errorf("task budget must be greater than zero for recurring work and no more than 100,000 USD; alert percent must be between 50 and 100")
	}
	if request.MaxConcurrency != nil && (*request.MaxConcurrency < 1 || *request.MaxConcurrency > 8) {
		return normalizedWorkItemRequest{}, fmt.Errorf("max_concurrency must be between 1 and 8")
	}
	if request.BudgetAlertPercent == 0 {
		request.BudgetAlertPercent = defaultTaskBudgetAlertPercent
	}

	contextSourceIDs := make([]uuid.UUID, 0, len(request.ContextSourceIDs))
	seenSources := make(map[uuid.UUID]struct{}, len(request.ContextSourceIDs))
	for _, rawID := range request.ContextSourceIDs {
		parsed, err := uuid.FromString(strings.TrimSpace(rawID))
		if err != nil || parsed == uuid.Nil {
			return normalizedWorkItemRequest{}, fmt.Errorf("context_source_ids must contain UUIDs")
		}
		if _, exists := seenSources[parsed]; !exists {
			seenSources[parsed] = struct{}{}
			contextSourceIDs = append(contextSourceIDs, parsed)
		}
	}
	if len(contextSourceIDs) == 0 {
		return normalizedWorkItemRequest{}, fmt.Errorf("select at least one relevant context source")
	}

	dependencyIDs := make([]uuid.UUID, 0, len(request.DependsOnWorkItemIDs))
	seenDependencies := make(map[uuid.UUID]struct{}, len(request.DependsOnWorkItemIDs))
	for _, rawID := range request.DependsOnWorkItemIDs {
		parsed, err := uuid.FromString(strings.TrimSpace(rawID))
		if err != nil || parsed == uuid.Nil {
			return normalizedWorkItemRequest{}, fmt.Errorf("depends_on_work_item_ids must contain UUIDs")
		}
		if _, exists := seenDependencies[parsed]; !exists {
			seenDependencies[parsed] = struct{}{}
			dependencyIDs = append(dependencyIDs, parsed)
		}
	}

	var requestID *uuid.UUID
	if raw := strings.TrimSpace(request.RequestID); raw != "" {
		parsed, err := uuid.FromString(raw)
		if err != nil || parsed == uuid.Nil {
			return normalizedWorkItemRequest{}, fmt.Errorf("request_id must be a UUID")
		}
		requestID = &parsed
	}
	var epicID *uuid.UUID
	if raw := strings.TrimSpace(request.EpicID); raw != "" {
		parsed, err := uuid.FromString(raw)
		if err != nil || parsed == uuid.Nil {
			return normalizedWorkItemRequest{}, errWorkItemEpicIDInvalid
		}
		epicID = &parsed
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Description = strings.TrimSpace(request.Description)
	request.ExpectedOutcome = strings.TrimSpace(request.ExpectedOutcome)
	request.AssignedAgent = strings.TrimSpace(request.AssignedAgent)
	return normalizedWorkItemRequest{
		ProjectID: projectID, RequestedBy: strings.TrimSpace(requestedBy), RequestID: requestID, EpicID: epicID,
		ContextSourceIDs: contextSourceIDs, DependencyIDs: dependencyIDs, Request: request,
	}, nil
}

var errWorkItemContextNotReady = errors.New("selected context source is missing, outside the project, or not ready")
var errWorkItemEpicIDInvalid = errors.New("epic_id must be a UUID")
var errWorkItemEpicNotInProject = errors.New("selected epic is not available in this project")
var errWorkItemEpicContainsSensitiveMaterial = errors.New("selected epic contains disallowed sensitive material")

type epicSnapshotMetadata struct {
	EpicID  uuid.UUID `json:"epic_id"`
	Status  string    `json:"status"`
	Summary string    `json:"summary"`
}

// attachWorkItemToEpicInTransaction validates project ownership while holding
// the epic row lock, then records a fresh active association and an immutable
// context snapshot. The association ID is the snapshot source ID so moving a
// task away and later re-associating it cannot collide with the old snapshot.
func attachWorkItemToEpicInTransaction(tx *gorm.DB, input normalizedWorkItemRequest, itemID uuid.UUID, now time.Time) error {
	if input.EpicID == nil {
		return nil
	}
	var epic models.DeliveryEpic
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND project_id = ?", *input.EpicID, input.ProjectID).
		First(&epic).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errWorkItemEpicNotInProject
		}
		return err
	}
	if containsEpicSensitiveMaterial(epic.Title) || containsEpicSensitiveMaterial(epic.Summary) {
		return errWorkItemEpicContainsSensitiveMaterial
	}
	now = now.UTC()
	association := models.DeliveryEpicWorkItem{
		ProjectID: input.ProjectID, EpicID: epic.ID, WorkItemID: itemID,
		CreatedBy: input.RequestedBy, CreatedAt: now,
	}
	if err := tx.Create(&association).Error; err != nil {
		return err
	}
	metadata, err := json.Marshal(epicSnapshotMetadata{EpicID: epic.ID, Status: epic.Status, Summary: epic.Summary})
	if err != nil {
		return err
	}
	revision := epic.UpdatedAt.UTC().Format(time.RFC3339Nano)
	snapshot := models.DeliveryContextSnapshot{
		WorkItemID: itemID, SourceID: association.ID, Kind: "epic", Name: epic.Title,
		Reference: "epic://" + epic.ID.String(), Revision: revision,
		MetadataJSON: string(metadata), CapturedAt: now,
	}
	return tx.Create(&snapshot).Error
}

// createWorkItemInTransaction is the sole materialization path for both user
// requests and scheduled occurrences. It only creates a planning-stage item
// and a durable plan continuation; it never approves a plan or enqueues an
// implementation/provider job.
func createWorkItemInTransaction(tx *gorm.DB, input normalizedWorkItemRequest, now time.Time) (models.DeliveryWorkItem, error) {
	if tx == nil || input.ProjectID == uuid.Nil || strings.TrimSpace(input.RequestedBy) == "" {
		return models.DeliveryWorkItem{}, fmt.Errorf("transaction, project and requesting actor are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	request := input.Request
	included, _ := json.Marshal(request.IncludedScope)
	excluded, _ := json.Marshal(request.ExcludedScope)
	acceptance, _ := json.Marshal(request.AcceptanceCriteria)
	item := models.DeliveryWorkItem{
		ProjectID: input.ProjectID, RequestedBy: input.RequestedBy, RequestID: input.RequestID,
		AssignedAgent: strings.TrimSpace(request.AssignedAgent), Title: strings.TrimSpace(request.Title),
		Description: strings.TrimSpace(request.Description), ExpectedOutcome: strings.TrimSpace(request.ExpectedOutcome),
		IncludedScopeJSON: string(included), ExcludedScopeJSON: string(excluded), AcceptanceJSON: string(acceptance),
		BudgetMicros: request.BudgetMicros, BudgetAlertPercent: request.BudgetAlertPercent,
		State: deliveryworkflow.StatePlanning,
	}

	if input.RequestID != nil {
		var sourceRequest models.DeliveryRequest
		if err := tx.Where("id = ? AND project_id = ?", *input.RequestID, input.ProjectID).First(&sourceRequest).Error; err != nil {
			return models.DeliveryWorkItem{}, err
		}
	}
	clientContext, err := snapshotClientContext(tx, input.ProjectID)
	if err != nil {
		return models.DeliveryWorkItem{}, err
	}
	item.ClientContextJSON = clientContext
	if err := tx.Create(&item).Error; err != nil {
		return models.DeliveryWorkItem{}, err
	}
	if err := attachWorkItemToEpicInTransaction(tx, input, item.ID, now); err != nil {
		return models.DeliveryWorkItem{}, err
	}
	var sources []models.DeliveryContextSource
	if err := tx.Where("project_id = ? AND status = ? AND id IN ?", input.ProjectID, "ready", input.ContextSourceIDs).Find(&sources).Error; err != nil {
		return models.DeliveryWorkItem{}, err
	}
	if len(sources) != len(input.ContextSourceIDs) {
		return models.DeliveryWorkItem{}, errWorkItemContextNotReady
	}
	// Environment routing and the project's workflow contract are mandatory
	// task context. Keep caller-selected context scoped, but do not let a
	// recurring template omit current project-level policy.
	var operationalSources []models.DeliveryContextSource
	if err := tx.Where(
		"project_id = ? AND status = ? AND (kind = ? OR (kind = ? AND reference LIKE ?) OR (kind = 'repository' AND metadata_json ->> 'catalog_managed' = 'true'))",
		input.ProjectID, "ready", "environment", "runbook", "workflow://%",
	).Order("kind ASC, reference ASC").Find(&operationalSources).Error; err != nil {
		return models.DeliveryWorkItem{}, err
	}
	sources = appendMandatoryProjectContext(sources, operationalSources)
	repositoryRefs := make([]string, 0, len(sources))
	for _, source := range sources {
		if strings.EqualFold(strings.TrimSpace(source.Kind), "repository") {
			repositoryRefs = append(repositoryRefs, source.Reference)
		}
	}
	mandateValue := defaultDeliveryMandate(item, repositoryRefs)
	if request.MaxConcurrency != nil {
		mandateValue.MaxConcurrency = *request.MaxConcurrency
	}
	mandate, err := marshalDeliveryMandate(mandateValue)
	if err != nil {
		return models.DeliveryWorkItem{}, err
	}
	item.MandateVersion = deliveryMandateVersion
	item.MandateJSON = mandate
	snapshots, err := taskContextSnapshots(item.ID, sources, request.PrimaryRepositorySourceID, now)
	if err != nil {
		return models.DeliveryWorkItem{}, err
	}
	if len(snapshots) > 0 {
		if err := tx.Create(&snapshots).Error; err != nil {
			return models.DeliveryWorkItem{}, err
		}
	}
	if len(input.DependencyIDs) > 0 {
		var dependencies []models.DeliveryWorkItem
		if err := tx.Where("project_id = ? AND id IN ?", input.ProjectID, input.DependencyIDs).Find(&dependencies).Error; err != nil {
			return models.DeliveryWorkItem{}, err
		}
		if len(dependencies) != len(input.DependencyIDs) {
			return models.DeliveryWorkItem{}, fmt.Errorf("every dependency must belong to this delivery project")
		}
		links := make([]models.DeliveryWorkItemDependency, 0, len(input.DependencyIDs))
		for _, dependencyID := range input.DependencyIDs {
			links = append(links, models.DeliveryWorkItemDependency{WorkItemID: item.ID, DependsOnWorkItemID: dependencyID})
		}
		if err := tx.Create(&links).Error; err != nil {
			return models.DeliveryWorkItem{}, err
		}
	}
	if input.RequestID != nil {
		if err := tx.Model(&models.DeliveryRequest{}).Where("id = ? AND project_id = ?", *input.RequestID, input.ProjectID).Update("status", "planned").Error; err != nil {
			return models.DeliveryWorkItem{}, err
		}
	}
	item.AgentProgress = "queued"
	item.UpdatedAt = now
	if err := tx.Save(&item).Error; err != nil {
		return models.DeliveryWorkItem{}, err
	}
	if err := tx.Create(&models.DeliveryContinuation{
		WorkItemID: item.ID, Epoch: item.AutomationEpoch, Phase: "plan", RequestedBy: input.RequestedBy,
		Status: "pending", AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		return models.DeliveryWorkItem{}, err
	}
	return item, nil
}
