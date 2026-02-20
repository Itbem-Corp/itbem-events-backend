package guestrepository

import (
	"context"
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/gueststatusrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
)

var pendingStatusID uuid.UUID

func GetPendingStatusID() uuid.UUID {
	if pendingStatusID == uuid.Nil {
		status, err := gueststatusrepository.GetGuestStatusByCode("pending")
		if err != nil {
			fmt.Printf("Error loading pending status: %v\n", err)
			return uuid.Nil
		}
		if status == nil {
			fmt.Println("Pending status not found in DB")
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
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{
		Filters: map[string]interface{}{
			"event_id": eventID,
		},
		OrderBy:  "id",
		OrderDir: "desc",
	})
	return list, err
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
	err := gormrepository.Insert(m)
	if err != nil {
		return err
	}

	if m.EventID != uuid.Nil {
		pattern := "all:" + m.EventID.String() + ":guests"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}

func UpdateGuest(m *models.Guest) error {
	err := gormrepository.Update(m, m.ID)
	if err == nil && m.EventID != uuid.Nil {
		pattern := "all:" + m.EventID.String() + ":guests"
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
			return delErr
		}
	}
	return err
}

func DeleteGuest(id uuid.UUID) error {
	// Traemos primero el guest para conocer su EventID
	var guest models.Guest
	if err := gormrepository.GetByID(&guest, id); err != nil {
		return err
	}

	// Eliminamos el registro
	if err := gormrepository.Delete(id, &models.Guest{}); err != nil {
		return err
	}

	// Invalidamos solo los guests de ese evento
	if guest.EventID != uuid.Nil {
		pattern := "all:" + guest.EventID.String() + ":guests"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}

func GetGuestByID(id uuid.UUID) (*models.Guest, error) {
	var model models.Guest
	err := gormrepository.GetByID(&model, id)
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

	err := gormrepository.InsertMany(models)
	if err != nil {
		return err
	}

	eventID := models[0].EventID
	if eventID != uuid.Nil {
		pattern := "all:" + eventID.String() + ":guests"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}

// ListAttendeesByEventID returns guests for an event ordered by display order.
// Only public-safe fields are returned (first_name, last_name, nickname, role, order).
func ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	var list []models.Guest
	err := configuration.DB.
		Select("first_name", "last_name", "nickname", "role", "\"order\"").
		Where("event_id = ? AND deleted_at IS NULL", eventID).
		Order("\"order\" ASC").
		Find(&list).Error
	return list, err
}

// GuestRepo implements ports.GuestRepository for DI.
type GuestRepo struct{}

func NewGuestRepo() *GuestRepo { return &GuestRepo{} }

func (r *GuestRepo) CreateGuest(m *models.Guest) error            { return CreateGuest(m) }
func (r *GuestRepo) UpdateGuest(m *models.Guest) error            { return UpdateGuest(m) }
func (r *GuestRepo) DeleteGuest(id uuid.UUID) error               { return DeleteGuest(id) }
func (r *GuestRepo) GetGuestByID(id uuid.UUID) (*models.Guest, error) { return GetGuestByID(id) }
func (r *GuestRepo) GetGuestByInvitationID(id uuid.UUID) (*models.Guest, error) {
	return GetGuestByInvitationID(id)
}
func (r *GuestRepo) CreateGuests(guests []models.Guest) error { return CreateGuests(guests) }
func (r *GuestRepo) GetPendingStatusID() uuid.UUID            { return GetPendingStatusID() }
