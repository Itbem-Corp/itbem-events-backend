package invitations

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/accesstoken"
	"events-stocks/internal/previewtoken"
	"events-stocks/internal/publicaccessproof"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type InvitationWithGuest = dtos.InvitationLookup

type RSVPConfirmationResult struct {
	Guest       *models.Guest
	PrettyToken string
}

type EventCoverViewURLFunc func(path, bucket string) (string, *time.Time)

var (
	ErrInvitationEventUnavailable  = errors.New("invitation event is unavailable")
	ErrInvitationEventAccessFailed = errors.New("invitation event access check failed")
	// ErrInvitationEventGone: the invitation's event was deleted (soft
	// delete hides it from reads). A guest with an old link gets 410, not a
	// 500 "access check failed" (EVT-9).
	ErrInvitationEventGone = errors.New("invitation event no longer exists")
	ErrInvalidRSVPRequest  = errors.New("invalid RSVP request")
	// ErrInvalidRSVPToken covers an unknown or expired token and a token whose
	// invitation or guest is gone: the caller answers 401.
	ErrInvalidRSVPToken = errors.New("invalid or expired token")
	// ErrInvitationPasswordRequired: the event has a password and the request
	// carries no valid verify-access proof (X-Event-Access-Token).
	ErrInvitationPasswordRequired = errors.New("event password required")
	// ErrRSVPClosed: the RSVP block is hidden or rsvp_deadline has passed.
	ErrRSVPClosed = errors.New("rsvp closed")
)

// PublicAccess carries the caller's public credentials besides the invitation
// token: the verify-access proof and an optional Studio preview token. A valid
// preview token bypasses only the password proof here; the active flag, the
// access window and the RSVP deadline still apply.
type PublicAccess struct {
	PasswordProof string
	PreviewToken  string
}

// _invitationSvc is the package-level singleton set by internal/app.
var _invitationSvc *InvitationService

// SetDefaultInvitationService wires the package-level functions to the DI instance.
func SetDefaultInvitationService(svc *InvitationService) { _invitationSvc = svc }

func GetInvitationByToken(token string) (*InvitationWithGuest, error) {
	return _invitationSvc.GetInvitationByToken(token)
}
func ConfirmRSVP(prettyToken string, status string, method string, guestCount int, dietaryRestrictions string, rsvpNotes ...string) (*models.Guest, error) {
	return _invitationSvc.ConfirmRSVP(prettyToken, status, method, guestCount, dietaryRestrictions, rsvpNotes...)
}
func ListInvitations() ([]models.Invitation, error) { return _invitationSvc.ListInvitations() }
func CreateInvitation(obj *models.Invitation) error { return _invitationSvc.CreateInvitation(obj) }
func UpdateInvitation(obj *models.Invitation) error { return _invitationSvc.UpdateInvitation(obj) }
func DeleteInvitation(id uuid.UUID) error           { return _invitationSvc.DeleteInvitation(id) }
func ListInvitationsByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return _invitationSvc.ListInvitationsByEventID(eventID)
}
func ResendInvitation(invitationID uuid.UUID) error {
	return _invitationSvc.ResendInvitation(invitationID)
}

// InvitationService is the injectable, struct-based invitation service.
type InvitationService struct {
	repo         ports.InvitationRepository
	guestRepo    ports.GuestRepository
	tokenRepo    ports.AccessTokenRepository
	logRepo      ports.InvitationLogRepository
	cache        ports.CacheRepository
	eventsRepo   ports.EventsRepository
	configRepo   ports.EventConfigRepository
	coverViewURL EventCoverViewURLFunc
	now          func() time.Time
}

type InvitationServiceDeps struct {
	Repo         ports.InvitationRepository
	GuestRepo    ports.GuestRepository
	TokenRepo    ports.AccessTokenRepository
	LogRepo      ports.InvitationLogRepository
	Cache        ports.CacheRepository
	EventsRepo   ports.EventsRepository
	ConfigRepo   ports.EventConfigRepository
	CoverViewURL EventCoverViewURLFunc
	Now          func() time.Time
}

func NewInvitationService(
	repo ports.InvitationRepository,
	guestRepo ports.GuestRepository,
	tokenRepo ports.AccessTokenRepository,
	logRepo ports.InvitationLogRepository,
	cache ports.CacheRepository,
) *InvitationService {
	return NewInvitationServiceWithDeps(InvitationServiceDeps{
		Repo:      repo,
		GuestRepo: guestRepo,
		TokenRepo: tokenRepo,
		LogRepo:   logRepo,
		Cache:     cache,
	})
}

func NewInvitationServiceWithDeps(deps InvitationServiceDeps) *InvitationService {
	return &InvitationService{
		repo:         deps.Repo,
		guestRepo:    deps.GuestRepo,
		tokenRepo:    deps.TokenRepo,
		logRepo:      deps.LogRepo,
		cache:        deps.Cache,
		eventsRepo:   deps.EventsRepo,
		configRepo:   deps.ConfigRepo,
		coverViewURL: deps.CoverViewURL,
		now:          deps.Now,
	}
}

func (s *InvitationService) GetInvitationByToken(token string) (*InvitationWithGuest, error) {
	return s.GetInvitationByTokenWithAccess(token, PublicAccess{})
}

// GetInvitationByTokenWithAccess is the public ByToken read: the same gates as
// the event page (active, access window, password proof) apply.
func (s *InvitationService) GetInvitationByTokenWithAccess(token string, access PublicAccess) (*InvitationWithGuest, error) {
	now := s.invitationEventNow()
	accessToken, err := accesstoken.Lookup(s.tokenRepo, token)
	if err != nil {
		return nil, err
	}
	if accessToken == nil {
		return nil, fmt.Errorf("access token not found")
	}
	if isExpiredAccessToken(accessToken, now) {
		return nil, fmt.Errorf("token expired")
	}
	invitation, err := s.repo.GetInvitationByID(accessToken.InvitationID)
	if err != nil {
		return nil, err
	}
	if invitation == nil {
		return nil, fmt.Errorf("invitation not found for token")
	}
	cfg, err := s.checkInvitationEventAccess(invitation, access)
	if err != nil {
		return nil, err
	}
	guest, err := s.guestRepo.GetGuestByInvitationID(accessToken.InvitationID)
	if err != nil {
		return nil, err
	}
	if s.logRepo != nil {
		logErr := s.logRepo.CreateInvitationLog(&models.InvitationLog{
			InvitationID: invitation.ID,
			Channel:      "token",
			Action:       "accessed",
			Status:       "success",
			Response:     "Invitation accessed via token",
			Timestamp:    now,
			CreatedAt:    now,
		})
		if logErr != nil {
			slog.Warn("failed to create invitation log", "error", logErr)
		}
	}
	responseToken := strings.TrimSpace(accessToken.PrettyToken)
	if responseToken == "" {
		responseToken = strings.TrimSpace(token)
	}
	result := s.buildInvitationLookup(invitation, guest, responseToken)
	if result.Event != nil {
		result.Event.RSVPDeadline = eventsService.RSVPDeadline(cfg)
		result.Event.RSVPOpen = eventsService.RSVPOpen(cfg, now)
	}
	return result, nil
}

func (s *InvitationService) buildInvitationLookup(invitation *models.Invitation, guest *models.Guest, prettyToken string) *dtos.InvitationLookup {
	var event *dtos.EventMeta
	if invitation != nil && invitation.Event.ID != uuid.Nil {
		event = s.invitationEventMeta(invitation.Event)
	} else if guest != nil && guest.Event.ID != uuid.Nil {
		event = s.invitationEventMeta(guest.Event)
	}

	result := &dtos.InvitationLookup{
		PrettyToken: prettyToken,
		Event:       event,
	}

	if invitation != nil {
		result.Invitation = dtos.InvitationLookupInvitation{
			ID:        invitation.ID,
			EventID:   invitation.EventID,
			MaxGuests: invitation.MaxGuests,
			Event:     event,
		}
	}

	if guest != nil {
		result.Guest = dtos.InvitationLookupGuest{
			ID:                  guest.ID,
			EventID:             guest.EventID,
			InvitationID:        guest.InvitationID,
			FirstName:           guest.FirstName,
			LastName:            guest.LastName,
			RSVPStatus:          guest.RSVPStatus,
			RSVPAt:              guest.RSVPAt,
			RSVPMethod:          guest.RSVPMethod,
			RSVPGuestCount:      guest.RSVPGuestCount,
			DietaryRestrictions: guest.DietaryRestrictions,
			RSVPNotes:           guest.RSVPNotes,
		}
	}

	return result
}

func (s *InvitationService) invitationEventMeta(event models.Event) *dtos.EventMeta {
	eventType := ""
	if event.EventType.Name != "" {
		eventType = event.EventType.Name
	}
	var eventDateTime *time.Time
	if !event.EventDateTime.IsZero() {
		eventDateTime = &event.EventDateTime
	}
	coverViewURL := strings.TrimSpace(event.CoverImageURL)
	var coverViewURLExpiresAt *time.Time
	if s != nil && s.coverViewURL != nil {
		if resolved, expiresAt := s.coverViewURL(event.CoverImageURL, event.MediaBucket); strings.TrimSpace(resolved) != "" {
			coverViewURL = resolved
			coverViewURLExpiresAt = expiresAt
		}
	}

	return &dtos.EventMeta{
		Name:                  event.Name,
		Identifier:            event.Identifier,
		Description:           event.Description,
		CoverImageURL:         event.CoverImageURL,
		CoverViewURL:          coverViewURL,
		CoverViewURLExpiresAt: coverViewURLExpiresAt,
		ViewURL:               coverViewURL,
		ViewURLExpiresAt:      coverViewURLExpiresAt,
		EventDateTime:         eventDateTime,
		Address:               event.Address,
		SecondAddress:         event.SecondAddress,
		Timezone:              event.Timezone,
		Language:              event.Language,
		OrganizerName:         event.OrganizerName,
		EventType:             eventType,
	}
}

func (s *InvitationService) ConfirmRSVP(prettyToken string, status string, method string, guestCount int, dietaryRestrictions string, rsvpNotes ...string) (*models.Guest, error) {
	result, err := s.ConfirmRSVPWithResult(prettyToken, status, method, guestCount, dietaryRestrictions, rsvpNotes...)
	if err != nil || result == nil {
		return nil, err
	}
	return result.Guest, nil
}

func (s *InvitationService) ConfirmRSVPWithResult(prettyToken string, status string, method string, guestCount int, dietaryRestrictions string, rsvpNotes ...string) (*RSVPConfirmationResult, error) {
	return s.ConfirmRSVPWithAccess(PublicAccess{}, prettyToken, status, method, guestCount, dietaryRestrictions, rsvpNotes...)
}

// ConfirmRSVPWithAccess is the public RSVP submit: event gates (active, window,
// password proof) first, then the RSVP availability (block shown, deadline).
func (s *InvitationService) ConfirmRSVPWithAccess(access PublicAccess, prettyToken string, status string, method string, guestCount int, dietaryRestrictions string, rsvpNotes ...string) (*RSVPConfirmationResult, error) {
	status = normalizeRSVPStatus(status)
	method = normalizeRSVPMethod(method)
	now := s.invitationEventNow()
	if !isValidRSVPStatus(status) {
		return nil, fmt.Errorf("%w: invalid RSVP status", ErrInvalidRSVPRequest)
	}
	// Bound the free text before any lookup: dietary_restrictions is
	// varchar(255), and an over-long value must be a 400, never a failed write.
	if err := checkRSVPTextLengths(dietaryRestrictions, rsvpNotes...); err != nil {
		return nil, err
	}
	accessToken, err := accesstoken.Lookup(s.tokenRepo, prettyToken)
	if err != nil || accessToken == nil {
		return nil, ErrInvalidRSVPToken
	}
	if isExpiredAccessToken(accessToken, now) {
		return nil, ErrInvalidRSVPToken
	}
	invitation, err := s.repo.GetInvitationByIDLite(accessToken.InvitationID)
	if err != nil || invitation == nil {
		return nil, fmt.Errorf("%w: invitation not found for token", ErrInvalidRSVPToken)
	}
	cfg, err := s.checkInvitationEventAccess(invitation, access)
	if err != nil {
		return nil, err
	}
	if !eventsService.RSVPOpen(cfg, now) {
		return nil, ErrRSVPClosed
	}
	if guestCount < 0 {
		return nil, fmt.Errorf("%w: guest count cannot be negative", ErrInvalidRSVPRequest)
	}
	if status == "confirmed" && guestCount < 1 {
		return nil, fmt.Errorf("%w: guest count must be at least 1 when RSVP is confirmed", ErrInvalidRSVPRequest)
	}
	maxGuests := effectiveInvitationMaxGuests(invitation.MaxGuests)
	if guestCount > maxGuests {
		return nil, fmt.Errorf("%w: guest count (%d) exceeds allowed max (%d)", ErrInvalidRSVPRequest, guestCount, maxGuests)
	}
	guest, err := s.guestRepo.GetGuestByInvitationID(accessToken.InvitationID)
	if err != nil || guest == nil {
		return nil, fmt.Errorf("%w: guest not found for token", ErrInvalidRSVPToken)
	}
	if guest.EventID == uuid.Nil && invitation.EventID != uuid.Nil {
		guest.EventID = invitation.EventID
	}
	previousStatus := normalizeRSVPStatus(guest.RSVPStatus)
	analyticsEventID := rsvpAnalyticsEventID(invitation, guest)
	guest.RSVPStatus = status
	// GST-62: the guest's status follows the RSVP like a host change does,
	// so exports, check-in filters and metrics that read guest_status_id do
	// not keep showing "pending" after a web RSVP.
	syncGuestStatusWithRSVP(s.guestRepo, guest, status)
	guest.RSVPAt = &now
	guest.RSVPMethod = method
	guest.RSVPTokenID = &accessToken.ID
	guest.RSVPGuestCount = guestCount
	guest.DietaryRestrictions = strings.TrimSpace(dietaryRestrictions)
	if len(rsvpNotes) > 0 {
		guest.RSVPNotes = strings.TrimSpace(rsvpNotes[0])
	}
	// The guest's own answer replaces a door check-in, so an undo at the door
	// can no longer restore the pre-check-in RSVP over it.
	guest.ClearDoorCheckin()
	if err := s.guestRepo.UpdateGuest(guest); err != nil {
		return nil, err
	}
	s.invalidateGuestCache(guest)
	trackRSVPAnalyticsTransition(analyticsEventID, previousStatus, status)
	if s.logRepo != nil {
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
	}
	return &RSVPConfirmationResult{
		Guest:       guest,
		PrettyToken: canonicalRSVPPrettyToken(accessToken, prettyToken),
	}, nil
}

type guestStatusCatalog interface {
	GuestStatusIDByCode(code string) (uuid.UUID, error)
}

// syncGuestStatusWithRSVP moves both status columns to the catalog status of
// rsvpStatus. Repositories without the catalog leave them untouched.
func syncGuestStatusWithRSVP(repo ports.GuestRepository, guest *models.Guest, rsvpStatus string) {
	catalog, ok := repo.(guestStatusCatalog)
	if !ok || guest == nil {
		return
	}
	id, err := catalog.GuestStatusIDByCode(rsvpStatus)
	if err != nil || id == uuid.Nil {
		return
	}
	guest.GuestStatusID = id
	guest.StatusID = id
}

// RSVPTextTooLongError is an RSVP whose free text is over its column limit.
// It wraps ErrInvalidRSVPRequest; Message is the Spanish text for the guest.
type RSVPTextTooLongError struct {
	Field *models.GuestFieldTooLongError
}

func (e *RSVPTextTooLongError) Error() string {
	return ErrInvalidRSVPRequest.Error() + ": " + e.Field.Error()
}

func (e *RSVPTextTooLongError) Unwrap() error { return ErrInvalidRSVPRequest }

func (e *RSVPTextTooLongError) Message() string { return e.Field.Message() }

func checkRSVPTextLengths(dietaryRestrictions string, rsvpNotes ...string) error {
	checks := []error{
		models.CheckTextLength("dietary_restrictions", "Restricciones alimentarias", dietaryRestrictions, models.GuestDietaryRestrictionsMaxChars),
	}
	if len(rsvpNotes) > 0 {
		checks = append(checks, models.CheckTextLength("rsvp_notes", "Notas", rsvpNotes[0], models.GuestRSVPNotesMaxChars))
	}
	for _, err := range checks {
		var tooLong *models.GuestFieldTooLongError
		if errors.As(err, &tooLong) {
			return &RSVPTextTooLongError{Field: tooLong}
		}
	}
	return nil
}

func effectiveInvitationMaxGuests(maxGuests int) int {
	if maxGuests > 0 {
		return maxGuests
	}
	return 1
}

func normalizeRSVPStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func canonicalRSVPPrettyToken(accessToken *models.InvitationAccessToken, requestedToken string) string {
	if accessToken != nil {
		if prettyToken := strings.TrimSpace(accessToken.PrettyToken); prettyToken != "" {
			return prettyToken
		}
	}
	return strings.TrimSpace(requestedToken)
}

func isExpiredAccessToken(accessToken *models.InvitationAccessToken, now time.Time) bool {
	return accessToken != nil && accessToken.ExpiresAt != nil && now.After(*accessToken.ExpiresAt)
}

// checkInvitationEventAccess applies the public event gates to a token holder
// and returns the event config (nil when no config repository is wired).
func (s *InvitationService) checkInvitationEventAccess(invitation *models.Invitation, access PublicAccess) (*models.EventConfig, error) {
	if invitation == nil || invitation.EventID == uuid.Nil {
		return nil, nil
	}
	if s.eventsRepo != nil {
		event, err := s.eventsRepo.GetEventByIDRaw(invitation.EventID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvitationEventGone
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvitationEventAccessFailed, err)
		}
		if event == nil || !event.IsActive {
			return nil, ErrInvitationEventUnavailable
		}
	}
	if s.configRepo == nil {
		return nil, nil
	}
	cfg, err := s.configRepo.GetEventConfigByID(invitation.EventID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvitationEventAccessFailed, err)
	}
	if !invitationEventAccessWindowOpen(cfg, s.invitationEventNow()) {
		return nil, ErrInvitationEventUnavailable
	}
	if cfg != nil && cfg.HasAuthPasswordPreview() &&
		!previewtoken.Validate(access.PreviewToken, invitation.EventID) &&
		!publicaccessproof.Validate(access.PasswordProof, invitation.EventID, eventsService.EventConfigAccessVersion(cfg)) {
		return nil, ErrInvitationPasswordRequired
	}
	return cfg, nil
}

func (s *InvitationService) invitationEventNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func invitationEventAccessWindowOpen(cfg *models.EventConfig, now time.Time) bool {
	return eventsService.EventAccessWindowOpen(cfg, now)
}

// normalizeRSVPMethod keeps the rsvp_method an invitation token holder sends
// to the channels a guest can answer through: "web" (what the public site
// sends) and "app" (the other guest channel the dashboard shows). "host" is
// the organizer's own answer and what a door check-in writes, so an anonymous
// caller must not record it; it and any other text are stored as "web", which
// keeps every deployed page working instead of answering 400.
func normalizeRSVPMethod(method string) string {
	switch method = strings.ToLower(strings.TrimSpace(method)); method {
	case "web", "app":
		return method
	default:
		return "web"
	}
}

func isValidRSVPStatus(status string) bool {
	switch status {
	case "confirmed", "declined", "pending":
		return true
	default:
		return false
	}
}

func rsvpAnalyticsEventID(invitation *models.Invitation, guest *models.Guest) uuid.UUID {
	if guest != nil && guest.EventID != uuid.Nil {
		return guest.EventID
	}
	if invitation != nil {
		return invitation.EventID
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

func (s *InvitationService) invalidateGuestCache(guest *models.Guest) {
	if s.cache == nil || guest == nil {
		return
	}
	if guest.ID != uuid.Nil {
		_ = s.cache.Invalidate(utils.RedisGuestsKey, guest.ID.String())
	}
	if guest.EventID != uuid.Nil {
		pattern := "all:" + guest.EventID.String() + ":" + utils.RedisGuestsKey
		_ = s.cache.DeleteKeysByPattern(context.Background(), pattern)
	}
}

func (s *InvitationService) ListInvitations() ([]models.Invitation, error) {
	cacheKey := "all:invitations"
	ctx := context.Background()
	if s.cache != nil {
		cached, err := s.cache.GetKey(ctx, cacheKey)
		if err == nil && cached != "" {
			var result []models.Invitation
			if err := json.Unmarshal([]byte(cached), &result); err == nil {
				return result, nil
			}
		}
	}
	data, err := s.repo.ListInvitations()
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		jsonStr, _ := json.Marshal(data)
		_ = s.cache.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs[utils.RedisInvitationsKey])
	}
	return data, nil
}

func (s *InvitationService) CreateInvitation(obj *models.Invitation) error {
	if err := s.repo.CreateInvitation(obj); err != nil {
		return err
	}
	return s.invalidateInvitationsCache()
}

func (s *InvitationService) UpdateInvitation(obj *models.Invitation) error {
	if err := s.repo.UpdateInvitation(obj); err != nil {
		return err
	}
	return s.invalidateInvitationsCache()
}

func (s *InvitationService) DeleteInvitation(id uuid.UUID) error {
	if err := s.repo.DeleteInvitation(id); err != nil {
		return err
	}
	return s.invalidateInvitationsCache()
}

func (s *InvitationService) invalidateInvitationsCache() error {
	if s.cache == nil {
		return nil
	}
	_ = s.cache.Invalidate(utils.RedisInvitationsKey, "all")
	return nil
}

func (s *InvitationService) ListInvitationsByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return s.repo.ListByEventID(eventID)
}

// ResendInvitation logs the re-send action and marks the invitation as sent.
// Actual message delivery (WhatsApp/email API) is handled client-side.
func (s *InvitationService) ResendInvitation(invitationID uuid.UUID) error {
	inv, err := s.repo.GetInvitationByIDLite(invitationID)
	if err != nil {
		return err
	}
	if inv == nil {
		return fmt.Errorf("invitation not found")
	}

	now := time.Now()
	var logs []models.InvitationLog

	if inv.EnableWhatsApp {
		logs = append(logs, models.InvitationLog{
			InvitationID: invitationID,
			Channel:      "whatsapp",
			Action:       "resent",
			Status:       "success",
			Timestamp:    now,
			CreatedAt:    now,
		})
	}
	if inv.EnableEmail {
		logs = append(logs, models.InvitationLog{
			InvitationID: invitationID,
			Channel:      "email",
			Action:       "resent",
			Status:       "success",
			Timestamp:    now,
			CreatedAt:    now,
		})
	}
	if len(logs) == 0 {
		logs = append(logs, models.InvitationLog{
			InvitationID: invitationID,
			Channel:      "manual",
			Action:       "resent",
			Status:       "success",
			Timestamp:    now,
			CreatedAt:    now,
		})
	}

	if s.logRepo != nil {
		if err := s.logRepo.CreateManyInvitationLogs(logs); err != nil {
			return err
		}
	}

	inv.InvitationSent = true
	return s.UpdateInvitation(inv)
}
