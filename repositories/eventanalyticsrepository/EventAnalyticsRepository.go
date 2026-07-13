package eventanalyticsrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func CreateEventAnalytics(m *models.EventAnalytics) error {
	return gormrepository.Insert(m)
}

func UpdateEventAnalytics(m *models.EventAnalytics) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteEventAnalytics(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.EventAnalytics{})
}

func GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	var model models.EventAnalytics
	err := gormrepository.GetByID(&model, id)
	return &model, err
}

func ListEventAnalyticss() ([]models.EventAnalytics, error) {
	var list []models.EventAnalytics
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}

func GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	var m models.EventAnalytics
	if err := gormrepository.DB().
		Table("event_analytics").
		Select(`
			event_analytics.id,
			event_analytics.event_id,
			event_analytics.views,
			GREATEST(event_analytics.moment_comments, COALESCE(moment_stats.with_message, 0)) AS moment_comments,
			GREATEST(event_analytics.moment_uploads, COALESCE(moment_stats.total, 0)) AS moment_uploads,
			COALESCE(moment_stats.total, 0) AS moment_total,
			COALESCE(moment_stats.approved, 0) AS moment_approved,
			COALESCE(moment_stats.pending, 0) AS moment_pending,
			event_analytics.rsvp_confirmed,
			event_analytics.rsvp_declined,
			event_analytics.created_at,
			event_analytics.updated_at
		`).
		Joins(`LEFT JOIN (
			SELECT
				event_id,
				COUNT(*) AS total,
				COUNT(*) FILTER (WHERE is_approved) AS approved,
				COUNT(*) FILTER (WHERE NOT is_approved AND processing_status <> 'failed') AS pending,
				COUNT(*) FILTER (WHERE BTRIM(description) <> '') AS with_message
			FROM moments
			WHERE deleted_at IS NULL
			GROUP BY event_id
		) AS moment_stats ON moment_stats.event_id = event_analytics.event_id`).
		Where("event_analytics.event_id = ?", eventID).
		First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// GetEventAnalyticsBaseByEventID reads only authoritative request-time
// counters. Expensive guest and moment aggregates are owned by the Rust rollup.
func GetEventAnalyticsBaseByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	var analytics models.EventAnalytics
	if err := gormrepository.DB().Where("event_id = ?", eventID).First(&analytics).Error; err != nil {
		return nil, err
	}
	return &analytics, nil
}

func GetEventAnalyticsRollupByEventID(eventID uuid.UUID) (*models.EventAnalyticsRollup, error) {
	var rollup models.EventAnalyticsRollup
	if err := gormrepository.DB().Where("event_id = ?", eventID).First(&rollup).Error; err != nil {
		return nil, err
	}
	return &rollup, nil
}

func AdjustEventAnalytics(eventID uuid.UUID, field string, delta int) error {
	column, ok := analyticsCounterColumn(field)
	if !ok || eventID == uuid.Nil || delta == 0 {
		return nil
	}

	analytics := models.EventAnalytics{EventID: eventID}
	setAnalyticsCounter(&analytics, field, nonNegative(delta))

	return gormrepository.DB().
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "event_id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				column:       gorm.Expr("GREATEST("+column+" + ?, 0)", delta),
				"updated_at": time.Now().UTC(),
			}),
		}).
		Create(&analytics).
		Error
}

func analyticsCounterColumn(field string) (string, bool) {
	switch field {
	case "views":
		return "views", true
	case "rsvp_confirmed":
		return "rsvp_confirmed", true
	case "rsvp_declined":
		return "rsvp_declined", true
	case "moment_comments":
		return "moment_comments", true
	case "moment_uploads":
		return "moment_uploads", true
	default:
		return "", false
	}
}

func setAnalyticsCounter(a *models.EventAnalytics, field string, value int) {
	switch field {
	case "views":
		a.Views = value
	case "rsvp_confirmed":
		a.RSVPConfirmed = value
	case "rsvp_declined":
		a.RSVPDeclined = value
	case "moment_comments":
		a.MomentComments = value
	case "moment_uploads":
		a.MomentUploads = value
	}
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

type EventAnalyticsRepo struct{}

func NewEventAnalyticsRepo() *EventAnalyticsRepo { return &EventAnalyticsRepo{} }

func (r *EventAnalyticsRepo) CreateEventAnalytics(m *models.EventAnalytics) error {
	return CreateEventAnalytics(m)
}
func (r *EventAnalyticsRepo) UpdateEventAnalytics(m *models.EventAnalytics) error {
	return UpdateEventAnalytics(m)
}
func (r *EventAnalyticsRepo) DeleteEventAnalytics(id uuid.UUID) error {
	return DeleteEventAnalytics(id)
}
func (r *EventAnalyticsRepo) GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	return GetEventAnalyticsByID(id)
}
func (r *EventAnalyticsRepo) GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	return GetEventAnalyticsByEventID(eventID)
}
func (r *EventAnalyticsRepo) ListEventAnalyticss() ([]models.EventAnalytics, error) {
	return ListEventAnalyticss()
}
func (r *EventAnalyticsRepo) AdjustEventAnalytics(eventID uuid.UUID, field string, delta int) error {
	return AdjustEventAnalytics(eventID, field, delta)
}
func (r *EventAnalyticsRepo) GetEventAnalyticsBaseByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	return GetEventAnalyticsBaseByEventID(eventID)
}
func (r *EventAnalyticsRepo) GetEventAnalyticsRollupByEventID(eventID uuid.UUID) (*models.EventAnalyticsRollup, error) {
	return GetEventAnalyticsRollupByEventID(eventID)
}
