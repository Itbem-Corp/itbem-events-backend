package events

import (
	"encoding/json"
	"errors"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func mustUUID(s string) uuid.UUID {
	id, err := uuid.FromString(s)
	if err != nil {
		panic(err)
	}
	return id
}

var (
	testTokenID      = mustUUID("aaaaaaaa-0000-0000-0000-000000000001")
	testInvitationID = mustUUID("bbbbbbbb-0000-0000-0000-000000000002")
	testEventID      = mustUUID("cccccccc-0000-0000-0000-000000000003")
	testSectionID1   = mustUUID("dddddddd-0000-0000-0000-000000000004")
	testSectionID2   = mustUUID("eeeeeeee-0000-0000-0000-000000000005")
	testEventTypeID  = mustUUID("eeeeeeee-0000-0000-0000-000000000006")
)

func stubToken() *models.InvitationAccessToken {
	return &models.InvitationAccessToken{
		ID:           testTokenID,
		InvitationID: testInvitationID,
		PrettyToken:  "ABC123",
	}
}

func stubInvitation() *models.Invitation {
	return &models.Invitation{
		ID:      testInvitationID,
		EventID: testEventID,
	}
}

func stubEvent(musicUrl string) *models.Event {
	return &models.Event{
		ID:            testEventID,
		Name:          "Graduación Izapa 2025",
		MusicUrl:      musicUrl,
		Identifier:    "grad-izapa-2025",
		CoverImageURL: "events/grad-izapa-2025/cover.webp",
		EventDateTime: time.Date(2026, time.August, 15, 20, 30, 0, 0, time.UTC),
		Address:       "Av. Universidad 123",
		SecondAddress: "Salon Izapa",
		Timezone:      "America/Mexico_City",
		Language:      "es",
		EventTypeID:   testEventTypeID,
		EventType:     models.EventType{ID: testEventTypeID, Name: "graduation"},
		IsActive:      true,
	}
}

func stubSections() []models.EventSection {
	return []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			Title:         "Cuenta regresiva",
			ComponentType: "CountdownHeader",
			Config:        `{"heading":"EL GRAN DÍA","targetDate":"2025-06-22T20:30:00-06:00"}`,
			Order:         1,
			IsVisible:     true,
		},
		{
			ID:            testSectionID2,
			EventID:       testEventID,
			Title:         "Portada",
			ComponentType: "GraduationHero",
			Config:        `{"title":"NOS GRADUAMOS","years":"2022 - 2025","school":"PREPARATORIA"}`,
			Order:         2,
			IsVisible:     true,
		},
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

type mockPageSpecAccessTokenRepo struct {
	GetByTokenFunc       func(token string) (*models.InvitationAccessToken, error)
	GetByPrettyTokenFunc func(code string) (*models.InvitationAccessToken, error)
}

func (m *mockPageSpecAccessTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	if m.GetByTokenFunc != nil {
		return m.GetByTokenFunc(token)
	}
	return stubToken(), nil
}

func (m *mockPageSpecAccessTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	if m.GetByPrettyTokenFunc != nil {
		return m.GetByPrettyTokenFunc(code)
	}
	return m.GetByToken(code)
}

func (m *mockPageSpecAccessTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABC123", nil
}

type mockPageSpecInvitationRepo struct {
	GetInvitationByIDLiteFunc func(id uuid.UUID) (*models.Invitation, error)
}

func (m *mockPageSpecInvitationRepo) CreateInvitation(obj *models.Invitation) error { return nil }
func (m *mockPageSpecInvitationRepo) UpdateInvitation(obj *models.Invitation) error { return nil }
func (m *mockPageSpecInvitationRepo) DeleteInvitation(id uuid.UUID) error           { return nil }
func (m *mockPageSpecInvitationRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	return m.GetInvitationByIDLite(id)
}
func (m *mockPageSpecInvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	if m.GetInvitationByIDLiteFunc != nil {
		return m.GetInvitationByIDLiteFunc(id)
	}
	return stubInvitation(), nil
}
func (m *mockPageSpecInvitationRepo) ListInvitations() ([]models.Invitation, error) {
	return nil, nil
}
func (m *mockPageSpecInvitationRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return nil, nil
}

func TestGetPageSpec_TokenNotFound(t *testing.T) {
	deps := pageSpecDeps{
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			return nil, errors.New("record not found")
		},
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) {
			t.Fatal("getInvitation should not be called when token is not found")
			return nil, nil
		},
		getEvent:    func(id uuid.UUID) (*models.Event, error) { return nil, nil },
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return nil, nil },
	}

	spec, err := getPageSpec(deps, "INVALID")
	assert.Nil(t, spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token not found")
}

func TestGetPageSpec_InvitationNotFound(t *testing.T) {
	deps := pageSpecDeps{
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			return stubToken(), nil
		},
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) {
			return nil, errors.New("record not found")
		},
		getEvent:    func(id uuid.UUID) (*models.Event, error) { return nil, nil },
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return nil, nil },
	}

	spec, err := getPageSpec(deps, "ABC123")
	assert.Nil(t, spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invitation not found")
}

func TestGetPageSpec_Success_WithMusic(t *testing.T) {
	musicUrl := "https://s3.amazonaws.com/bucket/audio.mp3"

	deps := pageSpecDeps{
		getToken:      func(token string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
		getEvent:      func(id uuid.UUID) (*models.Event, error) { return stubEvent(musicUrl), nil },
		getSections:   func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
	}

	spec, err := getPageSpec(deps, "ABC123")
	require.NoError(t, err)
	require.NotNil(t, spec)

	// meta
	assert.Equal(t, "Graduación Izapa 2025", spec.Meta.PageTitle)
	require.NotNil(t, spec.Meta.MusicUrl)
	assert.Equal(t, musicUrl, *spec.Meta.MusicUrl)
	assert.Equal(t, testEventID.String(), spec.Meta.EventID)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
	assert.Equal(t, "events/grad-izapa-2025/cover.webp", spec.Meta.CoverImageURL)
	require.NotNil(t, spec.Meta.EventDateTime)
	assert.Equal(t, time.Date(2026, time.August, 15, 20, 30, 0, 0, time.UTC), *spec.Meta.EventDateTime)
	assert.Equal(t, "Av. Universidad 123", spec.Meta.Address)
	assert.Equal(t, "Salon Izapa", spec.Meta.SecondAddress)
	assert.Equal(t, "America/Mexico_City", spec.Meta.Timezone)
	assert.Equal(t, "es", spec.Meta.Language)
	assert.Equal(t, "graduation", spec.Meta.EventType)
	assert.True(t, spec.Meta.FooterVisible)

	// sections
	require.Len(t, spec.Sections, 2)
	assert.Equal(t, "CountdownHeader", spec.Sections[0].Type)
	assert.Equal(t, "Cuenta regresiva", spec.Sections[0].Title)
	assert.Equal(t, 1, spec.Sections[0].Order)
	assert.Equal(t, testSectionID1.String(), spec.Sections[0].SectionId)

	// config is valid JSON
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, "EL GRAN DÍA", cfg["heading"])
}

func TestGetPageSpec_Success_NoMusic(t *testing.T) {
	deps := pageSpecDeps{
		getToken:      func(token string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
		getEvent:      func(id uuid.UUID) (*models.Event, error) { return stubEvent(""), nil }, // empty musicUrl
		getSections:   func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
	}

	spec, err := getPageSpec(deps, "ABC123")
	require.NoError(t, err)
	require.NotNil(t, spec)

	// musicUrl must be nil when empty (omitempty in JSON)
	assert.Nil(t, spec.Meta.MusicUrl)
}

func TestBuildPageSpec_UnwrapsLegacyEncodedSectionConfig(t *testing.T) {
	sectionID := mustUUID("dddddddd-1111-0000-0000-000000000004")

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return []models.EventSection{
				{
					ID:            sectionID,
					EventID:       eventID,
					ComponentType: "MAP",
					Config:        `"{\"title\":\"Ubicacion\",\"mapUrl\":\"https://maps.example.com\"}"`,
					Order:         1,
					IsVisible:     true,
				},
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)
	assert.Equal(t, "MAP", spec.Sections[0].Type)

	var config map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &config))
	assert.Equal(t, "Ubicacion", config["title"])
	assert.Equal(t, "https://maps.example.com", config["mapUrl"])
}

func TestBuildPageSpec_InjectsCountdownTargetDateFromEventDateTime(t *testing.T) {
	event := stubEvent("")
	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return []models.EventSection{
				{
					ID:            testSectionID1,
					EventID:       eventID,
					ComponentType: "CountdownHeader",
					Config:        `{"heading":"EL GRAN DIA","targetDate":"2025-01-01T00:00"}`,
					Order:         1,
					IsVisible:     true,
				},
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)

	var config map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &config))
	assert.Equal(t, "EL GRAN DIA", config["heading"])
	assert.Equal(t, event.EventDateTime.Format(time.RFC3339), config["targetDate"])
}

func TestBuildPageSpec_SortsSectionsByOrderWithStableTieBreak(t *testing.T) {
	firstID := mustUUID("dddddddd-1111-0000-0000-000000000001")
	secondID := mustUUID("dddddddd-2222-0000-0000-000000000002")
	thirdID := mustUUID("dddddddd-3333-0000-0000-000000000003")
	fourthID := mustUUID("dddddddd-4444-0000-0000-000000000004")

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return []models.EventSection{
				{ID: fourthID, EventID: eventID, ComponentType: "CustomFourth", Config: `{}`, Order: 3, IsVisible: true},
				{ID: thirdID, EventID: eventID, ComponentType: "CustomThird", Config: `{}`, Order: 2, IsVisible: true},
				{ID: firstID, EventID: eventID, ComponentType: "CustomFirst", Config: `{}`, Order: 1, IsVisible: true},
				{ID: secondID, EventID: eventID, ComponentType: "CustomSecond", Config: `{}`, Order: 2, IsVisible: true},
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 4)
	assert.Equal(t, []string{
		firstID.String(),
		secondID.String(),
		thirdID.String(),
		fourthID.String(),
	}, []string{
		spec.Sections[0].SectionId,
		spec.Sections[1].SectionId,
		spec.Sections[2].SectionId,
		spec.Sections[3].SectionId,
	})
}

func TestBuildPageSpec_ExposesEffectiveThemeFromEventConfig(t *testing.T) {
	templateID := mustUUID("aaaaaaaa-1111-0000-0000-000000000001")
	templatePaletteID := mustUUID("aaaaaaaa-1111-0000-0000-000000000002")
	overridePaletteID := mustUUID("aaaaaaaa-1111-0000-0000-000000000003")
	templateFontSetID := mustUUID("aaaaaaaa-1111-0000-0000-000000000004")
	overrideFontSetID := mustUUID("aaaaaaaa-1111-0000-0000-000000000005")

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				DesignTemplateID: &templateID,
				DesignTemplate: &models.DesignTemplate{
					ID:         templateID,
					Identifier: "classic-elegant",
					ColorPalette: &models.ColorPalette{
						ID:   templatePaletteID,
						Name: "Template palette",
						Patterns: []models.ColorPalettePattern{
							{Key: "primary", Color: models.Color{Value: "#111111"}},
						},
					},
					FontSet: &models.FontSet{
						ID:   templateFontSetID,
						Name: "Template fonts",
						Patterns: []models.FontSetPattern{
							{Key: "heading", Font: models.Font{
								Name:     "Playfair Display",
								Resource: models.Resource{Path: "base/fonts/playfair.woff2"},
							}},
						},
					},
				},
				ColorPaletteID: &overridePaletteID,
				ColorPalette: &models.ColorPalette{
					ID:   overridePaletteID,
					Name: "Override palette",
					Patterns: []models.ColorPalettePattern{
						{Key: "PRIMARY", Color: models.Color{Value: "#c8a45d"}},
						{Key: "background soft", Color: models.Color{Value: "#fff8ec"}},
					},
				},
				FontSetID: &overrideFontSetID,
				FontSet: &models.FontSet{
					ID:   overrideFontSetID,
					Name: "Override fonts",
					Patterns: []models.FontSetPattern{
						{Key: "HEADING", Font: models.Font{
							Name:     "Cormorant Garamond",
							Resource: models.Resource{Path: "base/fonts/cormorant.woff2"},
						}},
						{Key: "body", Font: models.Font{Name: "Inter"}},
					},
				},
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec.Meta.Theme)
	assert.Equal(t, templateID.String(), spec.Meta.Theme.DesignTemplateID)
	assert.Equal(t, "classic-elegant", spec.Meta.Theme.DesignTemplateIdentifier)
	assert.Equal(t, overridePaletteID.String(), spec.Meta.Theme.ColorPaletteID)
	assert.Equal(t, "Override palette", spec.Meta.Theme.ColorPaletteName)
	assert.Equal(t, "#c8a45d", spec.Meta.Theme.Colors["primary"])
	assert.Equal(t, "#fff8ec", spec.Meta.Theme.Colors["background_soft"])
	assert.Equal(t, overrideFontSetID.String(), spec.Meta.Theme.FontSetID)
	assert.Equal(t, "Override fonts", spec.Meta.Theme.FontSetName)
	assert.Equal(t, "Cormorant Garamond", spec.Meta.Theme.Fonts["heading"])
	assert.Equal(t, "Inter", spec.Meta.Theme.Fonts["body"])
	assert.Equal(t, "base/fonts/cormorant.woff2", spec.Meta.Theme.FontURLs["heading"])
	assert.NotContains(t, spec.Meta.Theme.FontURLs, "body")
}

func TestBuildPageSpec_ExposesThemeIDsFromPartialEventConfig(t *testing.T) {
	templateID := mustUUID("aaaaaaaa-2222-0000-0000-000000000001")
	paletteID := mustUUID("aaaaaaaa-2222-0000-0000-000000000002")
	fontSetID := mustUUID("aaaaaaaa-2222-0000-0000-000000000003")

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				DesignTemplateID: &templateID,
				ColorPaletteID:   &paletteID,
				FontSetID:        &fontSetID,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec.Meta.Theme)
	assert.Equal(t, templateID.String(), spec.Meta.Theme.DesignTemplateID)
	assert.Equal(t, paletteID.String(), spec.Meta.Theme.ColorPaletteID)
	assert.Equal(t, fontSetID.String(), spec.Meta.Theme.FontSetID)
	assert.Nil(t, spec.Meta.Theme.Colors)
	assert.Nil(t, spec.Meta.Theme.Fonts)
	assert.Nil(t, spec.Meta.Theme.FontURLs)
}

func TestPageSpecService_GetPageSpecByTokenUsesSpecEventLoader(t *testing.T) {
	rawCalled := false
	specCalled := false

	svc := NewPageSpecService(
		&mockPageSpecAccessTokenRepo{},
		&mockPageSpecInvitationRepo{},
		&mockEventsRepo{
			GetEventByIDRawFunc: func(id uuid.UUID) (*models.Event, error) {
				rawCalled = true
				return &models.Event{ID: id}, nil
			},
			GetEventByIDForSpecFunc: func(id uuid.UUID) (*models.Event, error) {
				specCalled = true
				return stubEvent(""), nil
			},
		},
		&mockEventSectionRepo{
			ListByEventIDForSpecFunc: func(eventID uuid.UUID) ([]models.EventSection, error) {
				return stubSections(), nil
			},
		},
		&mockEventConfigRepo{
			GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
				return NewDefaultEventConfig(id), nil
			},
		},
	)

	spec, err := svc.GetPageSpecByToken("ABC123")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.False(t, rawCalled)
	assert.True(t, specCalled)
	assert.Equal(t, "graduation", spec.Meta.EventType)
}

func TestPageSpecService_GetPageSpecByTokenAcceptsPrettyTokenFallback(t *testing.T) {
	rawLookupCalled := false
	prettyLookupCalled := false

	svc := NewPageSpecService(
		&mockPageSpecAccessTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				rawLookupCalled = true
				assert.Equal(t, "PRETTY123", token)
				return nil, errors.New("record not found")
			},
			GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
				prettyLookupCalled = true
				assert.Equal(t, "PRETTY123", code)
				return &models.InvitationAccessToken{InvitationID: testInvitationID, PrettyToken: code}, nil
			},
		},
		&mockPageSpecInvitationRepo{},
		&mockEventsRepo{
			GetEventByIDForSpecFunc: func(id uuid.UUID) (*models.Event, error) {
				return stubEvent(""), nil
			},
		},
		&mockEventSectionRepo{
			ListByEventIDForSpecFunc: func(eventID uuid.UUID) ([]models.EventSection, error) {
				return stubSections(), nil
			},
		},
		&mockEventConfigRepo{
			GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
				return NewDefaultEventConfig(id), nil
			},
		},
	)

	spec, err := svc.GetPageSpecByToken(" PRETTY123 ")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.True(t, rawLookupCalled)
	assert.True(t, prettyLookupCalled)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
}

func TestGetPageSpecByToken_RejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expiredAt := now.Add(-time.Hour)
	invitationLoaded := false
	eventLoaded := false

	deps := pageSpecDeps{
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				InvitationID: testInvitationID,
				ExpiresAt:    &expiredAt,
			}, nil
		},
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) {
			invitationLoaded = true
			return stubInvitation(), nil
		},
		getEvent: func(id uuid.UUID) (*models.Event, error) {
			eventLoaded = true
			return stubEvent(""), nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			t.Fatal("sections should not load when token is expired")
			return nil, nil
		},
		now: func() time.Time { return now },
	}

	spec, err := getPageSpec(deps, "expired-token")
	assert.Nil(t, spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token expired")
	assert.False(t, invitationLoaded)
	assert.False(t, eventLoaded)
}

func TestGetPageSpecByToken_UsesInjectedClockForTokenExpiry(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Minute)

	deps := pageSpecDeps{
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{
				InvitationID: testInvitationID,
				ExpiresAt:    &expiresAt,
			}, nil
		},
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) {
			return stubInvitation(), nil
		},
		getEvent: func(id uuid.UUID) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return stubSections(), nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return NewDefaultEventConfig(id), nil
		},
		now: func() time.Time { return now },
	}

	spec, err := getPageSpec(deps, "valid-at-injected-now")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
}

func TestGetPageSpecByToken_BlocksInactiveEvent(t *testing.T) {
	deps := pageSpecDeps{
		getToken:      func(token string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
		getEvent: func(id uuid.UUID) (*models.Event, error) {
			event := stubEvent("")
			event.IsActive = false
			return event, nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			t.Fatal("sections should not load when event is inactive")
			return nil, nil
		},
	}

	spec, err := getPageSpec(deps, "ABC123")
	assert.Nil(t, spec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPageSpecInactive))
}

func TestGetPageSpecByIdentifier_BlocksPrivateEventWithoutPreviewToken(t *testing.T) {
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			t.Fatal("sections should not load when public access is blocked")
			return nil, nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: false}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool { return false },
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "")
	assert.Nil(t, spec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPageSpecNotPublic))
}

func TestGetPageSpecByIdentifier_AllowsPrivateEventWithPreviewToken(t *testing.T) {
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return stubSections(), nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: false}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool {
			return token == "preview-ok" && eventID == testEventID
		},
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "preview-ok", "")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
	require.NotNil(t, spec.Meta.Access)
	assert.True(t, spec.Meta.Access.PreviewAuthorized)
	require.Len(t, spec.Sections, 2)
}

func TestGetPageSpecByIdentifier_MarksPublicEventPreviewAuthorizedWithValidPreviewToken(t *testing.T) {
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return stubSections(), nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: true}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool {
			return token == "preview-ok" && eventID == testEventID
		},
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "preview-ok", "")
	require.NoError(t, err)
	require.NotNil(t, spec)
	require.NotNil(t, spec.Meta.Access)
	assert.True(t, spec.Meta.Access.PreviewAuthorized)
}

func TestGetPageSpecByIdentifier_AllowsPublicEventWithoutPreviewToken(t *testing.T) {
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return stubSections(), nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: true}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool {
			t.Fatal("public events should not validate preview tokens")
			return false
		},
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
	require.Len(t, spec.Sections, 2)
}

func TestGetPageSpecByIdentifier_BlocksInactivePublicEventWithoutPreviewToken(t *testing.T) {
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			event := stubEvent("")
			event.IsActive = false
			return event, nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			t.Fatal("sections should not load when inactive public access is blocked")
			return nil, nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: true}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool {
			assert.Empty(t, token)
			assert.Equal(t, testEventID, eventID)
			return false
		},
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "")
	assert.Nil(t, spec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPageSpecInactive))
}

func TestGetPageSpecByIdentifier_AllowsInactiveEventWithPreviewToken(t *testing.T) {
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			event := stubEvent("")
			event.IsActive = false
			return event, nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return stubSections(), nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: false}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool {
			return token == "preview-ok" && eventID == testEventID
		},
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "preview-ok", "")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
	require.NotNil(t, spec.Meta.Access)
	assert.True(t, spec.Meta.Access.PreviewAuthorized)
}

func TestGetPageSpecByIdentifier_BlocksInactiveEventWithInvitationToken(t *testing.T) {
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			event := stubEvent("")
			event.IsActive = false
			return event, nil
		},
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			t.Fatal("invitation tokens should not reopen inactive events")
			return nil, nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			t.Fatal("sections should not load when event is inactive")
			return nil, nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: false}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool { return false },
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "invite-ok")
	assert.Nil(t, spec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPageSpecInactive))
}

func TestGetPageSpecByIdentifier_AllowsPrivateEventWithInvitationToken(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			if token != "invite-ok" {
				return nil, errors.New("not found")
			}
			return &models.InvitationAccessToken{InvitationID: invitationID}, nil
		},
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: id, EventID: testEventID}, nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return stubSections(), nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: false}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool { return false },
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "invite-ok")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
	require.Len(t, spec.Sections, 2)
}

func TestGetPageSpecByIdentifier_DoesNotAuthorizePreviewWhenInvitationTokenAllowsAccess(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			if token != "invite-ok" {
				return nil, errors.New("not found")
			}
			return &models.InvitationAccessToken{InvitationID: invitationID}, nil
		},
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: id, EventID: testEventID}, nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return stubSections(), nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: false}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool {
			return token == "preview-ok" && eventID == testEventID
		},
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "bad-preview", "invite-ok")
	require.NoError(t, err)
	require.NotNil(t, spec)
	require.NotNil(t, spec.Meta.Access)
	assert.False(t, spec.Meta.Access.PreviewAuthorized)
	require.Len(t, spec.Sections, 2)
}

func TestPageSpecService_GetPageSpecByIdentifierAcceptsPrettyTokenFallback(t *testing.T) {
	rawLookupCalled := false
	prettyLookupCalled := false

	svc := NewPageSpecService(
		&mockPageSpecAccessTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				rawLookupCalled = true
				assert.Equal(t, "PRETTY123", token)
				return nil, errors.New("record not found")
			},
			GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
				prettyLookupCalled = true
				assert.Equal(t, "PRETTY123", code)
				return &models.InvitationAccessToken{InvitationID: testInvitationID, PrettyToken: code}, nil
			},
		},
		&mockPageSpecInvitationRepo{},
		&mockEventsRepo{
			GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
				assert.Equal(t, "grad-izapa-2025", identifier)
				return stubEvent(""), nil
			},
		},
		&mockEventSectionRepo{
			ListByEventIDForSpecFunc: func(eventID uuid.UUID) ([]models.EventSection, error) {
				return stubSections(), nil
			},
		},
		&mockEventConfigRepo{
			GetEventConfigByIDFunc: func(id uuid.UUID) (*models.EventConfig, error) {
				return &models.EventConfig{IsPublic: false}, nil
			},
		},
	)

	spec, err := svc.GetPageSpecByIdentifier("grad-izapa-2025", "", " PRETTY123 ")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.True(t, rawLookupCalled)
	assert.True(t, prettyLookupCalled)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
	require.Len(t, spec.Sections, 2)
}

func TestGetPageSpecByIdentifier_BlocksPrivateEventWithExpiredInvitationToken(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expiredAt := now.Add(-time.Hour)
	invitationLoaded := false

	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			if token != "invite-expired" {
				return nil, errors.New("not found")
			}
			return &models.InvitationAccessToken{
				InvitationID: testInvitationID,
				ExpiresAt:    &expiredAt,
			}, nil
		},
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) {
			invitationLoaded = true
			return &models.Invitation{ID: id, EventID: testEventID}, nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			t.Fatal("sections should not load when invitation token is expired")
			return nil, nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: false}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool { return false },
		now:                  func() time.Time { return now },
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "invite-expired")
	assert.Nil(t, spec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPageSpecNotPublic))
	assert.False(t, invitationLoaded)
}

func TestGetPageSpecByIdentifier_UsesInjectedClockForInvitationTokenExpiry(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Minute)

	deps := identifierPageSpecDeps{
		getEventByIdentifier: func(identifier string) (*models.Event, error) {
			return stubEvent(""), nil
		},
		getToken: func(token string) (*models.InvitationAccessToken, error) {
			if token != "invite-ok" {
				return nil, errors.New("not found")
			}
			return &models.InvitationAccessToken{
				InvitationID: testInvitationID,
				ExpiresAt:    &expiresAt,
			}, nil
		},
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: id, EventID: testEventID}, nil
		},
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
			return stubSections(), nil
		},
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{IsPublic: false}, nil
		},
		validatePreviewToken: func(token string, eventID uuid.UUID) bool { return false },
		now:                  func() time.Time { return now },
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "invite-ok")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, "grad-izapa-2025", spec.Meta.Identifier)
}

func TestGetPageSpec_EmptyConfig_DefaultsToObject(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "PhotoGrid",
			Config:        "", // empty — should default to {}
			Order:         1,
			IsVisible:     true,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		},
	}

	deps := pageSpecDeps{
		getToken:      func(token string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
		getEvent:      func(id uuid.UUID) (*models.Event, error) { return stubEvent(""), nil },
		getSections:   func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
	}

	spec, err := getPageSpec(deps, "ABC123")
	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)

	// empty config must be serialized as {} not null
	assert.Equal(t, `{}`, string(spec.Sections[0].Config))
}

func TestGetPageSpec_MomentWallInjectsRuntimeConfig(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "MomentWall",
			Config:        `{"title":"Momentos"}`,
			Order:         1,
			IsVisible:     true,
		},
	}

	deps := pageSpecDeps{
		getToken:      func(token string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
		getEvent:      func(id uuid.UUID) (*models.Event, error) { return stubEvent(""), nil },
		getSections:   func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				AllowUploads:                true,
				AllowMessages:               true,
				AutoApproveUploads:          true,
				ShowMomentWall:              true,
				ShareUploadsEnabled:         true,
				MaxUploadsPerGuest:          12,
				DefaultMomentRequestMessage: "Comparte tus mejores recuerdos con nosotros",
			}, nil
		},
	}

	spec, err := getPageSpec(deps, "ABC123")
	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, "grad-izapa-2025", cfg["identifier"])
	assert.Equal(t, false, cfg["allow_uploads"])
	assert.Equal(t, true, cfg["allow_messages"])
	assert.Equal(t, true, cfg["auto_approve_uploads"])
	assert.Equal(t, true, cfg["published"])
	assert.Equal(t, true, cfg["moments_wall_published"])
	assert.Equal(t, true, cfg["show_moment_wall"])
	assert.Equal(t, false, cfg["share_uploads_enabled"])
	assert.Equal(t, float64(12), cfg["max_uploads_per_guest"])
	assert.Equal(t, "Comparte tus mejores recuerdos con nosotros", cfg["moment_request_message"])
	assert.Equal(t, "Comparte tus mejores recuerdos con nosotros", cfg["subtitle"])
}

func TestGetPageSpec_MomentWallInjectsRuntimeConfigIntoNonObjectConfig(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "MomentWall",
			Config:        `[]`,
			Order:         1,
			IsVisible:     true,
		},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				AllowUploads:        true,
				AllowMessages:       true,
				ShowMomentWall:      false,
				ShareUploadsEnabled: true,
				MaxUploadsPerGuest:  9,
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, "grad-izapa-2025", cfg["identifier"])
	assert.Equal(t, true, cfg["allow_uploads"])
	assert.Equal(t, true, cfg["allow_messages"])
	assert.Equal(t, false, cfg["published"])
	assert.Equal(t, false, cfg["moments_wall_published"])
	assert.Equal(t, false, cfg["show_moment_wall"])
	assert.Equal(t, true, cfg["share_uploads_enabled"])
	assert.Equal(t, float64(9), cfg["max_uploads_per_guest"])
}

func TestGetPageSpec_MomentWallExposesEffectiveSharedUploadFlag(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "MomentWall",
			Config:        `{"title":"Momentos"}`,
			Order:         1,
			IsVisible:     true,
		},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				AllowUploads:        false,
				ShowMomentWall:      true,
				ShareUploadsEnabled: true,
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, false, cfg["allow_uploads"])
	assert.Equal(t, false, cfg["share_uploads_enabled"])
}

func TestGetPageSpec_MomentWallKeepsSharedUploadOpenBeforePublishing(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "MomentWall",
			Config:        `{"title":"Momentos"}`,
			Order:         1,
			IsVisible:     true,
		},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				AllowUploads:        true,
				ShowMomentWall:      false,
				ShareUploadsEnabled: true,
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, true, cfg["allow_uploads"])
	assert.Equal(t, true, cfg["share_uploads_enabled"])
	assert.Equal(t, false, cfg["published"])
	assert.Equal(t, false, cfg["moments_wall_published"])
	assert.Equal(t, false, cfg["show_moment_wall"])
}

func TestBuildPageSpec_InjectsRSVPMessagesFromEventConfig(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "RSVPConfirmation",
			Config:        `{}`,
			Order:         1,
			IsVisible:     true,
		},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				ShowRSVPSection:             true,
				DefaultWelcomeMessage:       "Nos emociona celebrar contigo",
				DefaultThankYouMessage:      "Gracias por confirmar, nos vemos pronto",
				DefaultGuestSignatureTitle:  "Dejanos un mensaje",
				DefaultMomentRequestMessage: "No pertenece a RSVP",
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, "Nos emociona celebrar contigo", cfg["welcome_message"])
	assert.Equal(t, "Gracias por confirmar, nos vemos pronto", cfg["thank_you_message"])
	assert.Equal(t, "Dejanos un mensaje", cfg["guest_signature_title"])
	assert.NotContains(t, cfg, "moment_request_message")
}

func TestGetPageSpec_MomentWallUsesDefaultConfigAsPublished(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "MomentWall",
			Config:        `{"title":"Momentos"}`,
			Order:         1,
			IsVisible:     true,
		},
	}

	deps := pageSpecDeps{
		getToken:      func(token string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
		getInvitation: func(id uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
		getEvent:      func(id uuid.UUID) (*models.Event, error) { return stubEvent(""), nil },
		getSections:   func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return NewDefaultEventConfig(id), nil
		},
	}

	spec, err := getPageSpec(deps, "ABC123")
	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, true, cfg["published"])
	assert.Equal(t, true, cfg["moments_wall_published"])
	assert.Equal(t, false, cfg["allow_uploads"])
	assert.Equal(t, false, cfg["allow_messages"])
	assert.Equal(t, false, cfg["share_uploads_enabled"])
}

func TestBuildPageSpec_UsesDefaultConfigWhenConfigLoadFails(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "MomentWall",
			Config:        `{"title":"Momentos"}`,
			Order:         1,
			IsVisible:     true,
		},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return nil, errors.New("config unavailable")
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.True(t, spec.Meta.FooterVisible)
	require.Len(t, spec.Sections, 1)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, "grad-izapa-2025", cfg["identifier"])
	assert.Equal(t, true, cfg["published"])
	assert.Equal(t, true, cfg["moments_wall_published"])
	assert.Equal(t, false, cfg["allow_uploads"])
	assert.Equal(t, false, cfg["allow_messages"])
	assert.Equal(t, false, cfg["share_uploads_enabled"])
}

func TestBuildPageSpec_FiltersSectionsByEventConfig(t *testing.T) {
	sections := []models.EventSection{
		{ID: testSectionID1, EventID: testEventID, ComponentType: "CountdownHeader", Config: `{}`, Order: 1, IsVisible: true},
		{ID: testSectionID2, EventID: testEventID, ComponentType: "RSVPConfirmation", Config: `{}`, Order: 2, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000006"), EventID: testEventID, ComponentType: "EventVenue", Config: `{}`, Order: 3, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000007"), EventID: testEventID, ComponentType: "Reception", Config: `{}`, Order: 4, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000008"), EventID: testEventID, ComponentType: "PhotoGrid", Config: `{}`, Order: 5, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000009"), EventID: testEventID, ComponentType: "MomentWall", Config: `{"title":"Momentos"}`, Order: 6, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000010"), EventID: testEventID, ComponentType: "Agenda", Config: `{}`, Order: 7, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000011"), EventID: testEventID, ComponentType: "GraduationHero", Config: `{}`, Order: 8, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000012"), EventID: testEventID, ComponentType: "GraduatesList", Config: `{}`, Order: 9, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000020"), EventID: testEventID, ComponentType: "Hosts", Config: `{}`, Order: 9, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000021"), EventID: testEventID, ComponentType: "HostSection", Config: `{}`, Order: 9, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000022"), EventID: testEventID, ComponentType: "HostsSection", Config: `{}`, Order: 9, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000018"), EventID: testEventID, ComponentType: "AgendaSection", Config: `{}`, Order: 10, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000013"), EventID: testEventID, ComponentType: "SCHEDULE", Config: `{}`, Order: 10, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000014"), EventID: testEventID, ComponentType: "GALLERY", Config: `{}`, Order: 11, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000015"), EventID: testEventID, ComponentType: "HERO", Config: `{}`, Order: 12, IsVisible: true},
	}
	event := stubEvent("")
	event.OrganizerName = "Eventi"

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				ShowCountdown:      true,
				ShowRSVPSection:    false,
				ShowEventLocation:  true,
				ShowSecondLocation: false,
				ShowPhotoGallery:   false,
				ShowMomentWall:     true,
				ShowContactSection: false,
				ShowEventSchedule:  false,
				ShowHeader:         true,
				ShowHostsSection:   false,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Nil(t, spec.Meta.Contact)
	require.Len(t, spec.Sections, 5)
	assert.Equal(t, "CountdownHeader", spec.Sections[0].Type)
	assert.Equal(t, "EventVenue", spec.Sections[1].Type)
	assert.Equal(t, "MomentWall", spec.Sections[2].Type)
	assert.Equal(t, "GraduationHero", spec.Sections[3].Type)
	assert.Equal(t, "HERO", spec.Sections[4].Type)
}

func TestBuildPageSpec_FiltersHiddenSectionsFromRepositoryResults(t *testing.T) {
	hiddenID := mustUUID("eeeeeeee-0000-0000-0000-000000000030")
	visibleID := mustUUID("eeeeeeee-0000-0000-0000-000000000031")
	sections := []models.EventSection{
		{ID: hiddenID, EventID: testEventID, ComponentType: "CountdownHeader", Config: `{}`, Order: 1, IsVisible: false},
		{ID: visibleID, EventID: testEventID, ComponentType: "GraduationHero", Config: `{}`, Order: 2, IsVisible: true},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				VisibilityConfigured: true,
				ShowCountdown:        true,
				ShowHeader:           true,
				ShowFooter:           true,
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)
	assert.Equal(t, visibleID.String(), spec.Sections[0].SectionId)
	assert.Equal(t, "GraduationHero", spec.Sections[0].Type)
}

func TestBuildPageSpec_FiltersSectionsWithoutComponentType(t *testing.T) {
	validID := mustUUID("eeeeeeee-0000-0000-0000-000000000032")
	sections := []models.EventSection{
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000033"), EventID: testEventID, ComponentType: " ", Config: `{}`, Order: 1, IsVisible: true},
		{ID: validID, EventID: testEventID, ComponentType: "PhotoGrid", Config: `{}`, Order: 2, IsVisible: true},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				VisibilityConfigured: true,
				ShowPhotoGallery:     true,
				ShowFooter:           true,
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)
	assert.Equal(t, validID.String(), spec.Sections[0].SectionId)
	assert.Equal(t, "PhotoGrid", spec.Sections[0].Type)
}

func TestBuildPageSpec_AppliesVisibilityAndRuntimeConfigToSectionAliases(t *testing.T) {
	sections := []models.EventSection{
		{ID: testSectionID1, EventID: testEventID, ComponentType: "RSVP", Config: `{}`, Order: 1, IsVisible: true},
		{ID: testSectionID2, EventID: testEventID, ComponentType: "MOMENT_WALL", Config: `{"title":"Momentos"}`, Order: 2, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000023"), EventID: testEventID, ComponentType: "HOSTS", Config: `{}`, Order: 3, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000024"), EventID: testEventID, ComponentType: "PHOTO_GRID", Config: `{}`, Order: 4, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000025"), EventID: testEventID, ComponentType: "AGENDA", Config: `{}`, Order: 5, IsVisible: true},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				ShowRSVPSection:     false,
				ShowMomentWall:      true,
				ShowHostsSection:    false,
				ShowPhotoGallery:    false,
				ShowEventSchedule:   false,
				AllowUploads:        true,
				AllowMessages:       true,
				ShareUploadsEnabled: true,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	require.Len(t, spec.Sections, 1)
	assert.Equal(t, "MOMENT_WALL", spec.Sections[0].Type)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, "grad-izapa-2025", cfg["identifier"])
	assert.Equal(t, true, cfg["published"])
	assert.Equal(t, true, cfg["moments_wall_published"])
	assert.Equal(t, false, cfg["allow_uploads"])
	assert.Equal(t, true, cfg["allow_messages"])
	assert.Equal(t, false, cfg["share_uploads_enabled"])
}

func TestPageSpecSectionKindKeepsDashboardAliasesAlignedWithPublicRenderer(t *testing.T) {
	tests := map[string]string{
		"GraduatesList": "GraduatesList",
		"HOST_SECTION":  "Hosts",
		"hosts-section": "Hosts",
		"HERO":          "HERO",
		"LegacyHero":    "HERO",
		"TEXT":          "TEXT",
		"LegacyText":    "TEXT",
		"GALLERY":       "GALLERY",
		"LegacyGallery": "GALLERY",
		"MAP":           "MAP",
		"LegacyMap":     "MAP",
		"MUSIC":         "MUSIC",
		"LegacyMusic":   "MUSIC",
	}

	for sectionType, expectedKind := range tests {
		assert.Equal(t, expectedKind, pageSpecSectionKind(sectionType), sectionType)
	}
}

func TestBuildPageSpec_PreservesExplicitAllFalseVisibility(t *testing.T) {
	sections := []models.EventSection{
		{ID: testSectionID1, EventID: testEventID, ComponentType: "CountdownHeader", Config: `{}`, Order: 1, IsVisible: true},
		{ID: testSectionID2, EventID: testEventID, ComponentType: "RSVPConfirmation", Config: `{}`, Order: 2, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000006"), EventID: testEventID, ComponentType: "EventVenue", Config: `{}`, Order: 3, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000008"), EventID: testEventID, ComponentType: "PhotoGrid", Config: `{}`, Order: 4, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000009"), EventID: testEventID, ComponentType: "MomentWall", Config: `{}`, Order: 5, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000010"), EventID: testEventID, ComponentType: "Agenda", Config: `{}`, Order: 6, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000011"), EventID: testEventID, ComponentType: "GraduationHero", Config: `{}`, Order: 7, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000020"), EventID: testEventID, ComponentType: "Hosts", Config: `{}`, Order: 8, IsVisible: true},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{VisibilityConfigured: true}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.False(t, spec.Meta.FooterVisible)
	assert.Empty(t, spec.Sections)
}

func TestBuildPageSpec_FiltersLegacyHeroByHeaderConfig(t *testing.T) {
	sections := []models.EventSection{
		{ID: testSectionID1, EventID: testEventID, ComponentType: "HERO", Config: `{}`, Order: 1, IsVisible: true},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				ShowCountdown: true,
				ShowHeader:    false,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Empty(t, spec.Sections)
}

func TestBuildPageSpec_FiltersLegacyMapByLocationConfig(t *testing.T) {
	sections := []models.EventSection{
		{ID: testSectionID1, EventID: testEventID, ComponentType: "MAP", Config: `{"mapUrl":"https://maps.example/embed"}`, Order: 1, IsVisible: true},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				ShowCountdown:     true,
				ShowEventLocation: false,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Empty(t, spec.Sections)
}

func TestBuildPageSpec_FiltersLongLegacyAliasesByConfig(t *testing.T) {
	sections := []models.EventSection{
		{ID: testSectionID1, EventID: testEventID, ComponentType: "LegacyHero", Config: `{}`, Order: 1, IsVisible: true},
		{ID: testSectionID2, EventID: testEventID, ComponentType: "LegacyMap", Config: `{}`, Order: 2, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000016"), EventID: testEventID, ComponentType: "LegacyGallery", Config: `{}`, Order: 3, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000017"), EventID: testEventID, ComponentType: "LegacySchedule", Config: `{}`, Order: 4, IsVisible: true},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				ShowCountdown:     true,
				ShowHeader:        false,
				ShowEventLocation: false,
				ShowPhotoGallery:  false,
				ShowEventSchedule: false,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Empty(t, spec.Sections)
}

func TestBuildPageSpec_UsesFooterVisibilityFromEventConfig(t *testing.T) {
	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				ShowCountdown:   true,
				ShowRSVPSection: true,
				ShowHeader:      true,
				ShowFooter:      false,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.False(t, spec.Meta.FooterVisible)
}

func TestBuildPageSpec_ExposesAccessVersionFromEventConfig(t *testing.T) {
	updatedAt := time.Date(2026, 7, 7, 21, 15, 0, 123, time.FixedZone("CST", -6*60*60))

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				AuthPasswordPreview: "secreto",
				UpdatedAt:           updatedAt,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	require.NotNil(t, spec.Meta.Access)
	assert.True(t, spec.Meta.Access.PasswordProtected)
	assert.Equal(t, updatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.Access.AccessVersion)
}

func TestBuildPageSpec_ExposesContentVersionFromLatestEventConfigOrSectionChange(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	configUpdatedAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	sectionUpdatedAt := time.Date(2026, 7, 7, 14, 30, 0, 123, time.FixedZone("CST", -6*60*60))
	event := stubEvent("")
	event.UpdatedAt = eventUpdatedAt
	sections := stubSections()
	sections[0].UpdatedAt = sectionUpdatedAt

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{UpdatedAt: configUpdatedAt}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, sectionUpdatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.ContentVersion)
}

func TestBuildPageSpec_ExposesContentVersionFromLatestResourceChange(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	configUpdatedAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	resourceUpdatedAt := time.Date(2026, 7, 7, 16, 45, 0, 456, time.FixedZone("CST", -6*60*60))
	event := stubEvent("")
	event.UpdatedAt = eventUpdatedAt
	sections := append(stubSections(), models.EventSection{
		ID:            mustUUID("eeeeeeee-0000-0000-0000-000000000030"),
		EventID:       testEventID,
		Title:         "Graduados",
		ComponentType: "GraduatesList",
		Config:        `{}`,
		Order:         3,
		IsVisible:     true,
	})

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{UpdatedAt: configUpdatedAt}, nil
		},
		getResourceVersion: func(eventID uuid.UUID) (*time.Time, error) {
			return &resourceUpdatedAt, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, resourceUpdatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.ContentVersion)
}

func TestBuildPageSpec_ExposesContentVersionFromLatestVisibleResourceSectionChange(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	configUpdatedAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	resourceUpdatedAt := time.Date(2026, 7, 7, 16, 45, 0, 456, time.FixedZone("CST", -6*60*60))
	event := stubEvent("")
	event.UpdatedAt = eventUpdatedAt
	visibleResourceSectionID := mustUUID("eeeeeeee-0000-0000-0000-000000000032")
	hiddenResourceSectionID := mustUUID("eeeeeeee-0000-0000-0000-000000000033")
	configHiddenResourceSectionID := mustUUID("eeeeeeee-0000-0000-0000-000000000034")
	textOnlySectionID := mustUUID("eeeeeeee-0000-0000-0000-000000000035")
	sections := []models.EventSection{
		{ID: visibleResourceSectionID, EventID: testEventID, ComponentType: "PhotoGrid", Config: `{}`, Order: 1, IsVisible: true},
		{ID: hiddenResourceSectionID, EventID: testEventID, ComponentType: "PhotoGrid", Config: `{}`, Order: 2, IsVisible: false},
		{ID: configHiddenResourceSectionID, EventID: testEventID, ComponentType: "RSVPConfirmation", Config: `{}`, Order: 3, IsVisible: true},
		{ID: textOnlySectionID, EventID: testEventID, ComponentType: "Contact", Config: `{}`, Order: 4, IsVisible: true},
	}
	var requestedSectionIDs []uuid.UUID

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				UpdatedAt:            configUpdatedAt,
				VisibilityConfigured: true,
				ShowPhotoGallery:     true,
				ShowRSVPSection:      false,
				ShowContactSection:   true,
			}, nil
		},
		getResourceVersion: func(eventID uuid.UUID) (*time.Time, error) {
			t.Fatalf("expected section-scoped resource version lookup, got event-scoped lookup for %s", eventID)
			return nil, nil
		},
		getResourceVersionBySectionIDs: func(sectionIDs []uuid.UUID) (*time.Time, error) {
			requestedSectionIDs = append([]uuid.UUID(nil), sectionIDs...)
			return &resourceUpdatedAt, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, []uuid.UUID{visibleResourceSectionID}, requestedSectionIDs)
	assert.Equal(t, resourceUpdatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.ContentVersion)
}

func TestBuildPageSpec_IgnoresResourceVersionWithoutVisibleResourceSections(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	configUpdatedAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	resourceUpdatedAt := time.Date(2026, 7, 7, 16, 45, 0, 456, time.UTC)
	event := stubEvent("")
	event.UpdatedAt = eventUpdatedAt
	sections := []models.EventSection{
		{
			ID:            mustUUID("eeeeeeee-0000-0000-0000-000000000036"),
			EventID:       testEventID,
			ComponentType: "PhotoGrid",
			Config:        `{}`,
			Order:         1,
			IsVisible:     false,
		},
		{
			ID:            mustUUID("eeeeeeee-0000-0000-0000-000000000037"),
			EventID:       testEventID,
			ComponentType: "GraduationHero",
			Config:        `{}`,
			Order:         2,
			IsVisible:     true,
		},
		{
			ID:            mustUUID("eeeeeeee-0000-0000-0000-000000000038"),
			EventID:       testEventID,
			ComponentType: "Contact",
			Config:        `{}`,
			Order:         3,
			IsVisible:     true,
		},
	}
	resourceVersionCalled := false

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				UpdatedAt:            configUpdatedAt,
				VisibilityConfigured: true,
				ShowHeader:           false,
				ShowContactSection:   true,
			}, nil
		},
		getResourceVersionBySectionIDs: func(sectionIDs []uuid.UUID) (*time.Time, error) {
			resourceVersionCalled = true
			return &resourceUpdatedAt, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.False(t, resourceVersionCalled)
	assert.Equal(t, configUpdatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.ContentVersion)
}

func TestBuildPageSpec_ExposesContentVersionFromLatestPublicAttendeeChange(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	configUpdatedAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	resourceUpdatedAt := time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC)
	attendeeUpdatedAt := time.Date(2026, 7, 7, 18, 15, 0, 789, time.FixedZone("CST", -6*60*60))
	event := stubEvent("")
	event.UpdatedAt = eventUpdatedAt
	sections := append(stubSections(), models.EventSection{
		ID:            mustUUID("eeeeeeee-0000-0000-0000-000000000031"),
		EventID:       testEventID,
		Title:         "Graduados",
		ComponentType: "GraduatesList",
		Config:        `{}`,
		Order:         3,
		IsVisible:     true,
	})

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{UpdatedAt: configUpdatedAt}, nil
		},
		getResourceVersion: func(eventID uuid.UUID) (*time.Time, error) {
			return &resourceUpdatedAt, nil
		},
		getGuestVersion: func(eventID uuid.UUID) (*time.Time, error) {
			return &attendeeUpdatedAt, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, attendeeUpdatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.ContentVersion)
}

func TestBuildPageSpec_IgnoresPublicAttendeeVersionWithoutVisibleAttendeeSection(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	configUpdatedAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	resourceUpdatedAt := time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC)
	attendeeUpdatedAt := time.Date(2026, 7, 8, 18, 15, 0, 789, time.UTC)
	event := stubEvent("")
	event.UpdatedAt = eventUpdatedAt
	guestVersionCalled := false

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{UpdatedAt: configUpdatedAt}, nil
		},
		getResourceVersion: func(eventID uuid.UUID) (*time.Time, error) {
			return &resourceUpdatedAt, nil
		},
		getGuestVersion: func(eventID uuid.UUID) (*time.Time, error) {
			guestVersionCalled = true
			return &attendeeUpdatedAt, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.False(t, guestVersionCalled)
	assert.Equal(t, resourceUpdatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.ContentVersion)
}

func TestBuildPageSpec_IgnoresPublicAttendeeVersionWhenAttendeeSectionHiddenByConfig(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	configUpdatedAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	resourceUpdatedAt := time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC)
	attendeeUpdatedAt := time.Date(2026, 7, 8, 18, 15, 0, 789, time.UTC)
	event := stubEvent("")
	event.UpdatedAt = eventUpdatedAt
	sections := append(stubSections(), models.EventSection{
		ID:            mustUUID("eeeeeeee-0000-0000-0000-000000000031"),
		EventID:       testEventID,
		Title:         "Graduados",
		ComponentType: "GraduatesList",
		Config:        `{}`,
		Order:         3,
		IsVisible:     true,
	})
	guestVersionCalled := false

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				UpdatedAt:            configUpdatedAt,
				VisibilityConfigured: true,
				ShowHostsSection:     false,
			}, nil
		},
		getResourceVersion: func(eventID uuid.UUID) (*time.Time, error) {
			return &resourceUpdatedAt, nil
		},
		getGuestVersion: func(eventID uuid.UUID) (*time.Time, error) {
			guestVersionCalled = true
			return &attendeeUpdatedAt, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.False(t, guestVersionCalled)
	assert.Equal(t, resourceUpdatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.ContentVersion)
}

func TestBuildPageSpec_ExposesContentVersionFromLatestPublicMomentChange(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	configUpdatedAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	resourceUpdatedAt := time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC)
	attendeeUpdatedAt := time.Date(2026, 7, 7, 18, 15, 0, 789, time.UTC)
	momentUpdatedAt := time.Date(2026, 7, 8, 9, 20, 0, 321, time.FixedZone("CST", -6*60*60))
	event := stubEvent("")
	event.UpdatedAt = eventUpdatedAt

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{UpdatedAt: configUpdatedAt}, nil
		},
		getResourceVersion: func(eventID uuid.UUID) (*time.Time, error) {
			return &resourceUpdatedAt, nil
		},
		getGuestVersion: func(eventID uuid.UUID) (*time.Time, error) {
			return &attendeeUpdatedAt, nil
		},
		getMomentVersion: func(eventID uuid.UUID) (*time.Time, error) {
			return &momentUpdatedAt, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, momentUpdatedAt.UTC().Format(time.RFC3339Nano), spec.Meta.ContentVersion)
}

func TestBuildPageSpec_LoadsIndependentContentVersionsConcurrently(t *testing.T) {
	resourceUpdatedAt := time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC)
	attendeeUpdatedAt := time.Date(2026, 7, 7, 15, 0, 0, 0, time.UTC)
	momentUpdatedAt := time.Date(2026, 7, 7, 16, 0, 0, 0, time.UTC)
	sections := append(stubSections(), models.EventSection{
		ID:            mustUUID("eeeeeeee-0000-0000-0000-000000000039"),
		EventID:       testEventID,
		ComponentType: "GraduatesList",
		Config:        `{}`,
		Order:         3,
		IsVisible:     true,
	})

	started := make(chan string, 3)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	type buildResult struct {
		contentVersion string
		err            error
	}
	result := make(chan buildResult, 1)
	go func() {
		spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
			getSections: func(eventID uuid.UUID) ([]models.EventSection, error) {
				return sections, nil
			},
			getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
				return &models.EventConfig{}, nil
			},
			getResourceVersionBySectionIDs: func(sectionIDs []uuid.UUID) (*time.Time, error) {
				started <- "resources"
				<-release
				return &resourceUpdatedAt, nil
			},
			getGuestVersion: func(eventID uuid.UUID) (*time.Time, error) {
				started <- "guests"
				<-release
				return &attendeeUpdatedAt, nil
			},
			getMomentVersion: func(eventID uuid.UUID) (*time.Time, error) {
				started <- "moments"
				<-release
				return &momentUpdatedAt, nil
			},
		})
		contentVersion := ""
		if spec != nil {
			contentVersion = spec.Meta.ContentVersion
		}
		result <- buildResult{contentVersion: contentVersion, err: err}
	}()

	startedLoaders := make(map[string]bool, 3)
	for len(startedLoaders) < 3 {
		select {
		case name := <-started:
			startedLoaders[name] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("content-version queries did not overlap; started before release: %v", startedLoaders)
		}
	}
	close(release)
	released = true

	select {
	case got := <-result:
		require.NoError(t, got.err)
		assert.Equal(t, momentUpdatedAt.Format(time.RFC3339Nano), got.contentVersion)
	case <-time.After(time.Second):
		t.Fatal("page spec build did not complete after releasing version queries")
	}
}

func TestBuildPageSpec_TreatsBlankPasswordPreviewAsUnprotected(t *testing.T) {
	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{AuthPasswordPreview: "   "}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	require.NotNil(t, spec.Meta.Access)
	assert.False(t, spec.Meta.Access.PasswordProtected)
}

func TestBuildPageSpec_KeepsMomentWallForOpenPersonalUploads(t *testing.T) {
	sections := []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "MomentWall",
			Config:        `{"title":"Momentos"}`,
			Order:         1,
			IsVisible:     true,
		},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				AllowUploads:        true,
				AllowMessages:       true,
				ShowMomentWall:      false,
				ShareUploadsEnabled: false,
			}, nil
		},
	})

	require.NoError(t, err)
	require.Len(t, spec.Sections, 1)
	assert.Equal(t, "MomentWall", spec.Sections[0].Type)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[0].Config, &cfg))
	assert.Equal(t, "grad-izapa-2025", cfg["identifier"])
	assert.Equal(t, true, cfg["allow_uploads"])
	assert.Equal(t, true, cfg["allow_messages"])
	assert.Equal(t, false, cfg["published"])
	assert.Equal(t, false, cfg["moments_wall_published"])
	assert.Equal(t, false, cfg["share_uploads_enabled"])
}

func TestBuildPageSpec_LegacyAllFalseConfigKeepsSectionsVisible(t *testing.T) {
	event := stubEvent("")
	event.OrganizerName = "Eventi"

	spec, err := buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	require.NotNil(t, spec.Meta.Contact)
	assert.True(t, spec.Meta.FooterVisible)
	require.Len(t, spec.Sections, 2)
	assert.Equal(t, "CountdownHeader", spec.Sections[0].Type)
	assert.Equal(t, "GraduationHero", spec.Sections[1].Type)
}

func TestBuildPageSpec_LegacyVisibilityDefaultsKeepSectionsAroundOpenUploads(t *testing.T) {
	sections := []models.EventSection{
		{ID: testSectionID1, EventID: testEventID, ComponentType: "CountdownHeader", Config: `{}`, Order: 1, IsVisible: true},
		{ID: testSectionID2, EventID: testEventID, ComponentType: "GraduationHero", Config: `{}`, Order: 2, IsVisible: true},
		{ID: mustUUID("eeeeeeee-0000-0000-0000-000000000019"), EventID: testEventID, ComponentType: "MomentWall", Config: `{}`, Order: 3, IsVisible: true},
	}

	spec, err := buildPageSpecFromEvent(stubEvent(""), buildSpecDeps{
		getSections: func(eventID uuid.UUID) ([]models.EventSection, error) { return sections, nil },
		getConfig: func(id uuid.UUID) (*models.EventConfig, error) {
			return &models.EventConfig{
				AllowUploads:        true,
				ShareUploadsEnabled: true,
			}, nil
		},
	})

	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.True(t, spec.Meta.FooterVisible)
	require.Len(t, spec.Sections, 3)
	assert.Equal(t, "CountdownHeader", spec.Sections[0].Type)
	assert.Equal(t, "GraduationHero", spec.Sections[1].Type)
	assert.Equal(t, "MomentWall", spec.Sections[2].Type)

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal(spec.Sections[2].Config, &cfg))
	assert.Equal(t, true, cfg["allow_uploads"])
	assert.Equal(t, true, cfg["share_uploads_enabled"])
	assert.Equal(t, false, cfg["published"])
	assert.Equal(t, false, cfg["moments_wall_published"])
}
