package moments

import (
	"events-stocks/configuration/constants"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	momentsService "events-stocks/services/moments"
	"events-stocks/repositories/bucketrepository"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var momentSvc *momentsService.MomentService

func InitMomentsController(svc *momentsService.MomentService) {
	momentSvc = svc
}

// GET /moments?event_id=<uuid>  (filters by event when param provided)
// Returns only moments ready for review (processing_status NOT IN ('pending','processing')).
// This ensures admins only see moments that have been fully optimized by Lambda.
func ListMoments(c echo.Context) error {
	if eventIDStr := c.QueryParam("event_id"); eventIDStr != "" {
		eventID, err := uuid.FromString(eventIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid event_id", err.Error())
		}
		// Only return moments that are ready for review (optimized or legacy).
		// Excludes moments still queued/processing by Lambda.
		list, err := momentSvc.ListForDashboard(eventID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
		}
		cfg, ok := c.Get("config").(*models.Config)
		if ok && cfg != nil {
			for i := range list {
				list[i].ContentURL = presignMomentURL(list[i].ContentURL, cfg.AwsBucketName)
				list[i].ThumbnailURL = presignMomentURL(list[i].ThumbnailURL, cfg.AwsBucketName)
			}
		}
		return utils.Success(c, http.StatusOK, "Moments loaded", list)
	}
	list, err := momentSvc.ListMoments()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments loaded", list)
}

// GET /moments/:id
func GetMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	moment, err := momentSvc.GetMomentByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Moment not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment loaded", moment)
}

// POST /moments
func CreateMoment(c echo.Context) error {
	var moment models.Moment
	if err := c.Bind(&moment); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&moment); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}

	if err := momentSvc.CreateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating moment", err.Error())
	}

	if moment.EventID != nil {
		go eventsService.IncrementAnalytics(*moment.EventID, "moment_uploads")
	}

	return utils.Success(c, http.StatusCreated, "Moment created", moment)
}

// PUT /moments/:id
func UpdateMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var moment models.Moment
	if err := c.Bind(&moment); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&moment); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}

	moment.ID = id
	if err := momentSvc.UpdateMoment(&moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment updated", moment)
}

// PUT /moments/:id/content  — internal callback for Lambda after video transcoding
// Requires header: X-Internal-Secret matching INTERNAL_API_SECRET env var.
func UpdateMomentContent(c echo.Context) error {
	secret := os.Getenv("INTERNAL_API_SECRET")
	if secret == "" || c.Request().Header.Get("X-Internal-Secret") != secret {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var body struct {
		ContentURL           string `json:"content_url"`
		ProcessingStatus     string `json:"processing_status"`
		ThumbnailURL         string `json:"thumbnail_url"`
		EventID              *uuid.UUID `json:"event_id"`
		ProcessingDurationMs int64  `json:"processing_duration_ms"`
		OriginalSizeBytes    int64  `json:"original_size_bytes"`
		OptimizedSizeBytes   int64  `json:"optimized_size_bytes"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}

	if err := momentSvc.UpdateMomentContent(id, body.ContentURL, body.ProcessingStatus, body.ThumbnailURL, body.ProcessingDurationMs, body.OriginalSizeBytes, body.OptimizedSizeBytes, body.EventID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment content updated", nil)
}

// GET /moments/:id/download — proxies the S3 file through the backend so the
// browser can fetch it without S3 CORS restrictions (used for ZIP generation).
func DownloadMomentFile(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	moment, err := momentSvc.GetMomentByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Moment not found", err.Error())
	}

	key := moment.ContentURL
	if key == "" || strings.HasPrefix(key, "http") {
		return utils.Error(c, http.StatusUnprocessableEntity, "Moment has no downloadable file", "")
	}

	cfg, ok := c.Get("config").(*models.Config)
	if !ok || cfg.AwsBucketName == "" {
		return utils.Error(c, http.StatusInternalServerError, "Storage not configured", "")
	}

	parts := strings.SplitN(key, "/", -1)
	filename := parts[len(parts)-1]
	folder := strings.Join(parts[:len(parts)-1], "/")

	stream, err := bucketrepository.GetFileStream(filename, folder, cfg.AwsBucketName, constants.DefaultCloudProvider)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching file from storage", err.Error())
	}
	defer stream.Close()

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	contentTypeMap := map[string]string{
		"webp": "image/webp",
		"jpg":  "image/jpeg",
		"jpeg": "image/jpeg",
		"png":  "image/png",
		"gif":  "image/gif",
		"avif": "image/avif",
		"heic": "image/heic",
		"heif": "image/heif",
		"mp4":  "video/mp4",
		"webm": "video/webm",
		"mov":  "video/quicktime",
		"m4v":  "video/x-m4v",
		"3gp":  "video/3gpp",
	}
	contentType, ok := contentTypeMap[ext]
	if !ok {
		contentType = "application/octet-stream"
	}

	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Response().Header().Set("Cache-Control", "private, max-age=3600")
	c.Response().WriteHeader(http.StatusOK)
	c.Response().Header().Set(echo.HeaderContentType, contentType)
	_, _ = io.Copy(c.Response(), stream)
	return nil
}

// DELETE /moments/:id
func DeleteMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if err := momentSvc.DeleteMoment(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment deleted", nil)
}

// PUT /moments/:id/requeue — admin action to retry failed/stuck Lambda processing.
// Resets processing_status to "pending" and re-publishes the SQS job.
func RequeueMoment(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	moment, err := momentSvc.GetMomentByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Moment not found", err.Error())
	}

	if err := momentSvc.RequeueMoment(moment); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error requeueing moment", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Moment requeued", moment)
}

// POST /moments/bulk-approve — bulk approve or reject multiple moments.
func BulkApproveRejectMoments(c echo.Context) error {
	var body struct {
		IDs        []string `json:"ids"`
		IsApproved bool     `json:"is_approved"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if len(body.IDs) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No IDs provided", "")
	}

	uuids := make([]uuid.UUID, 0, len(body.IDs))
	for _, idStr := range body.IDs {
		id, err := uuid.FromString(idStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid UUID: "+idStr, "")
		}
		uuids = append(uuids, id)
	}

	if err := momentSvc.BulkUpdateApproval(uuids, body.IsApproved); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating moments", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Moments updated", nil)
}

// presignMomentURL converts a raw S3 key like "moments/id/raw/file.jpg"
// into a 12-hour presigned GET URL. Returns the original key unchanged on error.
func presignMomentURL(key, bucket string) string {
	if key == "" || strings.HasPrefix(key, "http") {
		return key // already a full URL (legacy rows) or empty
	}
	parts := strings.Split(key, "/")
	if len(parts) < 2 {
		return key
	}
	filename := parts[len(parts)-1]
	folder := strings.Join(parts[:len(parts)-1], "/")
	signed, err := bucketrepository.GetPresignedFileURL(filename, folder, bucket, constants.DefaultCloudProvider, 720)
	if err != nil {
		return key
	}
	return signed
}
