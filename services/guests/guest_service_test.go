package guests

import (
	"context"
	"events-stocks/models"
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
	CreateGuestFunc            func(m *models.Guest) error
	UpdateGuestFunc            func(m *models.Guest) error
	DeleteGuestFunc            func(id uuid.UUID) error
	GetGuestByIDFunc           func(id uuid.UUID) (*models.Guest, error)
	GetGuestByInvitationIDFunc func(invitationID uuid.UUID) (*models.Guest, error)
	CreateGuestsFunc           func(guests []models.Guest) error
	GetPendingStatusIDFunc     func() uuid.UUID

	// track calls for assertions
	createGuestCalled  bool
	createGuestsCalled bool
	createdGuests      []models.Guest
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
	return NewGuestService(repo, accessToken, cache, tx)
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
		{EventID: eventID, FirstName: "Frank", GuestStatusID: uuid.Nil},        // should get pending
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

func TestCreateGuestsWithInvitations_SetsIsHostForHostRole(t *testing.T) {
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
		{EventID: eventID, FirstName: "Mike", Role: "Host", GuestStatusID: uuid.Nil},
		{EventID: eventID, FirstName: "Nina", Role: "Guest", GuestStatusID: uuid.Nil},
	}

	err := svc.CreateGuestsWithInvitations(guests)

	require.NoError(t, err)
	assert.True(t, guests[0].IsHost,
		"guest with Role=Host must have IsHost=true")
	assert.False(t, guests[1].IsHost,
		"guest with Role!=Host must have IsHost=false")
}
