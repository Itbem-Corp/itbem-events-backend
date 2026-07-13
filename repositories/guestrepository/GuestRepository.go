package guestrepository

import (
	"database/sql"
	"errors"
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/gueststatusrepository"
	"events-stocks/utils"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

var pendingStatusID uuid.UUID

func GetPendingStatusID() uuid.UUID {
	if pendingStatusID == uuid.Nil {
		status, err := gueststatusrepository.GetGuestStatusByCode("pending")
		if err != nil {
			slog.Warn("error loading pending guest status", "error", err)
			return uuid.Nil
		}
		if status == nil {
			slog.Warn("pending guest status not found in db")
			return uuid.Nil
		}
		pendingStatusID = status.ID
	}
	return pendingStatusID
}

func GetGuestByInvitationID(invitationID uuid.UUID) (*models.Guest, error) {
	var guest models.Guest

	err := configuration.DB.
		Preload("Event").
		Preload("Event.EventType").
		Preload("Event.EventConfig").
		Preload("Event.EventConfig.DesignTemplate").
		Preload("Event.EventConfig.DesignTemplate.ColorPalette").
		Preload("Event.EventConfig.DesignTemplate.FontSet").
		Preload("Event.EventConfig.ColorPalette").
		Preload("Event.EventConfig.FontSet").
		Preload("Invitation").
		Preload("GuestStatus").
		Where("invitation_id = ?", invitationID).
		First(&guest).Error

	if err != nil {
		return nil, err
	}
	return &guest, nil
}

func GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	var model models.InvitationAccessToken
	err := configuration.DB.
		Where("pretty_token = ?", code).
		First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func ListGuestsByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	var list []models.Guest
	err := configuration.DB.
		Model(&models.Guest{}).
		Select("guests.*, invitation_access_tokens.pretty_token AS pretty_token, COALESCE(invitations.max_guests, 0) AS max_guests").
		Joins("LEFT JOIN invitation_access_tokens ON invitation_access_tokens.invitation_id = guests.invitation_id").
		Joins("LEFT JOIN invitations ON invitations.id = guests.invitation_id AND invitations.deleted_at IS NULL").
		Preload("GuestStatus").
		Preload("Table").
		Where("guests.event_id = ?", eventID).
		Order("guests.id DESC").
		Find(&list).Error
	return list, err
}

func ListCheckinGuests(eventID uuid.UUID, query dtos.CheckinGuestsListQuery) ([]models.Guest, int64, error) {
	statusExpr := "LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), \"GuestStatus\".code, 'pending')))"
	baseQuery := func() *gorm.DB {
		base := configuration.DB.Model(&models.Guest{}).
			Joins("LEFT JOIN invitation_access_tokens ON invitation_access_tokens.invitation_id = guests.invitation_id").
			Joins("LEFT JOIN invitations ON invitations.id = guests.invitation_id AND invitations.deleted_at IS NULL").
			Joins("GuestStatus").
			Joins("Table").
			Where("guests.event_id = ?", eventID)

		if qr := strings.TrimSpace(query.QR); qr != "" {
			base = base.Where("CAST(guests.id AS TEXT) = ? OR invitation_access_tokens.pretty_token = ?", qr, qr)
		} else if search := strings.TrimSpace(query.Search); search != "" {
			pattern := "%" + strings.ToLower(search) + "%"
			base = base.Where(`LOWER(CONCAT_WS(' ', guests.first_name, guests.last_name, guests.email, guests.phone, guests.table_number, "Table".name)) LIKE ?`, pattern)
		}

		switch strings.ToUpper(strings.TrimSpace(query.Filter)) {
		case "CONFIRMED":
			base = base.Where(statusExpr + " = 'confirmed'")
		case "PENDING":
			base = base.Where(statusExpr + " = 'pending'")
		case "DECLINED":
			base = base.Where(statusExpr + " = 'declined'")
		}
		return base
	}

	var total int64

	direction := "ASC"
	if strings.EqualFold(query.Direction, "desc") {
		direction = "DESC"
	}
	order := "LOWER(guests.first_name) " + direction + ", LOWER(guests.last_name) " + direction
	switch strings.ToLower(strings.TrimSpace(query.Sort)) {
	case "status":
		order = statusExpr + " " + direction + ", LOWER(guests.first_name) ASC"
	case "table":
		order = "LOWER(COALESCE(NULLIF(\"Table\".name, ''), guests.table_number, '')) " + direction + ", LOWER(guests.first_name) ASC"
	case "guests_count":
		order = "COALESCE(NULLIF(guests.rsvp_guest_count, 0), NULLIF(invitations.max_guests, 0), 1) " + direction + ", LOWER(guests.first_name) ASC"
	}

	var list []models.Guest
	group := new(errgroup.Group)
	if !query.SkipTotal {
		group.Go(func() error {
			return baseQuery().Count(&total).Error
		})
	}
	group.Go(func() error {
		return baseQuery().Select("guests.*, invitation_access_tokens.pretty_token AS pretty_token, COALESCE(invitations.max_guests, 0) AS max_guests").
			Order(order).Limit(query.PageSize).Offset((query.Page - 1) * query.PageSize).
			Find(&list).Error
	})
	if err := group.Wait(); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// ListAnalyticsGuestsByEventID returns only the fields consumed by dashboard analytics.
func ListAnalyticsGuestsByEventID(eventID uuid.UUID) ([]dtos.AnalyticsGuest, error) {
	var list []dtos.AnalyticsGuest
	err := analyticsGuestsQuery(configuration.DB, eventID).
		Scan(&list).Error
	dtos.HydrateAnalyticsGuestTables(list)
	return list, err
}

func analyticsGuestsQuery(db *gorm.DB, eventID uuid.UUID) *gorm.DB {
	return db.
		Table("guests").
		Select(`
			guests.id,
			guests.first_name,
			guests.last_name,
			guests.role,
			LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, 'pending'))) AS rsvp_status,
			guests.rsvp_at,
			guests.rsvp_method,
			guests.rsvp_guest_count,
			CASE
				WHEN LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, 'pending'))) = 'declined' THEN 0
				WHEN guests.rsvp_guest_count > 0 THEN guests.rsvp_guest_count
				WHEN invitations.max_guests > 0 THEN invitations.max_guests
				ELSE 1
			END AS guests_count,
			guests.dietary_restrictions,
			COALESCE(NULLIF(BTRIM(tables.name), ''), NULLIF(BTRIM(guests.table_number), '')) AS table_name
		`).
		Joins("LEFT JOIN guest_statuses ON guest_statuses.id = guests.guest_status_id AND guest_statuses.deleted_at IS NULL").
		Joins("LEFT JOIN invitations ON invitations.id = guests.invitation_id AND invitations.deleted_at IS NULL").
		Joins("LEFT JOIN tables ON tables.id = guests.table_id AND tables.deleted_at IS NULL").
		Where("guests.event_id = ? AND guests.deleted_at IS NULL", eventID).
		Order("guests.id DESC")
}

// ListSeatingGuestsByEventID returns only the fields required to render and edit table assignments.
func ListSeatingGuestsByEventID(eventID uuid.UUID) ([]dtos.SeatingGuest, error) {
	var list []dtos.SeatingGuest
	err := configuration.DB.
		Table("guests").
		Select(`
			guests.id,
			guests.first_name,
			guests.last_name,
			guests.email,
			guests.table_id,
			LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, 'pending'))) AS rsvp_status,
			guests.rsvp_guest_count,
			CASE
				WHEN LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, 'pending'))) = 'declined' THEN 0
				WHEN guests.rsvp_guest_count > 0 THEN guests.rsvp_guest_count
				WHEN invitations.max_guests > 0 THEN invitations.max_guests
				ELSE 1
			END AS guests_count`).
		Joins("LEFT JOIN guest_statuses ON guest_statuses.id = guests.guest_status_id").
		Joins("LEFT JOIN invitations ON invitations.id = guests.invitation_id").
		Where("guests.event_id = ? AND guests.deleted_at IS NULL", eventID).
		Order("LOWER(guests.first_name) ASC, LOWER(guests.last_name) ASC").
		Scan(&list).Error
	return list, err
}

type guestShareSummaryRow struct {
	Total             int64
	WithEmail         int64
	WithPhone         int64
	PendingWithEmail  int64
	FirstPendingID    *uuid.UUID
	FirstPendingName  *string
	FirstPendingEmail *string
	FirstPendingToken *string
}

type guestDashboardSummariesRow struct {
	Total             int64
	Confirmed         int64
	Pending           int64
	Declined          int64
	TotalAttendees    int64
	WithEmail         int64
	WithPhone         int64
	PendingWithEmail  int64
	FirstPendingID    *uuid.UUID
	FirstPendingName  *string
	FirstPendingEmail *string
	FirstPendingToken *string
}

const guestDashboardSummariesSQL = `
WITH guest_rollup AS (
	SELECT
		guests.id,
		guests.first_name,
		guests.email,
		guests.phone,
		invitation_access_tokens.pretty_token,
		CASE
			WHEN LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, ''))) = 'confirmed' THEN 'confirmed'
			WHEN LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, ''))) = 'declined' THEN 'declined'
			ELSE 'pending'
		END AS effective_status,
		CASE
			WHEN guests.rsvp_guest_count > 0 THEN guests.rsvp_guest_count
			WHEN invitations.max_guests > 0 THEN invitations.max_guests
			ELSE 1
		END AS party_size
	FROM guests
	LEFT JOIN guest_statuses ON guest_statuses.id = guests.guest_status_id AND guest_statuses.deleted_at IS NULL
	LEFT JOIN invitations ON invitations.id = guests.invitation_id AND invitations.deleted_at IS NULL
	LEFT JOIN invitation_access_tokens ON invitation_access_tokens.invitation_id = guests.invitation_id
	WHERE guests.event_id = ? AND guests.deleted_at IS NULL
)
SELECT
	COUNT(*) AS total,
	COUNT(*) FILTER (WHERE effective_status = 'confirmed') AS confirmed,
	COUNT(*) FILTER (WHERE effective_status = 'pending') AS pending,
	COUNT(*) FILTER (WHERE effective_status = 'declined') AS declined,
	COALESCE(SUM(party_size) FILTER (WHERE effective_status = 'confirmed'), 0) AS total_attendees,
	COUNT(*) FILTER (WHERE BTRIM(email) <> '') AS with_email,
	COUNT(*) FILTER (WHERE BTRIM(phone) <> '') AS with_phone,
	COUNT(*) FILTER (WHERE effective_status = 'pending' AND BTRIM(email) <> '' AND BTRIM(COALESCE(pretty_token, '')) <> '') AS pending_with_email,
	(ARRAY_AGG(id ORDER BY id DESC) FILTER (WHERE effective_status = 'pending' AND BTRIM(email) <> '' AND BTRIM(COALESCE(pretty_token, '')) <> ''))[1] AS first_pending_id,
	(ARRAY_AGG(first_name ORDER BY id DESC) FILTER (WHERE effective_status = 'pending' AND BTRIM(email) <> '' AND BTRIM(COALESCE(pretty_token, '')) <> ''))[1] AS first_pending_name,
	(ARRAY_AGG(email ORDER BY id DESC) FILTER (WHERE effective_status = 'pending' AND BTRIM(email) <> '' AND BTRIM(COALESCE(pretty_token, '')) <> ''))[1] AS first_pending_email,
	(ARRAY_AGG(pretty_token ORDER BY id DESC) FILTER (WHERE effective_status = 'pending' AND BTRIM(email) <> '' AND BTRIM(COALESCE(pretty_token, '')) <> ''))[1] AS first_pending_token
FROM guest_rollup
`

func guestDashboardSummariesQuery(db *gorm.DB, eventID uuid.UUID) *gorm.DB {
	return db.Raw(guestDashboardSummariesSQL, eventID)
}

func GetGuestDashboardSummariesByEventID(eventID uuid.UUID) (dtos.GuestSummary, dtos.GuestShareSummary, error) {
	var row guestDashboardSummariesRow
	err := guestDashboardSummariesQuery(configuration.DB, eventID).Scan(&row).Error
	guestSummary := dtos.GuestSummary{
		Total: row.Total, Confirmed: row.Confirmed, Pending: row.Pending,
		Declined: row.Declined, TotalAttendees: row.TotalAttendees,
	}
	shareSummary := dtos.GuestShareSummary{
		Total: row.Total, WithEmail: row.WithEmail, WithPhone: row.WithPhone, PendingWithEmail: row.PendingWithEmail,
	}
	if row.FirstPendingID != nil {
		shareSummary.FirstPending = &dtos.GuestShareRecipient{ID: *row.FirstPendingID}
		if row.FirstPendingName != nil {
			shareSummary.FirstPending.FirstName = *row.FirstPendingName
		}
		if row.FirstPendingEmail != nil {
			shareSummary.FirstPending.Email = *row.FirstPendingEmail
		}
		if row.FirstPendingToken != nil {
			shareSummary.FirstPending.PrettyToken = *row.FirstPendingToken
		}
	}
	return guestSummary, shareSummary, err
}

func guestShareSummaryQuery(db *gorm.DB, eventID uuid.UUID) *gorm.DB {
	statusExpression := "LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, 'pending')))"
	pendingRecipientCondition := statusExpression + " = 'pending' AND BTRIM(guests.email) <> '' AND BTRIM(COALESCE(invitation_access_tokens.pretty_token, '')) <> ''"
	return db.
		Table("guests").
		Select(`
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE BTRIM(guests.email) <> '') AS with_email,
			COUNT(*) FILTER (WHERE BTRIM(guests.phone) <> '') AS with_phone,
			COUNT(*) FILTER (
				WHERE `+statusExpression+` = 'pending'
				AND BTRIM(guests.email) <> ''
				AND BTRIM(COALESCE(invitation_access_tokens.pretty_token, '')) <> ''
			) AS pending_with_email,
			(ARRAY_AGG(guests.id ORDER BY guests.id DESC) FILTER (WHERE `+pendingRecipientCondition+`))[1] AS first_pending_id,
			(ARRAY_AGG(guests.first_name ORDER BY guests.id DESC) FILTER (WHERE `+pendingRecipientCondition+`))[1] AS first_pending_name,
			(ARRAY_AGG(guests.email ORDER BY guests.id DESC) FILTER (WHERE `+pendingRecipientCondition+`))[1] AS first_pending_email,
			(ARRAY_AGG(invitation_access_tokens.pretty_token ORDER BY guests.id DESC) FILTER (WHERE `+pendingRecipientCondition+`))[1] AS first_pending_token
		`).
		Joins("LEFT JOIN guest_statuses ON guest_statuses.id = guests.guest_status_id AND guest_statuses.deleted_at IS NULL").
		Joins("LEFT JOIN invitation_access_tokens ON invitation_access_tokens.invitation_id = guests.invitation_id").
		Where("guests.event_id = ? AND guests.deleted_at IS NULL", eventID)
}

func GetGuestShareSummaryByEventID(eventID uuid.UUID) (dtos.GuestShareSummary, error) {
	var row guestShareSummaryRow
	err := guestShareSummaryQuery(configuration.DB, eventID).Scan(&row).Error
	summary := dtos.GuestShareSummary{
		Total: row.Total, WithEmail: row.WithEmail, WithPhone: row.WithPhone, PendingWithEmail: row.PendingWithEmail,
	}
	if err != nil {
		return summary, err
	}
	if row.FirstPendingID != nil {
		summary.FirstPending = &dtos.GuestShareRecipient{ID: *row.FirstPendingID}
		if row.FirstPendingName != nil {
			summary.FirstPending.FirstName = *row.FirstPendingName
		}
		if row.FirstPendingEmail != nil {
			summary.FirstPending.Email = *row.FirstPendingEmail
		}
		if row.FirstPendingToken != nil {
			summary.FirstPending.PrettyToken = *row.FirstPendingToken
		}
	}
	return summary, nil
}

const guestSummarySQL = `
WITH guest_rollup AS (
	SELECT
		CASE
			WHEN LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, ''))) = 'confirmed' THEN 'confirmed'
			WHEN LOWER(BTRIM(COALESCE(NULLIF(BTRIM(guests.rsvp_status), ''), guest_statuses.code, ''))) = 'declined' THEN 'declined'
			ELSE 'pending'
		END AS effective_status,
		CASE
			WHEN guests.rsvp_guest_count > 0 THEN guests.rsvp_guest_count
			WHEN invitations.max_guests > 0 THEN invitations.max_guests
			ELSE 1
		END AS party_size
	FROM guests
	LEFT JOIN guest_statuses
		ON guest_statuses.id = guests.guest_status_id
		AND guest_statuses.deleted_at IS NULL
	LEFT JOIN invitations
		ON invitations.id = guests.invitation_id
		AND invitations.deleted_at IS NULL
	WHERE guests.event_id = ?
		AND guests.deleted_at IS NULL
)
SELECT
	COUNT(*) AS total,
	COUNT(*) FILTER (WHERE effective_status = 'confirmed') AS confirmed,
	COUNT(*) FILTER (WHERE effective_status = 'pending') AS pending,
	COUNT(*) FILTER (WHERE effective_status = 'declined') AS declined,
	COALESCE(SUM(party_size) FILTER (WHERE effective_status = 'confirmed'), 0) AS total_attendees
FROM guest_rollup
`

func guestSummaryQuery(db *gorm.DB, eventID uuid.UUID) *gorm.DB {
	return db.Raw(guestSummarySQL, eventID)
}

// GetGuestSummaryByEventID returns the complete RSVP rollup in one aggregate
// query. It intentionally avoids hydrating Guest models or running preload
// queries on this dashboard hot path.
func GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error) {
	var summary dtos.GuestSummary
	err := guestSummaryQuery(configuration.DB, eventID).Scan(&summary).Error
	return summary, err
}

func GetGuestByIDAsString(id uuid.UUID) (string, error) {
	var guest models.Guest
	err := gormrepository.GetByID(&guest, id)
	if err != nil {
		return "", err
	}
	return utils.MarshallData([]*models.Guest{&guest}, nil)
}

func CreateGuest(m *models.Guest) error {
	return gormrepository.Insert(m)
}

func UpdateGuest(m *models.Guest) error {
	result := configuration.DB.
		Model(&models.Guest{}).
		Where("id = ?", m.ID).
		Select("*").
		Omit("Event", "Table", "Invitation", "GuestStatus", "Status").
		Updates(m)
	err := result.Error
	if err == nil && result.RowsAffected == 0 {
		err = errors.New("record not found")
	}
	return err
}

func DeleteGuest(id uuid.UUID) error {
	var guest models.Guest
	if err := gormrepository.GetByID(&guest, id); err != nil {
		return err
	}
	return gormrepository.Delete(id, &models.Guest{})
}

func GetGuestByID(id uuid.UUID) (*models.Guest, error) {
	var model models.Guest
	err := configuration.DB.
		Model(&models.Guest{}).
		Select("guests.*, invitation_access_tokens.pretty_token AS pretty_token").
		Joins("LEFT JOIN invitation_access_tokens ON invitation_access_tokens.invitation_id = guests.invitation_id").
		Preload("GuestStatus").
		Preload("Invitation").
		Preload("Table").
		Where("guests.id = ?", id).
		First(&model).Error
	return &model, err
}

func ListGuests() ([]models.Guest, error) {
	var list []models.Guest
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}

func CreateGuests(models []models.Guest) error {
	if len(models) == 0 {
		return nil
	}

	return gormrepository.InsertMany(models)
}

// BulkDeleteGuests soft-deletes multiple guests by ID.
func BulkDeleteGuests(ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return configuration.DB.Where("id IN ?", ids).Delete(&models.Guest{}).Error
}

func BulkUpdateGuestStatus(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error {
	if len(ids) == 0 {
		return nil
	}
	return configuration.DB.Transaction(func(tx *gorm.DB) error {
		var statusCount int64
		if err := tx.Model(&models.GuestStatus{}).Where("id = ?", statusID).Count(&statusCount).Error; err != nil {
			return err
		}
		if statusCount != 1 {
			return fmt.Errorf("guest status does not exist")
		}
		result := tx.Model(&models.Guest{}).
			Where("event_id = ? AND id IN ?", eventID, ids).
			Updates(map[string]interface{}{
				"guest_status_id": statusID,
				"rsvp_status":     rsvpStatus, "rsvp_method": rsvpMethod,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return fmt.Errorf("one or more guests do not belong to event")
		}
		return nil
	})
}

// ListAttendeesByEventID returns guests for an event ordered by display order.
// Only public-safe profile fields are returned.
func ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	var list []models.Guest
	err := configuration.DB.
		Select("first_name", "last_name", "nickname", "role", "\"order\"", "is_host", "image_url", "headline", "bio", "signature").
		Where("event_id = ? AND deleted_at IS NULL", eventID).
		Order("\"order\" ASC, first_name ASC, last_name ASC, id ASC").
		Find(&list).Error
	return list, err
}

func LatestPublicAttendeeUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error) {
	var latest sql.NullTime
	err := configuration.DB.
		Table("guests").
		Select(`
			MAX(
				CASE
					WHEN deleted_at IS NOT NULL AND deleted_at > updated_at AND deleted_at > created_at THEN deleted_at
					WHEN updated_at > created_at THEN updated_at
					ELSE created_at
				END
			)
		`).
		Where("event_id = ?", eventID).
		Scan(&latest).Error
	if err != nil {
		return nil, err
	}
	if !latest.Valid {
		return nil, nil
	}
	return &latest.Time, nil
}

// GuestRepo implements ports.GuestRepository for DI.
type GuestRepo struct{}

func NewGuestRepo() *GuestRepo { return &GuestRepo{} }

func (r *GuestRepo) CreateGuest(m *models.Guest) error                { return CreateGuest(m) }
func (r *GuestRepo) UpdateGuest(m *models.Guest) error                { return UpdateGuest(m) }
func (r *GuestRepo) DeleteGuest(id uuid.UUID) error                   { return DeleteGuest(id) }
func (r *GuestRepo) GetGuestByID(id uuid.UUID) (*models.Guest, error) { return GetGuestByID(id) }
func (r *GuestRepo) GetGuestByInvitationID(id uuid.UUID) (*models.Guest, error) {
	return GetGuestByInvitationID(id)
}
func (r *GuestRepo) CreateGuests(guests []models.Guest) error { return CreateGuests(guests) }
func (r *GuestRepo) GetPendingStatusID() uuid.UUID            { return GetPendingStatusID() }
func (r *GuestRepo) BulkDeleteGuests(ids []uuid.UUID) error   { return BulkDeleteGuests(ids) }
func (r *GuestRepo) BulkUpdateGuestStatus(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error {
	return BulkUpdateGuestStatus(eventID, ids, statusID, rsvpStatus, rsvpMethod)
}
func (r *GuestRepo) ListGuestsByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return ListGuestsByEventID(eventID)
}
func (r *GuestRepo) ListCheckinGuests(eventID uuid.UUID, query dtos.CheckinGuestsListQuery) ([]models.Guest, int64, error) {
	return ListCheckinGuests(eventID, query)
}
func (r *GuestRepo) ListAnalyticsGuestsByEventID(eventID uuid.UUID) ([]dtos.AnalyticsGuest, error) {
	return ListAnalyticsGuestsByEventID(eventID)
}
func (r *GuestRepo) ListSeatingGuestsByEventID(eventID uuid.UUID) ([]dtos.SeatingGuest, error) {
	return ListSeatingGuestsByEventID(eventID)
}
func (r *GuestRepo) GetGuestShareSummaryByEventID(eventID uuid.UUID) (dtos.GuestShareSummary, error) {
	return GetGuestShareSummaryByEventID(eventID)
}
func (r *GuestRepo) GetGuestDashboardSummariesByEventID(eventID uuid.UUID) (dtos.GuestSummary, dtos.GuestShareSummary, error) {
	return GetGuestDashboardSummariesByEventID(eventID)
}
func (r *GuestRepo) GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error) {
	return GetGuestSummaryByEventID(eventID)
}
func (r *GuestRepo) ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return ListAttendeesByEventID(eventID)
}
func (r *GuestRepo) LatestPublicAttendeeUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error) {
	return LatestPublicAttendeeUpdatedAtByEventID(eventID)
}
