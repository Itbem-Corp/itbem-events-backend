package resourcerepository

import (
	"database/sql"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

func CreateResource(resource *models.Resource) error {
	return gormrepository.Insert(resource)
}

func UpdateResource(resource *models.Resource) error {
	return gormrepository.Update(resource, resource.ID)
}

func TouchResourceUpdatedAt(id uuid.UUID, updatedAt time.Time) error {
	return gormrepository.UpdateFieldsByID(id, map[string]interface{}{
		"updated_at": updatedAt,
	}, &models.Resource{})
}

func DeleteResource(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.Resource{})
}

func DeleteResourceAndTouchSection(resourceID uuid.UUID, sectionID uuid.UUID, updatedAt time.Time) error {
	return gormrepository.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.EventSection{}).
			Where("id = ?", sectionID).
			Update("updated_at", updatedAt).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", resourceID).Delete(&models.Resource{}).Error
	})
}

func GetResourceByID(id uuid.UUID) (*models.Resource, error) {
	var resource models.Resource
	err := gormrepository.GetByID(&resource, id, "ResourceType")
	return &resource, err
}

func ListResourcesBySection(sectionID *uuid.UUID) ([]models.Resource, error) {
	var resources []models.Resource

	filters := map[string]interface{}{
		"event_section_id": sectionID,
	}

	err := gormrepository.GetList(&resources, gormrepository.QueryOptions{
		Filters:  filters,
		OrderBy:  "position",
		OrderDir: "asc",
	})
	return resources, err
}

func LatestResourceUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error) {
	var latest sql.NullTime
	err := gormrepository.DB().
		Table("resources").
		Select("MAX(CASE WHEN resources.updated_at > resources.created_at THEN resources.updated_at ELSE resources.created_at END)").
		Joins("JOIN event_sections ON event_sections.id = resources.event_section_id").
		Where("event_sections.event_id = ? AND event_sections.deleted_at IS NULL", eventID).
		Scan(&latest).Error
	if err != nil {
		return nil, err
	}
	if !latest.Valid {
		return nil, nil
	}
	return &latest.Time, nil
}

func LatestResourceUpdatedAtBySectionIDs(sectionIDs []uuid.UUID) (*time.Time, error) {
	if len(sectionIDs) == 0 {
		return nil, nil
	}
	var latest sql.NullTime
	err := gormrepository.DB().
		Table("resources").
		Select("MAX(CASE WHEN updated_at > created_at THEN updated_at ELSE created_at END)").
		Where("event_section_id IN ?", sectionIDs).
		Scan(&latest).Error
	if err != nil {
		return nil, err
	}
	if !latest.Valid {
		return nil, nil
	}
	return &latest.Time, nil
}

type ResourceRepo struct{}

func NewResourceRepo() *ResourceRepo { return &ResourceRepo{} }

func (r *ResourceRepo) CreateResource(resource *models.Resource) error {
	return CreateResource(resource)
}
func (r *ResourceRepo) UpdateResource(resource *models.Resource) error {
	return UpdateResource(resource)
}
func (r *ResourceRepo) TouchResourceUpdatedAt(id uuid.UUID, updatedAt time.Time) error {
	return TouchResourceUpdatedAt(id, updatedAt)
}
func (r *ResourceRepo) DeleteResource(id uuid.UUID) error {
	return DeleteResource(id)
}
func (r *ResourceRepo) DeleteResourceAndTouchSection(resourceID uuid.UUID, sectionID uuid.UUID, updatedAt time.Time) error {
	return DeleteResourceAndTouchSection(resourceID, sectionID, updatedAt)
}
func (r *ResourceRepo) GetResourceByID(id uuid.UUID) (*models.Resource, error) {
	return GetResourceByID(id)
}
func (r *ResourceRepo) ListResourcesBySection(sectionID *uuid.UUID) ([]models.Resource, error) {
	return ListResourcesBySection(sectionID)
}
func (r *ResourceRepo) ListResourceTypesRaw() ([]models.ResourceType, error) {
	return ListResourceTypesRaw()
}
func (r *ResourceRepo) LatestResourceUpdatedAtByEventID(eventID uuid.UUID) (*time.Time, error) {
	return LatestResourceUpdatedAtByEventID(eventID)
}
func (r *ResourceRepo) LatestResourceUpdatedAtBySectionIDs(sectionIDs []uuid.UUID) (*time.Time, error) {
	return LatestResourceUpdatedAtBySectionIDs(sectionIDs)
}
