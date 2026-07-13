package publicaccess

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"events-stocks/internal/previewtoken"
	"events-stocks/internal/publicaccessproof"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockConfigRepo struct {
	cfg *models.EventConfig
	err error
}

func (m *mockConfigRepo) CreateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockConfigRepo) UpdateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockConfigRepo) DeleteEventConfig(id uuid.UUID) error            { return nil }
func (m *mockConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	return m.cfg, m.err
}

var _ ports.EventConfigRepository = (*mockConfigRepo)(nil)

type mockTokenRepo struct {
	token      *models.InvitationAccessToken
	err        error
	pretty     *models.InvitationAccessToken
	prettyErr  error
	seen       string
	prettySeen string
}

func (m *mockTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	m.seen = token
	return m.token, m.err
}
func (m *mockTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	m.prettySeen = code
	return m.pretty, m.prettyErr
}
func (m *mockTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABC123", nil
}

var _ ports.AccessTokenRepository = (*mockTokenRepo)(nil)

type mockInvitationRepo struct {
	invitation *models.Invitation
}

func (m *mockInvitationRepo) CreateInvitation(invitation *models.Invitation) error { return nil }
func (m *mockInvitationRepo) UpdateInvitation(invitation *models.Invitation) error { return nil }
func (m *mockInvitationRepo) DeleteInvitation(id uuid.UUID) error                  { return nil }
func (m *mockInvitationRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, nil
}
func (m *mockInvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, nil
}
func (m *mockInvitationRepo) ListInvitations() ([]models.Invitation, error) { return nil, nil }
func (m *mockInvitationRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return nil, nil
}

var _ ports.InvitationRepository = (*mockInvitationRepo)(nil)

func TestAllowEventRead_AllowsPublicEvents(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: true}},
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventReadWithConfigReturnsLoadedConfig(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{ID: eventID, IsPublic: true, VisibilityConfigured: true}

	result, err := AllowEventReadWithConfig(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: cfg},
	})

	require.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.Same(t, cfg, result.Config)
}

func TestAllowEventReadWithConfigAppliesLegacyVisibilityDefaults(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{ID: eventID, IsPublic: true}

	result, err := AllowEventReadWithConfig(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: cfg},
	})

	require.NoError(t, err)
	require.NotNil(t, result.Config)
	assert.True(t, result.Allowed)
	assert.NotSame(t, cfg, result.Config)
	assert.True(t, result.Config.ShowHeader)
	assert.True(t, result.Config.ShowFooter)
	assert.True(t, result.Config.ShowPhotoGallery)
	assert.True(t, result.Config.ShowHostsSection)
}

func TestAllowEventReadWithConfigPreservesConfiguredVisibility(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{
		ID:                   eventID,
		IsPublic:             true,
		VisibilityConfigured: true,
		ShowHeader:           false,
		ShowFooter:           false,
		ShowPhotoGallery:     false,
	}

	result, err := AllowEventReadWithConfig(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: cfg},
	})

	require.NoError(t, err)
	require.NotNil(t, result.Config)
	assert.True(t, result.Allowed)
	assert.Same(t, cfg, result.Config)
	assert.False(t, result.Config.ShowHeader)
	assert.False(t, result.Config.ShowFooter)
	assert.False(t, result.Config.ShowPhotoGallery)
}

func TestAllowEventRead_AllowsPasswordProtectedPublicEventWhenProofNotRequired(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{
			ID:                  eventID,
			IsPublic:            true,
			AuthPasswordPreview: "secreto",
		}},
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_TreatsBlankPasswordPreviewAsUnprotected(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{
			ID:                  eventID,
			IsPublic:            true,
			AuthPasswordPreview: "   ",
		}},
		RequirePasswordProof: true,
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_BlocksPasswordProtectedPublicEventWithoutProofWhenRequired(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{
			ID:                  eventID,
			IsPublic:            true,
			AuthPasswordPreview: "secreto",
		}},
		RequirePasswordProof: true,
	})

	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowEventRead_AllowsPasswordProtectedPublicEventWithProofWhenRequired(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{
		ID:                  eventID,
		IsPublic:            true,
		AuthPasswordPreview: "secreto",
	}
	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Hour)
	require.NoError(t, err)

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo:           &mockConfigRepo{cfg: cfg},
		RequirePasswordProof: true,
		PasswordProofToken:   proof,
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_AllowsPreviewForPasswordProtectedEventWhenProofRequired(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())
	token, err := previewtoken.Generate(eventID, time.Minute)
	require.NoError(t, err)

	allowed, err := AllowEventRead(eventID, token, "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{
			ID:                  eventID,
			IsPublic:            true,
			AuthPasswordPreview: "secreto",
		}},
		RequirePasswordProof: true,
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_BlocksInactivePublicEventsWithoutPreviewToken(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: true}},
		IsEventActive: func(id uuid.UUID) (bool, error) {
			assert.Equal(t, eventID, id)
			return false, nil
		},
	})

	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowEventRead_AllowsInactiveEventWithPreviewToken(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())
	token, err := previewtoken.Generate(eventID, time.Minute)
	require.NoError(t, err)

	allowed, err := AllowEventRead(eventID, token, "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: true}},
		IsEventActive: func(id uuid.UUID) (bool, error) {
			assert.Equal(t, eventID, id)
			return false, nil
		},
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_BlocksInactiveEventWithInvitationToken(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())

	allowed, err := AllowEventRead(eventID, "", "invite-token", EventReadDeps{
		ConfigRepo:     &mockConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
		TokenRepo:      &mockTokenRepo{token: &models.InvitationAccessToken{InvitationID: invitationID}},
		InvitationRepo: &mockInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
		IsEventActive: func(id uuid.UUID) (bool, error) {
			return false, nil
		},
	})

	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowEventRead_BlocksPublicEventBeforeActiveFrom(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC)
	activeFrom := now.Add(time.Hour)

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: true, ActiveFrom: activeFrom}},
		Now:        func() time.Time { return now },
	})

	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowEventRead_BlocksPublicEventAfterActiveUntil(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC)
	activeUntil := now.Add(-time.Hour)

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: true, ActiveUntil: &activeUntil}},
		Now:        func() time.Time { return now },
	})

	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowEventRead_AllowsPublicEventInsideActiveWindow(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC)
	activeFrom := now.Add(-time.Hour)
	activeUntil := now.Add(time.Hour)

	allowed, err := AllowEventRead(eventID, "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{
			IsPublic:    true,
			ActiveFrom:  activeFrom,
			ActiveUntil: &activeUntil,
		}},
		Now: func() time.Time { return now },
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_AllowsPreviewOutsideActiveWindow(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC)
	activeFrom := now.Add(time.Hour)
	token, err := previewtoken.Generate(eventID, time.Minute)
	require.NoError(t, err)

	allowed, err := AllowEventRead(eventID, token, "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: true, ActiveFrom: activeFrom}},
		Now:        func() time.Time { return now },
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_BlocksInvitationTokenOutsideActiveWindow(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC)
	activeFrom := now.Add(time.Hour)
	tokenRepo := &mockTokenRepo{token: &models.InvitationAccessToken{InvitationID: invitationID}}

	allowed, err := AllowEventRead(eventID, "", "invite-token", EventReadDeps{
		ConfigRepo:     &mockConfigRepo{cfg: &models.EventConfig{IsPublic: false, ActiveFrom: activeFrom}},
		TokenRepo:      tokenRepo,
		InvitationRepo: &mockInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
		Now:            func() time.Time { return now },
	})

	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Empty(t, tokenRepo.seen)
}

func TestAllowEventRead_AllowsPreviewToken(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())
	token, err := previewtoken.Generate(eventID, time.Minute)
	require.NoError(t, err)

	allowed, err := AllowEventRead(eventID, token, "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventReadFromRequestReadsPreviewAliases(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-secret")
	eventID := uuid.Must(uuid.NewV4())
	token, err := previewtoken.Generate(eventID, time.Minute)
	require.NoError(t, err)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/events/demo/page-spec?previewToken="+url.QueryEscape(token), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	allowed, err := AllowEventReadFromRequest(c, eventID, EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
	})

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_AllowsMatchingInvitationToken(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockTokenRepo{token: &models.InvitationAccessToken{InvitationID: invitationID}}

	allowed, err := AllowEventRead(eventID, "", "invite-token", EventReadDeps{
		ConfigRepo:     &mockConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
		TokenRepo:      tokenRepo,
		InvitationRepo: &mockInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
	})

	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, "invite-token", tokenRepo.seen)
}

func TestAllowEventRead_UsesInjectedClockForInvitationTokenExpiry(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Minute)
	tokenRepo := &mockTokenRepo{token: &models.InvitationAccessToken{
		InvitationID: invitationID,
		ExpiresAt:    &expiresAt,
	}}

	allowed, err := AllowEventRead(eventID, "", "invite-token", EventReadDeps{
		ConfigRepo:     &mockConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
		TokenRepo:      tokenRepo,
		InvitationRepo: &mockInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
		Now:            func() time.Time { return now },
	})

	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, "invite-token", tokenRepo.seen)
}

func TestAllowEventRead_AllowsPrettyInvitationTokenFallback(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockTokenRepo{
		err:    errors.New("raw token not found"),
		pretty: &models.InvitationAccessToken{InvitationID: invitationID},
	}

	allowed, err := AllowEventRead(eventID, "", "PRETTY123", EventReadDeps{
		ConfigRepo:     &mockConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
		TokenRepo:      tokenRepo,
		InvitationRepo: &mockInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
	})

	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, "PRETTY123", tokenRepo.seen)
	assert.Equal(t, "PRETTY123", tokenRepo.prettySeen)
}

func TestAllowEventRead_DeniesExpiredInvitationToken(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)

	allowed, err := AllowEventRead(eventID, "", "expired-token", EventReadDeps{
		ConfigRepo: &mockConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
		TokenRepo: &mockTokenRepo{token: &models.InvitationAccessToken{
			InvitationID: uuid.Must(uuid.NewV4()),
			ExpiresAt:    &expired,
		}},
		InvitationRepo: &mockInvitationRepo{invitation: &models.Invitation{EventID: eventID}},
		Now:            func() time.Time { return now },
	})

	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowEventRead_ReturnsConfigErrors(t *testing.T) {
	allowed, err := AllowEventRead(uuid.Must(uuid.NewV4()), "", "", EventReadDeps{
		ConfigRepo: &mockConfigRepo{err: errors.New("boom")},
	})

	assert.False(t, allowed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}
