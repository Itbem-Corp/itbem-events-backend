package services

import (
	services "events-stocks/services/validations"
	"fmt"
	"strings"

	"github.com/gofrs/uuid"
)

// momentUploadAllowedMimeTypes is intentionally narrower than general resource
// uploads: a public moment can only contain media that the wall can render.
var momentUploadAllowedMimeTypes = map[string]bool{
	"image/jpeg":       true,
	"image/png":        true,
	"image/gif":        true,
	"image/webp":       true,
	"image/heic":       true,
	"image/heif":       true,
	"image/avif":       true,
	"video/mp4":        true,
	"video/webm":       true,
	"video/quicktime":  true,
	"video/x-m4v":      true,
	"video/3gpp":       true,
	"video/x-msvideo":  true,
	"video/x-matroska": true,
}

func momentUploadLimit(contentType string) (int64, int) {
	if strings.HasPrefix(contentType, "video/") {
		return int64(MaxVideoFileSizeBytes), MaxVideoFileSizeMB
	}
	return int64(MaxMomentImageFileSizeBytes), MaxMomentImageFileSizeMB
}

func (rs *ResourceService) buildMomentRawKey(eventID, filename string) string {
	u, _ := uuid.NewV4()
	ext := ""
	if index := strings.LastIndex(filename, "."); index != -1 {
		ext = strings.ToLower(filename[index:])
	}
	return rs.scopedObjectPath(fmt.Sprintf("moments/%s/raw/%s%s", eventID, u.String(), ext))
}

func normalizeMomentUploadContentType(filename, contentType string) (string, error) {
	contentType = canonicalUploadContentType(contentType)
	if contentType == "" || contentType == "application/octet-stream" {
		ext := ""
		if index := strings.LastIndex(filename, "."); index != -1 {
			ext = strings.ToLower(filename[index+1:])
		}
		contentType = canonicalUploadContentType(guessMimeType(ext))
	}
	if !momentUploadAllowedMimeTypes[contentType] {
		return "", services.ValidationError{Msg: fmt.Sprintf("unsupported file type for moments: %s", contentType)}
	}
	return contentType, nil
}

func canonicalUploadContentType(contentType string) string {
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	if contentType == "image/jpg" {
		return "image/jpeg"
	}
	return contentType
}
