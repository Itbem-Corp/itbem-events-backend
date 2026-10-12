package moments

import (
	"errors"
	"log/slog"
	"strings"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

// ErrMomentDeleted is a media callback for a moment that no longer exists
// (soft-deleted or never stored). The callback handler answers it with a 2xx
// no-op: the media worker treats any non-2xx other than 409 as retryable, so
// an error status would only push the job to the DLQ (MOM-4).
var ErrMomentDeleted = errors.New("moment was deleted")

// DeletedMomentCallbackError carries the soft-deleted moment, when its row
// still exists, so the handler can remove the objects the late job wrote.
type DeletedMomentCallbackError struct {
	Moment *models.Moment
}

func (e *DeletedMomentCallbackError) Error() string { return ErrMomentDeleted.Error() }
func (e *DeletedMomentCallbackError) Unwrap() error { return ErrMomentDeleted }

// deletedMomentReader reads a moment including soft-deleted rows. It is
// optional so focused test doubles of ports.MomentRepository keep compiling.
type deletedMomentReader interface {
	GetMomentByIDUnscoped(id uuid.UUID) (*models.Moment, error)
}

// momentKeyReferenceReader reports which of keys a live moment of the event
// (other than excludeIDs) still references as its content, raw input or
// thumbnail. Duplicate moments of one upload share their raw object; it must
// survive while any of them is alive.
type momentKeyReferenceReader interface {
	LiveMomentKeyReferences(eventID uuid.UUID, excludeIDs []uuid.UUID, keys []string) (map[string]bool, error)
}

func (s *MomentService) deletedMomentCallback(momentID uuid.UUID) error {
	reader, ok := s.repo.(deletedMomentReader)
	if !ok {
		return &DeletedMomentCallbackError{}
	}
	moment, err := reader.GetMomentByIDUnscoped(momentID)
	if err != nil || moment == nil || moment.ID == uuid.Nil {
		return &DeletedMomentCallbackError{}
	}
	return &DeletedMomentCallbackError{Moment: moment}
}

// MomentObjectDeleter removes one object key from the moment's bucket.
type MomentObjectDeleter func(moment models.Moment, key string) error

// MomentPurgeResult counts a purge for the log line.
type MomentPurgeResult struct {
	Deleted int
	Failed  int
	Kept    int
}

// MomentStorageKeys lists the storage objects a moment owns: its content, raw
// input and thumbnail objects and every media variant, after canonical (the
// legacy URL to key normalization; nil keeps keys as stored). Only keys inside
// the moment's own event prefix (moments/<event>/…, optionally under
// organizations/<org>/) are returned, so a purge can never reach another
// event's or an external object.
func MomentStorageKeys(moment models.Moment, canonical func(string) string, extra ...string) []string {
	if moment.EventID == nil || *moment.EventID == uuid.Nil {
		return nil
	}
	candidates := []string{moment.ContentURL, moment.ProcessingInputKey, moment.ThumbnailURL}
	for _, variant := range moment.MediaVariants {
		candidates = append(candidates, variant.ObjectKey)
	}
	candidates = append(candidates, extra...)
	seen := make(map[string]struct{}, len(candidates))
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		key := strings.TrimSpace(candidate)
		if key == "" {
			continue
		}
		if canonical != nil {
			if normalized := strings.TrimSpace(canonical(key)); normalized != "" {
				key = normalized
			}
		}
		if !IsEventMomentKey(*moment.EventID, key) {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// PurgeMomentObjects deletes the storage objects of moments that were just
// deleted (MOM-3). It is best effort: every failure is logged and counted,
// never returned, because the database delete has already committed and the
// moment is no longer served. A key that another live moment of the same
// event still references is kept; when that check cannot run, the moment's
// objects are kept and logged rather than risk deleting shared media.
func (s *MomentService) PurgeMomentObjects(moments []models.Moment, canonical func(string) string, deleteObject MomentObjectDeleter, extraKeys ...string) MomentPurgeResult {
	var result MomentPurgeResult
	if deleteObject == nil || len(moments) == 0 {
		return result
	}
	excluded := make([]uuid.UUID, 0, len(moments))
	for _, moment := range moments {
		excluded = append(excluded, moment.ID)
	}
	references, _ := s.repo.(momentKeyReferenceReader)
	for _, moment := range moments {
		keys := MomentStorageKeys(moment, canonical, extraKeys...)
		if len(keys) == 0 {
			continue
		}
		if references == nil {
			slog.Warn("moment media purge skipped: reference check unavailable", "moment_id", moment.ID)
			result.Kept += len(keys)
			continue
		}
		referenced, err := references.LiveMomentKeyReferences(*moment.EventID, excluded, keys)
		if err != nil {
			slog.Error("moment media purge skipped: reference check failed", "moment_id", moment.ID, "error", err)
			result.Kept += len(keys)
			continue
		}
		for _, key := range keys {
			if referenced[key] {
				result.Kept++
				continue
			}
			if err := deleteObject(moment, key); err != nil {
				result.Failed++
				slog.Error("moment media purge failed", "moment_id", moment.ID, "event_id", *moment.EventID, "object_key", key, "error", err)
				continue
			}
			result.Deleted++
		}
	}
	slog.Info("moment media purged", "moments", len(moments), "deleted", result.Deleted, "failed", result.Failed, "kept", result.Kept)
	return result
}
