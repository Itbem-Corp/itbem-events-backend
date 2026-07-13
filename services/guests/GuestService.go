package guests

import (
	"context"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/cacheutil"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

type rsvpTokenWriter interface {
	GetByInvitationID(invitationID uuid.UUID) (*models.InvitationAccessToken, error)
	CreateInvitationAccessToken(token *models.InvitationAccessToken) error
	UpdateInvitationAccessToken(token *models.InvitationAccessToken) error
}

type analyticsGuestRepository interface {
	ListAnalyticsGuestsByEventID(eventID uuid.UUID) ([]dtos.AnalyticsGuest, error)
}

type seatingGuestRepository interface {
	ListSeatingGuestsByEventID(eventID uuid.UUID) ([]dtos.SeatingGuest, error)
}

type checkinGuestRepository interface {
	ListCheckinGuests(eventID uuid.UUID, query dtos.CheckinGuestsListQuery) ([]models.Guest, int64, error)
}

type guestShareSummaryRepository interface {
	GetGuestShareSummaryByEventID(eventID uuid.UUID) (dtos.GuestShareSummary, error)
}

type guestDashboardSummariesRepository interface {
	GetGuestDashboardSummariesByEventID(eventID uuid.UUID) (dtos.GuestSummary, dtos.GuestShareSummary, error)
}

// _guestSvc is the package-level singleton set by internal/app.
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
func GetGuestByID(id uuid.UUID) (*models.Guest, error) {
	return _guestSvc.GetGuestByID(id)
}
func EnsureRSVPToken(id uuid.UUID) (*models.Guest, error) {
	return _guestSvc.EnsureRSVPToken(id)
}
func ListGuestsByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return _guestSvc.ListGuestsByEventID(eventID)
}
func ListCheckinGuests(eventID uuid.UUID, query dtos.CheckinGuestsListQuery) ([]models.Guest, int64, error) {
	return _guestSvc.ListCheckinGuests(eventID, query)
}
func GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error) {
	return _guestSvc.GetGuestSummaryByEventID(eventID)
}
func ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return _guestSvc.ListAttendeesByEventID(eventID)
}

// GuestService is the injectable, struct-based guest service.
type GuestService struct {
	repo        ports.GuestRepository
	invitations ports.InvitationRepository
	accessToken ports.AccessTokenRepository
	cache       ports.CacheRepository
	tx          ports.Transactor
}

func NewGuestService(
	repo ports.GuestRepository,
	accessToken ports.AccessTokenRepository,
	cache ports.CacheRepository,
	tx ports.Transactor,
	invitationRepo ...ports.InvitationRepository,
) *GuestService {
	var invitations ports.InvitationRepository
	if len(invitationRepo) > 0 {
		invitations = invitationRepo[0]
	}
	return &GuestService{repo: repo, invitations: invitations, accessToken: accessToken, cache: cache, tx: tx}
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
		if IsPublicHostRole(guests[i].Role) {
			guests[i].IsHost = true
		}
		if guests[i].GuestsCount <= 0 && guests[i].RSVPGuestCount > 0 {
			guests[i].GuestsCount = guests[i].RSVPGuestCount
		}
		if guests[i].MaxGuests <= 0 {
			if guests[i].GuestsCount > 0 {
				guests[i].MaxGuests = guests[i].GuestsCount
			} else {
				guests[i].MaxGuests = 1
			}
		}
		if guests[i].RSVPGuestCount <= 0 && guests[i].GuestsCount > 0 {
			guests[i].RSVPGuestCount = guests[i].GuestsCount
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
	for i, inv := range invitations {
		var pretty string
		for {
			tmp, err := s.accessToken.GeneratePrettyToken(inv.EventID, 8)
			if err != nil {
				return err
			}
			if _, exists := usedPretty[tmp]; !exists {
				usedPretty[tmp] = struct{}{}
				pretty = tmp
				break
			}
		}
		guests[i].PrettyToken = pretty
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

	return s.invalidateGuestsCacheForEvent(guests[0].EventID)
}

func (s *GuestService) CreateGuest(obj *models.Guest) error {
	if obj.GuestStatusID == uuid.Nil {
		obj.GuestStatusID = s.repo.GetPendingStatusID()
	}
	if IsPublicHostRole(obj.Role) {
		obj.IsHost = true
	}
	if err := s.repo.CreateGuest(obj); err != nil {
		return err
	}
	return s.invalidateGuestsCacheForEvent(obj.EventID)
}

func (s *GuestService) UpdateGuest(obj *models.Guest) error {
	oldGuest, _ := s.repo.GetGuestByID(obj.ID)
	previousRSVPStatus := ""
	if oldGuest != nil {
		previousRSVPStatus = normalizeRSVPStatus(oldGuest.RSVPStatus)
	}
	if err := s.updateInvitationMaxGuests(obj, oldGuest); err != nil {
		return err
	}
	if IsPublicHostRole(obj.Role) {
		obj.IsHost = true
	}
	s.syncRSVPMetadata(obj, oldGuest)
	if err := s.repo.UpdateGuest(obj); err != nil {
		return err
	}
	trackRSVPAnalyticsTransition(rsvpAnalyticsEventID(obj, oldGuest), previousRSVPStatus, obj.RSVPStatus)
	if obj.ID != uuid.Nil && s.cache != nil {
		_ = s.cache.Invalidate("guests", obj.ID.String())
	}
	if oldGuest != nil && oldGuest.EventID != obj.EventID && oldGuest.EventID != uuid.Nil {
		_ = s.invalidateGuestsCacheForEvent(oldGuest.EventID)
	}
	_ = s.invalidateGuestsCacheForEvent(obj.EventID)
	return nil
}

func (s *GuestService) GetGuestByID(id uuid.UUID) (*models.Guest, error) {
	return s.repo.GetGuestByID(id)
}

func (s *GuestService) EnsureRSVPToken(id uuid.UUID) (*models.Guest, error) {
	guest, err := s.repo.GetGuestByID(id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(guest.PrettyToken) != "" {
		return guest, nil
	}
	if guest.InvitationID == nil || *guest.InvitationID == uuid.Nil {
		if s.invitations == nil {
			return nil, errors.New("invitation repository unavailable")
		}

		invitationID := uuid.Must(uuid.NewV4())
		maxGuests := guest.MaxGuests
		if maxGuests <= 0 {
			maxGuests = guest.GuestsCount
		}
		if maxGuests <= 0 {
			maxGuests = guest.RSVPGuestCount
		}
		if maxGuests <= 0 {
			maxGuests = 1
		}
		now := time.Now()
		invitation := &models.Invitation{
			ID:          invitationID,
			EventID:     guest.EventID,
			Type:        "default",
			SubType:     "general",
			EnableEmail: true,
			MaxGuests:   maxGuests,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.invitations.CreateInvitation(invitation); err != nil {
			return nil, err
		}

		guest.InvitationID = &invitationID
		if err := s.repo.UpdateGuest(guest); err != nil {
			_ = s.invitations.DeleteInvitation(invitationID)
			return nil, err
		}
	}

	writer, ok := s.accessToken.(rsvpTokenWriter)
	if !ok {
		return nil, errors.New("RSVP token writer unavailable")
	}

	token, tokenErr := writer.GetByInvitationID(*guest.InvitationID)
	if tokenErr != nil && !errors.Is(tokenErr, gorm.ErrRecordNotFound) {
		return nil, tokenErr
	}

	isNew := token == nil || errors.Is(tokenErr, gorm.ErrRecordNotFound)
	if isNew {
		token = &models.InvitationAccessToken{
			InvitationID: *guest.InvitationID,
			CreatedAt:    time.Now(),
		}
	}
	if strings.TrimSpace(token.PrettyToken) == "" {
		token.PrettyToken, err = s.accessToken.GeneratePrettyToken(guest.EventID, 8)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(token.Token) == "" {
		token.Token = uuid.Must(uuid.NewV4()).String()
	}
	token.UpdatedAt = time.Now()

	if isNew {
		err = writer.CreateInvitationAccessToken(token)
	} else {
		err = writer.UpdateInvitationAccessToken(token)
	}
	if err != nil {
		return nil, err
	}

	guest.PrettyToken = token.PrettyToken
	_ = s.invalidateGuestsCacheForEvent(guest.EventID)
	return guest, nil
}

func (s *GuestService) guestEventCacheKey(eventID uuid.UUID) string {
	return "all:" + eventID.String() + ":guests"
}

func (s *GuestService) invalidateGuestsCacheForEvent(eventID uuid.UUID) error {
	if s.cache == nil || eventID == uuid.Nil {
		return nil
	}
	_ = s.cache.DeleteKeysByPattern(context.Background(), s.guestEventCacheKey(eventID))
	return nil
}

func (s *GuestService) invalidateGuestsCacheForEvents(eventIDs map[uuid.UUID]struct{}) error {
	for eventID := range eventIDs {
		_ = s.invalidateGuestsCacheForEvent(eventID)
	}
	return nil
}

func (s *GuestService) syncRSVPMetadata(obj *models.Guest, oldGuest *models.Guest) {
	nextStatus := normalizeRSVPStatus(obj.RSVPStatus)
	if nextStatus == "" {
		obj.RSVPStatus = ""
		return
	}

	obj.RSVPStatus = nextStatus
	obj.RSVPMethod = strings.ToLower(strings.TrimSpace(obj.RSVPMethod))

	previousStatus := ""
	if oldGuest != nil {
		previousStatus = normalizeRSVPStatus(oldGuest.RSVPStatus)
	}

	statusChanged := oldGuest != nil && previousStatus != nextStatus
	statusFirstSet := oldGuest == nil && obj.RSVPAt == nil
	if !statusChanged && !statusFirstSet {
		return
	}

	now := time.Now()
	obj.RSVPAt = &now
	if obj.RSVPMethod == "" {
		obj.RSVPMethod = "host"
	}
}

func normalizeRSVPStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "confirmed":
		return "confirmed"
	case "declined":
		return "declined"
	case "pending":
		return "pending"
	default:
		return strings.TrimSpace(status)
	}
}

func rsvpAnalyticsEventID(obj *models.Guest, oldGuest *models.Guest) uuid.UUID {
	if obj != nil && obj.EventID != uuid.Nil {
		return obj.EventID
	}
	if oldGuest != nil {
		return oldGuest.EventID
	}
	return uuid.Nil
}

func trackRSVPAnalyticsTransition(eventID uuid.UUID, previousStatus, nextStatus string) {
	if eventID == uuid.Nil {
		return
	}
	previousField := rsvpAnalyticsField(previousStatus)
	nextField := rsvpAnalyticsField(nextStatus)
	if previousField == nextField {
		return
	}
	if previousField != "" {
		eventsService.AdjustAnalytics(eventID, previousField, -1)
	}
	if nextField != "" {
		eventsService.AdjustAnalytics(eventID, nextField, 1)
	}
}

func rsvpAnalyticsField(status string) string {
	switch normalizeRSVPStatus(status) {
	case "confirmed":
		return "rsvp_confirmed"
	case "declined":
		return "rsvp_declined"
	default:
		return ""
	}
}

func (s *GuestService) updateInvitationMaxGuests(obj *models.Guest, oldGuest *models.Guest) error {
	if s.invitations == nil || obj.MaxGuests <= 0 {
		return nil
	}

	invitationID := obj.InvitationID
	if invitationID == nil && oldGuest != nil {
		invitationID = oldGuest.InvitationID
	}
	if invitationID == nil || *invitationID == uuid.Nil {
		return nil
	}

	invitation, err := s.invitations.GetInvitationByIDLite(*invitationID)
	if err != nil {
		return err
	}
	if invitation == nil {
		return nil
	}
	if invitation.MaxGuests == obj.MaxGuests {
		return nil
	}

	invitation.MaxGuests = obj.MaxGuests
	return s.invitations.UpdateInvitation(invitation)
}

func (s *GuestService) DeleteGuest(id uuid.UUID) error {
	guest, err := s.repo.GetGuestByID(id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteGuest(id); err != nil {
		return err
	}
	return s.invalidateGuestsCacheForEvent(guest.EventID)
}

func (s *GuestService) BulkDeleteGuests(ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	eventIDs := make(map[uuid.UUID]struct{})
	for _, id := range ids {
		guest, err := s.repo.GetGuestByID(id)
		if err != nil {
			return err
		}
		if guest != nil && guest.EventID != uuid.Nil {
			eventIDs[guest.EventID] = struct{}{}
		}
	}

	if err := s.repo.BulkDeleteGuests(ids); err != nil {
		return err
	}
	return s.invalidateGuestsCacheForEvents(eventIDs)
}

type bulkGuestStatusRepository interface {
	BulkUpdateGuestStatus(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error
}

func (s *GuestService) BulkUpdateGuestStatus(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error {
	if len(ids) == 0 {
		return nil
	}
	repo, ok := s.repo.(bulkGuestStatusRepository)
	if !ok {
		return fmt.Errorf("bulk guest status repository unavailable")
	}
	if err := repo.BulkUpdateGuestStatus(eventID, ids, statusID, rsvpStatus, rsvpMethod); err != nil {
		return err
	}
	return s.invalidateGuestsCacheForEvent(eventID)
}

func (s *GuestService) ListGuestsByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+eventID.String()+":guests",
		utils.CacheTTLs[utils.RedisGuestsKey],
		func() ([]models.Guest, error) {
			return s.repo.ListGuestsByEventID(eventID)
		},
	)
}

func (s *GuestService) ListCheckinGuests(eventID uuid.UUID, query dtos.CheckinGuestsListQuery) ([]models.Guest, int64, error) {
	repo, ok := s.repo.(checkinGuestRepository)
	if !ok {
		return nil, 0, fmt.Errorf("check-in guest repository unavailable")
	}
	return repo.ListCheckinGuests(eventID, query)
}

func (s *GuestService) ListAnalyticsGuestsByEventID(eventID uuid.UUID) ([]dtos.AnalyticsGuest, error) {
	repo, ok := s.repo.(analyticsGuestRepository)
	if !ok {
		return nil, errors.New("analytics guest projection unavailable")
	}
	return repo.ListAnalyticsGuestsByEventID(eventID)
}

func (s *GuestService) ListSeatingGuestsByEventID(eventID uuid.UUID) ([]dtos.SeatingGuest, error) {
	repo, ok := s.repo.(seatingGuestRepository)
	if !ok {
		return nil, errors.New("seating guest projection unavailable")
	}
	return repo.ListSeatingGuestsByEventID(eventID)
}

func (s *GuestService) GetGuestShareSummaryByEventID(eventID uuid.UUID) (dtos.GuestShareSummary, error) {
	repo, ok := s.repo.(guestShareSummaryRepository)
	if !ok {
		return dtos.GuestShareSummary{}, errors.New("guest share summary unavailable")
	}
	return repo.GetGuestShareSummaryByEventID(eventID)
}

func (s *GuestService) GetGuestDashboardSummariesByEventID(eventID uuid.UUID) (dtos.GuestSummary, dtos.GuestShareSummary, error) {
	if repo, ok := s.repo.(guestDashboardSummariesRepository); ok {
		return repo.GetGuestDashboardSummariesByEventID(eventID)
	}

	var guestSummary dtos.GuestSummary
	var shareSummary dtos.GuestShareSummary
	group := new(errgroup.Group)
	group.Go(func() error {
		var err error
		guestSummary, err = s.repo.GetGuestSummaryByEventID(eventID)
		return err
	})
	group.Go(func() error {
		var err error
		shareSummary, err = s.GetGuestShareSummaryByEventID(eventID)
		return err
	})
	if err := group.Wait(); err != nil {
		return dtos.GuestSummary{}, dtos.GuestShareSummary{}, err
	}
	return guestSummary, shareSummary, nil
}

func (s *GuestService) GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error) {
	return s.repo.GetGuestSummaryByEventID(eventID)
}

func (s *GuestService) ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return s.repo.ListAttendeesByEventID(eventID)
}

func (s *GuestService) CreateGuests(objs []models.Guest) error {
	if len(objs) == 0 {
		return nil
	}
	for i := range objs {
		if objs[i].GuestStatusID == uuid.Nil {
			objs[i].GuestStatusID = s.repo.GetPendingStatusID()
		}
		if IsPublicHostRole(objs[i].Role) {
			objs[i].IsHost = true
		}
	}
	if err := s.repo.CreateGuests(objs); err != nil {
		return err
	}
	return s.invalidateGuestsCacheForEvent(objs[0].EventID)
}
