package invitations

import (
	"context"
	"errors"
	"events-stocks/models"
	"events-stocks/services/ports"
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
	GetByTokenFunc        func(token string) (*models.InvitationAccessToken, error)
	GetByPrettyTokenFunc  func(code string) (*models.InvitationAccessToken, error)
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
	CreateInvitationLogFunc     func(m *models.InvitationLog) error
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
	InvalidateFunc           func(resource string, key string) error
	DeleteKeysByPatternFunc  func(ctx context.Context, pattern string) error
	GetKeyFunc               func(ctx context.Context, key string) (string, error)
	SaveKeyFunc              func(ctx context.Context, key string, value string, ttl time.Duration) error
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
	guest, err := svc.ConfirmRSVP("BAD-TOKEN", "confirmed", "web", 1)

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
	guest, err := svc.ConfirmRSVP("MISSING", "confirmed", "web", 1)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired token")
	assert.Nil(t, guest)
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
	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 1)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invitation not found")
	assert.Nil(t, guest)
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
	guest, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 5)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds allowed max")
	assert.Nil(t, guest)
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
	result, err := svc.ConfirmRSVP("VALID-TOKEN", "confirmed", "web", 4)

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
	result, err := svc.ConfirmRSVP("MY-TOKEN", "confirmed", "web", 2)

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

func TestGetInvitationByToken_Success(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	future := time.Now().Add(24 * time.Hour)

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
				MaxGuests: 4,
			}, nil
		},
	}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
			return &models.Guest{
				ID:           guestID,
				InvitationID: &invitationID,
				FirstName:    "John",
				LastName:     "Doe",
			}, nil
		},
	}

	svc := newService(invRepo, guestRepo, tokenRepo, nil, nil)
	result, err := svc.GetInvitationByToken("valid-token-uuid")

	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, invitationID, result.Invitation.ID)
	assert.Equal(t, guestID, result.Guest.ID)
	assert.Equal(t, "NICE1234", result.PrettyToken)
	assert.Equal(t, "John", result.Guest.FirstName)
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
