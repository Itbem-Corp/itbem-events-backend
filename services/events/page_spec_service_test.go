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
		ID:       testEventID,
		Name:     "Graduación Izapa 2025",
		MusicUrl:   musicUrl,
		Identifier: "grad-izapa-2025",
	}
}

func stubSections() []models.EventSection {
	return []models.EventSection{
		{
			ID:            testSectionID1,
			EventID:       testEventID,
			ComponentType: "CountdownHeader",
			Config:        `{"heading":"EL GRAN DÍA","targetDate":"2025-06-22T20:30:00-06:00"}`,
			Order:         1,
			IsVisible:     true,
		},
		{
			ID:            testSectionID2,
			EventID:       testEventID,
			ComponentType: "GraduationHero",
			Config:        `{"title":"NOS GRADUAMOS","years":"2022 - 2025","school":"PREPARATORIA"}`,
			Order:         2,
			IsVisible:     true,
		},
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

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

	// sections
	require.Len(t, spec.Sections, 2)
	assert.Equal(t, "CountdownHeader", spec.Sections[0].Type)
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
