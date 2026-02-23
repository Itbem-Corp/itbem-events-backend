package moments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/eventconfigrepository"
	sqsrepository "events-stocks/repositories/sqsrepository"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	momentsService "events-stocks/services/moments"
	eventsService "events-stocks/services/events"
	"events-stocks/utils"

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

// uploadLimitKey returns the Redis key for tracking per-IP uploads for an event.
func uploadLimitKey(eventID, ip string) string {
	return fmt.Sprintf("moments:upload_count:%s:%s", eventID, ip)
}

const uploadWindowDays = 30

// defaultMaxUploadsPerIP is the global fallback when EventConfig.MaxUploadsPerGuest is 0.
// 30 = allows up to 3 batches of 10 files from the shared upload page.
const defaultMaxUploadsPerIP = 30

// checkAndIncrementUploadLimit checks the per-IP upload counter.
// maxUploads is the per-event configured limit; pass 0 to use the global default (3).
// Returns (limitReached, currentCount, error).
func checkAndIncrementUploadLimit(ctx context.Context, eventID, ip string, maxUploads int) (bool, int64, error) {
	if maxUploads <= 0 {
		maxUploads = defaultMaxUploadsPerIP
	}
	key := uploadLimitKey(eventID, ip)
	count, err := configuration.RedisClient.Incr(ctx, key).Result()
	if err != nil {
		// Redis unavailable — allow the upload rather than blocking
		return false, 0, nil
	}
	// Set TTL on first increment
	if count == 1 {
		configuration.RedisClient.Expire(ctx, key, uploadWindowDays*24*time.Hour)
	}
	return count > int64(maxUploads), count, nil
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

	// Respect ShowMomentWall flag
	cfg, _ := eventconfigrepository.GetEventConfigByID(event.ID)
	if cfg != nil && !cfg.ShowMomentWall {
		return utils.Success(c, http.StatusOK, "Moments not yet available", map[string]interface{}{
			"items":     []interface{}{},
			"published": false,
			"total":     0,
			"has_more":  false,
		})
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

	// Check if the requesting IP has already uploaded (for UI state)
	ip := c.RealIP()
	ctx := c.Request().Context()
	countStr, _ := configuration.RedisClient.Get(ctx, uploadLimitKey(event.ID.String(), ip)).Result()
	var uploadedCount int64
	fmt.Sscanf(countStr, "%d", &uploadedCount)

	// Cached + paginated approved moments
	items, total, err := momentsService.ListApprovedForWall(event.ID, page, limit)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}

	maxUploads := int64(defaultMaxUploadsPerIP)
	if cfg != nil && cfg.MaxUploadsPerGuest > 0 {
		maxUploads = int64(cfg.MaxUploadsPerGuest)
	}
	uploadsRemaining := maxUploads - uploadedCount
	if uploadsRemaining < 0 {
		uploadsRemaining = 0
	}

	return utils.Success(c, http.StatusOK, "Moments loaded", map[string]interface{}{
		"items":             items,
		"total":             total,
		"page":              page,
		"limit":             limit,
		"has_more":          int64(page*limit) < total,
		"published":         true,
		"uploads_remaining": uploadsRemaining,
		"uploads_used":      uploadedCount,
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

	// Per-IP upload limit — respect per-event config, fall back to global default
	maxUploads := 0
	if cfg != nil {
		maxUploads = cfg.MaxUploadsPerGuest
	}
	ip := c.RealIP()
	ctx := c.Request().Context()
	limitReached, _, _ := checkAndIncrementUploadLimit(ctx, event.ID.String(), ip, maxUploads)
	if maxUploads <= 0 {
		maxUploads = defaultMaxUploadsPerIP
	}
	if limitReached {
		return c.JSON(http.StatusTooManyRequests, map[string]interface{}{
			"message":          fmt.Sprintf("Gracias por compartir tus momentos en %s. ¡Ya registramos tus %d contribuciones!", event.Name, maxUploads),
			"already_uploaded": true,
			"event_name":       event.Name,
		})
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

	// Per-IP upload limit — respect per-event config, fall back to global default
	maxUploads := cfg.MaxUploadsPerGuest
	ip := c.RealIP()
	ctx := c.Request().Context()
	limitReached, _, _ := checkAndIncrementUploadLimit(ctx, event.ID.String(), ip, maxUploads)
	if maxUploads <= 0 {
		maxUploads = defaultMaxUploadsPerIP
	}
	if limitReached {
		return c.JSON(http.StatusTooManyRequests, map[string]interface{}{
			"message":          fmt.Sprintf("Gracias por compartir tus momentos en %s. ¡Ya registramos tus %d contribuciones!", event.Name, maxUploads),
			"already_uploaded": true,
			"event_name":       event.Name,
		})
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
