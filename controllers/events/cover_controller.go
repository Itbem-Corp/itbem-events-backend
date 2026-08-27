package events

import (
	"crypto/subtle"
	"errors"
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	sqsrepository "events-stocks/repositories/sqsrepository"
	eventsService "events-stocks/services/events"
	Resources "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
)

var coverResourceSvc *Resources.ResourceService

func InitCoverController(svc *Resources.ResourceService) { coverResourceSvc = svc }

// UploadEventCover stores a bounded source immediately and delegates image
// decode/resize work to the image queue. The previous public cover remains
// authoritative until a generation-matched terminal callback succeeds.
func UploadEventCover(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	if _, existingEvent, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	} else if existingEvent != nil {
		return uploadEventCoverForService(c, eventID, existingEvent, coverResourceSvc.WithBucket(existingEvent.MediaBucket).WithOrganization(existingEvent.ClientID))
	}
	return utils.Error(c, http.StatusNotFound, "Event not found", "")
}

func uploadEventCoverForService(c echo.Context, eventID uuid.UUID, existingEvent *models.Event, svc *Resources.ResourceService) error {
	header, err := c.FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "File is required", err.Error())
	}
	file, err := header.Open()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error opening file", err.Error())
	}
	defer file.Close()

	rawPath, contentType, _, err := svc.UploadRawEventCover(file, header, eventID.String())
	if err != nil {
		status, detail := Resources.UploadErrorResponse(err)
		return utils.Error(c, status, "Failed to upload cover", detail)
	}
	jobID := uuid.Must(uuid.NewV4()).String()
	message := dtos.NewMediaProcessMessage(eventID.String(), eventID.String(), rawPath, svc.Bucket, contentType, false)
	message.TargetType, message.JobID = dtos.MediaTargetEventCover, jobID
	event, supersededPending, durable, err := eventSvc.BeginCoverProcessingWithOutbox(eventID, rawPath, message)
	if err != nil {
		_ = svc.DeleteObjectByPath(rawPath)
		if errors.Is(err, eventsService.ErrEventNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Failed to start cover processing", err.Error())
	}
	enqueued, publishErr := durable, error(nil)
	if !durable {
		message.Generation = event.CoverProcessingGeneration
		enqueued, publishErr = sqsrepository.PublishMediaJob(message)
	}
	if publishErr != nil {
		_, _, _, _ = eventSvc.ApplyCoverProcessingCallback(eventID, dtos.MediaProcessingCallback{JobID: jobID, Generation: event.CoverProcessingGeneration, ProcessingStatus: "failed", ErrorMessage: "processing queue unavailable"})
		return utils.Error(c, http.StatusServiceUnavailable, "Cover upload is safe but processing could not start", publishErr.Error())
	}
	if !enqueued {
		return uploadCoverSynchronously(c, file, header, eventID, rawPath, supersededPending, svc)
	}
	cleanupSupersededPending(supersededPending, event.CoverImageURL, svc)
	pendingViewURL, pendingExpiresAt := coverViewURLWithExpiry(rawPath, existingEvent.MediaBucket)
	currentViewURL, currentExpiresAt := coverViewURLWithExpiry(event.CoverImageURL, existingEvent.MediaBucket)
	return utils.Success(c, http.StatusAccepted, "Cover image accepted for processing", dtos.EventCoverResponse{
		CoverImageURL: event.CoverImageURL, CoverViewURL: currentViewURL, CoverViewURLExpiresAt: currentExpiresAt,
		ViewURL: currentViewURL, ViewURLExpiresAt: currentExpiresAt, PendingURL: rawPath,
		PendingViewURL: pendingViewURL, PendingViewURLExpiresAt: pendingExpiresAt, ProcessingStatus: "pending",
		ProcessingJobID: jobID, ProcessingGeneration: event.CoverProcessingGeneration,
	})
}

func uploadCoverSynchronously(c echo.Context, file multipart.File, header *multipart.FileHeader, eventID uuid.UUID, rawPath, supersededPending string, svc *Resources.ResourceService) error {
	if _, err := file.Seek(0, 0); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Cover processing is not configured", err.Error())
	}
	path, variants, err := svc.UploadEventCover(file, header)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Cover processing is not configured", err.Error())
	}
	oldPath, oldVariants, err := eventSvc.ReplaceCoverImageWithVariants(eventID, path, variants)
	if err != nil {
		cleanupCoverObjects(path, variants, svc)
		return utils.Error(c, http.StatusInternalServerError, "Failed to update event", err.Error())
	}
	cleanupCoverObjects(oldPath, oldVariants, svc)
	_ = svc.DeleteObjectByPath(rawPath)
	cleanupSupersededPending(supersededPending, oldPath, svc)
	viewURL, expiresAt := coverViewURLWithExpiry(path, svc.Bucket)
	return utils.Success(c, http.StatusOK, "Cover image uploaded", dtos.EventCoverResponse{CoverImageURL: path, CoverViewURL: viewURL, CoverViewURLExpiresAt: expiresAt, ViewURL: viewURL, ViewURLExpiresAt: expiresAt, Variants: publicCoverVariants(variants, svc.Bucket), ProcessingStatus: "done"})
}

// BackfillEventCovers queues legacy covers without responsive variants. It is
// root-only, bounded, and idempotent: terminal done rows are never selected.
func BackfillEventCovers(c echo.Context) error {
	if _, err := authz.RequireRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	var body struct {
		Limit int `json:"limit"`
	}
	_ = c.Bind(&body)
	if body.Limit <= 0 {
		body.Limit = 25
	}
	if body.Limit > 100 {
		body.Limit = 100
	}
	var candidates []models.Event
	if err := configuration.DB.Where("cover_image_url <> '' AND jsonb_array_length(COALESCE(cover_variants, '[]'::jsonb)) = 0 AND COALESCE(cover_processing_status, '') NOT IN ('pending','processing','done')").Order("updated_at ASC").Limit(body.Limit).Find(&candidates).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to load cover backfill candidates", err.Error())
	}
	queued, failed := 0, 0
	for _, candidate := range candidates {
		svc := coverResourceSvc.WithBucket(candidate.MediaBucket)
		jobID := uuid.Must(uuid.NewV4()).String()
		message := dtos.NewMediaProcessMessage(candidate.ID.String(), candidate.ID.String(), candidate.CoverImageURL, svc.Bucket, "image/webp", false)
		message.TargetType, message.JobID = dtos.MediaTargetEventCover, jobID
		event, _, durable, err := eventSvc.BeginCoverProcessingWithOutbox(candidate.ID, candidate.CoverImageURL, message)
		if err != nil {
			failed++
			continue
		}
		enqueued, publishErr := durable, error(nil)
		if !durable {
			message.Generation = event.CoverProcessingGeneration
			enqueued, publishErr = sqsrepository.PublishMediaJob(message)
		}
		if publishErr != nil || !enqueued {
			failed++
			_, _, _, _ = eventSvc.ApplyCoverProcessingCallback(candidate.ID, dtos.MediaProcessingCallback{JobID: jobID, Generation: event.CoverProcessingGeneration, ProcessingStatus: "failed", ErrorMessage: "backfill queue unavailable"})
			continue
		}
		queued++
	}
	return utils.Success(c, http.StatusAccepted, "Cover backfill batch evaluated", map[string]int{"candidates": len(candidates), "queued": queued, "failed": failed})
}

// UpdateEventCoverContent is the authenticated Lambda callback endpoint.
func UpdateEventCoverContent(c echo.Context) error {
	if !validCoverInternalSecret(c.Request().Header.Get("X-Internal-Secret")) {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	var body struct {
		JobID                string              `json:"job_id"`
		Generation           int64               `json:"generation"`
		ObjectKey            string              `json:"object_key"`
		ProcessingStatus     string              `json:"processing_status"`
		ErrorMessage         string              `json:"error_message"`
		ProcessingDurationMs int64               `json:"processing_duration_ms"`
		OriginalSizeBytes    int64               `json:"original_size_bytes"`
		OptimizedSizeBytes   int64               `json:"optimized_size_bytes"`
		MediaVariants        []dtos.MediaVariant `json:"media_variants"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	event, oldPath, oldVariants, err := eventSvc.ApplyCoverProcessingCallback(eventID, dtos.MediaProcessingCallback{JobID: body.JobID, Generation: body.Generation, ObjectKey: body.ObjectKey, ProcessingStatus: body.ProcessingStatus, ErrorMessage: body.ErrorMessage, ProcessingDurationMs: body.ProcessingDurationMs, OriginalSizeBytes: body.OriginalSizeBytes, OptimizedSizeBytes: body.OptimizedSizeBytes, MediaVariants: body.MediaVariants})
	if err != nil {
		if errors.Is(err, eventsService.ErrStaleCoverProcessing) {
			return utils.Error(c, http.StatusConflict, "Stale cover processing callback", err.Error())
		}
		if errors.Is(err, eventsService.ErrInvalidCoverProcessingTransition) {
			return utils.Error(c, http.StatusBadRequest, "Invalid cover processing callback", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Failed to update cover processing", err.Error())
	}
	if body.ProcessingStatus == "done" {
		cleanupCoverObjects(oldPath, oldVariants, coverResourceSvc.WithBucket(event.MediaBucket))
	}
	return utils.Success(c, http.StatusOK, "Cover processing updated", map[string]bool{"updated": true})
}

func DeleteEventCover(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}
	event, _ := eventSvc.GetEventByID(eventID)
	oldPath, oldVariants, err := eventSvc.ClearCoverImageWithVariants(eventID)
	if err != nil {
		if errors.Is(err, eventsService.ErrEventNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Failed to remove cover", err.Error())
	}
	mediaBucket := ""
	if event != nil {
		mediaBucket = event.MediaBucket
	}
	svc := coverResourceSvc.WithBucket(mediaBucket)
	cleanupCoverObjects(oldPath, oldVariants, svc)
	if event != nil && event.CoverPendingURL != "" {
		_ = svc.DeleteObjectByPath(event.CoverPendingURL)
	}
	return utils.Success(c, http.StatusOK, "Cover image removed", dtos.EventCoverResponse{CoverImageURL: "", ViewURL: ""})
}

func validCoverInternalSecret(provided string) bool {
	if provided == "" {
		return false
	}
	valid := 0
	for _, name := range []string{"INTERNAL_API_SECRET", "INTERNAL_API_SECRET_PREVIOUS"} {
		if expected := os.Getenv(name); expected != "" {
			valid |= subtle.ConstantTimeCompare([]byte(provided), []byte(expected))
		}
	}
	return valid == 1
}

func cleanupCoverObjects(path string, variants models.MediaVariants, scoped ...*Resources.ResourceService) {
	if coverResourceSvc == nil {
		return
	}
	svc := coverResourceSvc
	if len(scoped) > 0 && scoped[0] != nil {
		svc = scoped[0]
	}
	if path != "" {
		if err := svc.DeleteObjectByPath(path); err != nil {
			slog.Warn("event cover cleanup failed", "path", path, "error", err)
		}
	}
	for _, variant := range variants {
		if err := svc.DeleteObjectByPath(variant.ObjectKey); err != nil {
			slog.Warn("event cover variant cleanup failed", "path", variant.ObjectKey, "error", err)
		}
	}
}

func cleanupSupersededPending(path, currentCover string, scoped ...*Resources.ResourceService) {
	if path == "" || path == currentCover || !strings.Contains(path, "/raw/") {
		return
	}
	svc := coverResourceSvc
	if len(scoped) > 0 && scoped[0] != nil {
		svc = scoped[0]
	}
	_ = svc.DeleteObjectByPath(path)
}

func publicCoverVariants(variants models.MediaVariants, buckets ...string) []dtos.PublicMediaVariant {
	result := make([]dtos.PublicMediaVariant, 0, len(variants))
	for _, variant := range variants {
		viewURL, expiresAt := coverViewURLWithExpiry(variant.ObjectKey, buckets...)
		result = append(result, dtos.PublicMediaVariant{URL: variant.ObjectKey, ViewURL: viewURL, ExpiresAt: expiresAt, Width: variant.Width, Format: variant.Format, Bytes: variant.Bytes})
	}
	return result
}
