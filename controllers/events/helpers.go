package events

import (
	"events-stocks/configuration/constants"
	"events-stocks/models"
	"events-stocks/repositories/bucketrepository"
	"strings"
)

// resolveCoverURL converts a raw S3 path (e.g. "events/uuid.webp") to a 12h presigned URL.
// Returns the input unchanged if it is already a full URL or empty.
func resolveCoverURL(rawPath string, bucket string) string {
	if rawPath == "" || strings.HasPrefix(rawPath, "http") {
		return rawPath
	}
	idx := strings.LastIndex(rawPath, "/")
	if idx < 0 {
		return rawPath
	}
	folder := rawPath[:idx]
	filename := rawPath[idx+1:]
	url, err := bucketrepository.GetPresignedFileURL(
		filename,
		folder,
		bucket,
		constants.DefaultCloudProvider,
		720, // 12 hours
	)
	if err != nil {
		return rawPath
	}
	return url
}

// resolveEventListCovers resolves cover URLs for a slice of events in-place.
func resolveEventListCovers(events []models.Event, bucket string) {
	for i := range events {
		events[i].CoverImageURL = resolveCoverURL(events[i].CoverImageURL, bucket)
		events[i].CoverImageURL2 = resolveCoverURL(events[i].CoverImageURL2, bucket)
	}
}
