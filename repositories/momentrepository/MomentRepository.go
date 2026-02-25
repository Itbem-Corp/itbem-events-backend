package momentrepository

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
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

func GetMomentByID(id uuid.UUID) (*models.Moment, error) {
    var model models.Moment
    err := gormrepository.GetByID(&model, id)
    return &model, err
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
func (r *MomentRepo) GetMomentByID(id uuid.UUID) (*models.Moment, error) { return GetMomentByID(id) }
func (r *MomentRepo) ListMoments() ([]models.Moment, error)              { return ListMoments() }

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
// Pass durationMs=0 to skip writing metrics (e.g. for "processing" status transitions).
func UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	updates := map[string]interface{}{
		"content_url":       contentURL,
		"processing_status": processingStatus,
		// error_message is always written — including "" to clear previous errors on retry.
		// This uses map[string]interface{} intentionally; GORM struct updates would suppress
		// the empty string as a zero value and silently skip the clear.
		"error_message": errorMessage,
	}
	if thumbnailURL != "" {
		updates["thumbnail_url"] = thumbnailURL
	}
	if durationMs > 0 {
		updates["processing_duration_ms"] = durationMs
		updates["original_size_bytes"]    = originalBytes
		updates["optimized_size_bytes"]   = optimizedBytes
	}
	return configuration.DB.Model(&models.Moment{}).Where("id = ?", id).Updates(updates).Error
}

func (r *MomentRepo) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	return UpdateMomentContent(id, contentURL, processingStatus, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes)
}

// ListForDashboard returns moments ready for admin review.
// Hides moments still being optimized by Lambda (pending/processing).
// Shows: '' (legacy direct-upload), 'done', 'failed'.
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

// ListApprovedForWall returns approved + fully optimized moments for the public wall, paginated.
// Only shows: is_approved=true AND processing_status IN ('', 'done').
// Uses a single SQL query with COUNT(*) OVER() window function to avoid a separate COUNT query.
func ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	type row struct {
		models.Moment
		TotalCount int64 `gorm:"column:total_count"`
	}

	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit
	var rows []row

	// Raw query: GORM soft-delete scope does NOT apply to Raw().
	// deleted_at IS NULL must be explicit here (unlike the ORM fallback below).
	err := configuration.DB.Raw(`
		SELECT m.*, COUNT(*) OVER() AS total_count
		FROM moments m
		WHERE m.event_id = ?
		  AND m.is_approved = true
		  AND m.processing_status IN ('', 'done')
		  AND m.deleted_at IS NULL
		ORDER BY m.created_at DESC
		LIMIT ? OFFSET ?
	`, eventID, limit, offset).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	if len(rows) == 0 {
		// Page is empty — need total for pagination (e.g. page 2 beyond last item).
		// Fall back to a simple count only on empty pages. GORM soft-delete scope
		// applies automatically here (deleted_at IS NULL injected by ORM).
		var total int64
		if err := configuration.DB.Model(&models.Moment{}).
			Where("event_id = ? AND is_approved = ? AND processing_status IN ? AND deleted_at IS NULL",
				eventID, true, []string{"", "done"}).
			Count(&total).Error; err != nil {
			return nil, 0, err
		}
		return nil, total, nil
	}

	moments := make([]models.Moment, len(rows))
	for i, r := range rows {
		moments[i] = r.Moment
	}
	return moments, rows[0].TotalCount, nil
}

func (r *MomentRepo) ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	return ListApprovedForWall(eventID, page, limit)
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
