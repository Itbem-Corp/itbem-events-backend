package dtos

import (
	"encoding/json"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventConfigPatchClearsSharedUploadsWhenUploadsAreDisabled(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"allow_uploads":false}`), &patch))

	config := models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
	}

	require.NoError(t, patch.ApplyTo(&config))

	assert.False(t, config.AllowUploads)
	assert.False(t, config.ShareUploadsEnabled)
}

func TestEventConfigPatchSharedUploadsEnableOpenUploadContract(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"share_uploads_enabled":true}`), &patch))

	config := models.EventConfig{
		AllowUploads:        false,
		ShareUploadsEnabled: false,
		ShowMomentWall:      true,
	}

	require.NoError(t, patch.ApplyTo(&config))

	assert.True(t, config.AllowUploads)
	assert.True(t, config.ShareUploadsEnabled)
	assert.False(t, config.ShowMomentWall)
	assert.True(t, config.VisibilityConfigured)
}

func TestEventConfigPatchSharedUploadsAliasEnablesOpenUploadContract(t *testing.T) {
	tests := []string{"shareUploadsEnabled", "sharedUploadsEnabled", "shared_uploads_enabled"}

	for _, field := range tests {
		t.Run(field, func(t *testing.T) {
			var patch EventConfigPatch
			require.NoError(t, json.Unmarshal([]byte(`{"`+field+`":true}`), &patch))

			config := models.EventConfig{
				AllowUploads:        false,
				ShareUploadsEnabled: false,
				ShowMomentWall:      true,
			}

			require.NoError(t, patch.ApplyTo(&config))

			assert.True(t, config.AllowUploads)
			assert.True(t, config.ShareUploadsEnabled)
			assert.False(t, config.ShowMomentWall)
			assert.True(t, config.VisibilityConfigured)
		})
	}
}

func TestEventConfigPatchTrimsAuthPasswordPreview(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"auth_password_preview":"  secreto  "}`), &patch))

	config := models.EventConfig{}

	require.NoError(t, patch.ApplyTo(&config))

	assert.Equal(t, "secreto", config.AuthPasswordPreview)
}

func TestEventConfigPatchAcceptsMomentWallPublishedAlias(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"moments_wall_published":true}`), &patch))

	config := models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
	}

	require.NoError(t, patch.ApplyTo(&config))

	assert.True(t, config.ShowMomentWall)
	assert.False(t, config.ShareUploadsEnabled)
	assert.True(t, config.VisibilityConfigured)
}

func TestEventConfigPatchRejectsAmbiguousPublishedAlias(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"published":true}`), &patch))

	err := patch.ApplyTo(&models.EventConfig{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown event config field: published")
}

func TestEventConfigPatchRejectsActiveUntilBeforeActiveFrom(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"active_from": "2026-07-10T18:00:00Z",
		"active_until": "2026-07-10T17:59:00Z"
	}`), &patch))

	config := models.EventConfig{}

	err := patch.ApplyTo(&config)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "active_until")
	assert.Contains(t, err.Error(), "must be after active_from")
}

func TestEventConfigPatchRejectsActiveUntilEqualToActiveFrom(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"active_from": "2026-07-10T18:00:00Z",
		"active_until": "2026-07-10T18:00:00Z"
	}`), &patch))

	config := models.EventConfig{}

	err := patch.ApplyTo(&config)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "active_until")
}

func TestEventConfigPatchAllowsActiveUntilWithoutActiveFrom(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"active_until": "2026-07-10T18:00:00Z"}`), &patch))

	config := models.EventConfig{}

	require.NoError(t, patch.ApplyTo(&config))
	require.NotNil(t, config.ActiveUntil)
	assert.Equal(t, 2026, config.ActiveUntil.Year())
}

func TestEventConfigPatchTrimsAndClearsActiveWindowDates(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"active_from": " 2026-07-10T18:00:00Z ",
		"active_until": "   "
	}`), &patch))

	activeUntil := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)
	config := models.EventConfig{ActiveUntil: &activeUntil}

	require.NoError(t, patch.ApplyTo(&config))

	assert.Equal(t, time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC), config.ActiveFrom)
	assert.Nil(t, config.ActiveUntil)
}

func TestEventConfigPatchAcceptsNumericStringUploadLimit(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"maxUploadsPerGuest":"25"}`), &patch))

	config := models.EventConfig{MaxUploadsPerGuest: 30}

	require.NoError(t, patch.ApplyTo(&config))

	assert.Equal(t, 25, config.MaxUploadsPerGuest)
}

func TestEventConfigPatchRejectsNegativeStringUploadLimit(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"max_uploads_per_guest":"-1"}`), &patch))

	config := models.EventConfig{MaxUploadsPerGuest: 30}

	err := patch.ApplyTo(&config)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_uploads_per_guest")
	assert.Contains(t, err.Error(), "must be greater than or equal to 0")
}

func TestEventConfigPatchExplicitCloseWinsOverSharedUploads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "uploads disabled",
			body: `{"allow_uploads":false,"share_uploads_enabled":true}`,
		},
		{
			name: "wall published",
			body: `{"show_moment_wall":true,"share_uploads_enabled":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var patch EventConfigPatch
			require.NoError(t, json.Unmarshal([]byte(tt.body), &patch))

			config := models.EventConfig{
				AllowUploads:        true,
				ShareUploadsEnabled: false,
				ShowMomentWall:      false,
			}

			require.NoError(t, patch.ApplyTo(&config))

			assert.False(t, config.ShareUploadsEnabled)
		})
	}
}

func TestEventConfigPatchAcceptsFrontendCasingAliases(t *testing.T) {
	templateID := uuid.Must(uuid.NewV4())
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"allowUploads": true,
		"allowMessages": true,
		"shareUploadsEnabled": true,
		"maxUploadsPerGuest": 12,
		"defaultWelcomeMessage": "Hola",
		"defaultThankYouMessage": "Gracias",
		"defaultMomentRequestMessage": "Comparte fotos",
		"defaultGuestSignatureTitle": "Firma",
		"showMomentWall": true,
		"showRSVPSection": false,
		"showFooter": false,
		"designTemplateId": "`+templateID.String()+`"
	}`), &patch))

	config := models.EventConfig{}

	require.NoError(t, patch.ApplyTo(&config))

	assert.True(t, config.AllowUploads)
	assert.True(t, config.AllowMessages)
	assert.False(t, config.ShareUploadsEnabled)
	assert.Equal(t, 12, config.MaxUploadsPerGuest)
	assert.Equal(t, "Hola", config.DefaultWelcomeMessage)
	assert.Equal(t, "Gracias", config.DefaultThankYouMessage)
	assert.Equal(t, "Comparte fotos", config.DefaultMomentRequestMessage)
	assert.Equal(t, "Firma", config.DefaultGuestSignatureTitle)
	assert.True(t, config.ShowMomentWall)
	assert.False(t, config.ShowRSVPSection)
	assert.False(t, config.ShowFooter)
	require.NotNil(t, config.DesignTemplateID)
	assert.Equal(t, templateID, *config.DesignTemplateID)
}

func TestEventConfigPatchAcceptsCommonTypeScriptAndLegacyAliases(t *testing.T) {
	templateID := uuid.Must(uuid.NewV4())
	paletteID := uuid.Must(uuid.NewV4())
	fontSetID := uuid.Must(uuid.NewV4())
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"Id": "ignored",
		"EventId": "ignored",
		"DesignTemplateId": "`+templateID.String()+`",
		"ColorPaletteId": "`+paletteID.String()+`",
		"FontSetId": "`+fontSetID.String()+`",
		"welcomeMessage": "Bienvenidos",
		"momentMessage": "Compartan sus fotos",
		"thankYouMessage": "Gracias por confirmar",
		"guestSignatureTitle": "Dejanos un mensaje",
		"showRsvpSection": false,
		"showLocation": false,
		"showGallery": false,
		"showWall": false,
		"showContact": false,
		"showSchedule": false
	}`), &patch))

	config := models.EventConfig{
		ShowRSVPSection:    true,
		ShowEventLocation:  true,
		ShowPhotoGallery:   true,
		ShowMomentWall:     true,
		ShowContactSection: true,
		ShowEventSchedule:  true,
	}

	require.NoError(t, patch.ApplyTo(&config))

	require.NotNil(t, config.DesignTemplateID)
	assert.Equal(t, templateID, *config.DesignTemplateID)
	require.NotNil(t, config.ColorPaletteID)
	assert.Equal(t, paletteID, *config.ColorPaletteID)
	require.NotNil(t, config.FontSetID)
	assert.Equal(t, fontSetID, *config.FontSetID)
	assert.Equal(t, "Bienvenidos", config.DefaultWelcomeMessage)
	assert.Equal(t, "Compartan sus fotos", config.DefaultMomentRequestMessage)
	assert.Equal(t, "Gracias por confirmar", config.DefaultThankYouMessage)
	assert.Equal(t, "Dejanos un mensaje", config.DefaultGuestSignatureTitle)
	assert.False(t, config.ShowRSVPSection)
	assert.False(t, config.ShowEventLocation)
	assert.False(t, config.ShowPhotoGallery)
	assert.False(t, config.ShowMomentWall)
	assert.False(t, config.ShowContactSection)
	assert.False(t, config.ShowEventSchedule)
	assert.True(t, config.VisibilityConfigured)
}

func TestEventConfigPatchMarksVisibilityConfiguredForFalseValue(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"show_header":false}`), &patch))

	config := models.EventConfig{ShowHeader: true}

	require.NoError(t, patch.ApplyTo(&config))

	assert.False(t, config.ShowHeader)
	assert.True(t, config.VisibilityConfigured)
}

func TestEventConfigPatchClearsSharedUploadsWhenMomentWallIsPublished(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{"show_moment_wall":true}`), &patch))

	config := models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
		ShowMomentWall:      false,
	}

	require.NoError(t, patch.ApplyTo(&config))

	assert.True(t, config.AllowUploads)
	assert.True(t, config.ShowMomentWall)
	assert.False(t, config.ShareUploadsEnabled)
}

func TestEventConfigPatchCanonicalFieldWinsOverAlias(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"allowUploads": true,
		"allow_uploads": false
	}`), &patch))

	config := models.EventConfig{
		AllowUploads:        true,
		ShareUploadsEnabled: true,
	}

	require.NoError(t, patch.ApplyTo(&config))

	assert.False(t, config.AllowUploads)
	assert.False(t, config.ShareUploadsEnabled)
}

func TestEventConfigPatchIgnoresReadOnlyFrontendCasingAliases(t *testing.T) {
	var patch EventConfigPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"EventID": "ignored",
		"DesignTemplate": {"identifier": "ignored"},
		"UpdatedAt": "2026-01-01T00:00:00Z"
	}`), &patch))

	config := models.EventConfig{}

	require.NoError(t, patch.ApplyTo(&config))
}

func TestNewEventConfigResponseNormalizesLegacyVisibilityDefaults(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	config := &models.EventConfig{ID: eventID, IsPublic: true}

	response := NewEventConfigResponse(config, eventID)

	assert.True(t, response.IsPublic)
	assert.True(t, response.ShowHeader)
	assert.True(t, response.ShowFooter)
	assert.True(t, response.ShowCountdown)
	assert.True(t, response.ShowMomentWall)
	assert.True(t, response.MomentsWallPublished)
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"moments_wall_published":true`)
	assert.False(t, config.ShowHeader)
}

func TestNewEventConfigResponseKeepsUploadWindowOpenForLegacyUploadConfig(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	config := &models.EventConfig{
		ID:                    eventID,
		AllowUploads:          true,
		ShareUploadsEnabled:   true,
		MaxUploadsPerGuest:    7,
		DefaultWelcomeMessage: "Hola",
	}

	response := NewEventConfigResponse(config, eventID)

	assert.True(t, response.ShowHeader)
	assert.True(t, response.ShowFooter)
	assert.False(t, response.ShowMomentWall)
	assert.False(t, response.MomentsWallPublished)
	assert.True(t, response.AllowUploads)
	assert.True(t, response.ShareUploadsEnabled)
	assert.Equal(t, 7, response.MaxUploadsPerGuest)
	assert.Equal(t, "Hola", response.DefaultWelcomeMessage)
}

func TestNewEventConfigResponseTrimsAuthPasswordPreview(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	config := &models.EventConfig{
		ID:                  eventID,
		AuthPasswordPreview: "  secreto  ",
	}

	response := NewEventConfigResponse(config, eventID)

	assert.Equal(t, "secreto", response.AuthPasswordPreview)
}

func TestNewEventConfigResponseOmitsZeroAccessDates(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	zeroUntil := time.Time{}
	config := &models.EventConfig{
		ID:          eventID,
		ActiveUntil: &zeroUntil,
	}

	response := NewEventConfigResponse(config, eventID)
	raw, err := json.Marshal(response)
	require.NoError(t, err)

	assert.Nil(t, response.ActiveFrom)
	assert.Nil(t, response.ActiveUntil)
	assert.NotContains(t, string(raw), "active_from")
	assert.NotContains(t, string(raw), "active_until")
}

func TestNewEventConfigResponseKeepsConfiguredAccessDates(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	activeFrom := time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC)
	activeUntil := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	config := &models.EventConfig{
		ID:          eventID,
		ActiveFrom:  activeFrom,
		ActiveUntil: &activeUntil,
	}

	response := NewEventConfigResponse(config, eventID)

	require.NotNil(t, response.ActiveFrom)
	require.NotNil(t, response.ActiveUntil)
	assert.Equal(t, activeFrom, *response.ActiveFrom)
	assert.Equal(t, activeUntil, *response.ActiveUntil)
}

func TestNewEventConfigResponsePreservesExplicitAllFalseVisibility(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	config := &models.EventConfig{
		ID:                   eventID,
		IsPublic:             true,
		VisibilityConfigured: true,
	}

	response := NewEventConfigResponse(config, eventID)

	assert.True(t, response.IsPublic)
	assert.True(t, response.VisibilityConfigured)
	assert.False(t, response.ShowHeader)
	assert.False(t, response.ShowFooter)
	assert.False(t, response.ShowCountdown)
	assert.False(t, response.ShowMomentWall)
	assert.False(t, response.MomentsWallPublished)
}
