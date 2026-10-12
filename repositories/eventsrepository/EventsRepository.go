package eventsrepository

import (
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/guestrepository"
	"events-stocks/repositories/outboxrepository"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

func GetEventByID(id uuid.UUID) (string, error) {
	var event models.Event
	err := gormrepository.GetByID(&event, id)
	if err != nil {
		return "", err
	}
	return utils.MarshallData([]*models.Event{&event}, nil)
}

func CreateEvent(event *models.Event) error {
	err := gormrepository.Insert(event)
	if err != nil {
		return ValidateError(err)
	}
	return nil
}

// UpdateEvent writes every column of the event but never its associations:
// handlers load the event with EventType (and sometimes Client) joined, and a
// Select("*") save would copy those loaded IDs back over a changed or cleared
// event_type_id / client_id.
func UpdateEvent(event *models.Event) error {
	if err := gormrepository.UpdateOmitAssociations(event, event.ID); err != nil {
		return ValidateError(err)
	}
	return nil
}

func DeleteEvent(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.Event{})
}

func ListEvents(page int, pageSize int, name string) ([]models.Event, error) {
	var events []models.Event

	filters := map[string]interface{}{}
	if name != "" {
		filters["name"] = name
	}

	opts := gormrepository.QueryOptions{
		Filters:  filters,
		OrderBy:  "id",
		OrderDir: "desc",
		Preload:  []string{"EventType"},
	}

	if pageSize > 0 {
		opts.Limit = pageSize
		opts.Offset = (page - 1) * pageSize
	}

	err := gormrepository.GetList(&events, opts)
	return events, err
}

// EventsRepo implements ports.EventsRepository.
type EventsRepo struct{}

func NewEventsRepo() *EventsRepo { return &EventsRepo{} }

// EventService type-asserts the optimized list projections. A signature drift
// would silently fall back to the in-memory path, so pin them at build time.
var (
	_ ports.EventsRepository             = (*EventsRepo)(nil)
	_ ports.EventsDashboardRepository    = (*EventsRepo)(nil)
	_ ports.EventsPageRepository         = (*EventsRepo)(nil)
	_ ports.EventsNotificationRepository = (*EventsRepo)(nil)
)

func (r *EventsRepo) CreateEvent(event *models.Event) error { return CreateEvent(event) }
func (r *EventsRepo) UpdateEvent(event *models.Event) error { return UpdateEvent(event) }
func (r *EventsRepo) DeleteEvent(id uuid.UUID) error        { return DeleteEvent(id) }
func (r *EventsRepo) ListEvents(page, pageSize int, name string) ([]models.Event, error) {
	return ListEvents(page, pageSize, name)
}
func (r *EventsRepo) GetEventByID(id uuid.UUID) (string, error) { return GetEventByID(id) }
func (r *EventsRepo) IdentifierExists(identifier string) bool   { return IdentifierExists(identifier) }
func (r *EventsRepo) GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	return GetEventByIDRaw(id)
}
func (r *EventsRepo) GetEventByIDForSpec(id uuid.UUID) (*models.Event, error) {
	return GetEventByIDForSpec(id)
}
func (r *EventsRepo) GetEventByIdentifier(identifier string) (*models.Event, error) {
	return GetEventByIdentifier(identifier)
}
func (r *EventsRepo) GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	return GetEventsByClientID(clientID)
}
func (r *EventsRepo) GetAllEventsForDashboard() ([]models.Event, error) {
	return GetAllEventsForDashboard()
}
func (r *EventsRepo) GetEventsForUser(access dtos.EventCallerAccess) ([]models.Event, error) {
	return GetEventsForUser(access)
}
func (r *EventsRepo) UpdateEventCover(id uuid.UUID, coverImageURL string) error {
	return UpdateEventCover(id, coverImageURL)
}
func (r *EventsRepo) GetEventDashboardOverview(clientID *uuid.UUID, caller *dtos.EventCallerAccess, now time.Time) (dtos.EventDashboardOverview, error) {
	return GetEventDashboardOverview(clientID, caller, now)
}
func (r *EventsRepo) ListEventPage(clientID *uuid.UUID, caller *dtos.EventCallerAccess, query dtos.EventListQuery) ([]models.Event, int, dtos.EventListCounts, error) {
	return ListEventPage(clientID, caller, query)
}
func (r *EventsRepo) ListEventNotifications(clientID *uuid.UUID, caller *dtos.EventCallerAccess, now time.Time) ([]dtos.EventNotification, error) {
	return ListEventNotifications(clientID, caller, now)
}

// callerAccessEventPredicate keeps an event when its organization is one the
// application session lists for the caller. It mirrors
// services/applications.SessionService.organizationAccess: an active
// membership with a role, in an active and not deleted organization, with an
// active member access row for the request application. Arguments: the
// request application ID, then the user ID. Only direct memberships count, as
// before; inherited Owner/Admin access is reached with an explicit client_id.
const callerAccessEventPredicate = `EXISTS (
	SELECT 1
	FROM client_members cm
	JOIN clients c ON c.id = cm.client_id AND c.deleted_at IS NULL AND c.is_active = true
	JOIN client_roles cr ON cr.id = cm.client_role_id
	JOIN client_member_applications cma ON cma.client_member_id = cm.id AND cma.is_active = true AND cma.application_id = ?
	WHERE cm.client_id = events.client_id
		AND cm.user_id = ?
		AND cm.is_active = true
)`

func scopeEventsToCallerAccess(query *gorm.DB, access dtos.EventCallerAccess) *gorm.DB {
	return query.Where(callerAccessEventPredicate, access.ApplicationID, access.UserID)
}

// dashboardEventsScope selects the events of one organization (clientID,
// authorized by the caller), the caller-access portfolio (caller), or every
// event when both are nil (platform roots only).
func dashboardEventsScope(query *gorm.DB, clientID *uuid.UUID, caller *dtos.EventCallerAccess) *gorm.DB {
	if clientID != nil {
		return query.Where("events.client_id = ?", *clientID)
	}
	if caller != nil {
		return scopeEventsToCallerAccess(query, *caller)
	}
	return query
}

func ListEventNotifications(clientID *uuid.UUID, caller *dtos.EventCallerAccess, now time.Time) ([]dtos.EventNotification, error) {
	calendarDate := "DATE(events.event_date_time AT TIME ZONE COALESCE(NULLIF(events.timezone, ''), 'UTC'))"
	today := "DATE(? AT TIME ZONE COALESCE(NULLIF(events.timezone, ''), 'UTC'))"
	var notifications []dtos.EventNotification
	err := dashboardEventsScope(gormrepository.DB().Model(&models.Event{}), clientID, caller).
		Select("events.id, events.name, events.identifier, events.event_date_time, events.timezone, events.is_active, events.client_id, events.event_type_id").
		Where(calendarDate+" BETWEEN ("+today+" - INTERVAL '3 days') AND ("+today+" + INTERVAL '3 days')", now, now).
		Order("events.event_date_time ASC").
		Scan(&notifications).Error
	return notifications, err
}

func GetEventDashboardOverview(clientID *uuid.UUID, caller *dtos.EventCallerAccess, now time.Time) (dtos.EventDashboardOverview, error) {
	db := gormrepository.DB()
	calendarDate := "DATE(events.event_date_time AT TIME ZONE COALESCE(NULLIF(events.timezone, ''), 'UTC'))"
	today := "DATE(? AT TIME ZONE COALESCE(NULLIF(events.timezone, ''), 'UTC'))"

	var metrics struct {
		Total         int
		Active        int
		Upcoming      int
		PastActive    int
		TotalCapacity int
	}
	var activeEvents []models.Event
	var next models.Event
	nextFound := false
	group := new(errgroup.Group)
	group.Go(func() error {
		return dashboardEventsScope(db.Model(&models.Event{}), clientID, caller).
			Select(
				"COUNT(*) AS total, "+
					"COUNT(*) FILTER (WHERE events.is_active = true) AS active, "+
					"COUNT(*) FILTER (WHERE events.is_active = true AND "+calendarDate+" >= "+today+") AS upcoming, "+
					"COUNT(*) FILTER (WHERE events.is_active = true AND "+calendarDate+" < "+today+") AS past_active, "+
					"COALESCE(SUM(events.max_guests), 0) AS total_capacity",
				now,
				now,
			).
			Scan(&metrics).Error
	})
	group.Go(func() error {
		return dashboardEventsScope(db.Model(&models.Event{}), clientID, caller).
			Select("events.*").
			Joins("EventType").
			Where("events.is_active = true").
			Order("events.event_date_time DESC").
			Limit(5).
			Find(&activeEvents).Error
	})
	group.Go(func() error {
		err := dashboardEventsScope(db.Model(&models.Event{}), clientID, caller).
			Select("events.*").
			Joins("EventType").
			Where("events.is_active = true").
			Where(calendarDate+" >= "+today, now).
			Order("events.event_date_time ASC").
			Limit(1).
			First(&next).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		nextFound = err == nil
		return err
	})
	if err := group.Wait(); err != nil {
		return dtos.EventDashboardOverview{}, err
	}

	overview := dtos.EventDashboardOverview{
		Metrics: dtos.EventDashboardMetrics{
			Total:         metrics.Total,
			Active:        metrics.Active,
			Upcoming:      metrics.Upcoming,
			PastActive:    metrics.PastActive,
			TotalCapacity: metrics.TotalCapacity,
		},
		ActiveEvents: dtos.NewEventResponses(activeEvents),
	}
	if nextFound {
		response := dtos.NewEventResponse(&next)
		overview.NextEvent = &response
		summary, err := guestrepository.GetGuestSummaryByEventID(next.ID)
		if err != nil {
			return dtos.EventDashboardOverview{}, err
		}
		overview.NextEventGuestSummary = &summary
	}
	return overview, nil
}

func applyEventListFilters(query *gorm.DB, listQuery dtos.EventListQuery) *gorm.DB {
	if search := strings.TrimSpace(listQuery.Search); search != "" {
		pattern := "%" + search + "%"
		query = query.Where(
			"events.name ILIKE ? OR events.address ILIKE ? OR events.organizer_name ILIKE ?",
			pattern,
			pattern,
			pattern,
		)
	}

	calendarDate := "DATE(events.event_date_time AT TIME ZONE COALESCE(NULLIF(events.timezone, ''), 'UTC'))"
	today := "DATE(? AT TIME ZONE COALESCE(NULLIF(events.timezone, ''), 'UTC'))"
	switch listQuery.Filter {
	case "upcoming":
		query = query.Where(calendarDate+" > "+today, listQuery.Now)
	case "today":
		query = query.Where(calendarDate+" = "+today, listQuery.Now)
	case "past":
		query = query.Where(calendarDate+" < "+today, listQuery.Now)
	}
	return query
}

func eventListNeedsFilteredTotal(query dtos.EventListQuery) bool {
	filter := strings.ToLower(strings.TrimSpace(query.Filter))
	return strings.TrimSpace(query.Search) != "" || (filter != "" && filter != "all")
}

func ListEventPage(clientID *uuid.UUID, caller *dtos.EventCallerAccess, query dtos.EventListQuery) ([]models.Event, int, dtos.EventListCounts, error) {
	db := gormrepository.DB()
	calendarDate := "DATE(events.event_date_time AT TIME ZONE COALESCE(NULLIF(events.timezone, ''), 'UTC'))"
	today := "DATE(? AT TIME ZONE COALESCE(NULLIF(events.timezone, ''), 'UTC'))"

	var counts dtos.EventListCounts
	var total int64
	var events []models.Event
	group := new(errgroup.Group)
	needsFilteredTotal := eventListNeedsFilteredTotal(query)
	group.Go(func() error {
		return dashboardEventsScope(db.Model(&models.Event{}), clientID, caller).
			Select(
				"COUNT(*) AS \"all\", "+
					"COUNT(*) FILTER (WHERE "+calendarDate+" > "+today+") AS upcoming, "+
					"COUNT(*) FILTER (WHERE "+calendarDate+" = "+today+") AS today, "+
					"COUNT(*) FILTER (WHERE "+calendarDate+" < "+today+") AS past",
				query.Now,
				query.Now,
				query.Now,
			).
			Scan(&counts).Error
	})
	if needsFilteredTotal {
		group.Go(func() error {
			return applyEventListFilters(
				dashboardEventsScope(db.Model(&models.Event{}), clientID, caller),
				query,
			).Count(&total).Error
		})
	}
	group.Go(func() error {
		return applyEventListFilters(
			dashboardEventsScope(db.Model(&models.Event{}), clientID, caller),
			query,
		).
			Select(`events.*, (
			SELECT COUNT(*)
			FROM moments
			WHERE moments.event_id = events.id
				AND moments.deleted_at IS NULL
				AND moments.is_approved = FALSE
				AND moments.processing_status NOT IN ('pending', 'processing')
		) AS pending_moment_count`).
			Joins("EventType").
			Order(eventListOrder(query)).
			Limit(query.PageSize).
			Offset((query.Page - 1) * query.PageSize).
			Find(&events).Error
	})
	if err := group.Wait(); err != nil {
		return nil, 0, dtos.EventListCounts{}, err
	}
	if !needsFilteredTotal {
		total = int64(counts.All)
	}

	return events, int(total), counts, nil
}

// EventNameTakenFold reports whether another live event of the organization
// (client_id IS NULL for unowned events) already uses name, ignoring case and
// surrounding spaces. excludeID (the event being updated) is skipped when set.
//
// The unique index idx_events_client_name_live stays exact; this check is the
// case-insensitive rule (EVT-22). It is advisory under a race: two concurrent
// writes of "Boda" and "boda" can both pass, while exact duplicates are still
// stopped by the index.
func EventNameTakenFold(db *gorm.DB, clientID *uuid.UUID, name string, excludeID uuid.UUID) (bool, error) {
	var count int64
	query := db.Model(&models.Event{}).Where("lower(btrim(name)) = lower(btrim(?))", name)
	if clientID == nil {
		query = query.Where("client_id IS NULL")
	} else {
		query = query.Where("client_id = ?", *clientID)
	}
	if excludeID != uuid.Nil {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// EventNameTaken is EventNameTakenFold on the default connection.
func (r *EventsRepo) EventNameTaken(clientID *uuid.UUID, name string, excludeID uuid.UUID) (bool, error) {
	return EventNameTakenFold(gormrepository.DB(), clientID, name, excludeID)
}

// IdentifierExists returns true if an event with the given identifier already exists.
func IdentifierExists(identifier string) bool {
	var count int64
	gormrepository.DB().Model(&models.Event{}).Where("identifier = ?", identifier).Count(&count)
	return count > 0
}

// GetEventByIDRaw returns the access/detail snapshot in one query. EventType is
// joined because the protected dashboard renders its label immediately.
func GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	var event models.Event
	err := gormrepository.DB().
		Select("events.*").
		Joins("EventType").
		Where("events.id = ?", id).
		First(&event).Error
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// GetEventByIDForSpec returns the event fields needed by public PageSpec builders.
func GetEventByIDForSpec(id uuid.UUID) (*models.Event, error) {
	var event models.Event
	err := gormrepository.DB().
		Preload("EventType").
		Where("id = ?", id).
		First(&event).Error
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func UpdateEventCover(id uuid.UUID, coverImageURL string) error {
	return gormrepository.UpdateFieldsByID(id, map[string]interface{}{
		"cover_image_url": coverImageURL,
	}, &models.Event{})
}

func (r *EventsRepo) UpdateEventCoverWithVariants(id uuid.UUID, coverImageURL string, variants models.MediaVariants) error {
	return gormrepository.UpdateFieldsByID(id, map[string]interface{}{
		"cover_image_url":             coverImageURL,
		"cover_variants":              variants,
		"cover_pending_url":           "",
		"cover_processing_status":     "",
		"cover_processing_job_id":     "",
		"cover_processing_error":      "",
		"cover_processing_generation": gorm.Expr("cover_processing_generation + 1"),
	}, &models.Event{})
}

func (r *EventsRepo) BeginEventCoverProcessing(id uuid.UUID, pendingURL, jobID string) (*models.Event, string, error) {
	var result *models.Event
	var previousPending string
	err := gormrepository.DB().Transaction(func(tx *gorm.DB) error {
		var err error
		result, previousPending, err = beginEventCoverProcessing(tx, id, pendingURL, jobID)
		return err
	})
	return result, previousPending, err
}

// BeginEventCoverProcessingWithOutbox ensures the cover's pending generation
// and the eventual Lambda invocation either commit together or not at all.
func (r *EventsRepo) BeginEventCoverProcessingWithOutbox(id uuid.UUID, pendingURL, jobID string, message dtos.MediaProcessMessage) (*models.Event, string, error) {
	var result *models.Event
	var previousPending string
	err := gormrepository.DB().Transaction(func(tx *gorm.DB) error {
		var err error
		result, previousPending, err = beginEventCoverProcessing(tx, id, pendingURL, jobID)
		if err != nil {
			return err
		}
		message.Generation = result.CoverProcessingGeneration
		body, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("marshal durable event-cover job: %w", err)
		}
		inserted, err := outboxrepository.Enqueue(tx, &models.OutboxEvent{
			EventType:     dtos.MediaProcessEventType,
			DedupeKey:     strings.TrimSpace(message.JobID),
			TenantCode:    strings.TrimSpace(message.Application),
			CorrelationID: strings.TrimSpace(message.CorrelationID),
			Payload:       string(body),
			State:         outboxrepository.StatePending,
			AvailableAt:   time.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("enqueue durable event-cover job: %w", err)
		}
		if !inserted {
			return fmt.Errorf("durable event-cover job %s already exists", message.JobID)
		}
		return nil
	})
	return result, previousPending, err
}

func beginEventCoverProcessing(tx *gorm.DB, id uuid.UUID, pendingURL, jobID string) (*models.Event, string, error) {
	var result models.Event
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&result).Error; err != nil {
		return nil, "", err
	}
	previousPending := result.CoverPendingURL
	result.CoverProcessingGeneration++
	updates := map[string]interface{}{
		"cover_pending_url":            pendingURL,
		"cover_processing_status":      "pending",
		"cover_processing_job_id":      jobID,
		"cover_processing_generation":  result.CoverProcessingGeneration,
		"cover_processing_error":       "",
		"cover_processing_duration_ms": 0,
		"cover_original_size_bytes":    0,
		"cover_optimized_size_bytes":   0,
	}
	if err := tx.Model(&models.Event{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, "", err
	}
	result.CoverPendingURL = pendingURL
	result.CoverProcessingStatus = "pending"
	result.CoverProcessingJobID = jobID
	result.CoverProcessingError = ""
	return &result, previousPending, nil
}

func (r *EventsRepo) ApplyEventCoverProcessing(id uuid.UUID, callback dtos.MediaProcessingCallback) (*models.Event, string, models.MediaVariants, error) {
	var result models.Event
	var oldCover string
	var oldVariants models.MediaVariants
	err := gormrepository.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&result).Error; err != nil {
			return err
		}
		if result.CoverProcessingJobID != callback.JobID || result.CoverProcessingGeneration != callback.Generation {
			return ports.ErrStaleCoverProcessing
		}
		current := result.CoverProcessingStatus
		if current == callback.ProcessingStatus {
			if current == "done" && result.CoverImageURL != callback.ObjectKey {
				return ports.ErrStaleCoverProcessing
			}
			return nil
		}
		if current != "pending" && current != "processing" {
			return ports.ErrInvalidCoverProcessingTransition
		}
		updates := map[string]interface{}{
			"cover_processing_status": callback.ProcessingStatus,
			"cover_processing_error":  callback.ErrorMessage,
		}
		switch callback.ProcessingStatus {
		case "processing":
			if current != "pending" {
				return ports.ErrInvalidCoverProcessingTransition
			}
		case "failed":
		case "done":
			oldCover, oldVariants = result.CoverImageURL, result.CoverVariants
			updates["cover_image_url"] = callback.ObjectKey
			updates["cover_variants"] = eventCoverVariantsFromDTO(callback.MediaVariants)
			updates["cover_pending_url"] = ""
			updates["cover_processing_duration_ms"] = callback.ProcessingDurationMs
			updates["cover_original_size_bytes"] = callback.OriginalSizeBytes
			updates["cover_optimized_size_bytes"] = callback.OptimizedSizeBytes
		default:
			return ports.ErrInvalidCoverProcessingTransition
		}
		if err := tx.Model(&models.Event{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		result.CoverProcessingStatus = callback.ProcessingStatus
		result.CoverProcessingError = callback.ErrorMessage
		if callback.ProcessingStatus == "done" {
			result.CoverImageURL = callback.ObjectKey
			result.CoverVariants = eventCoverVariantsFromDTO(callback.MediaVariants)
			result.CoverPendingURL = ""
		}
		return nil
	})
	return &result, oldCover, oldVariants, err
}

func eventCoverVariantsFromDTO(values []dtos.MediaVariant) models.MediaVariants {
	result := make(models.MediaVariants, 0, len(values))
	for _, value := range values {
		result = append(result, models.MediaVariant{ObjectKey: value.ObjectKey, Width: value.Width, Format: value.Format, Bytes: value.Bytes})
	}
	return result
}

// GetEventsByClientID returns all events belonging to a specific client.
func GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	var events []models.Event
	err := gormrepository.DB().
		Preload("EventType").
		Where("client_id = ?", clientID).
		Order("event_date_time DESC").
		Find(&events).Error
	return events, err
}

// GetAllEventsForDashboard returns all events (for root users).
func GetAllEventsForDashboard() ([]models.Event, error) {
	var events []models.Event
	err := gormrepository.DB().
		Preload("EventType").
		Order("event_date_time DESC").
		Find(&events).Error
	return events, err
}

// GetEventsForUser returns the caller-access portfolio: the events of every
// organization the request application session lists for the user.
func GetEventsForUser(access dtos.EventCallerAccess) ([]models.Event, error) {
	var events []models.Event
	err := scopeEventsToCallerAccess(gormrepository.DB().Preload("EventType"), access).
		Order("events.event_date_time DESC").
		Find(&events).Error
	return events, err
}

// GetEventByIdentifier returns a bare Event record by its slug identifier.
func GetEventByIdentifier(identifier string) (*models.Event, error) {
	var event models.Event
	err := gormrepository.DB().
		Preload("EventType").
		Where("identifier = ?", identifier).
		First(&event).Error
	if err != nil {
		return nil, err
	}
	return &event, nil
}
