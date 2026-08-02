package moments

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/previewtoken"
	"events-stocks/internal/publicaccessproof"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	momentsService "events-stocks/services/moments"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		GetMomentByID: func(id uuid.UUID) (*models.Moment, error) {
			return &models.Moment{ID: id}, nil
		},
	})
	t.Cleanup(restore)
}

func TestUploadLimitReachedPayloadIncludesQuota(t *testing.T) {
	payload := uploadLimitReachedPayload("Evento Demo", 3, 5)

	assert.True(t, payload.AlreadyUploaded)
	assert.Equal(t, "Evento Demo", payload.EventName)
	assert.Equal(t, int64(3), payload.UploadsLimit)
	assert.Equal(t, int64(5), payload.UploadsUsed)
	assert.Equal(t, int64(0), payload.UploadsRemaining)
	assert.Contains(t, payload.Message, "Evento Demo")
}

func TestUploadLimitReachedPayloadUsesDefaultQuotaWhenLimitUnset(t *testing.T) {
	payload := uploadLimitReachedPayload("Evento Demo", 0, 31)

	assert.Equal(t, int64(defaultMaxUploadsPerIP), payload.UploadsLimit)
	assert.Equal(t, int64(31), payload.UploadsUsed)
	assert.Equal(t, int64(0), payload.UploadsRemaining)
}

func TestGetUploadQuotaUsesDefaultLimitWhenConfigLimitUnset(t *testing.T) {
	origCounter := publicUploadCounter
	t.Cleanup(func() { publicUploadCounter = origCounter })
	publicUploadCounter = nil

	c, _ := newEchoCtx(http.MethodGet, "/api/events/demo/moments", "")
	quota := getUploadQuota(c, uuid.Must(uuid.NewV4()), &models.EventConfig{MaxUploadsPerGuest: 0})

	assert.Equal(t, int64(defaultMaxUploadsPerIP), quota.Limit)
	assert.Equal(t, int64(0), quota.Used)
	assert.Equal(t, int64(defaultMaxUploadsPerIP), quota.Remaining)
}

func TestPublicMomentUploadSizeLimitTreatsBackendVideoExtensionsAsVideo(t *testing.T) {
	for _, filename := range []string{
		"clip.mp4",
		"clip.mov",
		"clip.webm",
		"clip.m4v",
		"clip.3gp",
		"clip.avi",
		"clip.mkv",
	} {
		t.Run(filename, func(t *testing.T) {
			maxBytes, maxMB := publicMomentUploadSizeLimit(filename, "")

			assert.Equal(t, int64(resourcesService.MaxVideoFileSizeBytes), maxBytes)
			assert.Equal(t, resourcesService.MaxVideoFileSizeMB, maxMB)
		})
	}
}

func TestPublicMomentUploadSizeLimitUsesContentTypeForExtensionlessVideos(t *testing.T) {
	maxBytes, maxMB := publicMomentUploadSizeLimit("upload", " video/x-matroska ")

	assert.Equal(t, int64(resourcesService.MaxVideoFileSizeBytes), maxBytes)
	assert.Equal(t, resourcesService.MaxVideoFileSizeMB, maxMB)
}

func TestPublicMomentContentTypeFromObjectKeyCoversModernMedia(t *testing.T) {
	tests := map[string]string{
		"moments/event/raw/clip.avi":   "video/x-msvideo",
		"moments/event/raw/clip.mkv":   "video/x-matroska",
		"moments/event/raw/photo.heic": "image/heic",
		"moments/event/raw/photo.heif": "image/heif",
		"moments/event/raw/photo.avif": "image/avif",
	}

	for objectKey, expected := range tests {
		t.Run(objectKey, func(t *testing.T) {
			assert.Equal(t, expected, publicMomentContentTypeFromObjectKey(objectKey))
		})
	}
}

func TestWriteUploadLimitReachedUsesAPIResponseEnvelope(t *testing.T) {
	c, rec := newEchoCtx(http.MethodPost, "/api/events/demo/moments/shared/confirm", "")

	err := writeUploadLimitReached(c, "Evento Demo", 3, 5)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, float64(http.StatusTooManyRequests), body["status"])
	assert.Contains(t, body["message"], "Evento Demo")
	assert.NotContains(t, body, "error")

	var envelope struct {
		Data dtos.PublicUploadLimitReachedResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.True(t, envelope.Data.AlreadyUploaded)
	assert.Equal(t, "Evento Demo", envelope.Data.EventName)
	assert.Equal(t, int64(3), envelope.Data.UploadsLimit)
	assert.Equal(t, int64(5), envelope.Data.UploadsUsed)
	assert.Equal(t, int64(0), envelope.Data.UploadsRemaining)
}

func TestPublicMomentInvitationTokenReadsAliases(t *testing.T) {
	assert.Equal(t, "RAW", publicMomentInvitationToken(" RAW ", "PRETTY", "CAMEL"))
	assert.Equal(t, "CAMEL", publicMomentInvitationToken("", " CAMEL ", "RAW"))
	assert.Equal(t, "TOKEN", publicMomentInvitationToken("", "", " TOKEN "))
	assert.Empty(t, publicMomentInvitationToken(" ", "", ""))
}

func TestWithMomentUploadQuotaUsesPublicMomentDTO(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	moment := &models.Moment{
		ID:           momentID,
		EventID:      &eventID,
		InvitationID: &invitationID,
		GuestID:      &guestID,
		Guest:        &models.Guest{ID: guestID, Email: "ana@example.com"},
		Description:  "Foto de bienvenida",
		ContentURL:   "moments/event/photo.webp",
		CreatedAt:    time.Date(2026, 7, 5, 19, 10, 0, 0, time.UTC),
	}

	payload := withMomentUploadQuota(moment, uploadQuota{Limit: 3, Used: 1, Remaining: 2})
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var body dtos.PublicMomentUploadResponse
	require.NoError(t, json.Unmarshal(encoded, &body))
	assert.Equal(t, momentID, body.ID)
	assert.Equal(t, "Foto de bienvenida", body.Description)
	assert.Equal(t, int64(3), body.UploadsLimit)
	assert.Equal(t, int64(1), body.UploadsUsed)
	assert.Equal(t, int64(2), body.UploadsRemaining)
	assert.Equal(t, "pending_review", body.ApprovalStatus)
	assert.Equal(t, "pending_review", body.PublicationStatus)
	assert.NotContains(t, string(encoded), "invitation_id")
	assert.NotContains(t, string(encoded), "guest_id")
	assert.NotContains(t, string(encoded), "ana@example.com")
}

func TestPublicMomentUploadStatusDistinguishesProcessingAndPublished(t *testing.T) {
	published := dtos.NewPublicMoment(models.Moment{
		ID:               uuid.Must(uuid.NewV4()),
		IsApproved:       true,
		ProcessingStatus: "done",
	})
	assert.Equal(t, "approved", published.ApprovalStatus)
	assert.Equal(t, "published", published.PublicationStatus)

	processing := dtos.NewPublicMoment(models.Moment{
		ID:               uuid.Must(uuid.NewV4()),
		IsApproved:       true,
		ProcessingStatus: "pending",
	})
	assert.Equal(t, "approved", processing.ApprovalStatus)
	assert.Equal(t, "processing", processing.PublicationStatus)
}

func TestRecordMomentCreatedAnalyticsCountsUploadsAndComments(t *testing.T) {
	repo := useRecordingEventAnalyticsService(t)
	eventID := uuid.Must(uuid.NewV4())

	recordMomentCreatedAnalytics(eventID, "  Foto con mensaje  ")

	require.Len(t, repo.calls, 2)
	assert.Equal(t, recordedAnalyticsCall{eventID: eventID, field: "moment_uploads", delta: 1}, repo.calls[0])
	assert.Equal(t, recordedAnalyticsCall{eventID: eventID, field: "moment_comments", delta: 1}, repo.calls[1])
}

func TestRecordMomentCreatedAnalyticsSkipsBlankComments(t *testing.T) {
	repo := useRecordingEventAnalyticsService(t)
	eventID := uuid.Must(uuid.NewV4())

	recordMomentCreatedAnalytics(eventID, "   ")

	require.Len(t, repo.calls, 1)
	assert.Equal(t, recordedAnalyticsCall{eventID: eventID, field: "moment_uploads", delta: 1}, repo.calls[0])
}

func TestAdjustMomentCommentAnalyticsTracksDescriptionStateChanges(t *testing.T) {
	repo := useRecordingEventAnalyticsService(t)
	eventID := uuid.Must(uuid.NewV4())

	adjustMomentCommentAnalytics(eventID, "", " Nuevo mensaje ")
	adjustMomentCommentAnalytics(eventID, "Mensaje anterior", "   ")
	adjustMomentCommentAnalytics(eventID, "Mensaje anterior", "Mensaje editado")

	require.Len(t, repo.calls, 2)
	assert.Equal(t, recordedAnalyticsCall{eventID: eventID, field: "moment_comments", delta: 1}, repo.calls[0])
	assert.Equal(t, recordedAnalyticsCall{eventID: eventID, field: "moment_comments", delta: -1}, repo.calls[1])
}

func TestNewAdminMomentResponseAddsSignedViewURLMetadata(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	contentPath := "moments/" + eventID.String() + "/raw/photo.jpg"
	thumbnailPath := "moments/" + eventID.String() + "/thumbs/photo.webp"

	origResSvc := adminResSvc
	adminResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)
	t.Cleanup(func() {
		adminResSvc = origResSvc
	})

	response := newAdminMomentResponse(&models.Moment{
		ID:           uuid.Must(uuid.NewV4()),
		ContentURL:   contentPath,
		ThumbnailURL: thumbnailPath,
	})

	assert.Equal(t, contentPath, response.ContentURL)
	assert.Equal(t, thumbnailPath, response.ThumbnailURL)
	assert.Nil(t, response.ContentURLExpiresAt)
	assert.Nil(t, response.ThumbnailURLExpiresAt)
	assert.Equal(t, "https://signed.example.com/"+contentPath+"?ttl=720", response.ContentViewURL)
	assert.Equal(t, "https://signed.example.com/"+thumbnailPath+"?ttl=720", response.ThumbnailViewURL)
	require.NotNil(t, response.ContentViewURLExpiresAt)
	require.NotNil(t, response.ThumbnailViewURLExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(momentViewURLTTLMinutes*time.Minute), *response.ContentViewURLExpiresAt, 2*time.Second)
	assert.WithinDuration(t, time.Now().UTC().Add(momentViewURLTTLMinutes*time.Minute), *response.ThumbnailViewURLExpiresAt, 2*time.Second)
}

func TestPresignMomentURLWithExpiryPreservesAbsoluteURLLikeValues(t *testing.T) {
	resSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	for _, path := range []string{
		"https://cdn.example.com/photo.webp",
		"http://cdn.example.com/photo.webp",
		"//cdn.example.com/photo.webp",
		"blob:https://app.example.com/photo",
		"data:image/webp;base64,AAAA",
	} {
		t.Run(path, func(t *testing.T) {
			viewURL, expiresAt := presignMomentURLWithExpiry(resSvc, path)

			assert.Equal(t, path, viewURL)
			assert.Nil(t, expiresAt)
		})
	}
}

func TestPresignMomentURLWithExpiryNormalizesConfiguredLegacyCDNURL(t *testing.T) {
	t.Setenv("CDN_BASE_URL", "https://cdn.eventiapp.com.mx")
	resSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)
	legacyURL := "https://cdn.eventiapp.com.mx/moments/event/optimized/photo.webp"

	viewURL, expiresAt := presignMomentURLWithExpiry(resSvc, legacyURL)

	assert.Equal(t, "https://signed.example.com/moments/event/optimized/photo.webp?ttl=720", viewURL)
	require.NotNil(t, expiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(momentViewURLTTLMinutes*time.Minute), *expiresAt, 2*time.Second)
	assert.Equal(t, "moments/event/optimized/photo.webp", canonicalMomentStoragePath(resSvc, legacyURL))
}

func TestAllowMomentWallInvitationReadsPrettyTokenAlias(t *testing.T) {
	restoreTokenRepo := publicTokenRepo
	restoreInvitationRepo := publicInvitationRepo
	t.Cleanup(func() {
		publicTokenRepo = restoreTokenRepo
		publicInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	c, _ := newEchoCtx(http.MethodGet, "/api/events/demo/moments?prettyToken=CAMEL123", "")

	allowed, err := allowMomentWallInvitation(c, eventID)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, "CAMEL123", tokenRepo.seen)
}

func TestAllowMomentWallInvitationAllowsPrettyTokenFallback(t *testing.T) {
	restoreTokenRepo := publicTokenRepo
	restoreInvitationRepo := publicInvitationRepo
	t.Cleanup(func() {
		publicTokenRepo = restoreTokenRepo
		publicInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		err:    errors.New("raw token not found"),
		pretty: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	c, _ := newEchoCtx(http.MethodGet, "/api/events/demo/moments?pretty_token=PRETTY123", "")

	allowed, err := allowMomentWallInvitation(c, eventID)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, "PRETTY123", tokenRepo.seen)
	assert.Equal(t, "PRETTY123", tokenRepo.prettySeen)
}

func TestAllowMomentWallInvitationRejectsExpiredToken(t *testing.T) {
	restoreTokenRepo := publicTokenRepo
	restoreInvitationRepo := publicInvitationRepo
	t.Cleanup(func() {
		publicTokenRepo = restoreTokenRepo
		publicInvitationRepo = restoreInvitationRepo
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	expiredAt := time.Now().Add(-time.Hour)
	publicTokenRepo = &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{
			InvitationID: invitationID,
			ExpiresAt:    &expiredAt,
		},
	}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}

	c, _ := newEchoCtx(http.MethodGet, "/api/events/demo/moments?token=expired-token", "")

	allowed, err := allowMomentWallInvitation(c, eventID)
	assert.False(t, allowed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid invitation token")
}

// ── Mocks ─────────────────────────────────────────────────────────────────────

type recordedAnalyticsCall struct {
	eventID uuid.UUID
	field   string
	delta   int
}

type recordingEventAnalyticsRepo struct {
	calls []recordedAnalyticsCall
}

func useRecordingEventAnalyticsService(t *testing.T) *recordingEventAnalyticsRepo {
	t.Helper()
	repo := &recordingEventAnalyticsRepo{}
	eventsService.SetDefaultEventAnalyticsService(eventsService.NewEventAnalyticsService(repo, nil))
	t.Cleanup(func() {
		eventsService.SetDefaultEventAnalyticsService(nil)
	})
	return repo
}

func (r *recordingEventAnalyticsRepo) AdjustEventAnalytics(eventID uuid.UUID, field string, delta int) error {
	r.calls = append(r.calls, recordedAnalyticsCall{
		eventID: eventID,
		field:   field,
		delta:   delta,
	})
	return nil
}

func (r *recordingEventAnalyticsRepo) CreateEventAnalytics(m *models.EventAnalytics) error {
	return nil
}

func (r *recordingEventAnalyticsRepo) UpdateEventAnalytics(m *models.EventAnalytics) error {
	return nil
}

func (r *recordingEventAnalyticsRepo) DeleteEventAnalytics(id uuid.UUID) error {
	return nil
}

func (r *recordingEventAnalyticsRepo) GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	return nil, errors.New("not found")
}

func (r *recordingEventAnalyticsRepo) GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	return nil, errors.New("not found")
}

func (r *recordingEventAnalyticsRepo) ListEventAnalyticss() ([]models.EventAnalytics, error) {
	return nil, nil
}

var _ ports.EventAnalyticsRepository = (*recordingEventAnalyticsRepo)(nil)

type mockMomentRepo struct {
	CreateMomentFunc              func(m *models.Moment) error
	UpdateMomentFunc              func(m *models.Moment) error
	ListMomentsFunc               func() ([]models.Moment, error)
	ListForDashboardFunc          func(eventID uuid.UUID) ([]models.Moment, error)
	ListForDashboardPageFunc      func(eventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error)
	ListApprovedForWallFunc       func(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error)
	ListApprovedForWallCursorFunc func(eventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error)
	GetMomentsByIDsFunc           func(ids []uuid.UUID) ([]models.Moment, error)
	GetByEventAndContentURLFunc   func(eventID uuid.UUID, contentURL string) (*models.Moment, error)
	UpdateContentFunc             func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error
	BulkUpdateApprovalFunc        func(ids []uuid.UUID, isApproved bool) error
	ListProcessingFunc            func(eventID uuid.UUID, rawOnly bool) ([]models.Moment, error)
}

type casCallbackMomentRepo struct {
	*mockMomentRepo
	moment *models.Moment
}

func (m *casCallbackMomentRepo) GetMomentByID(uuid.UUID) (*models.Moment, error) {
	return m.moment, nil
}

func (m *casCallbackMomentRepo) BeginMediaProcessingJob(uuid.UUID, uuid.UUID, string, string) (int64, error) {
	return 0, nil
}

func (m *casCallbackMomentRepo) ApplyMediaProcessingUpdate(uuid.UUID, uuid.UUID, string, int64, []string, string, string, string, string, int64, int64, int64, models.MediaVariants) (bool, error) {
	return true, nil
}

func (m *mockMomentRepo) CreateMoment(obj *models.Moment) error {
	if m.CreateMomentFunc != nil {
		return m.CreateMomentFunc(obj)
	}
	return nil
}
func (m *mockMomentRepo) UpdateMoment(obj *models.Moment) error {
	if m.UpdateMomentFunc != nil {
		return m.UpdateMomentFunc(obj)
	}
	return nil
}
func (m *mockMomentRepo) DeleteMoment(id uuid.UUID) error { return nil }
func (m *mockMomentRepo) BulkDeleteMoments(ids []uuid.UUID) error {
	return nil
}
func (m *mockMomentRepo) GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	return &models.Moment{ID: id}, nil
}
func (m *mockMomentRepo) GetMomentByEventIDAndContentURL(eventID uuid.UUID, contentURL string) (*models.Moment, error) {
	if m.GetByEventAndContentURLFunc != nil {
		return m.GetByEventAndContentURLFunc(eventID, contentURL)
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMomentRepo) ListMoments() ([]models.Moment, error) {
	if m.ListMomentsFunc != nil {
		return m.ListMomentsFunc()
	}
	return nil, nil
}
func (m *mockMomentRepo) ListByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) {
	return nil, nil
}
func (m *mockMomentRepo) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	if m.UpdateContentFunc != nil {
		return m.UpdateContentFunc(id, contentURL, processingStatus, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes)
	}
	return nil
}
func (m *mockMomentRepo) ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	if m.ListForDashboardFunc != nil {
		return m.ListForDashboardFunc(eventID)
	}
	return nil, nil
}

func (m *mockMomentRepo) ListForDashboardPage(eventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error) {
	if m.ListForDashboardPageFunc != nil {
		return m.ListForDashboardPageFunc(eventID, page, pageSize)
	}
	items, err := m.ListForDashboard(eventID)
	if err != nil {
		return nil, dtos.MomentDashboardCounts{}, err
	}
	counts := dtos.MomentDashboardCounts{Total: int64(len(items))}
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []models.Moment{}, counts, nil
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], counts, nil
}
func (m *mockMomentRepo) ListPendingSummaryByEventIDs(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	return []dtos.MomentSummary{{EventID: eventIDs[0], PendingCount: 2}}, nil
}
func (m *mockMomentRepo) ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	if m.ListApprovedForWallFunc != nil {
		return m.ListApprovedForWallFunc(eventID, page, limit)
	}
	return nil, 0, nil
}
func (m *mockMomentRepo) BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	if m.BulkUpdateApprovalFunc != nil {
		return m.BulkUpdateApprovalFunc(ids, isApproved)
	}
	return nil
}
func (m *mockMomentRepo) GetDistinctEventIDsByMomentIDs(ids []uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (m *mockMomentRepo) GetMomentsByIDs(ids []uuid.UUID) ([]models.Moment, error) {
	if m.GetMomentsByIDsFunc != nil {
		return m.GetMomentsByIDsFunc(ids)
	}
	return nil, nil
}
func (m *mockMomentRepo) BulkUpdateOrder(updates map[uuid.UUID]int) error {
	return nil
}
func (m *mockMomentRepo) ListProcessingByEventID(eventID uuid.UUID, rawOnly bool) ([]models.Moment, error) {
	if m.ListProcessingFunc != nil {
		return m.ListProcessingFunc(eventID, rawOnly)
	}
	return nil, nil
}
func (m *mockMomentRepo) ListApprovedForWallCursor(eventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error) {
	if m.ListApprovedForWallCursorFunc != nil {
		return m.ListApprovedForWallCursorFunc(eventID, afterCreatedAt, afterID, afterOrder, limit)
	}
	return nil, 0, nil
}

var _ ports.MomentRepository = (*mockMomentRepo)(nil)

type mockObjectStorage struct {
	completeMultipartUploadCalls int
	presignedPutURLCalls         int
	completedParts               []dtos.CompletedUploadPart
	deletedObjects               []string
	abortMultipartCalls          int
	missingObject                bool
}

func (m *mockObjectStorage) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	return !m.missingObject, "https://storage.example.com/" + folder + "/" + filename, nil
}
func (m *mockObjectStorage) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	return "https://signed.example.com/" + folder + "/" + filename + "?ttl=720", nil
}
func (m *mockObjectStorage) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	m.presignedPutURLCalls++
	return "https://upload.example.com/" + objectKey + "?content_type=" + contentType, nil
}
func (m *mockObjectStorage) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	return "upload-123", nil
}
func (m *mockObjectStorage) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return "https://upload.example.com/" + objectKey + "/part/" + strconv.Itoa(partNumber), nil
}
func (m *mockObjectStorage) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	m.completeMultipartUploadCalls++
	m.completedParts = append([]dtos.CompletedUploadPart(nil), parts...)
	return nil
}
func (m *mockObjectStorage) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	m.abortMultipartCalls++
	return nil
}
func (m *mockObjectStorage) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	return "", nil
}
func (m *mockObjectStorage) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	return nil
}
func (m *mockObjectStorage) DeleteFile(filename, folder, bucket, provider string) error {
	m.deletedObjects = append(m.deletedObjects, folder+"/"+filename)
	return nil
}
func (m *mockObjectStorage) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

var _ ports.ObjectStorageRepository = (*mockObjectStorage)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(_, _ string) error                          { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error) {
	return "", errors.New("miss")
}
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

type mockMediaPublisher struct {
	PublishFunc func(msg dtos.MediaProcessMessage) (bool, error)
}

func (m *mockMediaPublisher) PublishMediaJob(msg dtos.MediaProcessMessage) (bool, error) {
	if m.PublishFunc != nil {
		return m.PublishFunc(msg)
	}
	return true, nil
}

var _ ports.MediaJobPublisher = (*mockMediaPublisher)(nil)

type mockUploadCounter struct {
	value      string
	increment  int64
	increments int
	decrements int
}

func (m *mockUploadCounter) GetKey(_ context.Context, _ string) (string, error) {
	return m.value, nil
}
func (m *mockUploadCounter) Increment(_ context.Context, _ string) (int64, error) {
	m.increments++
	if m.increment > 0 {
		m.value = strconv.FormatInt(m.increment, 10)
		return m.increment, nil
	}
	current, _ := strconv.ParseInt(m.value, 10, 64)
	current++
	m.value = strconv.FormatInt(current, 10)
	return current, nil
}
func (m *mockUploadCounter) Decrement(_ context.Context, _ string) (int64, error) {
	m.decrements++
	current, _ := strconv.ParseInt(m.value, 10, 64)
	if current > 0 {
		current--
	}
	m.value = strconv.FormatInt(current, 10)
	return current, nil
}
func (m *mockUploadCounter) Expire(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

type mockPublicEventRepo struct {
	event               *models.Event
	preserveActiveState bool
}

func (m *mockPublicEventRepo) eventForTest() *models.Event {
	if m.event == nil || m.preserveActiveState {
		return m.event
	}
	event := *m.event
	event.IsActive = true
	return &event
}

func (m *mockPublicEventRepo) CreateEvent(event *models.Event) error { return nil }
func (m *mockPublicEventRepo) UpdateEvent(event *models.Event) error { return nil }
func (m *mockPublicEventRepo) DeleteEvent(id uuid.UUID) error        { return nil }
func (m *mockPublicEventRepo) ListEvents(page int, pageSize int, name string) ([]models.Event, error) {
	return nil, nil
}
func (m *mockPublicEventRepo) GetEventByID(id uuid.UUID) (string, error) { return id.String(), nil }
func (m *mockPublicEventRepo) GetEventByIDRaw(id uuid.UUID) (*models.Event, error) {
	return m.eventForTest(), nil
}
func (m *mockPublicEventRepo) GetEventByIDForSpec(id uuid.UUID) (*models.Event, error) {
	return m.eventForTest(), nil
}
func (m *mockPublicEventRepo) GetEventByIdentifier(identifier string) (*models.Event, error) {
	return m.eventForTest(), nil
}
func (m *mockPublicEventRepo) GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockPublicEventRepo) GetAllEventsForDashboard() ([]models.Event, error) { return nil, nil }
func (m *mockPublicEventRepo) GetEventsForUser(userID uuid.UUID) ([]models.Event, error) {
	return nil, nil
}
func (m *mockPublicEventRepo) UpdateEventCover(id uuid.UUID, coverImageURL string) error { return nil }
func (m *mockPublicEventRepo) IdentifierExists(identifier string) bool                   { return false }

var _ ports.EventsRepository = (*mockPublicEventRepo)(nil)

type mockPublicEventConfigRepo struct {
	cfg *models.EventConfig
}

func (m *mockPublicEventConfigRepo) CreateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockPublicEventConfigRepo) UpdateEventConfig(cfg *models.EventConfig) error { return nil }
func (m *mockPublicEventConfigRepo) DeleteEventConfig(id uuid.UUID) error            { return nil }
func (m *mockPublicEventConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	return m.cfg, nil
}

var _ ports.EventConfigRepository = (*mockPublicEventConfigRepo)(nil)

type mockPublicAccessTokenRepo struct {
	token      *models.InvitationAccessToken
	err        error
	pretty     *models.InvitationAccessToken
	prettyErr  error
	seen       string
	prettySeen string
}

func (m *mockPublicAccessTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	m.seen = token
	return m.token, m.err
}
func (m *mockPublicAccessTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	m.prettySeen = code
	return m.pretty, m.prettyErr
}
func (m *mockPublicAccessTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABC123", nil
}

var _ ports.AccessTokenRepository = (*mockPublicAccessTokenRepo)(nil)

type mockPublicInvitationRepo struct {
	invitation *models.Invitation
	err        error
}

func (m *mockPublicInvitationRepo) CreateInvitation(invitation *models.Invitation) error {
	return nil
}
func (m *mockPublicInvitationRepo) UpdateInvitation(invitation *models.Invitation) error {
	return nil
}
func (m *mockPublicInvitationRepo) DeleteInvitation(id uuid.UUID) error { return nil }
func (m *mockPublicInvitationRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, m.err
}
func (m *mockPublicInvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	return m.invitation, m.err
}
func (m *mockPublicInvitationRepo) ListInvitations() ([]models.Invitation, error) {
	return nil, nil
}
func (m *mockPublicInvitationRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return nil, nil
}

var _ ports.InvitationRepository = (*mockPublicInvitationRepo)(nil)

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestListPublicMoments_UploadStatusProbeForPrivateEventDoesNotRequireInvitation(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:            eventID,
		Name:          "Evento Privado",
		Identifier:    "evento-privado",
		EventDateTime: time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC),
		EventType:     models.EventType{Name: "Boda"},
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:            false,
		AllowUploads:        true,
		ShareUploadsEnabled: false,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicTokenRepo = nil
	publicInvitationRepo = nil
	publicUploadCounter = &mockUploadCounter{value: "2"}
	publicResSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-privado/moments?purpose=upload&page=1&limit=1", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Upload status loaded"`)
	assert.Contains(t, rec.Body.String(), `"items":[]`)
	assert.Contains(t, rec.Body.String(), `"show_moment_wall":false`)
	assert.Contains(t, rec.Body.String(), `"allow_uploads":true`)
	assert.Contains(t, rec.Body.String(), `"share_uploads_enabled":false`)
	assert.Contains(t, rec.Body.String(), `"uploads_limit":3`)
	assert.Contains(t, rec.Body.String(), `"uploads_used":2`)
	assert.Contains(t, rec.Body.String(), `"uploads_remaining":1`)
}

func TestListPublicMoments_UploadStatusProbeClosesUploadsWhenWallPublished(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:            eventID,
		Name:          "Evento Publicado",
		Identifier:    "evento-publicado",
		EventDateTime: time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC),
		EventType:     models.EventType{Name: "Boda"},
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:            false,
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      true,
		MaxUploadsPerGuest:  3,
	}}
	publicUploadCounter = &mockUploadCounter{value: "0"}
	publicResSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-publicado/moments?purpose=upload&page=1&limit=1", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-publicado")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"moments_wall_published":true`)
	assert.Contains(t, rec.Body.String(), `"show_moment_wall":true`)
	assert.Contains(t, rec.Body.String(), `"allow_uploads":false`)
	assert.Contains(t, rec.Body.String(), `"share_uploads_enabled":false`)
}

func TestListPublicMoments_NormalizesLegacyVisibilityDefaults(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Legacy",
		Identifier: "evento-legacy",
		EventType:  models.EventType{Name: "Boda"},
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic: true,
	}}
	publicUploadCounter = &mockUploadCounter{value: "0"}
	publicResSvc = nil
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		ListApprovedForWallFunc: func(gotEventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
			require.Equal(t, eventID, gotEventID)
			return []models.Moment{}, 0, nil
		},
	}, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-legacy/moments?page=1&limit=20", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-legacy")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Moments loaded"`)
	assert.Contains(t, rec.Body.String(), `"moments_wall_published":true`)
	assert.Contains(t, rec.Body.String(), `"show_moment_wall":true`)
	assert.Contains(t, rec.Body.String(), `"allow_uploads":false`)
}

func TestListPublicMoments_PasswordProtectedWithoutProofReturns401(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Protegido",
		Identifier: "evento-protegido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:            true,
		ShowMomentWall:      true,
		AuthPasswordPreview: "secreto",
	}}
	publicUploadCounter = &mockUploadCounter{value: "0"}
	publicResSvc = nil
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		ListApprovedForWallFunc: func(uuid.UUID, int, int) ([]models.Moment, int64, error) {
			t.Fatal("moments should not load without event password proof")
			return nil, 0, nil
		},
	}, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-protegido/moments?page=1&limit=20", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-protegido")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event password required"`)
}

func TestListPublicMoments_PasswordProtectedWithProofLoadsMoments(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	cfg := &models.EventConfig{
		IsPublic:            true,
		ShowMomentWall:      true,
		AuthPasswordPreview: "secreto",
	}
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Protegido",
		Identifier: "evento-protegido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: cfg}
	publicUploadCounter = &mockUploadCounter{value: "0"}
	publicResSvc = nil
	momentID := uuid.Must(uuid.NewV4())
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		ListApprovedForWallFunc: func(gotEventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
			require.Equal(t, eventID, gotEventID)
			require.Equal(t, 1, page)
			require.Equal(t, 20, limit)
			return []models.Moment{{
				ID:               momentID,
				EventID:          &eventID,
				ContentURL:       "moments/event/photo.webp",
				IsApproved:       true,
				ProcessingStatus: "done",
				CreatedAt:        time.Now(),
			}}, 1, nil
		},
	}, &mockCacheRepo{}))
	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Hour)
	require.NoError(t, err)

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-protegido/moments?page=1&limit=20", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-protegido")
	c.Request().Header.Set("X-Event-Access-Token", proof)

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"message":"Moments loaded"`)
	assert.Contains(t, rec.Body.String(), momentID.String())
}

func TestListPublicMoments_BlocksInactiveEventWithoutPreview(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origTokenRepo := publicTokenRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicTokenRepo = origTokenRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{
		preserveActiveState: true,
		event: &models.Event{
			ID:         eventID,
			Name:       "Evento Apagado",
			Identifier: "evento-apagado",
			IsActive:   false,
		},
	}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:       true,
		ShowMomentWall: true,
	}}
	publicTokenRepo = nil
	publicInvitationRepo = nil
	publicUploadCounter = nil
	publicResSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-apagado/moments?page=1&limit=1", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-apagado")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
}

func TestListPublicMoments_BlocksBeforeActiveFromWithoutPreview(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origTokenRepo := publicTokenRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicTokenRepo = origTokenRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	activeFrom := time.Now().Add(time.Hour)
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Futuro",
		Identifier: "evento-futuro",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:       true,
		ShowMomentWall: true,
		ActiveFrom:     activeFrom,
	}}
	publicTokenRepo = nil
	publicInvitationRepo = nil
	publicUploadCounter = nil
	publicResSvc = nil

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-futuro/moments?page=1&limit=1", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-futuro")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
}

func TestCreateSharedMoment_MissingFileDoesNotConsumeUploadQuota(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	counter := &mockUploadCounter{value: "0", increment: 1}
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicUploadCounter = counter
	publicResSvc = nil

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-compartido/moments/shared", "{}")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CreateSharedMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Missing file"`)
	assert.Equal(t, 0, counter.increments)
	assert.Equal(t, "0", counter.value)
}

func TestCreateSharedMoment_ResourceServiceUnavailableReturnsEnvelope(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	counter := &mockUploadCounter{value: "0", increment: 1}
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicUploadCounter = counter
	publicResSvc = nil

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fileWriter, err := writer.CreateFormFile("file", "foto.jpg")
	require.NoError(t, err)
	_, err = fileWriter.Write([]byte("image bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	e := echo.New()
	e.Validator = customValidator.New()
	req := httptest.NewRequest(http.MethodPost, "/api/events/evento-compartido/moments/shared", &body)
	req.Header.Set(echo.HeaderContentType, writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CreateSharedMoment(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Resource service unavailable"`)
	assert.Equal(t, 0, counter.increments)
	assert.Equal(t, "0", counter.value)
}

func TestCreateSharedMoment_BlocksInactiveEventBeforeReadingFile(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	counter := &mockUploadCounter{value: "0", increment: 1}
	publicEventRepo = &mockPublicEventRepo{
		preserveActiveState: true,
		event: &models.Event{
			ID:         eventID,
			Name:       "Evento Compartido",
			Identifier: "evento-compartido",
			IsActive:   false,
		},
	}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicUploadCounter = counter
	publicResSvc = nil

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-compartido/moments/shared", "{}")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CreateSharedMoment(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
	assert.Equal(t, 0, counter.increments)
	assert.Equal(t, "0", counter.value)
}

func TestCreateSharedMoment_BlocksBeforeActiveFromBeforeReadingFile(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	activeFrom := time.Now().Add(time.Hour)
	counter := &mockUploadCounter{value: "0", increment: 1}
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
		ActiveFrom:          activeFrom,
	}}
	publicUploadCounter = counter
	publicResSvc = nil

	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento-compartido/moments/shared", "{}")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CreateSharedMoment(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event is not public"`)
	assert.Equal(t, 0, counter.increments)
	assert.Equal(t, "0", counter.value)
}

func TestListPublicMoments_PrivateEventAllowsInvitationTokenQuery(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:       false,
		ShowMomentWall: true,
	}}
	publicUploadCounter = &mockUploadCounter{value: "0"}
	publicResSvc = nil
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		ListApprovedForWallFunc: func(gotEventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
			require.Equal(t, eventID, gotEventID)
			return []models.Moment{}, 0, nil
		},
	}, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-privado/moments?page=1&limit=20&token=RAW%2F123", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "RAW/123", tokenRepo.seen)
	assert.Contains(t, rec.Body.String(), `"message":"Moments loaded"`)
}

func TestListPublicMoments_InvalidPreviewFallsBackToInvitationToken(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:       false,
		ShowMomentWall: true,
	}}
	publicUploadCounter = &mockUploadCounter{value: "0"}
	publicResSvc = nil
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		ListApprovedForWallFunc: func(gotEventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
			require.Equal(t, eventID, gotEventID)
			return []models.Moment{}, 0, nil
		},
	}, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-privado/moments?page=1&limit=20&preview_token=bad-token&token=RAW%2F123", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "RAW/123", tokenRepo.seen)
	assert.Contains(t, rec.Body.String(), `"message":"Moments loaded"`)
}

func TestListPublicMoments_ReturnsPublicDTOWithoutInternalRelations(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 5, 19, 10, 0, 0, time.UTC)
	eventDate := time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC)

	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:            eventID,
		Name:          "Evento Publicado",
		Identifier:    "evento-publicado",
		EventDateTime: eventDate,
		Timezone:      "America/Mexico_City",
		EventType:     models.EventType{Name: "Boda"},
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:            true,
		AllowUploads:        true,
		AllowMessages:       true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      true,
		MaxUploadsPerGuest:  3,
	}}
	publicUploadCounter = &mockUploadCounter{value: "1"}
	publicResSvc = nil
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		ListApprovedForWallFunc: func(gotEventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
			require.Equal(t, eventID, gotEventID)
			require.Equal(t, 1, page)
			require.Equal(t, 20, limit)
			return []models.Moment{
				{
					ID:                   momentID,
					EventID:              &eventID,
					InvitationID:         &invitationID,
					Invitation:           models.Invitation{ID: invitationID, EventID: eventID},
					GuestID:              &guestID,
					Guest:                &models.Guest{ID: guestID, FirstName: "Ana", Email: "ana@example.com"},
					Title:                "Entrada",
					Description:          "Foto de entrada",
					ContentURL:           "moments/event/photo.webp",
					ThumbnailURL:         "moments/event/thumb.webp",
					ContentType:          "image/webp",
					IsApproved:           true,
					ProcessingStatus:     "done",
					ProcessingDurationMs: 123,
					OriginalSizeBytes:    456,
					OptimizedSizeBytes:   78,
					CreatedAt:            createdAt,
				},
			}, 1, nil
		},
	}, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-publicado/moments?page=1&limit=20", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-publicado")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Message string                 `json:"message"`
		Data    dtos.PublicMomentsPage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Moments loaded", body.Message)
	assert.Equal(t, int64(1), body.Data.Total)
	assert.Equal(t, 1, body.Data.Page)
	assert.Equal(t, 20, body.Data.Limit)
	assert.False(t, body.Data.HasMore)
	assert.True(t, body.Data.Published)
	assert.True(t, body.Data.MomentsWallPublished)
	assert.True(t, body.Data.ShowMomentWall)
	assert.False(t, body.Data.AllowUploads)
	assert.True(t, body.Data.AllowMessages)
	assert.False(t, body.Data.ShareUploadsEnabled)
	assert.Equal(t, int64(3), body.Data.UploadsLimit)
	assert.Equal(t, int64(1), body.Data.UploadsUsed)
	assert.Equal(t, int64(2), body.Data.UploadsRemaining)
	assert.Equal(t, "Evento Publicado", body.Data.EventName)
	assert.Equal(t, "Boda", body.Data.EventType)
	require.NotNil(t, body.Data.EventDate)
	assert.Equal(t, eventDate, *body.Data.EventDate)
	require.NotNil(t, body.Data.EventDateTime)
	assert.Equal(t, eventDate, *body.Data.EventDateTime)
	assert.Equal(t, "America/Mexico_City", body.Data.Timezone)
	require.Len(t, body.Data.Items, 1)
	assert.Equal(t, momentID, body.Data.Items[0].ID)
	assert.Equal(t, "Entrada", body.Data.Items[0].Title)
	assert.Equal(t, "Foto de entrada", body.Data.Items[0].Description)
	assert.Equal(t, "moments/event/photo.webp", body.Data.Items[0].ContentURL)
	assert.Equal(t, "done", body.Data.Items[0].ProcessingStatus)

	payload := rec.Body.String()
	assert.Contains(t, payload, `"event_date":"2026-07-05T18:00:00Z"`)
	assert.Contains(t, payload, `"event_date_time":"2026-07-05T18:00:00Z"`)
	assert.NotContains(t, payload, "invitation_id")
	assert.NotContains(t, payload, "guest_id")
	assert.NotContains(t, payload, "moment_type")
	assert.NotContains(t, payload, "is_approved")
	assert.NotContains(t, payload, "ana@example.com")
}

func TestNewPublicMomentResponsesAddsSignedViewURLMetadata(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	contentPath := "moments/" + eventID.String() + "/optimized/photo.webp"
	thumbnailPath := "moments/" + eventID.String() + "/thumbs/photo.webp"
	absoluteURL := "https://cdn.example.com/already-signed.webp"

	origResSvc := publicResSvc
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)
	t.Cleanup(func() {
		publicResSvc = origResSvc
	})

	items := []models.Moment{
		{ID: uuid.Must(uuid.NewV4()), ContentURL: contentPath, ThumbnailURL: thumbnailPath},
		{ID: uuid.Must(uuid.NewV4()), ContentURL: absoluteURL},
	}

	responses := newPublicMomentResponses(items)

	require.Len(t, responses, 2)
	assert.Equal(t, contentPath, responses[0].ContentURL)
	assert.Equal(t, thumbnailPath, responses[0].ThumbnailURL)
	assert.Nil(t, responses[0].ContentURLExpiresAt)
	assert.Nil(t, responses[0].ThumbnailURLExpiresAt)
	assert.Equal(t, "https://signed.example.com/"+contentPath+"?ttl=720", responses[0].ContentViewURL)
	assert.Equal(t, "https://signed.example.com/"+thumbnailPath+"?ttl=720", responses[0].ThumbnailViewURL)
	require.NotNil(t, responses[0].ContentViewURLExpiresAt)
	require.NotNil(t, responses[0].ThumbnailViewURLExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(momentViewURLTTLMinutes*time.Minute), *responses[0].ContentViewURLExpiresAt, 2*time.Second)
	assert.WithinDuration(t, time.Now().UTC().Add(momentViewURLTTLMinutes*time.Minute), *responses[0].ThumbnailViewURLExpiresAt, 2*time.Second)

	assert.Equal(t, absoluteURL, responses[1].ContentURL)
	assert.Equal(t, absoluteURL, responses[1].ContentViewURL)
	assert.Nil(t, responses[1].ContentViewURLExpiresAt)
}

func TestListPublicMoments_CursorModeReturnsRealTotalAndNextCursor(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Cursor",
		Identifier: "evento-cursor",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:       true,
		ShowMomentWall: true,
	}}
	publicUploadCounter = &mockUploadCounter{value: "0"}
	publicResSvc = nil

	firstID := uuid.Must(uuid.NewV4())
	secondID := uuid.Must(uuid.NewV4())
	thirdID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, 7, 5, 21, 0, 0, 0, time.UTC)
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		ListApprovedForWallCursorFunc: func(gotEventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error) {
			require.Equal(t, eventID, gotEventID)
			require.Nil(t, afterCreatedAt)
			require.Empty(t, afterID)
			require.Nil(t, afterOrder)
			require.Equal(t, 3, limit)
			return []models.Moment{
				{ID: firstID, EventID: &eventID, ContentURL: "moments/event/1.webp", IsApproved: true, ProcessingStatus: "done", CreatedAt: now, Order: 1},
				{ID: secondID, EventID: &eventID, ContentURL: "moments/event/2.webp", IsApproved: true, ProcessingStatus: "done", CreatedAt: now.Add(-time.Minute), Order: 2},
				{ID: thirdID, EventID: &eventID, ContentURL: "moments/event/3.webp", IsApproved: true, ProcessingStatus: "done", CreatedAt: now.Add(-2 * time.Minute)},
			}, 10, nil
		},
	}, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-cursor/moments?cursor=&limit=2", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-cursor")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body struct {
		Data dtos.PublicMomentsPage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, int64(10), body.Data.Total)
	assert.Equal(t, 2, body.Data.Limit)
	assert.True(t, body.Data.HasMore)
	assert.NotEmpty(t, body.Data.NextCursor)
	require.Len(t, body.Data.Items, 2)
	assert.Equal(t, firstID, body.Data.Items[0].ID)
	assert.Equal(t, secondID, body.Data.Items[1].ID)
	assert.NotContains(t, rec.Body.String(), thirdID.String())
	cursor, err := decodeCursor(body.Data.NextCursor)
	require.NoError(t, err)
	require.NotNil(t, cursor.Order)
	assert.Equal(t, 2, *cursor.Order)
}

func TestDecodeCursorRejectsLegacyCursorWithoutOrder(t *testing.T) {
	payload, err := json.Marshal(publicMomentCursor{
		CreatedAt: time.Date(2026, 7, 5, 21, 0, 0, 0, time.UTC),
		ID:        uuid.Must(uuid.NewV4()).String(),
	})
	require.NoError(t, err)

	cursor, err := decodeCursor(base64.RawURLEncoding.EncodeToString(payload))
	require.Error(t, err)
	assert.Nil(t, cursor)
	assert.Contains(t, err.Error(), "restart pagination")
}

func TestListPublicMoments_CursorModePassesManualOrderBoundary(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Cursor",
		Identifier: "evento-cursor",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:       true,
		ShowMomentWall: true,
	}}
	publicUploadCounter = &mockUploadCounter{value: "0"}
	publicResSvc = nil

	afterID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, 7, 5, 21, 0, 0, 0, time.UTC)
	rawCursor := encodeCursor(models.Moment{ID: afterID, CreatedAt: now, Order: 7})
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		ListApprovedForWallCursorFunc: func(gotEventID uuid.UUID, afterCreatedAt *time.Time, gotAfterID string, afterOrder *int, limit int) ([]models.Moment, int64, error) {
			require.Equal(t, eventID, gotEventID)
			require.NotNil(t, afterCreatedAt)
			assert.True(t, afterCreatedAt.Equal(now))
			assert.Equal(t, afterID.String(), gotAfterID)
			require.NotNil(t, afterOrder)
			assert.Equal(t, 7, *afterOrder)
			assert.Equal(t, 3, limit)
			return []models.Moment{}, 2, nil
		},
	}, &mockCacheRepo{}))

	c, rec := newEchoCtx(http.MethodGet, "/api/events/evento-cursor/moments?cursor="+rawCursor+"&limit=2", "")
	c.SetParamNames("identifier")
	c.SetParamValues("evento-cursor")

	require.NoError(t, ListPublicMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

func TestRequestBatchSharedUploadURLs_ResourceServiceUnavailableReturns500(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:             false,
		AllowUploads:         true,
		ShareUploadsEnabled:  true,
		MaxUploadsPerGuest:   30,
		AutoApproveUploads:   false,
		ShowMomentWall:       false,
		ShowPhotoGallery:     false,
		ShowContactSection:   false,
		ShowEventLocation:    false,
		ShowSecondLocation:   false,
		ShowHostsSection:     false,
		ShowRSVPSection:      false,
		ShowCountdown:        false,
		NotifyOnMomentUpload: false,
	}}
	publicUploadCounter = nil
	publicResSvc = nil

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/batch-upload-urls",
		`{"files":[{"content_type":"image/jpeg","filename":"foto.jpg"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, RequestBatchSharedUploadURLs(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Resource service unavailable"`)
}

func TestRequestBatchSharedUploadURLs_PasswordProtectedWithoutProofReturns401(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:            true,
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		AuthPasswordPreview: "secreto",
	}}
	publicUploadCounter = nil
	publicResSvc = nil

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/batch-upload-urls",
		`{"files":[{"content_type":"image/jpeg","filename":"foto.jpg"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, RequestBatchSharedUploadURLs(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event password required"`)
}

func TestRequestBatchSharedUploadURLs_PublishedWallReturns403(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:            false,
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      true,
		MaxUploadsPerGuest:  30,
	}}
	publicUploadCounter = nil
	publicResSvc = nil

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/batch-upload-urls",
		`{"files":[{"content_type":"image/jpeg","filename":"foto.jpg"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, RequestBatchSharedUploadURLs(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Uploads are closed because the moments wall is already published"`)
}

func TestRequestBatchSharedUploadURLsReturnsTypedContract(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  7,
	}}
	publicUploadCounter = &mockUploadCounter{value: "4"}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/batch-upload-urls",
		`{"files":[{"content_type":"image/jpeg","filename":"foto.jpg"},{"ContentType":"video/quicktime","FileName":"clip.mov"},{"contentType":"image/png","fileName":"grafico.png"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, RequestBatchSharedUploadURLs(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status  int                               `json:"status"`
		Message string                            `json:"message"`
		Data    dtos.MomentUploadURLBatchResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, http.StatusOK, body.Status)
	assert.Equal(t, "Upload URLs generated", body.Message)
	require.Len(t, body.Data.URLs, 3)
	assert.NotEmpty(t, body.Data.URLs[0].UploadURL)
	assert.Contains(t, body.Data.URLs[0].ObjectKey, "moments/"+eventID.String()+"/raw/")
	assert.Contains(t, body.Data.URLs[0].S3Key, "moments/"+eventID.String()+"/raw/")
	assert.Equal(t, body.Data.URLs[0].ObjectKey, body.Data.URLs[0].S3Key)
	assert.Equal(t, "image/jpeg", body.Data.URLs[0].ContentType)
	assert.Equal(t, "video/quicktime", body.Data.URLs[1].ContentType)
	assert.Equal(t, "image/png", body.Data.URLs[2].ContentType)
	require.NotNil(t, body.Data.UploadsLimit)
	require.NotNil(t, body.Data.UploadsUsed)
	require.NotNil(t, body.Data.UploadsRemaining)
	assert.Equal(t, int64(7), *body.Data.UploadsLimit)
	assert.Equal(t, int64(4), *body.Data.UploadsUsed)
	assert.Equal(t, int64(3), *body.Data.UploadsRemaining)
	assert.NotContains(t, rec.Body.String(), "deleted_at")
}

func TestRequestBatchSharedUploadURLsChecksQuotaBeforeGeneratingURLs(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  7,
	}}
	publicUploadCounter = &mockUploadCounter{value: "6"}
	storage := &mockObjectStorage{}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/batch-upload-urls",
		`{"files":[{"content_type":"image/jpeg","filename":"foto-1.jpg"},{"content_type":"image/jpeg","filename":"foto-2.jpg"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, RequestBatchSharedUploadURLs(c))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, 0, storage.presignedPutURLCalls)
	assert.Contains(t, rec.Body.String(), `"uploads_remaining":1`)
}

func TestRequestSharedUploadURLAllowsInactiveEventWithPreviewToken(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	t.Setenv("EVENT_PREVIEW_SECRET", "test-preview-secret")
	previewToken, err := previewtoken.Generate(eventID, time.Hour)
	require.NoError(t, err)
	publicEventRepo = &mockPublicEventRepo{
		preserveActiveState: true,
		event: &models.Event{
			ID:         eventID,
			Name:       "Evento Preview",
			Identifier: "evento-preview",
			IsActive:   false,
		},
	}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  4,
	}}
	publicUploadCounter = &mockUploadCounter{value: "1"}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-preview/moments/shared/upload-url?previewToken="+previewToken,
		`{"ContentType":"image/jpeg","FileName":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-preview")

	require.NoError(t, RequestSharedUploadURL(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Upload URL generated"`)
	assert.Contains(t, rec.Body.String(), "moments/"+eventID.String()+"/raw/")

	var body struct {
		Data dtos.MomentUploadURLResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Data.UploadsLimit)
	require.NotNil(t, body.Data.UploadsUsed)
	require.NotNil(t, body.Data.UploadsRemaining)
	assert.Equal(t, int64(4), *body.Data.UploadsLimit)
	assert.Equal(t, int64(1), *body.Data.UploadsUsed)
	assert.Equal(t, int64(3), *body.Data.UploadsRemaining)
}

func TestRequestSharedUploadURLRejectsInvalidPreviewToken(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	t.Setenv("EVENT_PREVIEW_SECRET", "test-preview-secret")
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  30,
	}}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/upload-url?preview_token=bad-token",
		`{"content_type":"image/jpeg","filename":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, RequestSharedUploadURL(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Invalid preview token"`)
}

func TestStartSharedMultipartUploadAllowsInactiveEventWithPreviewToken(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	t.Setenv("EVENT_PREVIEW_SECRET", "test-preview-secret")
	previewToken, err := previewtoken.Generate(eventID, time.Hour)
	require.NoError(t, err)
	publicEventRepo = &mockPublicEventRepo{
		preserveActiveState: true,
		event: &models.Event{
			ID:         eventID,
			Name:       "Evento Preview",
			Identifier: "evento-preview",
			IsActive:   false,
		},
	}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  30,
	}}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-preview/moments/shared/multipart/start?preview_token="+previewToken,
		`{"content_type":"video/mp4","filename":"clip.mp4","file_size":`+strconv.FormatInt(resourcesService.MaxMomentImageFileSizeBytes+1, 10)+`}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-preview")

	require.NoError(t, StartSharedMultipartUpload(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Multipart upload started"`)
	assert.Contains(t, rec.Body.String(), `"upload_id":"upload-123"`)
	assert.Contains(t, rec.Body.String(), "moments/"+eventID.String()+"/raw/")
}

func TestStartSharedMultipartUploadRejectsOversizedImages(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{
		event: &models.Event{
			ID:         eventID,
			Name:       "Evento Compartido",
			Identifier: "evento-compartido",
			IsActive:   true,
		},
	}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  30,
	}}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/start",
		`{"content_type":"image/jpeg","filename":"foto.jpg","file_size":`+strconv.FormatInt(resourcesService.MaxMomentImageFileSizeBytes+1, 10)+`}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, StartSharedMultipartUpload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"file too large"`)
	assert.Contains(t, rec.Body.String(), `"error":"max 25 MB"`)
}

func TestStartSharedMultipartUploadRejectsUnsupportedMomentMedia(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{
		event: &models.Event{
			ID:         eventID,
			Name:       "Evento Compartido",
			Identifier: "evento-compartido",
			IsActive:   true,
		},
	}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  30,
	}}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/start",
		`{"content_type":"image/svg+xml","filename":"vector.svg","file_size":1024}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, StartSharedMultipartUpload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Error preparing multipart upload"`)
	assert.Contains(t, rec.Body.String(), `"unsupported file type for moments: image/svg+xml"`)
}

func TestCompleteSharedMultipartUploadAllowsInactiveEventWithPreviewToken(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	t.Setenv("EVENT_PREVIEW_SECRET", "test-preview-secret")
	previewToken, err := previewtoken.Generate(eventID, time.Hour)
	require.NoError(t, err)
	publicEventRepo = &mockPublicEventRepo{
		preserveActiveState: true,
		event: &models.Event{
			ID:         eventID,
			Name:       "Evento Preview",
			Identifier: "evento-preview",
			IsActive:   false,
		},
	}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  30,
	}}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	var capturedMoment *models.Moment
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		CreateMomentFunc: func(moment *models.Moment) error {
			copy := *moment
			capturedMoment = &copy
			return nil
		},
	}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/clip.mp4"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-preview/moments/shared/multipart/complete?preview_token="+previewToken,
		`{"uploadId":"upload-123","s3Key":"`+s3Key+`","contentType":"video/mp4","parts":[{"part_number":1,"etag":"etag-1"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-preview")

	require.NoError(t, CompleteSharedMultipartUpload(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Moment submitted for review"`)
	require.NotNil(t, capturedMoment)
	assert.Equal(t, s3Key, capturedMoment.ContentURL)
	assert.Equal(t, "video/mp4", capturedMoment.ContentType)
}

func TestConfirmSharedMoment_ReturnsMomentWithUploadQuota(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		IsPublic:            false,
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicUploadCounter = &mockUploadCounter{value: "2", increment: 3}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/foto.jpg"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/confirm",
		`{"objectKey":"`+s3Key+`","contentType":"image/jpeg","description":"Foto"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, ConfirmSharedMoment(c))
	assert.Equal(t, http.StatusCreated, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, float64(http.StatusCreated), body["status"])
	assert.Equal(t, "Moment submitted for review", body["message"])

	data, ok := body["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, s3Key, data["content_url"])
	assert.Equal(t, "https://signed.example.com/"+s3Key+"?ttl=720", data["content_view_url"])
	assert.Contains(t, data, "content_view_url_expires_at")
	assert.Equal(t, float64(3), data["uploads_limit"])
	assert.Equal(t, float64(3), data["uploads_used"])
	assert.Equal(t, float64(0), data["uploads_remaining"])
}

func TestConfirmSharedMoment_DropsDescriptionWhenMessagesDisabled(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		AllowMessages:       false,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicUploadCounter = &mockUploadCounter{value: "0", increment: 1}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	var capturedDescription string
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		CreateMomentFunc: func(moment *models.Moment) error {
			capturedDescription = moment.Description
			return nil
		},
	}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/foto.jpg"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/confirm",
		`{"ObjectKey":"`+s3Key+`","ContentType":"image/jpeg","description":"Mensaje no permitido"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, ConfirmSharedMoment(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Empty(t, capturedDescription)
}

func TestCompleteSharedMultipartUpload_UsesProvidedContentType(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  5,
	}}
	publicUploadCounter = &mockUploadCounter{value: "2"}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	var capturedContentType string
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		CreateMomentFunc: func(moment *models.Moment) error {
			capturedContentType = moment.ContentType
			return nil
		},
	}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/upload-without-useful-extension.bin"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/complete",
		`{"UploadID":"upload-123","S3Key":"`+s3Key+`","ContentType":"video/quicktime","parts":[{"PartNumber":1,"ETag":"etag-1"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CompleteSharedMultipartUpload(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "video/quicktime", capturedContentType)
}

func TestCompleteSharedMultipartUploadAcceptsCompletedPartsAliasesAndNormalizesParts(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  5,
	}}
	publicUploadCounter = &mockUploadCounter{value: "2"}
	storage := &mockObjectStorage{}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)

	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		CreateMomentFunc: func(moment *models.Moment) error {
			return nil
		},
	}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/video.mp4"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/complete",
		`{"UploadID":"upload-123","S3Key":"`+s3Key+`","ContentType":"video/mp4","CompletedParts":[{"PartNumber":2,"ETag":" etag-2 "},{"PartNumber":1,"ETag":"etag-1"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CompleteSharedMultipartUpload(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, []dtos.CompletedUploadPart{
		{PartNumber: 1, ETag: "etag-1"},
		{PartNumber: 2, ETag: "etag-2"},
	}, storage.completedParts)
}

func TestCompleteSharedMultipartUploadRejectsInvalidPartsAsBadRequest(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  5,
	}}
	publicUploadCounter = &mockUploadCounter{value: "2"}
	storage := &mockObjectStorage{}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)

	s3Key := "moments/" + eventID.String() + "/raw/video.mp4"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/complete",
		`{"upload_id":"upload-123","object_key":"`+s3Key+`","parts":[{"part_number":1,"etag":" "}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CompleteSharedMultipartUpload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Invalid multipart upload"`)
	assert.Equal(t, 0, storage.completeMultipartUploadCalls)
}

func TestCompleteSharedMultipartUploadInfersModernContentTypeFromObjectKey(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  5,
	}}
	publicUploadCounter = &mockUploadCounter{value: "2"}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	var capturedContentType string
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		CreateMomentFunc: func(moment *models.Moment) error {
			capturedContentType = moment.ContentType
			return nil
		},
	}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/clip.mkv"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/complete",
		`{"upload_id":"upload-123","object_key":"`+s3Key+`","parts":[{"part_number":1,"etag":"etag-1"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CompleteSharedMultipartUpload(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "video/x-matroska", capturedContentType)
}

func TestCompleteSharedMultipartUploadChecksQuotaBeforeCompletingStorage(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicUploadCounter = &mockUploadCounter{value: "3"}
	storage := &mockObjectStorage{}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)

	momentCreated := false
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		CreateMomentFunc: func(moment *models.Moment) error {
			momentCreated = true
			return nil
		},
	}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/clip.mp4"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/complete",
		`{"upload_id":"upload-123","object_key":"`+s3Key+`","content_type":"video/mp4","parts":[{"part_number":1,"etag":"etag-1"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, CompleteSharedMultipartUpload(c))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, 0, storage.completeMultipartUploadCalls)
	assert.False(t, momentCreated)
	assert.Contains(t, rec.Body.String(), `"uploads_remaining":0`)
}

func TestStartSharedMultipartUploadReturnsTypedContract(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  5,
	}}
	publicUploadCounter = &mockUploadCounter{value: "2"}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/start",
		`{"ContentType":"video/mp4","FileName":"clip.mp4","FileSize":`+strconv.FormatInt(multipartPartSize+1, 10)+`}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, StartSharedMultipartUpload(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status  int                                     `json:"status"`
		Message string                                  `json:"message"`
		Data    dtos.SharedMultipartUploadStartResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, http.StatusOK, body.Status)
	assert.Equal(t, "Multipart upload started", body.Message)
	assert.Equal(t, "upload-123", body.Data.UploadID)
	assert.Contains(t, body.Data.ObjectKey, "moments/"+eventID.String()+"/raw/")
	assert.Contains(t, body.Data.S3Key, "moments/"+eventID.String()+"/raw/")
	assert.Equal(t, body.Data.ObjectKey, body.Data.S3Key)
	assert.Equal(t, "video/mp4", body.Data.ContentType)
	require.Len(t, body.Data.PartURLs, 2)
	assert.Equal(t, 1, body.Data.PartURLs[0].PartNumber)
	assert.Contains(t, body.Data.PartURLs[0].URL, "/part/1")
	assert.Equal(t, 2, body.Data.PartURLs[1].PartNumber)
	require.NotNil(t, body.Data.UploadsLimit)
	require.NotNil(t, body.Data.UploadsUsed)
	require.NotNil(t, body.Data.UploadsRemaining)
	assert.Equal(t, int64(5), *body.Data.UploadsLimit)
	assert.Equal(t, int64(2), *body.Data.UploadsUsed)
	assert.Equal(t, int64(3), *body.Data.UploadsRemaining)
}

func TestRequestPublicMomentUploadURL_MissingPrettyTokenUsesAPIResponse(t *testing.T) {
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/upload-url",
		`{"content_type":"image/jpeg","filename":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, RequestPublicMomentUploadURL(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":401`)
	assert.Contains(t, rec.Body.String(), `"message":"Missing invitation token"`)
	assert.NotContains(t, rec.Body.String(), `"error":`)
}

func TestRequestPublicMomentUploadURL_PasswordProtectedWithoutProofReturns401(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	publicTokenRepo = &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: false,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
		AuthPasswordPreview: "secreto",
	}}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicUploadCounter = nil
	publicResSvc = nil

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/upload-url",
		`{"pretty_token":"RAW/123","content_type":"image/jpeg","filename":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, RequestPublicMomentUploadURL(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Event password required"`)
}

func TestRequestPublicMomentUploadURL_UsesDefaultConfigWhenMissing(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicUploadCounter = nil
	publicResSvc = nil

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/upload-url",
		`{"pretty_token":"RAW/123","content_type":"image/jpeg","filename":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, RequestPublicMomentUploadURL(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "RAW/123", tokenRepo.seen)
	assert.Contains(t, rec.Body.String(), `"message":"Uploads are closed because the moments wall is already published"`)
	assert.NotContains(t, rec.Body.String(), "Event config not found")
}

func TestRequestPublicMomentUploadURL_RejectsExpiredPrettyToken(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
	})

	invitationID := uuid.Must(uuid.NewV4())
	expiredAt := time.Now().Add(-time.Hour)
	publicTokenRepo = &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{
			InvitationID: invitationID,
			ExpiresAt:    &expiredAt,
		},
	}
	publicEventRepo = nil
	publicEventConfigRepo = nil
	publicInvitationRepo = &mockPublicInvitationRepo{}

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/upload-url",
		`{"pretty_token":"EXP123","content_type":"image/jpeg","filename":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, RequestPublicMomentUploadURL(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":401`)
	assert.Contains(t, rec.Body.String(), `"message":"Invalid invitation token"`)
}

func TestRequestPublicMomentUploadURL_UsesControllerClockForTokenAndAccessWindow(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	origNow := publicMomentNow
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		publicMomentNow = origNow
	})

	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Minute)
	activeUntil := now.Add(time.Minute)
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{
			InvitationID: invitationID,
			ExpiresAt:    &expiresAt,
		},
	}
	publicMomentNow = func() time.Time { return now }
	publicTokenRepo = tokenRepo
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: false,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
		ActiveUntil:         &activeUntil,
	}}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/upload-url",
		`{"pretty_token":"RAW/123","content_type":"image/jpeg","filename":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, RequestPublicMomentUploadURL(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "RAW/123", tokenRepo.seen)
	assert.Contains(t, rec.Body.String(), `"upload_url":`)
	assert.Contains(t, rec.Body.String(), `"object_key":`)
}

func TestRequestPublicMomentUploadURL_AllowsCamelPrettyTokenAlias(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: false,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/upload-url",
		`{"prettyToken":"RAW/123","contentType":"image/jpeg","fileName":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, RequestPublicMomentUploadURL(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "RAW/123", tokenRepo.seen)
	assert.Empty(t, tokenRepo.prettySeen)
	assert.Contains(t, rec.Body.String(), `"upload_url":`)
	assert.Contains(t, rec.Body.String(), `"object_key":`)
}

func TestRequestPublicMomentUploadURL_AllowsInvitationTokenAlias(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: false,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/upload-url",
		`{"invitationToken":"INV/123","contentType":"image/jpeg","fileName":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, RequestPublicMomentUploadURL(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "INV/123", tokenRepo.seen)
	assert.Contains(t, rec.Body.String(), `"upload_url":`)
}

func TestRequestPublicMomentUploadURL_AllowsPascalAliases(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: false,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicUploadCounter = nil
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/upload-url",
		`{"PrettyToken":"PASCAL/123","ContentType":"image/jpeg","FileName":"foto.jpg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, RequestPublicMomentUploadURL(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "PASCAL/123", tokenRepo.seen)
	assert.Contains(t, rec.Body.String(), `"upload_url":`)
	assert.Contains(t, rec.Body.String(), `"object_key":`)
}

func TestConfirmPublicMoment_AllowsTokenAlias(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: false,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicUploadCounter = &mockUploadCounter{value: "0", increment: 1}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/foto.jpg"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/confirm",
		`{"token":"RAW/123","object_key":"`+s3Key+`","content_type":"image/jpeg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, ConfirmPublicMoment(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "RAW/123", tokenRepo.seen)
	assert.Empty(t, tokenRepo.prettySeen)
	assert.Contains(t, rec.Body.String(), `"content_url":"`+s3Key+`"`)
}

func TestConfirmPublicMoment_AllowsInvitationTokenAlias(t *testing.T) {
	origTokenRepo := publicTokenRepo
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origInvitationRepo := publicInvitationRepo
	origUploadCounter := publicUploadCounter
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicTokenRepo = origTokenRepo
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicInvitationRepo = origInvitationRepo
		publicUploadCounter = origUploadCounter
		publicResSvc = origResSvc
		momentsService.SetDefaultMomentService(nil)
	})

	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenRepo := &mockPublicAccessTokenRepo{
		token: &models.InvitationAccessToken{InvitationID: invitationID},
	}
	publicTokenRepo = tokenRepo
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Privado",
		Identifier: "evento-privado",
	}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: false,
		ShowMomentWall:      false,
		MaxUploadsPerGuest:  3,
	}}
	publicInvitationRepo = &mockPublicInvitationRepo{
		invitation: &models.Invitation{ID: invitationID, EventID: eventID},
	}
	publicUploadCounter = &mockUploadCounter{value: "0", increment: 1}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{}))

	s3Key := "moments/" + eventID.String() + "/raw/foto.jpg"
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-privado/moments/confirm",
		`{"InvitationToken":"INV/123","ObjectKey":"`+s3Key+`","ContentType":"image/jpeg"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-privado")

	require.NoError(t, ConfirmPublicMoment(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "INV/123", tokenRepo.seen)
	assert.Contains(t, rec.Body.String(), `"content_url":"`+s3Key+`"`)
}

func TestAbortSharedMultipartUpload_RejectsKeyFromAnotherEvent(t *testing.T) {
	origEventRepo := publicEventRepo
	origResSvc := publicResSvc
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicResSvc = origResSvc
	})

	eventID := uuid.Must(uuid.NewV4())
	otherEventID := uuid.Must(uuid.NewV4())
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{
		ID:         eventID,
		Name:       "Evento Compartido",
		Identifier: "evento-compartido",
	}}
	publicResSvc = &resourcesService.ResourceService{}

	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento-compartido/moments/shared/multipart/abort",
		`{"upload_id":"upload-123","s3_key":"moments/`+otherEventID.String()+`/raw/video.mp4"}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento-compartido")

	require.NoError(t, AbortSharedMultipartUpload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Invalid upload key"`)
	assert.Contains(t, rec.Body.String(), `"invalid upload key for event"`)
}

func TestValidInternalAPISecretSupportsZeroDowntimeRotation(t *testing.T) {
	t.Setenv("INTERNAL_API_SECRET", "current-secret")
	t.Setenv("INTERNAL_API_SECRET_PREVIOUS", "rotation-secret")

	assert.True(t, validInternalAPISecret("current-secret"))
	assert.True(t, validInternalAPISecret("rotation-secret"))
	assert.False(t, validInternalAPISecret("wrong-secret"))
	assert.False(t, validInternalAPISecret(""))

	t.Setenv("INTERNAL_API_SECRET", "")
	assert.True(t, validInternalAPISecret("rotation-secret"))
}

func TestUpdateMomentContent_PersistsWorkerErrorMessage(t *testing.T) {
	t.Setenv("INTERNAL_API_SECRET", "secret")

	momentID := uuid.Must(uuid.NewV4())
	var capturedError string
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			assert.Equal(t, momentID, id)
			assert.Equal(t, "moments/raw/video.mp4", contentURL)
			assert.Equal(t, "failed", processingStatus)
			capturedError = errorMessage
			return nil
		},
	}
	svc := momentsService.NewMomentService(repo, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(
		http.MethodPut,
		"/moments/"+momentID.String()+"/content",
		`{"content_url":"moments/raw/video.mp4","processing_status":"failed","error_message":"ffmpeg exited with code 1"}`,
	)
	c.Request().Header.Set("X-Internal-Secret", "secret")
	c.SetParamNames("id")
	c.SetParamValues(momentID.String())

	require.NoError(t, UpdateMomentContent(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ffmpeg exited with code 1", capturedError)
}

func TestUpdateMomentContentAcceptsObjectKeyAliases(t *testing.T) {
	t.Setenv("INTERNAL_API_SECRET", "secret")

	momentID := uuid.Must(uuid.NewV4())
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			assert.Equal(t, momentID, id)
			assert.Equal(t, "moments/event/photos/photo.webp", contentURL)
			assert.Equal(t, "done", processingStatus)
			assert.Equal(t, "moments/event/photos/photo-thumb.webp", thumbnailURL)
			assert.Equal(t, int64(250), durationMs)
			assert.Equal(t, int64(1200), originalBytes)
			assert.Equal(t, int64(300), optimizedBytes)
			return nil
		},
	}
	svc := momentsService.NewMomentService(repo, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(
		http.MethodPut,
		"/moments/"+momentID.String()+"/content",
		`{"object_key":"moments/event/photos/photo.webp","content_url":"legacy-ignored","thumbnail_object_key":"moments/event/photos/photo-thumb.webp","thumbnail_url":"legacy-thumb-ignored","processing_status":"done","processing_duration_ms":250,"original_size_bytes":1200,"optimized_size_bytes":300}`,
	)
	c.Request().Header.Set("X-Internal-Secret", "secret")
	c.SetParamNames("id")
	c.SetParamValues(momentID.String())

	require.NoError(t, UpdateMomentContent(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateMomentContentAcceptsCamelCaseWorkerPayload(t *testing.T) {
	t.Setenv("INTERNAL_API_SECRET", "secret")

	momentID := uuid.Must(uuid.NewV4())
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			assert.Equal(t, momentID, id)
			assert.Equal(t, "moments/event/photos/photo.webp", contentURL)
			assert.Equal(t, "done", processingStatus)
			assert.Equal(t, "moments/event/photos/photo-thumb.webp", thumbnailURL)
			assert.Equal(t, "worker ok", errorMessage)
			assert.Equal(t, int64(250), durationMs)
			assert.Equal(t, int64(1200), originalBytes)
			assert.Equal(t, int64(300), optimizedBytes)
			return nil
		},
	}
	svc := momentsService.NewMomentService(repo, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(
		http.MethodPut,
		"/moments/"+momentID.String()+"/content",
		`{"objectKey":"moments/event/photos/photo.webp","contentUrl":"legacy-ignored","thumbnailObjectKey":"moments/event/photos/photo-thumb.webp","thumbnailUrl":"legacy-thumb-ignored","processingStatus":"done","processingDurationMs":250,"originalSizeBytes":1200,"optimizedSizeBytes":300,"errorMessage":"worker ok"}`,
	)
	c.Request().Header.Set("X-Internal-Secret", "secret")
	c.SetParamNames("id")
	c.SetParamValues(momentID.String())

	require.NoError(t, UpdateMomentContent(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateMomentContentRejectsInvalidProcessingStatus(t *testing.T) {
	t.Setenv("INTERNAL_API_SECRET", "secret")

	momentID := uuid.Must(uuid.NewV4())
	var updated bool
	repo := &mockMomentRepo{
		UpdateContentFunc: func(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
			updated = true
			return nil
		},
	}
	svc := momentsService.NewMomentService(repo, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(
		http.MethodPut,
		"/moments/"+momentID.String()+"/content",
		`{"content_url":"moments/raw/video.mp4","processing_status":"ready"}`,
	)
	c.Request().Header.Set("X-Internal-Secret", "secret")
	c.SetParamNames("id")
	c.SetParamValues(momentID.String())

	require.NoError(t, UpdateMomentContent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, updated)
	assert.Contains(t, rec.Body.String(), `"message":"Invalid processing status"`)
}

func TestUpdateMomentContentRejectsStaleGenerationWithConflict(t *testing.T) {
	t.Setenv("INTERNAL_API_SECRET", "secret")
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	currentJobID := uuid.Must(uuid.NewV4()).String()
	repo := &casCallbackMomentRepo{
		mockMomentRepo: &mockMomentRepo{},
		moment: &models.Moment{
			ID: momentID, EventID: &eventID, ContentType: "image/jpeg",
			ContentURL:         "moments/" + eventID.String() + "/raw/photo.jpg",
			ProcessingInputKey: "moments/" + eventID.String() + "/raw/photo.jpg",
			ProcessingStatus:   "processing", ProcessingGeneration: 2, ProcessingJobID: currentJobID,
		},
	}
	svc := momentsService.NewMomentService(repo, nil)
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodPut, "/moments/"+momentID.String()+"/content",
		`{"event_id":"`+eventID.String()+`","job_id":"`+uuid.Must(uuid.NewV4()).String()+`","generation":1,"object_key":"moments/`+eventID.String()+`/photos/`+momentID.String()+`.webp","processing_status":"done"}`)
	c.Request().Header.Set("X-Internal-Secret", "secret")
	c.SetParamNames("id")
	c.SetParamValues(momentID.String())

	require.NoError(t, UpdateMomentContent(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), `"message":"Stale processing callback"`)
}

func TestDeleteMoment_InvalidUUID_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/moments/bad-id", "")
	c.SetParamNames("id")
	c.SetParamValues("bad-id")
	require.NoError(t, DeleteMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetMoment_InvalidUUID_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/moments/bad-id", "")
	c.SetParamNames("id")
	c.SetParamValues("bad-id")
	require.NoError(t, GetMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateMoment_InvalidBody_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/moments", `{invalid json}`)
	require.NoError(t, CreateMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateMoment_ValidBody_Returns201(t *testing.T) {
	svc := momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPost, "/moments", `{"event_id":"`+eventID.String()+`","title":"Wedding"}`)
	setRootAuth(t, c)
	require.NoError(t, CreateMoment(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestBatchReoptimizeMomentsReturnsTypedContract(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	eligibleID := uuid.Must(uuid.NewV4())
	skippedID := uuid.Must(uuid.NewV4())
	var published dtos.MediaProcessMessage

	repo := &mockMomentRepo{
		GetMomentsByIDsFunc: func(ids []uuid.UUID) ([]models.Moment, error) {
			assert.ElementsMatch(t, []uuid.UUID{eligibleID, skippedID}, ids)
			return []models.Moment{
				{
					ID:                 eligibleID,
					EventID:            &eventID,
					ContentURL:         "moments/" + eventID.String() + "/optimized/foto.webp",
					ContentType:        "image/webp",
					ProcessingStatus:   "done",
					OptimizedSizeBytes: 1024,
				},
				{
					ID:               skippedID,
					EventID:          &eventID,
					ContentURL:       "moments/" + eventID.String() + "/raw/video.mp4",
					ContentType:      "video/mp4",
					ProcessingStatus: "done",
				},
			}, nil
		},
	}
	svc := momentsService.NewMomentService(repo, &mockCacheRepo{}, &mockMediaPublisher{
		PublishFunc: func(msg dtos.MediaProcessMessage) (bool, error) {
			published = msg
			return true, nil
		},
	})

	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(
		http.MethodPost,
		"/moments/batch/reoptimize",
		`{"ids":["`+eligibleID.String()+`","`+skippedID.String()+`"]}`,
	)
	setRootAuth(t, c)

	require.NoError(t, BatchReoptimizeMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status  int                            `json:"status"`
		Message string                         `json:"message"`
		Data    dtos.MomentBatchResultResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Moments queued for reoptimization", body.Message)
	assert.Equal(t, dtos.MomentBatchResultResponse{Succeeded: 1, Skipped: 1, Failed: 0}, body.Data)
	assert.Equal(t, eligibleID.String(), published.MomentID)
	assert.Equal(t, "moments/"+eventID.String()+"/optimized/foto.webp", published.ObjectKey)
	assert.Equal(t, "moments/"+eventID.String()+"/optimized/foto.webp", published.RawS3Key)
}

func TestBulkDeleteMoments_InvalidBody_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/moments/bulk", `{invalid json}`)
	setRootAuth(t, c)
	require.NoError(t, BulkDeleteMoments(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestBulkDeleteMoments_EmptyIDs_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/moments/bulk", `{"ids":[]}`)
	setRootAuth(t, c)
	require.NoError(t, BulkDeleteMoments(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestBulkDeleteMoments_ValidBody_Returns200(t *testing.T) {
	svc := momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	id1 := uuid.Must(uuid.NewV4())
	id2 := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodDelete, "/moments/bulk", `{"ids":["`+id1.String()+`","`+id2.String()+`"]}`)
	setRootAuth(t, c)
	require.NoError(t, BulkDeleteMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateMoment_InvalidBody_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/moments/"+id.String(), `{invalid json}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setRootAuth(t, c)
	require.NoError(t, UpdateMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateMomentAcceptsIsApprovedCamelAlias(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	var capturedApproved bool
	svc := momentsService.NewMomentService(&mockMomentRepo{
		UpdateMomentFunc: func(moment *models.Moment) error {
			capturedApproved = moment.IsApproved
			return nil
		},
	}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodPut, "/moments/"+id.String(), `{"isApproved":true}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	setRootAuth(t, c)

	require.NoError(t, UpdateMoment(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, capturedApproved)
}

func TestBulkApproveRejectMomentsAcceptsIsApprovedCamelAlias(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	var capturedIDs []uuid.UUID
	var capturedApproved bool
	svc := momentsService.NewMomentService(&mockMomentRepo{
		BulkUpdateApprovalFunc: func(ids []uuid.UUID, isApproved bool) error {
			capturedIDs = append([]uuid.UUID(nil), ids...)
			capturedApproved = isApproved
			return nil
		},
	}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(
		http.MethodPost,
		"/moments/bulk-approve",
		`{"ids":["`+id.String()+`"],"isApproved":true}`,
	)
	setRootAuth(t, c)

	require.NoError(t, BulkApproveRejectMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []uuid.UUID{id}, capturedIDs)
	assert.True(t, capturedApproved)
}

func TestListMoments_Returns200(t *testing.T) {
	svc := momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/moments", "")
	setRootAuth(t, c)
	require.NoError(t, ListMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestListMoments_ReturnsBoundedDashboardPageWithGlobalCounts(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	repo := &mockMomentRepo{
		ListForDashboardPageFunc: func(gotEventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error) {
			assert.Equal(t, eventID, gotEventID)
			assert.Equal(t, 2, page)
			assert.Equal(t, 40, pageSize)
			return []models.Moment{{ID: momentID, EventID: &eventID}}, dtos.MomentDashboardCounts{
				Total: 95, Pending: 12, Approved: 80, Failed: 3,
			}, nil
		},
		ListProcessingFunc: func(gotEventID uuid.UUID, rawOnly bool) ([]models.Moment, error) {
			assert.Equal(t, eventID, gotEventID)
			status := "reoptimizing"
			if rawOnly {
				status = "processing"
			}
			return []models.Moment{{ID: uuid.Must(uuid.NewV4()), EventID: &eventID, ProcessingStatus: status}}, nil
		},
	}
	svc := momentsService.NewMomentService(repo, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/moments?event_id="+eventID.String()+"&page=2&page_size=40", "")
	setRootAuth(t, c)
	require.NoError(t, ListMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"total":95`)
	assert.Contains(t, rec.Body.String(), `"page":2`)
	assert.Contains(t, rec.Body.String(), `"page_size":40`)
	assert.Contains(t, rec.Body.String(), `"total_pages":3`)
	assert.Contains(t, rec.Body.String(), `"pending":12`)
	assert.Contains(t, rec.Body.String(), momentID.String())
	assert.Contains(t, rec.Body.String(), `"in_flight":[`)
	assert.Contains(t, rec.Body.String(), `"reoptimizing":[`)
}

func TestListMoments_PresignsAdminMediaURLs(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	thumbnailPath := "moments/" + eventID.String() + "/thumbs/video.webp"
	contentPath := "moments/" + eventID.String() + "/optimized/photo.webp"
	absoluteURL := "https://cdn.example.com/already-signed.jpg"

	repo := &mockMomentRepo{
		ListForDashboardFunc: func(gotEventID uuid.UUID) ([]models.Moment, error) {
			assert.Equal(t, eventID, gotEventID)
			return []models.Moment{
				{
					ID:           uuid.Must(uuid.NewV4()),
					EventID:      &eventID,
					ContentURL:   contentPath,
					ThumbnailURL: thumbnailPath,
				},
				{
					ID:         uuid.Must(uuid.NewV4()),
					EventID:    &eventID,
					ContentURL: absoluteURL,
				},
			}, nil
		},
	}
	svc := momentsService.NewMomentService(repo, &mockCacheRepo{})
	resSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockObjectStorage{}},
	)

	origSvc := momentSvc
	origResSvc := adminResSvc
	momentSvc = svc
	adminResSvc = resSvc
	defer func() {
		momentSvc = origSvc
		adminResSvc = origResSvc
	}()

	c, rec := newEchoCtx(http.MethodGet, "/moments?event_id="+eventID.String(), "")
	setRootAuth(t, c)

	require.NoError(t, ListMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"content_url":"`+contentPath+`"`)
	assert.Contains(t, rec.Body.String(), `"thumbnail_url":"`+thumbnailPath+`"`)
	assert.Contains(t, rec.Body.String(), "https://signed.example.com/"+contentPath+"?ttl=720")
	assert.Contains(t, rec.Body.String(), "https://signed.example.com/"+thumbnailPath+"?ttl=720")
	assert.Contains(t, rec.Body.String(), `"content_view_url_expires_at":`)
	assert.Contains(t, rec.Body.String(), `"thumbnail_view_url_expires_at":`)
	assert.Contains(t, rec.Body.String(), absoluteURL)
}

func TestListMomentSummaries_MissingEventIDs_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/moments/summary", "")
	setRootAuth(t, c)
	require.NoError(t, ListMomentSummaries(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListMomentSummaries_InvalidEventID_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/moments/summary?event_ids=not-a-uuid", "")
	setRootAuth(t, c)
	require.NoError(t, ListMomentSummaries(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListMomentSummaries_Returns200(t *testing.T) {
	svc := momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	eventID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodGet, "/moments/summary?event_ids="+eventID.String(), "")
	setRootAuth(t, c)
	require.NoError(t, ListMomentSummaries(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"pending_count":2`)
}

func setupConfirmMomentTest(t *testing.T, repo *mockMomentRepo, storage *mockObjectStorage, counter *mockUploadCounter) {
	t.Helper()
	origResSvc := publicResSvc
	origCounter := publicUploadCounter
	t.Cleanup(func() {
		publicResSvc = origResSvc
		publicUploadCounter = origCounter
		momentsService.SetDefaultMomentService(nil)
	})
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)
	publicUploadCounter = counter
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(repo, &mockCacheRepo{}))
}

func TestConfirmPresignedMomentRetryReturnsExistingWithoutQuotaOrCreate(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	key := "moments/" + eventID.String() + "/raw/photo.webp"
	existing := &models.Moment{ID: uuid.Must(uuid.NewV4()), EventID: &eventID, ContentURL: key}
	createCalls := 0
	repo := &mockMomentRepo{
		GetByEventAndContentURLFunc: func(gotEventID uuid.UUID, gotKey string) (*models.Moment, error) {
			assert.Equal(t, eventID, gotEventID)
			assert.Equal(t, key, gotKey)
			return existing, nil
		},
		CreateMomentFunc: func(*models.Moment) error {
			createCalls++
			return nil
		},
	}
	storage := &mockObjectStorage{}
	counter := &mockUploadCounter{value: "4"}
	setupConfirmMomentTest(t, repo, storage, counter)
	c, _ := newEchoCtx(http.MethodPost, "/confirm", "")

	moment, err := confirmPresignedMoment(c, &models.Event{ID: eventID}, &models.EventConfig{MaxUploadsPerGuest: 5}, nil, key, "image/webp", 0, "")

	require.NoError(t, err)
	assert.Equal(t, existing.ID, moment.ID)
	assert.Zero(t, createCalls)
	assert.Zero(t, counter.increments)
	assert.Empty(t, storage.deletedObjects)
}

func TestCompleteSharedMultipartUploadRetryReturnsExistingBeforeQuotaOrStorageCompletion(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	origResSvc := publicResSvc
	origCounter := publicUploadCounter
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
		publicResSvc = origResSvc
		publicUploadCounter = origCounter
		momentsService.SetDefaultMomentService(nil)
	})
	eventID := uuid.Must(uuid.NewV4())
	key := "moments/" + eventID.String() + "/raw/clip.mp4"
	existing := &models.Moment{ID: uuid.Must(uuid.NewV4()), EventID: &eventID, ContentURL: key, ContentType: "video/mp4"}
	storage := &mockObjectStorage{}
	counter := &mockUploadCounter{value: "1"}
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{ID: eventID, Name: "Evento", Identifier: "evento"}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads: true, ShareUploadsEnabled: true, MaxUploadsPerGuest: 1,
	}}
	publicResSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: storage},
	)
	publicUploadCounter = counter
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(&mockMomentRepo{
		GetByEventAndContentURLFunc: func(uuid.UUID, string) (*models.Moment, error) { return existing, nil },
		CreateMomentFunc:            func(*models.Moment) error { t.Fatal("retry must not create another moment"); return nil },
	}, &mockCacheRepo{}))
	c, rec := newEchoCtx(
		http.MethodPost,
		"/api/events/evento/moments/shared/multipart/complete",
		`{"upload_id":"completed-upload","object_key":"`+key+`","content_type":"video/mp4","parts":[{"part_number":1,"etag":"etag-1"}]}`,
	)
	c.SetParamNames("identifier")
	c.SetParamValues("evento")

	require.NoError(t, CompleteSharedMultipartUpload(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Zero(t, storage.completeMultipartUploadCalls)
	assert.Zero(t, counter.increments)
	assert.Contains(t, rec.Body.String(), existing.ID.String())
}

func TestConfirmPresignedMomentDatabaseFailureDeletesObjectAndReleasesQuota(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	key := "moments/" + eventID.String() + "/raw/photo.jpg"
	repo := &mockMomentRepo{
		GetByEventAndContentURLFunc: func(uuid.UUID, string) (*models.Moment, error) {
			return nil, gorm.ErrRecordNotFound
		},
		CreateMomentFunc: func(*models.Moment) error { return errors.New("database unavailable") },
	}
	storage := &mockObjectStorage{}
	counter := &mockUploadCounter{value: "0"}
	setupConfirmMomentTest(t, repo, storage, counter)
	c, rec := newEchoCtx(http.MethodPost, "/confirm", "")

	_, err := confirmPresignedMoment(c, &models.Event{ID: eventID}, &models.EventConfig{MaxUploadsPerGuest: 5}, nil, key, "image/jpeg", 0, "")

	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, 1, counter.increments)
	assert.Equal(t, 1, counter.decrements)
	assert.Equal(t, "0", counter.value)
	assert.Equal(t, []string{key}, storage.deletedObjects)
}

func TestConfirmPresignedMomentQuotaFailureDeletesObjectAndReleasesReservation(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	key := "moments/" + eventID.String() + "/raw/photo.jpg"
	createCalls := 0
	repo := &mockMomentRepo{
		GetByEventAndContentURLFunc: func(uuid.UUID, string) (*models.Moment, error) {
			return nil, gorm.ErrRecordNotFound
		},
		CreateMomentFunc: func(*models.Moment) error { createCalls++; return nil },
	}
	storage := &mockObjectStorage{}
	counter := &mockUploadCounter{value: "1"}
	setupConfirmMomentTest(t, repo, storage, counter)
	c, rec := newEchoCtx(http.MethodPost, "/confirm", "")

	_, err := confirmPresignedMoment(c, &models.Event{ID: eventID, Name: "Evento"}, &models.EventConfig{MaxUploadsPerGuest: 1}, nil, key, "image/jpeg", 0, "")

	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Zero(t, createCalls)
	assert.Equal(t, 1, counter.decrements)
	assert.Equal(t, "1", counter.value)
	assert.Equal(t, []string{key}, storage.deletedObjects)
}

func TestConfirmPresignedMomentVerificationFailureDeletesObjectWithoutConsumingQuota(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	key := "moments/" + eventID.String() + "/raw/photo.jpg"
	repo := &mockMomentRepo{GetByEventAndContentURLFunc: func(uuid.UUID, string) (*models.Moment, error) {
		return nil, gorm.ErrRecordNotFound
	}}
	storage := &mockObjectStorage{missingObject: true}
	counter := &mockUploadCounter{value: "0"}
	setupConfirmMomentTest(t, repo, storage, counter)
	c, rec := newEchoCtx(http.MethodPost, "/confirm", "")

	_, err := confirmPresignedMoment(c, &models.Event{ID: eventID}, &models.EventConfig{MaxUploadsPerGuest: 5}, nil, key, "image/jpeg", 0, "")

	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Zero(t, counter.increments)
	assert.Zero(t, counter.decrements)
	assert.Equal(t, []string{key}, storage.deletedObjects)
}

func TestCreateSharedMomentDatabaseFailureDeletesDirectUploadAndReleasesQuota(t *testing.T) {
	origEventRepo := publicEventRepo
	origConfigRepo := publicEventConfigRepo
	t.Cleanup(func() {
		publicEventRepo = origEventRepo
		publicEventConfigRepo = origConfigRepo
	})
	eventID := uuid.Must(uuid.NewV4())
	storage := &mockObjectStorage{}
	counter := &mockUploadCounter{value: "0"}
	repo := &mockMomentRepo{CreateMomentFunc: func(*models.Moment) error { return errors.New("database unavailable") }}
	setupConfirmMomentTest(t, repo, storage, counter)
	publicEventRepo = &mockPublicEventRepo{event: &models.Event{ID: eventID, Name: "Evento", Identifier: "evento"}}
	publicEventConfigRepo = &mockPublicEventConfigRepo{cfg: &models.EventConfig{
		AllowUploads: true, ShareUploadsEnabled: true, MaxUploadsPerGuest: 5,
	}}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "photo.jpg")
	require.NoError(t, err)
	_, err = part.Write([]byte("jpeg payload"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/events/evento/moments/shared", &body)
	req.Header.Set(echo.HeaderContentType, writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("identifier")
	c.SetParamValues("evento")

	require.NoError(t, CreateSharedMoment(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, 1, counter.decrements)
	assert.Equal(t, "0", counter.value)
	require.Len(t, storage.deletedObjects, 1)
	assert.Contains(t, storage.deletedObjects[0], "moments/"+eventID.String()+"/raw/")
}
