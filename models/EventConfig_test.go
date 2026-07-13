package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventConfigVisibilityDefaultsPreserveExplicitAllFalse(t *testing.T) {
	config := &EventConfig{VisibilityConfigured: true}

	normalized := config.WithVisibilityDefaults()

	assert.Same(t, config, normalized)
	assert.False(t, normalized.ShowHeader)
	assert.False(t, normalized.ShowFooter)
	assert.False(t, normalized.ShowMomentWall)
}

func TestEventConfigVisibilityDefaultsNormalizeLegacyAllFalse(t *testing.T) {
	config := &EventConfig{}

	normalized := config.WithVisibilityDefaults()

	assert.NotSame(t, config, normalized)
	assert.True(t, normalized.ShowHeader)
	assert.True(t, normalized.ShowFooter)
	assert.True(t, normalized.ShowMomentWall)
	assert.False(t, config.ShowHeader)
}
