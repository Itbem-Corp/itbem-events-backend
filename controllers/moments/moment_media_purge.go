package moments

import (
	"log/slog"

	"events-stocks/models"
)

// runMomentMediaPurge runs a purge after the response path; tests make it
// synchronous. A bulk delete can name thousands of moments, so the S3 deletes
// never hold the request open.
var runMomentMediaPurge = func(purge func()) { go purge() }

// scheduleMomentMediaPurge removes the storage objects of moments whose
// database delete has already committed (MOM-3): content, raw input,
// thumbnail and variants, each in the moment's own bucket, plus extraKeys
// (keys a late media callback reports). Best effort and logged: a failure
// leaves an orphan object, never an error for the caller. Keys outside the
// moment's event prefix and keys another live moment still uses are skipped
// by MomentService.PurgeMomentObjects.
func scheduleMomentMediaPurge(moments []models.Moment, extraKeys ...string) {
	if len(moments) == 0 || momentSvc == nil {
		return
	}
	resSvc := adminResSvc
	if resSvc == nil {
		slog.Warn("moment media purge skipped: resource service unavailable", "moments", len(moments))
		return
	}
	svc := momentSvc
	snapshot := append([]models.Moment(nil), moments...)
	extra := append([]string(nil), extraKeys...)
	runMomentMediaPurge(func() {
		svc.PurgeMomentObjects(snapshot, resSvc.CanonicalObjectKey, func(moment models.Moment, key string) error {
			return resSvc.WithBucket(moment.MediaBucket).DeleteMomentUpload(key)
		}, extra...)
	})
}
