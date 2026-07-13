package events

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/previewtoken"
	"events-stocks/internal/publicaccessproof"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	"io"
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

func setRootAuth(t *testing.T, c echo.Context) {
	t.Helper()
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, IsActive: true}, nil
		},
	})
	t.Cleanup(restore)
}

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockEventsRepo struct {
	CreateEventFunc          func(event *models.Event) error
	UpdateEventFunc          func(event *models.Event) error
	GetEventByIDRawFunc      func(id uuid.UUID) (*models.Event, error)
	GetEventByIDForSpecFunc  func(id uuid.UUID) (*models.Event, error)
	GetEventByIdentifierFunc func(identifier string) (*models.Event, error)
	UpdateEventCoverFunc     func(id uuid.UUID, coverImageURL string) error
}

func (m *mockEventsRepo) CreateEvent(event *models.Event) error {
	if m.CreateEventFunc != nil {
		return m.CreateEventFunc(event)
	}
	return nil
}
func (m *mockEventsRepo) UpdateEvent(event *models.Event) error {
	if m.UpdateEventFunc != nil {
		return m.UpdateEventFunc(event)
	}
	return nil
}
func (m *mockEventsRepo) DeleteEvent(id uuid.UUID) error                            { return nil }
func (m *mockEventsRepo) ListEvents(_ int, _ int, _ string) ([]models.Event, error) { return nil, nil }
func (m *mockEventsRepo) GetEventByID(_ uuid.UUID) (string, error)                  { return `[{}]`, nil }
func (m *mockEventsRepo) GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	if m.GetEventByIDRawFunc != nil {
		return m.GetEventByIDRawFunc(id)
	}
	return &models.Event{ID: id, IsActive: true}, nil
}
func (m *mockEventsRepo) GetEventByIDForSpec(id uuid.UUID) (*models.Event, error) {
	if m.GetEventByIDForSpecFunc != nil {
		return m.GetEventByIDForSpecFunc(id)
	}
	return &models.Event{ID: id, IsActive: true}, nil
}
func (m *mockEventsRepo) GetEventByIdentifier(identifier string) (*models.Event, error) {
	if m.GetEventByIdentifierFunc != nil {
		return m.GetEventByIdentifierFunc(identifier)
	}
	return &models.Event{IsActive: true}, nil
}
func (m *mockEventsRepo) GetEventsByClientID(_ uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockEventsRepo) GetAllEventsForDashboard() ([]models.Event, error) {
	return nil, nil
}
func (m *mockEventsRepo) GetEventsForUser(_ uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockEventsRepo) UpdateEventCover(id uuid.UUID, coverImageURL string) error {
	if m.UpdateEventCoverFunc != nil {
		return m.UpdateEventCoverFunc(id, coverImageURL)
	}
	return nil
}
func (m *mockEventsRepo) IdentifierExists(_ string) bool { return false }

var _ ports.EventsRepository = (*mockEventsRepo)(nil)

func TestBuildDashboardOverviewBoundsPayloadAndPreservesMetrics(t *testing.T) {
	now := time.Date(2026, time.July, 10, 18, 0, 0, 0, time.UTC)
	capacity := 100
	events := make([]models.Event, 0, 9)
	for i := 0; i < 7; i++ {
		events = append(events, models.Event{
			ID:            uuid.Must(uuid.NewV4()),
			Name:          "Active event",
			IsActive:      true,
			MaxGuests:     &capacity,
			Timezone:      "America/Mexico_City",
			EventDateTime: now.Add(time.Duration(i+1) * 24 * time.Hour),
		})
	}
	events = append(events,
		models.Event{ID: uuid.Must(uuid.NewV4()), IsActive: true, MaxGuests: &capacity, Timezone: "UTC", EventDateTime: now.Add(-48 * time.Hour)},
		models.Event{ID: uuid.Must(uuid.NewV4()), IsActive: false, MaxGuests: &capacity, Timezone: "UTC", EventDateTime: now},
	)

	overview := eventsService.BuildDashboardOverview(events, now)

	assert.Equal(t, 9, overview.Metrics.Total)
	assert.Equal(t, 8, overview.Metrics.Active)
	assert.Equal(t, 7, overview.Metrics.Upcoming)
	assert.Equal(t, 1, overview.Metrics.PastActive)
	assert.Equal(t, 900, overview.Metrics.TotalCapacity)
	require.Len(t, overview.ActiveEvents, 5)
	require.NotNil(t, overview.NextEvent)
	assert.Equal(t, events[0].ID, overview.NextEvent.ID)
}

type mockCoverStorage struct {
	deletedFilename string
	deletedFolder   string
	presignedCalls  int
}

func (m *mockCoverStorage) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	return true, "", nil
}
func (m *mockCoverStorage) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	m.presignedCalls++
	return "https://signed.example.com/" + folder + "/" + filename, nil
}
func (m *mockCoverStorage) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	return "", nil
}
func (m *mockCoverStorage) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	return "", nil
}
func (m *mockCoverStorage) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return "", nil
}
func (m *mockCoverStorage) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	return nil
}
func (m *mockCoverStorage) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	return nil
}
func (m *mockCoverStorage) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	return "", nil
}
func (m *mockCoverStorage) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	return nil
}
func (m *mockCoverStorage) DeleteFile(filename, folder, bucket, provider string) error {
	m.deletedFilename = filename
	m.deletedFolder = folder
	return nil
}
func (m *mockCoverStorage) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

var _ ports.ObjectStorageRepository = (*mockCoverStorage)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(_, _ string) error                                  { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error         { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error)            { return "", nil }
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

type mockEventConfigRepo struct {
	cfg *models.EventConfig
	err error
}

func (m *mockEventConfigRepo) CreateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockEventConfigRepo) UpdateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockEventConfigRepo) DeleteEventConfig(id uuid.UUID) error            { return nil }
func (m *mockEventConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	return m.cfg, m.err
}

var _ ports.EventConfigRepository = (*mockEventConfigRepo)(nil)

type mockEventSectionRepo struct {
	sections []models.EventSection
}

func (m *mockEventSectionRepo) CreateEventSection(section *models.EventSection) error { return nil }
func (m *mockEventSectionRepo) UpdateEventSection(section *models.EventSection) error { return nil }
func (m *mockEventSectionRepo) DeleteEventSection(id uuid.UUID) error                 { return nil }
func (m *mockEventSectionRepo) BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error {
	return nil
}
func (m *mockEventSectionRepo) GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	return &models.EventSection{ID: id}, nil
}
func (m *mockEventSectionRepo) ListEventSections() ([]models.EventSection, error) {
	return nil, nil
}
func (m *mockEventSectionRepo) ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	return nil, nil
}
func (m *mockEventSectionRepo) ListByEventIDForSpec(eventID uuid.UUID) ([]models.EventSection, error) {
	return m.sections, nil
}

var _ ports.EventSectionRepository = (*mockEventSectionRepo)(nil)

type mockAccessTokenRepo struct {
	token      *models.InvitationAccessToken
	err        error
	pretty     *models.InvitationAccessToken
	prettyErr  error
	seen       string
	prettySeen string
}

func (m *mockAccessTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	m.seen = token
	return m.token, m.err
}
func (m *mockAccessTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	m.prettySeen = code
	return m.pretty, m.prettyErr
}
func (m *mockAccessTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABC123", nil
}

var _ ports.AccessTokenRepository = (*mockAccessTokenRepo)(nil)

type mockInvitationRepo struct {
	invitation *models.Invitation
	err        error
}

func (m *mockInvitationRepo) CreateInvitation(invitation *models.Invitation) error { return nil }
func (m *mockInvitationRepo) UpdateInvitation(invitation *models.Invitation) error { return nil }
func (m *mockInvitationRepo) DeleteInvitation(id uuid.UUID) error                  { return nil }
func (m *mockInvitationRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, m.err
}
func (m *mockInvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, m.err
}
func (m *mockInvitationRepo) ListInvitations() ([]models.Invitation, error) { return nil, nil }
func (m *mockInvitationRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return nil, nil
}

var _ ports.InvitationRepository = (*mockInvitationRepo)(nil)

type mockEventAnalyticsRepo struct {
	adjusted chan analyticsAdjustment
}

type analyticsAdjustment struct {
	eventID uuid.UUID
	field   string
	delta   int
}

func (m *mockEventAnalyticsRepo) CreateEventAnalytics(_ *models.EventAnalytics) error { return nil }
func (m *mockEventAnalyticsRepo) UpdateEventAnalytics(_ *models.EventAnalytics) error { return nil }
func (m *mockEventAnalyticsRepo) DeleteEventAnalytics(_ uuid.UUID) error              { return nil }
func (m *mockEventAnalyticsRepo) GetEventAnalyticsByID(_ uuid.UUID) (*models.EventAnalytics, error) {
	return &models.EventAnalytics{}, nil
}
func (m *mockEventAnalyticsRepo) GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	return &models.EventAnalytics{EventID: eventID}, nil
}
func (m *mockEventAnalyticsRepo) ListEventAnalyticss() ([]models.EventAnalytics, error) {
	return nil, nil
}
func (m *mockEventAnalyticsRepo) AdjustEventAnalytics(eventID uuid.UUID, field string, delta int) error {
	if m.adjusted != nil {
		m.adjusted <- analyticsAdjustment{eventID: eventID, field: field, delta: delta}
	}
	return nil
}

var _ ports.EventAnalyticsRepository = (*mockEventAnalyticsRepo)(nil)

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestGetPageSpec_ExpiredTokenReturns401(t *testing.T) {
	t.Cleanup(func() {
		eventsService.SetDefaultPageSpecService(nil)
	})

	expiredAt := time.Now().Add(-time.Hour)
	eventsService.SetDefaultPageSpecService(eventsService.NewPageSpecService(
		&mockAccessTokenRepo{
			token: &models.InvitationAccessToken{
				InvitationID: uuid.Must(uuid.NewV4()),
				ExpiresAt:    &expiredAt,
			},
		},
		&mockInvitationRepo{},
		&mockEventsRepo{},
		&mockEventSectionRepo{},
		&mockEventConfigRepo{},
	))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/page-spec?token=expired", "")

	require.NoError(t, GetPageSpec(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Invalid invitation token"`)
}

func TestGetPageSpec_InactiveEventReturns403(t *testing.T) {
	t.Cleanup(func() {
		eventsService.SetDefaultPageSpecService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventsService.SetDefaultPageSpecService(eventsService.NewPageSpecService(
		&mockAccessTokenRepo{
			token: &models.InvitationAccessToken{InvitationID: invitationID},
		},
		&mockInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
		&mockEventsRepo{
			GetEventByIDForSpecFunc: func(id uuid.UUID) (*models.Event, error) {
				return &models.Event{ID: id, Identifier: "evento-apagado", IsActive: false}, nil
			},
		},
		&mockEventSectionRepo{},
		&mockEventConfigRepo{},
	))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/page-spec?token=ABC123", "")

	require.NoError(t, GetPageSpec(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
}

func TestGetPageSpec_PasswordProtectedTokenUsesProofHeader(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	t.Cleanup(func() {
		eventsService.SetDefaultPageSpecService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{
		ID:                  eventID,
		IsPublic:            true,
		AuthPasswordPreview: "secreto",
		ShowCountdown:       true,
		ShowContactSection:  true,
		ShowFooter:          true,
		UpdatedAt:           time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC),
	}
	eventsService.SetDefaultPageSpecService(eventsService.NewPageSpecService(
		&mockAccessTokenRepo{
			token: &models.InvitationAccessToken{InvitationID: invitationID},
		},
		&mockInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
		&mockEventsRepo{
			GetEventByIDForSpecFunc: func(id uuid.UUID) (*models.Event, error) {
				return &models.Event{
					ID:            id,
					Name:          "Boda Privada",
					Identifier:    "boda-privada",
					IsActive:      true,
					CoverImageURL: "events/private/cover.webp",
					OrganizerName: "Eventi",
				}, nil
			},
		},
		&mockEventSectionRepo{sections: []models.EventSection{
			{ID: sectionID, EventID: eventID, ComponentType: "CountdownHeader", Config: `{}`, Order: 1, IsVisible: true},
		}},
		&mockEventConfigRepo{cfg: cfg},
	))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/page-spec?token=ABC123", "")

	require.NoError(t, GetPageSpec(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var lockedBody struct {
		Data dtos.PageSpec `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &lockedBody))
	assert.Equal(t, "Boda Privada", lockedBody.Data.Meta.PageTitle)
	assert.Empty(t, lockedBody.Data.Sections)
	require.NotNil(t, lockedBody.Data.Meta.Access)
	assert.True(t, lockedBody.Data.Meta.Access.PasswordProtected)
	assert.False(t, lockedBody.Data.Meta.Access.PasswordVerified)
	assert.Empty(t, lockedBody.Data.Meta.CoverImageURL)
	assert.Nil(t, lockedBody.Data.Meta.Contact)
	assert.False(t, lockedBody.Data.Meta.FooterVisible)

	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Hour)
	require.NoError(t, err)

	c, rec = newEchoCtx(http.MethodGet, "/api/events/page-spec?token=ABC123", "")
	c.Request().Header.Set("X-Event-Access-Token", proof)

	require.NoError(t, GetPageSpec(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var fullBody struct {
		Data dtos.PageSpec `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fullBody))
	require.Len(t, fullBody.Data.Sections, 1)
	assert.Equal(t, "CountdownHeader", fullBody.Data.Sections[0].Type)
	assert.Equal(t, "events/private/cover.webp", fullBody.Data.Meta.CoverImageURL)
	require.NotNil(t, fullBody.Data.Meta.Contact)
	require.NotNil(t, fullBody.Data.Meta.Access)
	assert.True(t, fullBody.Data.Meta.Access.PasswordVerified)
	assert.True(t, fullBody.Data.Meta.FooterVisible)
}

func TestGetPageSpecByIdentifier_InactiveEventReturns403(t *testing.T) {
	t.Cleanup(func() {
		eventsService.SetDefaultPageSpecService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	eventsService.SetDefaultPageSpecService(eventsService.NewPageSpecService(
		nil,
		nil,
		&mockEventsRepo{
			GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
				return &models.Event{ID: eventID, Identifier: identifier, IsActive: false}, nil
			},
		},
		&mockEventSectionRepo{},
		&mockEventConfigRepo{cfg: &models.EventConfig{ID: eventID, IsPublic: true}},
	))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-apagado/page-spec", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-apagado")

	require.NoError(t, GetPageSpecByIdentifier(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
}

func TestGetPageSpecByIdentifier_PasswordProtectedWithoutProofReturnsLockedSpec(t *testing.T) {
	t.Cleanup(func() {
		eventsService.SetDefaultPageSpecService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{
		ID:                  eventID,
		IsPublic:            true,
		AuthPasswordPreview: "secreto",
		ShowCountdown:       true,
		ShowContactSection:  true,
		ShowFooter:          true,
		UpdatedAt:           time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC),
	}
	eventsService.SetDefaultPageSpecService(eventsService.NewPageSpecService(
		nil,
		nil,
		&mockEventsRepo{
			GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
				return &models.Event{
					ID:            eventID,
					Name:          "Boda Privada",
					Identifier:    identifier,
					IsActive:      true,
					CoverImageURL: "events/private/cover.webp",
					OrganizerName: "Eventi",
				}, nil
			},
		},
		&mockEventSectionRepo{sections: []models.EventSection{
			{ID: sectionID, EventID: eventID, ComponentType: "CountdownHeader", Config: `{}`, Order: 1, IsVisible: true},
		}},
		&mockEventConfigRepo{cfg: cfg},
	))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/boda-privada/page-spec", "")
	c.SetParamNames("identifier")
	c.SetParamValues("boda-privada")

	require.NoError(t, GetPageSpecByIdentifier(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Data dtos.PageSpec `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Boda Privada", body.Data.Meta.PageTitle)
	assert.Empty(t, body.Data.Sections)
	require.NotNil(t, body.Data.Meta.Access)
	assert.True(t, body.Data.Meta.Access.PasswordProtected)
	assert.False(t, body.Data.Meta.Access.PasswordVerified)
	assert.Empty(t, body.Data.Meta.CoverImageURL)
	assert.Nil(t, body.Data.Meta.Contact)
	assert.False(t, body.Data.Meta.FooterVisible)
}

func TestGetPageSpecByIdentifier_PasswordProtectedWithProofReturnsFullSpec(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	t.Cleanup(func() {
		eventsService.SetDefaultPageSpecService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{
		ID:                  eventID,
		IsPublic:            true,
		AuthPasswordPreview: "secreto",
		ShowCountdown:       true,
		ShowContactSection:  true,
		ShowFooter:          true,
		UpdatedAt:           time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC),
	}
	eventsService.SetDefaultPageSpecService(eventsService.NewPageSpecService(
		nil,
		nil,
		&mockEventsRepo{
			GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
				return &models.Event{
					ID:            eventID,
					Name:          "Boda Privada",
					Identifier:    identifier,
					IsActive:      true,
					CoverImageURL: "events/private/cover.webp",
					OrganizerName: "Eventi",
				}, nil
			},
		},
		&mockEventSectionRepo{sections: []models.EventSection{
			{ID: sectionID, EventID: eventID, ComponentType: "CountdownHeader", Config: `{}`, Order: 1, IsVisible: true},
		}},
		&mockEventConfigRepo{cfg: cfg},
	))
	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Hour)
	require.NoError(t, err)

	c, rec := newEchoCtx(http.MethodGet, "/api/events/boda-privada/page-spec", "")
	c.SetParamNames("identifier")
	c.SetParamValues("boda-privada")
	c.Request().Header.Set("X-Event-Access-Token", proof)

	require.NoError(t, GetPageSpecByIdentifier(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Data dtos.PageSpec `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data.Sections, 1)
	assert.Equal(t, "CountdownHeader", body.Data.Sections[0].Type)
	assert.Equal(t, "events/private/cover.webp", body.Data.Meta.CoverImageURL)
	require.NotNil(t, body.Data.Meta.Contact)
	require.NotNil(t, body.Data.Meta.Access)
	assert.True(t, body.Data.Meta.Access.PasswordVerified)
	assert.True(t, body.Data.Meta.FooterVisible)
}

func TestGetEvents_ReturnsPublicEventWithoutRedisCache(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			require.Equal(t, "boda-demo", identifier)
			return &models.Event{
				ID:         eventID,
				Name:       "Boda Demo",
				Identifier: identifier,
				IsActive:   true,
				EventType:  models.EventType{Name: "wedding"},
			}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true},
	}, &mockCacheRepo{})
	coverResourceSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/boda-demo", "")
	c.SetParamNames("key")
	c.SetParamValues("boda-demo")

	require.NoError(t, GetEvents(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Message string               `json:"message"`
		Data    []dtos.EventResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Event loaded", body.Message)
	require.Len(t, body.Data, 1)
	assert.Equal(t, eventID, body.Data[0].ID)
	assert.Equal(t, "Boda Demo", body.Data[0].Name)
	assert.Equal(t, "boda-demo", body.Data[0].Identifier)
}

func TestGetEvents_BlocksPrivateEventWithoutToken(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Name: "Privado", Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/privado", "")
	c.SetParamNames("key")
	c.SetParamValues("privado")

	require.NoError(t, GetEvents(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
}

func TestGetEvents_AllowsPrivateEventWithInvitationToken(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Name: "Privado", Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = &mockAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	c, rec := newEchoCtx(http.MethodGet, "/api/events/privado?token=ABC123", "")
	c.SetParamNames("key")
	c.SetParamValues("privado")

	require.NoError(t, GetEvents(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"name":"Privado"`)
}

func TestCreateEvent_InvalidJSON_Returns400(t *testing.T) {
	orig := eventSvc
	eventSvc = nil
	defer func() { eventSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/events", `{invalid json}`)
	setRootAuth(t, c)
	require.NoError(t, CreateEvent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateEvent_ValidationError_Returns400(t *testing.T) {
	orig := eventSvc
	eventSvc = nil
	defer func() { eventSvc = orig }()

	// missing required "name" and "identifier" fields
	c, rec := newEchoCtx(http.MethodPost, "/events", `{}`)
	setRootAuth(t, c)
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

func TestIssuePreviewTokenReturnsTypedContract(t *testing.T) {
	t.Setenv("EVENT_PREVIEW_SECRET", "test-preview-secret")
	eventID := uuid.Must(uuid.NewV4())

	c, rec := newEchoCtx(http.MethodPost, "/events/"+eventID.String()+"/preview-token", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, IssuePreviewToken(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status  int                       `json:"status"`
		Message string                    `json:"message"`
		Data    dtos.PreviewTokenResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Preview token created", body.Message)
	assert.NotEmpty(t, body.Data.Token)
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), body.Data.ExpiresAt, 5*time.Second)
	assert.True(t, previewtoken.Validate(body.Data.Token, eventID))
}

func TestListPhrasesReturnsTypedContractAndCapsCount(t *testing.T) {
	c, rec := newEchoCtx(http.MethodGet, "/events/phrases?type=%20BODA%20&count=60", "")

	require.NoError(t, ListPhrases(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status  int                       `json:"status"`
		Message string                    `json:"message"`
		Data    dtos.EventPhrasesResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Phrases loaded", body.Message)
	require.Len(t, body.Data.Phrases, 50)
	assert.Equal(t, "public, max-age=3600, stale-while-revalidate=86400", rec.Header().Get(echo.HeaderCacheControl))
	seen := make(map[string]bool, len(body.Data.Phrases))
	for _, phrase := range body.Data.Phrases {
		assert.NotEmpty(t, strings.TrimSpace(phrase))
		assert.False(t, seen[phrase], "phrases must not repeat in one response")
		seen[phrase] = true
	}
}

func TestPhrasesByTypeNormalizesCaseAndAccents(t *testing.T) {
	phrases := phrasesByType(" GRADUACIÓN ")

	require.GreaterOrEqual(t, len(phrases), 15)
	assert.Contains(t, phrases[0], "costó")
}

func TestCreateEvent_ValidRequest_Returns201(t *testing.T) {
	svc := eventsService.NewEventService(&mockEventsRepo{}, &mockCacheRepo{})
	orig := eventSvc
	eventSvc = svc
	defer func() { eventSvc = orig }()

	clientID := uuid.Must(uuid.NewV4())
	body := `{"name":"Test Event","identifier":"test-event","client_id":"` + clientID.String() + `"}`
	c, rec := newEchoCtx(http.MethodPost, "/events", body)
	setRootAuth(t, c)
	require.NoError(t, CreateEvent(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestCreateEvent_AcceptsEmptyOptionalDashboardFields(t *testing.T) {
	var captured *models.Event
	svc := eventsService.NewEventService(&mockEventsRepo{
		CreateEventFunc: func(event *models.Event) error {
			captured = event
			return nil
		},
	}, &mockCacheRepo{})
	orig := eventSvc
	eventSvc = svc
	defer func() { eventSvc = orig }()

	clientID := uuid.Must(uuid.NewV4())
	body := `{"name":"Test Event","identifier":"","client_id":"` + clientID.String() + `","event_type_id":"","max_guests":null,"event_date_time":"","is_active":true}`
	c, rec := newEchoCtx(http.MethodPost, "/events", body)
	setRootAuth(t, c)

	require.NoError(t, CreateEvent(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "test-event", captured.Identifier)
	require.NotNil(t, captured.ClientID)
	assert.Equal(t, clientID, *captured.ClientID)
	assert.Equal(t, uuid.Nil, captured.EventTypeID)
	assert.Nil(t, captured.MaxGuests)
	assert.True(t, captured.EventDateTime.IsZero())
	assert.True(t, captured.IsActive)
}

func TestDeleteEventCover_ClearsCoverAndDeletesOldObject(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	var updatedCover string
	storage := &mockCoverStorage{}
	svc := eventsService.NewEventService(&mockEventsRepo{
		GetEventByIDRawFunc: func(id uuid.UUID) (*models.Event, error) {
			require.Equal(t, eventID, id)
			return &models.Event{ID: id, CoverImageURL: "events/old-cover.webp"}, nil
		},
		UpdateEventCoverFunc: func(id uuid.UUID, coverImageURL string) error {
			require.Equal(t, eventID, id)
			updatedCover = coverImageURL
			return nil
		},
	}, &mockCacheRepo{})
	resSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)

	origEventSvc := eventSvc
	origCoverResourceSvc := coverResourceSvc
	eventSvc = svc
	coverResourceSvc = resSvc
	t.Cleanup(func() {
		eventSvc = origEventSvc
		coverResourceSvc = origCoverResourceSvc
	})

	c, rec := newEchoCtx(http.MethodDelete, "/events/"+eventID.String()+"/cover", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, DeleteEventCover(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, updatedCover)
	assert.Equal(t, "old-cover.webp", storage.deletedFilename)
	assert.Equal(t, "events", storage.deletedFolder)

	var body struct {
		Status  int                     `json:"status"`
		Message string                  `json:"message"`
		Data    dtos.EventCoverResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Cover image removed", body.Message)
	assert.Equal(t, dtos.EventCoverResponse{}, body.Data)
}

func TestEventResponseWithCoverViewPreservesRawCoverAndAddsViewURLs(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	storage := &mockCoverStorage{}
	resSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)

	origCoverResourceSvc := coverResourceSvc
	coverResourceSvc = resSvc
	t.Cleanup(func() {
		coverResourceSvc = origCoverResourceSvc
	})

	response := eventResponseWithCoverView(&models.Event{
		ID:             eventID,
		CoverImageURL:  "events/evt-001/cover.webp",
		CoverImageURL2: "events/evt-001/cover-2.webp",
	})

	assert.Equal(t, "events/evt-001/cover.webp", response.CoverImageURL)
	assert.Equal(t, "events/evt-001/cover-2.webp", response.CoverImageURL2)
	assert.Equal(t, "https://signed.example.com/events/evt-001/cover.webp", response.CoverViewURL)
	assert.Equal(t, "https://signed.example.com/events/evt-001/cover-2.webp", response.CoverViewURL2)
	assert.Equal(t, response.CoverViewURL, response.ViewURL)
	require.NotNil(t, response.CoverViewURLExpiresAt)
	require.NotNil(t, response.CoverViewURL2ExpiresAt)
	require.NotNil(t, response.ViewURLExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(eventCoverViewURLTTLMinutes*time.Minute), *response.CoverViewURLExpiresAt, 2*time.Second)
	assert.Equal(t, response.CoverViewURLExpiresAt, response.ViewURLExpiresAt)
}

func TestWithPageSpecCoverViewURLPreservesRawCoverAndAddsViewMetadata(t *testing.T) {
	storage := &mockCoverStorage{}
	resSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)

	origCoverResourceSvc := coverResourceSvc
	coverResourceSvc = resSvc
	t.Cleanup(func() {
		coverResourceSvc = origCoverResourceSvc
	})

	spec := &dtos.PageSpec{
		Meta: dtos.PageSpecMeta{
			PageTitle:     "Evento",
			CoverImageURL: "events/event-1/cover.webp",
			Theme: &dtos.PageSpecTheme{
				Fonts: map[string]string{
					"heading": "Cormorant Garamond",
				},
				FontURLs: map[string]string{
					"heading": "base/fonts/cormorant.woff2",
				},
			},
		},
	}

	response := withPageSpecCoverViewURL(spec)

	assert.Equal(t, "events/event-1/cover.webp", spec.Meta.CoverImageURL)
	assert.Nil(t, spec.Meta.CoverImageURLExpiresAt)
	require.NotNil(t, spec.Meta.Theme)
	assert.Nil(t, spec.Meta.Theme.FontViewURLs)
	assert.Nil(t, spec.Meta.Theme.FontViewURLsExpiresAt)
	require.NotNil(t, response)
	require.NotNil(t, response.Sections)
	assert.Empty(t, response.Sections)
	assert.Equal(t, "events/event-1/cover.webp", response.Meta.CoverImageURL)
	assert.Nil(t, response.Meta.CoverImageURLExpiresAt)
	assert.Equal(t, "https://signed.example.com/events/event-1/cover.webp", response.Meta.CoverViewURL)
	require.NotNil(t, response.Meta.CoverViewURLExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(eventCoverViewURLTTLMinutes*time.Minute), *response.Meta.CoverViewURLExpiresAt, 2*time.Second)
	require.NotNil(t, response.Meta.Theme)
	assert.Equal(t, "base/fonts/cormorant.woff2", response.Meta.Theme.FontURLs["heading"])
	assert.Equal(t, "https://signed.example.com/base/fonts/cormorant.woff2", response.Meta.Theme.FontViewURLs["heading"])
	require.NotNil(t, response.Meta.Theme.FontViewURLsExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(resourcesService.ResourceViewURLTTLMinutes*time.Minute), *response.Meta.Theme.FontViewURLsExpiresAt, 2*time.Second)
}

func TestResourceViewURLWithExpiryPreservesAbsoluteURLLikeValues(t *testing.T) {
	storage := &mockCoverStorage{}
	resSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)

	origCoverResourceSvc := coverResourceSvc
	coverResourceSvc = resSvc
	t.Cleanup(func() {
		coverResourceSvc = origCoverResourceSvc
	})

	for _, value := range []string{
		"https://cdn.example.com/events/cover.webp",
		"//cdn.example.com/events/cover.webp",
		"blob:https://app.example/cover-id",
		"data:image/svg+xml;base64,PHN2Zy8+",
	} {
		viewURL, expiresAt := resourceViewURLWithExpiry(value, eventCoverViewURLTTLMinutes)
		assert.Equal(t, value, viewURL)
		assert.Nil(t, expiresAt)
	}
	assert.Zero(t, storage.presignedCalls)
}

func TestVerifyEventAccess_BlocksPrivateEventWithoutToken(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false, AuthPasswordPreview: "secreto"},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-privado/verify-access", `{"password":"secreto"}`)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, VerifyEventAccess(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
}

func TestVerifyEventAccess_AllowsPrivateEventWithMatchingInvitationToken(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false, AuthPasswordPreview: "secreto"},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = &mockAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-privado/verify-access?token=ABC123", `{"password":"secreto"}`)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, VerifyEventAccess(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Access granted"`)
	assert.Contains(t, rec.Body.String(), `"accessToken"`)
}

func TestVerifyEventAccess_AllowsPrivateEventWithPrettyTokenAlias(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false, AuthPasswordPreview: "secreto"},
	}, &mockCacheRepo{})
	tokenRepo := &mockAccessTokenRepo{
		err:    errors.New("raw token not found"),
		pretty: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	eventAccessTokenRepo = tokenRepo
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-privado/verify-access?pretty_token=PRETTY123", `{"password":"secreto"}`)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, VerifyEventAccess(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Access granted"`)
	assert.Contains(t, rec.Body.String(), `"accessToken"`)
	assert.Equal(t, "PRETTY123", tokenRepo.seen)
	assert.Equal(t, "PRETTY123", tokenRepo.prettySeen)
}

func TestVerifyEventAccess_TrimsConfiguredAndSubmittedPassword(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true, AuthPasswordPreview: "  secreto  "},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-publico/verify-access", `{"password":"  secreto  "}`)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-publico")

	require.NoError(t, VerifyEventAccess(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Access granted"`)
	assert.Contains(t, rec.Body.String(), `"accessToken"`)
}

func TestGetEventMeta_ReturnsPublicOgContract(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
		eventsService.SetDefaultPageSpecService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	eventDate := time.Date(2026, time.August, 15, 20, 30, 0, 0, time.UTC)
	contentUpdatedAt := time.Date(2026, time.July, 9, 12, 0, 0, 0, time.UTC)
	eventRepo := &mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			require.Equal(t, "boda-demo", identifier)
			return &models.Event{
				ID:            eventID,
				Name:          "Boda Demo",
				Description:   "Celebra con nosotros",
				Identifier:    identifier,
				CoverImageURL: "events/boda-demo/cover.webp",
				EventDateTime: eventDate,
				Address:       "Salon Central",
				SecondAddress: "Terraza Central",
				Timezone:      "America/Mexico_City",
				Language:      "es",
				OrganizerName: "Ana y Luis",
				EventType:     models.EventType{Name: "wedding"},
				IsActive:      true,
			}, nil
		},
	}
	configRepo := &mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true, UpdatedAt: contentUpdatedAt},
	}
	eventSvc = eventsService.NewEventService(eventRepo, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(configRepo, &mockCacheRepo{})
	eventsService.SetDefaultPageSpecService(eventsService.NewPageSpecService(
		&mockAccessTokenRepo{},
		&mockInvitationRepo{},
		eventRepo,
		&mockEventSectionRepo{},
		configRepo,
	))
	coverResourceSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/boda-demo/meta", "")
	c.SetParamNames("identifier")
	c.SetParamValues("boda-demo")

	require.NoError(t, GetEventMeta(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Message string         `json:"message"`
		Data    dtos.EventMeta `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Event meta loaded", body.Message)
	assert.Equal(t, "Boda Demo", body.Data.Name)
	assert.Equal(t, "boda-demo", body.Data.Identifier)
	assert.Equal(t, "Celebra con nosotros", body.Data.Description)
	assert.Equal(t, "events/boda-demo/cover.webp", body.Data.CoverImageURL)
	assert.Equal(t, "events/boda-demo/cover.webp", body.Data.CoverViewURL)
	assert.Equal(t, "events/boda-demo/cover.webp", body.Data.ViewURL)
	assert.Nil(t, body.Data.CoverViewURLExpiresAt)
	assert.Nil(t, body.Data.ViewURLExpiresAt)
	require.NotNil(t, body.Data.EventDateTime)
	assert.Equal(t, eventDate, *body.Data.EventDateTime)
	assert.Equal(t, "Salon Central", body.Data.Address)
	assert.Equal(t, "Terraza Central", body.Data.SecondAddress)
	assert.Equal(t, "America/Mexico_City", body.Data.Timezone)
	assert.Equal(t, "es", body.Data.Language)
	assert.Equal(t, "Ana y Luis", body.Data.OrganizerName)
	assert.Equal(t, "wedding", body.Data.EventType)
	assert.Equal(t, contentUpdatedAt.UTC().Format(time.RFC3339Nano), body.Data.ContentVersion)
}

func TestGetEventMeta_BlocksPasswordProtectedEventWithoutProof(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Name: "Evento Protegido", Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true, AuthPasswordPreview: "secret"},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil
	coverResourceSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/protegido/meta", "")
	c.SetParamNames("identifier")
	c.SetParamValues("protegido")

	require.NoError(t, GetEventMeta(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
}

func TestGetEventMeta_AllowsPasswordProtectedEventWithProof(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{ID: eventID, IsPublic: true, AuthPasswordPreview: "secret"}
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{
				ID:            eventID,
				Name:          "Evento Protegido",
				Identifier:    identifier,
				CoverImageURL: "events/protegido/cover.webp",
				IsActive:      true,
			}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{cfg: cfg}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil
	coverResourceSvc = nil

	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Minute)
	require.NoError(t, err)

	c, rec := newEchoCtx(http.MethodGet, "/api/events/protegido/meta", "")
	c.SetParamNames("identifier")
	c.SetParamValues("protegido")
	c.Request().Header.Set("X-Event-Access-Token", proof)

	require.NoError(t, GetEventMeta(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Data dtos.EventMeta `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Evento Protegido", body.Data.Name)
	assert.Equal(t, "events/protegido/cover.webp", body.Data.CoverImageURL)
}

func TestGetEventMeta_PreservesRawCoverAndAddsSignedViewURL(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			require.Equal(t, "boda-demo", identifier)
			return &models.Event{
				ID:            eventID,
				Name:          "Boda Demo",
				Identifier:    identifier,
				CoverImageURL: "events/boda-demo/cover.webp",
				IsActive:      true,
			}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true},
	}, &mockCacheRepo{})
	coverResourceSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockCoverStorage{}},
	)

	c, rec := newEchoCtx(http.MethodGet, "/api/events/boda-demo/meta", "")
	c.SetParamNames("identifier")
	c.SetParamValues("boda-demo")

	require.NoError(t, GetEventMeta(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Data dtos.EventMeta `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "events/boda-demo/cover.webp", body.Data.CoverImageURL)
	assert.Equal(t, "https://signed.example.com/events/boda-demo/cover.webp", body.Data.CoverViewURL)
	assert.Equal(t, body.Data.CoverViewURL, body.Data.ViewURL)
	require.NotNil(t, body.Data.CoverViewURLExpiresAt)
	require.NotNil(t, body.Data.ViewURLExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(eventCoverViewURLTTLMinutes*time.Minute), *body.Data.CoverViewURLExpiresAt, 2*time.Second)
	assert.Equal(t, body.Data.CoverViewURLExpiresAt, body.Data.ViewURLExpiresAt)
}

func TestGetEventMeta_OmitsZeroEventDate(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{
				ID:         eventID,
				Name:       "Evento Sin Fecha",
				Identifier: identifier,
				IsActive:   true,
			}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true},
	}, &mockCacheRepo{})
	coverResourceSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/sin-fecha/meta", "")
	c.SetParamNames("identifier")
	c.SetParamValues("sin-fecha")

	require.NoError(t, GetEventMeta(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Evento Sin Fecha", data["name"])
	assert.NotContains(t, data, "event_date_time")
}

func TestGetEventMeta_AllowsPrivateEventWithInvitationToken(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			require.Equal(t, "privado", identifier)
			return &models.Event{
				ID:            eventID,
				Name:          "Evento Privado",
				Identifier:    identifier,
				CoverImageURL: "events/privado/cover.webp",
				IsActive:      true,
			}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = &mockAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	coverResourceSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/privado/meta?token=ABC123", "")
	c.SetParamNames("identifier")
	c.SetParamValues("privado")

	require.NoError(t, GetEventMeta(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Data dtos.EventMeta `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Evento Privado", body.Data.Name)
	assert.Equal(t, "events/privado/cover.webp", body.Data.CoverImageURL)
	assert.Equal(t, "events/privado/cover.webp", body.Data.CoverViewURL)
	assert.Nil(t, body.Data.CoverViewURLExpiresAt)
}

func TestGetEventMeta_BlocksPrivateEventWithExpiredInvitationToken(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	expiredAt := time.Now().Add(-time.Hour)
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Name: "Evento Privado", Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = &mockAccessTokenRepo{
		token: &models.InvitationAccessToken{
			InvitationID: invitationID,
			ExpiresAt:    &expiredAt,
		},
	}
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	coverResourceSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/privado/meta?token=expired", "")
	c.SetParamNames("identifier")
	c.SetParamValues("privado")

	require.NoError(t, GetEventMeta(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
}

func TestGetEventMeta_AllowsPrivateEventWithPrettyTokenAlias(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreCoverResourceSvc := coverResourceSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		coverResourceSvc = restoreCoverResourceSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{
				ID:         eventID,
				Name:       "Evento Privado",
				Identifier: identifier,
				IsActive:   true,
			}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	tokenRepo := &mockAccessTokenRepo{
		err:    errors.New("raw token not found"),
		pretty: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	eventAccessTokenRepo = tokenRepo
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	coverResourceSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/privado/meta?pretty_token=PRETTY123", "")
	c.SetParamNames("identifier")
	c.SetParamValues("privado")

	require.NoError(t, GetEventMeta(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "PRETTY123", tokenRepo.seen)
	assert.Equal(t, "PRETTY123", tokenRepo.prettySeen)
}

func TestTrackView_PublicEventIncrementsViews(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventsService.SetDefaultEventAnalyticsService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			require.Equal(t, "boda-demo", identifier)
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true},
	}, &mockCacheRepo{})

	adjusted := make(chan analyticsAdjustment, 1)
	eventsService.SetDefaultEventAnalyticsService(eventsService.NewEventAnalyticsService(
		&mockEventAnalyticsRepo{adjusted: adjusted},
		&mockCacheRepo{},
	))

	c, rec := newEchoCtx(http.MethodPost, "/api/events/boda-demo/view", "")
	c.SetParamNames("identifier")
	c.SetParamValues("boda-demo")

	require.NoError(t, TrackView(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Message string                         `json:"message"`
		Data    dtos.EventViewTrackingResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "View tracked", body.Message)
	assert.True(t, body.Data.Tracked)

	select {
	case got := <-adjusted:
		assert.Equal(t, eventID, got.eventID)
		assert.Equal(t, "views", got.field)
		assert.Equal(t, 1, got.delta)
	case <-time.After(time.Second):
		t.Fatal("expected TrackView to increment views")
	}
}

func TestTrackView_PrivateEventWithoutTokenDoesNotIncrementViews(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
		eventsService.SetDefaultEventAnalyticsService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil

	adjusted := make(chan analyticsAdjustment, 1)
	eventsService.SetDefaultEventAnalyticsService(eventsService.NewEventAnalyticsService(
		&mockEventAnalyticsRepo{adjusted: adjusted},
		&mockCacheRepo{},
	))

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-privado/view", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, TrackView(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Message string                         `json:"message"`
		Data    dtos.EventViewTrackingResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "View ignored", body.Message)
	assert.False(t, body.Data.Tracked)

	select {
	case got := <-adjusted:
		t.Fatalf("expected no analytics increment, got %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTrackView_UnknownEventReturnsIgnoredContract(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventsService.SetDefaultEventAnalyticsService(nil)
	})

	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			require.Equal(t, "evento-inexistente", identifier)
			return nil, errors.New("not found")
		},
	}, &mockCacheRepo{})
	eventConfigSvc = nil

	adjusted := make(chan analyticsAdjustment, 1)
	eventsService.SetDefaultEventAnalyticsService(eventsService.NewEventAnalyticsService(
		&mockEventAnalyticsRepo{adjusted: adjusted},
		&mockCacheRepo{},
	))

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-inexistente/view", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-inexistente")

	require.NoError(t, TrackView(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Message string                         `json:"message"`
		Data    dtos.EventViewTrackingResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "View ignored", body.Message)
	assert.False(t, body.Data.Tracked)

	select {
	case got := <-adjusted:
		t.Fatalf("expected no analytics increment, got %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTrackView_PrivateEventWithPrettyTokenIncrementsViews(t *testing.T) {
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
		eventsService.SetDefaultEventAnalyticsService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	tokenRepo := &mockAccessTokenRepo{
		pretty: &models.InvitationAccessToken{InvitationID: invitationID, PrettyToken: "PRETTY123"},
	}
	eventAccessTokenRepo = tokenRepo
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	adjusted := make(chan analyticsAdjustment, 1)
	eventsService.SetDefaultEventAnalyticsService(eventsService.NewEventAnalyticsService(
		&mockEventAnalyticsRepo{adjusted: adjusted},
		&mockCacheRepo{},
	))

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-privado/view?prettyToken=PRETTY123", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, TrackView(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "PRETTY123", tokenRepo.prettySeen)

	select {
	case got := <-adjusted:
		assert.Equal(t, eventID, got.eventID)
		assert.Equal(t, "views", got.field)
		assert.Equal(t, 1, got.delta)
	case <-time.After(time.Second):
		t.Fatal("expected TrackView to increment views for invitation-token access")
	}
}

func TestTrackView_PasswordProtectedEventWithProofIncrementsViews(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
		eventsService.SetDefaultEventAnalyticsService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{ID: eventID, IsPublic: true, AuthPasswordPreview: "secret"}
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{cfg: cfg}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil
	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Minute)
	require.NoError(t, err)

	adjusted := make(chan analyticsAdjustment, 1)
	eventsService.SetDefaultEventAnalyticsService(eventsService.NewEventAnalyticsService(
		&mockEventAnalyticsRepo{adjusted: adjusted},
		&mockCacheRepo{},
	))

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-privado/view", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")
	c.Request().Header.Set("X-Event-Access-Token", proof)

	require.NoError(t, TrackView(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	select {
	case got := <-adjusted:
		assert.Equal(t, eventID, got.eventID)
		assert.Equal(t, "views", got.field)
		assert.Equal(t, 1, got.delta)
	case <-time.After(time.Second):
		t.Fatal("expected TrackView to increment views for password proof access")
	}
}

func TestAllowPublicViewTracking_AllowsPublicEventWithoutToken(t *testing.T) {
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: uuid.Must(uuid.NewV4()), IsPublic: true},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil

	allowed, err := allowPublicViewTracking(&models.Event{ID: uuid.Must(uuid.NewV4()), IsActive: true}, "", "")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowPublicViewTracking_BlocksPrivateEventWithoutToken(t *testing.T) {
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: uuid.Must(uuid.NewV4()), IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil

	allowed, err := allowPublicViewTracking(&models.Event{ID: uuid.Must(uuid.NewV4()), IsActive: true}, "", "")
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowPublicViewTracking_AllowsPrivateEventWithMatchingInvitationToken(t *testing.T) {
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = &mockAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	allowed, err := allowPublicViewTracking(&models.Event{ID: eventID, IsActive: true}, "ABC123", "")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowPublicViewTracking_BlocksPrivateEventWithExpiredInvitationToken(t *testing.T) {
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	expiredAt := time.Now().Add(-time.Hour)
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = &mockAccessTokenRepo{
		token: &models.InvitationAccessToken{
			InvitationID: invitationID,
			ExpiresAt:    &expiredAt,
		},
	}
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	allowed, err := allowPublicViewTracking(&models.Event{ID: eventID, IsActive: true}, "expired", "")
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowPublicViewTracking_BlocksPrivateEventWithOtherEventToken(t *testing.T) {
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: false},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = &mockAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	eventInvitationRepo = &mockInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: uuid.Must(uuid.NewV4())},
	}

	allowed, err := allowPublicViewTracking(&models.Event{ID: eventID, IsActive: true}, "ABC123", "")
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowPublicViewTracking_BlocksInactivePublicEvent(t *testing.T) {
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil

	allowed, err := allowPublicViewTracking(&models.Event{ID: eventID, IsActive: false}, "", "")
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllowPublicViewTracking_AllowsPasswordProtectedEventWithProof(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{ID: eventID, IsPublic: true, AuthPasswordPreview: "secret"}
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{cfg: cfg}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil
	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Minute)
	require.NoError(t, err)

	allowed, err := allowPublicViewTracking(&models.Event{ID: eventID, IsActive: true}, "", proof)
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowPublicViewTracking_BlocksPasswordProtectedEventWithoutProof(t *testing.T) {
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	t.Cleanup(func() {
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true, AuthPasswordPreview: "secret"},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil

	allowed, err := allowPublicViewTracking(&models.Event{ID: eventID, IsActive: true}, "", "")
	require.NoError(t, err)
	assert.False(t, allowed)
}
