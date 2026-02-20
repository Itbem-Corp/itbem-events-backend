package utils

import (
	"events-stocks/configuration/constants"
	"time"
)

const RedisServiceEventsKey = "events"
const RedisColorsServiceKey = "colors"
const RedisPaletteServiceKey = "palettecolors"
const RedisFontsKey = "fonts"
const RedisFontSetKey = "fontsets"
const RedisResourceTypeKey = "resourcetypes"
const RedisResourcesKey = "resources"
const RedisGuestsKey = "guests"
const RedisGuestStatussKey = "gueststatus"
const RedisInvitationsKey = "invitations"
const RedisInvitationTokensKey = "invitationtokens"

var CacheTTLs = map[string]time.Duration{
	RedisServiceEventsKey:    constants.ShortTimeTTL,
	RedisFontSetKey:          constants.LargeTimeTTL,
	RedisPaletteServiceKey:   constants.MediumTimeTTL,
	RedisResourceTypeKey:     constants.XLongTimeTTL,
	RedisResourcesKey:        constants.MediumLargeTimeTTL,
	RedisColorsServiceKey:    constants.XXLongTimeTTL,
	RedisFontsKey:            constants.XXLongTimeTTL,
	RedisGuestsKey:           constants.MediumTimeTTL,
	RedisGuestStatussKey:     constants.XLongTimeTTL,
	RedisInvitationsKey:      constants.MediumLargeTimeTTL,
	RedisInvitationTokensKey: constants.XXLongTimeTTL,
}
