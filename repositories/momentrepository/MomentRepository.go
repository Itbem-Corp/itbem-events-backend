package momentrepository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/outboxrepository"
	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

func CreateMoment(m *models.Moment) error {
	return gormrepository.Insert(m)
}

func UpdateMoment(m *models.Moment) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteMoment(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.Moment{})
}

func BulkDeleteMoments(ids []uuid.UUID) error {
	return configuration.DB.Where("id IN ?", ids).Delete(&models.Moment{}).Error
}

func GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	var model models.Moment
	err := gormrepository.GetByID(&model, id)
	return &model, err
}

func GetMomentByEventIDAndContentURL(eventID uuid.UUID, contentURL string) (*models.Moment, error) {
	var model models.Moment
	err := momentByEventIDAndContentURLQuery(configuration.DB, eventID, contentURL).First(&model).Error
	return &model, err
}

func momentByEventIDAndContentURLQuery(db *gorm.DB, eventID uuid.UUID, contentURL string) *gorm.DB {
	return db.Where("event_id = ? AND content_url = ?", eventID, contentURL)
}

func ListMoments() ([]models.Moment, error) {
	var list []models.Moment
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}

// MomentRepo implements ports.MomentRepository.
type MomentRepo struct{}

func NewMomentRepo() *MomentRepo { return &MomentRepo{} }

func (r *MomentRepo) CreateMoment(m *models.Moment) error                { return CreateMoment(m) }
func (r *MomentRepo) UpdateMoment(m *models.Moment) error                { return UpdateMoment(m) }
func (r *MomentRepo) DeleteMoment(id uuid.UUID) error                    { return DeleteMoment(id) }
func (r *MomentRepo) BulkDeleteMoments(ids []uuid.UUID) error            { return BulkDeleteMoments(ids) }
func (r *MomentRepo) GetMomentByID(id uuid.UUID) (*models.Moment, error) { return GetMomentByID(id) }
func (r *MomentRepo) GetMomentByEventIDAndContentURL(eventID uuid.UUID, contentURL string) (*models.Moment, error) {
	return GetMomentByEventIDAndContentURL(eventID, contentURL)
}
func (r *MomentRepo) ListMoments() ([]models.Moment, error) { return ListMoments() }

// ListMomentsByEventID returns moments for a specific event, optionally filtering to approved only.
func ListMomentsByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) {
	var list []models.Moment
	query := configuration.DB.Where("event_id = ?", eventID)
	if approvedOnly {
		query = query.Where("is_approved = ?", true)
	}
	err := query.Order("created_at DESC").Find(&list).Error
	return list, err
}

func (r *MomentRepo) ListByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) {
	return ListMomentsByEventID(eventID, approvedOnly)
}

// UpdateMomentContent updates ContentURL, ProcessingStatus, and optional Lambda metrics.
// Pass thumbnailURL="" to skip writing it (e.g. for images or "processing" transitions).
// Pass errorMessage="" to clear any previous processing error.
// Pass durationMs=0 to skip writing metrics (e.g. for "processing" status transitions).
func UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	updates := map[string]interface{}{
		"content_url":       contentURL,
		"processing_status": processingStatus,
		"error_message":     errorMessage,
	}
	if thumbnailURL != "" {
		updates["thumbnail_url"] = thumbnailURL
	}
	if durationMs > 0 {
		updates["processing_duration_ms"] = durationMs
		updates["original_size_bytes"] = originalBytes
		updates["optimized_size_bytes"] = optimizedBytes
	}
	return configuration.DB.Model(&models.Moment{}).Where("id = ?", id).Updates(updates).Error
}

func (r *MomentRepo) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	return UpdateMomentContent(id, contentURL, processingStatus, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes)
}

// BeginMediaProcessingJob atomically increments the generation and installs a
// new job identity. The compare-and-swap prevents concurrent requeues from
// publishing two jobs with the same generation.
func (r *MomentRepo) BeginMediaProcessingJob(id, eventID uuid.UUID, inputKey, jobID string) (int64, error) {
	return beginMediaProcessingJob(configuration.DB, id, eventID, inputKey, jobID)
}

// BeginMediaProcessingJobWithOutbox keeps the local state transition and the
// Lambda handoff in one transaction. If either half fails, neither a pending
// generation nor a durable message is committed.
func (r *MomentRepo) BeginMediaProcessingJobWithOutbox(id, eventID uuid.UUID, inputKey, jobID string, message dtos.MediaProcessMessage) (int64, error) {
	var generation int64
	err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		generation, err = beginMediaProcessingJob(tx, id, eventID, inputKey, jobID)
		if err != nil {
			return err
		}
		message.Generation = generation
		body, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("marshal durable media job: %w", err)
		}
		inserted, err := outboxrepository.Enqueue(tx, &models.OutboxEvent{
			EventType:     dtos.MediaProcessEventType,
			DedupeKey:     strings.TrimSpace(message.JobID),
			TenantCode:    strings.TrimSpace(message.Application),
			CorrelationID: strings.TrimSpace(message.CorrelationID),
			Payload:       string(body),
			State:         outboxrepository.StatePending,
			AvailableAt:   time.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("enqueue durable media job: %w", err)
		}
		if !inserted {
			return fmt.Errorf("durable media job %s already exists", message.JobID)
		}
		return nil
	})
	return generation, err
}

func beginMediaProcessingJob(db *gorm.DB, id, eventID uuid.UUID, inputKey, jobID string) (int64, error) {
	for attempt := 0; attempt < 5; attempt++ {
		var current models.Moment
		if err := db.
			Select("id", "processing_generation").
			Where("id = ? AND event_id = ?", id, eventID).
			First(&current).Error; err != nil {
			return 0, err
		}

		nextGeneration := current.ProcessingGeneration + 1
		result := db.Model(&models.Moment{}).
			Where("id = ? AND event_id = ? AND processing_generation = ?", id, eventID, current.ProcessingGeneration).
			Updates(map[string]interface{}{
				"processing_generation": nextGeneration,
				"processing_job_id":     jobID,
				"processing_input_key":  inputKey,
				"processing_status":     "pending",
				"error_message":         "",
			})
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected == 1 {
			return nextGeneration, nil
		}
	}
	return 0, errors.New("media processing generation changed concurrently")
}

// ApplyMediaProcessingUpdate performs the final state transition as a CAS over
// moment, event, generation, job and current status. A false result means the
// callback lost a race or belongs to a stale delivery; no fields were changed.
func (r *MomentRepo) ApplyMediaProcessingUpdate(
	id, eventID uuid.UUID,
	jobID string,
	generation int64,
	allowedCurrentStatuses []string,
	contentURL, processingStatus, thumbnailURL, errorMessage string,
	durationMs, originalBytes, optimizedBytes int64,
	mediaVariants models.MediaVariants,
) (bool, error) {
	updates := map[string]interface{}{
		"content_url":       contentURL,
		"processing_status": processingStatus,
		"error_message":     errorMessage,
	}
	if thumbnailURL != "" {
		updates["thumbnail_url"] = thumbnailURL
	}
	if durationMs > 0 {
		updates["processing_duration_ms"] = durationMs
		updates["original_size_bytes"] = originalBytes
		updates["optimized_size_bytes"] = optimizedBytes
	}
	if processingStatus == "done" {
		updates["media_variants"] = mediaVariants
	}

	query := configuration.DB.Model(&models.Moment{}).
		Where("id = ? AND event_id = ?", id, eventID).
		Where("processing_generation = ? AND processing_job_id = ?", generation, jobID).
		Where("processing_status IN ?", allowedCurrentStatuses)
	result := query.Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// ListForDashboard returns moments ready for admin review.
// Hides moments still being optimized by Lambda (pending/processing).
// Shows: ” (legacy direct-upload), 'done', 'failed'.
func ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	var list []models.Moment
	err := configuration.DB.
		Where("event_id = ?", eventID).
		Where("processing_status NOT IN ?", []string{"pending", "processing"}).
		Order("created_at DESC").
		Find(&list).Error
	return list, err
}

func (r *MomentRepo) ListForDashboard(eventID uuid.UUID) ([]models.Moment, error) {
	return ListForDashboard(eventID)
}

func ListForDashboardPage(eventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error) {
	baseQuery := func() *gorm.DB {
		return configuration.DB.Model(&models.Moment{}).
			Where("event_id = ?", eventID).
			Where("processing_status NOT IN ?", []string{"pending", "processing"})
	}

	var counts dtos.MomentDashboardCounts
	var list []models.Moment
	group := new(errgroup.Group)
	group.Go(func() error {
		return baseQuery().Select(`
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE NOT is_approved AND processing_status <> 'failed') AS pending,
			COUNT(*) FILTER (WHERE is_approved) AS approved,
			COUNT(*) FILTER (WHERE processing_status = 'failed') AS failed,
			COUNT(*) FILTER (WHERE is_approved AND COALESCE(content_type, '') NOT LIKE 'video/%' AND COALESCE(content_url, '') <> '') AS photos,
			COUNT(*) FILTER (WHERE is_approved AND COALESCE(content_type, '') LIKE 'video/%') AS videos,
			COUNT(*) FILTER (WHERE BTRIM(COALESCE(description, '')) <> '') AS notes,
			COUNT(*) FILTER (WHERE COALESCE(processing_status, '') = '') AS legacy
		`).Scan(&counts).Error
	})
	group.Go(func() error {
		return baseQuery().Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	})
	if err := group.Wait(); err != nil {
		return nil, counts, err
	}
	return list, counts, nil
}

func (r *MomentRepo) ListForDashboardPage(eventID uuid.UUID, page, pageSize int) ([]models.Moment, dtos.MomentDashboardCounts, error) {
	return ListForDashboardPage(eventID, page, pageSize)
}

func ListPendingSummaryByEventIDs(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error) {
	if len(eventIDs) == 0 {
		return []dtos.MomentSummary{}, nil
	}
	var summaries []dtos.MomentSummary
	err := configuration.DB.Model(&models.Moment{}).
		Select("event_id, COUNT(*) AS pending_count").
		Where("event_id IN ?", eventIDs).
		Where("is_approved = ?", false).
		Where("processing_status NOT IN ?", []string{"pending", "processing"}).
		Group("event_id").
		Scan(&summaries).Error
	return summaries, err
}

func (r *MomentRepo) ListPendingSummaryByEventIDs(eventIDs []uuid.UUID) ([]dtos.MomentSummary, error) {
	return ListPendingSummaryByEventIDs(eventIDs)
}

// ListApprovedForWall returns approved + fully optimized moments for the public wall, paginated.
// Only shows: is_approved=true AND processing_status IN (”, 'done').
func ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	offset := (page - 1) * limit
	return loadPublicWallPage(
		func(ctx context.Context) (int64, error) {
			var total int64
			err := approvedForWallBaseQuery(eventID).WithContext(ctx).Count(&total).Error
			return total, err
		},
		func(ctx context.Context) ([]models.Moment, error) {
			var list []models.Moment
			err := orderApprovedForWallQuery(approvedForWallBaseQuery(eventID).WithContext(ctx)).
				Offset(offset).
				Limit(limit).
				Find(&list).Error
			return list, err
		},
	)
}

func (r *MomentRepo) ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	return ListApprovedForWall(eventID, page, limit)
}

func LatestPublicMomentUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error) {
	var latest sql.NullTime
	err := latestPublicMomentUpdatedAtQuery(configuration.DB, eventID).
		Scan(&latest).Error
	if err != nil {
		return nil, err
	}
	if !latest.Valid {
		return nil, nil
	}
	return &latest.Time, nil
}

func latestPublicMomentUpdatedAtQuery(db *gorm.DB, eventID uuid.UUID) *gorm.DB {
	return applyPublicWallVisibleMomentFilters(
		db.
			Table("moments").
			Select(`
			MAX(
				CASE
					WHEN deleted_at IS NOT NULL AND deleted_at > updated_at AND deleted_at > created_at THEN deleted_at
					WHEN updated_at > created_at THEN updated_at
					ELSE created_at
				END
			)
		`),
		eventID,
	)
}

func (r *MomentRepo) LatestPublicMomentUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error) {
	return LatestPublicMomentUpdatedAtByEventID(eventID)
}

// BulkUpdateApproval updates is_approved for multiple moments in a single query.
func BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	return configuration.DB.Model(&models.Moment{}).
		Where("id IN ?", ids).
		Update("is_approved", isApproved).Error
}

func (r *MomentRepo) BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error {
	return BulkUpdateApproval(ids, isApproved)
}

// GetDistinctEventIDsByMomentIDs returns the unique event_id values for the given moment IDs.
func GetDistinctEventIDsByMomentIDs(ids []uuid.UUID) ([]uuid.UUID, error) {
	var eventIDs []uuid.UUID
	err := configuration.DB.Model(&models.Moment{}).
		Where("id IN ? AND event_id IS NOT NULL", ids).
		Distinct("event_id").
		Pluck("event_id", &eventIDs).Error
	return eventIDs, err
}

func (r *MomentRepo) GetDistinctEventIDsByMomentIDs(ids []uuid.UUID) ([]uuid.UUID, error) {
	return GetDistinctEventIDsByMomentIDs(ids)
}

func GetMomentsByIDs(ids []uuid.UUID) ([]models.Moment, error) {
	var list []models.Moment
	err := configuration.DB.
		Where("id IN ?", ids).
		Find(&list).Error
	return list, err
}

func (r *MomentRepo) GetMomentsByIDs(ids []uuid.UUID) ([]models.Moment, error) {
	return GetMomentsByIDs(ids)
}

const bulkUpdateOrderSQL = `
	UPDATE moments AS moment
	SET "order" = requested.new_order,
		updated_at = CURRENT_TIMESTAMP
	FROM jsonb_to_recordset(?::jsonb) AS requested(id uuid, new_order bigint)
	WHERE moment.id = requested.id
		AND moment.deleted_at IS NULL
`

type momentOrderUpdate struct {
	ID       string `json:"id"`
	NewOrder int    `json:"new_order"`
}

func encodeMomentOrderUpdates(updates map[uuid.UUID]int) ([]byte, error) {
	ordered := make([]momentOrderUpdate, 0, len(updates))
	for id, order := range updates {
		ordered = append(ordered, momentOrderUpdate{
			ID:       id.String(),
			NewOrder: order,
		})
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})
	return json.Marshal(ordered)
}

func bulkUpdateOrder(db *gorm.DB, updates map[uuid.UUID]int) error {
	payload, err := encodeMomentOrderUpdates(updates)
	if err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if len(updates) == 0 {
			return nil
		}
		return tx.Exec(bulkUpdateOrderSQL, string(payload)).Error
	})
}

func BulkUpdateOrder(updates map[uuid.UUID]int) error {
	return bulkUpdateOrder(configuration.DB, updates)
}

func (r *MomentRepo) BulkUpdateOrder(updates map[uuid.UUID]int) error {
	return BulkUpdateOrder(updates)
}

func ListProcessingByEventID(eventID uuid.UUID, rawOnly bool) ([]models.Moment, error) {
	var list []models.Moment
	query := configuration.DB.
		Where("event_id = ?", eventID).
		Where("processing_status IN ?", []string{"pending", "processing"})
	if rawOnly {
		query = query.Where("content_url LIKE ?", "%/raw/%")
	} else {
		query = query.Where("content_url NOT LIKE ?", "%/raw/%")
	}
	err := query.Order("updated_at DESC").Find(&list).Error
	return list, err
}

func (r *MomentRepo) ListProcessingByEventID(eventID uuid.UUID, rawOnly bool) ([]models.Moment, error) {
	return ListProcessingByEventID(eventID, rawOnly)
}

func approvedForWallBaseQuery(eventID uuid.UUID) *gorm.DB {
	return applyPublicWallVisibleMomentFilters(configuration.DB.Model(&models.Moment{}), eventID)
}

func applyPublicWallVisibleMomentFilters(query *gorm.DB, eventID uuid.UUID) *gorm.DB {
	return query.
		Where("event_id = ?", eventID).
		// These values are invariants, not request input. Keep them literal so
		// PostgreSQL can prove that a generic prepared plan implies the partial
		// public-wall index predicate. Binding true/""/"done" prevents that
		// proof when plan_cache_mode selects a generic plan.
		Where(publicWallVisibleStatePredicate)
}

const publicWallVisibleStatePredicate = `is_approved = TRUE AND processing_status IN ('', 'done')`

func publicWallOrderClauses() []string {
	return []string{
		publicWallOrderGroupExpr + ` ASC`,
		`"order" ASC`,
		"created_at DESC",
		"id DESC",
	}
}

const publicWallOrderGroupExpr = `CASE WHEN "order" > 0 THEN 0 ELSE 1 END`

func orderApprovedForWallQuery(query *gorm.DB) *gorm.DB {
	for _, clause := range publicWallOrderClauses() {
		query = query.Order(clause)
	}
	return query
}

func publicWallCursorWhereClause() string {
	return `(
		(` + publicWallOrderGroupExpr + `) > ?
		OR ((` + publicWallOrderGroupExpr + `) = ? AND "order" > ?)
		OR ((` + publicWallOrderGroupExpr + `) = ? AND "order" = ? AND created_at < ?)
		OR ((` + publicWallOrderGroupExpr + `) = ? AND "order" = ? AND created_at = ? AND id < ?::uuid)
	)`
}

func applyApprovedForWallCursor(query *gorm.DB, afterCreatedAt *time.Time, afterID string, afterOrder *int) *gorm.DB {
	if afterCreatedAt == nil || afterID == "" {
		return query
	}
	if afterOrder == nil {
		return query.Where("(created_at < ? OR (created_at = ? AND id < ?::uuid))", *afterCreatedAt, *afterCreatedAt, afterID)
	}
	orderGroup := 1
	if *afterOrder > 0 {
		orderGroup = 0
	}
	return query.Where(
		publicWallCursorWhereClause(),
		orderGroup,
		orderGroup, *afterOrder,
		orderGroup, *afterOrder, *afterCreatedAt,
		orderGroup, *afterOrder, *afterCreatedAt, afterID,
	)
}

// loadPublicWallPage overlaps the independent total-count and item queries.
// The fan-out is fixed at two goroutines, regardless of page size, and the
// count error keeps the same priority as the former sequential implementation.
func loadPublicWallPage(
	loadTotal func(context.Context) (int64, error),
	loadItems func(context.Context) ([]models.Moment, error),
) ([]models.Moment, int64, error) {
	type countResult struct {
		total      int64
		err        error
		panicValue any
	}
	type itemsResult struct {
		items      []models.Moment
		err        error
		panicValue any
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		cancelOnce   sync.Once
		firstFailure string
	)
	markFailure := func(worker string) {
		cancelOnce.Do(func() {
			firstFailure = worker
			cancel()
		})
	}

	// Buffers let both workers publish their terminal result while the caller
	// joins them. A failure cancels the sibling query through the shared
	// context, but no worker is abandoned before returning or re-panicking.
	countResults := make(chan countResult, 1)
	itemsResults := make(chan itemsResult, 1)
	go func() {
		result := countResult{}
		defer func() {
			if panicValue := recover(); panicValue != nil {
				result.panicValue = panicValue
			}
			if result.err != nil || result.panicValue != nil {
				markFailure("count")
			}
			countResults <- result
		}()
		result.total, result.err = loadTotal(ctx)
	}()
	go func() {
		result := itemsResult{}
		defer func() {
			if panicValue := recover(); panicValue != nil {
				result.panicValue = panicValue
			}
			if result.err != nil || result.panicValue != nil {
				markFailure("items")
			}
			itemsResults <- result
		}()
		result.items, result.err = loadItems(ctx)
	}()

	count := <-countResults
	items := <-itemsResults
	cancel()

	// Re-panic on the request goroutine so Echo's recovery middleware can
	// contain loader panics instead of letting a worker crash the process.
	if count.panicValue != nil {
		panic(count.panicValue)
	}
	// An items failure may cancel an otherwise healthy count query. Do not let
	// that derived context.Canceled mask the originating items error.
	countCanceledByItems := firstFailure == "items" && errors.Is(count.err, context.Canceled)
	if count.err != nil && !countCanceledByItems {
		return nil, 0, count.err
	}
	if items.panicValue != nil {
		panic(items.panicValue)
	}
	if items.err != nil {
		return items.items, count.total, items.err
	}
	if count.err != nil {
		return nil, 0, count.err
	}
	return items.items, count.total, items.err
}

func ListApprovedForWallCursor(eventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error) {
	return loadPublicWallPage(
		func(ctx context.Context) (int64, error) {
			var total int64
			err := approvedForWallBaseQuery(eventID).WithContext(ctx).Count(&total).Error
			return total, err
		},
		func(ctx context.Context) ([]models.Moment, error) {
			var list []models.Moment
			query := applyApprovedForWallCursor(approvedForWallBaseQuery(eventID).WithContext(ctx), afterCreatedAt, afterID, afterOrder)
			err := orderApprovedForWallQuery(query).
				Limit(limit).
				Find(&list).Error
			return list, err
		},
	)
}

func (r *MomentRepo) ListApprovedForWallCursor(eventID uuid.UUID, afterCreatedAt *time.Time, afterID string, afterOrder *int, limit int) ([]models.Moment, int64, error) {
	return ListApprovedForWallCursor(eventID, afterCreatedAt, afterID, afterOrder, limit)
}
