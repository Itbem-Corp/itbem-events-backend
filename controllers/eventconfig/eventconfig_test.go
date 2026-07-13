package eventconfig

import (
	"context"
	"encoding/json"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
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
			return &models.Event{ID: id}, nil
		},
	})
	t.Cleanup(restore)
}

type mockEventConfigRepo struct {
	configs []*models.EventConfig
	gets    int
	updated *models.EventConfig
}

func (m *mockEventConfigRepo) CreateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockEventConfigRepo) DeleteEventConfig(id uuid.UUID) error            { return nil }
func (m *mockEventConfigRepo) UpdateEventConfig(cfg *models.EventConfig) error {
	copied := *cfg
	m.updated = &copied
	return nil
}
func (m *mockEventConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	if len(m.configs) == 0 {
		return &models.EventConfig{ID: id}, nil
	}
	index := m.gets
	if index >= len(m.configs) {
		index = len(m.configs) - 1
	}
	m.gets++
	return m.configs[index], nil
}

var _ ports.EventConfigRepository = (*mockEventConfigRepo)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(_, _ string) error                                  { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error         { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error)            { return "", nil }
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

type mockEventConfigStorage struct{}

func (m *mockEventConfigStorage) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	return true, "", nil
}

func (m *mockEventConfigStorage) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	return "https://signed.example.com/" + folder + "/" + filename, nil
}

func (m *mockEventConfigStorage) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	return "", nil
}

func (m *mockEventConfigStorage) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	return "", nil
}

func (m *mockEventConfigStorage) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return "", nil
}

func (m *mockEventConfigStorage) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	return nil
}

func (m *mockEventConfigStorage) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	return nil
}

func (m *mockEventConfigStorage) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	return "", nil
}

func (m *mockEventConfigStorage) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	return nil
}

func (m *mockEventConfigStorage) DeleteFile(filename, folder, bucket, provider string) error {
	return nil
}

func (m *mockEventConfigStorage) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

var _ ports.ObjectStorageRepository = (*mockEventConfigStorage)(nil)

func TestGetEventConfigReturnsDashboardContract(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{
			{
				ID:                   eventID,
				IsPublic:             true,
				AuthPasswordPreview:  "secreto",
				ShowHeader:           true,
				ShareUploadsEnabled:  true,
				MaxUploadsPerGuest:   30,
				AutoApproveUploads:   false,
				NotifyOnMomentUpload: true,
			},
		},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/config", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, GetEventConfig(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].(map[string]interface{})
	assert.Equal(t, eventID.String(), data["id"])
	assert.Equal(t, eventID.String(), data["event_id"])
	assert.Equal(t, true, data["is_public"])
	assert.Equal(t, "secreto", data["auth_password_preview"])
	assert.Equal(t, true, data["share_uploads_enabled"])
	assert.NotContains(t, data, "active_from")
	assert.NotContains(t, rec.Body.String(), "deleted_at")
}

func TestGetEventConfigAddsSignedDesignCatalogMedia(t *testing.T) {
	previousSvc := eventConfigSvc
	previousResourceSvc := eventConfigResourceSvc
	t.Cleanup(func() {
		eventConfigSvc = previousSvc
		eventConfigResourceSvc = previousResourceSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	templateID := uuid.Must(uuid.NewV4())
	templateFontSetID := uuid.Must(uuid.NewV4())
	overrideFontSetID := uuid.Must(uuid.NewV4())
	templateFontID := uuid.Must(uuid.NewV4())
	overrideFontID := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{
			{
				ID:               eventID,
				DesignTemplateID: &templateID,
				DesignTemplate: &models.DesignTemplate{
					ID:         templateID,
					Name:       "Modern",
					Identifier: "modern-editorial",
					PreviewURL: "base/templates/modern.webp",
					FontSetID:  &templateFontSetID,
					FontSet: &models.FontSet{
						ID:   templateFontSetID,
						Name: "Template fonts",
						Patterns: []models.FontSetPattern{
							{
								Key: "heading",
								Font: models.Font{
									ID:       templateFontID,
									Name:     "Cormorant Garamond",
									Resource: models.Resource{Path: "base/fonts/cormorant.woff2"},
								},
							},
						},
					},
				},
				FontSetID: &overrideFontSetID,
				FontSet: &models.FontSet{
					ID:   overrideFontSetID,
					Name: "Override fonts",
					Patterns: []models.FontSetPattern{
						{
							Key: "body",
							Font: models.Font{
								ID:       overrideFontID,
								Name:     "Inter",
								Resource: models.Resource{Path: "base/fonts/inter.woff2"},
							},
						},
					},
				},
			},
		},
	}
	resourceSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockEventConfigStorage{}},
	)
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}), resourceSvc)

	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/config", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, GetEventConfig(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].(map[string]interface{})
	template := data["design_template"].(map[string]interface{})
	assert.Equal(t, "base/templates/modern.webp", template["preview_url"])
	assert.Equal(t, "https://signed.example.com/base/templates/modern.webp", template["preview_view_url"])
	assert.Contains(t, template, "preview_view_url_expires_at")

	defaultFontSet := template["default_font_set"].(map[string]interface{})
	defaultPattern := defaultFontSet["patterns"].([]interface{})[0].(map[string]interface{})
	defaultFont := defaultPattern["font"].(map[string]interface{})
	assert.Equal(t, "base/fonts/cormorant.woff2", defaultFont["url"])
	assert.Equal(t, "https://signed.example.com/base/fonts/cormorant.woff2", defaultFont["view_url"])
	assert.Contains(t, defaultFont, "view_url_expires_at")

	overrideFontSet := data["font_set"].(map[string]interface{})
	overridePattern := overrideFontSet["patterns"].([]interface{})[0].(map[string]interface{})
	overrideFont := overridePattern["font"].(map[string]interface{})
	assert.Equal(t, "base/fonts/inter.woff2", overrideFont["url"])
	assert.Equal(t, "https://signed.example.com/base/fonts/inter.woff2", overrideFont["view_url"])
	assert.Contains(t, overrideFont, "view_url_expires_at")
}

func TestGetEventConfigReturnsOnlyConfiguredAccessDates(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	activeFrom := time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC)
	activeUntil := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{
			{
				ID:          eventID,
				ActiveFrom:  activeFrom,
				ActiveUntil: &activeUntil,
			},
		},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/config", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, GetEventConfig(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].(map[string]interface{})
	assert.Equal(t, activeFrom.Format(time.RFC3339), data["active_from"])
	assert.Equal(t, activeUntil.Format(time.RFC3339), data["active_until"])
}

func TestUpdateEventConfigReturnsReloadedConfig(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	oldTemplateID := uuid.Must(uuid.NewV4())
	newTemplateID := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{
			{
				ID:               eventID,
				DesignTemplateID: &oldTemplateID,
				DesignTemplate:   &models.DesignTemplate{ID: oldTemplateID, Identifier: "old-template"},
			},
			{
				ID:               eventID,
				DesignTemplateID: &newTemplateID,
				DesignTemplate:   &models.DesignTemplate{ID: newTemplateID, Identifier: "new-template"},
			},
		},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	c, rec := newEchoCtx(
		http.MethodPut,
		"/events/"+eventID.String()+"/config",
		`{"design_template_id":"`+newTemplateID.String()+`"}`,
	)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, UpdateEventConfig(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	require.NotNil(t, repo.updated.DesignTemplateID)
	assert.Equal(t, newTemplateID, *repo.updated.DesignTemplateID)
	assert.Equal(t, 2, repo.gets)
	assert.Contains(t, rec.Body.String(), "new-template")
	assert.Contains(t, rec.Body.String(), `"event_id":"`+eventID.String()+`"`)
	assert.NotContains(t, rec.Body.String(), "old-template")
}

func TestUpdateEventConfigAppliesPatchAndIgnoresReadOnlyFields(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	otherID := uuid.Must(uuid.NewV4())
	activeUntil := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{
			{
				ID:                    eventID,
				IsPublic:              true,
				ShowFooter:            true,
				ActiveUntil:           &activeUntil,
				MaxUploadsPerGuest:    30,
				DefaultWelcomeMessage: "Anterior",
			},
			{
				ID:                    eventID,
				IsPublic:              false,
				ShowFooter:            false,
				MaxUploadsPerGuest:    0,
				DefaultWelcomeMessage: "Hola legacy",
			},
		},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	body := `{
		"id":"` + otherID.String() + `",
		"event_id":"` + otherID.String() + `",
		"is_public":false,
		"show_footer":false,
		"active_until":null,
		"welcome_message":"Hola legacy",
		"max_uploads_per_guest":0,
		"design_template":{"identifier":"no-debe-mutarse"}
	}`
	c, rec := newEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/config", body)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, UpdateEventConfig(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.Equal(t, eventID, repo.updated.ID)
	assert.False(t, repo.updated.IsPublic)
	assert.False(t, repo.updated.ShowFooter)
	assert.Nil(t, repo.updated.ActiveUntil)
	assert.Equal(t, "Hola legacy", repo.updated.DefaultWelcomeMessage)
	assert.Equal(t, 0, repo.updated.MaxUploadsPerGuest)
	assert.NotContains(t, rec.Body.String(), otherID.String())
}

func TestUpdateEventConfigPreservesExplicitAllFalseVisibility(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{
			{
				ID:                   eventID,
				ShowCountdown:        true,
				ShowRSVPSection:      true,
				ShowEventLocation:    true,
				ShowSecondLocation:   true,
				ShowHostsSection:     true,
				ShowPhotoGallery:     true,
				ShowMomentWall:       true,
				ShowContactSection:   true,
				ShowHeader:           true,
				ShowFooter:           true,
				ShowEventSchedule:    true,
				VisibilityConfigured: true,
			},
			{
				ID:                   eventID,
				VisibilityConfigured: true,
			},
		},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	body := `{
		"show_countdown":false,
		"show_rsvp_section":false,
		"show_event_location":false,
		"show_second_location":false,
		"show_hosts_section":false,
		"show_photo_gallery":false,
		"show_moment_wall":false,
		"show_contact_section":false,
		"show_header":false,
		"show_footer":false,
		"show_event_schedule":false
	}`
	c, rec := newEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/config", body)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, UpdateEventConfig(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.True(t, repo.updated.VisibilityConfigured)
	assert.False(t, repo.updated.ShowHeader)
	assert.False(t, repo.updated.ShowFooter)
	assert.False(t, repo.updated.ShowMomentWall)
	assert.Contains(t, rec.Body.String(), `"visibility_configured":true`)
	assert.Contains(t, rec.Body.String(), `"show_header":false`)
	assert.Contains(t, rec.Body.String(), `"show_footer":false`)
	assert.Contains(t, rec.Body.String(), `"show_moment_wall":false`)
}

func TestUpdateEventConfigSharedUploadsEnableOpenUploadContract(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{
			{
				ID:                   eventID,
				AllowUploads:         false,
				ShareUploadsEnabled:  false,
				ShowMomentWall:       true,
				ShowHeader:           true,
				ShowFooter:           true,
				ShowCountdown:        true,
				ShowRSVPSection:      true,
				ShowEventLocation:    true,
				ShowPhotoGallery:     true,
				ShowContactSection:   true,
				ShowEventSchedule:    true,
				ShowSecondLocation:   true,
				ShowHostsSection:     true,
				MaxUploadsPerGuest:   30,
				NotifyOnMomentUpload: true,
			},
			{
				ID:                  eventID,
				AllowUploads:        true,
				ShareUploadsEnabled: true,
				ShowMomentWall:      false,
				ShowHeader:          true,
				ShowFooter:          true,
				ShowCountdown:       true,
				ShowRSVPSection:     true,
				ShowEventLocation:   true,
				ShowPhotoGallery:    true,
				ShowContactSection:  true,
				ShowEventSchedule:   true,
				ShowSecondLocation:  true,
				ShowHostsSection:    true,
				MaxUploadsPerGuest:  30,
			},
		},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/config", `{"share_uploads_enabled":true}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, UpdateEventConfig(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.True(t, repo.updated.AllowUploads)
	assert.True(t, repo.updated.ShareUploadsEnabled)
	assert.False(t, repo.updated.ShowMomentWall)
	assert.Contains(t, rec.Body.String(), `"allow_uploads":true`)
	assert.Contains(t, rec.Body.String(), `"share_uploads_enabled":true`)
	assert.Contains(t, rec.Body.String(), `"show_moment_wall":false`)
}

func TestUpdateEventConfigAcceptsPublicMomentWallAlias(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{
			{
				ID:                   eventID,
				AllowUploads:         true,
				ShareUploadsEnabled:  true,
				ShowMomentWall:       false,
				ShowHeader:           true,
				ShowFooter:           true,
				ShowCountdown:        true,
				ShowRSVPSection:      true,
				ShowEventLocation:    true,
				ShowPhotoGallery:     true,
				ShowContactSection:   true,
				ShowEventSchedule:    true,
				ShowSecondLocation:   true,
				ShowHostsSection:     true,
				MaxUploadsPerGuest:   30,
				NotifyOnMomentUpload: true,
			},
			{
				ID:                  eventID,
				AllowUploads:        true,
				ShareUploadsEnabled: false,
				ShowMomentWall:      true,
				ShowHeader:          true,
				ShowFooter:          true,
				ShowCountdown:       true,
				ShowRSVPSection:     true,
				ShowEventLocation:   true,
				ShowPhotoGallery:    true,
				ShowContactSection:  true,
				ShowEventSchedule:   true,
				ShowSecondLocation:  true,
				ShowHostsSection:    true,
				MaxUploadsPerGuest:  30,
			},
		},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/config", `{"moments_wall_published":true}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, UpdateEventConfig(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	assert.True(t, repo.updated.ShowMomentWall)
	assert.False(t, repo.updated.ShareUploadsEnabled)
	assert.Contains(t, rec.Body.String(), `"show_moment_wall":true`)
	assert.Contains(t, rec.Body.String(), `"share_uploads_enabled":false`)
}

func TestUpdateEventConfigRejectsUnknownField(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{{ID: eventID}},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/config", `{"not_a_config_field":true}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, UpdateEventConfig(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, repo.updated)
	assert.Contains(t, rec.Body.String(), "unknown event config field")
}

func TestUpdateEventConfigRejectsInvalidActiveRange(t *testing.T) {
	previousSvc := eventConfigSvc
	t.Cleanup(func() { eventConfigSvc = previousSvc })

	eventID := uuid.Must(uuid.NewV4())
	repo := &mockEventConfigRepo{
		configs: []*models.EventConfig{{ID: eventID}},
	}
	InitEventConfigController(eventsService.NewEventConfigService(repo, &mockCacheRepo{}))

	c, rec := newEchoCtx(
		http.MethodPut,
		"/events/"+eventID.String()+"/config",
		`{"active_from":"2026-07-10T18:00:00Z","active_until":"2026-07-10T17:59:00Z"}`,
	)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuth(t, c)

	require.NoError(t, UpdateEventConfig(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, repo.updated)
	assert.Contains(t, rec.Body.String(), "active_until")
	assert.Contains(t, rec.Body.String(), "must be after active_from")
}
