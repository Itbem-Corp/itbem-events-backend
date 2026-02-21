package events

import (
	"encoding/json"
	"events-stocks/dtos"
	"events-stocks/models"
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

	// 3. Fetch event (Name as pageTitle, MusicUrl if set)
	event, err := deps.getEvent(invitation.EventID)
	if err != nil || event == nil {
		return nil, fmt.Errorf("event not found")
	}

	// 4. Fetch visible SDUI sections ordered by position
	sections, err := deps.getSections(invitation.EventID)
	if err != nil {
		return nil, fmt.Errorf("failed to load sections: %w", err)
	}

	// 5. Build contact only if event has organizer data
	var contact *dtos.PageSpecContact
	if event.OrganizerName != "" || event.OrganizerPhone != "" || event.OrganizerEmail != "" {
		contact = &dtos.PageSpecContact{
			Name:  event.OrganizerName,
			Phone: event.OrganizerPhone,
			Email: event.OrganizerEmail,
		}
	}

	// 6. Build meta
	meta := dtos.PageSpecMeta{
		PageTitle:  event.Name,
		Contact:    contact,
		EventID:    event.ID.String(),
		Identifier: event.Identifier,
	}
	if event.MusicUrl != "" {
		musicUrl := event.MusicUrl
		meta.MusicUrl = &musicUrl
	}

	// 7. Build sections
	specSections := make([]dtos.PageSpecSection, 0, len(sections))
	for _, s := range sections {
		config := json.RawMessage(s.Config)
		if len(config) == 0 || string(config) == "null" || string(config) == "" {
			config = json.RawMessage("{}")
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

// GetPageSpecByToken builds a SDUI PageSpec for the event associated with the given access token.
// The token can be either the raw UUID token or the human-readable pretty_token.
func GetPageSpecByToken(token string) (*dtos.PageSpec, error) {
	return getPageSpec(pageSpecDeps{
		getToken:      invitationaccesstokenrepository.GetByToken,
		getInvitation: invitationrepository.GetInvitationByIDLite,
		getEvent:      eventsrepository.GetEventByIDRaw,
		getSections:   eventsectionrepository.ListByEventIDForSpec,
	}, token)
}
