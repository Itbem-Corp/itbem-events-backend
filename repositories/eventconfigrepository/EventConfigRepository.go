package eventconfigrepository

import (
	"errors"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

func CreateEventConfig(m *models.EventConfig) error {
	return gormrepository.Insert(m)
}

func UpdateEventConfig(m *models.EventConfig) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteEventConfig(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.EventConfig{})
}

func GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	var model models.EventConfig
	if err := gormrepository.GetByID(&model, id); err != nil {
		return &model, err
	}

	var designTemplate models.DesignTemplate
	var colorPalette models.ColorPalette
	var fontSet models.FontSet
	var designTemplateFound, colorPaletteFound, fontSetFound bool
	group := new(errgroup.Group)
	if model.DesignTemplateID != nil {
		designTemplateID := *model.DesignTemplateID
		group.Go(func() error {
			err := gormrepository.DB().
				Preload("ColorPalette.Patterns.Color").
				Preload("FontSet.Patterns.Font.Resource").
				First(&designTemplate, "id = ?", designTemplateID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			designTemplateFound = err == nil
			return err
		})
	}
	if model.ColorPaletteID != nil {
		colorPaletteID := *model.ColorPaletteID
		group.Go(func() error {
			err := gormrepository.DB().
				Preload("Patterns.Color").
				First(&colorPalette, "id = ?", colorPaletteID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			colorPaletteFound = err == nil
			return err
		})
	}
	if model.FontSetID != nil {
		fontSetID := *model.FontSetID
		group.Go(func() error {
			err := gormrepository.DB().
				Preload("Patterns.Font.Resource").
				First(&fontSet, "id = ?", fontSetID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			fontSetFound = err == nil
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return &model, err
	}
	if designTemplateFound {
		model.DesignTemplate = &designTemplate
	}
	if colorPaletteFound {
		model.ColorPalette = &colorPalette
	}
	if fontSetFound {
		model.FontSet = &fontSet
	}
	return &model, nil
}

func ListEventConfigs() ([]models.EventConfig, error) {
	var list []models.EventConfig
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}

// EventConfigRepo implements ports.EventConfigRepository.
type EventConfigRepo struct{}

func NewEventConfigRepo() *EventConfigRepo { return &EventConfigRepo{} }

func (r *EventConfigRepo) CreateEventConfig(m *models.EventConfig) error { return CreateEventConfig(m) }
func (r *EventConfigRepo) UpdateEventConfig(m *models.EventConfig) error { return UpdateEventConfig(m) }
func (r *EventConfigRepo) DeleteEventConfig(id uuid.UUID) error          { return DeleteEventConfig(id) }
func (r *EventConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	return GetEventConfigByID(id)
}
