package invitations

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"fmt"
	"log/slog"
	"time"

	"github.com/gofrs/uuid"
)

type InvitationWithGuest struct {
	Invitation  models.Invitation `json:"invitation"`
	Guest       models.Guest      `json:"guest"`
	PrettyToken string            `json:"pretty_token,omitempty"`
}

// _invitationSvc is the package-level singleton set by server.go.
var _invitationSvc *InvitationService

// SetDefaultInvitationService wires the package-level functions to the DI instance.
func SetDefaultInvitationService(svc *InvitationService) { _invitationSvc = svc }

func GetInvitationByToken(token string) (*InvitationWithGuest, error) {
	return _invitationSvc.GetInvitationByToken(token)
}
func ConfirmRSVP(prettyToken string, status string, method string, guestCount int) (*models.Guest, error) {
	return _invitationSvc.ConfirmRSVP(prettyToken, status, method, guestCount)
}
func ListInvitations() ([]models.Invitation, error)   { return _invitationSvc.ListInvitations() }
func CreateInvitation(obj *models.Invitation) error   { return _invitationSvc.CreateInvitation(obj) }
func UpdateInvitation(obj *models.Invitation) error   { return _invitationSvc.UpdateInvitation(obj) }
func DeleteInvitation(id uuid.UUID) error             { return _invitationSvc.DeleteInvitation(id) }
func ListInvitationsByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return _invitationSvc.ListInvitationsByEventID(eventID)
}

// InvitationService is the injectable, struct-based invitation service.
type InvitationService struct {
	repo      ports.InvitationRepository
	guestRepo ports.GuestRepository
	tokenRepo ports.AccessTokenRepository
	logRepo   ports.InvitationLogRepository
	cache     ports.CacheRepository
}

func NewInvitationService(
	repo ports.InvitationRepository,
	guestRepo ports.GuestRepository,
	tokenRepo ports.AccessTokenRepository,
	logRepo ports.InvitationLogRepository,
	cache ports.CacheRepository,
) *InvitationService {
	return &InvitationService{
		repo:      repo,
		guestRepo: guestRepo,
		tokenRepo: tokenRepo,
		logRepo:   logRepo,
		cache:     cache,
	}
}

func (s *InvitationService) GetInvitationByToken(token string) (*InvitationWithGuest, error) {
	accessToken, err := s.tokenRepo.GetByToken(token)
	if err != nil {
		return nil, err
	}
	if accessToken == nil {
		return nil, fmt.Errorf("access token not found")
	}
	if accessToken.ExpiresAt != nil && time.Now().After(*accessToken.ExpiresAt) {
		return nil, fmt.Errorf("token expired")
	}
	invitation, err := s.repo.GetInvitationByID(accessToken.InvitationID)
	if err != nil {
		return nil, err
	}
	guest, err := s.guestRepo.GetGuestByInvitationID(accessToken.InvitationID)
	if err != nil {
		return nil, err
	}
	logErr := s.logRepo.CreateInvitationLog(&models.InvitationLog{
		InvitationID: invitation.ID,
		Channel:      "token",
		Action:       "accessed",
		Status:       "success",
		Response:     "Invitation accessed via token",
		Timestamp:    time.Now(),
		CreatedAt:    time.Now(),
	})
	if logErr != nil {
		slog.Warn("failed to create invitation log", "error", logErr)
	}
	return &InvitationWithGuest{
		Invitation:  *invitation,
		Guest:       *guest,
		PrettyToken: accessToken.PrettyToken,
	}, nil
}

func (s *InvitationService) ConfirmRSVP(prettyToken string, status string, method string, guestCount int) (*models.Guest, error) {
	accessToken, err := s.tokenRepo.GetByPrettyToken(prettyToken)
	if err != nil || accessToken == nil {
		return nil, fmt.Errorf("invalid or expired token")
	}
	invitation, err := s.repo.GetInvitationByIDLite(accessToken.InvitationID)
	if err != nil || invitation == nil {
		return nil, fmt.Errorf("invitation not found for token")
	}
	if guestCount > invitation.MaxGuests {
		return nil, fmt.Errorf("guest count (%d) exceeds allowed max (%d)", guestCount, invitation.MaxGuests)
	}
	guest, err := s.guestRepo.GetGuestByInvitationID(accessToken.InvitationID)
	if err != nil || guest == nil {
		return nil, fmt.Errorf("guest not found for token")
	}
	now := time.Now()
	guest.RSVPStatus = status
	guest.RSVPAt = &now
	guest.RSVPMethod = method
	guest.RSVPTokenID = &accessToken.ID
	guest.RSVPGuestCount = guestCount
	if err := s.guestRepo.UpdateGuest(guest); err != nil {
		return nil, err
	}
	_ = s.logRepo.CreateInvitationLog(&models.InvitationLog{
		InvitationID: accessToken.InvitationID,
		Channel:      "rsvp",
		Action:       "confirmed",
		Status:       status,
		Response: fmt.Sprintf(
			"RSVP confirmed via %s with pretty_token %s, guest_count=%d",
			method, prettyToken, guestCount,
		),
		Timestamp: now,
		CreatedAt: now,
	})
	return guest, nil
}

func (s *InvitationService) ListInvitations() ([]models.Invitation, error) {
	cacheKey := "all:invitations"
	ctx := context.Background()
	cached, err := s.cache.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.Invitation
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}
	data, err := s.repo.ListInvitations()
	if err != nil {
		return nil, err
	}
	jsonStr, _ := json.Marshal(data)
	_ = s.cache.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["invitations"])
	return data, nil
}

func (s *InvitationService) CreateInvitation(obj *models.Invitation) error {
	if err := s.repo.CreateInvitation(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("invitations", "all")
}

func (s *InvitationService) UpdateInvitation(obj *models.Invitation) error {
	if err := s.repo.UpdateInvitation(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("invitations", "all")
}

func (s *InvitationService) DeleteInvitation(id uuid.UUID) error {
	if err := s.repo.DeleteInvitation(id); err != nil {
		return err
	}
	return s.cache.Invalidate("invitations", "all")
}

func (s *InvitationService) ListInvitationsByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return s.repo.ListByEventID(eventID)
}
