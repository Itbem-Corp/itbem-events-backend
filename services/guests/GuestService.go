package guests

import (
	"context"
	"events-stocks/models"
	guestrepository "events-stocks/repositories/guestrepository"
	"events-stocks/services/ports"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

// _guestSvc is the package-level singleton set by server.go.
var _guestSvc *GuestService

// SetDefaultGuestService wires the package-level functions to the DI instance.
func SetDefaultGuestService(svc *GuestService) { _guestSvc = svc }

func CreateGuestsWithInvitations(guests []models.Guest) error {
	return _guestSvc.CreateGuestsWithInvitations(guests)
}
func CreateGuest(obj *models.Guest) error    { return _guestSvc.CreateGuest(obj) }
func UpdateGuest(obj *models.Guest) error    { return _guestSvc.UpdateGuest(obj) }
func DeleteGuest(id uuid.UUID) error         { return _guestSvc.DeleteGuest(id) }
func CreateGuests(objs []models.Guest) error { return _guestSvc.CreateGuests(objs) }
func BulkDeleteGuests(ids []uuid.UUID) error { return _guestSvc.BulkDeleteGuests(ids) }

// GuestService is the injectable, struct-based guest service.
type GuestService struct {
	repo        ports.GuestRepository
	accessToken ports.AccessTokenRepository
	cache       ports.CacheRepository
	tx          ports.Transactor
}

func NewGuestService(repo ports.GuestRepository, accessToken ports.AccessTokenRepository, cache ports.CacheRepository, tx ports.Transactor) *GuestService {
	return &GuestService{repo: repo, accessToken: accessToken, cache: cache, tx: tx}
}

func (s *GuestService) CreateGuestsWithInvitations(guests []models.Guest) error {
	if len(guests) == 0 {
		return nil
	}
	now := time.Now()
	usedPretty := make(map[string]struct{})

	var invitations []models.Invitation
	for i := range guests {
		if guests[i].GuestStatusID == uuid.Nil {
			guests[i].GuestStatusID = s.repo.GetPendingStatusID()
		}
		if guests[i].Role == "Host" {
			guests[i].IsHost = true
		}
		invID := uuid.Must(uuid.NewV4())
		guests[i].InvitationID = &invID
		invitations = append(invitations, models.Invitation{
			ID:          invID,
			EventID:     guests[i].EventID,
			Type:        "default",
			SubType:     "general",
			EnableEmail: true,
			MaxGuests:   guests[i].MaxGuests,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	var tokens []models.InvitationAccessToken
	for _, inv := range invitations {
		var pretty string
		for {
			tmp, _ := s.accessToken.GeneratePrettyToken(inv.EventID, 8)
			if _, exists := usedPretty[tmp]; !exists {
				usedPretty[tmp] = struct{}{}
				pretty = tmp
				break
			}
		}
		tokens = append(tokens, models.InvitationAccessToken{
			InvitationID: inv.ID,
			Token:        uuid.Must(uuid.NewV4()).String(),
			PrettyToken:  pretty,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	var logs []models.InvitationLog
	for _, inv := range invitations {
		logs = append(logs, models.InvitationLog{
			InvitationID: inv.ID,
			Channel:      "system",
			Action:       "created",
			Status:       "success",
			Response:     "Invitation created automatically with guest",
			Timestamp:    now,
			CreatedAt:    now,
		})
	}

	err := s.tx.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&invitations).Error; err != nil {
			return err
		}
		if err := tx.Create(&guests).Error; err != nil {
			return err
		}
		if err := tx.Create(&tokens).Error; err != nil {
			return err
		}
		if err := tx.Create(&logs).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	eventID := guests[0].EventID
	if eventID != uuid.Nil {
		pattern := "all:" + eventID.String() + ":guests"
		return s.cache.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}

func (s *GuestService) CreateGuest(obj *models.Guest) error {
	if obj.GuestStatusID == uuid.Nil {
		obj.GuestStatusID = s.repo.GetPendingStatusID()
	}
	if err := s.repo.CreateGuest(obj); err != nil {
		return err
	}
	if obj.EventID != uuid.Nil {
		pattern := "all:" + obj.EventID.String() + ":guests"
		return s.cache.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}

func (s *GuestService) UpdateGuest(obj *models.Guest) error {
	oldGuest, _ := s.repo.GetGuestByID(obj.ID)
	if err := s.repo.UpdateGuest(obj); err != nil {
		return err
	}
	if obj.ID != uuid.Nil {
		_ = s.cache.Invalidate("guests", obj.ID.String())
	}
	if oldGuest != nil && oldGuest.EventID != obj.EventID && oldGuest.EventID != uuid.Nil {
		patternOld := "all:" + oldGuest.EventID.String() + ":guests"
		_ = s.cache.DeleteKeysByPattern(context.Background(), patternOld)
	}
	if obj.EventID != uuid.Nil {
		patternNew := "all:" + obj.EventID.String() + ":guests"
		_ = s.cache.DeleteKeysByPattern(context.Background(), patternNew)
	}
	return nil
}

func (s *GuestService) DeleteGuest(id uuid.UUID) error {
	guest, err := s.repo.GetGuestByID(id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteGuest(id); err != nil {
		return err
	}
	if guest.EventID != uuid.Nil {
		pattern := "all:" + guest.EventID.String() + ":guests"
		return s.cache.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}

func (s *GuestService) BulkDeleteGuests(ids []uuid.UUID) error {
	return guestrepository.BulkDeleteGuests(ids)
}

func (s *GuestService) CreateGuests(objs []models.Guest) error {
	if len(objs) == 0 {
		return nil
	}
	for i := range objs {
		if objs[i].GuestStatusID == uuid.Nil {
			objs[i].GuestStatusID = s.repo.GetPendingStatusID()
		}
	}
	if err := s.repo.CreateGuests(objs); err != nil {
		return err
	}
	eventID := objs[0].EventID
	if eventID != uuid.Nil {
		pattern := "all:" + eventID.String() + ":guests"
		return s.cache.DeleteKeysByPattern(context.Background(), pattern)
	}
	return nil
}
