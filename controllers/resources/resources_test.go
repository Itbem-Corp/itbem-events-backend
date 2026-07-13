package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/publicaccessproof"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"
	Resources "events-stocks/services/resources"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockResourceSectionRepo struct {
	section *models.EventSection
	err     error
}

func (m *mockResourceSectionRepo) CreateEventSection(section *models.EventSection) error { return nil }
func (m *mockResourceSectionRepo) UpdateEventSection(section *models.EventSection) error { return nil }
func (m *mockResourceSectionRepo) DeleteEventSection(id uuid.UUID) error                 { return nil }
func (m *mockResourceSectionRepo) BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error {
	return nil
}
func (m *mockResourceSectionRepo) GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	return m.section, m.err
}
func (m *mockResourceSectionRepo) ListEventSections() ([]models.EventSection, error) {
	return nil, nil
}
func (m *mockResourceSectionRepo) ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	return nil, nil
}
func (m *mockResourceSectionRepo) ListByEventIDForSpec(eventID uuid.UUID) ([]models.EventSection, error) {
	return nil, nil
}

var _ ports.EventSectionRepository = (*mockResourceSectionRepo)(nil)

type mockResourceEventRepo struct {
	event *models.Event
	err   error
}

func (m *mockResourceEventRepo) CreateEvent(event *models.Event) error { return nil }
func (m *mockResourceEventRepo) UpdateEvent(event *models.Event) error { return nil }
func (m *mockResourceEventRepo) DeleteEvent(id uuid.UUID) error        { return nil }
func (m *mockResourceEventRepo) ListEvents(page int, pageSize int, name string) ([]models.Event, error) {
	return nil, nil
}
func (m *mockResourceEventRepo) GetEventByID(id uuid.UUID) (string, error) { return "", nil }
func (m *mockResourceEventRepo) GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	return m.event, m.err
}
func (m *mockResourceEventRepo) GetEventByIDForSpec(id uuid.UUID) (*models.Event, error) {
	return m.event, m.err
}
func (m *mockResourceEventRepo) GetEventByIdentifier(identifier string) (*models.Event, error) {
	return m.event, m.err
}
func (m *mockResourceEventRepo) GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockResourceEventRepo) GetAllEventsForDashboard() ([]models.Event, error) {
	return nil, nil
}
func (m *mockResourceEventRepo) GetEventsForUser(userID uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockResourceEventRepo) UpdateEventCover(id uuid.UUID, coverImageURL string) error {
	return nil
}
func (m *mockResourceEventRepo) IdentifierExists(identifier string) bool { return false }

var _ ports.EventsRepository = (*mockResourceEventRepo)(nil)

type mockResourceConfigRepo struct {
	cfg   *models.EventConfig
	calls int
}

func (m *mockResourceConfigRepo) CreateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockResourceConfigRepo) UpdateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockResourceConfigRepo) DeleteEventConfig(id uuid.UUID) error            { return nil }
func (m *mockResourceConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	m.calls++
	return m.cfg, nil
}

var _ ports.EventConfigRepository = (*mockResourceConfigRepo)(nil)

type mockResourceTokenRepo struct {
	token      *models.InvitationAccessToken
	err        error
	pretty     *models.InvitationAccessToken
	prettyErr  error
	seen       string
	prettySeen string
}

func (m *mockResourceTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	m.seen = token
	return m.token, m.err
}
func (m *mockResourceTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	m.prettySeen = code
	return m.pretty, m.prettyErr
}
func (m *mockResourceTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABCD1234", nil
}

var _ ports.AccessTokenRepository = (*mockResourceTokenRepo)(nil)

type mockResourceInvitationRepo struct {
	invitation *models.Invitation
	err        error
}

func (m *mockResourceInvitationRepo) CreateInvitation(invitation *models.Invitation) error {
	return nil
}
func (m *mockResourceInvitationRepo) UpdateInvitation(invitation *models.Invitation) error {
	return nil
}
func (m *mockResourceInvitationRepo) DeleteInvitation(id uuid.UUID) error { return nil }
func (m *mockResourceInvitationRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, m.err
}
func (m *mockResourceInvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, m.err
}
func (m *mockResourceInvitationRepo) ListInvitations() ([]models.Invitation, error) {
	return nil, nil
}
func (m *mockResourceInvitationRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return nil, nil
}

var _ ports.InvitationRepository = (*mockResourceInvitationRepo)(nil)

type mockResourceMutationRepo struct {
	resource              *models.Resource
	updateCalls           int
	touchCalls            int
	touchedID             uuid.UUID
	touchedAt             time.Time
	listCalls             int
	listResourcesResponse []models.Resource
}

func (m *mockResourceMutationRepo) CreateResource(resource *models.Resource) error { return nil }
func (m *mockResourceMutationRepo) UpdateResource(resource *models.Resource) error {
	m.updateCalls++
	if m.resource != nil {
		m.resource = resource
	}
	return nil
}
func (m *mockResourceMutationRepo) TouchResourceUpdatedAt(id uuid.UUID, updatedAt time.Time) error {
	m.touchCalls++
	m.touchedID = id
	m.touchedAt = updatedAt
	return nil
}
func (m *mockResourceMutationRepo) DeleteResource(id uuid.UUID) error { return nil }
func (m *mockResourceMutationRepo) GetResourceByID(id uuid.UUID) (*models.Resource, error) {
	if m.resource != nil {
		return m.resource, nil
	}
	return &models.Resource{ID: id}, nil
}
func (m *mockResourceMutationRepo) ListResourcesBySection(sectionID *uuid.UUID) ([]models.Resource, error) {
	m.listCalls++
	return m.listResourcesResponse, nil
}
func (m *mockResourceMutationRepo) ListResourceTypesRaw() ([]models.ResourceType, error) {
	return nil, nil
}

var _ ports.ResourceRepository = (*mockResourceMutationRepo)(nil)

type mockResourceMutationCache struct {
	invalidations []string
}

func (m *mockResourceMutationCache) Invalidate(resource, key string) error {
	m.invalidations = append(m.invalidations, resource+":"+key)
	return nil
}
func (m *mockResourceMutationCache) DeleteKeysByPattern(ctx context.Context, pattern string) error {
	return nil
}
func (m *mockResourceMutationCache) GetKey(ctx context.Context, key string) (string, error) {
	return "", nil
}
func (m *mockResourceMutationCache) SaveKey(ctx context.Context, key, value string, ttl time.Duration) error {
	return nil
}

var _ ports.CacheRepository = (*mockResourceMutationCache)(nil)

type mockResourceMutationStorage struct {
	updatedFilename  string
	updatedFolder    string
	uploadedFilename string
	uploadedFolder   string
	deletedFilename  string
	deletedFolder    string
	signedFilename   string
	signedFolder     string
}

func (m *mockResourceMutationStorage) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	return true, "", nil
}
func (m *mockResourceMutationStorage) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	m.signedFilename = filename
	m.signedFolder = folder
	return "https://signed.example.com/" + folder + "/" + filename, nil
}
func (m *mockResourceMutationStorage) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	return "", nil
}
func (m *mockResourceMutationStorage) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	return "", nil
}
func (m *mockResourceMutationStorage) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return "", nil
}
func (m *mockResourceMutationStorage) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	return nil
}
func (m *mockResourceMutationStorage) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	return nil
}
func (m *mockResourceMutationStorage) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	m.updatedFilename = filename
	m.updatedFolder = folder
	return "", nil
}
func (m *mockResourceMutationStorage) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	m.uploadedFilename = filename
	m.uploadedFolder = folder
	return nil
}
func (m *mockResourceMutationStorage) DeleteFile(filename, folder, bucket, provider string) error {
	m.deletedFilename = filename
	m.deletedFolder = folder
	return nil
}
func (m *mockResourceMutationStorage) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

var _ ports.ObjectStorageRepository = (*mockResourceMutationStorage)(nil)

func withResourceAccessDeps(t *testing.T, sectionRepo ports.EventSectionRepository, configRepo ports.EventConfigRepository) {
	t.Helper()
	origSectionRepo := resourceSectionRepo
	origConfigRepo := resourceConfigRepo
	origTokenRepo := resourceAccessTokenRepo
	origInvitationRepo := resourceInvitationRepo
	origEventRepo := resourceEventRepo
	t.Cleanup(func() {
		resourceSectionRepo = origSectionRepo
		resourceConfigRepo = origConfigRepo
		resourceAccessTokenRepo = origTokenRepo
		resourceInvitationRepo = origInvitationRepo
		resourceEventRepo = origEventRepo
	})
	resourceSectionRepo = sectionRepo
	resourceConfigRepo = configRepo
	resourceAccessTokenRepo = nil
	resourceInvitationRepo = nil
	resourceEventRepo = nil
}

func withFullResourceAccessDeps(
	t *testing.T,
	sectionRepo ports.EventSectionRepository,
	configRepo ports.EventConfigRepository,
	tokenRepo ports.AccessTokenRepository,
	invitationRepo ports.InvitationRepository,
	eventRepo ...ports.EventsRepository,
) {
	t.Helper()
	withResourceAccessDeps(t, sectionRepo, configRepo)
	resourceAccessTokenRepo = tokenRepo
	resourceInvitationRepo = invitationRepo
	if len(eventRepo) > 0 {
		resourceEventRepo = eventRepo[0]
	}
}

func newMultipartFileRequest(t *testing.T, fieldName, filename, contentType string, content []byte) (*http.Request, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="`+fieldName+`"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPut, "/api/resources/resource-id/content", body)
	req.Header.Set(echo.HeaderContentType, writer.FormDataContentType())
	return req, writer.FormDataContentType()
}

func TestResourceResponseBuildsPublicContract(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	resourceTypeID := uuid.Must(uuid.NewV4())
	position := 2
	createdAt := time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC)

	resource := &models.Resource{
		ID:             uuid.Must(uuid.NewV4()),
		EventSectionID: &sectionID,
		ResourceTypeID: resourceTypeID,
		Path:           "events/demo/photo.webp",
		AltText:        "Entrada principal",
		Title:          "Entrada",
		Position:       &position,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt.Add(time.Hour),
	}

	response := resourceResponse(resource, "https://signed.example.com/photo.webp", 15)

	assert.Equal(t, resource.ID, response.ID)
	assert.Equal(t, sectionID, response.EventSectionID)
	assert.Equal(t, resourceTypeID, response.ResourceTypeID)
	assert.Equal(t, "Entrada principal", response.AltText)
	assert.Equal(t, "Entrada", response.Title)
	assert.Equal(t, 2, response.Position)
	assert.Equal(t, "https://signed.example.com/photo.webp", response.URL)
	assert.Equal(t, "https://signed.example.com/photo.webp", response.ViewURL)
	assert.Equal(t, createdAt, response.CreatedAt)
	require.NotNil(t, response.ViewURLExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(15*time.Minute), *response.ViewURLExpiresAt, 2*time.Second)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"view_url":"https://signed.example.com/photo.webp"`)
	assert.Contains(t, string(encoded), `"url":"https://signed.example.com/photo.webp"`)
	assert.NotContains(t, string(encoded), "events/demo/photo.webp")
	assert.NotContains(t, string(encoded), "updated_at")
	assert.NotContains(t, string(encoded), "resource_type\":")
}

func TestResourceResponseDoesNotExpireAbsoluteURLLikePaths(t *testing.T) {
	resource := &models.Resource{
		ID:    uuid.Must(uuid.NewV4()),
		Path:  "data:image/webp;base64,AAAA",
		Title: "Inline preview",
	}

	response := resourceResponse(resource, "data:image/webp;base64,AAAA", 15)

	assert.Equal(t, "data:image/webp;base64,AAAA", response.ViewURL)
	assert.Nil(t, response.ViewURLExpiresAt)
}

func TestAdminResourceResponseIncludesInternalPath(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	resourceTypeID := uuid.Must(uuid.NewV4())
	position := 2
	createdAt := time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)

	resource := &models.Resource{
		ID:             uuid.Must(uuid.NewV4()),
		EventSectionID: &sectionID,
		ResourceTypeID: resourceTypeID,
		Path:           "events/demo/photo.webp",
		AltText:        "Entrada principal",
		Title:          "Entrada",
		Position:       &position,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}

	response := dtos.NewAdminResourceResponse(resource, "https://signed.example.com/photo.webp", nil)

	assert.Equal(t, "events/demo/photo.webp", response.Path)
	assert.Equal(t, updatedAt, response.UpdatedAt)
	assert.Equal(t, "https://signed.example.com/photo.webp", response.ViewURL)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"path":"events/demo/photo.webp"`)
	assert.Contains(t, string(encoded), `"view_url":"https://signed.example.com/photo.webp"`)
	assert.Contains(t, string(encoded), `"updated_at":"2026-07-05T19:00:00Z"`)
}

func TestResourceFileMutationResponseContract(t *testing.T) {
	expiresAt := time.Date(2026, 7, 5, 18, 15, 0, 0, time.UTC)
	response := dtos.NewResourceFileMutationResponse(
		"events/demo/photo.webp",
		"https://signed.example.com/photo.webp",
		&expiresAt,
	)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"path":"events/demo/photo.webp",
		"url":"https://signed.example.com/photo.webp",
		"view_url":"https://signed.example.com/photo.webp",
		"view_url_expires_at":"2026-07-05T18:15:00Z"
	}`, string(encoded))
}

func TestResourceSectionIDFormValueReadsCanonicalAndAliases(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		want string
	}{
		{
			name: "canonical section_id",
			form: url.Values{"section_id": {" section-1 "}},
			want: "section-1",
		},
		{
			name: "legacy event_section_id",
			form: url.Values{"event_section_id": {"section-2"}},
			want: "section-2",
		},
		{
			name: "camel sectionId",
			form: url.Values{"sectionId": {"section-3"}},
			want: "section-3",
		},
		{
			name: "camel eventSectionId",
			form: url.Values{"eventSectionId": {"section-4"}},
			want: "section-4",
		},
		{
			name: "capital eventSectionID",
			form: url.Values{"eventSectionID": {"section-5"}},
			want: "section-5",
		},
		{
			name: "pascal EventSectionId",
			form: url.Values{"EventSectionId": {"section-6"}},
			want: "section-6",
		},
		{
			name: "canonical wins over aliases",
			form: url.Values{"section_id": {"section-canonical"}, "event_section_id": {"section-legacy"}},
			want: "section-canonical",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/api/resources", strings.NewReader(tt.form.Encode()))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			assert.Equal(t, tt.want, resourceSectionIDFormValue(c))
		})
	}
}

func TestResourceFormValueReadsResourceMetadataAliases(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		read func(echo.Context) string
		want string
	}{
		{
			name: "canonical resource_type_id",
			form: url.Values{"resource_type_id": {" type-1 "}},
			read: resourceTypeIDFormValue,
			want: "type-1",
		},
		{
			name: "camel resourceTypeId",
			form: url.Values{"resourceTypeId": {"type-2"}},
			read: resourceTypeIDFormValue,
			want: "type-2",
		},
		{
			name: "capital resourceTypeID",
			form: url.Values{"resourceTypeID": {"type-3"}},
			read: resourceTypeIDFormValue,
			want: "type-3",
		},
		{
			name: "pascal ResourceTypeId",
			form: url.Values{"ResourceTypeId": {"type-4"}},
			read: resourceTypeIDFormValue,
			want: "type-4",
		},
		{
			name: "canonical resource type wins over aliases",
			form: url.Values{"resource_type_id": {"canonical"}, "resourceTypeId": {"alias"}},
			read: resourceTypeIDFormValue,
			want: "canonical",
		},
		{
			name: "camel altText",
			form: url.Values{"altText": {" Entrada principal "}},
			read: func(c echo.Context) string {
				return resourceFormValue(c, "alt_text", "altText", "AltText")
			},
			want: "Entrada principal",
		},
		{
			name: "capital Title",
			form: url.Values{"Title": {"Hero"}},
			read: func(c echo.Context) string {
				return resourceFormValue(c, "title", "Title")
			},
			want: "Hero",
		},
		{
			name: "capital Position",
			form: url.Values{"Position": {"2"}},
			read: resourcePositionFormValue,
			want: "2",
		},
		{
			name: "sortOrder position alias",
			form: url.Values{"sortOrder": {"3"}},
			read: resourcePositionFormValue,
			want: "3",
		},
		{
			name: "order position alias",
			form: url.Values{"order": {"4"}},
			read: resourcePositionFormValue,
			want: "4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/api/resources", strings.NewReader(tt.form.Encode()))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			assert.Equal(t, tt.want, tt.read(c))
		})
	}
}

func TestGetResourcesBySectionID_DoesNotLoadPrivateEventResources(t *testing.T) {
	origResourceSvc := resourceSvc
	t.Cleanup(func() {
		resourceSvc = origResourceSvc
	})

	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	repo := &mockResourceMutationRepo{}
	storage := &mockResourceMutationStorage{}
	resourceSvc = Resources.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		Resources.ResourceServiceDeps{
			Repo:    repo,
			Cache:   &mockResourceMutationCache{},
			Storage: storage,
		},
	)
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID, IsVisible: true}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("key")
	c.SetParamValues(sectionID.String())

	require.NoError(t, GetResourcesBySectionID(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 0, repo.listCalls)
	assert.Empty(t, storage.signedFilename)
}

func TestGetResourcesBySectionIDAdminReturnsInternalPath(t *testing.T) {
	origResourceSvc := resourceSvc
	t.Cleanup(func() {
		resourceSvc = origResourceSvc
	})

	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	resourceID := uuid.Must(uuid.NewV4())
	resourceTypeID := uuid.Must(uuid.NewV4())
	position := 1
	repo := &mockResourceMutationRepo{
		listResourcesResponse: []models.Resource{
			{
				ID:             resourceID,
				EventSectionID: &sectionID,
				ResourceTypeID: resourceTypeID,
				Path:           "events/admin-photo.webp",
				Title:          "Admin photo",
				Position:       &position,
			},
		},
	}
	storage := &mockResourceMutationStorage{}
	resourceSvc = Resources.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		Resources.ResourceServiceDeps{
			Repo:    repo,
			Cache:   &mockResourceMutationCache{},
			Storage: storage,
		},
	)

	restoreAuthz := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventSectionByID: func(id uuid.UUID) (*models.EventSection, error) {
			assert.Equal(t, sectionID, id)
			return &models.EventSection{ID: id, EventID: eventID}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			assert.Equal(t, eventID, id)
			return &models.Event{ID: id}, nil
		},
	})
	t.Cleanup(restoreAuthz)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("key")
	c.SetParamValues(sectionID.String())

	require.NoError(t, GetResourcesBySectionIDAdmin(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, repo.listCalls)
	assert.Equal(t, "admin-photo.webp", storage.signedFilename)
	assert.Contains(t, rec.Body.String(), `"path":"events/admin-photo.webp"`)
	assert.Contains(t, rec.Body.String(), `"view_url":"https://signed.example.com/events/admin-photo.webp"`)
}

func TestGetResource_DoesNotSignPrivateResource(t *testing.T) {
	origResourceSvc := resourceSvc
	t.Cleanup(func() {
		resourceSvc = origResourceSvc
	})

	resourceID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	repo := &mockResourceMutationRepo{
		resource: &models.Resource{
			ID:             resourceID,
			EventSectionID: &sectionID,
			Path:           "events/private.webp",
		},
	}
	storage := &mockResourceMutationStorage{}
	resourceSvc = Resources.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		Resources.ResourceServiceDeps{
			Repo:    repo,
			Cache:   &mockResourceMutationCache{},
			Storage: storage,
		},
	)
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID, IsVisible: true}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/"+resourceID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(resourceID.String())

	require.NoError(t, GetResource(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, storage.signedFilename)
}

func TestRequirePublicResourceSectionAccess_DeniesPrivateEventWithoutToken(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Event is not public")
}

func TestRequirePublicResourceSectionAccess_AllowsPublicEvent(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID, ComponentType: "PhotoGrid", IsVisible: true}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: true}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequirePublicResourceSectionAccess_BlocksSectionWithoutComponentType(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{
			ID:            sectionID,
			EventID:       eventID,
			ComponentType: " ",
			IsVisible:     true,
		}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: true}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Section is not public")
}

func TestRequirePublicResourceSectionAccess_BlocksPasswordProtectedPublicEventWithoutProof(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID, ComponentType: "PhotoGrid", IsVisible: true}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{
			ID:                  eventID,
			IsPublic:            true,
			AuthPasswordPreview: "secreto",
		}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Event is not public")
}

func TestRequirePublicResourceSectionAccess_AllowsPasswordProtectedPublicEventWithProof(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{
		ID:                  eventID,
		IsPublic:            true,
		AuthPasswordPreview: "secreto",
	}
	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Hour)
	require.NoError(t, err)
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID, ComponentType: "PhotoGrid", IsVisible: true}},
		&mockResourceConfigRepo{cfg: cfg},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	req.Header.Set("X-Event-Access-Token", proof)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequirePublicResourceSectionAccess_BlocksHiddenSection(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{
			ID:            sectionID,
			EventID:       eventID,
			ComponentType: "PhotoGrid",
			IsVisible:     false,
		}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: true, ShowPhotoGallery: true}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Section is not public")
}

func TestRequirePublicResourceSectionAccess_BlocksSectionHiddenByEventConfig(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	configRepo := &mockResourceConfigRepo{cfg: &models.EventConfig{
		IsPublic:         true,
		ShowHeader:       true,
		ShowPhotoGallery: false,
	}}
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{
			ID:            sectionID,
			EventID:       eventID,
			ComponentType: "PhotoGrid",
			IsVisible:     true,
		}},
		configRepo,
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Section is not public")
	assert.Equal(t, 1, configRepo.calls)
}

func TestRequirePublicResourceSectionAccess_BlocksInactivePublicEvent(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	withFullResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: true}},
		nil,
		nil,
		&mockResourceEventRepo{event: &models.Event{ID: eventID, IsActive: false}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Event is not public")
}

func TestRequirePublicResourceSectionAccess_AllowsPrivateEventWithPrettyTokenQuery(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockResourceTokenRepo{
		err:    errors.New("raw token not found"),
		pretty: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	withFullResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID, ComponentType: "PhotoGrid", IsVisible: true}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: false}},
		tokenRepo,
		&mockResourceInvitationRepo{invitation: &models.Invitation{ID: invitationID, EventID: eventID}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/section/"+sectionID.String()+"?prettyToken=PRETTY%2F123", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceSectionAccess(c, sectionID))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "PRETTY/123", tokenRepo.seen)
	assert.Equal(t, "PRETTY/123", tokenRepo.prettySeen)
}

func TestRequirePublicResourceAccess_DeniesUnscopedResource(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/"+uuid.Must(uuid.NewV4()).String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceAccess(c, &models.Resource{}))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Resource is not public")
}

func TestRequirePublicResourceAccess_AllowsPublicSectionResource(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	withResourceAccessDeps(t,
		&mockResourceSectionRepo{section: &models.EventSection{ID: sectionID, EventID: eventID, ComponentType: "PhotoGrid", IsVisible: true}},
		&mockResourceConfigRepo{cfg: &models.EventConfig{IsPublic: true}},
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/"+uuid.Must(uuid.NewV4()).String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, requirePublicResourceAccess(c, &models.Resource{EventSectionID: &sectionID}))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateFileContentTouchesResourceVersionWhenPathDoesNotChange(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	resource := &models.Resource{
		ID:             resourceID,
		EventSectionID: &sectionID,
		Path:           "events/base/hero/photo.svg",
	}
	repo := &mockResourceMutationRepo{resource: resource}
	cache := &mockResourceMutationCache{}
	storage := &mockResourceMutationStorage{}

	origResourceSvc := resourceSvc
	resourceSvc = Resources.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		Resources.ResourceServiceDeps{Repo: repo, Cache: cache, Storage: storage},
	)
	t.Cleanup(func() { resourceSvc = origResourceSvc })

	restoreAuthz := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetResourceByID: func(id uuid.UUID) (*models.Resource, error) {
			assert.Equal(t, resourceID, id)
			return resource, nil
		},
		GetEventSectionByID: func(id uuid.UUID) (*models.EventSection, error) {
			assert.Equal(t, sectionID, id)
			return &models.EventSection{ID: id, EventID: eventID}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			assert.Equal(t, eventID, id)
			return &models.Event{ID: id}, nil
		},
	})
	t.Cleanup(restoreAuthz)

	req, _ := newMultipartFileRequest(
		t,
		"file",
		"photo.svg",
		"image/svg+xml",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
	)
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(resourceID.String())

	require.NoError(t, UpdateFileContent(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "photo.svg", storage.updatedFilename)
	assert.Equal(t, "events/base/hero", storage.updatedFolder)
	assert.Zero(t, repo.updateCalls)
	assert.Equal(t, 1, repo.touchCalls)
	assert.Equal(t, resourceID, repo.touchedID)
	assert.False(t, repo.touchedAt.IsZero())
	assert.Equal(t, []string{"resources:" + sectionID.String()}, cache.invalidations)
	assert.Contains(t, rec.Body.String(), `"path":"events/base/hero/photo.svg"`)
	assert.Contains(t, rec.Body.String(), `"view_url":"https://signed.example.com/events/base/hero/photo.svg"`)
}

func TestReplaceFileUpdatesPathInvalidatesSectionCacheAndDeletesOldObject(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	resource := &models.Resource{
		ID:             resourceID,
		EventSectionID: &sectionID,
		Path:           "events/base/hero/photo.svg",
	}
	repo := &mockResourceMutationRepo{resource: resource}
	cache := &mockResourceMutationCache{}
	storage := &mockResourceMutationStorage{}

	origResourceSvc := resourceSvc
	resourceSvc = Resources.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		Resources.ResourceServiceDeps{Repo: repo, Cache: cache, Storage: storage},
	)
	t.Cleanup(func() { resourceSvc = origResourceSvc })

	restoreAuthz := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetResourceByID: func(id uuid.UUID) (*models.Resource, error) {
			assert.Equal(t, resourceID, id)
			return resource, nil
		},
		GetEventSectionByID: func(id uuid.UUID) (*models.EventSection, error) {
			assert.Equal(t, sectionID, id)
			return &models.EventSection{ID: id, EventID: eventID}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			assert.Equal(t, eventID, id)
			return &models.Event{ID: id}, nil
		},
	})
	t.Cleanup(restoreAuthz)

	req, _ := newMultipartFileRequest(
		t,
		"file",
		"replacement.svg",
		"image/svg+xml",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
	)
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(resourceID.String())

	require.NoError(t, ReplaceFile(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, repo.updateCalls)
	assert.NotEmpty(t, storage.uploadedFilename)
	assert.Equal(t, "events/base/hero", storage.uploadedFolder)
	assert.NotEqual(t, "photo.svg", storage.uploadedFilename)
	assert.Equal(t, "photo.svg", storage.deletedFilename)
	assert.Equal(t, "events/base/hero", storage.deletedFolder)
	assert.Equal(t, []string{"resources:" + sectionID.String()}, cache.invalidations)
	assert.Contains(t, rec.Body.String(), `"path":"events/base/hero/`+storage.uploadedFilename+`"`)
	assert.Contains(t, rec.Body.String(), `"view_url":"https://signed.example.com/events/base/hero/`+storage.uploadedFilename+`"`)
}
