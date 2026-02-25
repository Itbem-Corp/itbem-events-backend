//go:build integration

package integration_test

// Integration tests for the public moments endpoints:
//   GET  /api/events/:identifier/moments          — ListPublicMoments
//   POST /api/events/:identifier/moments/shared   — CreateSharedMoment (QR upload, no token)
//
// TestMain (Postgres + miniredis) is declared in rsvp_test.go and shared by the whole package.
// Tests that would reach S3 (UploadRawToMomentsFolder) are not covered here;
// add a Localstack container in TestMain for full happy-path coverage.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"events-stocks/configuration"
	momentsCtrl "events-stocks/controllers/moments"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	invitationaccesstokenrepository "events-stocks/repositories/invitationaccesstokenrepository"
	momentrepository "events-stocks/repositories/momentrepository"
	redisrepository "events-stocks/repositories/redisrepository"
	momentsSvc "events-stocks/services/moments"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// sharedUploadEcho returns a minimal Echo instance wired for public moment routes.
// publicResSvc is left nil: upload tests that reach the S3 call path will get
// a nil-dereference (caught by the Recover test variant).
// The package-level moments service is wired with real repos so ListPublicMoments works.
func sharedUploadEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.Validator = customValidator.New()
	tokenRepo := invitationaccesstokenrepository.NewAccessTokenRepo()
	momentsCtrl.InitPublicMomentsController(tokenRepo, nil)
	// Wire moments service so ListPublicMoments can call _momentSvc.ListApprovedForWall.
	svc := momentsSvc.NewMomentService(momentrepository.NewMomentRepo(), redisrepository.NewRedisRepo())
	momentsSvc.SetDefaultMomentService(svc)
	e.GET("/api/events/:identifier/moments", momentsCtrl.ListPublicMoments)
	e.POST("/api/events/:identifier/moments/shared", momentsCtrl.CreateSharedMoment)
	return e
}

type sharedUploadOpts struct {
	shareEnabled bool
	allowUploads bool
	showWall     bool
}

type sharedUploadFx struct {
	identifier string
	eventID    uuid.UUID
}

// setupMomentsEvent creates an Event + EventConfig with the given options and
// registers a t.Cleanup to delete them after the test.
func setupMomentsEvent(t *testing.T, opts sharedUploadOpts) sharedUploadFx {
	t.Helper()
	db := configuration.DB
	suffix := uuid.Must(uuid.NewV4()).String()[:8]

	eventType := models.EventType{
		ID: uuid.Must(uuid.NewV4()), Name: "upload-type-" + suffix,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Create(&eventType).Error)

	eventID := uuid.Must(uuid.NewV4())
	identifier := "test-up-" + suffix
	event := models.Event{
		ID: eventID, Name: "Upload Test " + suffix, Identifier: identifier,
		EventTypeID: eventType.ID, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Create(&event).Error)

	cfg := models.EventConfig{
		ID:                  eventID, // 1:1 with Event
		ShareUploadsEnabled: opts.shareEnabled,
		AllowUploads:        opts.allowUploads,
		ShowMomentWall:      opts.showWall,
		MaxUploadsPerGuest:  30,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	require.NoError(t, db.Create(&cfg).Error)

	t.Cleanup(func() {
		db.Unscoped().Where("id = ?", eventID).Delete(&models.EventConfig{})
		db.Unscoped().Where("id = ?", eventID).Delete(&models.Event{})
		db.Unscoped().Where("id = ?", eventType.ID).Delete(&models.EventType{})
	})

	return sharedUploadFx{identifier: identifier, eventID: eventID}
}

// multipartNoFile builds a POST request with only a description field (no file).
func multipartNoFile(t *testing.T, url string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("description", "test description")
	w.Close()
	req := httptest.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// multipartWithFile builds a POST request with a minimal JPEG file attached.
// The part Content-Type is set to "image/jpeg" explicitly so the MIME validation
// guard in UploadRawToMomentsFolder passes (it reads header.Header.Get("Content-Type")).
func multipartWithFile(t *testing.T, url string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// Use CreatePart directly so we can set Content-Type: image/jpeg on the part.
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="photo.jpg"`)
	h.Set("Content-Type", "image/jpeg")
	fw, err := w.CreatePart(h)
	require.NoError(t, err)
	// Minimal JPEG: SOI + EOI markers — enough to pass FormFile parsing
	_, err = fw.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	require.NoError(t, err)
	_ = w.WriteField("description", "Un momento especial")
	w.Close()
	req := httptest.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// ── CreateSharedMoment ────────────────────────────────────────────────────────

func TestCreateSharedMoment_EventNotFound_Returns404(t *testing.T) {
	e := sharedUploadEcho()
	req := multipartNoFile(t, "/api/events/does-not-exist-xyz/moments/shared")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

func TestCreateSharedMoment_NoEventConfig_Returns404(t *testing.T) {
	// Event exists but no EventConfig row → 404
	db := configuration.DB
	suffix := uuid.Must(uuid.NewV4()).String()[:8]

	eventType := models.EventType{
		ID: uuid.Must(uuid.NewV4()), Name: "nocfg-type-" + suffix,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Create(&eventType).Error)

	eventID := uuid.Must(uuid.NewV4())
	identifier := "nocfg-" + suffix
	event := models.Event{
		ID: eventID, Name: "NoCfg " + suffix, Identifier: identifier,
		EventTypeID: eventType.ID, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Create(&event).Error)
	t.Cleanup(func() {
		db.Unscoped().Where("id = ?", eventID).Delete(&models.Event{})
		db.Unscoped().Where("id = ?", eventType.ID).Delete(&models.EventType{})
	})

	e := sharedUploadEcho()
	req := multipartNoFile(t, fmt.Sprintf("/api/events/%s/moments/shared", identifier))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

func TestCreateSharedMoment_ShareUploadsDisabled_Returns403(t *testing.T) {
	fx := setupMomentsEvent(t, sharedUploadOpts{shareEnabled: false, allowUploads: true})
	e := sharedUploadEcho()
	req := multipartNoFile(t, fmt.Sprintf("/api/events/%s/moments/shared", fx.identifier))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
}

func TestCreateSharedMoment_AllUploadsDisabled_Returns403(t *testing.T) {
	fx := setupMomentsEvent(t, sharedUploadOpts{shareEnabled: true, allowUploads: false})
	e := sharedUploadEcho()
	req := multipartNoFile(t, fmt.Sprintf("/api/events/%s/moments/shared", fx.identifier))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
}

func TestCreateSharedMoment_MissingFile_Returns400(t *testing.T) {
	fx := setupMomentsEvent(t, sharedUploadOpts{shareEnabled: true, allowUploads: true})
	e := sharedUploadEcho()
	// Description field present but no "file" part → 400
	req := multipartNoFile(t, fmt.Sprintf("/api/events/%s/moments/shared", fx.identifier))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
}

// TestCreateSharedMoment_WithFile_ReachesS3 verifies the handler proceeds past all
// validations and attempts the S3 upload. Without a real S3/Localstack the call will
// panic (nil publicResSvc) → Echo's Recover returns 500 — which tells us every
// guard before S3 passed successfully.
func TestCreateSharedMoment_WithFile_ReachesS3(t *testing.T) {
	fx := setupMomentsEvent(t, sharedUploadOpts{shareEnabled: true, allowUploads: true})

	// Add Recover middleware so the nil S3 panic is caught and returns 500.
	e := sharedUploadEcho()
	e.Use(echo.WrapMiddleware(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			h.ServeHTTP(w, r)
		})
	}))

	req := multipartWithFile(t, fmt.Sprintf("/api/events/%s/moments/shared", fx.identifier))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// 500 = got past all guards and hit the (nil) S3 call.
	// 403/404/400 would mean a guard rejected the request unexpectedly.
	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"expected to reach S3 (nil → 500), got: %s", rec.Body.String())
}

// ── ListPublicMoments ─────────────────────────────────────────────────────────

func TestListPublicMoments_EventNotFound_Returns404(t *testing.T) {
	e := sharedUploadEcho()
	req := httptest.NewRequest(http.MethodGet, "/api/events/ghost-event-xyz/moments", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

func TestListPublicMoments_WallHidden_ReturnsEmptyUnpublished(t *testing.T) {
	// ShowMomentWall=false → endpoint returns 200 with published=false and empty items
	fx := setupMomentsEvent(t, sharedUploadOpts{showWall: false})
	e := sharedUploadEcho()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/events/%s/moments?page=1&limit=20", fx.identifier), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok, "expected data object in response")
	assert.Equal(t, false, data["published"])
	items, _ := data["items"].([]interface{})
	assert.Empty(t, items)
}

func TestListPublicMoments_WallEnabled_ReturnsEmptyList(t *testing.T) {
	// ShowMomentWall=true, no moments → 200 with published=true, empty items
	fx := setupMomentsEvent(t, sharedUploadOpts{showWall: true})
	e := sharedUploadEcho()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/events/%s/moments?page=1&limit=20", fx.identifier), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok, "expected data object in response")
	assert.Equal(t, true, data["published"])
	items, _ := data["items"].([]interface{})
	assert.Empty(t, items)
}

// ── Preview Token ─────────────────────────────────────────────────────────────

// previewEcho wires both ListPublicMoments and CreatePreviewToken.
// No JWT middleware — tests call the preview-token endpoint directly.
func previewEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.Validator = customValidator.New()
	tokenRepo := invitationaccesstokenrepository.NewAccessTokenRepo()
	momentsCtrl.InitPublicMomentsController(tokenRepo, nil)
	svc := momentsSvc.NewMomentService(momentrepository.NewMomentRepo(), redisrepository.NewRedisRepo())
	momentsSvc.SetDefaultMomentService(svc)
	e.GET("/api/events/:identifier/moments", momentsCtrl.ListPublicMoments)
	e.POST("/events/:id/preview-token", momentsCtrl.CreatePreviewToken)
	return e
}

func TestPreviewToken_ValidToken_BypassesWall(t *testing.T) {
	fx := setupMomentsEvent(t, sharedUploadOpts{showWall: false})
	e := previewEcho()

	token := uuid.Must(uuid.NewV4()).String()
	redisKey := fmt.Sprintf("preview:moments:%s:%s", fx.eventID.String(), token)
	require.NoError(t, redisrepository.SaveKey(context.Background(), redisKey, "1", time.Hour))

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/events/%s/moments?preview_token=%s", fx.identifier, token), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data := resp["data"].(map[string]interface{})
	assert.NotEqual(t, false, data["published"], "preview should bypass wall")
}

func TestPreviewToken_InvalidToken_Returns403(t *testing.T) {
	fx := setupMomentsEvent(t, sharedUploadOpts{showWall: false})
	e := previewEcho()

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/events/%s/moments?preview_token=not-a-real-token", fx.identifier), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestPreviewToken_ValidToken_AllowsPagination(t *testing.T) {
	fx := setupMomentsEvent(t, sharedUploadOpts{showWall: false})
	e := previewEcho()

	token := uuid.Must(uuid.NewV4()).String()
	redisKey := fmt.Sprintf("preview:moments:%s:%s", fx.eventID.String(), token)
	require.NoError(t, redisrepository.SaveKey(context.Background(), redisKey, "1", time.Hour))

	url := fmt.Sprintf("/api/events/%s/moments?preview_token=%s", fx.identifier, token)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "page %d should succeed with same token", i+1)
	}
}
