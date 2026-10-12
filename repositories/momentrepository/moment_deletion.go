package momentrepository

import (
	"events-stocks/configuration"
	"events-stocks/models"

	"github.com/gofrs/uuid"
)

// GetMomentByIDUnscoped reads a moment including a soft-deleted row, so a
// late media callback can tell a deleted moment from a missing one.
func GetMomentByIDUnscoped(id uuid.UUID) (*models.Moment, error) {
	var model models.Moment
	err := configuration.DB.Unscoped().Where("id = ?", id).First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// LiveMomentKeyReferences reports which keys a live (not soft-deleted) moment
// of the event, other than excludeIDs, still uses as content, raw input or
// thumbnail.
func LiveMomentKeyReferences(eventID uuid.UUID, excludeIDs []uuid.UUID, keys []string) (map[string]bool, error) {
	referenced := make(map[string]bool)
	if len(keys) == 0 {
		return referenced, nil
	}
	type row struct {
		ContentURL         string
		ProcessingInputKey string
		ThumbnailURL       string
	}
	var rows []row
	query := configuration.DB.Model(&models.Moment{}).
		Select("content_url, processing_input_key, thumbnail_url").
		Where("event_id = ?", eventID).
		Where("content_url IN ? OR processing_input_key IN ? OR thumbnail_url IN ?", keys, keys, keys)
	if len(excludeIDs) > 0 {
		query = query.Where("id NOT IN ?", excludeIDs)
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	for _, r := range rows {
		for _, value := range []string{r.ContentURL, r.ProcessingInputKey, r.ThumbnailURL} {
			if _, ok := wanted[value]; ok {
				referenced[value] = true
			}
		}
	}
	return referenced, nil
}

func (r *MomentRepo) GetMomentByIDUnscoped(id uuid.UUID) (*models.Moment, error) {
	return GetMomentByIDUnscoped(id)
}

func (r *MomentRepo) LiveMomentKeyReferences(eventID uuid.UUID, excludeIDs []uuid.UUID, keys []string) (map[string]bool, error) {
	return LiveMomentKeyReferences(eventID, excludeIDs, keys)
}
