package guests

import (
	"context"
	"encoding/json"
	"events-stocks/controllers/publicaccess"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/publicaccessproof"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	guestsService "events-stocks/services/guests"
	"events-stocks/services/ports"
	"fmt"
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

func setAuthzContext(t *testing.T, c echo.Context, eventID uuid.UUID, guest *models.Guest) {
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
		GetGuestByID: func(id uuid.UUID) (*models.Guest, error) {
			if guest != nil {
				return guest, nil
			}
			return &models.Guest{ID: id, EventID: eventID, FirstName: "Ana"}, nil
		},
	})
	t.Cleanup(restore)
}

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockGuestRepo struct {
	bulkUpdateGuestStatusFunc func(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error
}

func (m *mockGuestRepo) CreateGuest(obj *models.Guest) error              { return nil }
func (m *mockGuestRepo) UpdateGuest(g *models.Guest) error                { return nil }
func (m *mockGuestRepo) DeleteGuest(id uuid.UUID) error                   { return nil }
func (m *mockGuestRepo) GetGuestByID(id uuid.UUID) (*models.Guest, error) { return nil, nil }
func (m *mockGuestRepo) CreateGuests(guests []models.Guest) error         { return nil }
func (m *mockGuestRepo) BulkDeleteGuests(ids []uuid.UUID) error           { return nil }
func (m *mockGuestRepo) BulkUpdateGuestStatus(eventID uuid.UUID, ids []uuid.UUID, statusID uuid.UUID, rsvpStatus, rsvpMethod string) error {
	if m.bulkUpdateGuestStatusFunc != nil {
		return m.bulkUpdateGuestStatusFunc(eventID, ids, statusID, rsvpStatus, rsvpMethod)
	}
	return nil
}
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
	return nil, nil
}

var _ ports.GuestRepository = (*mockGuestRepo)(nil)

type captureUpdateGuestRepo struct {
	mockGuestRepo
	existing *models.Guest
	reloaded *models.Guest
	updated  *models.Guest
}

type checkinSummaryGuestRepo struct {
	mockGuestRepo
	guest   models.Guest
	summary dtos.GuestSummary
	share   dtos.GuestShareSummary
}

func (m *checkinSummaryGuestRepo) ListCheckinGuests(eventID uuid.UUID, query dtos.CheckinGuestsListQuery) ([]models.Guest, int64, error) {
	return []models.Guest{m.guest}, 1, nil
}

func (m *checkinSummaryGuestRepo) GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error) {
	return m.summary, nil
}

func (m *checkinSummaryGuestRepo) GetGuestShareSummaryByEventID(eventID uuid.UUID) (dtos.GuestShareSummary, error) {
	return m.share, nil
}

func (m *captureUpdateGuestRepo) UpdateGuest(g *models.Guest) error {
	copied := *g
	m.updated = &copied
	return nil
}

func (m *captureUpdateGuestRepo) GetGuestByID(id uuid.UUID) (*models.Guest, error) {
	if m.updated != nil && m.reloaded != nil {
		return m.reloaded, nil
	}
	if m.existing != nil {
		return m.existing, nil
	}
	return nil, nil
}

var _ ports.GuestRepository = (*captureUpdateGuestRepo)(nil)

func TestGetGuests_CheckinEmbedsEventSummary(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	repo := &checkinSummaryGuestRepo{
		guest:   models.Guest{ID: guestID, EventID: eventID, FirstName: "Ana"},
		summary: dtos.GuestSummary{Total: 8, Confirmed: 5, Pending: 2, Declined: 1, TotalAttendees: 9},
	}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/guests/checkin:"+eventID.String()+"?page=1&page_size=60&filter=ALL", "")
	c.SetParamNames("key")
	c.SetParamValues("checkin:" + eventID.String())
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, GetGuests(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data dtos.CheckinGuestsPageResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Data.Summary)
	assert.Equal(t, int64(8), body.Data.Summary.Total)
	assert.Equal(t, guestID, body.Data.Data[0].ID)
}

func TestGetGuests_InvitationsEmbedsShareSummary(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repo := &checkinSummaryGuestRepo{
		guest: models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, FirstName: "Ana"},
		share: dtos.GuestShareSummary{Total: 8, WithEmail: 6, WithPhone: 7, PendingWithEmail: 2},
	}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/guests/invitations:"+eventID.String()+"?page=1&page_size=50&filter=ALL", "")
	c.SetParamNames("key")
	c.SetParamValues("invitations:" + eventID.String())
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, GetGuests(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data dtos.CheckinGuestsPageResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Data.ShareSummary)
	assert.Equal(t, int64(6), body.Data.ShareSummary.WithEmail)
	assert.Equal(t, int64(2), body.Data.ShareSummary.PendingWithEmail)
}

func TestExportGuestsCSV_ReturnsCompleteSpreadsheetSafePayload(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repo := &checkinSummaryGuestRepo{guest: models.Guest{
		ID: uuid.Must(uuid.NewV4()), EventID: eventID, FirstName: "Ana, María", LastName: "López",
		Email: "ana@example.com", Phone: "+52 555", RSVPStatus: "confirmed", GuestsCount: 3,
		DietaryRestrictions: "Sin gluten", RSVPNotes: "Mesa principal", Notes: "VIP",
	}}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/guests/export?filter=CONFIRMED", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, ExportGuestsCSV(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/csv; charset=utf-8", rec.Header().Get(echo.HeaderContentType))
	assert.True(t, strings.HasPrefix(rec.Body.String(), "\xEF\xBB\xBF"))
	assert.Contains(t, rec.Body.String(), `"Ana, María",López,ana@example.com`)
	assert.Contains(t, rec.Body.String(), `,3,2,CONFIRMED,`)
}

func TestExportGuestsCSV_InvitationViewIncludesRSVPColumns(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	respondedAt := time.Date(2026, time.July, 11, 18, 30, 0, 0, time.FixedZone("CDT", -6*60*60))
	repo := &checkinSummaryGuestRepo{guest: models.Guest{
		ID: uuid.Must(uuid.NewV4()), EventID: eventID, FirstName: "Ana", LastName: "López",
		Email: "ana@example.com", Phone: "+52 555", RSVPStatus: "confirmed", GuestsCount: 2,
		RSVPAt: &respondedAt, RSVPMethod: "web",
	}}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/guests/export?view=invitations", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, ExportGuestsCSV(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get(echo.HeaderContentDisposition), "invitaciones.csv")
	assert.Contains(t, rec.Body.String(), "Estado RSVP,Fecha respuesta,Método,Acompañantes")
	assert.Contains(t, rec.Body.String(), "Ana López,ana@example.com,+52 555,CONFIRMED,2026-07-12T00:30:00Z,web,1")
}

func TestExportGuestsCSV_RSVPViewIncludesCompleteAuditColumns(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, time.July, 11, 19, 0, 0, 0, time.UTC)
	repo := &checkinSummaryGuestRepo{guest: models.Guest{
		ID: uuid.Must(uuid.NewV4()), EventID: eventID, FirstName: "Ana", LastName: "López",
		Email: "ana@example.com", RSVPStatus: "confirmed", GuestsCount: 2, RSVPMethod: "host",
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/guests/export?view=rsvp", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, ExportGuestsCSV(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get(echo.HeaderContentDisposition), "rsvp.csv")
	assert.Contains(t, rec.Body.String(), "Estado,Canal,+1s,Respondió,Agregado")
	assert.Contains(t, rec.Body.String(), "Ana López,ana@example.com,,CONFIRMED,host,1,2026-07-11T19:00:00Z,2026-07-01T12:00:00Z")
}

type listGuestRepo struct {
	mockGuestRepo
	guests  []models.Guest
	called  bool
	eventID uuid.UUID
}

func (m *listGuestRepo) ListGuestsByEventID(eventID uuid.UUID) ([]models.Guest, error) {
	m.called = true
	m.eventID = eventID
	return m.guests, nil
}

var _ ports.GuestRepository = (*listGuestRepo)(nil)

type summaryGuestRepo struct {
	mockGuestRepo
	summary dtos.GuestSummary
	err     error
	called  bool
	eventID uuid.UUID
}

func (m *summaryGuestRepo) GetGuestSummaryByEventID(eventID uuid.UUID) (dtos.GuestSummary, error) {
	m.called = true
	m.eventID = eventID
	return m.summary, m.err
}

var _ ports.GuestRepository = (*summaryGuestRepo)(nil)

type mockTokenRepo struct {
	token      *models.InvitationAccessToken
	pretty     *models.InvitationAccessToken
	seen       string
	prettySeen string
}

func (m *mockTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	m.seen = token
	return m.token, nil
}
func (m *mockTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	m.prettySeen = code
	return m.pretty, nil
}
func (m *mockTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABCD1234", nil
}

var _ ports.AccessTokenRepository = (*mockTokenRepo)(nil)

type mockGuestConfigRepo struct {
	cfg *models.EventConfig
}

func (m *mockGuestConfigRepo) CreateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockGuestConfigRepo) UpdateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockGuestConfigRepo) DeleteEventConfig(id uuid.UUID) error            { return nil }
func (m *mockGuestConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	return m.cfg, nil
}

var _ ports.EventConfigRepository = (*mockGuestConfigRepo)(nil)

type mockGuestInvitationRepo struct {
	invitation *models.Invitation
}

func (m *mockGuestInvitationRepo) CreateInvitation(invitation *models.Invitation) error {
	return nil
}
func (m *mockGuestInvitationRepo) UpdateInvitation(invitation *models.Invitation) error {
	return nil
}
func (m *mockGuestInvitationRepo) DeleteInvitation(id uuid.UUID) error { return nil }
func (m *mockGuestInvitationRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, nil
}
func (m *mockGuestInvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, nil
}
func (m *mockGuestInvitationRepo) ListInvitations() ([]models.Invitation, error) {
	return nil, nil
}
func (m *mockGuestInvitationRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return nil, nil
}

var _ ports.InvitationRepository = (*mockGuestInvitationRepo)(nil)

type mockGuestEventRepo struct {
	event *models.Event
	err   error
}

func (m *mockGuestEventRepo) CreateEvent(event *models.Event) error { return nil }
func (m *mockGuestEventRepo) UpdateEvent(event *models.Event) error { return nil }
func (m *mockGuestEventRepo) DeleteEvent(id uuid.UUID) error        { return nil }
func (m *mockGuestEventRepo) ListEvents(page int, pageSize int, name string) ([]models.Event, error) {
	return nil, nil
}
func (m *mockGuestEventRepo) GetEventByID(id uuid.UUID) (string, error) { return "", nil }
func (m *mockGuestEventRepo) GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	return m.event, m.err
}
func (m *mockGuestEventRepo) GetEventByIDForSpec(id uuid.UUID) (*models.Event, error) {
	return m.event, m.err
}
func (m *mockGuestEventRepo) GetEventByIdentifier(identifier string) (*models.Event, error) {
	return m.event, m.err
}
func (m *mockGuestEventRepo) GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockGuestEventRepo) GetAllEventsForDashboard() ([]models.Event, error) { return nil, nil }
func (m *mockGuestEventRepo) GetEventsForUser(userID uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockGuestEventRepo) UpdateEventCover(id uuid.UUID, coverImageURL string) error {
	return nil
}
func (m *mockGuestEventRepo) IdentifierExists(identifier string) bool { return false }

var _ ports.EventsRepository = (*mockGuestEventRepo)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(_, _ string) error                                  { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error         { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error)            { return "", nil }
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error { return nil }

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

func TestCreateGuest_ValidBody_CreatesInvitation(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	body := `{"first_name":"Ana","event_id":"` + eventID.String() + `","guests_count":2}`
	c, rec := newEchoCtx(http.MethodPost, "/guests", body)
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, CreateGuest(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), "Guest and invitation created")
	assert.Contains(t, rec.Body.String(), "invitation_id")
	assert.Contains(t, rec.Body.String(), `"pretty_token":"ABCD1234"`)
}

func TestCreateGuest_AcceptsTypeScriptAliases(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	body := `{"firstName":"Ana","lastName":"Lopez","eventId":"` + eventID.String() + `","guestCount":2}`
	c, rec := newEchoCtx(http.MethodPost, "/guests", body)
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, CreateGuest(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"first_name":"Ana"`)
	assert.Contains(t, rec.Body.String(), `"last_name":"Lopez"`)
	assert.Contains(t, rec.Body.String(), `"guests_count":2`)
	assert.Contains(t, rec.Body.String(), `"rsvp_guest_count":2`)
}

func TestCreateGuest_UsesRSVPGuestCountAliasAsPartySizeFallback(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	body := `{"firstName":"Ana","eventId":"` + eventID.String() + `","rsvpGuestCount":3}`
	c, rec := newEchoCtx(http.MethodPost, "/guests", body)
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, CreateGuest(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"guests_count":3`)
	assert.Contains(t, rec.Body.String(), `"rsvp_guest_count":3`)
	assert.Contains(t, rec.Body.String(), `"max_guests":3`)
}

func TestCreateGuest_AcceptsPascalAliases(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	body := `{"FirstName":"Ana","LastName":"Lopez","EventID":"` + eventID.String() + `","GuestCount":2,"Role":"graduate","ImageURL":"profiles/ana.webp","DisplayOrder":"4","Nickname":"Anita","Headline":"Ingenieria","Bio":"Bio publica","Signature":"Gracias","IsHost":true}`
	c, rec := newEchoCtx(http.MethodPost, "/guests", body)
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, CreateGuest(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"first_name":"Ana"`)
	assert.Contains(t, rec.Body.String(), `"last_name":"Lopez"`)
	assert.Contains(t, rec.Body.String(), `"guests_count":2`)
	assert.Contains(t, rec.Body.String(), `"rsvp_guest_count":2`)
	assert.Contains(t, rec.Body.String(), `"role":"graduate"`)
	assert.Contains(t, rec.Body.String(), `"image_url":"profiles/ana.webp"`)
	assert.Contains(t, rec.Body.String(), `"order":4`)
	assert.Contains(t, rec.Body.String(), `"nickname":"Anita"`)
	assert.Contains(t, rec.Body.String(), `"headline":"Ingenieria"`)
	assert.Contains(t, rec.Body.String(), `"bio":"Bio publica"`)
	assert.Contains(t, rec.Body.String(), `"signature":"Gracias"`)
	assert.Contains(t, rec.Body.String(), `"is_host":true`)
}

func TestGetGuests_AllLoadsAfterAuth(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	repo := &listGuestRepo{
		guests: []models.Guest{{
			ID:        guestID,
			EventID:   eventID,
			FirstName: "Ana",
		}},
	}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/guests/all:"+eventID.String(), "")
	c.SetParamNames("key")
	c.SetParamValues("all:" + eventID.String())
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, GetGuests(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, repo.called)
	assert.Equal(t, eventID, repo.eventID)
	assert.Contains(t, rec.Body.String(), `"first_name":"Ana"`)
}

func TestGetGuests_AllDoesNotLoadWhenUnauthorized(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	repo := &listGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/guests/all:"+eventID.String(), "")
	c.SetParamNames("key")
	c.SetParamValues("all:" + eventID.String())
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: userID, IsRoot: false}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, ClientID: &clientID}, nil
		},
		CheckAccessRecursive: func(userID, targetClientID uuid.UUID) (bool, string) {
			assert.Equal(t, clientID, targetClientID)
			return false, ""
		},
	})
	t.Cleanup(restore)

	require.NoError(t, GetGuests(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, repo.called)
}

func TestGetGuests_SummaryReturnsCompactNumericRollupAfterAuth(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repo := &summaryGuestRepo{summary: dtos.GuestSummary{
		Total:          7,
		Confirmed:      3,
		Pending:        2,
		Declined:       2,
		TotalAttendees: 8,
	}}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/guests/summary:"+eventID.String(), "")
	c.SetParamNames("key")
	c.SetParamValues("summary:" + eventID.String())
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, GetGuests(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, repo.called)
	assert.Equal(t, eventID, repo.eventID)
	var body struct {
		Data dtos.GuestSummary `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, repo.summary, body.Data)
	assert.JSONEq(t, `{"total":7,"confirmed":3,"pending":2,"declined":2,"total_attendees":8}`, mustJSON(t, body.Data))
}

func TestGetGuests_SummaryRejectsInvalidEventUUIDBeforeRepositoryCall(t *testing.T) {
	repo := &summaryGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/guests/summary:not-a-uuid", "")
	c.SetParamNames("key")
	c.SetParamValues("summary:not-a-uuid")

	require.NoError(t, GetGuests(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, repo.called)
}

func TestGetGuests_SummaryDoesNotAggregateWhenEventAccessIsDenied(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	repo := &summaryGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/guests/summary:"+eventID.String(), "")
	c.SetParamNames("key")
	c.SetParamValues("summary:" + eventID.String())
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: userID}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, ClientID: &clientID}, nil
		},
		CheckAccessRecursive: func(gotUserID, gotClientID uuid.UUID) (bool, string) {
			assert.Equal(t, userID, gotUserID)
			assert.Equal(t, clientID, gotClientID)
			return false, ""
		},
	})
	t.Cleanup(restore)

	require.NoError(t, GetGuests(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, repo.called)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
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
	setAuthzContext(t, c, uuid.Must(uuid.NewV4()), &models.Guest{ID: id, EventID: uuid.Must(uuid.NewV4()), FirstName: "Original"})
	require.NoError(t, UpdateGuest(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateGuest_StatusOnlyDoesNotRewriteRSVPGuestCount(t *testing.T) {
	repo := &captureUpdateGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{
		ID:             id,
		EventID:        eventID,
		FirstName:      "Ana",
		GuestsCount:    4,
		RSVPGuestCount: 0,
	}
	body := `{"status_id":"` + statusID.String() + `","rsvp_status":"confirmed","rsvp_method":"host"}`
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), body)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, eventID, guest)

	require.NoError(t, UpdateGuest(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.Equal(t, 0, repo.updated.RSVPGuestCount)
}

func TestUpdateGuest_ExplicitGuestsCountUpdatesRSVPGuestCount(t *testing.T) {
	repo := &captureUpdateGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{
		ID:             id,
		EventID:        eventID,
		FirstName:      "Ana",
		GuestsCount:    1,
		RSVPGuestCount: 1,
	}
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), `{"first_name":"Ana","guests_count":3}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, eventID, guest)

	require.NoError(t, UpdateGuest(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.Equal(t, 3, repo.updated.RSVPGuestCount)
}

func TestUpdateGuest_CamelGuestCountUpdatesRSVPGuestCount(t *testing.T) {
	repo := &captureUpdateGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{
		ID:             id,
		EventID:        eventID,
		FirstName:      "Ana",
		GuestsCount:    1,
		RSVPGuestCount: 1,
	}
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), `{"guestCount":"3"}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, eventID, guest)

	require.NoError(t, UpdateGuest(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.Equal(t, 3, repo.updated.GuestsCount)
	assert.Equal(t, 3, repo.updated.RSVPGuestCount)
}

func TestUpdateGuest_AcceptsStatusAndRSVPCamelAliases(t *testing.T) {
	repo := &captureUpdateGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{
		ID:        id,
		EventID:   eventID,
		FirstName: "Original",
	}
	body := `{"firstName":"Ana","guestStatusId":"` + statusID.String() + `","rsvpStatus":"confirmed","rsvpMethod":"host"}`
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), body)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, eventID, guest)

	require.NoError(t, UpdateGuest(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.Equal(t, "Ana", repo.updated.FirstName)
	assert.Equal(t, statusID, repo.updated.StatusID)
	assert.Equal(t, statusID, repo.updated.GuestStatusID)
	assert.Equal(t, "confirmed", repo.updated.RSVPStatus)
	assert.Equal(t, "host", repo.updated.RSVPMethod)
	assert.Contains(t, rec.Body.String(), `"status_id":"`+statusID.String()+`"`)
	assert.Contains(t, rec.Body.String(), `"guest_status_id":"`+statusID.String()+`"`)
}

func TestUpdateGuest_ReturnsHydratedStatusAliasesAfterUpdate(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	status := models.GuestStatus{
		ID:    statusID,
		Code:  "CONFIRMED",
		Label: "Confirmado",
		Color: "lime",
		Order: 2,
	}
	repo := &captureUpdateGuestRepo{
		existing: &models.Guest{
			ID:            id,
			EventID:       eventID,
			FirstName:     "Ana",
			GuestStatusID: uuid.Must(uuid.NewV4()),
			RSVPStatus:    "pending",
		},
		reloaded: &models.Guest{
			ID:            id,
			EventID:       eventID,
			FirstName:     "Ana",
			GuestStatusID: statusID,
			StatusID:      statusID,
			GuestStatus:   status,
			Status:        &status,
			RSVPStatus:    "confirmed",
			RSVPMethod:    "host",
		},
	}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	body := `{"status_id":"` + statusID.String() + `","rsvp_status":"confirmed","rsvp_method":"host"}`
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), body)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, eventID, repo.existing)

	require.NoError(t, UpdateGuest(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	var envelope struct {
		Data dtos.GuestResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, statusID, envelope.Data.StatusID)
	assert.Equal(t, statusID, envelope.Data.GuestStatusID)
	require.NotNil(t, envelope.Data.Status)
	require.NotNil(t, envelope.Data.GuestStatus)
	assert.Equal(t, "CONFIRMED", envelope.Data.Status.Code)
	assert.Equal(t, envelope.Data.Status.ID, envelope.Data.GuestStatus.ID)
}

func TestUpdateGuest_AcceptsPublicProfileAliases(t *testing.T) {
	repo := &captureUpdateGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{
		ID:        id,
		EventID:   eventID,
		FirstName: "Original",
		LastName:  "Guest",
	}
	body := `{"firstName":"Ana","lastName":"Garcia","displayOrder":"7","imageURL":"profiles/ana.webp","isHost":true}`
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), body)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, eventID, guest)

	require.NoError(t, UpdateGuest(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.Equal(t, "Ana", repo.updated.FirstName)
	assert.Equal(t, "Garcia", repo.updated.LastName)
	assert.Equal(t, 7, repo.updated.Order)
	assert.Equal(t, "profiles/ana.webp", repo.updated.ImageURL)
	assert.True(t, repo.updated.IsHost)
	assert.Contains(t, rec.Body.String(), `"order":7`)
	assert.Contains(t, rec.Body.String(), `"image_url":"profiles/ana.webp"`)
	assert.Contains(t, rec.Body.String(), `"is_host":true`)
}

func TestUpdateGuest_AcceptsPascalPublicProfileAliases(t *testing.T) {
	repo := &captureUpdateGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{
		ID:        id,
		EventID:   eventID,
		FirstName: "Original",
		LastName:  "Guest",
	}
	body := `{"FirstName":"Ana","LastName":"Garcia","GuestStatusID":"` + statusID.String() + `","RSVPStatus":"confirmed","RSVPMethod":"host","RSVPNotes":"Mesa cerca","GuestsCount":"3","MaxGuests":"5","DisplayOrder":"7","ImageURL":"profiles/ana.webp","IsHost":"true","Nickname":"Anita","Role":"graduate","Headline":"Ingenieria","Bio":"Bio publica","Signature":"Gracias","Notes":"Nota interna"}`
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), body)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, eventID, guest)

	require.NoError(t, UpdateGuest(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.Equal(t, "Ana", repo.updated.FirstName)
	assert.Equal(t, "Garcia", repo.updated.LastName)
	assert.Equal(t, statusID, repo.updated.StatusID)
	assert.Equal(t, statusID, repo.updated.GuestStatusID)
	assert.Equal(t, "confirmed", repo.updated.RSVPStatus)
	assert.Equal(t, "host", repo.updated.RSVPMethod)
	assert.Equal(t, "Mesa cerca", repo.updated.RSVPNotes)
	assert.Equal(t, 3, repo.updated.GuestsCount)
	assert.Equal(t, 3, repo.updated.RSVPGuestCount)
	assert.Equal(t, 5, repo.updated.MaxGuests)
	assert.Equal(t, 7, repo.updated.Order)
	assert.Equal(t, "profiles/ana.webp", repo.updated.ImageURL)
	assert.True(t, repo.updated.IsHost)
	assert.Equal(t, "Anita", repo.updated.Nickname)
	assert.Equal(t, "graduate", repo.updated.Role)
	assert.Equal(t, "Ingenieria", repo.updated.Headline)
	assert.Equal(t, "Bio publica", repo.updated.Bio)
	assert.Equal(t, "Gracias", repo.updated.Signature)
	assert.Equal(t, "Nota interna", repo.updated.Notes)
	assert.Contains(t, rec.Body.String(), `"guest_status_id":"`+statusID.String()+`"`)
	assert.Contains(t, rec.Body.String(), `"rsvp_guest_count":3`)
	assert.Contains(t, rec.Body.String(), `"rsvp_notes":"Mesa cerca"`)
	assert.Contains(t, rec.Body.String(), `"image_url":"profiles/ana.webp"`)
}

func TestUpdateGuest_PreservesDashboardInternalNotes(t *testing.T) {
	repo := &captureUpdateGuestRepo{}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guest := &models.Guest{
		ID:        id,
		EventID:   eventID,
		FirstName: "Ana",
		Notes:     "Nota anterior",
	}
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), `{"first_name":"Ana","notes":"Prefiere entrada lateral"}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, eventID, guest)

	require.NoError(t, UpdateGuest(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.Equal(t, "Prefiere entrada lateral", repo.updated.Notes)
	assert.Contains(t, rec.Body.String(), `"notes":"Prefiere entrada lateral"`)
}

func TestUpdateGuest_InvalidBody_Returns400(t *testing.T) {
	orig := guestSvc
	guestSvc = nil
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), `{invalid json}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, uuid.Must(uuid.NewV4()), &models.Guest{ID: id, EventID: uuid.Must(uuid.NewV4()), FirstName: "Original"})
	require.NoError(t, UpdateGuest(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateGuest_ValidationError_MissingFirstName_Returns400(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+id.String(), `{"first_name":""}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setAuthzContext(t, c, uuid.Must(uuid.NewV4()), &models.Guest{ID: id, EventID: uuid.Must(uuid.NewV4()), FirstName: "Original"})
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
	setAuthzContext(t, c, uuid.Nil, nil)
	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestBulkUpdateGuestStatus_UpdatesSelectionInOneServiceCall(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	guestIDs := []uuid.UUID{uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())}
	var capturedIDs []uuid.UUID
	repo := &mockGuestRepo{
		bulkUpdateGuestStatusFunc: func(gotEventID uuid.UUID, ids []uuid.UUID, gotStatusID uuid.UUID, rsvpStatus, rsvpMethod string) error {
			assert.Equal(t, eventID, gotEventID)
			assert.Equal(t, statusID, gotStatusID)
			assert.Equal(t, "confirmed", rsvpStatus)
			assert.Equal(t, "host", rsvpMethod)
			capturedIDs = append([]uuid.UUID(nil), ids...)
			return nil
		},
	}
	svc := guestsService.NewGuestService(repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	body := `{"event_id":"` + eventID.String() + `","ids":["` + guestIDs[0].String() + `","` + guestIDs[1].String() + `"],"status_id":"` + statusID.String() + `","rsvp_status":"confirmed","rsvp_method":"host"}`
	c, rec := newEchoCtx(http.MethodPatch, "/guests/bulk/status", body)
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, BulkUpdateGuestStatus(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, guestIDs, capturedIDs)
}

// ── GetAttendees ──────────────────────────────────────────────────────────────

func TestCreateGuests_AcceptsTypeScriptAliasesInBatch(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	body := `[{"firstName":"Bob","lastName":"Rivera","eventId":"` + eventID.String() + `","guestCount":2}]`
	c, rec := newEchoCtx(http.MethodPost, "/guests/batch", body)
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"first_name":"Bob"`)
	assert.Contains(t, rec.Body.String(), `"last_name":"Rivera"`)
	assert.Contains(t, rec.Body.String(), `"guests_count":2`)
	assert.Contains(t, rec.Body.String(), `"rsvp_guest_count":2`)
}

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

func TestGetAttendees_PrivateEventWithoutAccessReturns403(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	calledAttendees := false
	deps := attendeeDeps{
		getSection: func(id uuid.UUID) (*models.EventSection, error) {
			return &models.EventSection{ID: sectionID, EventID: eventID}, nil
		},
		getAttendees: func(id uuid.UUID) ([]models.Guest, error) {
			calledAttendees = true
			return nil, nil
		},
		allowAccess: func(c echo.Context, id uuid.UUID) (bool, error) {
			assert.Equal(t, eventID, id)
			return false, nil
		},
	}

	c, rec := newEchoCtx(http.MethodGet, "/events/section/"+sectionID.String()+"/attendees", "")
	c.SetParamNames("sectionId")
	c.SetParamValues(sectionID.String())
	require.NoError(t, handleGetAttendees(deps, c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, calledAttendees)
}

func TestGetAttendees_AllowsPrivateEventWithPrettyTokenQuery(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockTokenRepo{
		pretty: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	deps := attendeeDeps{
		getSection: func(id uuid.UUID) (*models.EventSection, error) {
			return &models.EventSection{ID: sectionID, EventID: eventID, IsVisible: true}, nil
		},
		getAttendees: func(id uuid.UUID) ([]models.Guest, error) {
			assert.Equal(t, eventID, id)
			return []models.Guest{{FirstName: "Ana", LastName: "Garcia"}}, nil
		},
		allowAccess: func(c echo.Context, id uuid.UUID) (bool, error) {
			assert.Equal(t, eventID, id)
			return publicaccess.AllowEventReadFromRequest(c, id, publicaccess.EventReadDeps{
				ConfigRepo:     &mockGuestConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
				TokenRepo:      tokenRepo,
				InvitationRepo: &mockGuestInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
			})
		},
	}

	c, rec := newEchoCtx(http.MethodGet, "/events/section/"+sectionID.String()+"/attendees?prettyToken=PRETTY%2F123", "")
	c.SetParamNames("sectionId")
	c.SetParamValues(sectionID.String())
	require.NoError(t, handleGetAttendees(deps, c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "PRETTY/123", tokenRepo.seen)
	assert.Equal(t, "PRETTY/123", tokenRepo.prettySeen)
	assert.Contains(t, rec.Body.String(), "Ana")
}

func TestGetAttendees_HiddenSectionReturns403(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	calledAttendees := false
	deps := attendeeDeps{
		getSection: func(id uuid.UUID) (*models.EventSection, error) {
			return &models.EventSection{
				ID:            sectionID,
				EventID:       eventID,
				ComponentType: "GraduatesList",
				IsVisible:     false,
			}, nil
		},
		getAttendees: func(id uuid.UUID) ([]models.Guest, error) {
			calledAttendees = true
			return nil, nil
		},
		allowAccess: func(c echo.Context, id uuid.UUID) (bool, error) {
			assert.Equal(t, eventID, id)
			return true, nil
		},
	}

	c, rec := newEchoCtx(http.MethodGet, "/events/section/"+sectionID.String()+"/attendees", "")
	c.SetParamNames("sectionId")
	c.SetParamValues(sectionID.String())
	require.NoError(t, handleGetAttendees(deps, c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Section is not public")
	assert.False(t, calledAttendees)
}

func TestGetAttendees_SectionHiddenByEventConfigReturns403(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	calledAttendees := false
	section := &models.EventSection{
		ID:            sectionID,
		EventID:       eventID,
		ComponentType: "GraduatesList",
		IsVisible:     true,
	}
	deps := attendeeDeps{
		getSection: func(id uuid.UUID) (*models.EventSection, error) {
			return section, nil
		},
		getAttendees: func(id uuid.UUID) ([]models.Guest, error) {
			calledAttendees = true
			return nil, nil
		},
		allowAccess: func(c echo.Context, id uuid.UUID) (bool, error) {
			assert.Equal(t, eventID, id)
			return true, nil
		},
		sectionVisible: func(section *models.EventSection) (bool, error) {
			return eventsService.PageSpecSectionVisible(section.ComponentType, &models.EventConfig{
				IsPublic:         true,
				ShowHeader:       true,
				ShowHostsSection: false,
			}), nil
		},
	}

	c, rec := newEchoCtx(http.MethodGet, "/events/section/"+sectionID.String()+"/attendees", "")
	c.SetParamNames("sectionId")
	c.SetParamValues(sectionID.String())
	require.NoError(t, handleGetAttendees(deps, c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Section is not public")
	assert.False(t, calledAttendees)
}

func TestGuestPublicEventReadDeps_BlocksInactivePublicEvent(t *testing.T) {
	origConfigRepo := guestConfigRepo
	origTokenRepo := guestAccessTokenRepo
	origInvitationRepo := guestInvitationRepo
	origEventRepo := guestEventRepo
	t.Cleanup(func() {
		guestConfigRepo = origConfigRepo
		guestAccessTokenRepo = origTokenRepo
		guestInvitationRepo = origInvitationRepo
		guestEventRepo = origEventRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	guestConfigRepo = &mockGuestConfigRepo{cfg: &models.EventConfig{IsPublic: true}}
	guestAccessTokenRepo = nil
	guestInvitationRepo = nil
	guestEventRepo = &mockGuestEventRepo{event: &models.Event{ID: eventID, IsActive: false}}

	c, _ := newEchoCtx(http.MethodGet, "/events/section/"+uuid.Must(uuid.NewV4()).String()+"/attendees", "")
	allowed, err := publicaccess.AllowEventReadFromRequest(c, eventID, guestPublicEventReadDeps())

	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestGuestPublicEventReadDeps_BlocksPasswordProtectedPublicEventWithoutProof(t *testing.T) {
	origConfigRepo := guestConfigRepo
	origTokenRepo := guestAccessTokenRepo
	origInvitationRepo := guestInvitationRepo
	origEventRepo := guestEventRepo
	t.Cleanup(func() {
		guestConfigRepo = origConfigRepo
		guestAccessTokenRepo = origTokenRepo
		guestInvitationRepo = origInvitationRepo
		guestEventRepo = origEventRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	guestConfigRepo = &mockGuestConfigRepo{cfg: &models.EventConfig{
		ID:                  eventID,
		IsPublic:            true,
		AuthPasswordPreview: "secreto",
	}}
	guestAccessTokenRepo = nil
	guestInvitationRepo = nil
	guestEventRepo = &mockGuestEventRepo{event: &models.Event{ID: eventID, IsActive: true}}

	c, _ := newEchoCtx(http.MethodGet, "/events/section/"+uuid.Must(uuid.NewV4()).String()+"/attendees", "")
	allowed, err := publicaccess.AllowEventReadFromRequest(c, eventID, guestPublicEventReadDeps())

	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestGuestPublicEventReadDeps_AllowsPasswordProtectedPublicEventWithProof(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	origConfigRepo := guestConfigRepo
	origTokenRepo := guestAccessTokenRepo
	origInvitationRepo := guestInvitationRepo
	origEventRepo := guestEventRepo
	t.Cleanup(func() {
		guestConfigRepo = origConfigRepo
		guestAccessTokenRepo = origTokenRepo
		guestInvitationRepo = origInvitationRepo
		guestEventRepo = origEventRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{
		ID:                  eventID,
		IsPublic:            true,
		AuthPasswordPreview: "secreto",
	}
	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Hour)
	require.NoError(t, err)
	guestConfigRepo = &mockGuestConfigRepo{cfg: cfg}
	guestAccessTokenRepo = nil
	guestInvitationRepo = nil
	guestEventRepo = &mockGuestEventRepo{event: &models.Event{ID: eventID, IsActive: true}}

	c, _ := newEchoCtx(http.MethodGet, "/events/section/"+uuid.Must(uuid.NewV4()).String()+"/attendees", "")
	c.Request().Header.Set("X-Event-Access-Token", proof)
	allowed, err := publicaccess.AllowEventReadFromRequest(c, eventID, guestPublicEventReadDeps())

	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestGetAttendees_ReturnsPublicAttendeeContract(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	section := &models.EventSection{EventID: eventID, IsVisible: true}
	guests := []models.Guest{
		{
			ID:                  uuid.Must(uuid.NewV4()),
			EventID:             eventID,
			FirstName:           "Ana",
			LastName:            "Garcia",
			Email:               "ana@example.com",
			Phone:               "+525511111111",
			Role:                "Graduado",
			Order:               1,
			DietaryRestrictions: "Sin nueces",
			Notes:               "Nota privada del dashboard",
			ImageURL:            "profiles/ana.webp",
			Headline:            "Ingenieria en Sistemas",
			Bio:                 "Apasionada por la tecnologia",
			Signature:           "Gracias por acompanarme",
			IsHost:              true,
		},
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

	var body struct {
		Message string                `json:"message"`
		Data    []dtos.PublicAttendee `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Attendees loaded", body.Message)
	require.Len(t, body.Data, 1)
	assert.Equal(t, dtos.PublicAttendee{
		FirstName:    "Ana",
		LastName:     "Garcia",
		Role:         "Graduado",
		Order:        1,
		ImageURL:     "profiles/ana.webp",
		ImageViewURL: "profiles/ana.webp",
		Headline:     "Ingenieria en Sistemas",
		Bio:          "Apasionada por la tecnologia",
		Signature:    "Gracias por acompanarme",
	}, body.Data[0])

	payload := rec.Body.String()
	assert.NotContains(t, payload, "ana@example.com")
	assert.NotContains(t, payload, "phone")
	assert.NotContains(t, payload, "dietary_restrictions")
	assert.NotContains(t, payload, "Nota privada")
	assert.NotContains(t, payload, `"notes"`)
	assert.NotContains(t, payload, "guest_status")
	assert.NotContains(t, payload, "event_id")
	assert.NotContains(t, payload, "is_host")
}

func TestGetAttendees_AddsSignedPublicAttendeeImageViewURL(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	imageExpiresAt := time.Date(2026, 7, 8, 18, 0, 0, 0, time.UTC)
	deps := attendeeDeps{
		getSection: func(id uuid.UUID) (*models.EventSection, error) {
			return &models.EventSection{EventID: eventID, IsVisible: true}, nil
		},
		getAttendees: func(id uuid.UUID) ([]models.Guest, error) {
			assert.Equal(t, eventID, id)
			return []models.Guest{{
				FirstName: "Ana",
				LastName:  "Garcia",
				ImageURL:  "profiles/ana.webp",
				Order:     1,
			}}, nil
		},
		imageViewURL: func(path string) (string, *time.Time) {
			assert.Equal(t, "profiles/ana.webp", path)
			return "https://signed.example.com/profiles/ana.webp", &imageExpiresAt
		},
	}

	c, rec := newEchoCtx(http.MethodGet, "/events/section/"+sectionID.String()+"/attendees", "")
	c.SetParamNames("sectionId")
	c.SetParamValues(sectionID.String())
	require.NoError(t, handleGetAttendees(deps, c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Data []dtos.PublicAttendee `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	assert.Equal(t, "profiles/ana.webp", body.Data[0].ImageURL)
	assert.Equal(t, "https://signed.example.com/profiles/ana.webp", body.Data[0].ImageViewURL)
	require.NotNil(t, body.Data[0].ImageViewURLExpiresAt)
	assert.Equal(t, imageExpiresAt, *body.Data[0].ImageViewURLExpiresAt)
}

func TestGetAttendees_HostSectionFiltersOnlyHosts(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	section := &models.EventSection{EventID: eventID, ComponentType: "HostsSection", IsVisible: true}
	guests := []models.Guest{
		{FirstName: "Ana", LastName: "Host", Role: "host", Order: 1},
		{FirstName: "Luis", LastName: "Marcado", IsHost: true, Order: 2},
		{FirstName: "Mateo", LastName: "Acento", Role: "Anfitri\u00f3n", Order: 3},
		{FirstName: "Sofia", LastName: "Cohost", Role: "co-host", Order: 4},
		{FirstName: "Valeria", LastName: "Invitada", Role: "guest", Order: 5},
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

	var body struct {
		Data []dtos.PublicAttendee `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 4)
	assert.Equal(t, "Ana", body.Data[0].FirstName)
	assert.Equal(t, "Luis", body.Data[1].FirstName)
	assert.Equal(t, "Mateo", body.Data[2].FirstName)
	assert.Equal(t, "Sofia", body.Data[3].FirstName)
	assert.NotContains(t, rec.Body.String(), "Valeria")
	assert.NotContains(t, rec.Body.String(), "is_host")
}

func TestGetAttendees_HostAliasFiltersOnlyHosts(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	section := &models.EventSection{EventID: eventID, ComponentType: "HOST", IsVisible: true}
	guests := []models.Guest{
		{FirstName: "Ana", LastName: "Host", Role: "host", Order: 1},
		{FirstName: "Valeria", LastName: "Invitada", Role: "guest", Order: 2},
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

	var body struct {
		Data []dtos.PublicAttendee `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	assert.Equal(t, "Ana", body.Data[0].FirstName)
	assert.NotContains(t, rec.Body.String(), "Valeria")
}

func TestGetAttendees_GraduatesListFiltersGraduatesWhenRolesExist(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	section := &models.EventSection{EventID: eventID, ComponentType: "GraduatesList", IsVisible: true}
	guests := []models.Guest{
		{FirstName: "Ana", LastName: "Graduada", Role: "graduate", Order: 1},
		{FirstName: "Luis", LastName: "Graduado", Role: "Graduado", Order: 2},
		{FirstName: "Mar", LastName: "Student", Role: "Graduate Student", Order: 3},
		{FirstName: "Valeria", LastName: "Invitada", Role: "guest", Order: 4},
		{FirstName: "Mario", LastName: "Host", Role: "host", Order: 5},
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

	var body struct {
		Data []dtos.PublicAttendee `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 3)
	assert.Equal(t, "Ana", body.Data[0].FirstName)
	assert.Equal(t, "Luis", body.Data[1].FirstName)
	assert.Equal(t, "Mar", body.Data[2].FirstName)
	assert.NotContains(t, rec.Body.String(), "Valeria")
	assert.NotContains(t, rec.Body.String(), "Mario")
}

func TestGetAttendees_GraduatesListAliasFiltersGraduatesWhenRolesExist(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	section := &models.EventSection{EventID: eventID, ComponentType: "GRADUATES_LIST", IsVisible: true}
	guests := []models.Guest{
		{FirstName: "Ana", LastName: "Graduada", Role: "graduate", Order: 1},
		{FirstName: "Valeria", LastName: "Invitada", Role: "guest", Order: 2},
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

	var body struct {
		Data []dtos.PublicAttendee `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	assert.Equal(t, "Ana", body.Data[0].FirstName)
	assert.NotContains(t, rec.Body.String(), "Valeria")
}

func TestGetAttendees_GraduatesListKeepsLegacyGuestsWhenNoGraduateRolesExist(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	section := &models.EventSection{EventID: eventID, ComponentType: "GraduatesList", IsVisible: true}
	guests := []models.Guest{
		{FirstName: "Ana", LastName: "SinRol", Order: 1},
		{FirstName: "Luis", LastName: "Invitado", Role: "guest", Order: 2},
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

	var body struct {
		Data []dtos.PublicAttendee `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 2)
	assert.Equal(t, "Ana", body.Data[0].FirstName)
	assert.Equal(t, "Luis", body.Data[1].FirstName)
}

func TestGetAttendees_Success_Returns200(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	section := &models.EventSection{EventID: eventID, IsVisible: true}
	guests := []models.Guest{
		{
			FirstName: "Ana",
			LastName:  "García",
			Role:      "Graduado",
			Order:     1,
			Headline:  "Ingeniería en Sistemas",
			Bio:       "Apasionada por la tecnología",
			Signature: "Gracias por acompañarme",
		},
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
	assert.Contains(t, rec.Body.String(), "Ingeniería en Sistemas")
	assert.Contains(t, rec.Body.String(), "Apasionada por la tecnología")
	assert.Contains(t, rec.Body.String(), "Gracias por acompañarme")
}
