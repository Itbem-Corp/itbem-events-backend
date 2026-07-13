package events

import (
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	eventsService "events-stocks/services/events"
	Resources "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"log/slog"
	"net/http"
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
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
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
		status, detail := Resources.UploadErrorResponse(err)
		return utils.Error(c, status, "Failed to upload cover", detail)
	}

	oldCoverPath, err := eventSvc.ReplaceCoverImage(eventID, s3Path)
	if err != nil {
		if cleanupErr := coverResourceSvc.DeleteObjectByPath(s3Path); cleanupErr != nil {
			slog.Error("new event cover rollback failed", "event_id", eventID, "path", s3Path, "error", cleanupErr)
		}
		if errors.Is(err, eventsService.ErrEventNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Failed to update event", err.Error())
	}

	// Delete old cover from S3 only after DB points at the new one.
	if oldCoverPath != "" && coverResourceSvc != nil {
		if cleanupErr := coverResourceSvc.DeleteObjectByPath(oldCoverPath); cleanupErr != nil {
			slog.Warn("old event cover cleanup failed", "event_id", eventID, "path", oldCoverPath, "error", cleanupErr)
		}
	}

	viewURL, viewURLExpiresAt := coverViewURLWithExpiry(s3Path)

	return utils.Success(c, http.StatusOK, "Cover image uploaded", dtos.EventCoverResponse{
		CoverImageURL:         s3Path,
		CoverViewURL:          viewURL,
		CoverViewURLExpiresAt: viewURLExpiresAt,
		ViewURL:               viewURL,
		ViewURLExpiresAt:      viewURLExpiresAt,
	})
}

// DeleteEventCover handles DELETE /api/events/:id/cover.
// It clears the cover reference first, then best-effort deletes the old S3 object.
func DeleteEventCover(c echo.Context) error {
	idParam := c.Param("id")
	eventID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	oldCoverPath, err := eventSvc.ClearCoverImage(eventID)
	if err != nil {
		if errors.Is(err, eventsService.ErrEventNotFound) {
			return utils.Error(c, http.StatusNotFound, "Event not found", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Failed to remove cover", err.Error())
	}

	if oldCoverPath != "" && coverResourceSvc != nil {
		if cleanupErr := coverResourceSvc.DeleteObjectByPath(oldCoverPath); cleanupErr != nil {
			slog.Warn("removed event cover object cleanup failed", "event_id", eventID, "path", oldCoverPath, "error", cleanupErr)
		}
	}

	return utils.Success(c, http.StatusOK, "Cover image removed", dtos.EventCoverResponse{
		CoverImageURL: "",
		ViewURL:       "",
	})
}
