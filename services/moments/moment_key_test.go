package moments

import (
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
)

func TestIsEventMomentKey(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	other := uuid.Must(uuid.NewV4()).String()
	org := uuid.Must(uuid.NewV4()).String()
	cases := map[string]bool{
		"moments/{event}/raw/a.jpg":                           true,
		"moments/{event}/photos/a-320.webp":                   true,
		"  moments/{event}/videos/a.mp4  ":                    true,
		"organizations/" + org + "/moments/{event}/raw/a.jpg": true,
		"":                                false,
		"   ":                             false,
		"private/secret.jpg":              false,
		"moments/" + other + "/raw/a.jpg": false,
		"organizations/" + org + "/moments/" + other + "/a.jpg": false,
		"moments/{event}/":                                                 false,
		"moments/{event}":                                                  false,
		"moments/{event}x/a.jpg":                                           false,
		"/moments/{event}/raw/a.jpg":                                       false,
		"moments/{event}/../" + other + "/a.jpg":                           false,
		`moments/{event}\raw\a.jpg`:                                        false,
		"events/{event}/covers/a.webp":                                     false,
		"resources/moments/{event}/a.jpg":                                  false,
		"tmp/moments/{event}/raw/a.jpg":                                    false,
		"organizations/not-a-uuid/moments/{event}/raw/a.jpg":               false,
		"organizations/" + uuid.Nil.String() + "/moments/{event}/a":        false,
		"organizations/{" + org + "}/moments/{event}/raw/a.jpg":            false,
		"organizations/" + strings.ToUpper(org) + "/moments/{event}/a.jpg": false,
		"organizations/" + org + "/x/moments/{event}/raw/a.jpg":            false,
		"https://bucket.s3.amazonaws.com/moments/{event}/raw/a.jpg":        false,
		"//cdn.example.com/moments/{event}/raw/a.jpg":                      false,
	}
	for key, want := range cases {
		resolved := strings.ReplaceAll(key, "{event}", eventID.String())
		t.Run(resolved, func(t *testing.T) {
			assert.Equal(t, want, IsEventMomentKey(eventID, resolved))
		})
	}
	assert.False(t, IsEventMomentKey(uuid.Nil, "moments/"+uuid.Nil.String()+"/raw/a.jpg"), "a moment without an event owns no key")
}
