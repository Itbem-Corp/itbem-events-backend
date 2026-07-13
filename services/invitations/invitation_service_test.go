package invitations

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockInvitationRepo implements ports.InvitationRepository
type mockInvitationRepo struct {
	GetInvitationByIDFunc     func(id uuid.UUID) (*models.Invitation, error)
	GetInvitationByIDLiteFunc func(id uuid.UUID) (*models.Invitation, error)
	CreateInvitationFunc      func(m *models.Invitation) error
	UpdateInvitationFunc      func(m *models.Invitation) error
	DeleteInvitationFunc      func(id uuid.UUID) error
	ListInvitationsFunc       func() ([]models.Invitation, error)
}

func (m *mockInvitationRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	if m.GetInvitationByIDFunc != nil {
		return m.GetInvitationByIDFunc(id)
	}
	return nil, nil
}
func (m *mockInvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	if m.GetInvitationByIDLiteFunc != nil {
		return m.GetInvitationByIDLiteFunc(id)
	}
	return nil, nil
}
func (m *mockInvitationRepo) CreateInvitation(obj *models.Invitation) error {
	if m.CreateInvitationFunc != nil {
		return m.CreateInvitationFunc(obj)
	}
	return nil
}
func (m *mockInvitationRepo) UpdateInvitation(obj *models.Invitation) error {
	if m.UpdateInvitationFunc != nil {
		return m.UpdateInvitationFunc(obj)
	}
	return nil
}
func (m *mockInvitationRepo) DeleteInvitation(id uuid.UUID) error {
	if m.DeleteInvitationFunc != nil {
		return m.DeleteInvitationFunc(id)
	}
	return nil
}
func (m *mockInvitationRepo) ListInvitations() ([]models.Invitation, error) {
	if m.ListInvitationsFunc != nil {
		return m.ListInvitationsFunc()
	}
	return nil, nil
}
func (m *mockInvitationRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return []models.Invitation{}, nil
}

// Verify mockInvitationRepo satisfies the interface at compile time.
var _ ports.InvitationRepository = (*mockInvitationRepo)(nil)

// ---------------------------------------------------------------------------

// mockGuestRepo implements ports.GuestRepository
type mockGuestRepo struct {
	GetGuestByInvitationIDFunc func(invitationID uuid.UUID) (*models.Guest, error)
	CreateGuestFunc            func(m *models.Guest) error
	UpdateGuestFunc            func(m *models.Guest) error
	DeleteGuestFunc            func(id uuid.UUID) error
	GetGuestByIDFunc           func(id uuid.UUID) (*models.Guest, error)
	CreateGuestsFunc           func(guests []models.Guest) error
	BulkDeleteGuestsFunc       func(ids []uuid.UUID) error
	ListGuestsByEventIDFunc    func(eventID uuid.UUID) ([]models.Guest, error)
	GetPendingStatusIDFunc     func() uuid.UUID
}

func (m *mockGuestRepo) GetGuestByInvitationID(invitationID uuid.UUID) (*models.Guest, error) {
	if m.GetGuestByInvitationIDFunc != nil {
		return m.GetGuestByInvitationIDFunc(invitationID)
	}
	return nil, nil
}
func (m *mockGuestRepo) CreateGuest(obj *models.Guest) error {
	if m.CreateGuestFunc != nil {
		return m.CreateGuestFunc(obj)
	}
	return nil
}
func (m *mockGuestRepo) UpdateGuest(obj *models.Guest) error {
	if m.UpdateGuestFunc != nil {
		return m.UpdateGuestFunc(obj)
	}
	return nil
}
func (m *mockGuestRepo) DeleteGuest(id uuid.UUID) error {
	if m.DeleteGuestFunc != nil {
		return m.DeleteGuestFunc(id)
	}
	return nil
}
func (m *mockGuestRepo) GetGuestByID(id uuid.UUID) (*models.Guest, error) {
	if m.GetGuestByIDFunc != nil {
		return m.GetGuestByIDFunc(id)
	}
	return nil, nil
}
func (m *mockGuestRepo) CreateGuests(guests []models.Guest) error {
	if m.CreateGuestsFunc != nil {
		return m.CreateGuestsFunc(guests)
	}
	return nil
}
func (m *mockGuestRepo) BulkDeleteGuests(ids []uuid.UUID) error {
	if m.BulkDeleteGuestsFunc != nil {
		return m.BulkDeleteGuestsFunc(ids)
	}
	return nil
}
func (m *mockGuestRepo) ListGuestsByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	if m.ListGuestsByEventIDFunc != nil {
		return m.ListGuestsByEventIDFunc(eventID)
	}
	return nil, nil
}
func (m *mockGuestRepo) GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error) {
	return dtos.GuestSummary{}, nil
}
func (m *mockGuestRepo) ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return nil, nil
}
func (m *mockGuestRepo) GetPendingStatusID() uuid.UUID {
	if m.GetPendingStatusIDFunc != nil {
		return m.GetPendingStatusIDFunc()
	}
	return uuid.Nil
}

var _ ports.GuestRepository = (*mockGuestRepo)(nil)

// ---------------------------------------------------------------------------

// mockAccessTokenRepo implements ports.AccessTokenRepository
type mockAccessTokenRepo struct {
	GetByTokenFunc          func(token string) (*models.InvitationAccessToken, error)
	GetByPrettyTokenFunc    func(code string) (*models.InvitationAccessToken, error)
	GeneratePrettyTokenFunc func(eventID uuid.UUID, length int) (string, error)
}

func (m *mockAccessTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	if m.GetByTokenFunc != nil {
		return m.GetByTokenFunc(token)
	}
	return nil, nil
}
func (m *mockAccessTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	if m.GetByPrettyTokenFunc != nil {
		return m.GetByPrettyTokenFunc(code)
	}
	return nil, nil
}
func (m *mockAccessTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	if m.GeneratePrettyTokenFunc != nil {
		return m.GeneratePrettyTokenFunc(eventID, length)
	}
	return "ABCD1234", nil
}

var _ ports.AccessTokenRepository = (*mockAccessTokenRepo)(nil)

// ---------------------------------------------------------------------------

// mockInvitationLogRepo implements ports.InvitationLogRepository
type mockInvitationLogRepo struct {
	CreateInvitationLogFunc      func(m *models.InvitationLog) error
	CreateManyInvitationLogsFunc func(logs []models.InvitationLog) error
}

func (m *mockInvitationLogRepo) CreateInvitationLog(obj *models.InvitationLog) error {
	if m.CreateInvitationLogFunc != nil {
		return m.CreateInvitationLogFunc(obj)
	}
	return nil
}
func (m *mockInvitationLogRepo) CreateManyInvitationLogs(logs []models.InvitationLog) error {
	if m.CreateManyInvitationLogsFunc != nil {
		return m.CreateManyInvitationLogsFunc(logs)
	}
	return nil
}

var _ ports.InvitationLogRepository = (*mockInvitationLogRepo)(nil)

// ---------------------------------------------------------------------------

// mockCacheRepo implements ports.CacheRepository
type mockCacheRepo struct {
	InvalidateFunc          func(resource string, key string) error
	DeleteKeysByPatternFunc func(ctx context.Context, pattern string) error
	GetKeyFunc              func(ctx context.Context, key string) (string, error)
	SaveKeyFunc             func(ctx context.Context, key string, value string, ttl time.Duration) error
}

func (m *mockCacheRepo) Invalidate(resource string, key string) error {
	if m.InvalidateFunc != nil {
		return m.InvalidateFunc(resource, key)
	}
	return nil
}
func (m *mockCacheRepo) DeleteKeysByPattern(ctx context.Context, pattern string) error {
	if m.DeleteKeysByPatternFunc != nil {
		return m.DeleteKeysByPatternFunc(ctx, pattern)
	}
	return nil
}
func (m *mockCacheRepo) GetKey(ctx context.Context, key string) (string, error) {
	if m.GetKeyFunc != nil {
		return m.GetKeyFunc(ctx, key)
	}
	return "", errors.New("cache miss")
}
func (m *mockCacheRepo) SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error {
	if m.SaveKeyFunc != nil {
		return m.SaveKeyFunc(ctx, key, value, ttl)
	}
	return nil
}

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

// ---------------------------------------------------------------------------

type mockEventsRepo struct {
	GetEventByIDRawFunc func(id uuid.UUID) (*models.Event, error)
}

func (m *mockEventsRepo) CreateEvent(event *models.Event) error { return nil }
func (m *mockEventsRepo) UpdateEvent(event *models.Event) error { return nil }
func (m *mockEventsRepo) DeleteEvent(id uuid.UUID) error        { return nil }
func (m *mockEventsRepo) ListEvents(page int, pageSize int, name string) ([]models.Event, error) {
	return nil, nil
}
func (m *mockEventsRepo) GetEventByID(id uuid.UUID) (string, error) { return "", nil }
func (m *mockEventsRepo) GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	if m.GetEventByIDRawFunc != nil {
		return m.GetEventByIDRawFunc(id)
	}
	return &models.Event{ID: id, IsActive: true}, nil
}
func (m *mockEventsRepo) GetEventByIDForSpec(id uuid.UUID) (*models.Event, error) {
	return m.GetEventByIDRaw(id)
}
func (m *mockEventsRepo) GetEventByIdentifier(identifier string) (*models.Event, error) {
	return nil, nil
}
func (m *mockEventsRepo) GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockEventsRepo) GetAllEventsForDashboard() ([]models.Event, error) { return nil, nil }
func (m *mockEventsRepo) GetEventsForUser(userID uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockEventsRepo) UpdateEventCover(id uuid.UUID, coverImageURL string) error { return nil }
func (m *mockEventsRepo) IdentifierExists(identifier string) bool                   { return false }

var _ ports.EventsRepository = (*mockEventsRepo)(nil)

// ---------------------------------------------------------------------------

type mockEventConfigRepo struct {
	GetEventConfigByIDFunc func(id uuid.UUID) (*models.EventConfig, error)
}

func (m *mockEventConfigRepo) CreateEventConfig(model *models.EventConfig) error { return nil }
func (m *mockEventConfigRepo) UpdateEventConfig(model *models.EventConfig) error { return nil }
func (m *mockEventConfigRepo) DeleteEventConfig(id uuid.UUID) error              { return nil }
func (m *mockEventConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	if m.GetEventConfigByIDFunc != nil {
		return m.GetEventConfigByIDFunc(id)
	}
	return &models.EventConfig{ID: id}, nil
}

var _ ports.EventConfigRepository = (*mockEventConfigRepo)(nil)

// ---------------------------------------------------------------------------

type mockEventAnalyticsRepo struct {
	current *models.EventAnalytics
	created *models.EventAnalytics
	updated *models.EventAnalytics
}

func (m *mockEventAnalyticsRepo) CreateEventAnalytics(model *models.EventAnalytics) error {
	copy := *model
	m.created = &copy
	m.current = &copy
	return nil
}

func (m *mockEventAnalyticsRepo) UpdateEventAnalytics(model *models.EventAnalytics) error {
	copy := *model
	m.updated = &copy
	m.current = &copy
	return nil
}

func (m *mockEventAnalyticsRepo) DeleteEventAnalytics(id uuid.UUID) error { return nil }

func (m *mockEventAnalyticsRepo) GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	if m.current == nil {
		return nil, errors.New("record not found")
	}
	copy := *m.current
	return &copy, nil
}

func (m *mockEventAnalyticsRepo) GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	if m.current == nil || m.current.EventID != eventID {
		return nil, errors.New("record not found")
	}
	copy := *m.current
	return &copy, nil
}

func (m *mockEventAnalyticsRepo) ListEventAnalyticss() ([]models.EventAnalytics, error) {
	if m.current == nil {
		return nil, nil
	}
	return []models.EventAnalytics{*m.current}, nil
}

var _ ports.EventAnalyticsRepository = (*mockEventAnalyticsRepo)(nil)

func withAnalyticsService(repo *mockEventAnalyticsRepo, cache *mockCacheRepo) func() {
	if cache == nil {
		cache = &mockCacheRepo{}
	}
	eventsService.SetDefaultEventAnalyticsService(eventsService.NewEventAnalyticsService(repo, cache))
	return func() {
		eventsService.SetDefaultEventAnalyticsService(nil)
	}
}

// ---------------------------------------------------------------------------
// Helper: build a default InvitationService wired with provided mocks.
// Any nil mock argument falls back to a no-op mock.
// ---------------------------------------------------------------------------

func newService(
	inv *mockInvitationRepo,
	guest *mockGuestRepo,
	token *mockAccessTokenRepo,
	log *mockInvitationLogRepo,
	cache *mockCacheRepo,
) *InvitationService {
	if inv == nil {
		inv = &mockInvitationRepo{}
	}
	if guest == nil {
		guest = &mockGuestRepo{}
	}
	if token == nil {
		token = &mockAccessTokenRepo{}
	}
	if log == nil {
		log = &mockInvitationLogRepo{}
	}
	if cache == nil {
		cache = &mockCacheRepo{}
	}
	return NewInvitationService(inv, guest, token, log, cache)
}

func newServiceWithPublicAccess(
	inv *mockInvitationRepo,
	guest *mockGuestRepo,
	token *mockAccessTokenRepo,
	log *mockInvitationLogRepo,
	cache *mockCacheRepo,
	eventsRepo ports.EventsRepository,
	configRepo ports.EventConfigRepository,
	now func() time.Time,
) *InvitationService {
	if inv == nil {
		inv = &mockInvitationRepo{}
	}
	if guest == nil {
		guest = &mockGuestRepo{}
	}
	if token == nil {
		token = &mockAccessTokenRepo{}
	}
	if log == nil {
		log = &mockInvitationLogRepo{}
	}
	if cache == nil {
		cache = &mockCacheRepo{}
	}
	return NewInvitationServiceWithDeps(InvitationServiceDeps{
		Repo:       inv,
		GuestRepo:  guest,
		TokenRepo:  token,
		LogRepo:    log,
		Cache:      cache,
		EventsRepo: eventsRepo,
		ConfigRepo: configRepo,
		Now:        now,
	})
}

// ---------------------------------------------------------------------------
// ConfirmRSVP tests
// ---------------------------------------------------------------------------

func TestConfirmRSVP_InvalidToken(t *testing.T) {
	// tokenRepo.GetByPrettyToken returns an error → must propagate "invalid or expired token"
	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return nil, errors.New("record not found")
		},
	}

	svc := newService(nil, nil, tokenRepo, nil, nil)
	guest, err := svc.ConfirmRSVP("BAD-TOKEN", "confirmed", "web", 1, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired token")
	assert.Nil(t, guest)
}

func TestConfirmRSVP_InvalidToken_NilReturn(t *testing.T) {
	// tokenRepo returns (nil, nil) — token simply does not exist
	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return nil, nil // nil pointer is also treated as invalid
		},
	}

	svc := newService(nil, nil, tokenRepo, nil, nil)
	guest, err := svc.ConfirmRSVP("MISSING", "confirmed", "web", 1, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired token")
	assert.Nil(t, guest)
}

func TestConfirmRSVP_ExpiredToken(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	expiredAt := time.Now().Add(-time.Hour)
	invitationLookupCalled := false
	guestLookupCalled := false

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
				ExpiresAt:    &expiredAt,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			invitationLookupCalled = true
			return &models.Invitation{ID: id, MaxGuests: 3}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			guestLookupCalled = true
			return &models.Guest{InvitationID: &invID}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	guest, err := svc.ConfirmRSVP("EXPIRED", "confirmed", "web", 1, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired token")
	assert.Nil(t, guest)
	assert.False(t, invitationLookupCalled)
	assert.False(t, guestLookupCalled)
}

func TestConfirmRSVP_UsesInjectedClockForTokenExpiryAndTimestamp(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Minute)
	var capturedGuest *models.Guest
	var capturedLog *models.InvitationLog

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
				ExpiresAt:    &expiresAt,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 3}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, InvitationID: &invitationID}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			copy := *g
			capturedGuest = &copy
			return nil
		},
	}
	logRepo := &mockInvitationLogRepo{
		CreateInvitationLogFunc: func(m *models.InvitationLog) error {
			copy := *m
			capturedLog = &copy
			return nil
		},
	}

	svc := newServiceWithPublicAccess(invRepo, guestRepo, tokenRepo, logRepo, nil, nil, nil, func() time.Time {
		return now
	})
	result, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 1, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, capturedGuest)
	require.NotNil(t, capturedGuest.RSVPAt)
	assert.Equal(t, now, *capturedGuest.RSVPAt)
	require.NotNil(t, capturedLog)
	assert.Equal(t, now, capturedLog.Timestamp)
	assert.Equal(t, now, capturedLog.CreatedAt)
}

func TestConfirmRSVP_InvitationNotFound(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return nil, errors.New("invitation not found")
		},
	}

	svc := newService(invRepo, nil, tokenRepo, nil, nil)
	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 1, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invitation not found")
	assert.Nil(t, guest)
}

func TestConfirmRSVP_BlocksInactiveEventBeforeGuestLookup(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestLookupCalled := false

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 3}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			guestLookupCalled = true
			return &models.Guest{InvitationID: &invitationID}, nil
		},
	}
	eventsRepo := &mockEventsRepo{
		GetEventByIDRawFunc: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, IsActive: false}, nil
		},
	}

	svc := newServiceWithPublicAccess(invRepo, guestRepo, tokenRepo, nil, nil, eventsRepo, nil, nil)
	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 1, "")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvitationEventUnavailable)
	assert.Nil(t, guest)
	assert.False(t, guestLookupCalled)
}

func TestConfirmRSVP_BlocksBeforeActiveFromBeforeGuestLookup(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	activeFrom := now.Add(time.Hour)
	guestLookupCalled := false

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 3}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			guestLookupCalled = true
			return &models.Guest{InvitationID: &invitationID}, nil
		},
	}
	configRepo := &mockEventConfigRepo{
		GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{ID: id, ActiveFrom: activeFrom}, nil
		},
	}

	svc := newServiceWithPublicAccess(invRepo, guestRepo, tokenRepo, nil, nil, nil, configRepo, func() time.Time {
		return now
	})
	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 1, "")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvitationEventUnavailable)
	assert.Nil(t, guest)
	assert.False(t, guestLookupCalled)
}

func TestConfirmRSVP_ExceedsMaxGuests(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				MaxGuests: 3, // max is 3
			}, nil
		},
	}

	svc := newService(invRepo, nil, tokenRepo, nil, nil)
	// Requesting 5 guests when max is 3 — must fail
	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 5, "")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRSVPRequest)
	assert.Contains(t, err.Error(), "exceeds allowed max")
	assert.Nil(t, guest)
}

func TestConfirmRSVP_UsesOneAsMinimumForLegacyZeroMaxGuests(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	var capturedGuest *models.Guest

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 0}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, InvitationID: &invitationID}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			copy := *g
			capturedGuest = &copy
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 1, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, capturedGuest)
	assert.Equal(t, 1, capturedGuest.RSVPGuestCount)
}

func TestConfirmRSVP_RejectsNegativeGuestCount(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				MaxGuests: 3,
			}, nil
		},
	}

	svc := newService(invRepo, nil, tokenRepo, nil, nil)
	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", -1, "")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRSVPRequest)
	assert.Contains(t, err.Error(), "cannot be negative")
	assert.Nil(t, guest)
}

func TestConfirmRSVP_RejectsConfirmedWithoutGuestCount(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				MaxGuests: 3,
			}, nil
		},
	}

	svc := newService(invRepo, nil, tokenRepo, nil, nil)
	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 0, "")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRSVPRequest)
	assert.Contains(t, err.Error(), "at least 1")
	assert.Nil(t, guest)
}

func TestConfirmRSVP_RejectsInvalidStatus(t *testing.T) {
	svc := newService(nil, nil, nil, nil, nil)

	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "maybe", "web", 1, "")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRSVPRequest)
	assert.Contains(t, err.Error(), "invalid RSVP status")
	assert.Nil(t, guest)
}

func TestConfirmRSVP_NormalizesStatusAndMethod(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	var capturedGuest *models.Guest

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 3}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, InvitationID: &invitationID}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			copy := *g
			capturedGuest = &copy
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("VALID-TOKEN", " CONFIRMED ", " WEB ", 2, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, capturedGuest)
	assert.Equal(t, "confirmed", capturedGuest.RSVPStatus)
	assert.Equal(t, "web", capturedGuest.RSVPMethod)
}

func TestConfirmRSVP_ExceedsMaxGuests_ExactBoundary(t *testing.T) {
	// Exactly at the limit (guestCount == MaxGuests) should succeed, not fail
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				MaxGuests: 4,
			}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:            guestID,
				InvitationID:  &invitationID,
				GuestStatusID: uuid.Must(uuid.NewV4()),
			}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	// guestCount == MaxGuests: should succeed
	result, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 4, "")

	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestConfirmRSVP_Success(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				MaxGuests: 5,
			}, nil
		},
	}

	var capturedGuest *models.Guest
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:            guestID,
				InvitationID:  &invitationID,
				GuestStatusID: uuid.Must(uuid.NewV4()),
			}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			capturedGuest = g
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("MY-TOKEN", "confirmed", "web", 2, "")

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify the RSVP fields were populated correctly
	assert.Equal(t, "confirmed", result.RSVPStatus)
	assert.Equal(t, "web", result.RSVPMethod)
	assert.Equal(t, 2, result.RSVPGuestCount)
	assert.NotNil(t, result.RSVPAt)
	assert.Equal(t, &tokenID, result.RSVPTokenID)

	// Verify UpdateGuest was called with the same data
	require.NotNil(t, capturedGuest)
	assert.Equal(t, "confirmed", capturedGuest.RSVPStatus)
}

func TestConfirmRSVP_AcceptsRawToken(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	var rawSeen string
	var prettySeen string
	var capturedGuest *models.Guest

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			rawSeen = token
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				Token:        token,
			}, nil
		},
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			prettySeen = code
			return nil, errors.New("pretty token should not be needed")
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 5}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:           guestID,
				InvitationID: &invitationID,
			}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			copy := *g
			capturedGuest = &copy
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("RAW/123", "confirmed", "web", 2, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, capturedGuest)
	assert.Equal(t, "RAW/123", rawSeen)
	assert.Empty(t, prettySeen)
	assert.Equal(t, &tokenID, capturedGuest.RSVPTokenID)
	assert.Equal(t, "confirmed", capturedGuest.RSVPStatus)
}

func TestConfirmRSVPWithResult_ReturnsCanonicalPrettyToken(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				Token:        token,
				PrettyToken:  "PRETTY/123",
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 5}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:           guestID,
				InvitationID: &invitationID,
			}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVPWithResult("RAW/123", "confirmed", "web", 2, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Guest)
	assert.Equal(t, "PRETTY/123", result.PrettyToken)
	assert.Equal(t, &tokenID, result.Guest.RSVPTokenID)
}

func TestConfirmRSVP_ReplacesDietaryNotesWithLatestRSVPNotes(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 5}, nil
		},
	}

	var capturedGuest *models.Guest
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:                  guestID,
				InvitationID:        &invitationID,
				DietaryRestrictions: "Sin gluten",
			}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			copy := *g
			capturedGuest = &copy
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("MY-TOKEN", "declined", "web", 0, "   ")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, capturedGuest)
	assert.Equal(t, "declined", capturedGuest.RSVPStatus)
	assert.Equal(t, "", capturedGuest.DietaryRestrictions)
}

func TestConfirmRSVP_StoresDietaryRestrictionsAndGuestNotesSeparately(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 5}, nil
		},
	}

	var capturedGuest *models.Guest
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:           guestID,
				InvitationID: &invitationID,
				Notes:        "nota anterior",
			}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			copy := *g
			capturedGuest = &copy
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("MY-TOKEN", "confirmed", "web", 2, " Vegano ", " Mesa cerca ")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, capturedGuest)
	assert.Equal(t, "Vegano", capturedGuest.DietaryRestrictions)
	assert.Equal(t, "Mesa cerca", capturedGuest.RSVPNotes)
	assert.Equal(t, "nota anterior", capturedGuest.Notes)
}

func TestConfirmRSVP_SuccessInvalidatesGuestCaches(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				MaxGuests: 5,
			}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:            guestID,
				EventID:       eventID,
				InvitationID:  &invitationID,
				GuestStatusID: uuid.Must(uuid.NewV4()),
			}, nil
		},
	}

	var invalidatedResource string
	var invalidatedKey string
	var deletedPattern string
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			invalidatedResource = resource
			invalidatedKey = key
			return nil
		},
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			deletedPattern = pattern
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, cache)
	result, err := svc.ConfirmRSVP("MY-TOKEN", "confirmed", "web", 2, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, utils.RedisGuestsKey, invalidatedResource)
	assert.Equal(t, guestID.String(), invalidatedKey)
	assert.Equal(t, "all:"+eventID.String()+":guests", deletedPattern)
}

func TestConfirmRSVP_InvalidatesEventGuestCacheFromInvitationWhenGuestEventIDIsMissing(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				EventID:   eventID,
				MaxGuests: 5,
			}, nil
		},
	}

	var updatedGuest *models.Guest
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:           guestID,
				InvitationID: &invitationID,
			}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			copy := *g
			updatedGuest = &copy
			return nil
		},
	}

	var deletedPattern string
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			deletedPattern = pattern
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, cache)
	result, err := svc.ConfirmRSVP("MY-TOKEN", "confirmed", "web", 2, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, updatedGuest)
	assert.Equal(t, eventID, updatedGuest.EventID)
	assert.Equal(t, eventID, result.EventID)
	assert.Equal(t, "all:"+eventID.String()+":guests", deletedPattern)
}

func TestConfirmRSVP_AdjustsAnalyticsFromPendingToConfirmed(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	analyticsRepo := &mockEventAnalyticsRepo{
		current: &models.EventAnalytics{ID: uuid.Must(uuid.NewV4()), EventID: eventID},
	}
	defer withAnalyticsService(analyticsRepo, nil)()

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{ID: tokenID, InvitationID: invitationID, PrettyToken: code}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 5}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, EventID: eventID, InvitationID: &invitationID, RSVPStatus: "pending"}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("MY-TOKEN", "confirmed", "web", 2, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, analyticsRepo.updated)
	assert.Equal(t, 1, analyticsRepo.updated.RSVPConfirmed)
	assert.Equal(t, 0, analyticsRepo.updated.RSVPDeclined)
}

func TestConfirmRSVP_AdjustsAnalyticsFromConfirmedToDeclined(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	analyticsRepo := &mockEventAnalyticsRepo{
		current: &models.EventAnalytics{
			ID:            uuid.Must(uuid.NewV4()),
			EventID:       eventID,
			RSVPConfirmed: 3,
			RSVPDeclined:  1,
		},
	}
	defer withAnalyticsService(analyticsRepo, nil)()

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{ID: tokenID, InvitationID: invitationID, PrettyToken: code}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 5}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, EventID: eventID, InvitationID: &invitationID, RSVPStatus: "confirmed"}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("MY-TOKEN", "declined", "web", 0, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, analyticsRepo.updated)
	assert.Equal(t, 2, analyticsRepo.updated.RSVPConfirmed)
	assert.Equal(t, 2, analyticsRepo.updated.RSVPDeclined)
}

func TestConfirmRSVP_DoesNotDoubleCountAnalyticsForSameStatus(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	analyticsRepo := &mockEventAnalyticsRepo{
		current: &models.EventAnalytics{
			ID:            uuid.Must(uuid.NewV4()),
			EventID:       eventID,
			RSVPConfirmed: 3,
			RSVPDeclined:  1,
		},
	}
	defer withAnalyticsService(analyticsRepo, nil)()

	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{ID: tokenID, InvitationID: invitationID, PrettyToken: code}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 5}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, EventID: eventID, InvitationID: &invitationID, RSVPStatus: "confirmed"}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.ConfirmRSVP("MY-TOKEN", "confirmed", "web", 2, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Nil(t, analyticsRepo.updated)
}

func TestResendInvitationInvalidatesInvitationCache(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	var updatedInvitation *models.Invitation
	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			require.Equal(t, invitationID, id)
			return &models.Invitation{
				ID:      invitationID,
				EventID: eventID,
			}, nil
		},
		UpdateInvitationFunc: func(inv *models.Invitation) error {
			copy := *inv
			updatedInvitation = &copy
			return nil
		},
	}

	var logs []models.InvitationLog
	logRepo := &mockInvitationLogRepo{
		CreateManyInvitationLogsFunc: func(items []models.InvitationLog) error {
			logs = append(logs, items...)
			return nil
		},
	}

	var invalidatedResource string
	var invalidatedKey string
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			invalidatedResource = resource
			invalidatedKey = key
			return nil
		},
	}

	svc := newService(invRepo, nil, nil, logRepo, cache)
	err := svc.ResendInvitation(invitationID)

	require.NoError(t, err)
	require.NotNil(t, updatedInvitation)
	assert.True(t, updatedInvitation.InvitationSent)
	require.Len(t, logs, 1)
	assert.Equal(t, "manual", logs[0].Channel)
	assert.Equal(t, utils.RedisInvitationsKey, invalidatedResource)
	assert.Equal(t, "all", invalidatedKey)
}

func TestInvitationMutationsIgnoreCacheInvalidationError(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	var created bool
	var updated bool
	var deleted bool
	var invalidations int

	invRepo := &mockInvitationRepo{
		CreateInvitationFunc: func(inv *models.Invitation) error {
			created = true
			return nil
		},
		UpdateInvitationFunc: func(inv *models.Invitation) error {
			updated = true
			return nil
		},
		DeleteInvitationFunc: func(id uuid.UUID) error {
			deleted = true
			return nil
		},
	}
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			invalidations++
			assert.Equal(t, utils.RedisInvitationsKey, resource)
			assert.Equal(t, "all", key)
			return errors.New("redis unavailable")
		},
	}

	svc := newService(invRepo, nil, nil, nil, cache)

	require.NoError(t, svc.CreateInvitation(&models.Invitation{ID: invitationID}))
	require.NoError(t, svc.UpdateInvitation(&models.Invitation{ID: invitationID}))
	require.NoError(t, svc.DeleteInvitation(invitationID))
	assert.True(t, created)
	assert.True(t, updated)
	assert.True(t, deleted)
	assert.Equal(t, 3, invalidations)
}

func TestResendInvitationIgnoresCacheInvalidationError(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	var updatedInvitation *models.Invitation
	var logs []models.InvitationLog

	invRepo := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			require.Equal(t, invitationID, id)
			return &models.Invitation{ID: invitationID}, nil
		},
		UpdateInvitationFunc: func(inv *models.Invitation) error {
			copy := *inv
			updatedInvitation = &copy
			return nil
		},
	}
	logRepo := &mockInvitationLogRepo{
		CreateManyInvitationLogsFunc: func(items []models.InvitationLog) error {
			logs = append(logs, items...)
			return nil
		},
	}
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			assert.Equal(t, utils.RedisInvitationsKey, resource)
			assert.Equal(t, "all", key)
			return errors.New("redis unavailable")
		},
	}

	svc := newService(invRepo, nil, nil, logRepo, cache)
	err := svc.ResendInvitation(invitationID)

	require.NoError(t, err)
	require.NotNil(t, updatedInvitation)
	assert.True(t, updatedInvitation.InvitationSent)
	require.Len(t, logs, 1)
}

func TestListInvitationsWorksWithoutCache(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	invRepo := &mockInvitationRepo{
		ListInvitationsFunc: func() ([]models.Invitation, error) {
			return []models.Invitation{{ID: invitationID}}, nil
		},
	}

	svc := NewInvitationService(invRepo, &mockGuestRepo{}, &mockAccessTokenRepo{}, &mockInvitationLogRepo{}, nil)
	result, err := svc.ListInvitations()

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, invitationID, result[0].ID)
}

// ---------------------------------------------------------------------------
// GetInvitationByToken tests
// ---------------------------------------------------------------------------

func TestGetInvitationByToken_ExpiredToken(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour) // 1 hour in the past

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: uuid.Must(uuid.NewV4()),
				Token:        token,
				PrettyToken:  "ABCD1234",
				ExpiresAt:    &past,
			}, nil
		},
	}

	svc := newService(nil, nil, tokenRepo, nil, nil)
	result, err := svc.GetInvitationByToken("some-expired-token")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "token expired")
	assert.Nil(t, result)
}

func TestGetInvitationByToken_UsesInjectedClockForTokenExpiryAndAccessLog(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Minute)
	var capturedLog *models.InvitationLog

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				Token:        token,
				PrettyToken:  "ABCD1234",
				ExpiresAt:    &expiresAt,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 3}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, InvitationID: &invitationID}, nil
		},
	}
	logRepo := &mockInvitationLogRepo{
		CreateInvitationLogFunc: func(m *models.InvitationLog) error {
			copy := *m
			capturedLog = &copy
			return nil
		},
	}

	svc := newServiceWithPublicAccess(invRepo, guestRepo, tokenRepo, logRepo, nil, nil, nil, func() time.Time {
		return now
	})
	result, err := svc.GetInvitationByToken("valid-at-injected-now")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "ABCD1234", result.PrettyToken)
	require.NotNil(t, capturedLog)
	assert.Equal(t, now, capturedLog.Timestamp)
	assert.Equal(t, now, capturedLog.CreatedAt)
}

func TestGetInvitationByToken_TokenNotFound(t *testing.T) {
	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return nil, errors.New("record not found")
		},
	}

	svc := newService(nil, nil, tokenRepo, nil, nil)
	result, err := svc.GetInvitationByToken("non-existent-token")

	require.Error(t, err)
	assert.Nil(t, result)
}

func TestGetInvitationByToken_TokenNilResult(t *testing.T) {
	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return nil, nil // token simply not found
		},
	}

	svc := newService(nil, nil, tokenRepo, nil, nil)
	result, err := svc.GetInvitationByToken("missing-token")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "access token not found")
	assert.Nil(t, result)
}

func TestGetInvitationByToken_InvitationNotFound(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	guestLookupCalled := false
	logCalled := false

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				Token:        token,
				PrettyToken:  "NICE1234",
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return nil, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			guestLookupCalled = true
			return &models.Guest{InvitationID: &invitationID}, nil
		},
	}
	logRepo := &mockInvitationLogRepo{
		CreateInvitationLogFunc: func(m *models.InvitationLog) error {
			logCalled = true
			return nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, logRepo, nil)
	result, err := svc.GetInvitationByToken("valid-token")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invitation not found for token")
	assert.Nil(t, result)
	assert.False(t, guestLookupCalled)
	assert.False(t, logCalled)
}

func TestGetInvitationByToken_BlocksInactiveEventBeforeGuestLookup(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guestLookupCalled := false
	logCalled := false

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				Token:        token,
				PrettyToken:  "NICE1234",
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, EventID: eventID}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			guestLookupCalled = true
			return &models.Guest{InvitationID: &invitationID}, nil
		},
	}
	logRepo := &mockInvitationLogRepo{
		CreateInvitationLogFunc: func(m *models.InvitationLog) error {
			logCalled = true
			return nil
		},
	}
	eventsRepo := &mockEventsRepo{
		GetEventByIDRawFunc: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, IsActive: false}, nil
		},
	}

	svc := newServiceWithPublicAccess(invRepo, guestRepo, tokenRepo, logRepo, nil, eventsRepo, nil, nil)
	result, err := svc.GetInvitationByToken("valid-token")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvitationEventUnavailable)
	assert.Nil(t, result)
	assert.False(t, guestLookupCalled)
	assert.False(t, logCalled)
}

func TestGetInvitationByToken_BlocksAfterActiveUntilBeforeGuestLookup(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	activeUntil := now.Add(-time.Hour)
	guestLookupCalled := false

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				Token:        token,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, EventID: eventID}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			guestLookupCalled = true
			return &models.Guest{InvitationID: &invitationID}, nil
		},
	}
	configRepo := &mockEventConfigRepo{
		GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{ID: id, ActiveUntil: &activeUntil}, nil
		},
	}

	svc := newServiceWithPublicAccess(invRepo, guestRepo, tokenRepo, nil, nil, nil, configRepo, func() time.Time {
		return now
	})
	result, err := svc.GetInvitationByToken("valid-token")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvitationEventUnavailable)
	assert.Nil(t, result)
	assert.False(t, guestLookupCalled)
}

func TestGetInvitationByToken_ReturnsRawTokenWhenPrettyTokenIsMissing(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				Token:        token,
				PrettyToken:  "",
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID, MaxGuests: 3}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, InvitationID: &invitationID}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.GetInvitationByToken("RAW/123")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "RAW/123", result.PrettyToken)
	assert.Equal(t, invitationID, result.Invitation.ID)
	assert.Equal(t, guestID, result.Guest.ID)
}

func TestGetInvitationByToken_AcceptsPrettyTokenFallback(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	var rawSeen string
	var prettySeen string

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			rawSeen = token
			return nil, errors.New("record not found")
		},
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			prettySeen = code
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				PrettyToken:  code,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:      id,
				EventID: eventID,
				Event: models.Event{
					ID:   eventID,
					Name: "Evento Privado",
				},
			}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, EventID: eventID, InvitationID: &invitationID}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.GetInvitationByToken("PRETTY123")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "PRETTY123", rawSeen)
	assert.Equal(t, "PRETTY123", prettySeen)
	assert.Equal(t, "PRETTY123", result.PrettyToken)
	assert.Equal(t, invitationID, result.Invitation.ID)
	assert.Equal(t, guestID, result.Guest.ID)
	require.NotNil(t, result.Event)
	assert.Equal(t, "Evento Privado", result.Event.Name)
}

func TestGetInvitationByToken_Success(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	future := time.Now().Add(24 * time.Hour)
	rsvpAt := time.Date(2026, 7, 1, 18, 45, 0, 0, time.UTC)

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           tokenID,
				InvitationID: invitationID,
				Token:        token,
				PrettyToken:  "NICE1234",
				ExpiresAt:    &future,
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				EventID:   eventID,
				MaxGuests: 4,
				Event: models.Event{
					ID:               eventID,
					Name:             "Boda Ana y Luis",
					Identifier:       "boda-ana-luis",
					Description:      "Ceremonia y recepcion",
					CoverImageURL:    "covers/event.webp",
					EventDateTime:    time.Date(2026, 8, 15, 20, 30, 0, 0, time.UTC),
					Address:          "Jardin Central",
					SecondAddress:    "Salon Central",
					Timezone:         "America/Mexico_City",
					Language:         "es",
					OrganizerName:    "Ana y Luis",
					OrganizerEmail:   "private@example.com",
					OrganizerPhone:   "+525555555555",
					EventType:        models.EventType{Name: "wedding"},
					AllowGuestAccess: true,
				},
			}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:           guestID,
				EventID:      eventID,
				InvitationID: &invitationID,
				FirstName:    "John",
				LastName:     "Doe",
				Email:        "john@example.com",
				Phone:        "+525511111111",
				RSVPStatus:   "pending",
				RSVPAt:       &rsvpAt,
				RSVPMethod:   "web",
			}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.GetInvitationByToken("valid-token-uuid")

	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, invitationID, result.Invitation.ID)
	assert.Equal(t, eventID, result.Invitation.EventID)
	assert.Equal(t, guestID, result.Guest.ID)
	assert.Equal(t, "NICE1234", result.PrettyToken)
	assert.Equal(t, "John", result.Guest.FirstName)
	require.NotNil(t, result.Guest.RSVPAt)
	assert.Equal(t, rsvpAt, *result.Guest.RSVPAt)
	assert.Equal(t, "web", result.Guest.RSVPMethod)
	require.NotNil(t, result.Event)
	assert.Equal(t, "Boda Ana y Luis", result.Event.Name)
	assert.Equal(t, "boda-ana-luis", result.Event.Identifier)
	assert.Equal(t, "wedding", result.Event.EventType)
	assert.Equal(t, "Salon Central", result.Event.SecondAddress)
	assert.Equal(t, "America/Mexico_City", result.Event.Timezone)
	assert.Equal(t, "es", result.Event.Language)
	require.NotNil(t, result.Invitation.Event)
	assert.Equal(t, "Jardin Central", result.Invitation.Event.Address)
	assert.Equal(t, "es", result.Invitation.Event.Language)

	payload, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"pretty_token":"NICE1234"`)
	assert.Contains(t, string(payload), `"event":{"name":"Boda Ana y Luis"`)
	assert.NotContains(t, string(payload), "private@example.com")
	assert.NotContains(t, string(payload), "john@example.com")
	assert.NotContains(t, string(payload), "event_config")
}

func TestGetInvitationByToken_UsesEventCoverViewURLResolver(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	expiresAt := time.Date(2026, 3, 1, 12, 5, 0, 0, time.UTC)
	var seenCoverPath string

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				Token:        token,
				PrettyToken:  "NICE1234",
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:        invitationID,
				EventID:   eventID,
				MaxGuests: 2,
				Event: models.Event{
					ID:            eventID,
					Name:          "Evento con portada",
					CoverImageURL: "events/evento/cover.webp",
				},
			}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:           guestID,
				EventID:      eventID,
				InvitationID: &invitationID,
			}, nil
		},
	}

	svc := NewInvitationServiceWithDeps(InvitationServiceDeps{
		Repo:      invRepo,
		GuestRepo: guestRepo,
		TokenRepo: tokenRepo,
		CoverViewURL: func(path string) (string, *time.Time) {
			seenCoverPath = path
			return "https://signed.example.com/events/evento/cover.webp", &expiresAt
		},
	})

	result, err := svc.GetInvitationByToken("valid-token-uuid")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "events/evento/cover.webp", seenCoverPath)
	require.NotNil(t, result.Event)
	assert.Equal(t, "events/evento/cover.webp", result.Event.CoverImageURL)
	assert.Equal(t, "https://signed.example.com/events/evento/cover.webp", result.Event.CoverViewURL)
	assert.Equal(t, "https://signed.example.com/events/evento/cover.webp", result.Event.ViewURL)
	require.NotNil(t, result.Event.CoverViewURLExpiresAt)
	assert.Equal(t, expiresAt, *result.Event.CoverViewURLExpiresAt)
	require.NotNil(t, result.Event.ViewURLExpiresAt)
	assert.Equal(t, expiresAt, *result.Event.ViewURLExpiresAt)
	require.NotNil(t, result.Invitation.Event)
	assert.Equal(t, result.Event.CoverViewURL, result.Invitation.Event.CoverViewURL)
	assert.Equal(t, result.Event.ViewURL, result.Invitation.Event.ViewURL)
}

func TestGetInvitationByToken_NonExpiredToken_NoExpirySet(t *testing.T) {
	// ExpiresAt == nil means the token never expires; must succeed
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())

	tokenRepo := &mockAccessTokenRepo{
		GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				Token:        token,
				PrettyToken:  "NOEXP000",
				ExpiresAt:    nil, // no expiry
			}, nil
		},
	}
	invRepo := &mockInvitationRepo{
		GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: invitationID}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: guestID, InvitationID: &invitationID}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.GetInvitationByToken("no-expiry-token")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "NOEXP000", result.PrettyToken)
}
