package utils

import (
	"events-stocks/configuration/constants"
	"time"
)

const RedisServiceEventsKey = "events"
const RedisEventTypesKey = "event_types"
const RedisEventAnalyticsKey = "event_analytics"
const RedisEventSectionsKey = "event_sections"
const RedisColorsServiceKey = "colors"
const RedisColorPalettesKey = "color_palettes"
const RedisColorPalettePatternsKey = "color_palette_patterns"
const RedisPaletteServiceKey = RedisColorPalettesKey
const RedisFontsKey = "fonts"
const RedisFontSetKey = "font_sets:v2"
const RedisTemplatesKey = "templates"
const RedisMomentsKey = "moments"
const RedisResourceTypeKey = "resourcetypes"
const RedisResourcesKey = "resources"
const RedisGuestsKey = "guests"
const RedisGuestStatussKey = "guest_statuses"
const RedisInvitationsKey = "invitations"
const RedisInvitationTokensKey = "invitationtokens"

const ResourceCacheTTL = 5 * time.Minute

var CacheTTLs = map[string]time.Duration{
	RedisServiceEventsKey:        constants.ShortTimeTTL,
	RedisEventTypesKey:           constants.ShortTimeTTL,
	RedisEventAnalyticsKey:       constants.ShortTimeTTL,
	RedisEventSectionsKey:        constants.ShortTimeTTL,
	RedisFontSetKey:              constants.LargeTimeTTL,
	RedisTemplatesKey:            constants.MediumTimeTTL,
	RedisMomentsKey:              constants.ShortTimeTTL,
	RedisColorPalettesKey:        constants.MediumTimeTTL,
	RedisColorPalettePatternsKey: constants.MediumTimeTTL,
	RedisResourceTypeKey:         constants.XLongTimeTTL,
	RedisResourcesKey:            ResourceCacheTTL,
	RedisColorsServiceKey:        constants.XXLongTimeTTL,
	RedisFontsKey:                constants.XXLongTimeTTL,
	RedisGuestsKey:               constants.MediumTimeTTL,
	RedisGuestStatussKey:         constants.XLongTimeTTL,
	RedisInvitationsKey:          constants.MediumLargeTimeTTL,
	RedisInvitationTokensKey:     constants.XXLongTimeTTL,
}
