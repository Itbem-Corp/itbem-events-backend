package invitations

import (
	"context"
	"errors"
	"events-stocks/models"
	"events-stocks/services/ports"
	invitationsService "events-stocks/services/invitations"
	customValidator "events-stocks/middleware/validator"
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

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockInvRepo struct {
	GetInvitationByIDFunc     func(id uuid.UUID) (*models.Invitation, error)
	GetInvitationByIDLiteFunc func(id uuid.UUID) (*models.Invitation, error)
	ListByEventIDFunc         func(eventID uuid.UUID) ([]models.Invitation, error)
}

func (m *mockInvRepo) CreateInvitation(obj *models.Invitation) error       { return nil }
func (m *mockInvRepo) UpdateInvitation(obj *models.Invitation) error       { return nil }
func (m *mockInvRepo) DeleteInvitation(id uuid.UUID) error                 { return nil }
func (m *mockInvRepo) ListInvitations() ([]models.Invitation, error)       { return nil, nil }
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

type mockGuestRepo struct{}

func (m *mockGuestRepo) CreateGuest(obj *models.Guest) error                          { return nil }
func (m *mockGuestRepo) UpdateGuest(g *models.Guest) error                            { return nil }
func (m *mockGuestRepo) DeleteGuest(id uuid.UUID) error                               { return nil }
func (m *mockGuestRepo) GetGuestByID(id uuid.UUID) (*models.Guest, error)             { return nil, nil }
func (m *mockGuestRepo) CreateGuests(guests []models.Guest) error                     { return nil }
func (m *mockGuestRepo) GetPendingStatusID() uuid.UUID                                { return uuid.Nil }
func (m *mockGuestRepo) GetGuestByInvitationID(invID uuid.UUID) (*models.Guest, error) {
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

func (m *mockCacheRepo) Invalidate(resource, key string) error                                    { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error                    { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error)                      { return "", errors.New("miss") }
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error           { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestConfirmRSVP_MissingToken_Returns400(t *testing.T) {
	orig := invitationSvc
	invitationSvc = nil
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/rsvp", `{}`)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
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
	require.NoError(t, ResendInvitation(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	if assert.Len(t, logRepo.LastLogs, 1) {
		assert.Equal(t, "manual", logRepo.LastLogs[0].Channel)
	}
}
