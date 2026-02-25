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
	"events-stocks/repositories/eventconfigrepository"
	redisrepository "events-stocks/repositories/redisrepository"
	sqsrepository "events-stocks/repositories/sqsrepository"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	momentsService "events-stocks/services/moments"
	eventsService "events-stocks/services/events"
	"events-stocks/utils"

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
			valid, _ := redisrepository.ExistKey(ctx, redisKey)
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

	// Build a UUID-named file under the shared staging prefix.
	// Using a fixed staging prefix (not event-scoped) lets us put a simple S3
	// lifecycle rule on "moments/uploads/tmp/" to auto-expire orphaned files
	// (uploads where the browser never called /confirm) after 1 day.
	ext := ""
	if idx := strings.LastIndex(body.Filename, "."); idx != -1 {
		ext = strings.ToLower(body.Filename[idx:])
	}
	u, _ := uuid.NewV4()
	filename := u.String() + ext
	folder := "moments/uploads/tmp"
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

	// Guard: key must come from our own staging prefix to prevent forged paths
	const tmpPrefix = "moments/uploads/tmp/"
	if !strings.HasPrefix(body.S3Key, tmpPrefix) {
		return utils.Error(c, http.StatusBadRequest, "Invalid s3_key", "")
	}

	filename := body.S3Key[len(tmpPrefix):]
	rawKey, contentType, err := promoteFromStaging(filename, event.ID.String(), publicResSvc.Bucket)
	if err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, err.Error(), "")
	}

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

// promoteFromStaging validates a staged file and moves it to the event-scoped
// raw folder. Returns the final S3 key and the content-type read from S3.
// Returns an error if the file is missing, oversized, or cannot be moved.
func promoteFromStaging(filename, eventID, bucket string) (rawKey, contentType string, err error) {
	// FIX #3 — verify file exists; FIX #2 — enforce size limits
	// FIX #4 — read content-type from S3, not from client
	fileSize, s3ContentType, err := bucketrepository.GetFileMeta(filename, "moments/uploads/tmp", bucket, constants.DefaultCloudProvider)
	if err != nil {
		return "", "", fmt.Errorf("file not found in storage — upload may have failed")
	}

	isVid := strings.HasPrefix(s3ContentType, "video/")
	maxBytes := int64(25 * 1024 * 1024)
	if isVid {
		maxBytes = int64(200 * 1024 * 1024)
	}
	if fileSize > maxBytes {
		limitMB := 25
		if isVid {
			limitMB = 200
		}
		_ = bucketrepository.DeleteFile(filename, "moments/uploads/tmp", bucket, constants.DefaultCloudProvider)
		return "", "", fmt.Errorf("file exceeds %d MB limit", limitMB)
	}

	rawFolder := fmt.Sprintf("moments/%s/raw", eventID)
	if err := bucketrepository.CopyFile(filename, "moments/uploads/tmp", filename, rawFolder, bucket, constants.DefaultCloudProvider); err != nil {
		return "", "", fmt.Errorf("error moving file to storage: %w", err)
	}
	_ = bucketrepository.DeleteFile(filename, "moments/uploads/tmp", bucket, constants.DefaultCloudProvider)

	return fmt.Sprintf("%s/%s", rawFolder, filename), s3ContentType, nil
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
	folder := "moments/uploads/tmp"
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

	const tmpPrefix = "moments/uploads/tmp/"
	if !strings.HasPrefix(body.S3Key, tmpPrefix) {
		return utils.Error(c, http.StatusBadRequest, "Invalid s3_key", "")
	}

	filename := body.S3Key[len(tmpPrefix):]
	rawKey, contentType, err := promoteFromStaging(filename, event.ID.String(), publicResSvc.Bucket)
	if err != nil {
		return utils.Error(c, http.StatusUnprocessableEntity, err.Error(), "")
	}

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
