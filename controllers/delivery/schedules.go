package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultDeliverySchedulePageSize = 25
	maxDeliverySchedulePageSize     = 100
	maxDeliveryScheduleOffset       = 10000
)

type deliveryScheduleDTO struct {
	ID           uuid.UUID                  `json:"id"`
	ProjectID    uuid.UUID                  `json:"project_id"`
	Name         string                     `json:"name"`
	Status       string                     `json:"status"`
	Revision     int                        `json:"revision"`
	Template     deliveryScheduleTemplate   `json:"template"`
	Recurrence   deliveryScheduleRecurrence `json:"recurrence"`
	NextRunAt    *time.Time                 `json:"next_run_at,omitempty"`
	NextRunLocal string                     `json:"next_run_local,omitempty"`
	LastRunAt    *time.Time                 `json:"last_run_at,omitempty"`
	PausedAt     *time.Time                 `json:"paused_at,omitempty"`
	CreatedAt    time.Time                  `json:"created_at"`
	UpdatedAt    time.Time                  `json:"updated_at"`
}

type deliveryScheduleListResponse struct {
	Items      []deliveryScheduleDTO `json:"items"`
	Limit      int                   `json:"limit"`
	Offset     int                   `json:"offset"`
	NextOffset *int                  `json:"next_offset,omitempty"`
}

type deliveryScheduleOccurrenceDTO struct {
	ID               uuid.UUID  `json:"id"`
	ScheduleID       uuid.UUID  `json:"schedule_id"`
	ScheduleRevision int        `json:"schedule_revision"`
	OccurrenceKey    string     `json:"occurrence_key"`
	ScheduledFor     time.Time  `json:"scheduled_for"`
	LocalOccurrence  string     `json:"local_occurrence"`
	TimeZone         string     `json:"time_zone"`
	Status           string     `json:"status"`
	WorkItemID       *uuid.UUID `json:"work_item_id,omitempty"`
	FailureCode      string     `json:"failure_code,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	MaterializedAt   *time.Time `json:"materialized_at,omitempty"`
}

type deliveryScheduleEventDTO struct {
	ID           uuid.UUID  `json:"id"`
	ScheduleID   uuid.UUID  `json:"schedule_id"`
	OccurrenceID *uuid.UUID `json:"occurrence_id,omitempty"`
	WorkItemID   *uuid.UUID `json:"work_item_id,omitempty"`
	EventType    string     `json:"event_type"`
	ActorSubject string     `json:"actor_subject,omitempty"`
	OccurredAt   time.Time  `json:"occurred_at"`
}

type deliverySchedulePageResponse[T any] struct {
	Items      []T  `json:"items"`
	Limit      int  `json:"limit"`
	Offset     int  `json:"offset"`
	NextOffset *int `json:"next_offset,omitempty"`
}

func CreateSchedule(c echo.Context) error {
	projectID, err := parseDeliveryScheduleID(c, "projectId", "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryManage)
	if err != nil {
		return err
	}
	if err := projectPresent(projectID); err != nil {
		return lookup(c, "Delivery project", err)
	}
	var request deliveryScheduleCreateRequest
	if err := c.Bind(&request); err != nil {
		return badRequest(c, "Invalid recurring schedule", "request body is invalid")
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 180 {
		return badRequest(c, "Invalid recurring schedule", "name is required and must be at most 180 characters")
	}
	recurrence, _, err := normalizeDeliveryScheduleRecurrence(request.Recurrence)
	if err != nil {
		return badRequest(c, "Invalid recurring schedule", err.Error())
	}
	now := time.Now().UTC()
	nextRun, _, err := nextDeliveryScheduleOccurrence(recurrence, now)
	if err != nil {
		return badRequest(c, "Invalid recurring schedule", err.Error())
	}
	if nextRun == nil {
		return badRequest(c, "Invalid recurring schedule", "recurrence has no future occurrence")
	}
	template, input, err := normalizeDeliveryScheduleTemplate(projectID, actor.CognitoSub, request.Template)
	if err != nil {
		return badRequest(c, "Invalid recurring schedule", err.Error())
	}
	var schedule models.DeliveryAutomationSchedule
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		if err := validateDeliveryScheduleTemplateSources(tx, projectID, template, input); err != nil {
			return err
		}
		templateJSON, err := json.Marshal(template)
		if err != nil {
			return err
		}
		recurrenceJSON, err := json.Marshal(recurrence)
		if err != nil {
			return err
		}
		schedule = models.DeliveryAutomationSchedule{
			ProjectID: projectID, Name: name, Status: models.DeliveryAutomationScheduleActive, Revision: 1,
			TemplateJSON: string(templateJSON), RecurrenceJSON: string(recurrenceJSON), TimeZone: recurrence.TimeZone,
			NextRunAt: nextRun, CreatedBy: actor.CognitoSub, UpdatedBy: actor.CognitoSub, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&schedule).Error; err != nil {
			return err
		}
		return appendDeliveryScheduleEvent(tx, schedule.ID, nil, nil, "created", actor.CognitoSub, map[string]any{"revision": schedule.Revision, "status": schedule.Status}, now)
	})
	if err != nil {
		if errors.Is(err, errWorkItemContextNotReady) || errors.Is(err, errInvalidTaskPrimaryRepository) {
			return badRequest(c, "Invalid recurring schedule", err.Error())
		}
		return utilsError(c, err)
	}
	return created(c, "Recurring schedule created", projectDeliveryScheduleDTO(schedule))
}

func ListSchedules(c echo.Context) error {
	projectID, err := parseDeliveryScheduleID(c, "projectId", "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	if err := projectPresent(projectID); err != nil {
		return lookup(c, "Delivery project", err)
	}
	limit, offset, err := deliverySchedulePagination(c)
	if err != nil {
		return badRequest(c, "Invalid recurring schedule pagination", err.Error())
	}
	var rows []models.DeliveryAutomationSchedule
	if err := configuration.DB.Where("project_id = ?", projectID).Order("created_at DESC, id DESC").Limit(limit + 1).Offset(offset).Find(&rows).Error; err != nil {
		return utilsError(c, err)
	}
	response := deliveryScheduleListResponse{Items: make([]deliveryScheduleDTO, 0, limit), Limit: limit, Offset: offset}
	if len(rows) > limit {
		rows = rows[:limit]
		next := offset + limit
		response.NextOffset = &next
	}
	for _, row := range rows {
		response.Items = append(response.Items, projectDeliveryScheduleDTO(row))
	}
	return success(c, "Recurring schedules", response)
}

func GetSchedule(c echo.Context) error {
	projectID, scheduleID, err := authorizeDeliverySchedule(c, deliveryView)
	if err != nil {
		return err
	}
	var schedule models.DeliveryAutomationSchedule
	if err := configuration.DB.Where("id = ? AND project_id = ?", scheduleID, projectID).First(&schedule).Error; err != nil {
		return lookup(c, "Recurring schedule", err)
	}
	return success(c, "Recurring schedule", projectDeliveryScheduleDTO(schedule))
}

func PatchSchedule(c echo.Context) error {
	projectID, scheduleID, err := authorizeDeliverySchedule(c, deliveryManage)
	if err != nil {
		return err
	}
	var patch deliverySchedulePatchRequest
	if err := c.Bind(&patch); err != nil {
		return badRequest(c, "Invalid recurring schedule", "request body is invalid")
	}
	if patch.Name == nil && patch.Template == nil && patch.Recurrence == nil {
		return badRequest(c, "Invalid recurring schedule", "provide at least one field to update")
	}
	now := time.Now().UTC()
	var updated models.DeliveryAutomationSchedule
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var schedule models.DeliveryAutomationSchedule
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ?", scheduleID, projectID).First(&schedule).Error; err != nil {
			return err
		}
		if schedule.Status == models.DeliveryAutomationScheduleEnded {
			return fmt.Errorf("ended schedules cannot be edited")
		}
		if patch.Name != nil {
			name := strings.TrimSpace(*patch.Name)
			if name == "" || len(name) > 180 {
				return fmt.Errorf("name is required and must be at most 180 characters")
			}
			schedule.Name = name
		}
		var template deliveryScheduleTemplate
		if err := json.Unmarshal([]byte(schedule.TemplateJSON), &template); err != nil {
			return fmt.Errorf("stored schedule template is invalid")
		}
		if patch.Template != nil {
			template = *patch.Template
		}
		normalizedTemplate, input, err := normalizeDeliveryScheduleTemplate(projectID, scheduleActorSubject(c), template)
		if err != nil {
			return err
		}
		if err := validateDeliveryScheduleTemplateSources(tx, projectID, normalizedTemplate, input); err != nil {
			return err
		}
		recurrence := deliveryScheduleRecurrence{}
		if err := json.Unmarshal([]byte(schedule.RecurrenceJSON), &recurrence); err != nil {
			return fmt.Errorf("stored schedule recurrence is invalid")
		}
		if patch.Recurrence != nil {
			recurrence = *patch.Recurrence
		}
		recurrence, _, err = normalizeDeliveryScheduleRecurrence(recurrence)
		if err != nil {
			return err
		}
		nextRun, _, err := nextDeliveryScheduleOccurrence(recurrence, now)
		if err != nil {
			return err
		}
		if nextRun == nil {
			return fmt.Errorf("recurrence has no future occurrence")
		}
		templateJSON, err := json.Marshal(normalizedTemplate)
		if err != nil {
			return err
		}
		recurrenceJSON, err := json.Marshal(recurrence)
		if err != nil {
			return err
		}
		schedule.TemplateJSON = string(templateJSON)
		schedule.RecurrenceJSON = string(recurrenceJSON)
		schedule.TimeZone = recurrence.TimeZone
		schedule.Revision++
		schedule.NextRunAt = nextRun
		schedule.UpdatedBy = scheduleActorSubject(c)
		schedule.UpdatedAt = now
		if err := tx.Save(&schedule).Error; err != nil {
			return err
		}
		if err := appendDeliveryScheduleEvent(tx, schedule.ID, nil, nil, "updated", schedule.UpdatedBy, map[string]any{"revision": schedule.Revision, "status": schedule.Status}, now); err != nil {
			return err
		}
		updated = schedule
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return lookup(c, "Recurring schedule", err)
		}
		if errors.Is(err, errWorkItemContextNotReady) || errors.Is(err, errInvalidTaskPrimaryRepository) || strings.Contains(err.Error(), "recurrence") || strings.Contains(err.Error(), "name is required") || strings.Contains(err.Error(), "budget") || strings.Contains(err.Error(), "context_source_ids") {
			return badRequest(c, "Invalid recurring schedule", err.Error())
		}
		return conflict(c, "Recurring schedule not updated", err.Error())
	}
	return success(c, "Recurring schedule updated", projectDeliveryScheduleDTO(updated))
}

func PauseSchedule(c echo.Context) error {
	return transitionDeliverySchedule(c, false)
}

func ResumeSchedule(c echo.Context) error {
	return transitionDeliverySchedule(c, true)
}

func transitionDeliverySchedule(c echo.Context, resume bool) error {
	projectID, scheduleID, err := authorizeDeliverySchedule(c, deliveryManage)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var updated models.DeliveryAutomationSchedule
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var schedule models.DeliveryAutomationSchedule
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ?", scheduleID, projectID).First(&schedule).Error; err != nil {
			return err
		}
		if schedule.Status == models.DeliveryAutomationScheduleEnded {
			return fmt.Errorf("ended schedules cannot be resumed or paused")
		}
		eventType := "paused"
		if resume {
			if schedule.Status == models.DeliveryAutomationScheduleActive {
				updated = schedule
				return nil
			}
			recurrence := deliveryScheduleRecurrence{}
			if err := json.Unmarshal([]byte(schedule.RecurrenceJSON), &recurrence); err != nil {
				return fmt.Errorf("stored schedule recurrence is invalid")
			}
			nextRun, _, err := nextDeliveryScheduleOccurrence(recurrence, now)
			if err != nil {
				return err
			}
			if nextRun == nil {
				schedule.Status = models.DeliveryAutomationScheduleEnded
				schedule.NextRunAt = nil
				eventType = "ended"
			} else {
				schedule.Status = models.DeliveryAutomationScheduleActive
				schedule.NextRunAt = nextRun
				schedule.PausedAt = nil
				eventType = "resumed"
			}
		} else {
			if schedule.Status == models.DeliveryAutomationSchedulePaused {
				updated = schedule
				return nil
			}
			schedule.Status = models.DeliveryAutomationSchedulePaused
			schedule.PausedAt = &now
		}
		schedule.UpdatedBy = scheduleActorSubject(c)
		schedule.UpdatedAt = now
		if err := tx.Save(&schedule).Error; err != nil {
			return err
		}
		if err := appendDeliveryScheduleEvent(tx, schedule.ID, nil, nil, eventType, schedule.UpdatedBy, map[string]any{"revision": schedule.Revision, "status": schedule.Status}, now); err != nil {
			return err
		}
		updated = schedule
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return lookup(c, "Recurring schedule", err)
		}
		return conflict(c, "Recurring schedule transition rejected", err.Error())
	}
	return success(c, "Recurring schedule updated", projectDeliveryScheduleDTO(updated))
}

func ListScheduleOccurrences(c echo.Context) error {
	projectID, scheduleID, err := authorizeDeliverySchedule(c, deliveryView)
	if err != nil {
		return err
	}
	limit, offset, err := deliverySchedulePagination(c)
	if err != nil {
		return badRequest(c, "Invalid recurring schedule pagination", err.Error())
	}
	var rows []models.DeliveryAutomationScheduleOccurrence
	if err := configuration.DB.Table("delivery_automation_schedule_occurrences AS occurrence").
		Joins("JOIN delivery_automation_schedules AS schedule ON schedule.id = occurrence.schedule_id").
		Where("occurrence.schedule_id = ? AND schedule.project_id = ?", scheduleID, projectID).
		Order("occurrence.scheduled_for DESC, occurrence.id DESC").Limit(limit + 1).Offset(offset).Find(&rows).Error; err != nil {
		return utilsError(c, err)
	}
	page := deliverySchedulePageResponse[deliveryScheduleOccurrenceDTO]{Items: make([]deliveryScheduleOccurrenceDTO, 0, limit), Limit: limit, Offset: offset}
	if len(rows) > limit {
		rows = rows[:limit]
		next := offset + limit
		page.NextOffset = &next
	}
	for _, row := range rows {
		page.Items = append(page.Items, deliveryScheduleOccurrenceDTO{ID: row.ID, ScheduleID: row.ScheduleID, ScheduleRevision: row.ScheduleRevision,
			OccurrenceKey: row.OccurrenceKey, ScheduledFor: row.ScheduledFor.UTC(), LocalOccurrence: row.LocalOccurrence, TimeZone: row.TimeZone,
			Status: row.Status, WorkItemID: row.WorkItemID, FailureCode: row.FailureCode, CreatedAt: row.CreatedAt.UTC(), MaterializedAt: row.MaterializedAt})
	}
	return success(c, "Recurring schedule occurrences", page)
}

func ListScheduleEvents(c echo.Context) error {
	projectID, scheduleID, err := authorizeDeliverySchedule(c, deliveryView)
	if err != nil {
		return err
	}
	limit, offset, err := deliverySchedulePagination(c)
	if err != nil {
		return badRequest(c, "Invalid recurring schedule pagination", err.Error())
	}
	var rows []models.DeliveryAutomationScheduleEvent
	if err := configuration.DB.Table("delivery_automation_schedule_events AS event").
		Joins("JOIN delivery_automation_schedules AS schedule ON schedule.id = event.schedule_id").
		Where("event.schedule_id = ? AND schedule.project_id = ?", scheduleID, projectID).
		Order("event.occurred_at DESC, event.id DESC").Limit(limit + 1).Offset(offset).Find(&rows).Error; err != nil {
		return utilsError(c, err)
	}
	page := deliverySchedulePageResponse[deliveryScheduleEventDTO]{Items: make([]deliveryScheduleEventDTO, 0, limit), Limit: limit, Offset: offset}
	if len(rows) > limit {
		rows = rows[:limit]
		next := offset + limit
		page.NextOffset = &next
	}
	for _, row := range rows {
		page.Items = append(page.Items, deliveryScheduleEventDTO{ID: row.ID, ScheduleID: row.ScheduleID, OccurrenceID: row.OccurrenceID,
			WorkItemID: row.WorkItemID, EventType: row.EventType, ActorSubject: row.ActorSubject, OccurredAt: row.OccurredAt.UTC()})
	}
	return success(c, "Recurring schedule events", page)
}

func normalizeDeliveryScheduleTemplate(projectID uuid.UUID, actorSubject string, template deliveryScheduleTemplate) (deliveryScheduleTemplate, normalizedWorkItemRequest, error) {
	request := workItemRequest{
		ContextSourceIDs: template.ContextSourceIDs, PrimaryRepositorySourceID: strings.TrimSpace(template.PrimaryRepositorySourceID),
		Title: template.Title, Description: template.Description, ExpectedOutcome: template.ExpectedOutcome,
		AssignedAgent: template.AssignedAgent, IncludedScope: template.IncludedScope, ExcludedScope: template.ExcludedScope,
		AcceptanceCriteria: template.AcceptanceCriteria, BudgetMicros: template.BudgetMicros,
		BudgetAlertPercent: template.BudgetAlertPercent, MaxConcurrency: template.MaxConcurrency,
	}
	input, err := normalizeWorkItemRequest(projectID, actorSubject, request, true)
	if err != nil {
		return template, normalizedWorkItemRequest{}, err
	}
	if len(template.Title) > 240 || len(template.Description) > 12000 || len(template.ExpectedOutcome) > 12000 || len(template.AssignedAgent) > 128 {
		return template, normalizedWorkItemRequest{}, fmt.Errorf("template text exceeds its allowed length")
	}
	template.Title = input.Request.Title
	template.Description = input.Request.Description
	template.ExpectedOutcome = input.Request.ExpectedOutcome
	template.AssignedAgent = input.Request.AssignedAgent
	template.ContextSourceIDs = make([]string, 0, len(input.ContextSourceIDs))
	for _, id := range input.ContextSourceIDs {
		template.ContextSourceIDs = append(template.ContextSourceIDs, id.String())
	}
	template.PrimaryRepositorySourceID = strings.TrimSpace(template.PrimaryRepositorySourceID)
	template.IncludedScope = cleanStrings(template.IncludedScope)
	template.ExcludedScope = cleanStrings(template.ExcludedScope)
	template.AcceptanceCriteria = cleanStrings(template.AcceptanceCriteria)
	template.BudgetAlertPercent = input.Request.BudgetAlertPercent
	input.Request = workItemRequest{
		ContextSourceIDs: template.ContextSourceIDs, PrimaryRepositorySourceID: template.PrimaryRepositorySourceID,
		Title: template.Title, Description: template.Description, ExpectedOutcome: template.ExpectedOutcome,
		AssignedAgent: template.AssignedAgent, IncludedScope: template.IncludedScope, ExcludedScope: template.ExcludedScope,
		AcceptanceCriteria: template.AcceptanceCriteria, BudgetMicros: template.BudgetMicros,
		BudgetAlertPercent: template.BudgetAlertPercent, MaxConcurrency: template.MaxConcurrency,
	}
	return template, input, nil
}

func validateDeliveryScheduleTemplateSources(tx *gorm.DB, projectID uuid.UUID, template deliveryScheduleTemplate, input normalizedWorkItemRequest) error {
	var sources []models.DeliveryContextSource
	if err := tx.Where("project_id = ? AND status = ? AND id IN ?", projectID, "ready", input.ContextSourceIDs).Find(&sources).Error; err != nil {
		return err
	}
	if len(sources) != len(input.ContextSourceIDs) {
		return errWorkItemContextNotReady
	}
	primaryID := strings.TrimSpace(template.PrimaryRepositorySourceID)
	if primaryID == "" {
		return nil
	}
	parsed, err := uuid.FromString(primaryID)
	if err != nil || parsed == uuid.Nil {
		return errInvalidTaskPrimaryRepository
	}
	for _, source := range sources {
		if source.ID == parsed && strings.EqualFold(strings.TrimSpace(source.Kind), "repository") && strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
			return nil
		}
	}
	return errInvalidTaskPrimaryRepository
}

func projectDeliveryScheduleDTO(schedule models.DeliveryAutomationSchedule) deliveryScheduleDTO {
	dto := deliveryScheduleDTO{ID: schedule.ID, ProjectID: schedule.ProjectID, Name: schedule.Name, Status: schedule.Status,
		Revision: schedule.Revision, NextRunAt: schedule.NextRunAt, LastRunAt: schedule.LastRunAt, PausedAt: schedule.PausedAt,
		CreatedAt: schedule.CreatedAt.UTC(), UpdatedAt: schedule.UpdatedAt.UTC()}
	_ = json.Unmarshal([]byte(schedule.TemplateJSON), &dto.Template)
	_ = json.Unmarshal([]byte(schedule.RecurrenceJSON), &dto.Recurrence)
	if schedule.NextRunAt != nil {
		if location, err := time.LoadLocation(schedule.TimeZone); err == nil {
			dto.NextRunLocal = schedule.NextRunAt.In(location).Format(time.RFC3339)
		}
	}
	return dto
}

func appendDeliveryScheduleEvent(tx *gorm.DB, scheduleID uuid.UUID, occurrenceID, workItemID *uuid.UUID, eventType, actor string, details map[string]any, at time.Time) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return tx.Create(&models.DeliveryAutomationScheduleEvent{ScheduleID: scheduleID, OccurrenceID: occurrenceID, WorkItemID: workItemID,
		EventType: eventType, ActorSubject: strings.TrimSpace(actor), DetailsJSON: string(encoded), OccurredAt: at.UTC()}).Error
}

func deliveryScheduleOccurrenceKey(scheduleID uuid.UUID, revision int, localOccurrence, timeZone string) string {
	material := fmt.Sprintf("%s\x00%d\x00%s\x00%s", scheduleID.String(), revision, localOccurrence, timeZone)
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}

func parseDeliveryScheduleID(c echo.Context, parameter, kind string) (uuid.UUID, error) {
	value, err := uuid.FromString(strings.TrimSpace(c.Param(parameter)))
	if err != nil || value == uuid.Nil {
		return uuid.Nil, utils.Error(c, http.StatusBadRequest, "Invalid "+kind, "id must be a UUID")
	}
	return value, nil
}

func authorizeDeliverySchedule(c echo.Context, permission deliveryPermission) (uuid.UUID, uuid.UUID, error) {
	projectID, err := parseDeliveryScheduleID(c, "projectId", "project")
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if _, err := projectActor(c, projectID, permission); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if err := projectPresent(projectID); err != nil {
		return uuid.Nil, uuid.Nil, lookup(c, "Delivery project", err)
	}
	scheduleID, err := parseDeliveryScheduleID(c, "scheduleId", "schedule")
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	var schedule models.DeliveryAutomationSchedule
	if err := configuration.DB.Select("id").Where("id = ? AND project_id = ?", scheduleID, projectID).First(&schedule).Error; err != nil {
		return uuid.Nil, uuid.Nil, lookup(c, "Recurring schedule", err)
	}
	return projectID, scheduleID, nil
}

func deliverySchedulePagination(c echo.Context) (int, int, error) {
	limit := defaultDeliverySchedulePageSize
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxDeliverySchedulePageSize {
			return 0, 0, fmt.Errorf("limit must be between 1 and %d", maxDeliverySchedulePageSize)
		}
		limit = parsed
	}
	offset := 0
	if raw := strings.TrimSpace(c.QueryParam("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > maxDeliveryScheduleOffset {
			return 0, 0, fmt.Errorf("offset must be between 0 and %d", maxDeliveryScheduleOffset)
		}
		offset = parsed
	}
	return limit, offset, nil
}

func scheduleActorSubject(c echo.Context) string {
	if user, err := authz.CurrentUser(c); err == nil && user != nil {
		return strings.TrimSpace(user.CognitoSub)
	}
	return ""
}
