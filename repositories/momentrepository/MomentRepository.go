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
// Pass durationMs=0 to skip writing metrics (e.g. for "processing" status transitions).
func UpdateMomentContent(id uuid.UUID, contentURL, processingStatus string, durationMs, originalBytes, optimizedBytes int64) error {
	updates := map[string]interface{}{
		"content_url":       contentURL,
		"processing_status": processingStatus,
	}
	if durationMs > 0 {
		updates["processing_duration_ms"] = durationMs
		updates["original_size_bytes"]    = originalBytes
		updates["optimized_size_bytes"]   = optimizedBytes
	}
	return configuration.DB.Model(&models.Moment{}).Where("id = ?", id).Updates(updates).Error
}

func (r *MomentRepo) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus string, durationMs, originalBytes, optimizedBytes int64) error {
	return UpdateMomentContent(id, contentURL, processingStatus, durationMs, originalBytes, optimizedBytes)
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
func ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) {
	var list []models.Moment
	var total int64

	offset := (page - 1) * limit
	query := configuration.DB.Model(&models.Moment{}).
		Where("event_id = ?", eventID).
		Where("is_approved = ?", true).
		Where("processing_status IN ?", []string{"", "done"})

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&list).Error
	return list, total, err
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
