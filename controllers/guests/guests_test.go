package guests

import (
	"context"
	"events-stocks/models"
	"fmt"
	"events-stocks/services/ports"
	guestsService "events-stocks/services/guests"
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

type mockGuestRepo struct{}

func (m *mockGuestRepo) CreateGuest(obj *models.Guest) error                           { return nil }
func (m *mockGuestRepo) UpdateGuest(g *models.Guest) error                             { return nil }
func (m *mockGuestRepo) DeleteGuest(id uuid.UUID) error                                { return nil }
func (m *mockGuestRepo) GetGuestByID(id uuid.UUID) (*models.Guest, error)              { return nil, nil }
func (m *mockGuestRepo) CreateGuests(guests []models.Guest) error                      { return nil }
func (m *mockGuestRepo) GetPendingStatusID() uuid.UUID                                 { return uuid.Nil }
func (m *mockGuestRepo) GetGuestByInvitationID(invID uuid.UUID) (*models.Guest, error) { return nil, nil }

var _ ports.GuestRepository = (*mockGuestRepo)(nil)

type mockTokenRepo struct{}

func (m *mockTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	return nil, nil
}
func (m *mockTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	return nil, nil
}
func (m *mockTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABCD1234", nil
}

var _ ports.AccessTokenRepository = (*mockTokenRepo)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(_, _ string) error                                    { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error           { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error)              { return "", nil }
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error   { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

type mockTransactor struct{}

func (m *mockTransactor) Transaction(fn func(tx *gorm.DB) error) error { return nil }

var _ ports.Transactor = (*mockTransactor)(nil)

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestCreateGuest_InvalidBody_Returns400(t *testing.T) {
	orig := guestSvc
	guestSvc = nil
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/guests", `{invalid json}`)
	require.NoError(t, CreateGuest(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateGuest_ValidationError_Returns400(t *testing.T) {
	orig := guestSvc
	guestSvc = nil
	defer func() { guestSvc = orig }()

	// missing required "first_name"
	c, rec := newEchoCtx(http.MethodPost, "/guests", `{}`)
	require.NoError(t, CreateGuest(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeleteGuest_InvalidUUID_Returns400(t *testing.T) {
	orig := guestSvc
	guestSvc = nil
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/guests/bad-id", "")
	c.SetParamNames("id")
	c.SetParamValues("bad-id")
	require.NoError(t, DeleteGuest(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateGuests_EmptyBody_Returns400(t *testing.T) {
	orig := guestSvc
	guestSvc = nil
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/guests/bulk", `[]`)
	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateGuest_ValidUUID_Returns200(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), `{"first_name":"Ana"}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	require.NoError(t, UpdateGuest(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateGuest_InvalidBody_Returns400(t *testing.T) {
	orig := guestSvc
	guestSvc = nil
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), `{invalid json}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	require.NoError(t, UpdateGuest(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateGuest_ValidationError_MissingFirstName_Returns400(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	// missing first_name which is validate:"required"
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), `{"last_name":"Garcia"}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	require.NoError(t, UpdateGuest(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateGuests_ValidBody_Returns201(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	body := `[{"first_name":"Bob","event_id":"` + uuid.Must(uuid.NewV4()).String() + `"}]`
	c, rec := newEchoCtx(http.MethodPost, "/guests/bulk", body)
	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// ── GetAttendees ──────────────────────────────────────────────────────────────

func TestGetAttendees_InvalidUUID_Returns400(t *testing.T) {
	c, rec := newEchoCtx(http.MethodGet, "/events/section/bad-uuid/attendees", "")
	c.SetParamNames("sectionId")
	c.SetParamValues("bad-uuid")
	require.NoError(t, GetAttendees(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetAttendees_SectionNotFound_Returns404(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	deps := attendeeDeps{
		getSection:   func(id uuid.UUID) (*models.EventSection, error) { return nil, fmt.Errorf("not found") },
		getAttendees: func(id uuid.UUID) ([]models.Guest, error) { return nil, nil },
	}
	c, rec := newEchoCtx(http.MethodGet, "/events/section/"+sectionID.String()+"/attendees", "")
	c.SetParamNames("sectionId")
	c.SetParamValues(sectionID.String())
	require.NoError(t, handleGetAttendees(deps, c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGetAttendees_Success_Returns200(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID   := uuid.Must(uuid.NewV4())
	section   := &models.EventSection{EventID: eventID}
	guests    := []models.Guest{
		{FirstName: "Ana", LastName: "García", Role: "Graduado", Order: 1},
		{FirstName: "Luis", LastName: "Pérez", Role: "Graduado", Order: 2},
	}

	deps := attendeeDeps{
		getSection:   func(id uuid.UUID) (*models.EventSection, error) { return section, nil },
		getAttendees: func(id uuid.UUID) ([]models.Guest, error) { return guests, nil },
	}

	c, rec := newEchoCtx(http.MethodGet, "/events/section/"+sectionID.String()+"/attendees", "")
	c.SetParamNames("sectionId")
	c.SetParamValues(sectionID.String())
	require.NoError(t, handleGetAttendees(deps, c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Ana")
	assert.Contains(t, rec.Body.String(), "García")
}
