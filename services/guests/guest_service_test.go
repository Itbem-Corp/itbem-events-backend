package guests

import (
	"context"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockGuestRepo implements ports.GuestRepository
type mockGuestRepo struct {
	CreateGuestFunc              func(m *models.Guest) error
	UpdateGuestFunc              func(m *models.Guest) error
	DeleteGuestFunc              func(id uuid.UUID) error
	GetGuestByIDFunc             func(id uuid.UUID) (*models.Guest, error)
	GetGuestByInvitationIDFunc   func(invitationID uuid.UUID) (*models.Guest, error)
	CreateGuestsFunc             func(guests []models.Guest) error
	BulkDeleteGuestsFunc         func(ids []uuid.UUID) error
	BulkUpdateGuestStatusFunc    func(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error
	ListGuestsByEventIDFunc      func(eventID uuid.UUID) ([]models.Guest, error)
	GetGuestSummaryByEventIDFunc func(eventID uuid.UUID) (dtos.GuestSummary, error)
	ListAttendeesByEventIDFunc   func(eventID uuid.UUID) ([]models.Guest, error)
	GetPendingStatusIDFunc       func() uuid.UUID

	// track calls for assertions
	createGuestCalled  bool
	createGuestsCalled bool
	bulkDeleteCalled   bool
	createdGuests      []models.Guest
	bulkDeletedIDs     []uuid.UUID
}

func (m *mockGuestRepo) CreateGuest(obj *models.Guest) error {
	m.createGuestCalled = true
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
func (m *mockGuestRepo) GetGuestByInvitationID(invitationID uuid.UUID) (*models.Guest, error) {
	if m.GetGuestByInvitationIDFunc != nil {
		return m.GetGuestByInvitationIDFunc(invitationID)
	}
	return nil, nil
}
func (m *mockGuestRepo) CreateGuests(guests []models.Guest) error {
	m.createGuestsCalled = true
	m.createdGuests = guests
	if m.CreateGuestsFunc != nil {
		return m.CreateGuestsFunc(guests)
	}
	return nil
}
func (m *mockGuestRepo) BulkDeleteGuests(ids []uuid.UUID) error {
	m.bulkDeleteCalled = true
	m.bulkDeletedIDs = ids
	if m.BulkDeleteGuestsFunc != nil {
		return m.BulkDeleteGuestsFunc(ids)
	}
	return nil
}
func (m *mockGuestRepo) BulkUpdateGuestStatus(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error {
	if m.BulkUpdateGuestStatusFunc != nil {
		return m.BulkUpdateGuestStatusFunc(eventID, ids, statusID, rsvpStatus, rsvpMethod)
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
	if m.GetGuestSummaryByEventIDFunc != nil {
		return m.GetGuestSummaryByEventIDFunc(eventID)
	}
	return dtos.GuestSummary{}, nil
}
func (m *mockGuestRepo) ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	if m.ListAttendeesByEventIDFunc != nil {
		return m.ListAttendeesByEventIDFunc(eventID)
	}
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
	GetByTokenFunc                  func(token string) (*models.InvitationAccessToken, error)
	GetByPrettyTokenFunc            func(code string) (*models.InvitationAccessToken, error)
	GeneratePrettyTokenFunc         func(eventID uuid.UUID, length int) (string, error)
	GetByInvitationIDFunc           func(invitationID uuid.UUID) (*models.InvitationAccessToken, error)
	CreateInvitationAccessTokenFunc func(token *models.InvitationAccessToken) error
	UpdateInvitationAccessTokenFunc func(token *models.InvitationAccessToken) error
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
func (m *mockAccessTokenRepo) GetByInvitationID(invitationID uuid.UUID) (*models.InvitationAccessToken, error) {
	if m.GetByInvitationIDFunc != nil {
		return m.GetByInvitationIDFunc(invitationID)
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockAccessTokenRepo) CreateInvitationAccessToken(token *models.InvitationAccessToken) error {
	if m.CreateInvitationAccessTokenFunc != nil {
		return m.CreateInvitationAccessTokenFunc(token)
	}
	return nil
}
func (m *mockAccessTokenRepo) UpdateInvitationAccessToken(token *models.InvitationAccessToken) error {
	if m.UpdateInvitationAccessTokenFunc != nil {
		return m.UpdateInvitationAccessTokenFunc(token)
	}
	return nil
}

var _ ports.AccessTokenRepository = (*mockAccessTokenRepo)(nil)

// ---------------------------------------------------------------------------

// mockInvitationRepo implements ports.InvitationRepository
type mockInvitationRepo struct {
	GetInvitationByIDFunc     func(id uuid.UUID) (*models.Invitation, error)
	GetInvitationByIDLiteFunc func(id uuid.UUID) (*models.Invitation, error)
	CreateInvitationFunc      func(m *models.Invitation) error
	UpdateInvitationFunc      func(m *models.Invitation) error
	DeleteInvitationFunc      func(id uuid.UUID) error
	ListInvitationsFunc       func() ([]models.Invitation, error)
	ListByEventIDFunc         func(eventID uuid.UUID) ([]models.Invitation, error)
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
	if m.ListByEventIDFunc != nil {
		return m.ListByEventIDFunc(eventID)
	}
	return nil, nil
}

var _ ports.InvitationRepository = (*mockInvitationRepo)(nil)

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
	return "", nil
}
func (m *mockCacheRepo) SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error {
	if m.SaveKeyFunc != nil {
		return m.SaveKeyFunc(ctx, key, value, ttl)
	}
	return nil
}

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

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

// mockTransactor implements ports.Transactor — does not execute the fn
// (we are not testing DB writes here, only the guest preparation logic)
type mockTransactor struct {
	// capturedGuests is populated by inspecting guests just before tx runs
	TransactionFunc func(fn func(tx *gorm.DB) error) error
}

func (m *mockTransactor) Transaction(fn func(tx *gorm.DB) error) error {
	if m.TransactionFunc != nil {
		return m.TransactionFunc(fn)
	}
	// no-op: skip the actual DB call
	return nil
}

var _ ports.Transactor = (*mockTransactor)(nil)

// ---------------------------------------------------------------------------
// Helper: build a default GuestService
// ---------------------------------------------------------------------------

func newGuestService(
	repo *mockGuestRepo,
	accessToken *mockAccessTokenRepo,
	cache *mockCacheRepo,
	tx *mockTransactor,
	invitationRepo ...ports.InvitationRepository,
) *GuestService {
	if repo == nil {
		repo = &mockGuestRepo{}
	}
	if accessToken == nil {
		accessToken = &mockAccessTokenRepo{}
	}
	if cache == nil {
		cache = &mockCacheRepo{}
	}
	if tx == nil {
		tx = &mockTransactor{}
	}
	return NewGuestService(repo, accessToken, cache, tx, invitationRepo...)
}

func TestListGuestsByEventID_UsesServiceCacheKey(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	expectedKey := "all:" + eventID.String() + ":guests"
	repoCalls := 0

	repo := &mockGuestRepo{
		ListGuestsByEventIDFunc: func(id uuid.UUID) ([]models.Guest, error) {
			repoCalls++
			assert.Equal(t, eventID, id)
			return []models.Guest{{
				ID:        guestID,
				EventID:   eventID,
				FirstName: "Ana",
			}}, nil
		},
	}

	var getKey string
	var savedKey string
	var savedPayload string
	cache := &mockCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			getKey = key
			return "", errors.New("cache miss")
		},
		SaveKeyFunc: func(ctx context.Context, key string, value string, ttl time.Duration) error {
			savedKey = key
			savedPayload = value
			assert.Positive(t, ttl)
			return nil
		},
	}

	svc := newGuestService(repo, nil, cache, nil)
	guests, err := svc.ListGuestsByEventID(eventID)

	require.NoError(t, err)
	require.Len(t, guests, 1)
	assert.Equal(t, guestID, guests[0].ID)
	assert.Equal(t, 1, repoCalls)
	assert.Equal(t, expectedKey, getKey)
	assert.Equal(t, expectedKey, savedKey)
	assert.Contains(t, savedPayload, `"first_name":"Ana"`)
}

func TestGetGuestSummaryByEventID_DelegatesSingleRollupToRepository(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	expected := dtos.GuestSummary{
		Total:          9,
		Confirmed:      4,
		Pending:        3,
		Declined:       2,
		TotalAttendees: 11,
	}
	calls := 0
	repo := &mockGuestRepo{
		GetGuestSummaryByEventIDFunc: func(id uuid.UUID) (dtos.GuestSummary, error) {
			calls++
			assert.Equal(t, eventID, id)
			return expected, nil
		},
	}

	summary, err := newGuestService(repo, nil, nil, nil).GetGuestSummaryByEventID(eventID)

	require.NoError(t, err)
	assert.Equal(t, expected, summary)
	assert.Equal(t, 1, calls)
}

func TestListGuestsByEventID_AllowsNilCache(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	repoCalls := 0

	repo := &mockGuestRepo{
		ListGuestsByEventIDFunc: func(id uuid.UUID) ([]models.Guest, error) {
			repoCalls++
			assert.Equal(t, eventID, id)
			return []models.Guest{{
				ID:        guestID,
				EventID:   eventID,
				FirstName: "Ana",
			}}, nil
		},
	}

	svc := NewGuestService(repo, nil, nil, nil)
	guests, err := svc.ListGuestsByEventID(eventID)

	require.NoError(t, err)
	require.Len(t, guests, 1)
	assert.Equal(t, guestID, guests[0].ID)
	assert.Equal(t, 1, repoCalls)
}

func TestEnsureRSVPToken_CreatesTokenForLegacyGuest(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{ID: guestID, EventID: eventID, InvitationID: &invitationID}

	var created *models.InvitationAccessToken
	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			assert.Equal(t, guestID, id)
			return guest, nil
		},
	}
	tokens := &mockAccessTokenRepo{
		GetByInvitationIDFunc: func(id uuid.UUID) (*models.InvitationAccessToken, error) {
			assert.Equal(t, invitationID, id)
			return nil, gorm.ErrRecordNotFound
		},
		GeneratePrettyTokenFunc: func(id uuid.UUID, length int) (string, error) {
			assert.Equal(t, eventID, id)
			assert.Equal(t, 8, length)
			return "LINK1234", nil
		},
		CreateInvitationAccessTokenFunc: func(token *models.InvitationAccessToken) error {
			created = token
			return nil
		},
	}

	result, err := newGuestService(repo, tokens, nil, nil).EnsureRSVPToken(guestID)

	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, invitationID, created.InvitationID)
	assert.Equal(t, "LINK1234", created.PrettyToken)
	assert.NotEmpty(t, created.Token)
	assert.Equal(t, "LINK1234", result.PrettyToken)
}

func TestEnsureRSVPToken_CreatesInvitationForOlderLegacyGuest(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{ID: guestID, EventID: eventID, GuestsCount: 2}
	var createdInvitation *models.Invitation
	var updatedGuest *models.Guest
	var createdToken *models.InvitationAccessToken
	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(uuid.UUID) (*models.Guest, error) {
			return guest, nil
		},
		UpdateGuestFunc: func(candidate *models.Guest) error {
			updatedGuest = candidate
			return nil
		},
	}
	invitations := &mockInvitationRepo{
		CreateInvitationFunc: func(candidate *models.Invitation) error {
			createdInvitation = candidate
			return nil
		},
	}
	tokens := &mockAccessTokenRepo{
		GetByInvitationIDFunc: func(uuid.UUID) (*models.InvitationAccessToken, error) {
			return nil, gorm.ErrRecordNotFound
		},
		GeneratePrettyTokenFunc: func(id uuid.UUID, length int) (string, error) {
			assert.Equal(t, eventID, id)
			assert.Equal(t, 8, length)
			return "OLDER123", nil
		},
		CreateInvitationAccessTokenFunc: func(candidate *models.InvitationAccessToken) error {
			createdToken = candidate
			return nil
		},
	}

	result, err := newGuestService(repo, tokens, nil, nil, invitations).EnsureRSVPToken(guestID)

	require.NoError(t, err)
	require.NotNil(t, createdInvitation)
	require.NotNil(t, updatedGuest)
	require.NotNil(t, updatedGuest.InvitationID)
	require.NotNil(t, createdToken)
	assert.Equal(t, eventID, createdInvitation.EventID)
	assert.Equal(t, 2, createdInvitation.MaxGuests)
	assert.Equal(t, createdInvitation.ID, *updatedGuest.InvitationID)
	assert.Equal(t, createdInvitation.ID, createdToken.InvitationID)
	assert.Equal(t, "OLDER123", result.PrettyToken)
}

// ---------------------------------------------------------------------------
// GuestService.CreateGuest tests
// ---------------------------------------------------------------------------

func TestCreateGuest_AssignsPendingStatus(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID {
			return pendingID
		},
	}

	svc := newGuestService(repo, nil, nil, nil)

	guest := &models.Guest{
		EventID:       eventID,
		FirstName:     "Alice",
		GuestStatusID: uuid.Nil, // not set — service must assign pending
	}

	err := svc.CreateGuest(guest)

	require.NoError(t, err)
	assert.Equal(t, pendingID, guest.GuestStatusID,
		"GuestStatusID should be set to the pending status ID when uuid.Nil is passed")
	assert.True(t, repo.createGuestCalled, "CreateGuest on repo must be called")
}

func TestCreateGuest_DoesNotOverrideExistingStatus(t *testing.T) {
	// If GuestStatusID is already set (not uuid.Nil), it must NOT be overwritten
	pendingID := uuid.Must(uuid.NewV4())
	customStatusID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID {
			return pendingID
		},
	}

	svc := newGuestService(repo, nil, nil, nil)

	guest := &models.Guest{
		EventID:       eventID,
		FirstName:     "Bob",
		GuestStatusID: customStatusID, // already set
	}

	err := svc.CreateGuest(guest)

	require.NoError(t, err)
	assert.Equal(t, customStatusID, guest.GuestStatusID,
		"GuestStatusID should remain unchanged when it is already set")
}

func TestCreateGuest_IgnoresCacheInvalidationError(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	repo := &mockGuestRepo{}
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			assert.Equal(t, "all:"+eventID.String()+":guests", pattern)
			return errors.New("redis unavailable")
		},
	}

	svc := NewGuestService(repo, nil, cache, nil)
	err := svc.CreateGuest(&models.Guest{
		EventID:       eventID,
		FirstName:     "Ana",
		GuestStatusID: statusID,
	})

	require.NoError(t, err)
	assert.True(t, repo.createGuestCalled)
}

func TestCreateGuest_SetsIsHostForPublicHostRoleAlias(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID { return pendingID },
	}

	svc := newGuestService(repo, nil, nil, nil)
	guest := &models.Guest{
		EventID:   eventID,
		FirstName: "Ana",
		Role:      "co-host",
	}

	err := svc.CreateGuest(guest)

	require.NoError(t, err)
	assert.True(t, guest.IsHost)
}

func TestUpdateGuest_UpdatesLinkedInvitationMaxGuests(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, InvitationID: &invitationID}, nil
		},
	}
	var updatedInvitation *models.Invitation
	invitations := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			require.Equal(t, invitationID, id)
			return &models.Invitation{ID: id, EventID: eventID, MaxGuests: 2}, nil
		},
		UpdateInvitationFunc: func(inv *models.Invitation) error {
			updatedInvitation = inv
			return nil
		},
	}

	svc := newGuestService(repo, nil, nil, nil, invitations)
	guest := &models.Guest{
		ID:           guestID,
		EventID:      eventID,
		InvitationID: &invitationID,
		FirstName:    "Ana",
		MaxGuests:    5,
	}

	err := svc.UpdateGuest(guest)

	require.NoError(t, err)
	require.NotNil(t, updatedInvitation)
	assert.Equal(t, invitationID, updatedInvitation.ID)
	assert.Equal(t, 5, updatedInvitation.MaxGuests)
}

func TestUpdateGuest_NormalizesRSVPStatusAndSetsHostMetadata(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	oldRSVPAt := time.Now().Add(-24 * time.Hour)
	beforeUpdate := time.Now()

	var captured *models.Guest
	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:         id,
				EventID:    eventID,
				FirstName:  "Ana",
				RSVPStatus: "pending",
				RSVPAt:     &oldRSVPAt,
			}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			captured = g
			return nil
		},
	}

	svc := newGuestService(repo, nil, nil, nil)
	guest := &models.Guest{
		ID:         guestID,
		EventID:    eventID,
		FirstName:  "Ana",
		RSVPStatus: " CONFIRMED ",
	}

	err := svc.UpdateGuest(guest)

	require.NoError(t, err)
	require.NotNil(t, captured)
	require.NotNil(t, captured.RSVPAt)
	assert.Equal(t, "confirmed", captured.RSVPStatus)
	assert.Equal(t, "host", captured.RSVPMethod)
	assert.True(t, captured.RSVPAt.After(beforeUpdate) || captured.RSVPAt.Equal(beforeUpdate))
}

func TestUpdateGuest_SetsIsHostForPublicHostRoleAlias(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	var captured *models.Guest
	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, FirstName: "Ana"}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			captured = g
			return nil
		},
	}

	svc := newGuestService(repo, nil, nil, nil)
	err := svc.UpdateGuest(&models.Guest{
		ID:        guestID,
		EventID:   eventID,
		FirstName: "Ana",
		Role:      "Anfitri\u00f3n",
	})

	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.True(t, captured.IsHost)
}

func TestUpdateGuest_AllowsNilCache(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	var updated bool

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, FirstName: "Ana"}, nil
		},
		UpdateGuestFunc: func(g *models.Guest) error {
			updated = true
			return nil
		},
	}

	svc := NewGuestService(repo, nil, nil, nil)
	err := svc.UpdateGuest(&models.Guest{
		ID:        guestID,
		EventID:   eventID,
		FirstName: "Ana",
	})

	require.NoError(t, err)
	assert.True(t, updated)
}

func TestUpdateGuest_IgnoresCacheInvalidationError(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	var invalidatedGuest bool
	var invalidatedEvent bool

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, FirstName: "Ana"}, nil
		},
	}
	cache := &mockCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			invalidatedGuest = true
			assert.Equal(t, "guests", resource)
			assert.Equal(t, guestID.String(), key)
			return errors.New("redis unavailable")
		},
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			invalidatedEvent = true
			assert.Equal(t, "all:"+eventID.String()+":guests", pattern)
			return errors.New("redis unavailable")
		},
	}

	svc := NewGuestService(repo, nil, cache, nil)
	err := svc.UpdateGuest(&models.Guest{
		ID:        guestID,
		EventID:   eventID,
		FirstName: "Ana",
	})

	require.NoError(t, err)
	assert.True(t, invalidatedGuest)
	assert.True(t, invalidatedEvent)
}

func TestUpdateGuest_AdjustsAnalyticsFromPendingToConfirmed(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	analyticsRepo := &mockEventAnalyticsRepo{
		current: &models.EventAnalytics{EventID: eventID},
	}
	defer withAnalyticsService(analyticsRepo, nil)()

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, FirstName: "Ana", RSVPStatus: "pending"}, nil
		},
	}
	svc := newGuestService(repo, nil, nil, nil)

	err := svc.UpdateGuest(&models.Guest{
		ID:         guestID,
		EventID:    eventID,
		FirstName:  "Ana",
		RSVPStatus: "confirmed",
	})

	require.NoError(t, err)
	require.NotNil(t, analyticsRepo.updated)
	assert.Equal(t, 1, analyticsRepo.updated.RSVPConfirmed)
	assert.Equal(t, 0, analyticsRepo.updated.RSVPDeclined)
}

func TestUpdateGuest_AdjustsAnalyticsFromConfirmedToDeclined(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	analyticsRepo := &mockEventAnalyticsRepo{
		current: &models.EventAnalytics{
			EventID:       eventID,
			RSVPConfirmed: 3,
			RSVPDeclined:  1,
		},
	}
	defer withAnalyticsService(analyticsRepo, nil)()

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, FirstName: "Ana", RSVPStatus: "confirmed"}, nil
		},
	}
	svc := newGuestService(repo, nil, nil, nil)

	err := svc.UpdateGuest(&models.Guest{
		ID:         guestID,
		EventID:    eventID,
		FirstName:  "Ana",
		RSVPStatus: "declined",
	})

	require.NoError(t, err)
	require.NotNil(t, analyticsRepo.updated)
	assert.Equal(t, 2, analyticsRepo.updated.RSVPConfirmed)
	assert.Equal(t, 2, analyticsRepo.updated.RSVPDeclined)
}

func TestUpdateGuest_DoesNotDoubleCountAnalyticsForSameRSVPStatus(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	analyticsRepo := &mockEventAnalyticsRepo{
		current: &models.EventAnalytics{
			EventID:       eventID,
			RSVPConfirmed: 3,
			RSVPDeclined:  1,
		},
	}
	defer withAnalyticsService(analyticsRepo, nil)()

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, FirstName: "Ana", RSVPStatus: "confirmed"}, nil
		},
	}
	svc := newGuestService(repo, nil, nil, nil)

	err := svc.UpdateGuest(&models.Guest{
		ID:         guestID,
		EventID:    eventID,
		FirstName:  "Ana",
		RSVPStatus: "confirmed",
	})

	require.NoError(t, err)
	assert.Nil(t, analyticsRepo.updated)
	assert.Nil(t, analyticsRepo.created)
}

// ---------------------------------------------------------------------------
// GuestService.CreateGuests (batch) tests
// ---------------------------------------------------------------------------

func TestCreateGuests_BatchAssignsPendingStatus(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID {
			return pendingID
		},
	}

	svc := newGuestService(repo, nil, nil, nil)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Carol", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Dave", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Eve", GuestStatusID: uuid.Nil},
	}

	err := svc.CreateGuests(guests)

	require.NoError(t, err)

	// Verify all guests in the batch received the pending status
	for i, g := range guests {
		assert.Equal(t, pendingID, g.GuestStatusID,
			"guest at index %d should have pending status ID", i)
	}

	assert.True(t, repo.createGuestsCalled, "CreateGuests on repo must be called")
}

func TestCreateGuests_BatchDoesNotOverrideExistingStatus(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	existingStatusID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID {
			return pendingID
		},
	}

	svc := newGuestService(repo, nil, nil, nil)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Frank", GuestStatusID: uuid.Nil},         // should get pending
		{EventID: eventID, FirstName: "Grace", GuestStatusID: existingStatusID}, // should keep existing
	}

	err := svc.CreateGuests(guests)

	require.NoError(t, err)
	assert.Equal(t, pendingID, guests[0].GuestStatusID,
		"guest with uuid.Nil status should get pending ID")
	assert.Equal(t, existingStatusID, guests[1].GuestStatusID,
		"guest with existing status ID should keep it")
}

func TestCreateGuests_EmptySlice_ReturnsNoError(t *testing.T) {
	svc := newGuestService(nil, nil, nil, nil)

	err := svc.CreateGuests([]models.Guest{})

	require.NoError(t, err, "empty guest slice should return nil error")
}

func TestDeleteGuest_IgnoresCacheInvalidationError(t *testing.T) {
	guestID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	var deleted bool

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID}, nil
		},
		DeleteGuestFunc: func(id uuid.UUID) error {
			deleted = true
			return nil
		},
	}
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			assert.True(t, deleted, "cache should be invalidated after DB delete succeeds")
			assert.Equal(t, "all:"+eventID.String()+":guests", pattern)
			return errors.New("redis unavailable")
		},
	}

	svc := NewGuestService(repo, nil, cache, nil)
	err := svc.DeleteGuest(guestID)

	require.NoError(t, err)
	assert.True(t, deleted)
}

// ---------------------------------------------------------------------------
// GuestService.CreateGuestsWithInvitations — status assignment tests
// (We skip the actual transaction execution; we test preparation logic only)
// ---------------------------------------------------------------------------

func TestCreateGuestsWithInvitations_AssignsPendingStatus(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	// Capture the guests state after the service mutates them but before the tx
	var capturedGuests []models.Guest

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID {
			return pendingID
		},
	}
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(eventID uuid.UUID, length int) (string, error) {
			return uuid.Must(uuid.NewV4()).String()[:8], nil
		},
	}
	tx := &mockTransactor{
		TransactionFunc: func(fn func(tx *gorm.DB) error) error {
			// We don't execute fn (no real DB), just signal success
			return nil
		},
	}

	svc := newGuestService(repo, accessToken, nil, tx)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Heidi", GuestStatusID: uuid.Nil},
	}

	// We need to capture the state of guests[0].GuestStatusID after service mutation.
	// The service mutates the slice in-place before calling tx.Transaction.
	// Since our mockTransactor.TransactionFunc is a no-op, we check the slice after the call.
	err := svc.CreateGuestsWithInvitations(guests)

	require.NoError(t, err)

	// guests slice was mutated in-place by the service
	assert.Equal(t, pendingID, guests[0].GuestStatusID,
		"GuestStatusID must be set to pending when passed as uuid.Nil")

	_ = capturedGuests // silence "declared and not used" if not needed
}

func TestCreateGuestsWithInvitations_BatchAssignsPendingStatus(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	counter := 0
	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID {
			return pendingID
		},
	}
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(eID uuid.UUID, length int) (string, error) {
			counter++
			// Return a unique token per invocation to avoid infinite loop in service
			return uuid.Must(uuid.NewV4()).String()[:8], nil
		},
	}
	tx := &mockTransactor{
		TransactionFunc: func(fn func(tx *gorm.DB) error) error {
			return nil // skip DB
		},
	}

	svc := newGuestService(repo, accessToken, nil, tx)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Ivan", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Judy", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Karl", GuestStatusID: uuid.Nil},
	}

	err := svc.CreateGuestsWithInvitations(guests)

	require.NoError(t, err)

	for i, g := range guests {
		assert.Equal(t, pendingID, g.GuestStatusID,
			"guest at index %d must receive pending status ID", i)
	}
}

func TestCreateGuestsWithInvitations_EmptySlice_ReturnsNoError(t *testing.T) {
	svc := newGuestService(nil, nil, nil, nil)

	err := svc.CreateGuestsWithInvitations([]models.Guest{})

	require.NoError(t, err, "empty guest slice should be a no-op")
}

func TestCreateGuestsWithInvitations_SetsInvitationIDOnGuests(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID { return pendingID },
	}
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(eID uuid.UUID, length int) (string, error) {
			return uuid.Must(uuid.NewV4()).String()[:8], nil
		},
	}
	tx := &mockTransactor{
		TransactionFunc: func(fn func(tx *gorm.DB) error) error { return nil },
	}

	svc := newGuestService(repo, accessToken, nil, tx)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Laura", GuestStatusID: uuid.Nil},
	}

	err := svc.CreateGuestsWithInvitations(guests)

	require.NoError(t, err)
	assert.NotNil(t, guests[0].InvitationID,
		"InvitationID must be set by CreateGuestsWithInvitations")
	assert.NotEqual(t, uuid.Nil, *guests[0].InvitationID,
		"InvitationID must be a valid (non-nil) UUID")
}

func TestCreateGuestsWithInvitations_AttachesPrettyTokenToGuests(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID { return pendingID },
	}
	generatedTokens := []string{"TOKEN001", "TOKEN002"}
	tokenIndex := 0
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(eID uuid.UUID, length int) (string, error) {
			token := generatedTokens[tokenIndex]
			tokenIndex++
			return token, nil
		},
	}
	tx := &mockTransactor{
		TransactionFunc: func(fn func(tx *gorm.DB) error) error { return nil },
	}

	svc := newGuestService(repo, accessToken, nil, tx)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Leo", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Mia", GuestStatusID: uuid.Nil},
	}

	err := svc.CreateGuestsWithInvitations(guests)

	require.NoError(t, err)
	require.Equal(t, len(generatedTokens), tokenIndex)
	assert.Equal(t, "TOKEN001", guests[0].PrettyToken)
	assert.Equal(t, "TOKEN002", guests[1].PrettyToken)
}

func TestCreateGuestsWithInvitations_ReturnsPrettyTokenGenerationError(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	expectedErr := errors.New("token lookup failed")
	txCalled := false

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID { return pendingID },
	}
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(eID uuid.UUID, length int) (string, error) {
			return "", expectedErr
		},
	}
	tx := &mockTransactor{
		TransactionFunc: func(fn func(tx *gorm.DB) error) error {
			txCalled = true
			return nil
		},
	}

	svc := newGuestService(repo, accessToken, nil, tx)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Noa", GuestStatusID: uuid.Nil},
	}

	err := svc.CreateGuestsWithInvitations(guests)

	require.ErrorIs(t, err, expectedErr)
	assert.False(t, txCalled, "transaction should not start when token generation fails")
}

func TestCreateGuestsWithInvitations_UsesGuestsCountAsMaxGuestsFallback(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID { return pendingID },
	}
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(eID uuid.UUID, length int) (string, error) {
			return uuid.Must(uuid.NewV4()).String()[:8], nil
		},
	}
	tx := &mockTransactor{
		TransactionFunc: func(fn func(tx *gorm.DB) error) error { return nil },
	}

	svc := newGuestService(repo, accessToken, nil, tx)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Lola", GuestsCount: 4},
		{EventID: eventID, FirstName: "Nia", RSVPGuestCount: 3},
		{EventID: eventID, FirstName: "Mara"},
	}

	err := svc.CreateGuestsWithInvitations(guests)

	require.NoError(t, err)
	assert.Equal(t, 4, guests[0].MaxGuests)
	assert.Equal(t, 3, guests[1].GuestsCount)
	assert.Equal(t, 3, guests[1].MaxGuests)
	assert.Equal(t, 1, guests[2].MaxGuests)
}

func TestCreateGuestsWithInvitations_UsesGuestsCountAsInitialRSVPGuestCount(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID { return pendingID },
	}
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(eID uuid.UUID, length int) (string, error) {
			return uuid.Must(uuid.NewV4()).String()[:8], nil
		},
	}
	tx := &mockTransactor{
		TransactionFunc: func(fn func(tx *gorm.DB) error) error { return nil },
	}

	svc := newGuestService(repo, accessToken, nil, tx)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Lola", GuestsCount: 4},
		{EventID: eventID, FirstName: "Mara", GuestsCount: 2, RSVPGuestCount: 3},
		{EventID: eventID, FirstName: "Nia", RSVPGuestCount: 5},
		{EventID: eventID, FirstName: "Nico"},
	}

	err := svc.CreateGuestsWithInvitations(guests)

	require.NoError(t, err)
	assert.Equal(t, 4, guests[0].RSVPGuestCount)
	assert.Equal(t, 3, guests[1].RSVPGuestCount)
	assert.Equal(t, 5, guests[2].GuestsCount)
	assert.Equal(t, 5, guests[2].RSVPGuestCount)
	assert.Equal(t, 0, guests[3].RSVPGuestCount)
}

func TestCreateGuestsWithInvitations_SetsIsHostForPublicHostRoleAliases(t *testing.T) {
	pendingID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	repo := &mockGuestRepo{
		GetPendingStatusIDFunc: func() uuid.UUID { return pendingID },
	}
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(eID uuid.UUID, length int) (string, error) {
			return uuid.Must(uuid.NewV4()).String()[:8], nil
		},
	}
	tx := &mockTransactor{
		TransactionFunc: func(fn func(tx *gorm.DB) error) error { return nil },
	}

	svc := newGuestService(repo, accessToken, nil, tx)

	guests := []models.Guest{
		{EventID: eventID, FirstName: "Mike", Role: "host", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Nina", Role: "Host", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Sofia", Role: "co-host", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Mateo", Role: "Anfitri\u00f3n", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Omar", Role: "guest", GuestStatusID: uuid.Nil},
	}

	err := svc.CreateGuestsWithInvitations(guests)

	require.NoError(t, err)
	assert.True(t, guests[0].IsHost,
		"guest with Role=host must have IsHost=true")
	assert.True(t, guests[1].IsHost,
		"guest with Role=Host must have IsHost=true")
	assert.True(t, guests[2].IsHost,
		"guest with Role=co-host must have IsHost=true")
	assert.True(t, guests[3].IsHost,
		"guest with Role=Anfitrion must have IsHost=true")
	assert.False(t, guests[4].IsHost,
		"guest with Role!=host must have IsHost=false")
}

func TestBulkDeleteGuests_UsesInjectedRepository(t *testing.T) {
	repo := &mockGuestRepo{}
	svc := newGuestService(repo, nil, nil, nil)
	ids := []uuid.UUID{uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())}

	err := svc.BulkDeleteGuests(ids)

	require.NoError(t, err)
	assert.True(t, repo.bulkDeleteCalled, "BulkDeleteGuests on repo must be called")
	assert.Equal(t, ids, repo.bulkDeletedIDs)
}

func TestBulkDeleteGuests_InvalidatesEachEventCacheAfterDelete(t *testing.T) {
	eventA := uuid.Must(uuid.NewV4())
	eventB := uuid.Must(uuid.NewV4())
	idA := uuid.Must(uuid.NewV4())
	idB := uuid.Must(uuid.NewV4())
	idC := uuid.Must(uuid.NewV4())
	guestsByID := map[uuid.UUID]*models.Guest{
		idA: {ID: idA, EventID: eventA},
		idB: {ID: idB, EventID: eventA},
		idC: {ID: idC, EventID: eventB},
	}

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return guestsByID[id], nil
		},
	}
	deleted := false
	var invalidated []string
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			assert.True(t, deleted, "cache should be invalidated after DB delete succeeds")
			invalidated = append(invalidated, pattern)
			return nil
		},
	}
	repo.BulkDeleteGuestsFunc = func(ids []uuid.UUID) error {
		deleted = true
		return nil
	}

	svc := newGuestService(repo, nil, cache, nil)
	err := svc.BulkDeleteGuests([]uuid.UUID{idA, idB, idC})

	require.NoError(t, err)
	assert.True(t, repo.bulkDeleteCalled)
	assert.ElementsMatch(t, []string{
		"all:" + eventA.String() + ":guests",
		"all:" + eventB.String() + ":guests",
	}, invalidated)
}

func TestBulkDeleteGuests_IgnoresCacheInvalidationError(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	guestsByID := map[uuid.UUID]*models.Guest{
		guestID: {ID: guestID, EventID: eventID},
	}

	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return guestsByID[id], nil
		},
	}
	deleted := false
	cache := &mockCacheRepo{
		DeleteKeysByPatternFunc: func(ctx context.Context, pattern string) error {
			assert.True(t, deleted, "cache should be invalidated after DB delete succeeds")
			assert.Equal(t, "all:"+eventID.String()+":guests", pattern)
			return errors.New("redis unavailable")
		},
	}
	repo.BulkDeleteGuestsFunc = func(ids []uuid.UUID) error {
		deleted = true
		return nil
	}

	svc := NewGuestService(repo, nil, cache, nil)
	err := svc.BulkDeleteGuests([]uuid.UUID{guestID})

	require.NoError(t, err)
	assert.True(t, repo.bulkDeleteCalled)
}
