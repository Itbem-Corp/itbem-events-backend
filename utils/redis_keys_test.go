package utils

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResourceCacheTTLMatchesPublicFrontendFreshness(t *testing.T) {
	assert.Equal(t, 5*time.Minute, ResourceCacheTTL)
	assert.Equal(t, ResourceCacheTTL, CacheTTLs[RedisResourcesKey])
}

func TestFrontendIntegrationCacheTTLsAreNamedAndPositive(t *testing.T) {
	for _, key := range []string{
		RedisServiceEventsKey,
		RedisEventTypesKey,
		RedisEventAnalyticsKey,
		RedisEventSectionsKey,
		RedisTemplatesKey,
		RedisMomentsKey,
		RedisResourcesKey,
		RedisGuestStatussKey,
		RedisColorPalettesKey,
		RedisColorPalettePatternsKey,
	} {
		assert.Positive(t, CacheTTLs[key], key)
	}
}
