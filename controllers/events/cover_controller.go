package events

import (
	"events-stocks/repositories/bucketrepository"
	"events-stocks/repositories/eventsrepository"
	Resources "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strings"
)

var coverResourceSvc *Resources.ResourceService

// InitCoverController wires the ResourceService needed for cover uploads.
func InitCoverController(svc *Resources.ResourceService) {
	coverResourceSvc = svc
}

// UploadEventCover handles POST /api/events/:id/cover
// Accepts multipart/form-data with a "file" field.
// Uploads the image to S3, updates Event.CoverImageURL, and returns the presigned view URL.
func UploadEventCover(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "File is required", err.Error())
	}

	file, err := fileHeader.Open()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error opening file", err.Error())
	}
	defer file.Close()

	// 1. Upload + optimize → S3 "events/{uuid}.webp"
	s3Path, err := coverResourceSvc.UploadEventCover(file, fileHeader)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to upload cover", err.Error())
	}

	// 2. Fetch current event and update CoverImageURL
	event, err := eventsrepository.GetEventByIDRaw(eventID)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Event not found", err.Error())
	}

	// Delete old cover from S3 if it exists
	if event.CoverImageURL != "" {
		_ = coverResourceSvc.DeleteObjectByPath(event.CoverImageURL)
	}

	event.CoverImageURL = s3Path
	if err := eventSvc.UpdateEvent(event); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to update event", err.Error())
	}

	// 3. Generate presigned URL for immediate display
	parts := strings.Split(s3Path, "/")
	filename := parts[len(parts)-1]
	folder := strings.Join(parts[:len(parts)-1], "/")

	viewURL, _ := bucketrepository.GetPresignedFileURL(
		filename,
		folder,
		coverResourceSvc.Bucket,
		coverResourceSvc.Provider,
		720,
	)

	return utils.Success(c, http.StatusOK, "Cover image uploaded", map[string]string{
		"cover_image_url": s3Path,
		"view_url":        viewURL,
	})
}
