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
	if IsInvalidMediaFile(err) {
		slog.Info("rejected invalid media upload", "error", err)
		return http.StatusBadRequest, InvalidMediaFileCode
	}
	if errors.Is(err, ErrUploadNotFound) {
		slog.Info("upload confirmation without a stored object", "error", err)
		return http.StatusUnprocessableEntity, UploadNotFoundCode
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

// UploadNotFoundCode is the "error" code (422) of a confirmation whose object
// never reached storage (the presigned PUT failed or was not sent). It is a
// client condition: retrying the same confirmation cannot succeed, the file
// has to be uploaded again.
const UploadNotFoundCode = "upload_not_found"

// UploadNotFoundMessage is the Spanish message shown for UploadNotFoundCode.
const UploadNotFoundMessage = "No recibimos tu archivo. Vuelve a subirlo."

// ErrUploadNotFound marks a confirmation whose object is not in storage.
var ErrUploadNotFound = errors.New(UploadNotFoundCode + ": the uploaded object is not in storage")

// InvalidMediaFileCode is the "error" code of an upload whose bytes are not
// the image or video it claims to be (400).
const InvalidMediaFileCode = "invalid_media_file"

// InvalidMediaFileMessage is the Spanish message shown for InvalidMediaFileCode.
const InvalidMediaFileMessage = "El archivo no es una imagen o video válido. Elige otro archivo."

// InvalidMediaFileError: the uploaded file's content does not match an
// allowed image/video type or cannot be decoded. Reason stays in server logs.
type InvalidMediaFileError struct {
	Reason string
}

func (e InvalidMediaFileError) Error() string {
	return InvalidMediaFileCode + ": " + e.Reason
}

// ErrImageDecode marks an image whose bytes the optimizer cannot decode
// (corrupt or mislabeled): the file is the problem, so it maps to
// InvalidMediaFileError. Any other optimizer failure (I/O, libvips support,
// processing) stays a server error.
var ErrImageDecode = errors.New("image content could not be decoded")

func IsInvalidMediaFile(err error) bool {
	var target InvalidMediaFileError
	return errors.As(err, &target)
}

// UploadErrorMessage is the message for a failed upload: the Spanish
// invalid-file text when that is the cause, fallback otherwise.
func UploadErrorMessage(err error, fallback string) string {
	if IsInvalidMediaFile(err) {
		return InvalidMediaFileMessage
	}
	if errors.Is(err, ErrUploadNotFound) {
		return UploadNotFoundMessage
	}
	return fallback
}
