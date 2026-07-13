package services

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	validations "events-stocks/services/validations"
)

type uploadStorageError interface {
	error
	ClientMessage() string
	Temporary() bool
}

// UploadErrorResponse maps internal upload failures to a stable HTTP contract.
// AWS request IDs, bucket names, signatures, and endpoint details stay in server
// logs instead of being reflected to browsers.
func UploadErrorResponse(err error) (status int, detail string) {
	if err == nil {
		return http.StatusInternalServerError, "The upload could not be completed."
	}
	if validations.IsValidationError(err) {
		message := err.Error()
		switch {
		case strings.Contains(message, "size exceeds"), strings.Contains(message, "file too large"):
			return http.StatusRequestEntityTooLarge, message
		case strings.Contains(message, "unsupported") && strings.Contains(message, "type"):
			return http.StatusBadRequest, message
		default:
			return http.StatusBadRequest, message
		}
	}

	var storageErr uploadStorageError
	if errors.As(err, &storageErr) {
		slog.Error("object storage upload failed", "error", err, "temporary", storageErr.Temporary())
		if storageErr.Temporary() {
			return http.StatusServiceUnavailable, storageErr.ClientMessage()
		}
		return http.StatusInternalServerError, storageErr.ClientMessage()
	}
	return http.StatusInternalServerError, "The upload could not be completed. Please retry."
}
