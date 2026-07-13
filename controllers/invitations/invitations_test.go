package invitations

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	invitationsService "events-stocks/services/invitations"
	"events-stocks/services/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newEchoCtx(method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	e.Validator = customValidator.New()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func setAuthzContext(t *testing.T, c echo.Context, eventID uuid.UUID, invitation *models.Invitation, invitationErr error) {
	t.Helper()
	c.Set("cognito_sub", "test-sub")
	if eventID == uuid.Nil {
		eventID = uuid.Must(uuid.NewV4())
	}
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id}, nil
		},
		GetInvitationByIDLite: func(id uuid.UUID) (*models.Invitation, error) {
			if invitationErr != nil {
				return nil, invitationErr
			}
			if invitation != nil {
				if invitation.EventID == uuid.Nil {
					copy := *invitation
					copy.EventID = eventID
					return &copy, nil
				}
				return invitation, nil
			}
			return &models.Invitation{ID: id, EventID: eventID, MaxGuests: 10}, nil
		},
	})
	t.Cleanup(restore)
}

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockInvRepo struct {
	GetInvitationByIDFunc     func(id uuid.UUID) (*models.Invitation, error)
	GetInvitationByIDLiteFunc func(id uuid.UUID) (*models.Invitation, error)
	ListByEventIDFunc         func(eventID uuid.UUID) ([]models.Invitation, error)
}

func (m *mockInvRepo) CreateInvitation(obj *models.Invitation) error { return nil }
func (m *mockInvRepo) UpdateInvitation(obj *models.Invitation) error { return nil }
func (m *mockInvRepo) DeleteInvitation(id uuid.UUID) error           { return nil }
func (m *mockInvRepo) ListInvitations() ([]models.Invitation, error) { return nil, nil }
func (m *mockInvRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	if m.GetInvitationByIDFunc != nil {
		return m.GetInvitationByIDFunc(id)
	}
	return &models.Invitation{ID: id, MaxGuests: 10}, nil
}
func (m *mockInvRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	if m.GetInvitationByIDLiteFunc != nil {
		return m.GetInvitationByIDLiteFunc(id)
	}
	return &models.Invitation{ID: id, MaxGuests: 10}, nil
}
func (m *mockInvRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	if m.ListByEventIDFunc != nil {
		return m.ListByEventIDFunc(eventID)
	}
	return []models.Invitation{}, nil
}

var _ ports.InvitationRepository = (*mockInvRepo)(nil)

type mockGuestRepo struct {
	GetGuestByInvitationIDFunc func(invID uuid.UUID) (*models.Guest, error)
	UpdateGuestFunc            func(g *models.Guest) error
}

func (m *mockGuestRepo) CreateGuest(obj *models.Guest) error { return nil }
func (m *mockGuestRepo) UpdateGuest(g *models.Guest) error {
	if m.UpdateGuestFunc != nil {
		return m.UpdateGuestFunc(g)
	}
	return nil
}
func (m *mockGuestRepo) DeleteGuest(id uuid.UUID) error                   { return nil }
func (m *mockGuestRepo) GetGuestByID(id uuid.UUID) (*models.Guest, error) { return nil, nil }
func (m *mockGuestRepo) CreateGuests(guests []models.Guest) error         { return nil }
func (m *mockGuestRepo) BulkDeleteGuests(ids []uuid.UUID) error           { return nil }
func (m *mockGuestRepo) ListGuestsByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return nil, nil
}
func (m *mockGuestRepo) GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error) {
	return dtos.GuestSummary{}, nil
}
func (m *mockGuestRepo) ListAttendeesByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	return nil, nil
}
func (m *mockGuestRepo) GetPendingStatusID() uuid.UUID { return uuid.Nil }
func (m *mockGuestRepo) GetGuestByInvitationID(invID uuid.UUID) (*models.Guest, error) {
	if m.GetGuestByInvitationIDFunc != nil {
		return m.GetGuestByInvitationIDFunc(invID)
	}
	return &models.Guest{ID: uuid.Must(uuid.NewV4())}, nil
}

var _ ports.GuestRepository = (*mockGuestRepo)(nil)

type mockTokenRepo struct {
	GetByTokenFunc       func(token string) (*models.InvitationAccessToken, error)
	GetByPrettyTokenFunc func(code string) (*models.InvitationAccessToken, error)
}

func (m *mockTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	if m.GetByTokenFunc != nil {
		return m.GetByTokenFunc(token)
	}
	invID := uuid.Must(uuid.NewV4())
	return &models.InvitationAccessToken{
		ID:           uuid.Must(uuid.NewV4()),
		InvitationID: invID,
		Token:        token,
	}, nil
}
func (m *mockTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABCD1234", nil
}
func (m *mockTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	if m.GetByPrettyTokenFunc != nil {
		return m.GetByPrettyTokenFunc(code)
	}
	invID := uuid.Must(uuid.NewV4())
	return &models.InvitationAccessToken{
		ID:           uuid.Must(uuid.NewV4()),
		InvitationID: invID,
		PrettyToken:  code,
	}, nil
}

var _ ports.AccessTokenRepository = (*mockTokenRepo)(nil)

type mockLogRepo struct {
	CreateManyCalled bool
	LastLogs         []models.InvitationLog
}

func (m *mockLogRepo) CreateInvitationLog(log *models.InvitationLog) error { return nil }
func (m *mockLogRepo) CreateManyInvitationLogs(logs []models.InvitationLog) error {
	m.CreateManyCalled = true
	m.LastLogs = logs
	return nil
}

var _ ports.InvitationLogRepository = (*mockLogRepo)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(resource, key string) error                 { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error) {
	return "", errors.New("miss")
}
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

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

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestConfirmRSVP_MissingToken_Returns400(t *testing.T) {
	orig := invitationSvc
	invitationSvc = nil
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/rsvp", `{}`)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Token is required"`)
}

func TestGetInvitationByToken_EmptyParam_Returns400(t *testing.T) {
	orig := invitationSvc
	invitationSvc = nil
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken/", "")
	// c.Param("token") returns "" when no route param is set
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestConfirmRSVP_InvalidStatus_Returns400(t *testing.T) {
	orig := invitationSvc
	invitationSvc = nil
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"ABC123","status":"invalid_status"}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestConfirmRSVP_NegativeGuestCount_Returns400(t *testing.T) {
	orig := invitationSvc
	invitationSvc = nil
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"ABC123","status":"confirmed","guest_count":-1}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestConfirmRSVP_NegativeStringGuestCount_Returns400(t *testing.T) {
	orig := invitationSvc
	invitationSvc = nil
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"ABC123","status":"confirmed","guest_count":"-1"}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestConfirmRSVP_ConfirmedWithoutGuestCount_Returns400(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{}, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"ABC123","status":"confirmed","guest_count":0}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid RSVP request")
}

func TestConfirmRSVP_ExceedsMaxGuests_Returns400(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{
			GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, MaxGuests: 2}, nil
			},
		},
		&mockGuestRepo{},
		&mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					Token:        token,
				}, nil
			},
		},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"ABC123","status":"confirmed","guest_count":3}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "exceeds allowed max")
}

func TestConfirmRSVP_ValidRequest_Returns200(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{}, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"ABC123","status":"confirmed","guest_count":1}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestConfirmRSVP_EventWindowClosed_Returns403(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	activeFrom := time.Now().Add(time.Hour)
	svc := invitationsService.NewInvitationServiceWithDeps(invitationsService.InvitationServiceDeps{
		Repo: &mockInvRepo{
			GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 3}, nil
			},
		},
		GuestRepo: &mockGuestRepo{},
		TokenRepo: &mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					Token:        token,
				}, nil
			},
		},
		LogRepo: &mockLogRepo{},
		Cache:   &mockCacheRepo{},
		ConfigRepo: &mockEventConfigRepo{
			GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
				return &models.EventConfig{ID: id, ActiveFrom: activeFrom}, nil
			},
		},
	})
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"token":"RAW123","status":"confirmed","guest_count":1}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestConfirmRSVP_TokenAliasReturns200(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	var rawSeen string
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{
			GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, MaxGuests: 3}, nil
			},
		},
		&mockGuestRepo{
			GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
				return &models.Guest{ID: uuid.Must(uuid.NewV4()), InvitationID: &invitationID}, nil
			},
		},
		&mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				rawSeen = token
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					Token:        token,
				}, nil
			},
		},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"token":"RAW/123","status":"confirmed","guest_count":1}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "RAW/123", rawSeen)
}

func TestConfirmRSVP_TokenAliasReturnsCanonicalPrettyToken(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{
			GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, MaxGuests: 3}, nil
			},
		},
		&mockGuestRepo{
			GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
				return &models.Guest{ID: uuid.Must(uuid.NewV4()), InvitationID: &invitationID}, nil
			},
		},
		&mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					Token:        token,
					PrettyToken:  "PRETTY/123",
				}, nil
			},
		},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"token":"RAW/123","status":"confirmed","guest_count":1}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data dtos.RSVPConfirmationResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "PRETTY/123", payload.Data.PrettyToken)
}

func TestConfirmRSVP_InvitationTokenAliasesReturn200(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "snake invitation token",
			body: `{"invitation_token":"INV/123","status":"confirmed","guest_count":1}`,
			want: "INV/123",
		},
		{
			name: "camel invitation token",
			body: `{"invitationToken":"INV-CAMEL","status":"confirmed","guest_count":1}`,
			want: "INV-CAMEL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			invitationID := uuid.Must(uuid.NewV4())
			var rawSeen string
			svc := invitationsService.NewInvitationService(
				&mockInvRepo{
					GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
						return &models.Invitation{ID: invitationID, MaxGuests: 3}, nil
					},
				},
				&mockGuestRepo{
					GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
						return &models.Guest{ID: uuid.Must(uuid.NewV4()), InvitationID: &invitationID}, nil
					},
				},
				&mockTokenRepo{
					GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
						rawSeen = token
						return &models.InvitationAccessToken{
							ID:           uuid.Must(uuid.NewV4()),
							InvitationID: invitationID,
							Token:        token,
						}, nil
					},
				},
				&mockLogRepo{},
				&mockCacheRepo{},
			)
			orig := invitationSvc
			invitationSvc = svc
			defer func() { invitationSvc = orig }()

			c, rec := newEchoCtx(http.MethodPost, "/rsvp", tc.body)
			require.NoError(t, ConfirmRSVP(c))

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tc.want, rawSeen)
		})
	}
}

func TestConfirmRSVP_PrettyTokenCamelAliasReturns200(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	var rawSeen string
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{
			GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, MaxGuests: 3}, nil
			},
		},
		&mockGuestRepo{
			GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
				return &models.Guest{ID: uuid.Must(uuid.NewV4()), InvitationID: &invitationID}, nil
			},
		},
		&mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				rawSeen = token
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					Token:        token,
				}, nil
			},
		},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"prettyToken":"CAMEL/123","status":"confirmed","guest_count":1}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "CAMEL/123", rawSeen)
}

func TestConfirmRSVP_AcceptsGuestCountAliases(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "guests_count from dashboard model",
			body: `{"pretty_token":"ABC123","status":"confirmed","guests_count":3}`,
			want: 3,
		},
		{
			name: "guestCount from TypeScript clients",
			body: `{"pretty_token":"ABC123","status":"confirmed","guestCount":2}`,
			want: 2,
		},
		{
			name: "GuestCount from Pascal-cased integrations",
			body: `{"pretty_token":"ABC123","status":"confirmed","GuestCount":2}`,
			want: 2,
		},
		{
			name: "guest_count as numeric string",
			body: `{"pretty_token":"ABC123","status":"confirmed","guest_count":"2"}`,
			want: 2,
		},
		{
			name: "guestsCount from TypeScript dashboard models",
			body: `{"pretty_token":"ABC123","status":"confirmed","guestsCount":5}`,
			want: 5,
		},
		{
			name: "rsvp_guest_count from API response model",
			body: `{"pretty_token":"ABC123","status":"confirmed","rsvp_guest_count":4}`,
			want: 4,
		},
		{
			name: "rsvpGuestCount from TypeScript API models",
			body: `{"pretty_token":"ABC123","status":"confirmed","rsvpGuestCount":6}`,
			want: 6,
		},
		{
			name: "RSVPGuestCount from Pascal-cased API models",
			body: `{"pretty_token":"ABC123","status":"confirmed","RSVPGuestCount":6}`,
			want: 6,
		},
		{
			name: "rsvpGuestCount as numeric string",
			body: `{"pretty_token":"ABC123","status":"confirmed","rsvpGuestCount":"6"}`,
			want: 6,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured *models.Guest
			svc := invitationsService.NewInvitationService(
				&mockInvRepo{},
				&mockGuestRepo{
					UpdateGuestFunc: func(g *models.Guest) error {
						copy := *g
						captured = &copy
						return nil
					},
				},
				&mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
			)
			orig := invitationSvc
			invitationSvc = svc
			defer func() { invitationSvc = orig }()

			c, rec := newEchoCtx(http.MethodPost, "/rsvp", tc.body)
			require.NoError(t, ConfirmRSVP(c))

			assert.Equal(t, http.StatusOK, rec.Code)
			require.NotNil(t, captured)
			assert.Equal(t, tc.want, captured.RSVPGuestCount)
		})
	}
}

func TestConfirmRSVP_NormalizesStatusAndMethod(t *testing.T) {
	var captured *models.Guest
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{
			UpdateGuestFunc: func(g *models.Guest) error {
				copy := *g
				captured = &copy
				return nil
			},
		},
		&mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"ABC123","status":" CONFIRMED ","method":" WEB ","guest_count":1}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "confirmed", captured.RSVPStatus)
	assert.Equal(t, "web", captured.RSVPMethod)
}

func TestConfirmRSVP_AcceptsRSVPAliasesAndDietaryNotes(t *testing.T) {
	var captured *models.Guest
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{
			UpdateGuestFunc: func(g *models.Guest) error {
				copy := *g
				captured = &copy
				return nil
			},
		},
		&mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{
		"prettyToken":"ABC123",
		"rsvpStatus":" CONFIRMED ",
		"rsvpMethod":" HOST ",
		"guestCount":2,
		"dietaryRestrictions":" Vegano ",
		"notes":" Mesa cerca de la pista "
	}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "confirmed", captured.RSVPStatus)
	assert.Equal(t, "host", captured.RSVPMethod)
	assert.Equal(t, 2, captured.RSVPGuestCount)
	assert.Equal(t, "Vegano", captured.DietaryRestrictions)
	assert.Equal(t, "Mesa cerca de la pista", captured.RSVPNotes)
}

func TestConfirmRSVP_LegacyNotesStillMapToDietaryRestrictions(t *testing.T) {
	var captured *models.Guest
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{
			UpdateGuestFunc: func(g *models.Guest) error {
				copy := *g
				captured = &copy
				return nil
			},
		},
		&mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{
		"pretty_token":"ABC123",
		"status":"confirmed",
		"guest_count":1,
		"notes":" Vegano "
	}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "Vegano", captured.DietaryRestrictions)
	assert.Equal(t, "", captured.RSVPNotes)
}

func TestConfirmRSVP_AcceptsExplicitRSVPNotesWithoutOverloadingLegacyNotes(t *testing.T) {
	var captured *models.Guest
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{
			GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
				return &models.Guest{
					ID:    uuid.Must(uuid.NewV4()),
					Notes: "Nota interna",
				}, nil
			},
			UpdateGuestFunc: func(g *models.Guest) error {
				copy := *g
				captured = &copy
				return nil
			},
		},
		&mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{
		"pretty_token":"ABC123",
		"status":"confirmed",
		"guest_count":1,
		"rsvp_notes":" Mesa cerca de la pista "
	}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "", captured.DietaryRestrictions)
	assert.Equal(t, "Mesa cerca de la pista", captured.RSVPNotes)
	assert.Equal(t, "Nota interna", captured.Notes)
}

func TestConfirmRSVP_AcceptsPascalAliases(t *testing.T) {
	var captured *models.Guest
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{
			UpdateGuestFunc: func(g *models.Guest) error {
				copy := *g
				captured = &copy
				return nil
			},
		},
		&mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{
		"Token":"ABC123",
		"RSVPStatus":" CONFIRMED ",
		"RSVPMethod":" KIOSK ",
		"RSVPGuestCount":"2",
		"DietaryRestrictions":" Sin gluten "
	}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "confirmed", captured.RSVPStatus)
	assert.Equal(t, "kiosk", captured.RSVPMethod)
	assert.Equal(t, 2, captured.RSVPGuestCount)
	assert.Equal(t, "Sin gluten", captured.DietaryRestrictions)
}

func TestConfirmRSVP_ReturnsPublicSafePayload(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{
			GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 3}, nil
			},
		},
		&mockGuestRepo{
			GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
				return &models.Guest{
					ID:           guestID,
					EventID:      eventID,
					InvitationID: &invitationID,
					FirstName:    "Ana",
					LastName:     "Garcia",
					Email:        "ana@example.com",
					Phone:        "+525511111111",
				}, nil
			},
		},
		&mockTokenRepo{
			GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					PrettyToken:  code,
				}, nil
			},
		},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"ABC123","status":"confirmed","guest_count":2}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Message string                        `json:"message"`
		Data    dtos.RSVPConfirmationResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "RSVP confirmed", payload.Message)
	assert.Equal(t, "ABC123", payload.Data.PrettyToken)
	assert.Equal(t, guestID, payload.Data.Guest.ID)
	assert.Equal(t, "confirmed", payload.Data.Guest.RSVPStatus)
	require.NotNil(t, payload.Data.Guest.RSVPAt)
	assert.Equal(t, "web", payload.Data.Guest.RSVPMethod)
	assert.Equal(t, 2, payload.Data.Guest.RSVPGuestCount)

	raw := rec.Body.String()
	assert.NotContains(t, raw, "ana@example.com")
	assert.NotContains(t, raw, "phone")
	assert.NotContains(t, raw, "email")
}

func TestGetInvitationByToken_ValidToken_Returns200(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{}, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken/ABC123", "")
	c.SetParamNames("token")
	c.SetParamValues("ABC123")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetInvitationByToken_EventWindowClosed_Returns403(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	activeFrom := time.Now().Add(time.Hour)
	svc := invitationsService.NewInvitationServiceWithDeps(invitationsService.InvitationServiceDeps{
		Repo: &mockInvRepo{
			GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, EventID: eventID}, nil
			},
		},
		GuestRepo: &mockGuestRepo{},
		TokenRepo: &mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					Token:        token,
				}, nil
			},
		},
		LogRepo: &mockLogRepo{},
		Cache:   &mockCacheRepo{},
		ConfigRepo: &mockEventConfigRepo{
			GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
				return &models.EventConfig{ID: id, ActiveFrom: activeFrom}, nil
			},
		},
	})
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken/RAW123", "")
	c.SetParamNames("token")
	c.SetParamValues("RAW123")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGetInvitationByToken_EventAccessCheckError_Returns500(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	svc := invitationsService.NewInvitationServiceWithDeps(invitationsService.InvitationServiceDeps{
		Repo: &mockInvRepo{
			GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, EventID: eventID}, nil
			},
		},
		GuestRepo: &mockGuestRepo{},
		TokenRepo: &mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					Token:        token,
				}, nil
			},
		},
		LogRepo: &mockLogRepo{},
		Cache:   &mockCacheRepo{},
		ConfigRepo: &mockEventConfigRepo{
			GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
				return nil, errors.New("database offline")
			},
		},
	})
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken/RAW123", "")
	c.SetParamNames("token")
	c.SetParamValues("RAW123")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestGetInvitationByToken_ReturnsPublicInvitationContract(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	eventDate := time.Date(2026, 8, 15, 20, 30, 0, 0, time.UTC)
	rsvpAt := time.Date(2026, 7, 1, 18, 45, 0, 0, time.UTC)

	svc := invitationsService.NewInvitationService(
		&mockInvRepo{
			GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{
					ID:        invitationID,
					EventID:   eventID,
					MaxGuests: 3,
					Event: models.Event{
						ID:             eventID,
						Name:           "Boda Ana y Luis",
						Description:    "Ceremonia y recepcion",
						CoverImageURL:  "covers/event.webp",
						EventDateTime:  eventDate,
						Address:        "Jardin Central",
						OrganizerName:  "Ana y Luis",
						OrganizerEmail: "private@example.com",
						EventType:      models.EventType{Name: "wedding"},
					},
				}, nil
			},
		},
		&mockGuestRepo{
			GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
				return &models.Guest{
					ID:                  guestID,
					EventID:             eventID,
					InvitationID:        &invitationID,
					FirstName:           "Ana",
					LastName:            "Garcia",
					Email:               "ana@example.com",
					Phone:               "+525511111111",
					RSVPStatus:          "pending",
					RSVPAt:              &rsvpAt,
					RSVPMethod:          "web",
					DietaryRestrictions: "Vegano",
					RSVPNotes:           "Mesa cerca",
					Notes:               "Nota interna del dashboard",
				}, nil
			},
		},
		&mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{
					ID:           uuid.Must(uuid.NewV4()),
					InvitationID: invitationID,
					Token:        token,
					PrettyToken:  "NICE1234",
				}, nil
			},
		},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken/RAW123", "")
	c.SetParamNames("token")
	c.SetParamValues("RAW123")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Message string                `json:"message"`
		Data    dtos.InvitationLookup `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Invitation loaded", body.Message)
	assert.Equal(t, invitationID, body.Data.Invitation.ID)
	assert.Equal(t, eventID, body.Data.Invitation.EventID)
	assert.Equal(t, 3, body.Data.Invitation.MaxGuests)
	assert.Equal(t, guestID, body.Data.Guest.ID)
	assert.Equal(t, "Ana", body.Data.Guest.FirstName)
	assert.Equal(t, "pending", body.Data.Guest.RSVPStatus)
	require.NotNil(t, body.Data.Guest.RSVPAt)
	assert.Equal(t, rsvpAt, *body.Data.Guest.RSVPAt)
	assert.Equal(t, "web", body.Data.Guest.RSVPMethod)
	assert.Equal(t, "Vegano", body.Data.Guest.DietaryRestrictions)
	assert.Equal(t, "Mesa cerca", body.Data.Guest.RSVPNotes)
	assert.Equal(t, "NICE1234", body.Data.PrettyToken)
	require.NotNil(t, body.Data.Event)
	assert.Equal(t, "Boda Ana y Luis", body.Data.Event.Name)
	assert.Equal(t, "covers/event.webp", body.Data.Event.CoverImageURL)
	assert.Equal(t, "covers/event.webp", body.Data.Event.CoverViewURL)
	assert.Equal(t, "covers/event.webp", body.Data.Event.ViewURL)
	assert.Nil(t, body.Data.Event.CoverViewURLExpiresAt)
	assert.Nil(t, body.Data.Event.ViewURLExpiresAt)
	require.NotNil(t, body.Data.Event.EventDateTime)
	assert.Equal(t, eventDate, *body.Data.Event.EventDateTime)
	assert.Equal(t, "wedding", body.Data.Event.EventType)

	payload := rec.Body.String()
	assert.NotContains(t, payload, "private@example.com")
	assert.NotContains(t, payload, "ana@example.com")
	assert.NotContains(t, payload, "organizer_email")
	assert.NotContains(t, payload, "Nota interna del dashboard")
	assert.NotContains(t, payload, `"notes"`)
}

func TestGetInvitationByToken_QueryTokenWithURLSensitiveCharacters_Returns200(t *testing.T) {
	var capturedToken string
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{},
		&mockTokenRepo{GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			capturedToken = token
			invID := uuid.Must(uuid.NewV4())
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invID,
				PrettyToken:  token,
			}, nil
		}},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	e := echo.New()
	e.Validator = customValidator.New()
	e.GET("/api/invitations/ByToken", GetInvitationByToken)
	e.GET("/api/invitations/ByToken/:token", GetInvitationByToken)

	req := httptest.NewRequest(http.MethodGet, "/api/invitations/ByToken?token=ABC%2F123%20%2B%23", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ABC/123 +#", capturedToken)
}

func TestGetInvitationByToken_QueryPrettyTokenAlias_Returns200(t *testing.T) {
	var capturedToken string
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{},
		&mockTokenRepo{GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			capturedToken = token
			invID := uuid.Must(uuid.NewV4())
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invID,
				PrettyToken:  token,
			}, nil
		}},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	e := echo.New()
	e.Validator = customValidator.New()
	e.GET("/api/invitations/ByToken", GetInvitationByToken)

	req := httptest.NewRequest(http.MethodGet, "/api/invitations/ByToken?prettyToken=CAMEL123", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "CAMEL123", capturedToken)
}

func TestGetInvitationByToken_QueryInvitationTokenAlias_Returns200(t *testing.T) {
	var capturedToken string
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{},
		&mockTokenRepo{GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			capturedToken = token
			invID := uuid.Must(uuid.NewV4())
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invID,
				PrettyToken:  token,
			}, nil
		}},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	e := echo.New()
	e.Validator = customValidator.New()
	e.GET("/api/invitations/ByToken", GetInvitationByToken)

	req := httptest.NewRequest(http.MethodGet, "/api/invitations/ByToken?invitationToken=INV%2F123", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "INV/123", capturedToken)
}

func TestConfirmRSVP_DeclinedStatus_Returns200(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{}, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"TOKEN01","status":"declined","guest_count":0}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"rsvp_guest_count":0`)
}

func TestConfirmRSVP_PendingStatus_Returns200(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{}, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	body := `{"pretty_token":"TOKEN02","status":"pending","guest_count":0}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetInvitationByToken_TokenNotFound_Returns401(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{},
		&mockTokenRepo{GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return nil, errors.New("token not found")
		}, GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return nil, errors.New("pretty token not found")
		}},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken/BADTOK", "")
	c.SetParamNames("token")
	c.SetParamValues("BADTOK")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// ── ListByEvent tests ──────────────────────────────────────────────────────────

func TestGetInvitationByToken_InvitationNotFound_Returns401(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{
			GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return nil, nil
			},
		},
		&mockGuestRepo{
			GetGuestByInvitationIDFunc: func(invID uuid.UUID) (*models.Guest, error) {
				require.Fail(t, "guest lookup should not run when invitation is missing")
				return nil, nil
			},
		},
		&mockTokenRepo{GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				ID:           uuid.Must(uuid.NewV4()),
				InvitationID: invitationID,
				Token:        token,
			}, nil
		}},
		&mockLogRepo{},
		&mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken/ORPHAN", "")
	c.SetParamNames("token")
	c.SetParamValues("ORPHAN")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid or expired token")
}

func TestListByEvent_InvalidUUID_Returns400(t *testing.T) {
	orig := invitationSvc
	invitationSvc = nil
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/events/not-a-uuid/invitations", "")
	c.SetParamNames("id")
	c.SetParamValues("not-a-uuid")
	require.NoError(t, ListByEvent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListByEvent_ServiceError_Returns500(t *testing.T) {
	repo := &mockInvRepo{
		ListByEventIDFunc: func(eventID uuid.UUID) ([]models.Invitation, error) {
			return nil, errors.New("db error")
		},
	}
	svc := invitationsService.NewInvitationService(
		repo, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/invitations", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setAuthzContext(t, c, eventID, nil, nil)
	require.NoError(t, ListByEvent(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestListByEvent_EmptyResult_Returns200(t *testing.T) {
	repo := &mockInvRepo{
		ListByEventIDFunc: func(eventID uuid.UUID) ([]models.Invitation, error) {
			return []models.Invitation{}, nil
		},
	}
	svc := invitationsService.NewInvitationService(
		repo, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/invitations", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setAuthzContext(t, c, eventID, nil, nil)
	require.NoError(t, ListByEvent(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestListByEvent_PopulatedResult_Returns200(t *testing.T) {
	invID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	repo := &mockInvRepo{
		ListByEventIDFunc: func(eID uuid.UUID) ([]models.Invitation, error) {
			return []models.Invitation{
				{ID: invID, EventID: eID, MaxGuests: 2},
			}, nil
		},
	}
	svc := invitationsService.NewInvitationService(
		repo, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/invitations", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setAuthzContext(t, c, eventID, nil, nil)
	require.NoError(t, ListByEvent(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// ── ResendInvitation tests ─────────────────────────────────────────────────────

func TestResendInvitation_InvalidUUID_Returns400(t *testing.T) {
	orig := invitationSvc
	invitationSvc = nil
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/invitations/not-a-uuid/resend", "")
	c.SetParamNames("id")
	c.SetParamValues("not-a-uuid")
	require.NoError(t, ResendInvitation(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestResendInvitation_NotFound_Returns404(t *testing.T) {
	repo := &mockInvRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return nil, gorm.ErrRecordNotFound
		},
	}
	svc := invitationsService.NewInvitationService(
		repo, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	invID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPost, "/invitations/"+invID.String()+"/resend", "")
	c.SetParamNames("id")
	c.SetParamValues(invID.String())
	setAuthzContext(t, c, uuid.Must(uuid.NewV4()), nil, gorm.ErrRecordNotFound)
	require.NoError(t, ResendInvitation(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestResendInvitation_ServiceError_Returns500(t *testing.T) {
	repo := &mockInvRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return nil, errors.New("db connection error")
		},
	}
	svc := invitationsService.NewInvitationService(
		repo, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	invID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPost, "/invitations/"+invID.String()+"/resend", "")
	c.SetParamNames("id")
	c.SetParamValues(invID.String())
	setAuthzContext(t, c, uuid.Must(uuid.NewV4()), &models.Invitation{ID: invID}, nil)
	require.NoError(t, ResendInvitation(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestResendInvitation_Success_Returns200(t *testing.T) {
	invID := uuid.Must(uuid.NewV4())
	repo := &mockInvRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:             id,
				EnableWhatsApp: true,
				EnableEmail:    false,
				InvitationSent: false,
			}, nil
		},
	}
	logRepo := &mockLogRepo{}
	svc := invitationsService.NewInvitationService(
		repo, &mockGuestRepo{}, &mockTokenRepo{}, logRepo, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/invitations/"+invID.String()+"/resend", "")
	c.SetParamNames("id")
	c.SetParamValues(invID.String())
	setAuthzContext(t, c, uuid.Must(uuid.NewV4()), &models.Invitation{ID: invID}, nil)
	require.NoError(t, ResendInvitation(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	assert.True(t, logRepo.CreateManyCalled, "expected CreateManyInvitationLogs to be called")
	if assert.Len(t, logRepo.LastLogs, 1) {
		assert.Equal(t, "whatsapp", logRepo.LastLogs[0].Channel)
		assert.Equal(t, "resent", logRepo.LastLogs[0].Action)
		assert.Equal(t, "success", logRepo.LastLogs[0].Status)
	}
}

func TestResendInvitation_Success_ManualFallback_Returns200(t *testing.T) {
	invID := uuid.Must(uuid.NewV4())
	repo := &mockInvRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{
				ID:             id,
				EnableWhatsApp: false,
				EnableEmail:    false,
				InvitationSent: false,
			}, nil
		},
	}
	logRepo := &mockLogRepo{}
	svc := invitationsService.NewInvitationService(
		repo, &mockGuestRepo{}, &mockTokenRepo{}, logRepo, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/invitations/"+invID.String()+"/resend", "")
	c.SetParamNames("id")
	c.SetParamValues(invID.String())
	setAuthzContext(t, c, uuid.Must(uuid.NewV4()), &models.Invitation{ID: invID}, nil)
	require.NoError(t, ResendInvitation(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	if assert.Len(t, logRepo.LastLogs, 1) {
		assert.Equal(t, "manual", logRepo.LastLogs[0].Channel)
	}
}
