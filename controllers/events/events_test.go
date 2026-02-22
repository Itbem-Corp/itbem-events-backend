package events

import (
	"context"
	"events-stocks/models"
	"events-stocks/services/ports"
	eventsService "events-stocks/services/events"
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

type mockEventsRepo struct{}

func (m *mockEventsRepo) CreateEvent(event *models.Event) error                                  { return nil }
func (m *mockEventsRepo) UpdateEvent(event *models.Event) error                                  { return nil }
func (m *mockEventsRepo) DeleteEvent(id uuid.UUID) error                                         { return nil }
func (m *mockEventsRepo) ListEvents(_ int, _ int, _ string) ([]models.Event, error)              { return nil, nil }
func (m *mockEventsRepo) GetEventByID(_ uuid.UUID) (string, error)                               { return `[{}]`, nil }
func (m *mockEventsRepo) IdentifierExists(_ string) bool                                         { return false }

var _ ports.EventsRepository = (*mockEventsRepo)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(_, _ string) error                                    { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error           { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error)              { return "", nil }
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error   { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestCreateEvent_InvalidJSON_Returns400(t *testing.T) {
	orig := eventSvc
	eventSvc = nil
	defer func() { eventSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/events", `{invalid json}`)
	require.NoError(t, CreateEvent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateEvent_ValidationError_Returns400(t *testing.T) {
	orig := eventSvc
	eventSvc = nil
	defer func() { eventSvc = orig }()

	// missing required "name" and "identifier" fields
	c, rec := newEchoCtx(http.MethodPost, "/events", `{}`)
	require.NoError(t, CreateEvent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeleteEvent_InvalidUUID_Returns400(t *testing.T) {
	orig := eventSvc
	eventSvc = nil
	defer func() { eventSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/events/bad-id", "")
	c.SetParamNames("id")
	c.SetParamValues("bad-id")
	require.NoError(t, DeleteEvent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateEvent_InvalidUUID_Returns400(t *testing.T) {
	orig := eventSvc
	eventSvc = nil
	defer func() { eventSvc = orig }()

	c, rec := newEchoCtx(http.MethodPut, "/events/bad-id", `{}`)
	c.SetParamNames("id")
	c.SetParamValues("bad-id")
	require.NoError(t, UpdateEvent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateEvent_ValidRequest_Returns201(t *testing.T) {
	svc := eventsService.NewEventService(&mockEventsRepo{}, &mockCacheRepo{})
	orig := eventSvc
	eventSvc = svc
	defer func() { eventSvc = orig }()

	body := `{"name":"Test Event","identifier":"test-event"}`
	c, rec := newEchoCtx(http.MethodPost, "/events", body)
	require.NoError(t, CreateEvent(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
}
