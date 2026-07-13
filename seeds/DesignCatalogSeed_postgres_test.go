package seeds

import (
	"events-stocks/models"
	"os"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// This opt-in test proves the behavior AutoMigrate/unit mocks cannot: running
// the publication twice against Postgres is idempotent and does not replace a
// customer-created catalog entry. The outer transaction always rolls back.
func TestSeedDesignCatalogPostgresIsIdempotentAndPreservesCustomEntries(t *testing.T) {
	dsn := os.Getenv("DESIGN_CATALOG_TEST_DSN")
	if dsn == "" {
		t.Skip("DESIGN_CATALOG_TEST_DSN is not configured")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })

	customID := uuid.Must(uuid.NewV4())
	customIdentifier := "test-custom-" + customID.String()
	require.NoError(t, tx.Create(&models.DesignTemplate{
		ID: customID, Name: "Custom test template", Identifier: customIdentifier, IsActive: true,
	}).Error)

	require.NoError(t, SeedDesignCatalog(tx))
	require.NoError(t, SeedDesignCatalog(tx))

	_, definitions := designCatalogSeedDefinitions()
	for _, definition := range definitions {
		var template models.DesignTemplate
		require.NoError(t, tx.Preload("ColorPalette.Patterns.Color").First(&template, "id = ?", definition.ID).Error)
		assert.Equal(t, definition.Identifier, template.Identifier)
		assert.True(t, template.IsActive)
		assert.False(t, template.IsPremium)
		require.NotNil(t, template.ColorPaletteID)
		require.NotNil(t, template.ColorPalette)
		assert.Len(t, template.ColorPalette.Patterns, 7)

		var identifierCount int64
		require.NoError(t, tx.Model(&models.DesignTemplate{}).
			Where("identifier = ?", definition.Identifier).
			Count(&identifierCount).Error)
		assert.EqualValues(t, 1, identifierCount)
	}

	var custom models.DesignTemplate
	require.NoError(t, tx.First(&custom, "id = ?", customID).Error)
	assert.Equal(t, customIdentifier, custom.Identifier)
}
