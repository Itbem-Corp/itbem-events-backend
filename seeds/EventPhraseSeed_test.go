package seeds

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSeedEventPhrasesRejectsMissingDatabase(t *testing.T) {
	err := SeedEventPhrases(nil)

	assert.EqualError(t, err, "event phrase database is not initialized")
}
