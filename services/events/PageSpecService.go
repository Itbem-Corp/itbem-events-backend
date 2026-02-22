package events

import (
	"encoding/json"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/eventconfigrepository"
	"events-stocks/repositories/eventsectionrepository"
	"events-stocks/repositories/eventsrepository"
	"events-stocks/repositories/invitationaccesstokenrepository"
	"events-stocks/repositories/invitationrepository"
	"fmt"

	"github.com/gofrs/uuid"
)

// pageSpecDeps holds the repository functions needed to build a PageSpec.
// Extracted as a struct so tests can inject mocks without modifying package-level state.
type pageSpecDeps struct {
	getToken      func(token string) (*models.InvitationAccessToken, error)
	getInvitation func(id uuid.UUID) (*models.Invitation, error)
	getEvent      func(id uuid.UUID) (*models.Event, error)
	getSections   func(eventID uuid.UUID) ([]models.EventSection, error)
	getConfig     func(id uuid.UUID) (*models.EventConfig, error)
}

// buildSpecDeps holds the repository functions needed after the event is resolved.
type buildSpecDeps struct {
	getSections func(eventID uuid.UUID) ([]models.EventSection, error)
	getConfig   func(id uuid.UUID) (*models.EventConfig, error)
}

// buildPageSpecFromEvent builds a PageSpec from an already-resolved event.
// Shared by both the token-based and identifier-based flows.
func buildPageSpecFromEvent(event *models.Event, deps buildSpecDeps) (*dtos.PageSpec, error) {
	// 1. Fetch visible SDUI sections ordered by position
	sections, err := deps.getSections(event.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load sections: %w", err)
	}

	// 2. Build contact only if event has organizer data
	var contact *dtos.PageSpecContact
	if event.OrganizerName != "" || event.OrganizerPhone != "" || event.OrganizerEmail != "" {
		contact = &dtos.PageSpecContact{
			Name:  event.OrganizerName,
			Phone: event.OrganizerPhone,
			Email: event.OrganizerEmail,
		}
	}

	// 3. Build access control from EventConfig (best-effort — never blocks the page)
	var access *dtos.PageSpecAccess
	if deps.getConfig != nil {
		cfg, cfgErr := deps.getConfig(event.ID)
		if cfgErr == nil && cfg != nil {
			isZero := cfg.ActiveFrom.IsZero() || cfg.ActiveFrom.Year() <= 1970
			access = &dtos.PageSpecAccess{
				PasswordProtected: cfg.AuthPasswordPreview != "",
			}
			if !isZero {
				t := cfg.ActiveFrom
				access.ActiveFrom = &t
			}
			if cfg.ActiveUntil != nil && !cfg.ActiveUntil.IsZero() {
				access.ActiveUntil = cfg.ActiveUntil
			}
		}
	}

	// 4. Build meta
	meta := dtos.PageSpecMeta{
		PageTitle:  event.Name,
		Contact:    contact,
		EventID:    event.ID.String(),
		Identifier: event.Identifier,
		Access:     access,
	}
	if event.MusicUrl != "" {
		musicUrl := event.MusicUrl
		meta.MusicUrl = &musicUrl
	}

	// 5. Build sections — for MomentWall, inject runtime flags from EventConfig
	specSections := make([]dtos.PageSpecSection, 0, len(sections))
	for _, s := range sections {
		config := json.RawMessage(s.Config)
		if len(config) == 0 || string(config) == "null" || string(config) == "" {
			config = json.RawMessage("{}")
		}

		if s.ComponentType == "MomentWall" {
			var cfgForSection *models.EventConfig
			if deps.getConfig != nil {
				cfgForSection, _ = deps.getConfig(event.ID)
			}
			allowUploads := cfgForSection != nil && cfgForSection.AllowUploads
			allowMessages := cfgForSection != nil && cfgForSection.AllowMessages
			published := cfgForSection != nil && cfgForSection.ShowMomentWall
			shareEnabled := cfgForSection != nil && cfgForSection.ShareUploadsEnabled

			var configMap map[string]interface{}
			if err := json.Unmarshal(config, &configMap); err == nil {
				configMap["allow_uploads"] = allowUploads
				configMap["allow_messages"] = allowMessages
				configMap["published"] = published
				configMap["share_uploads_enabled"] = shareEnabled
				if updated, mergeErr := json.Marshal(configMap); mergeErr == nil {
					config = json.RawMessage(updated)
				}
			}
		}

		specSections = append(specSections, dtos.PageSpecSection{
			Type:      s.ComponentType,
			SectionId: s.ID.String(),
			Order:     s.Order,
			Config:    config,
		})
	}

	return &dtos.PageSpec{
		Meta:     meta,
		Sections: specSections,
	}, nil
}

// getPageSpec is the testable core — it accepts deps explicitly.
func getPageSpec(deps pageSpecDeps, token string) (*dtos.PageSpec, error) {
	// 1. Resolve token → access token record
	accessToken, err := deps.getToken(token)
	if err != nil || accessToken == nil {
		return nil, fmt.Errorf("token not found")
	}

	// 2. Access token → invitation → event ID
	invitation, err := deps.getInvitation(accessToken.InvitationID)
	if err != nil || invitation == nil {
		return nil, fmt.Errorf("invitation not found")
	}

	// 3. Fetch event
	event, err := deps.getEvent(invitation.EventID)
	if err != nil || event == nil {
		return nil, fmt.Errorf("event not found")
	}

	return buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: deps.getSections,
		getConfig:   deps.getConfig,
	})
}

// GetPageSpecByToken builds a SDUI PageSpec for the event associated with the given access token.
// The token can be either the raw UUID token or the human-readable pretty_token.
func GetPageSpecByToken(token string) (*dtos.PageSpec, error) {
	return getPageSpec(pageSpecDeps{
		getToken:      invitationaccesstokenrepository.GetByToken,
		getInvitation: invitationrepository.GetInvitationByIDLite,
		getEvent:      eventsrepository.GetEventByIDRaw,
		getSections:   eventsectionrepository.ListByEventIDForSpec,
		getConfig:     eventconfigrepository.GetEventConfigByID,
	}, token)
}

// GetPageSpecByIdentifier builds a SDUI PageSpec by event slug identifier.
// Used by the public preview route (/e/{identifier}).
func GetPageSpecByIdentifier(identifier string) (*dtos.PageSpec, error) {
	event, err := eventsrepository.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		return nil, fmt.Errorf("event not found")
	}

	return buildPageSpecFromEvent(event, buildSpecDeps{
		getSections: eventsectionrepository.ListByEventIDForSpec,
		getConfig:   eventconfigrepository.GetEventConfigByID,
	})
}
