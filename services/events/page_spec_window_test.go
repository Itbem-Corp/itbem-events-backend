package events

import (
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var windowTestNow = time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)

func closedWindowConfigs() map[string]*models.EventConfig {
	yesterday := windowTestNow.Add(-24 * time.Hour)
	return map[string]*models.EventConfig{
		AccessWindowEnded: {
			IsPublic:    true,
			ActiveFrom:  windowTestNow.Add(-72 * time.Hour),
			ActiveUntil: &yesterday,
		},
		AccessWindowNotStarted: {
			IsPublic:   true,
			ActiveFrom: windowTestNow.Add(24 * time.Hour),
		},
	}
}

func TestGetPageSpecByIdentifier_ClosedWindowServesLockedSpec(t *testing.T) {
	for status, cfg := range closedWindowConfigs() {
		cfg := cfg
		t.Run(status, func(t *testing.T) {
			deps := identifierPageSpecDeps{
				getEventByIdentifier: func(string) (*models.Event, error) { return stubEvent("https://music"), nil },
				getSections: func(uuid.UUID) ([]models.EventSection, error) {
					t.Fatal("sections must not load outside the access window")
					return nil, nil
				},
				getConfig: func(uuid.UUID) (*models.EventConfig, error) { return cfg, nil },
				now:       func() time.Time { return windowTestNow },
			}

			spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "")
			require.NoError(t, err)
			require.NotNil(t, spec)
			assert.Empty(t, spec.Sections)
			assert.Empty(t, spec.Meta.Address)
			assert.Nil(t, spec.Meta.Contact)
			assert.Nil(t, spec.Meta.MusicUrl)
			assert.Nil(t, spec.Meta.Theme)
			assert.Empty(t, spec.Meta.CoverImageURL)
			require.NotNil(t, spec.Meta.Access)
			assert.Equal(t, status, spec.Meta.Access.WindowStatus)
		})
	}
}

func TestGetPageSpecByIdentifier_PreviewTokenBypassesClosedWindow(t *testing.T) {
	for status, cfg := range closedWindowConfigs() {
		cfg := cfg
		t.Run(status, func(t *testing.T) {
			deps := identifierPageSpecDeps{
				getEventByIdentifier: func(string) (*models.Event, error) { return stubEvent(""), nil },
				getSections:          func(uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
				getConfig:            func(uuid.UUID) (*models.EventConfig, error) { return cfg, nil },
				validatePreviewToken: func(token string, eventID uuid.UUID) bool { return token == "preview-ok" },
				now:                  func() time.Time { return windowTestNow },
			}

			spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "preview-ok", "")
			require.NoError(t, err)
			require.Len(t, spec.Sections, 2)
			assert.True(t, spec.Meta.Access.PreviewAuthorized)
		})
	}
}

func TestGetPageSpecByIdentifier_ClosedWindowStillDeniesPrivateEventWithoutToken(t *testing.T) {
	cfg := closedWindowConfigs()[AccessWindowEnded]
	cfg.IsPublic = false
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(string) (*models.Event, error) { return stubEvent(""), nil },
		getConfig:            func(uuid.UUID) (*models.EventConfig, error) { return cfg, nil },
		now:                  func() time.Time { return windowTestNow },
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "")
	assert.Nil(t, spec)
	assert.ErrorIs(t, err, ErrPageSpecNotPublic)
}

func TestGetPageSpecByIdentifier_OpenWindowServesSectionsAndReportsOpen(t *testing.T) {
	tomorrow := windowTestNow.Add(24 * time.Hour)
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(string) (*models.Event, error) { return stubEvent(""), nil },
		getSections:          func(uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: true, ActiveFrom: windowTestNow.Add(-time.Hour), ActiveUntil: &tomorrow}, nil
		},
		now: func() time.Time { return windowTestNow },
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "")
	require.NoError(t, err)
	require.Len(t, spec.Sections, 2)
	assert.Equal(t, AccessWindowOpen, spec.Meta.Access.WindowStatus)
}

func TestGetPageSpecByToken_ClosedWindowServesLockedSpec(t *testing.T) {
	for status, cfg := range closedWindowConfigs() {
		cfg := cfg
		t.Run(status, func(t *testing.T) {
			deps := pageSpecDeps{
				getToken:      func(string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
				getInvitation: func(uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
				getEvent:      func(uuid.UUID) (*models.Event, error) { return stubEvent(""), nil },
				getSections: func(uuid.UUID) ([]models.EventSection, error) {
					t.Fatal("sections must not load outside the access window")
					return nil, nil
				},
				getConfig: func(uuid.UUID) (*models.EventConfig, error) { return cfg, nil },
				now:       func() time.Time { return windowTestNow },
			}

			spec, err := getPageSpec(deps, "ABC123")
			require.NoError(t, err)
			assert.Empty(t, spec.Sections)
			require.NotNil(t, spec.Meta.Access)
			assert.Equal(t, status, spec.Meta.Access.WindowStatus)
		})
	}
}

func TestEventAccessWindowStatus_TreatsUnsetBoundsAsOpen(t *testing.T) {
	epoch := time.Unix(0, 0).UTC()
	assert.Equal(t, AccessWindowOpen, EventAccessWindowStatus(nil, windowTestNow))
	assert.Equal(t, AccessWindowOpen, EventAccessWindowStatus(&models.EventConfig{}, windowTestNow))
	assert.Equal(t, AccessWindowOpen, EventAccessWindowStatus(&models.EventConfig{ActiveFrom: epoch, ActiveUntil: &epoch}, windowTestNow))
}
