package events

import (
	"net/http"
	"testing"

	"events-stocks/models"
	eventsService "events-stocks/services/events"
	resourcesService "events-stocks/services/resources"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCoverImageTest(t *testing.T, event *models.Event, cfg *models.EventConfig) {
	t.Helper()
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
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(string) (*models.Event, error) { return event, nil },
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{cfg: cfg}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil
	coverResourceSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockCoverStorage{}},
	)
}

func getCoverImage(t *testing.T, identifier string) (int, http.Header) {
	t.Helper()
	c, rec := newEchoCtx(http.MethodGet, "/api/events/"+identifier+"/cover-image", "")
	c.SetParamNames("identifier")
	c.SetParamValues(identifier)
	require.NoError(t, GetEventCoverImage(c))
	return rec.Code, rec.Header()
}

// STU-49: og:image points at a route that never expires and redirects to a
// fresh signed URL of the cover.
func TestGetEventCoverImage_RedirectsToFreshViewURL(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	setupCoverImageTest(t,
		&models.Event{ID: eventID, Identifier: "boda-demo", IsActive: true, CoverImageURL: "events/boda-demo/cover.webp"},
		&models.EventConfig{ID: eventID, IsPublic: true})

	code, header := getCoverImage(t, "boda-demo")
	assert.Equal(t, http.StatusFound, code)
	assert.Equal(t, "https://signed.example.com/events/boda-demo/cover.webp", header.Get("Location"))
	assert.Equal(t, "public, max-age=600", header.Get("Cache-Control"))
	assert.Equal(t, "/api/events/boda-demo/cover-image", ogImageURL("boda-demo", "events/boda-demo/cover.webp"))
	assert.Empty(t, ogImageURL("boda-demo", ""))
}

func TestGetEventCoverImage_PasswordProtectedEventIsNotFound(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	setupCoverImageTest(t,
		&models.Event{ID: eventID, Identifier: "protegido", IsActive: true, CoverImageURL: "events/p/cover.webp"},
		&models.EventConfig{ID: eventID, IsPublic: true, AuthPasswordPreview: "secret"})

	code, header := getCoverImage(t, "protegido")
	assert.Equal(t, http.StatusNotFound, code)
	assert.Empty(t, header.Get("Location"))
}

func TestGetEventCoverImage_RejectsNonHTTPAbsoluteCover(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	setupCoverImageTest(t,
		&models.Event{ID: eventID, Identifier: "raro", IsActive: true, CoverImageURL: "javascript:alert(1)"},
		&models.EventConfig{ID: eventID, IsPublic: true})

	code, _ := getCoverImage(t, "raro")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestGetEventCoverImage_NoCoverIsNotFound(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	setupCoverImageTest(t,
		&models.Event{ID: eventID, Identifier: "sin-portada", IsActive: true},
		&models.EventConfig{ID: eventID, IsPublic: true})

	code, _ := getCoverImage(t, "sin-portada")
	assert.Equal(t, http.StatusNotFound, code)
}

// Review S3: an organizer-entered external cover URL is never redirected to.
func TestGetEventCoverImage_ExternalAbsoluteCoverIsNotFound(t *testing.T) {
	for _, cover := range []string{"https://evil.example.com/phish.png", "http://example.org/a.jpg", "data:image/png;base64,AAAA", "//evil.example.com/x.png"} {
		eventID := uuid.Must(uuid.NewV4())
		setupCoverImageTest(t,
			&models.Event{ID: eventID, Identifier: "externa", IsActive: true, CoverImageURL: cover},
			&models.EventConfig{ID: eventID, IsPublic: true})

		code, header := getCoverImage(t, "externa")
		assert.Equal(t, http.StatusNotFound, code, cover)
		assert.Empty(t, header.Get("Location"), cover)
	}
}

func TestCoverStorageKeyAcceptsOnlyOwnStorage(t *testing.T) {
	key, ok := coverStorageKey("events/boda/cover.webp", "media-bucket")
	assert.True(t, ok)
	assert.Equal(t, "events/boda/cover.webp", key)

	key, ok = coverStorageKey("https://media-bucket.s3.us-east-1.amazonaws.com/events/boda/cover.webp", "media-bucket")
	assert.True(t, ok, "a legacy URL of the event bucket is presigned again")
	assert.Equal(t, "events/boda/cover.webp", key)

	_, ok = coverStorageKey("https://other-bucket.s3.us-east-1.amazonaws.com/events/boda/cover.webp", "media-bucket")
	assert.False(t, ok, "another bucket is not our storage")
	_, ok = coverStorageKey("https://evil.example.com/cover.webp", "media-bucket")
	assert.False(t, ok)
}
