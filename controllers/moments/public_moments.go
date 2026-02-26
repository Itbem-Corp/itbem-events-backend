package moments

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/configuration/constants"
	"events-stocks/models"
	"events-stocks/repositories/bucketrepository"
	"events-stocks/repositories/clientrepository"
	"events-stocks/repositories/eventconfigrepository"
	redisrepository "events-stocks/repositories/redisrepository"
	sqsrepository "events-stocks/repositories/sqsrepository"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	momentsService "events-stocks/services/moments"
	eventsService "events-stocks/services/events"
	"events-stocks/services/users"
	"events-stocks/utils"

	"golang.org/x/sync/errgroup"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var (
	publicTokenRepo ports.AccessTokenRepository
	publicResSvc    *resourcesService.ResourceService
)

func InitPublicMomentsController(
	tokenRepo ports.AccessTokenRepository,
	resSvc *resourcesService.ResourceService,
) {
	publicTokenRepo = tokenRepo
	publicResSvc = resSvc
}

func getEventByIdentifier(identifier string) (*models.Event, error) {
	var event models.Event
	err := configuration.DB.Where("identifier = ?", identifier).First(&event).Error
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// detectIsVideo returns true when the file is a video based on MIME type or extension.
func detectIsVideo(contentType, filename string) bool {
	if strings.HasPrefix(contentType, "video/") {
		return true
	}
	videoExts := map[string]bool{
		".mp4": true, ".webm": true, ".mov": true,
		".avi": true, ".mkv": true, ".m4v": true,
	}
	return videoExts[strings.ToLower(filepath.Ext(filename))]
}

// publishMediaJob queues optimization for images AND videos via SQS.
// Runs synchronously so errors are captured and logged; the moment is already
// saved so a failed publish can be retried via PUT /moments/:id/requeue.
// Returns true if the job was enqueued. When SQS is not configured (local/staging
// without queues), returns false so the caller can set processing_status="" to
// make the moment visible immediately.
func publishMediaJob(moment *models.Moment, rawKey, bucket, contentType string) bool {
	if moment.EventID == nil {
		return false
	}
	enqueued, err := sqsrepository.PublishMediaJob(sqsrepository.MediaProcessMessage{
		MomentID:    moment.ID.String(),
		EventID:     moment.EventID.String(),
		RawS3Key:    rawKey,
		Bucket:      bucket,
		ContentType: contentType,
		IsVideo:     detectIsVideo(contentType, rawKey),
	})
	if err != nil {
		slog.Error("failed to queue media job", "momentId", moment.ID, "error", err)
		// Don't fail the request — moment is created, job can be manually requeued
		return false
	}
	return enqueued
}

const defaultPageLimit = 20
const maxPageLimit = 50

// GET /api/events/:identifier/moments?page=1&limit=20
// Returns only approved + fully optimized moments (processing_status IN ('','done')).
// Results are cached in Redis/Valkey; cache is busted on approval changes or Lambda completion.
func ListPublicMoments(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	// Respect ShowMomentWall flag — unless a valid admin preview token is present.
	cfg, _ := eventconfigrepository.GetEventConfigByID(event.ID)
	if cfg != nil && !cfg.ShowMomentWall {
		previewToken := c.QueryParam("preview_token")
		if previewToken != "" {
			redisKey := fmt.Sprintf("preview:moments:%s:%s", event.ID.String(), previewToken)
			ctx := c.Request().Context()
			valid, redisErr := redisrepository.ExistKey(ctx, redisKey)
			if redisErr != nil {
				return utils.Error(c, http.StatusServiceUnavailable, "Cache unavailable", redisErr.Error())
			}
			if !valid {
				return utils.Error(c, http.StatusForbidden, "Invalid or expired preview token", "")
			}
			// Token remains valid in Redis until TTL expires (1 hour) so pagination works.
		} else {
			return utils.Success(c, http.StatusOK, "Moments not yet available", map[string]interface{}{
				"items":     []interface{}{},
				"published": false,
				"total":     0,
				"has_more":  false,
			})
		}
	}

	// Parse pagination params
	page := 1
	limit := defaultPageLimit
	if p, err := strconv.Atoi(c.QueryParam("page")); err == nil && p > 0 {
		page = p
	}
	if l, err := strconv.Atoi(c.QueryParam("limit")); err == nil && l > 0 && l <= maxPageLimit {
		limit = l
	}

	// Cached + paginated approved moments
	items, total, err := momentsService.ListApprovedForWall(event.ID, page, limit)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}

	// Presign S3 URLs so the browser streams directly from S3 (no backend proxy).
	// presignMomentURL is a no-op for keys that are already full URLs or empty strings.
	bucket := publicResSvc.Bucket
	for i := range items {
		items[i].ContentURL = presignMomentURL(items[i].ContentURL, bucket)
		items[i].ThumbnailURL = presignMomentURL(items[i].ThumbnailURL, bucket)
	}

	return utils.Success(c, http.StatusOK, "Moments loaded", map[string]interface{}{
		"items":    items,
		"total":    total,
		"page":     page,
		"limit":    limit,
		"has_more": int64(page*limit) < total,
		"published": true,
	})
}

// POST /events/:id/preview-token  — protected (Cognito JWT required)
// Generates a single-use admin preview token for the moments wall.
// Token is stored in Redis with a 1-hour TTL; validated (without deletion) by
// ListPublicMoments so paginated preview requests work for the full hour.
func CreatePreviewToken(c echo.Context) error {
	// Verify caller identity
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	caller, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}

	var event models.Event
	if err := configuration.DB.First(&event, "id = ?", eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	// Non-root users must have access to the event's client
	if !caller.IsRoot && event.ClientID != nil {
		allowed, _ := clientrepository.CheckAccessRecursive(caller.ID, *event.ClientID)
		if !allowed {
			return utils.Error(c, http.StatusForbidden, "Access denied", "")
		}
	}

	token, err := uuid.NewV4()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error generating token", err.Error())
	}

	redisKey := fmt.Sprintf("preview:moments:%s:%s", eventID.String(), token.String())
	ctx := c.Request().Context()
	if err := redisrepository.SaveKey(ctx, redisKey, "1", time.Hour); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Cache unavailable", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Preview token created", map[string]interface{}{
		"token":      token.String(),
		"expires_in": 3600,
	})
}

// POST /api/events/:identifier/moments  — requires personal invitation pretty_token
func CreatePublicMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	prettyToken := c.FormValue("pretty_token")
	if prettyToken == "" {
		return utils.Error(c, http.StatusUnauthorized, "Missing invitation token", "")
	}

	token, err := publicTokenRepo.GetByPrettyToken(prettyToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusUnauthorized, "Invalid invitation token", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error validating token", err.Error())
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	cfg, _ := eventconfigrepository.GetEventConfigByID(event.ID)
	if cfg != nil && !cfg.AllowUploads {
		return utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}

	// Verify token belongs to this event
	var inv models.Invitation
	if err := configuration.DB.Where("id = ? AND event_id = ?", token.InvitationID, event.ID).First(&inv).Error; err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Token does not belong to this event", "")
	}

	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Missing file", err.Error())
	}
	defer file.Close()

	rawKey, contentType, err := publicResSvc.UploadRawToMomentsFolder(file, header, event.ID.String())
	if err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, "Error uploading file", err.Error())
	}

	description := c.FormValue("description")
	eventID := event.ID
	invID := token.InvitationID
	isApproved := false
	if cfg != nil && cfg.AutoApproveUploads {
		isApproved = true
	}
	moment := models.Moment{
		EventID:          &eventID,
		InvitationID:     &invID,
		ContentURL:       rawKey,
		ContentType:      contentType,
		Description:      description,
		IsApproved:       isApproved,
		ProcessingStatus: "pending",
	}

	if err := momentsService.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}

	// Queue optimization job; if SQS is not configured, mark as ready immediately
	if !publishMediaJob(&moment, rawKey, publicResSvc.Bucket, contentType) {
		moment.ProcessingStatus = ""
		_ = momentsService.UpdateMoment(&moment)
	}

	go eventsService.IncrementAnalytics(eventID, "moment_uploads")

	return utils.Success(c, http.StatusCreated, "Moment submitted for review", moment)
}

// POST /api/events/:identifier/moments/shared/upload-url
// Step 1 of direct-upload flow: returns a short-lived presigned S3 PUT URL plus the
// S3 key the client must use. The browser can then PUT the file bytes directly to S3
// without routing through the backend. After the PUT succeeds, the client calls the
// /confirm endpoint to persist the moment and queue Lambda processing.
func RequestSharedUploadURL(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	cfg, err := eventconfigrepository.GetEventConfigByID(event.ID)
	if err != nil || cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	if !cfg.ShareUploadsEnabled {
		return utils.Error(c, http.StatusForbidden, "Shared uploads are not enabled for this event", "")
	}
	if !cfg.AllowUploads {
		return utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}

	var body struct {
		ContentType string `json:"content_type"`
		Filename    string `json:"filename"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if body.ContentType == "" {
		return utils.Error(c, http.StatusBadRequest, "content_type is required", "")
	}

	// Validate content type
	isImg := strings.HasPrefix(body.ContentType, "image/")
	isVid := strings.HasPrefix(body.ContentType, "video/")
	if !isImg && !isVid {
		return utils.Error(c, http.StatusBadRequest, fmt.Sprintf("unsupported file type: %s", body.ContentType), "")
	}

	// Build the final S3 key directly — no staging needed for single-PUT uploads.
	// The browser uploads straight to the event-scoped raw folder.
	ext := ""
	if idx := strings.LastIndex(body.Filename, "."); idx != -1 {
		ext = strings.ToLower(body.Filename[idx:])
	}
	u, _ := uuid.NewV4()
	filename := u.String() + ext
	folder := "moments/" + event.ID.String() + "/raw"
	s3Key := fmt.Sprintf("%s/%s", folder, filename)

	uploadURL, err := bucketrepository.GetPresignedUploadURL(filename, folder, body.ContentType, publicResSvc.Bucket, constants.DefaultCloudProvider, 15)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error generating upload URL", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Upload URL ready", map[string]string{
		"upload_url": uploadURL,
		"s3_key":     s3Key,
	})
}

// POST /api/events/:identifier/moments/shared/confirm
// Step 2 of direct-upload flow: called after the browser has PUT the file to S3.
// Creates the Moment record in the DB and queues the Lambda processing job.
func ConfirmSharedMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	cfg, err := eventconfigrepository.GetEventConfigByID(event.ID)
	if err != nil || cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	if !cfg.ShareUploadsEnabled {
		return utils.Error(c, http.StatusForbidden, "Shared uploads are not enabled for this event", "")
	}
	if !cfg.AllowUploads {
		return utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}

	var body struct {
		S3Key       string `json:"s3_key"`
		ContentType string `json:"content_type"`
		Description string `json:"description"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if body.S3Key == "" || body.ContentType == "" {
		return utils.Error(c, http.StatusBadRequest, "s3_key and content_type are required", "")
	}

	// Guard: key must match our event-scoped raw path to prevent forged keys
	if !validateMultipartKey(body.S3Key, event.ID.String()) {
		return utils.Error(c, http.StatusBadRequest, "Invalid s3_key", "")
	}

	rawFolder := "moments/" + event.ID.String() + "/raw"
	filename := body.S3Key[len(rawFolder)+1:]
	_, contentType, err := bucketrepository.GetFileMeta(filename, rawFolder, publicResSvc.Bucket, constants.DefaultCloudProvider)
	if err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, "file not found in storage — upload may have failed", "")
	}

	rawKey := body.S3Key

	eventID := event.ID
	moment := models.Moment{
		EventID:          &eventID,
		ContentURL:       rawKey,
		ContentType:      contentType,
		Description:      body.Description,
		IsApproved:       cfg.AutoApproveUploads,
		ProcessingStatus: "pending",
	}

	if err := momentsService.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}

	if !publishMediaJob(&moment, rawKey, publicResSvc.Bucket, contentType) {
		moment.ProcessingStatus = ""
		_ = momentsService.UpdateMoment(&moment)
	}

	go eventsService.IncrementAnalytics(eventID, "moment_uploads")

	return utils.Success(c, http.StatusCreated, "Moment submitted for review", moment)
}

// POST /api/events/:identifier/moments/upload-url  — personal invitation upload
// Step 1: returns a presigned PUT URL for direct browser → S3 upload.
func RequestPersonalUploadURL(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	var body struct {
		PrettyToken string `json:"pretty_token"`
		ContentType string `json:"content_type"`
		Filename    string `json:"filename"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if body.PrettyToken == "" {
		return utils.Error(c, http.StatusUnauthorized, "Missing invitation token", "")
	}
	if body.ContentType == "" {
		return utils.Error(c, http.StatusBadRequest, "content_type is required", "")
	}

	token, err := publicTokenRepo.GetByPrettyToken(body.PrettyToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusUnauthorized, "Invalid invitation token", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error validating token", err.Error())
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	cfg, _ := eventconfigrepository.GetEventConfigByID(event.ID)
	if cfg != nil && !cfg.AllowUploads {
		return utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}

	if err := configuration.DB.Where("id = ? AND event_id = ?", token.InvitationID, event.ID).First(&models.Invitation{}).Error; err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Token does not belong to this event", "")
	}

	isImg := strings.HasPrefix(body.ContentType, "image/")
	isVid := strings.HasPrefix(body.ContentType, "video/")
	if !isImg && !isVid {
		return utils.Error(c, http.StatusBadRequest, fmt.Sprintf("unsupported file type: %s", body.ContentType), "")
	}

	ext := ""
	if idx := strings.LastIndex(body.Filename, "."); idx != -1 {
		ext = strings.ToLower(body.Filename[idx:])
	}
	u, _ := uuid.NewV4()
	filename := u.String() + ext
	folder := "moments/" + event.ID.String() + "/raw"
	s3Key := fmt.Sprintf("%s/%s", folder, filename)

	uploadURL, err := bucketrepository.GetPresignedUploadURL(filename, folder, body.ContentType, publicResSvc.Bucket, constants.DefaultCloudProvider, 15)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error generating upload URL", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Upload URL ready", map[string]string{
		"upload_url": uploadURL,
		"s3_key":     s3Key,
	})
}

// POST /api/events/:identifier/moments/confirm  — personal invitation upload
// Step 2: validates the file in S3, moves it to the event folder, creates the
// Moment record, and queues Lambda.
func ConfirmPersonalMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	var body struct {
		PrettyToken string `json:"pretty_token"`
		S3Key       string `json:"s3_key"`
		Description string `json:"description"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if body.PrettyToken == "" {
		return utils.Error(c, http.StatusUnauthorized, "Missing invitation token", "")
	}
	if body.S3Key == "" {
		return utils.Error(c, http.StatusBadRequest, "s3_key is required", "")
	}

	token, err := publicTokenRepo.GetByPrettyToken(body.PrettyToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusUnauthorized, "Invalid invitation token", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error validating token", err.Error())
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	cfg, _ := eventconfigrepository.GetEventConfigByID(event.ID)
	if cfg != nil && !cfg.AllowUploads {
		return utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}

	var inv models.Invitation
	if err := configuration.DB.Where("id = ? AND event_id = ?", token.InvitationID, event.ID).First(&inv).Error; err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Token does not belong to this event", "")
	}

	// Guard: key must match our event-scoped raw path to prevent forged keys
	if !validateMultipartKey(body.S3Key, event.ID.String()) {
		return utils.Error(c, http.StatusBadRequest, "Invalid s3_key", "")
	}

	rawFolder := "moments/" + event.ID.String() + "/raw"
	filename := body.S3Key[len(rawFolder)+1:]
	_, contentType, err := bucketrepository.GetFileMeta(filename, rawFolder, publicResSvc.Bucket, constants.DefaultCloudProvider)
	if err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, "file not found in storage — upload may have failed", "")
	}

	rawKey := body.S3Key

	eventID := event.ID
	invID := token.InvitationID
	isApproved := false
	if cfg != nil && cfg.AutoApproveUploads {
		isApproved = true
	}
	moment := models.Moment{
		EventID:          &eventID,
		InvitationID:     &invID,
		ContentURL:       rawKey,
		ContentType:      contentType,
		Description:      body.Description,
		IsApproved:       isApproved,
		ProcessingStatus: "pending",
	}

	if err := momentsService.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}

	if !publishMediaJob(&moment, rawKey, publicResSvc.Bucket, contentType) {
		moment.ProcessingStatus = ""
		_ = momentsService.UpdateMoment(&moment)
	}

	go eventsService.IncrementAnalytics(eventID, "moment_uploads")

	return utils.Success(c, http.StatusCreated, "Moment submitted for review", moment)
}

// validateMultipartKey checks that the s3_key was generated by our /start endpoint
// for the given event. Rejects forged keys that would let a user complete a multipart
// upload to an arbitrary path.
func validateMultipartKey(s3Key, eventID string) bool {
	prefix := fmt.Sprintf("moments/%s/raw/", eventID)
	if !strings.HasPrefix(s3Key, prefix) {
		return false
	}
	// Remaining part must be a non-empty filename with no path separators
	filename := s3Key[len(prefix):]
	return len(filename) > 0 && !strings.Contains(filename, "/")
}

// POST /api/events/:identifier/moments/shared/multipart/start
//
// Step 1 of multipart upload: creates the S3 multipart upload and returns
// presigned UploadPart URLs for every part. The browser uploads parts directly
// to S3 using these URLs, then calls /complete with the collected ETags.
//
// The file lands at moments/{eventID}/raw/{uuid}.ext — no staging promotion needed.
func RequestMultipartUploadStart(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	cfg, err := eventconfigrepository.GetEventConfigByID(event.ID)
	if err != nil || cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	if !cfg.ShareUploadsEnabled {
		return utils.Error(c, http.StatusForbidden, "Shared uploads are not enabled for this event", "")
	}
	if !cfg.AllowUploads {
		return utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}

	var body struct {
		ContentType string `json:"content_type"`
		Filename    string `json:"filename"`
		FileSize    int64  `json:"file_size"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if body.ContentType == "" || body.Filename == "" || body.FileSize <= 0 {
		return utils.Error(c, http.StatusBadRequest, "content_type, filename, and file_size are required", "")
	}
	if !strings.HasPrefix(body.ContentType, "video/") {
		return utils.Error(c, http.StatusBadRequest, "multipart upload is only supported for video files", "")
	}

	const maxVideoBytes = int64(200 * 1024 * 1024)
	if body.FileSize > maxVideoBytes {
		return utils.Error(c, http.StatusBadRequest, "file size exceeds 200 MB limit", "")
	}

	// Build the final S3 key directly (no staging — multipart is initiated by the backend)
	ext := ""
	if idx := strings.LastIndex(body.Filename, "."); idx != -1 {
		ext = strings.ToLower(body.Filename[idx:])
	}
	u, _ := uuid.NewV4()
	filename := u.String() + ext
	s3Key := fmt.Sprintf("moments/%s/raw/%s", event.ID.String(), filename)

	uploadID, err := bucketrepository.CreateMultipartUpload(s3Key, publicResSvc.Bucket, body.ContentType, constants.DefaultCloudProvider)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error initiating multipart upload", err.Error())
	}

	// Sign all part URLs at once. Part size = 8 MB; last part may be smaller.
	const partSize = int64(8 * 1024 * 1024)
	totalParts := int((body.FileSize + partSize - 1) / partSize)
	if totalParts < 1 {
		totalParts = 1
	}

	type partURL struct {
		PartNumber int    `json:"part_number"`
		URL        string `json:"url"`
	}
	partURLs := make([]partURL, totalParts)

	eg := new(errgroup.Group)
	for i := 0; i < totalParts; i++ {
		i := i // capture loop variable
		eg.Go(func() error {
			u, err := bucketrepository.GetPresignedPartURL(s3Key, publicResSvc.Bucket, uploadID, i+1, 60, constants.DefaultCloudProvider)
			if err != nil {
				return err
			}
			partURLs[i] = partURL{PartNumber: i + 1, URL: u}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		// Abort the initiated upload before returning
		_ = bucketrepository.AbortMultipartUpload(s3Key, publicResSvc.Bucket, uploadID, constants.DefaultCloudProvider)
		return utils.Error(c, http.StatusInternalServerError, "Error signing part URLs", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Multipart upload started", map[string]interface{}{
		"upload_id": uploadID,
		"s3_key":    s3Key,
		"part_urls": partURLs,
	})
}

// POST /api/events/:identifier/moments/shared/multipart/complete
//
// Step 2 of multipart upload: assembles the S3 object, creates the Moment in DB,
// and queues Lambda processing — exactly like ConfirmSharedMoment but without
// the staging promotion (HeadObject + CopyObject + DeleteObject).
func CompleteMultipartMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	cfg, err := eventconfigrepository.GetEventConfigByID(event.ID)
	if err != nil || cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	if !cfg.ShareUploadsEnabled {
		return utils.Error(c, http.StatusForbidden, "Shared uploads are not enabled for this event", "")
	}
	if !cfg.AllowUploads {
		return utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}

	var body struct {
		UploadID    string `json:"upload_id"`
		S3Key       string `json:"s3_key"`
		Description string `json:"description"`
		Parts       []struct {
			PartNumber int    `json:"part_number"`
			ETag       string `json:"etag"`
		} `json:"parts"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if body.UploadID == "" || body.S3Key == "" {
		return utils.Error(c, http.StatusBadRequest, "upload_id and s3_key are required", "")
	}
	if len(body.Parts) == 0 {
		return utils.Error(c, http.StatusBadRequest, "parts must not be empty", "")
	}

	// Security: reject keys that don't belong to this event
	if !validateMultipartKey(body.S3Key, event.ID.String()) {
		return utils.Error(c, http.StatusBadRequest, "Invalid s3_key for this event", "")
	}

	// Convert to bucket repo type
	parts := make([]bucketrepository.CompletedPart, len(body.Parts))
	for i, p := range body.Parts {
		parts[i] = bucketrepository.CompletedPart{PartNumber: p.PartNumber, ETag: p.ETag}
	}

	if err := bucketrepository.CompleteMultipartUpload(body.S3Key, publicResSvc.Bucket, body.UploadID, parts, constants.DefaultCloudProvider); err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, "Error completing multipart upload", err.Error())
	}

	// Read the actual content type from S3 — don't trust the client-supplied value.
	// The content type was set in S3 when CreateMultipartUpload was called at /start.
	lastSlash := strings.LastIndex(body.S3Key, "/")
	s3Filename := body.S3Key[lastSlash+1:]
	s3Folder := body.S3Key[:lastSlash]
	_, actualContentType, err := bucketrepository.GetFileMeta(s3Filename, s3Folder, publicResSvc.Bucket, constants.DefaultCloudProvider)
	if err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, "Error reading uploaded file metadata", err.Error())
	}

	eventID := event.ID
	moment := models.Moment{
		EventID:          &eventID,
		ContentURL:       body.S3Key,
		ContentType:      actualContentType,
		Description:      body.Description,
		IsApproved:       cfg.AutoApproveUploads,
		ProcessingStatus: "pending",
	}

	if err := momentsService.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}

	if !publishMediaJob(&moment, body.S3Key, publicResSvc.Bucket, actualContentType) {
		moment.ProcessingStatus = ""
		_ = momentsService.UpdateMoment(&moment)
	}

	go eventsService.IncrementAnalytics(eventID, "moment_uploads")

	return utils.Success(c, http.StatusCreated, "Moment submitted for review", moment)
}

// POST /api/events/:identifier/moments/shared/multipart/abort
//
// Called by the browser when an upload fails or the user cancels.
// Always returns 200 — abort failures are non-fatal (S3 lifecycle cleans up).
func AbortMultipartMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	// Minimal event lookup — just to validate the identifier exists
	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	var body struct {
		UploadID string `json:"upload_id"`
		S3Key    string `json:"s3_key"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if body.UploadID == "" || body.S3Key == "" {
		return utils.Error(c, http.StatusBadRequest, "upload_id and s3_key are required", "")
	}

	// Security: reject keys that don't belong to this event
	if !validateMultipartKey(body.S3Key, event.ID.String()) {
		return utils.Error(c, http.StatusBadRequest, "Invalid s3_key for this event", "")
	}

	// Best-effort abort — log but don't surface errors
	if err := bucketrepository.AbortMultipartUpload(body.S3Key, publicResSvc.Bucket, body.UploadID, constants.DefaultCloudProvider); err != nil {
		slog.Warn("AbortMultipartUpload failed", "key", body.S3Key, "err", err)
	}

	return utils.Success(c, http.StatusOK, "Aborted", nil)
}

// POST /api/events/:identifier/moments/shared — shared QR upload (no personal token)
// Requires EventConfig.ShareUploadsEnabled = true.
func CreateSharedMoment(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event identifier", "")
	}

	event, err := getEventByIdentifier(identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", "")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error loading event", err.Error())
	}

	cfg, err := eventconfigrepository.GetEventConfigByID(event.ID)
	if err != nil || cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	if !cfg.ShareUploadsEnabled {
		return utils.Error(c, http.StatusForbidden, "Shared uploads are not enabled for this event", "")
	}
	if !cfg.AllowUploads {
		return utils.Error(c, http.StatusForbidden, "Uploads are disabled for this event", "")
	}

	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Missing file", err.Error())
	}
	defer file.Close()

	rawKey, contentType, err := publicResSvc.UploadRawToMomentsFolder(file, header, event.ID.String())
	if err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, "Error uploading file", err.Error())
	}

	description := c.FormValue("description")
	eventID := event.ID
	isApproved := cfg.AutoApproveUploads
	moment := models.Moment{
		EventID:          &eventID,
		ContentURL:       rawKey,
		ContentType:      contentType,
		Description:      description,
		IsApproved:       isApproved,
		ProcessingStatus: "pending",
	}

	if err := momentsService.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error saving moment", err.Error())
	}

	// Queue optimization job; if SQS is not configured, mark as ready immediately
	if !publishMediaJob(&moment, rawKey, publicResSvc.Bucket, contentType) {
		moment.ProcessingStatus = ""
		_ = momentsService.UpdateMoment(&moment)
	}

	go eventsService.IncrementAnalytics(eventID, "moment_uploads")

	return utils.Success(c, http.StatusCreated, "Moment submitted for review", moment)
}
