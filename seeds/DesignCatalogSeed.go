package seeds

import (
	"events-stocks/models"
	"log/slog"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type designPaletteSeed struct {
	ID     uuid.UUID
	Name   string
	Colors map[string]string
}

type designTemplateSeed struct {
	ID          uuid.UUID
	Name        string
	Identifier  string
	Description string
	Category    string
	PaletteID   uuid.UUID
	Premium     bool
	DarkMode    bool
}

func designCatalogSeedDefinitions() ([]designPaletteSeed, []designTemplateSeed) {
	palettes := []designPaletteSeed{
		{
			ID:   uuid.Must(uuid.FromString("7d2ddf6f-4f58-4d63-9bf5-0ad4d8aa2001")),
			Name: "Editorial Romance",
			Colors: map[string]string{
				"background": "#FFF8F5", "surface": "#FFFFFF", "text": "#24303A",
				"heading": "#102F3F", "accent": "#DD2284", "muted": "#6B7F89", "border": "#E9D6DE",
			},
		},
		{
			ID:   uuid.Must(uuid.FromString("7d2ddf6f-4f58-4d63-9bf5-0ad4d8aa2002")),
			Name: "Noche Contemporánea",
			Colors: map[string]string{
				"background": "#09090B", "surface": "#18181B", "text": "#F4F4F5",
				"heading": "#FFFFFF", "accent": "#A78BFA", "muted": "#A1A1AA", "border": "#3F3F46",
			},
		},
		{
			ID:   uuid.Must(uuid.FromString("7d2ddf6f-4f58-4d63-9bf5-0ad4d8aa2003")),
			Name: "Celebración Cálida",
			Colors: map[string]string{
				"background": "#FFF9ED", "surface": "#FFFFFF", "text": "#3D2C22",
				"heading": "#6B3A25", "accent": "#D97706", "muted": "#8A6F60", "border": "#E8D1B8",
			},
		},
	}

	templates := []designTemplateSeed{
		{
			ID: models.DefaultDesignTemplateID(), Name: "Editorial Romance", Identifier: models.DefaultDesignTemplateIdentifier,
			Description: "Luz suave, tipografía editorial y acento romántico para bodas y celebraciones elegantes.",
			Category:    "romantic", PaletteID: palettes[0].ID,
		},
		{
			ID: uuid.Must(uuid.FromString("7d2ddf6f-4f58-4d63-9bf5-0ad4d8aa1002")), Name: "Noche Contemporánea", Identifier: "contemporary-night",
			Description: "Contraste cinematográfico y acentos violeta para eventos nocturnos y experiencias modernas.",
			Category:    "modern", PaletteID: palettes[1].ID, DarkMode: true,
		},
		{
			ID: uuid.Must(uuid.FromString("7d2ddf6f-4f58-4d63-9bf5-0ad4d8aa1003")), Name: "Celebración Cálida", Identifier: "warm-celebration",
			Description: "Tonos marfil, terracota y ámbar para graduaciones, cumpleaños y reuniones familiares.",
			Category:    "celebration", PaletteID: palettes[2].ID,
		},
	}
	return palettes, templates
}

// SeedDesignCatalog publishes a small, opinionated starter catalog. It is
// idempotent and updates only the stable seed IDs, preserving custom entries.
func SeedDesignCatalog(db *gorm.DB) error {
	palettes, templates := designCatalogSeedDefinitions()
	now := time.Now().UTC()
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, paletteSeed := range palettes {
			palette := models.ColorPalette{ID: paletteSeed.ID, Name: paletteSeed.Name, CreatedAt: now, UpdatedAt: now}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"name", "updated_at", "deleted_at"}),
			}).Create(&palette).Error; err != nil {
				return err
			}

			for order, key := range []string{"background", "surface", "text", "heading", "accent", "muted", "border"} {
				value := paletteSeed.Colors[key]
				colorID := uuid.NewV5(paletteSeed.ID, key+":"+value)
				color := models.Color{ID: colorID, Name: paletteSeed.Name + " " + key, Value: value, CreatedAt: now, UpdatedAt: now}
				if err := tx.Clauses(clause.OnConflict{
					Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"name", "value", "updated_at", "deleted_at"}),
				}).Create(&color).Error; err != nil {
					return err
				}

				pattern := models.ColorPalettePattern{
					ID: uuid.NewV5(paletteSeed.ID, "pattern:"+key), ColorPaletteID: paletteSeed.ID,
					ColorID: colorID, Key: key, Order: order + 1, CreatedAt: now, UpdatedAt: now,
				}
				if err := tx.Clauses(clause.OnConflict{
					Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"color_palette_id", "color_id", "key", "order", "updated_at", "deleted_at"}),
				}).Create(&pattern).Error; err != nil {
					return err
				}
			}
		}

		for _, templateSeed := range templates {
			paletteID := templateSeed.PaletteID
			template := models.DesignTemplate{
				ID: templateSeed.ID, Name: templateSeed.Name, Identifier: templateSeed.Identifier,
				Description: templateSeed.Description, ColorPaletteID: &paletteID,
				AnimationsEnabled: true, HasDarkMode: templateSeed.DarkMode, Category: templateSeed.Category,
				IsPremium: templateSeed.Premium, IsActive: true, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"name", "identifier", "description", "color_palette_id", "animations_enabled",
					"has_dark_mode", "category", "is_premium", "is_active", "updated_at", "deleted_at",
				}),
			}).Create(&template).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("error seeding design catalog", "error", err)
		return err
	}
	slog.Info("design catalog seeded", "templates", len(templates), "palettes", len(palettes))
	return nil
}
