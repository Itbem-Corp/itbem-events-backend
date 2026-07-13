package eventsection

import (
	"context"
	"errors"
	"events-stocks/internal/authz"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
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
)

func newSectionEchoCtx(method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	e.Validator = customValidator.New()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func setSectionRootAuth(t *testing.T, sections map[uuid.UUID]models.EventSection) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id}, nil
		},
		GetEventSectionByID: func(id uuid.UUID) (*models.EventSection, error) {
			section, ok := sections[id]
			if !ok {
				return nil, errors.New("section not found")
			}
			return &section, nil
		},
	})
	t.Cleanup(restore)
}

type mockSectionRepo struct {
	UpdateEventSectionFunc func(obj *models.EventSection) error
	BulkUpdateOrderFunc    func(eventID uuid.UUID, updates map[uuid.UUID]int) error
}

func (m *mockSectionRepo) CreateEventSection(obj *models.EventSection) error { return nil }
func (m *mockSectionRepo) UpdateEventSection(obj *models.EventSection) error {
	if m.UpdateEventSectionFunc != nil {
		return m.UpdateEventSectionFunc(obj)
	}
	return nil
}
func (m *mockSectionRepo) DeleteEventSection(id uuid.UUID) error { return nil }
func (m *mockSectionRepo) BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error {
	if m.BulkUpdateOrderFunc != nil {
		return m.BulkUpdateOrderFunc(eventID, updates)
	}
	return nil
}
func (m *mockSectionRepo) GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	return &models.EventSection{ID: id}, nil
}
func (m *mockSectionRepo) ListEventSections() ([]models.EventSection, error) { return nil, nil }
func (m *mockSectionRepo) ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	return nil, nil
}
func (m *mockSectionRepo) ListByEventIDForSpec(eventID uuid.UUID) ([]models.EventSection, error) {
	return nil, nil
}

var _ ports.EventSectionRepository = (*mockSectionRepo)(nil)

type mockSectionCache struct{}

func (m *mockSectionCache) Invalidate(resource, key string) error { return nil }
func (m *mockSectionCache) DeleteKeysByPattern(ctx context.Context, pattern string) error {
	return nil
}
func (m *mockSectionCache) GetKey(ctx context.Context, key string) (string, error) {
	return "", errors.New("miss")
}
func (m *mockSectionCache) SaveKey(ctx context.Context, key, value string, ttl time.Duration) error {
	return nil
}

var _ ports.CacheRepository = (*mockSectionCache)(nil)

func TestUpdateSection_PartialConfigPreservesExistingFields(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	original := models.EventSection{
		ID:            sectionID,
		EventID:       eventID,
		Key:           "hosts",
		Title:         "Anfitriones",
		ComponentType: "HostSection",
		Config:        `{"closing":"Gracias"}`,
		Order:         7,
		IsVisible:     true,
	}
	setSectionRootAuth(t, map[uuid.UUID]models.EventSection{
		sectionID: original,
	})

	var updated *models.EventSection
	repo := &mockSectionRepo{
		UpdateEventSectionFunc: func(obj *models.EventSection) error {
			copy := *obj
			updated = &copy
			return nil
		},
	}
	orig := eventSectionSvc
	eventSectionSvc = eventsService.NewEventSectionService(repo, &mockSectionCache{})
	t.Cleanup(func() { eventSectionSvc = orig })

	c, rec := newSectionEchoCtx(http.MethodPut, "/sections/"+sectionID.String(), `{"config":{"closing":"Nuevo cierre"}}`)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(sectionID.String())

	require.NoError(t, UpdateSection(c))
	require.NotNil(t, updated)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, sectionID, updated.ID)
	assert.Equal(t, eventID, updated.EventID)
	assert.Equal(t, "hosts", updated.Key)
	assert.Equal(t, "Anfitriones", updated.Title)
	assert.Equal(t, "HostSection", updated.ComponentType)
	assert.Equal(t, 7, updated.Order)
	assert.True(t, updated.IsVisible)
	assert.JSONEq(t, `{"closing":"Nuevo cierre"}`, updated.Config)
}

func TestReorderSections_ValidBody_Returns200(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	sectionA := uuid.Must(uuid.NewV4())
	sectionB := uuid.Must(uuid.NewV4())
	setSectionRootAuth(t, map[uuid.UUID]models.EventSection{
		sectionA: {ID: sectionA, EventID: eventID},
		sectionB: {ID: sectionB, EventID: eventID},
	})

	var gotEventID uuid.UUID
	var gotUpdates map[uuid.UUID]int
	repo := &mockSectionRepo{
		BulkUpdateOrderFunc: func(eventID uuid.UUID, updates map[uuid.UUID]int) error {
			gotEventID = eventID
			gotUpdates = updates
			return nil
		},
	}
	orig := eventSectionSvc
	eventSectionSvc = eventsService.NewEventSectionService(repo, &mockSectionCache{})
	t.Cleanup(func() { eventSectionSvc = orig })

	body := `{"sections":[{"id":"` + sectionA.String() + `","order":2},{"id":"` + sectionB.String() + `","order":1}]}`
	c, rec := newSectionEchoCtx(http.MethodPatch, "/events/"+eventID.String()+"/sections/reorder", body)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())

	require.NoError(t, ReorderSections(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, eventID, gotEventID)
	assert.Equal(t, map[uuid.UUID]int{sectionA: 2, sectionB: 1}, gotUpdates)
}

func TestReorderSections_AcceptsSortOrderAliases(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	sectionA := uuid.Must(uuid.NewV4())
	sectionB := uuid.Must(uuid.NewV4())
	setSectionRootAuth(t, map[uuid.UUID]models.EventSection{
		sectionA: {ID: sectionA, EventID: eventID},
		sectionB: {ID: sectionB, EventID: eventID},
	})

	var gotUpdates map[uuid.UUID]int
	repo := &mockSectionRepo{
		BulkUpdateOrderFunc: func(eventID uuid.UUID, updates map[uuid.UUID]int) error {
			gotUpdates = updates
			return nil
		},
	}
	orig := eventSectionSvc
	eventSectionSvc = eventsService.NewEventSectionService(repo, &mockSectionCache{})
	t.Cleanup(func() { eventSectionSvc = orig })

	body := `{"sections":[{"id":"` + sectionA.String() + `","sortOrder":2},{"id":"` + sectionB.String() + `","sort_order":1}]}`
	c, rec := newSectionEchoCtx(http.MethodPatch, "/events/"+eventID.String()+"/sections/reorder", body)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())

	require.NoError(t, ReorderSections(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[uuid.UUID]int{sectionA: 2, sectionB: 1}, gotUpdates)
}

func TestReorderSections_RejectsSectionFromAnotherEvent(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	otherEventID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	setSectionRootAuth(t, map[uuid.UUID]models.EventSection{
		sectionID: {ID: sectionID, EventID: otherEventID},
	})

	orig := eventSectionSvc
	eventSectionSvc = eventsService.NewEventSectionService(&mockSectionRepo{}, &mockSectionCache{})
	t.Cleanup(func() { eventSectionSvc = orig })

	body := `{"sections":[{"id":"` + sectionID.String() + `","order":1}]}`
	c, rec := newSectionEchoCtx(http.MethodPatch, "/events/"+eventID.String()+"/sections/reorder", body)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())

	require.NoError(t, ReorderSections(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Section does not belong to event")
}

func TestReorderSections_MissingOrderReturns400(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	setSectionRootAuth(t, map[uuid.UUID]models.EventSection{
		sectionID: {ID: sectionID, EventID: eventID},
	})

	orig := eventSectionSvc
	eventSectionSvc = eventsService.NewEventSectionService(&mockSectionRepo{}, &mockSectionCache{})
	t.Cleanup(func() { eventSectionSvc = orig })

	body := `{"sections":[{"id":"` + sectionID.String() + `"}]}`
	c, rec := newSectionEchoCtx(http.MethodPatch, "/events/"+eventID.String()+"/sections/reorder", body)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())

	require.NoError(t, ReorderSections(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Missing section order")
}

func TestReorderSections_InvalidBody_Returns400(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	setSectionRootAuth(t, map[uuid.UUID]models.EventSection{})

	orig := eventSectionSvc
	eventSectionSvc = eventsService.NewEventSectionService(&mockSectionRepo{}, &mockSectionCache{})
	t.Cleanup(func() { eventSectionSvc = orig })

	c, rec := newSectionEchoCtx(http.MethodPatch, "/events/"+eventID.String()+"/sections/reorder", `{invalid json}`)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())

	require.NoError(t, ReorderSections(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
