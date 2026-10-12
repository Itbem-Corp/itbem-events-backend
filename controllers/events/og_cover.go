package events

import (
	"net/http"
	"net/url"
	"strings"

	"events-stocks/repositories/awsrepository"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
)

// eventCoverRedirectTTLMinutes is the lifetime of the presigned URL the cover
// route redirects to. The route itself never expires, which is what social
// previews need (STU-49): og:image can point at it for as long as the event
// is public, and each fetch gets a fresh short-lived URL.
const eventCoverRedirectTTLMinutes = 60

// eventCoverRedirectCacheControl lets crawlers and CDNs reuse the redirect for
// a while, well inside the presigned URL lifetime.
const eventCoverRedirectCacheControl = "public, max-age=600"

// EventCoverImagePath is the stable public path of an event's cover, relative
// to the API base (…/api). /meta returns it as og_image_url.
func EventCoverImagePath(identifier string) string {
	return "/api/events/" + url.PathEscape(identifier) + "/cover-image"
}

func ogImageURL(identifier, cover string) string {
	if strings.TrimSpace(cover) == "" || strings.TrimSpace(identifier) == "" {
		return ""
	}
	return EventCoverImagePath(identifier)
}

// coverStorageKey returns the object key of a cover stored in our media
// storage: a plain key, or a legacy absolute URL of the event bucket (S3,
// s3:// or the configured CDN_BASE_URL) normalized back to its key. Any other
// absolute URL (an organizer-entered external link, javascript:, data:...) is
// rejected, so the route only ever redirects to a fresh presigned URL of our
// own storage and is never an open redirector.
func coverStorageKey(cover, bucket string) (string, bool) {
	if strings.HasPrefix(strings.TrimSpace(cover), "//") {
		return "", false // protocol-relative: a host, not a key
	}
	key := strings.TrimSpace(awsrepository.S3KeyFromURL(cover, bucket))
	if key == "" || utils.IsAbsoluteURLLike(key) || strings.Contains(key, "://") {
		return "", false
	}
	return key, true
}

// GetEventCoverImage redirects (302) to a fresh view URL of the event cover.
// It uses the same read gate as /meta, so a password-protected or inactive
// event never reveals its cover; any denial or a missing cover is 404.
//
//	GET /api/events/:identifier/cover-image
func GetEventCoverImage(c echo.Context) error {
	identifier := strings.TrimSpace(c.Param("identifier"))
	if identifier == "" {
		return utils.Error(c, http.StatusNotFound, "Portada no encontrada", "cover_not_found")
	}
	event, err := eventSvc.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		return utils.Error(c, http.StatusNotFound, "Portada no encontrada", "cover_not_found")
	}
	access, err := allowPublicEventReadWithProofAndConfig(c, event)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading event config", err.Error())
	}
	cover := strings.TrimSpace(event.CoverImageURL)
	if !access.Allowed || cover == "" {
		return utils.Error(c, http.StatusNotFound, "Portada no encontrada", "cover_not_found")
	}
	key, ok := coverStorageKey(cover, event.MediaBucket)
	if !ok {
		return utils.Error(c, http.StatusNotFound, "Portada no encontrada", "cover_not_found")
	}
	target, expiresAt := resourceViewURLWithExpiry(key, eventCoverRedirectTTLMinutes, event.MediaBucket)
	if expiresAt == nil || target == "" {
		return utils.Error(c, http.StatusServiceUnavailable, "La portada no está disponible por ahora", "cover_unavailable")
	}
	if c.Response().Header().Get(echo.HeaderCacheControl) == "" {
		c.Response().Header().Set(echo.HeaderCacheControl, eventCoverRedirectCacheControl)
	}
	return c.Redirect(http.StatusFound, target)
}
