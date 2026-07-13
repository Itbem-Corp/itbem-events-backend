package seeds

import (
	"events-stocks/models"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDesignCatalogSeedDefinitionsAreUsableAndStable(t *testing.T) {
	palettes, templates := designCatalogSeedDefinitions()
	require.Len(t, palettes, 3)
	require.Len(t, templates, 3)

	seenIdentifiers := map[string]bool{}
	for _, palette := range palettes {
		for _, key := range []string{"background", "surface", "text", "heading", "accent", "muted", "border"} {
			assert.Regexp(t, `^#[0-9A-F]{6}$`, palette.Colors[key], "%s must define %s", palette.Name, key)
		}
	}
	for _, template := range templates {
		assert.NotEmpty(t, template.Name)
		assert.NotEmpty(t, template.Description)
		assert.False(t, template.Premium, "starter template %s requires no entitlement", template.Identifier)
		assert.False(t, seenIdentifiers[template.Identifier], "duplicate identifier %s", template.Identifier)
		seenIdentifiers[template.Identifier] = true
	}

	assert.Equal(t, models.DefaultDesignTemplateID(), templates[0].ID)
	assert.Equal(t, models.DefaultDesignTemplateIdentifier, templates[0].Identifier)
}
