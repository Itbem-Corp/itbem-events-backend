package moments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/awsrepository"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
)

var ErrInvalidMomentProcessingStatus = errors.New("invalid moment processing status")
var ErrInvalidMomentProcessingCallback = errors.New("invalid moment processing callback")
var ErrStaleMomentProcessingCallback = errors.New("stale moment processing callback")

// _momentSvc is the package-level singleton set by internal/app.
var _momentSvc *MomentService

// SetDefaultMomentService wires the package-level functions to the DI instance.
func SetDefaultMomentService(svc *MomentService) { _momentSvc = svc }
func IsInitialized() bool                        { return _momentSvc != nil }

func ListMoments() ([]models.Moment, error)              { return _momentSvc.ListMoments() }
func GetMomentByID(id uuid.UUID) (*models.Moment, error) { return _momentSvc.GetMomentByID(id) }
func GetMomentByEventIDAndContentURL(eventID uuid.UUID, contentURL string) (*models.Moment, error) {
	return _momentSvc.GetMomentByEventIDAndContentURL(eventID, contentURL)
}
func CreateMoment(obj *models.Moment) error   { return _momentSvc.CreateMoment(obj) }
func UpdateMoment(obj *models.Moment) error   { return _momentSvc.UpdateMoment(obj) }
func DeleteMoment(id uuid.UUID) error         { return _momentSvc.DeleteMoment(id) }
func BulkDeleteMoments(ids []uuid.UUID) error { return _momentSvc.BulkDeleteMoments(ids) }
func ListMomentsByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) {
	return _momentSvc.ListByEventID(eventID, approvedOnly)
}
func UpdateMomentContent(id uuid.UUID, contentURL, status, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	return _momentSvc.UpdateMomentContent(id, contentURL, status, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes)
}
func ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	return _momentSvc.ListForDashboard(eventID)
}
func ListForDashboardPage(eventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error) {
	return _momentSvc.ListForDashboardPage(eventID, page, pageSize)
}
func ListPendingSummaryByEventIDs(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error) {
	return _momentSvc.ListPendingSummaryByEventIDs(eventIDs)
}
func ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	return _momentSvc.ListApprovedForWall(eventID, page, limit)
}
func ListApprovedForWallCursor(eventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error) {
	return _momentSvc.ListApprovedForWallCursor(eventID, afterCreatedAt, afterID, afterOrder, limit)
}
func BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	return _momentSvc.BulkUpdateApproval(ids, isApproved)
}
func EnqueueMediaProcessing(moment *models.Moment, rawKey, bucket, contentType string) bool {
	return _momentSvc.EnqueueMediaProcessing(moment, rawKey, bucket, contentType)
}

// MomentService is the injectable, struct-based moment service.
type MomentService struct {
	repo           ports.MomentRepository
	cache          ports.CacheRepository
	mediaPublisher ports.MediaJobPublisher
}

func NewMomentService(repo ports.MomentRepository, cache ports.CacheRepository, mediaPublisher ...ports.MediaJobPublisher) *MomentService {
	var publisher ports.MediaJobPublisher
	if len(mediaPublisher) > 0 {
		publisher = mediaPublisher[0]
	}
	return &MomentService{repo: repo, cache: cache, mediaPublisher: publisher}
}

// wallCacheKey returns the Redis key for a paginated public wall result.
func wallCacheKey(eventID uuid.UUID, page, limit int) string {
	return fmt.Sprintf("moments:wall:%s:p%d:l%d", eventID.String(), page, limit)
}

// invalidateWallCache removes all cached wall pages for an event.
func (s *MomentService) invalidateWallCache(eventID uuid.UUID) {
	if s.cache == nil || eventID == uuid.Nil {
		return
	}
	_ = s.cache.DeleteKeysByPattern(context.Background(), fmt.Sprintf("moments:wall:%s:*", eventID.String()))
}

func (s *MomentService) invalidateMomentsCache() error {
	if s.cache == nil {
		return nil
	}
	_ = s.cache.Invalidate(utils.RedisMomentsKey, "all")
	return nil
}

func (s *MomentService) ListMoments() ([]models.Moment, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisMomentsKey,
		utils.CacheTTLs[utils.RedisMomentsKey],
		func() ([]models.Moment, error) {
			return s.repo.ListMoments()
		},
	)
}

func (s *MomentService) GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	return s.repo.GetMomentByID(id)
}

func (s *MomentService) GetMomentByEventIDAndContentURL(eventID uuid.UUID, contentURL string) (*models.Moment, error) {
	return s.repo.GetMomentByEventIDAndContentURL(eventID, strings.TrimSpace(contentURL))
}

func (s *MomentService) CreateMoment(obj *models.Moment) error {
	if err := s.repo.CreateMoment(obj); err != nil {
		return err
	}
	if obj.EventID != nil {
		s.invalidateWallCache(*obj.EventID)
	}
	return s.invalidateMomentsCache()
}

func (s *MomentService) UpdateMoment(obj *models.Moment) error {
	if err := s.repo.UpdateMoment(obj); err != nil {
		return err
	}
	if obj.EventID != nil {
		s.invalidateWallCache(*obj.EventID)
	}
	return s.invalidateMomentsCache()
}

func (s *MomentService) DeleteMoment(id uuid.UUID) error {
	m, _ := s.repo.GetMomentByID(id)
	if err := s.repo.DeleteMoment(id); err != nil {
		return err
	}
	if m != nil && m.EventID != nil {
		s.invalidateWallCache(*m.EventID)
	}
	return s.invalidateMomentsCache()
}

func (s *MomentService) BulkDeleteMoments(ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	eventIDs, _ := s.repo.GetDistinctEventIDsByMomentIDs(ids)
	if err := s.repo.BulkDeleteMoments(ids); err != nil {
		return err
	}
	for _, eventID := range eventIDs {
		s.invalidateWallCache(eventID)
	}
	return s.invalidateMomentsCache()
}

func (s *MomentService) ListByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) {
	return s.repo.ListByEventID(eventID, approvedOnly)
}

// ListForDashboard returns moments ready for admin review and skips cache so admins see worker state changes immediately.
func (s *MomentService) ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	return s.repo.ListForDashboard(eventID)
}

func (s *MomentService) ListForDashboardPage(eventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error) {
	return s.repo.ListForDashboardPage(eventID, page, pageSize)
}

func (s *MomentService) ListPendingSummaryByEventIDs(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error) {
	return s.repo.ListPendingSummaryByEventIDs(eventIDs)
}

// ListApprovedForWall returns approved + optimized moments for the public wall, paginated.
func (s *MomentService) ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	ctx := context.Background()
	cacheKey := wallCacheKey(eventID, page, limit)

	if s.cache != nil {
		if cached, err := s.cache.GetKey(ctx, cacheKey); err == nil && cached != "" {
			var payload struct {
				Items []models.Moment `json:"items"`
				Total int64           `json:"total"`
			}
			if err := json.Unmarshal([]byte(cached), &payload); err == nil {
				return payload.Items, payload.Total, nil
			}
		}
	}

	items, total, err := s.repo.ListApprovedForWall(eventID, page, limit)
	if err != nil {
		return nil, 0, err
	}

	if s.cache != nil {
		data, _ := json.Marshal(struct {
			Items []models.Moment `json:"items"`
			Total int64           `json:"total"`
		}{items, total})
		_ = s.cache.SaveKey(ctx, cacheKey, string(data), 5*time.Minute)
	}

	return items, total, nil
}

func (s *MomentService) ListApprovedForWallCursor(eventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error) {
	return s.repo.ListApprovedForWallCursor(eventID, afterCreatedAt, afterID, afterOrder, limit)
}

// UpdateMomentContent is called by the worker after media processing completes.
func (s *MomentService) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	normalizedStatus, err := normalizeMomentProcessingStatus(processingStatus)
	if err != nil {
		return err
	}
	if err := s.repo.UpdateMomentContent(id, contentURL, normalizedStatus, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes); err != nil {
		return err
	}
	m, _ := s.repo.GetMomentByID(id)
	if m != nil && m.EventID != nil {
		s.invalidateWallCache(*m.EventID)
	}
	return s.invalidateMomentsCache()
}

// ApplyMediaProcessingCallback validates the worker identity, event ownership,
// output keys and monotonic transition before the repository performs a CAS.
// Repositories that predate the CAS extension keep the legacy behavior for
// compatibility in tests and non-production adapters.
func (s *MomentService) ApplyMediaProcessingCallback(callback dtos.MediaProcessingCallback) error {
	status, err := normalizeMomentProcessingStatus(callback.ProcessingStatus)
	if err != nil {
		return err
	}
	if status != "processing" && status != "done" && status != "failed" {
		return fmt.Errorf("%w: workers may only report processing, done, or failed", ErrInvalidMomentProcessingCallback)
	}
	momentID, err := uuid.FromString(strings.TrimSpace(callback.MomentID))
	if err != nil {
		return fmt.Errorf("%w: invalid moment_id", ErrInvalidMomentProcessingCallback)
	}

	processingRepo, supportsCAS := s.repo.(ports.MomentProcessingRepository)
	if !supportsCAS {
		return s.UpdateMomentContent(momentID, callback.ObjectKey, status, callback.ThumbnailObjectKey, callback.ErrorMessage, callback.ProcessingDurationMs, callback.OriginalSizeBytes, callback.OptimizedSizeBytes)
	}

	moment, err := s.repo.GetMomentByID(momentID)
	if err != nil {
		return err
	}
	if moment == nil || moment.EventID == nil {
		return fmt.Errorf("%w: moment has no event", ErrInvalidMomentProcessingCallback)
	}
	eventID := *moment.EventID
	if strings.TrimSpace(callback.EventID) != "" {
		callbackEventID, parseErr := uuid.FromString(strings.TrimSpace(callback.EventID))
		if parseErr != nil || callbackEventID != eventID {
			return fmt.Errorf("%w: event_id does not own moment", ErrInvalidMomentProcessingCallback)
		}
	} else if moment.ProcessingGeneration > 0 {
		return fmt.Errorf("%w: event_id is required for generated jobs", ErrInvalidMomentProcessingCallback)
	}

	if moment.ProcessingGeneration > 0 || moment.ProcessingJobID != "" {
		if callback.Generation != moment.ProcessingGeneration || strings.TrimSpace(callback.JobID) != moment.ProcessingJobID {
			return fmt.Errorf("%w: job identity no longer current", ErrStaleMomentProcessingCallback)
		}
	} else if callback.Generation != 0 || strings.TrimSpace(callback.JobID) != "" {
		return fmt.Errorf("%w: legacy moment has no matching job identity", ErrStaleMomentProcessingCallback)
	}

	if err := validateMediaCallbackKeys(moment, status, callback.ObjectKey, callback.ThumbnailObjectKey); err != nil {
		return err
	}
	if len(callback.ErrorMessage) > 500 {
		return fmt.Errorf("%w: error_message exceeds 500 bytes", ErrInvalidMomentProcessingCallback)
	}

	currentStatus := strings.ToLower(strings.TrimSpace(moment.ProcessingStatus))
	if currentStatus == "done" || currentStatus == "failed" {
		if currentStatus == status && (status != "done" || (moment.ContentURL == callback.ObjectKey && (callback.ThumbnailObjectKey == "" || moment.ThumbnailURL == callback.ThumbnailObjectKey))) {
			return nil // exact terminal replay is idempotent
		}
		return fmt.Errorf("%w: terminal state %s cannot transition to %s", ErrStaleMomentProcessingCallback, currentStatus, status)
	}
	if currentStatus != "pending" && currentStatus != "processing" {
		return fmt.Errorf("%w: state %q is not owned by an active job", ErrStaleMomentProcessingCallback, currentStatus)
	}

	applied, err := processingRepo.ApplyMediaProcessingUpdate(
		momentID, eventID, moment.ProcessingJobID, moment.ProcessingGeneration,
		[]string{"pending", "processing"},
		callback.ObjectKey, status, callback.ThumbnailObjectKey, callback.ErrorMessage,
		callback.ProcessingDurationMs, callback.OriginalSizeBytes, callback.OptimizedSizeBytes,
	)
	if err != nil {
		return err
	}
	if !applied {
		return fmt.Errorf("%w: state changed before callback commit", ErrStaleMomentProcessingCallback)
	}
	s.invalidateWallCache(eventID)
	return s.invalidateMomentsCache()
}

func validateMediaCallbackKeys(moment *models.Moment, status, objectKey, thumbnailKey string) error {
	objectKey = strings.TrimSpace(objectKey)
	thumbnailKey = strings.TrimSpace(thumbnailKey)
	if objectKey == "" || moment == nil || moment.EventID == nil {
		return fmt.Errorf("%w: object_key is required", ErrInvalidMomentProcessingCallback)
	}
	eventID := moment.EventID.String()
	expectedInput := strings.TrimSpace(moment.ProcessingInputKey)
	if expectedInput == "" && (moment.ProcessingStatus == "pending" || moment.ProcessingStatus == "processing" || moment.ProcessingStatus == "failed") {
		expectedInput = strings.TrimSpace(moment.ContentURL)
	}
	if status == "processing" || status == "failed" {
		if expectedInput == "" || objectKey != expectedInput || thumbnailKey != "" {
			return fmt.Errorf("%w: non-final callback must reference the current input object", ErrInvalidMomentProcessingCallback)
		}
		return nil
	}

	isVideo := detectMediaIsVideo(moment.ContentType, expectedInput)
	allowedObjectKeys := map[string]bool{}
	if isVideo {
		allowedObjectKeys[fmt.Sprintf("moments/%s/videos/%s.mp4", eventID, moment.ID)] = true
		// Compatibility with the first processor key layout.
		allowedObjectKeys[fmt.Sprintf("moments/%s/%s.mp4", eventID, moment.ID)] = true
		expectedThumbnail := fmt.Sprintf("moments/%s/videos/%s-thumb.webp", eventID, moment.ID)
		legacyThumbnail := fmt.Sprintf("moments/%s/%s-thumb.webp", eventID, moment.ID)
		if thumbnailKey != "" && thumbnailKey != expectedThumbnail && thumbnailKey != legacyThumbnail {
			return fmt.Errorf("%w: thumbnail key is outside the moment video prefix", ErrInvalidMomentProcessingCallback)
		}
	} else {
		for _, extension := range []string{".webp", ".gif", ".avif"} {
			allowedObjectKeys[fmt.Sprintf("moments/%s/photos/%s%s", eventID, moment.ID, extension)] = true
			allowedObjectKeys[fmt.Sprintf("moments/%s/%s%s", eventID, moment.ID, extension)] = true
		}
		if thumbnailKey != "" {
			return fmt.Errorf("%w: image callback cannot set a video thumbnail", ErrInvalidMomentProcessingCallback)
		}
	}
	if !allowedObjectKeys[objectKey] {
		return fmt.Errorf("%w: final object key does not belong to moment/event", ErrInvalidMomentProcessingCallback)
	}
	return nil
}

func normalizeMomentProcessingStatus(value string) (string, error) {
	status := strings.ToLower(strings.TrimSpace(value))
	switch status {
	case "", "pending", "processing", "done", "failed":
		return status, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidMomentProcessingStatus, value)
	}
}

func detectMediaIsVideo(contentType, filename string) bool {
	if strings.HasPrefix(contentType, "video/") {
		return true
	}
	videoExts := map[string]bool{
		".mp4":  true,
		".webm": true,
		".mov":  true,
		".avi":  true,
		".mkv":  true,
		".m4v":  true,
	}
	return videoExts[strings.ToLower(filepath.Ext(filename))]
}

// EnqueueMediaProcessing queues optimization for images and videos.
func (s *MomentService) EnqueueMediaProcessing(moment *models.Moment, rawKey, bucket, contentType string) bool {
	if moment.EventID == nil {
		return false
	}
	if s.mediaPublisher == nil {
		s.failMediaProcessingJob(moment, errors.New("media publisher is not configured"))
		return false
	}
	jobID, generation, err := s.prepareMediaProcessingJob(moment, rawKey)
	if err != nil {
		slog.Error("failed to prepare media job", "momentId", moment.ID, "error", err)
		s.failMediaProcessingJob(moment, err)
		return false
	}
	msg := dtos.NewMediaProcessMessage(
		moment.ID.String(),
		moment.EventID.String(),
		rawKey,
		bucket,
		contentType,
		detectMediaIsVideo(contentType, rawKey),
	)
	msg.JobID = jobID
	msg.Generation = generation
	enqueued, err := s.mediaPublisher.PublishMediaJob(msg)
	if err != nil {
		slog.Error("failed to queue media job", "momentId", moment.ID, "error", err)
		s.failMediaProcessingJob(moment, err)
		return false
	}
	if !enqueued {
		s.failMediaProcessingJob(moment, errors.New("media publisher did not confirm enqueue"))
		return false
	}
	return enqueued
}

// failMediaProcessingJob moves only the currently-authorized pending job to a
// terminal failed state. Raw media stays private and can be explicitly
// requeued; it must never be exposed as a legacy-ready moment just because SQS
// was temporarily unavailable.
func (s *MomentService) failMediaProcessingJob(moment *models.Moment, cause error) {
	if moment == nil || moment.EventID == nil {
		return
	}
	const publicFailure = "media processing queue unavailable"
	if processingRepo, ok := s.repo.(ports.MomentProcessingRepository); ok {
		applied, err := processingRepo.ApplyMediaProcessingUpdate(
			moment.ID, *moment.EventID, moment.ProcessingJobID, moment.ProcessingGeneration,
			[]string{"pending"}, moment.ContentURL, "failed", "", publicFailure, 0, 0, 0,
		)
		if err != nil || !applied {
			slog.Error("failed to persist media enqueue failure", "momentId", moment.ID, "cause", cause, "error", err, "applied", applied)
			return
		}
	} else if err := s.repo.UpdateMomentContent(moment.ID, moment.ContentURL, "failed", "", publicFailure, 0, 0, 0); err != nil {
		slog.Error("failed to persist legacy media enqueue failure", "momentId", moment.ID, "cause", cause, "error", err)
		return
	}
	moment.ProcessingStatus = "failed"
	moment.ErrorMessage = publicFailure
}

// restoreReoptimizationJob keeps the last valid optimized asset visible when
// a best-effort reoptimization publish fails. The CAS prevents compensation
// from overwriting a worker callback if SQS accepted the message but the
// publisher lost the acknowledgement.
func (s *MomentService) restoreReoptimizationJob(moment *models.Moment, status, errorMessage string, cause error) {
	if moment == nil || moment.EventID == nil {
		return
	}
	if processingRepo, ok := s.repo.(ports.MomentProcessingRepository); ok {
		applied, err := processingRepo.ApplyMediaProcessingUpdate(
			moment.ID, *moment.EventID, moment.ProcessingJobID, moment.ProcessingGeneration,
			[]string{"pending"}, moment.ContentURL, status, "", errorMessage, 0, 0, 0,
		)
		if err != nil || !applied {
			slog.Error("failed to restore reoptimization state", "momentId", moment.ID, "cause", cause, "error", err, "applied", applied)
			return
		}
	} else if err := s.repo.UpdateMomentContent(moment.ID, moment.ContentURL, status, "", errorMessage, 0, 0, 0); err != nil {
		slog.Error("failed to restore legacy reoptimization state", "momentId", moment.ID, "cause", cause, "error", err)
		return
	}
	moment.ProcessingStatus = status
	moment.ErrorMessage = errorMessage
}

func (s *MomentService) prepareMediaProcessingJob(moment *models.Moment, inputKey string) (string, int64, error) {
	processingRepo, ok := s.repo.(ports.MomentProcessingRepository)
	if !ok {
		return moment.ProcessingJobID, moment.ProcessingGeneration, nil
	}
	jobUUID, err := uuid.NewV4()
	if err != nil {
		return "", 0, err
	}
	jobID := jobUUID.String()
	generation, err := processingRepo.BeginMediaProcessingJob(moment.ID, *moment.EventID, inputKey, jobID)
	if err != nil {
		return "", 0, err
	}
	moment.ProcessingJobID = jobID
	moment.ProcessingGeneration = generation
	moment.ProcessingInputKey = inputKey
	moment.ProcessingStatus = "pending"
	moment.ErrorMessage = ""
	return jobID, generation, nil
}

// RequeueMoment resets processing_status to pending and republishes the media job.
func (s *MomentService) RequeueMoment(moment *models.Moment) error {
	if moment.EventID == nil {
		return fmt.Errorf("cannot requeue: moment has no EventID")
	}
	bucket := mediaBucketName()
	objectKey := awsrepository.S3KeyFromURL(moment.ContentURL, bucket)
	if utils.IsAbsoluteURLLike(objectKey) {
		return fmt.Errorf("cannot requeue: content URL is not a trusted CDN or S3 object")
	}
	if !strings.Contains(objectKey, "/raw/") {
		return fmt.Errorf("cannot requeue: optimized file already processed, raw key not available")
	}
	if s.mediaPublisher == nil {
		return fmt.Errorf("cannot requeue: media publisher is not configured")
	}

	contentType := moment.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(objectKey))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}

	if _, supportsCAS := s.repo.(ports.MomentProcessingRepository); !supportsCAS {
		if err := s.repo.UpdateMomentContent(moment.ID, moment.ContentURL, "pending", "", "", 0, 0, 0); err != nil {
			return err
		}
		moment.ProcessingStatus = "pending"
		moment.ErrorMessage = ""
	}
	jobID, generation, err := s.prepareMediaProcessingJob(moment, objectKey)
	if err != nil {
		return fmt.Errorf("requeue job preparation failed: %w", err)
	}
	msg := dtos.NewMediaProcessMessage(
		moment.ID.String(),
		moment.EventID.String(),
		objectKey,
		bucket,
		contentType,
		detectMediaIsVideo(contentType, objectKey),
	)
	msg.JobID = jobID
	msg.Generation = generation
	enqueued, publishErr := s.mediaPublisher.PublishMediaJob(msg)
	if publishErr != nil || !enqueued {
		if publishErr == nil {
			publishErr = errors.New("media publisher did not confirm enqueue")
		}
		s.failMediaProcessingJob(moment, publishErr)
		return fmt.Errorf("requeue SQS publish failed: %w", publishErr)
	}

	s.invalidateWallCache(*moment.EventID)
	return s.invalidateMomentsCache()
}

func (s *MomentService) ListInFlight(eventID uuid.UUID) ([]models.Moment, error) {
	return s.repo.ListProcessingByEventID(eventID, true)
}

func (s *MomentService) ListReoptimizing(eventID uuid.UUID) ([]models.Moment, error) {
	return s.repo.ListProcessingByEventID(eventID, false)
}

func (s *MomentService) BulkUpdateOrder(updates map[uuid.UUID]int) error {
	if len(updates) == 0 {
		return nil
	}
	eventIDs, _ := s.repo.GetDistinctEventIDsByMomentIDs(keysFromOrderUpdates(updates))
	if err := s.repo.BulkUpdateOrder(updates); err != nil {
		return err
	}
	for _, eventID := range eventIDs {
		s.invalidateWallCache(eventID)
	}
	return s.invalidateMomentsCache()
}

func (s *MomentService) BatchReoptimize(ids []uuid.UUID) (succeeded, skipped, failed int, err error) {
	if len(ids) == 0 {
		return 0, 0, 0, nil
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
		if len(unique) == 200 {
			break
		}
	}

	moments, err := s.repo.GetMomentsByIDs(unique)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, moment := range moments {
		if moment.EventID == nil || (moment.ProcessingStatus != "done" && moment.ProcessingStatus != "failed") {
			skipped++
			continue
		}
		bucket := mediaBucketName()
		objectKey := awsrepository.S3KeyFromURL(moment.ContentURL, bucket)
		if objectKey == "" || utils.IsAbsoluteURLLike(objectKey) || strings.Contains(objectKey, "/raw/") {
			skipped++
			continue
		}
		contentType := moment.ContentType
		if contentType == "" {
			contentType = mime.TypeByExtension(filepath.Ext(objectKey))
			if contentType == "" {
				contentType = "application/octet-stream"
			}
		}
		originalStatus := moment.ProcessingStatus
		originalError := moment.ErrorMessage
		if s.mediaPublisher == nil {
			failed++
			continue
		}
		if _, supportsCAS := s.repo.(ports.MomentProcessingRepository); !supportsCAS {
			if err := s.repo.UpdateMomentContent(moment.ID, moment.ContentURL, "pending", "", "", 0, 0, 0); err != nil {
				failed++
				slog.Error("failed to mark moment for reoptimization", "momentId", moment.ID, "error", err)
				continue
			}
			moment.ProcessingStatus = "pending"
			moment.ErrorMessage = ""
		}
		jobID, generation, prepareErr := s.prepareMediaProcessingJob(&moment, objectKey)
		if prepareErr != nil {
			failed++
			s.restoreReoptimizationJob(&moment, originalStatus, originalError, prepareErr)
			slog.Error("failed to prepare reoptimization job", "momentId", moment.ID, "error", prepareErr)
			continue
		}
		msg := dtos.NewMediaProcessMessage(
			moment.ID.String(),
			moment.EventID.String(),
			objectKey,
			bucket,
			contentType,
			detectMediaIsVideo(contentType, objectKey),
		)
		msg.JobID = jobID
		msg.Generation = generation
		enqueued, publishErr := s.mediaPublisher.PublishMediaJob(msg)
		if publishErr != nil || !enqueued {
			if publishErr == nil {
				publishErr = errors.New("media publisher did not confirm enqueue")
			}
			failed++
			s.restoreReoptimizationJob(&moment, originalStatus, originalError, publishErr)
			slog.Error("failed to publish reoptimization job", "momentId", moment.ID, "error", publishErr)
			continue
		}
		s.invalidateWallCache(*moment.EventID)
		succeeded++
	}
	_ = s.invalidateMomentsCache()
	return succeeded, skipped, failed, nil
}

func keysFromOrderUpdates(updates map[uuid.UUID]int) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(updates))
	for id := range updates {
		ids = append(ids, id)
	}
	return ids
}

func mediaBucketName() string {
	if bucket := os.Getenv("AWS_BUCKET_NAME"); bucket != "" {
		return bucket
	}
	return os.Getenv("S3_BUCKET_NAME")
}

// BulkUpdateApproval updates is_approved for multiple moments by ID.
func (s *MomentService) BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	eventIDs, _ := s.repo.GetDistinctEventIDsByMomentIDs(ids)

	if err := s.repo.BulkUpdateApproval(ids, isApproved); err != nil {
		return err
	}

	for _, eventID := range eventIDs {
		s.invalidateWallCache(eventID)
	}
	return s.invalidateMomentsCache()
}
