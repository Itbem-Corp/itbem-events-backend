package moments

import (
	"events-stocks/models"
	"events-stocks/repositories/awsrepository"
)

// rewriteMomentURL rewrites a single URL string from S3 to CDN if CDN_BASE_URL is set.
// Safe to call with empty strings or non-S3 URLs (no-op).
func rewriteMomentURL(url string) string {
	return awsrepository.RewriteToCDN(url)
}

// rewriteMomentURLs rewrites S3 content/thumbnail URLs in a Moment to CDN URLs.
// Safe to call when CDN_BASE_URL is unset (no-op).
func rewriteMomentURLs(m *models.Moment) {
	m.ContentURL   = awsrepository.RewriteToCDN(m.ContentURL)
	m.ThumbnailURL = awsrepository.RewriteToCDN(m.ThumbnailURL)
}

// rewriteMomentsURLs rewrites URLs for a slice of moments.
func rewriteMomentsURLs(moments []models.Moment) {
	for i := range moments {
		rewriteMomentURLs(&moments[i])
	}
}
