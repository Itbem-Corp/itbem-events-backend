package moments

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"events-stocks/models"
	sqsrepository "events-stocks/repositories/sqsrepository"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

// _momentSvc is the package-level singleton set by server.go.
var _momentSvc *MomentService

// SetDefaultMomentService wires the package-level functions to the DI instance.
func SetDefaultMomentService(svc *MomentService) { _momentSvc = svc }

func ListMoments() ([]models.Moment, error)              { return _momentSvc.ListMoments() }
func GetMomentByID(id uuid.UUID) (*models.Moment, error) { return _momentSvc.GetMomentByID(id) }
func CreateMoment(obj *models.Moment) error              { return _momentSvc.CreateMoment(obj) }
func UpdateMoment(obj *models.Moment) error              { return _momentSvc.UpdateMoment(obj) }
func DeleteMoment(id uuid.UUID) error                    { return _momentSvc.DeleteMoment(id) }
func ListMomentsByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) {
	return _momentSvc.ListByEventID(eventID, approvedOnly)
}
func UpdateMomentContent(id uuid.UUID, contentURL, status, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64, eventID *uuid.UUID) error {
	return _momentSvc.UpdateMomentContent(id, contentURL, status, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes, eventID)
}
func ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	return _momentSvc.ListForDashboard(eventID)
}
func ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	return _momentSvc.ListApprovedForWall(eventID, page, limit)
}
func BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	return _momentSvc.BulkUpdateApproval(ids, isApproved)
}

// MomentService is the injectable, struct-based moment service.
type MomentService struct {
	repo  ports.MomentRepository
	cache ports.CacheRepository
}

func NewMomentService(repo ports.MomentRepository, cache ports.CacheRepository) *MomentService {
	return &MomentService{repo: repo, cache: cache}
}

// wallCacheKey returns the Redis key for a paginated public wall result.
func wallCacheKey(eventID uuid.UUID, page, limit int) string {
	return fmt.Sprintf("moments:wall:%s:p%d:l%d", eventID.String(), page, limit)
}

// invalidateWallCache removes all cached wall pages for an event.
func (s *MomentService) invalidateWallCache(eventID uuid.UUID) {
	ctx := context.Background()
	_ = s.cache.DeleteKeysByPattern(ctx, fmt.Sprintf("moments:wall:%s:*", eventID.String()))
}

func (s *MomentService) ListMoments() ([]models.Moment, error) {
	cacheKey := "all:moments"
	ctx := context.Background()
	cached, err := s.cache.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.Moment
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}
	data, err := s.repo.ListMoments()
	if err != nil {
		return nil, err
	}
	jsonStr, _ := json.Marshal(data)
	_ = s.cache.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["moments"])
	return data, nil
}

func (s *MomentService) GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	return s.repo.GetMomentByID(id)
}

func (s *MomentService) CreateMoment(obj *models.Moment) error {
	if err := s.repo.CreateMoment(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("moments", "all")
}

func (s *MomentService) UpdateMoment(obj *models.Moment) error {
	if err := s.repo.UpdateMoment(obj); err != nil {
		return err
	}
	// Invalidate wall cache when approval status changes
	if obj.EventID != nil {
		s.invalidateWallCache(*obj.EventID)
	}
	return s.cache.Invalidate("moments", "all")
}

func (s *MomentService) DeleteMoment(id uuid.UUID) error {
	// Fetch before deleting so we can invalidate the wall cache for the right event
	m, _ := s.repo.GetMomentByID(id)
	if err := s.repo.DeleteMoment(id); err != nil {
		return err
	}
	if m != nil && m.EventID != nil {
		s.invalidateWallCache(*m.EventID)
	}
	return s.cache.Invalidate("moments", "all")
}

func (s *MomentService) ListByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) {
	return s.repo.ListByEventID(eventID, approvedOnly)
}

// ListForDashboard returns moments ready for admin review — excludes pending/processing.
// Dashboard always gets fresh data so admins see processing state changes immediately.
func (s *MomentService) ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	return s.repo.ListForDashboard(eventID)
}

// ListApprovedForWall returns approved + optimized moments for the public wall, paginated.
// Results are cached in Redis for 5 minutes; cache is busted on approval changes or Lambda completion.
func (s *MomentService) ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	ctx := context.Background()
	cacheKey := wallCacheKey(eventID, page, limit)

	if cached, err := s.cache.GetKey(ctx, cacheKey); err == nil && cached != "" {
		var payload struct {
			Items []models.Moment `json:"items"`
			Total int64           `json:"total"`
		}
		if err := json.Unmarshal([]byte(cached), &payload); err == nil {
			return payload.Items, payload.Total, nil
		}
	}

	items, total, err := s.repo.ListApprovedForWall(eventID, page, limit)
	if err != nil {
		return nil, 0, err
	}

	data, _ := json.Marshal(struct {
		Items []models.Moment `json:"items"`
		Total int64           `json:"total"`
	}{items, total})
	_ = s.cache.SaveKey(ctx, cacheKey, string(data), 5*time.Minute)

	return items, total, nil
}

// UpdateMomentContent is called by the Lambda after media processing completes.
// thumbnailURL is the S3 key of the extracted thumbnail; pass "" to skip (images, failures).
// durationMs/originalBytes/optimizedBytes are Lambda processing metrics; pass 0 to skip.
// eventID, when non-nil, is used directly to bust the wall cache without an extra DB query.
// When nil, falls back to a GetMomentByID lookup (backward compatibility).
func (s *MomentService) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64, eventID *uuid.UUID) error {
	if err := s.repo.UpdateMomentContent(id, contentURL, processingStatus, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes); err != nil {
		return err
	}
	if eventID != nil {
		s.invalidateWallCache(*eventID)
	} else {
		// Fallback: fetch moment to get event_id for cache invalidation
		m, _ := s.repo.GetMomentByID(id)
		if m != nil && m.EventID != nil {
			s.invalidateWallCache(*m.EventID)
		}
	}
	return s.cache.Invalidate("moments", "all")
}

// RequeueMoment resets processing_status to "pending" and republishes the SQS job.
// Called by admin when a moment is stuck in "failed" or "processing".
// The raw S3 key must still be present in ContentURL (i.e. the moment has not yet
// been fully optimized and the raw path replaced). If the path no longer contains
// "/raw/", an error is returned — the moment cannot be requeued without the raw file.
func (s *MomentService) RequeueMoment(moment *models.Moment) error {
	if moment.EventID == nil {
		return fmt.Errorf("cannot requeue: moment has no EventID")
	}

	if !strings.Contains(moment.ContentURL, "/raw/") {
		return fmt.Errorf("cannot requeue: optimized file already processed, raw key not available")
	}

	// Reset to pending — preserve existing thumbnail_url (pass "" = no-op in repo)
	if err := s.repo.UpdateMomentContent(moment.ID, moment.ContentURL, "pending", "", "", 0, 0, 0); err != nil {
		return err
	}

	isVideo := strings.HasSuffix(moment.ContentURL, ".mp4") ||
		strings.HasSuffix(moment.ContentURL, ".mov") ||
		strings.HasSuffix(moment.ContentURL, ".webm")

	// Use stored ContentType if available; otherwise infer from file extension.
	ct := moment.ContentType
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(moment.ContentURL))
		if ct == "" {
			ct = "application/octet-stream"
		}
	}

	if _, err := sqsrepository.PublishMediaJob(sqsrepository.MediaProcessMessage{
		MomentID:    moment.ID.String(),
		EventID:     moment.EventID.String(),
		RawS3Key:    moment.ContentURL,
		Bucket:      os.Getenv("S3_BUCKET_NAME"),
		ContentType: ct,
		IsVideo:     isVideo,
	}); err != nil {
		return fmt.Errorf("requeue SQS publish failed: %w", err)
	}

	// Invalidate wall cache
	s.invalidateWallCache(*moment.EventID)
	return s.cache.Invalidate("moments", "all")
}

// SummaryByEventIDs returns the pending moment count for a batch of events.
// No cache — counts change frequently and the query is a single GROUP BY.
func (s *MomentService) SummaryByEventIDs(eventIDs []uuid.UUID) ([]models.MomentSummary, error) {
	return s.repo.SummaryByEventIDs(eventIDs)
}

func SummaryByEventIDs(eventIDs []uuid.UUID) ([]models.MomentSummary, error) {
	return _momentSvc.SummaryByEventIDs(eventIDs)
}

// BulkReorder sets custom display order and busts all wall caches for affected events.
func (s *MomentService) BulkReorder(items []models.MomentOrderItem) error {
	if err := s.repo.BulkReorder(items); err != nil {
		return err
	}
	// Bust all wall caches — pattern-delete all wall keys for safety.
	ctx := context.Background()
	_ = s.cache.DeleteKeysByPattern(ctx, "moments:wall:*")
	return nil
}

func BulkReorder(items []models.MomentOrderItem) error {
	return _momentSvc.BulkReorder(items)
}

// BulkUpdateApproval updates is_approved for multiple moments by ID.
func (s *MomentService) BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	// Fetch distinct event IDs before updating so we can invalidate per-event wall caches.
	eventIDs, _ := s.repo.GetDistinctEventIDsByMomentIDs(ids)

	if err := s.repo.BulkUpdateApproval(ids, isApproved); err != nil {
		return err
	}

	for _, eid := range eventIDs {
		s.invalidateWallCache(eid)
	}
	return s.cache.Invalidate("moments", "all")
}
