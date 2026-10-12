// Package eventclientrepository stores EventiApp event-client grants
// (event_client_grants). Every "active" read applies the same predicates as
// authorization: the grant is not revoked, the event is not deleted and still
// belongs to the grant's organization, the organization is active and not
// deleted, and the holder has no client_members row (active or inactive) in
// that organization.
package eventclientrepository

import (
	"errors"
	"strings"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const activeGrantJoins = `
	JOIN events e ON e.id = g.event_id AND e.deleted_at IS NULL AND e.client_id = g.organization_id
	JOIN clients c ON c.id = g.organization_id AND c.deleted_at IS NULL AND c.is_active = true`

const activeGrantPredicate = `g.revoked_at IS NULL
	AND NOT EXISTS (SELECT 1 FROM client_members cm WHERE cm.user_id = g.user_id AND cm.client_id = g.organization_id)`

// ErrGrantConflict is returned when the active grant could not be read back
// after an insert that did nothing.
var ErrGrantConflict = errors.New("event client grant could not be confirmed")

type Repository struct{ db *gorm.DB }

func New(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) conn(tx *gorm.DB) *gorm.DB {
	if tx != nil {
		return tx
	}
	return r.db
}

// Transaction runs fn in one database transaction.
func (r *Repository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

func (r *Repository) HasActiveGrant(eventID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.Raw(`SELECT EXISTS (SELECT 1 FROM event_client_grants g `+activeGrantJoins+`
		WHERE g.event_id = ? AND g.user_id = ? AND `+activeGrantPredicate+`)`, eventID, userID).Row().Scan(&exists)
	return exists, err
}

func (r *Repository) CountActiveForUser(userID uuid.UUID) (int, error) {
	var count int64
	err := r.db.Raw(`SELECT COUNT(DISTINCT g.event_id) FROM event_client_grants g `+activeGrantJoins+`
		WHERE g.user_id = ? AND `+activeGrantPredicate, userID).Row().Scan(&count)
	return int(count), err
}

func (r *Repository) CountActiveForUserInOrganization(orgID, userID uuid.UUID) (int, error) {
	var count int64
	err := r.db.Raw(`SELECT COUNT(DISTINCT g.event_id) FROM event_client_grants g `+activeGrantJoins+`
		WHERE g.user_id = ? AND g.organization_id = ? AND `+activeGrantPredicate, userID, orgID).Row().Scan(&count)
	return int(count), err
}

func (r *Repository) ActiveGrantedEventIDs(userID uuid.UUID, eventIDs []uuid.UUID) (map[uuid.UUID]struct{}, error) {
	result := make(map[uuid.UUID]struct{})
	if len(eventIDs) == 0 {
		return result, nil
	}
	var ids []uuid.UUID
	err := r.db.Raw(`SELECT DISTINCT g.event_id FROM event_client_grants g `+activeGrantJoins+`
		WHERE g.user_id = ? AND g.event_id IN ? AND `+activeGrantPredicate, userID, eventIDs).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result, nil
}

type EventClientRow struct {
	UserID          uuid.UUID `json:"user_id"`
	Email           string    `json:"email"`
	FirstName       string    `json:"first_name"`
	LastName        string    `json:"last_name"`
	GrantedAt       time.Time `json:"granted_at"`
	GrantedByUserID uuid.UUID `json:"granted_by_user_id"`
}

func (r *Repository) ListActiveForEvent(eventID uuid.UUID) ([]EventClientRow, error) {
	rows := make([]EventClientRow, 0)
	err := r.db.Raw(`SELECT u.id AS user_id, u.email, u.first_name, u.last_name,
			g.created_at AS granted_at, g.granted_by_user_id
		FROM event_client_grants g `+activeGrantJoins+`
		JOIN users u ON u.id = g.user_id AND u.deleted_at IS NULL
		WHERE g.event_id = ? AND `+activeGrantPredicate+`
		ORDER BY g.created_at ASC`, eventID).Scan(&rows).Error
	return rows, err
}

func (r *Repository) ActiveGrantForEvent(eventID, userID uuid.UUID) (*EventClientRow, error) {
	var row EventClientRow
	result := r.db.Raw(`SELECT u.id AS user_id, u.email, u.first_name, u.last_name,
			g.created_at AS granted_at, g.granted_by_user_id
		FROM event_client_grants g `+activeGrantJoins+`
		JOIN users u ON u.id = g.user_id AND u.deleted_at IS NULL
		WHERE g.event_id = ? AND g.user_id = ? AND `+activeGrantPredicate+`
		LIMIT 1`, eventID, userID).Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &row, nil
}

type PersonRow struct {
	UserID    uuid.UUID `json:"user_id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
}

type OrganizationGrantRow struct {
	GrantID       uuid.UUID `json:"grant_id"`
	EventID       uuid.UUID `json:"event_id"`
	EventName     string    `json:"event_name"`
	EventDateTime time.Time `json:"event_date_time"`
	Client        PersonRow `json:"client" gorm:"embedded;embeddedPrefix:client_"`
	GrantedBy     PersonRow `json:"granted_by" gorm:"embedded;embeddedPrefix:granted_by_"`
	GrantedAt     time.Time `json:"granted_at"`
}

func (r *Repository) ListActiveForOrganization(orgID uuid.UUID, page, pageSize int) ([]OrganizationGrantRow, int, error) {
	base := `FROM event_client_grants g ` + activeGrantJoins + `
		JOIN users u ON u.id = g.user_id AND u.deleted_at IS NULL
		LEFT JOIN users gb ON gb.id = g.granted_by_user_id
		WHERE g.organization_id = ? AND ` + activeGrantPredicate
	var total int64
	if err := r.db.Raw(`SELECT COUNT(*) `+base, orgID).Row().Scan(&total); err != nil {
		return nil, 0, err
	}
	rows := make([]OrganizationGrantRow, 0)
	err := r.db.Raw(`SELECT g.id AS grant_id, g.event_id, e.name AS event_name, e.event_date_time,
			u.id AS client_user_id, u.email AS client_email, u.first_name AS client_first_name, u.last_name AS client_last_name,
			g.granted_by_user_id AS granted_by_user_id, COALESCE(gb.email, '') AS granted_by_email,
			COALESCE(gb.first_name, '') AS granted_by_first_name, COALESCE(gb.last_name, '') AS granted_by_last_name,
			g.created_at AS granted_at `+base+`
		ORDER BY g.created_at DESC, g.id ASC
		LIMIT ? OFFSET ?`, orgID, pageSize, (page-1)*pageSize).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, int(total), nil
}

type CandidateRow struct {
	UserID          uuid.UUID `json:"user_id"`
	Email           string    `json:"email"`
	FirstName       string    `json:"first_name"`
	LastName        string    `json:"last_name"`
	AssignedEvents  int       `json:"assigned_events"`
	AssignedToEvent bool      `json:"assigned_to_event"`
}

// ListOrganizationClients lists people who hold or held a grant in orgID.
// It never lists anyone outside that organization's grant history.
func (r *Repository) ListOrganizationClients(orgID, eventID uuid.UUID, search string, limit int) ([]CandidateRow, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	args := []interface{}{eventID, orgID, orgID}
	filter := ""
	if normalized := strings.ToLower(strings.TrimSpace(search)); normalized != "" {
		like := "%" + escapeLike(normalized) + "%"
		filter = ` AND (LOWER(u.email) LIKE ? OR LOWER(u.first_name) LIKE ? OR LOWER(u.last_name) LIKE ?)`
		args = append(args, like, like, like)
	}
	args = append(args, limit)
	rows := make([]CandidateRow, 0)
	err := r.db.Raw(`SELECT u.id AS user_id, u.email, u.first_name, u.last_name,
			(SELECT COUNT(DISTINCT g.event_id) FROM event_client_grants g `+activeGrantJoins+`
				WHERE g.user_id = u.id AND g.organization_id = people.organization_id AND `+activeGrantPredicate+`) AS assigned_events,
			EXISTS (SELECT 1 FROM event_client_grants g `+activeGrantJoins+`
				WHERE g.user_id = u.id AND g.event_id = ? AND `+activeGrantPredicate+`) AS assigned_to_event
		FROM (SELECT DISTINCT user_id, organization_id FROM event_client_grants WHERE organization_id = ?) people
		JOIN users u ON u.id = people.user_id AND u.deleted_at IS NULL
		WHERE people.organization_id = ?`+filter+`
		ORDER BY LOWER(u.first_name), LOWER(u.last_name), u.email
		LIMIT ?`, args...).Scan(&rows).Error
	return rows, err
}

// IsOrganizationClient reports whether userID holds or held any grant in orgID.
func (r *Repository) IsOrganizationClient(orgID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.Raw(`SELECT EXISTS (SELECT 1 FROM event_client_grants WHERE organization_id = ? AND user_id = ?)`, orgID, userID).Row().Scan(&exists)
	return exists, err
}

type AssignmentRow struct {
	EventID       uuid.UUID `json:"event_id"`
	Name          string    `json:"name"`
	EventDateTime time.Time `json:"event_date_time"`
	Timezone      string    `json:"timezone"`
	IsActive      bool      `json:"is_active"`
	Assigned      bool      `json:"assigned"`
	CanManage     bool      `json:"can_manage" gorm:"-"`
}

// ListOrganizationEventsForClient pages the organization's events with the
// client's assignment state on each.
func (r *Repository) ListOrganizationEventsForClient(orgID, userID uuid.UUID, search string, page, pageSize int) ([]AssignmentRow, int, error) {
	args := []interface{}{orgID}
	filter := ""
	if normalized := strings.ToLower(strings.TrimSpace(search)); normalized != "" {
		filter = ` AND LOWER(ev.name) LIKE ?`
		args = append(args, "%"+escapeLike(normalized)+"%")
	}
	base := `FROM events ev WHERE ev.client_id = ? AND ev.deleted_at IS NULL` + filter
	var total int64
	if err := r.db.Raw(`SELECT COUNT(*) `+base, args...).Row().Scan(&total); err != nil {
		return nil, 0, err
	}
	rows := make([]AssignmentRow, 0)
	pageArgs := append([]interface{}{userID}, args...)
	pageArgs = append(pageArgs, pageSize, (page-1)*pageSize)
	err := r.db.Raw(`SELECT ev.id AS event_id, ev.name, ev.event_date_time, ev.timezone, ev.is_active,
			EXISTS (SELECT 1 FROM event_client_grants g `+activeGrantJoins+`
				WHERE g.event_id = ev.id AND g.user_id = ? AND `+activeGrantPredicate+`) AS assigned
		`+base+`
		ORDER BY ev.event_date_time DESC, ev.id ASC
		LIMIT ? OFFSET ?`, pageArgs...).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, int(total), nil
}

type EventOwnerRow struct {
	ID       uuid.UUID
	ClientID *uuid.UUID
}

// EventOrganizations returns the organization of each non-deleted event.
func (r *Repository) EventOrganizations(eventIDs []uuid.UUID) (map[uuid.UUID]*uuid.UUID, error) {
	result := make(map[uuid.UUID]*uuid.UUID, len(eventIDs))
	if len(eventIDs) == 0 {
		return result, nil
	}
	var rows []EventOwnerRow
	if err := r.db.Raw(`SELECT id, client_id FROM events WHERE id IN ? AND deleted_at IS NULL`, eventIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		clientID := row.ClientID
		result[row.ID] = clientID
	}
	return result, nil
}

type LiveUser struct {
	ID         uuid.UUID
	CognitoSub string
	IsActive   bool
}

// FindLiveUsersByEmail returns at most limit non-deleted users whose email
// matches case-insensitively. Two rows mean the email is ambiguous.
func (r *Repository) FindLiveUsersByEmail(email string, limit int) ([]LiveUser, error) {
	rows := make([]LiveUser, 0)
	err := r.db.Raw(`SELECT id, cognito_sub, is_active FROM users
		WHERE LOWER(email) = LOWER(?) AND deleted_at IS NULL
		ORDER BY created_at ASC LIMIT ?`, strings.TrimSpace(email), limit).Scan(&rows).Error
	return rows, err
}

// HasMembershipInOrganizationTree reports any client_members row (active or
// not) of userID in orgID or one of its ancestors.
func (r *Repository) HasMembershipInOrganizationTree(orgID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.Raw(`
		WITH RECURSIVE ancestry AS (
			SELECT id, parent_id, 0 AS depth FROM clients WHERE id = ?
			UNION ALL
			SELECT parent.id, parent.parent_id, ancestry.depth + 1
			FROM ancestry JOIN clients parent ON parent.id = ancestry.parent_id
			WHERE ancestry.depth < 31
		)
		SELECT EXISTS (SELECT 1 FROM client_members cm JOIN ancestry ON ancestry.id = cm.client_id WHERE cm.user_id = ?)
	`, orgID, userID).Row().Scan(&exists)
	return exists, err
}

// Grant creates the active grant for (event, user) or returns the existing
// one. Active rows left by an earlier organization of the event are revoked
// first, so a moved event never keeps a stale grant.
func (r *Repository) Grant(tx *gorm.DB, eventID, userID, orgID, actorID uuid.UUID) (bool, *models.EventClientGrant, error) {
	db := r.conn(tx)
	now := time.Now().UTC()
	if err := db.Model(&models.EventClientGrant{}).
		Where("event_id = ? AND user_id = ? AND revoked_at IS NULL AND organization_id <> ?", eventID, userID, orgID).
		Updates(map[string]interface{}{"revoked_at": now, "revoked_by_user_id": actorID, "updated_at": now}).Error; err != nil {
		return false, nil, err
	}
	grant := models.EventClientGrant{
		ID:              uuid.Must(uuid.NewV4()),
		EventID:         eventID,
		UserID:          userID,
		OrganizationID:  orgID,
		GrantedByUserID: actorID,
	}
	result := db.Clauses(clause.OnConflict{
		Columns:     []clause.Column{{Name: "event_id"}, {Name: "user_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "revoked_at IS NULL"}}},
		DoNothing:   true,
	}).Create(&grant)
	if result.Error != nil {
		return false, nil, result.Error
	}
	created := result.RowsAffected > 0
	var active models.EventClientGrant
	if err := db.Where("event_id = ? AND user_id = ? AND revoked_at IS NULL", eventID, userID).First(&active).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil, ErrGrantConflict
		}
		return false, nil, err
	}
	return created, &active, nil
}

func (r *Repository) revoke(tx *gorm.DB, actorID uuid.UUID, where string, args ...interface{}) (int64, error) {
	now := time.Now().UTC()
	result := r.conn(tx).Model(&models.EventClientGrant{}).
		Where("revoked_at IS NULL").
		Where(where, args...).
		Updates(map[string]interface{}{"revoked_at": now, "revoked_by_user_id": actorID, "updated_at": now})
	return result.RowsAffected, result.Error
}

// Revoke ends the active grant of userID on eventID. It reports whether a row
// was revoked.
func (r *Repository) Revoke(tx *gorm.DB, eventID, userID, actorID uuid.UUID) (bool, error) {
	count, err := r.revoke(tx, actorID, "event_id = ? AND user_id = ?", eventID, userID)
	return count > 0, err
}

func (r *Repository) RevokeAllForEvent(tx *gorm.DB, eventID, actorID uuid.UUID) (int64, error) {
	return r.revoke(tx, actorID, "event_id = ?", eventID)
}

func (r *Repository) RevokeAllForOrganizationUser(tx *gorm.DB, orgID, userID, actorID uuid.UUID) (int64, error) {
	return r.revoke(tx, actorID, "organization_id = ? AND user_id = ?", orgID, userID)
}

// UserIDsWithActiveGrantsOnEvent lists holders of active rows on an event,
// used to invalidate their cached sessions after a bulk revoke.
func (r *Repository) UserIDsWithActiveGrantsOnEvent(eventID uuid.UUID) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0)
	err := r.db.Raw(`SELECT DISTINCT user_id FROM event_client_grants WHERE event_id = ? AND revoked_at IS NULL`, eventID).Scan(&ids).Error
	return ids, err
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
