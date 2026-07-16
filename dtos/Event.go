package dtos

import (
	"encoding/json"
	"errors"
	"events-stocks/models"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

type EventPayload struct {
	ClientID         json.RawMessage `json:"client_id"`
	Name             *string         `json:"name"`
	Identifier       *string         `json:"identifier"`
	Description      *string         `json:"description"`
	CustomDomain     *string         `json:"custom_domain"`
	Address          *string         `json:"address"`
	SecondAddress    *string         `json:"second_address"`
	MusicURL         *string         `json:"music_url"`
	EventDateTime    json.RawMessage `json:"event_date_time"`
	Timezone         *string         `json:"timezone"`
	Language         *string         `json:"language"`
	EventTypeID      json.RawMessage `json:"event_type_id"`
	OrganizerName    *string         `json:"organizer_name"`
	OrganizerEmail   *string         `json:"organizer_email"`
	OrganizerPhone   *string         `json:"organizer_phone"`
	MaxGuests        json.RawMessage `json:"max_guests"`
	AllowGuestAccess *bool           `json:"allow_guest_access"`
	SlugLocked       *bool           `json:"slug_locked"`
	IsActive         *bool           `json:"is_active"`
}

func (p *EventPayload) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	var next EventPayload
	var err error

	next.ClientID = eventPayloadRaw(fields, "client_id", "clientId", "clientID", "ClientID", "ClientId")
	if next.Name, err = eventPayloadString(fields, "name", "Name"); err != nil {
		return fieldError("name", err)
	}
	if next.Identifier, err = eventPayloadString(fields, "identifier", "Identifier", "slug", "Slug"); err != nil {
		return fieldError("identifier", err)
	}
	if next.Description, err = eventPayloadString(fields, "description", "Description"); err != nil {
		return fieldError("description", err)
	}
	if next.CustomDomain, err = eventPayloadString(fields, "custom_domain", "customDomain", "CustomDomain"); err != nil {
		return fieldError("custom_domain", err)
	}
	if next.Address, err = eventPayloadString(fields, "address", "Address"); err != nil {
		return fieldError("address", err)
	}
	if next.SecondAddress, err = eventPayloadString(fields, "second_address", "secondAddress", "SecondAddress"); err != nil {
		return fieldError("second_address", err)
	}
	if next.MusicURL, err = eventPayloadString(fields, "music_url", "musicUrl", "musicURL", "MusicURL", "MusicUrl"); err != nil {
		return fieldError("music_url", err)
	}
	next.EventDateTime = eventPayloadRaw(fields, "event_date_time", "eventDateTime", "EventDateTime", "eventDate", "EventDate")
	if next.Timezone, err = eventPayloadString(fields, "timezone", "timeZone", "Timezone", "TimeZone"); err != nil {
		return fieldError("timezone", err)
	}
	if next.Language, err = eventPayloadString(fields, "language", "locale", "Language", "Locale"); err != nil {
		return fieldError("language", err)
	}
	next.EventTypeID = eventPayloadRaw(fields, "event_type_id", "eventTypeId", "eventTypeID", "EventTypeID", "EventTypeId")
	if next.OrganizerName, err = eventPayloadString(fields, "organizer_name", "organizerName", "OrganizerName"); err != nil {
		return fieldError("organizer_name", err)
	}
	if next.OrganizerEmail, err = eventPayloadString(fields, "organizer_email", "organizerEmail", "OrganizerEmail"); err != nil {
		return fieldError("organizer_email", err)
	}
	if next.OrganizerPhone, err = eventPayloadString(fields, "organizer_phone", "organizerPhone", "OrganizerPhone"); err != nil {
		return fieldError("organizer_phone", err)
	}
	next.MaxGuests = eventPayloadRaw(fields, "max_guests", "maxGuests", "MaxGuests")
	if next.AllowGuestAccess, err = eventPayloadBool(fields, "allow_guest_access", "allowGuestAccess", "AllowGuestAccess"); err != nil {
		return fieldError("allow_guest_access", err)
	}
	if next.SlugLocked, err = eventPayloadBool(fields, "slug_locked", "slugLocked", "SlugLocked"); err != nil {
		return fieldError("slug_locked", err)
	}
	if next.IsActive, err = eventPayloadBool(fields, "is_active", "isActive", "IsActive"); err != nil {
		return fieldError("is_active", err)
	}

	*p = next
	return nil
}

func eventPayloadRaw(fields map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw, ok := fields[key]; ok {
			return raw
		}
	}
	return nil
}

func eventPayloadString(fields map[string]json.RawMessage, keys ...string) (*string, error) {
	raw := eventPayloadRaw(fields, keys...)
	if raw == nil || isJSONNull(raw) {
		return nil, nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func eventPayloadBool(fields map[string]json.RawMessage, keys ...string) (*bool, error) {
	raw := eventPayloadRaw(fields, keys...)
	if raw == nil || isJSONNull(raw) {
		return nil, nil
	}

	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func (p EventPayload) ApplyTo(event *models.Event) error {
	if event == nil {
		return errors.New("event is nil")
	}
	if p.ClientID != nil {
		if err := decodeUUIDPointer(p.ClientID, &event.ClientID); err != nil {
			return fieldError("client_id", err)
		}
	}
	if p.Name != nil {
		event.Name = strings.TrimSpace(*p.Name)
	}
	if p.Identifier != nil {
		event.Identifier = strings.TrimSpace(*p.Identifier)
	}
	if p.Description != nil {
		event.Description = strings.TrimSpace(*p.Description)
	}
	if p.CustomDomain != nil {
		event.CustomDomain = strings.TrimSpace(*p.CustomDomain)
	}
	if p.Address != nil {
		event.Address = strings.TrimSpace(*p.Address)
	}
	if p.SecondAddress != nil {
		event.SecondAddress = strings.TrimSpace(*p.SecondAddress)
	}
	if p.MusicURL != nil {
		event.MusicUrl = strings.TrimSpace(*p.MusicURL)
	}
	if p.EventDateTime != nil {
		if err := decodeTime(p.EventDateTime, &event.EventDateTime); err != nil {
			return fieldError("event_date_time", err)
		}
	}
	if p.Timezone != nil {
		event.Timezone = strings.TrimSpace(*p.Timezone)
	}
	if p.Language != nil {
		event.Language = strings.TrimSpace(*p.Language)
	}
	if p.EventTypeID != nil {
		if err := decodeUUIDValue(p.EventTypeID, &event.EventTypeID); err != nil {
			return fieldError("event_type_id", err)
		}
	}
	if p.OrganizerName != nil {
		event.OrganizerName = strings.TrimSpace(*p.OrganizerName)
	}
	if p.OrganizerEmail != nil {
		event.OrganizerEmail = strings.TrimSpace(*p.OrganizerEmail)
	}
	if p.OrganizerPhone != nil {
		event.OrganizerPhone = strings.TrimSpace(*p.OrganizerPhone)
	}
	if p.MaxGuests != nil {
		if err := decodeOptionalNonNegativeIntPointer(p.MaxGuests, &event.MaxGuests); err != nil {
			return fieldError("max_guests", err)
		}
	}
	if p.AllowGuestAccess != nil {
		event.AllowGuestAccess = *p.AllowGuestAccess
	}
	if p.SlugLocked != nil {
		event.SlugLocked = *p.SlugLocked
	}
	if p.IsActive != nil {
		event.IsActive = *p.IsActive
	}
	return nil
}

func decodeUUIDValue(raw json.RawMessage, dest *uuid.UUID) error {
	if isJSONNull(raw) {
		*dest = uuid.Nil
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		*dest = uuid.Nil
		return nil
	}
	id, err := uuid.FromString(value)
	if err != nil {
		return err
	}
	*dest = id
	return nil
}

func decodeOptionalNonNegativeIntPointer(raw json.RawMessage, dest **int) error {
	if isJSONNull(raw) {
		*dest = nil
		return nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			*dest = nil
			return nil
		}
		value, parseErr := strconv.Atoi(text)
		if parseErr != nil {
			return parseErr
		}
		if value < 0 {
			return errors.New("must be greater than or equal to 0")
		}
		*dest = &value
		return nil
	}

	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value < 0 {
		return errors.New("must be greater than or equal to 0")
	}
	*dest = &value
	return nil
}

type EventResponse struct {
	ID                           uuid.UUID              `json:"id"`
	ClientID                     *uuid.UUID             `json:"client_id"`
	Client                       *ClientSummaryResponse `json:"client,omitempty"`
	Name                         string                 `json:"name"`
	Identifier                   string                 `json:"identifier"`
	Description                  string                 `json:"description"`
	CoverImageURL                string                 `json:"cover_image_url"`
	CoverImageURL2               string                 `json:"cover_image_url2"`
	CoverViewURL                 string                 `json:"cover_view_url,omitempty"`
	CoverViewURL2                string                 `json:"cover_view_url2,omitempty"`
	CoverViewURLExpiresAt        *time.Time             `json:"cover_view_url_expires_at,omitempty"`
	CoverViewURL2ExpiresAt       *time.Time             `json:"cover_view_url2_expires_at,omitempty"`
	ViewURL                      string                 `json:"view_url,omitempty"`
	ViewURLExpiresAt             *time.Time             `json:"view_url_expires_at,omitempty"`
	CoverVariants                []PublicMediaVariant   `json:"cover_variants,omitempty"`
	CoverPendingURL              string                 `json:"cover_pending_url,omitempty"`
	CoverPendingViewURL          string                 `json:"cover_pending_view_url,omitempty"`
	CoverPendingViewURLExpiresAt *time.Time             `json:"cover_pending_view_url_expires_at,omitempty"`
	CoverProcessingStatus        string                 `json:"cover_processing_status,omitempty"`
	CoverProcessingJobID         string                 `json:"cover_processing_job_id,omitempty"`
	CoverProcessingGeneration    int64                  `json:"cover_processing_generation,omitempty"`
	CoverProcessingError         string                 `json:"cover_processing_error,omitempty"`
	CustomDomain                 string                 `json:"custom_domain"`
	Address                      string                 `json:"address"`
	SecondAddress                string                 `json:"second_address"`
	MusicURL                     string                 `json:"music_url"`
	EventDateTime                time.Time              `json:"event_date_time"`
	Timezone                     string                 `json:"timezone"`
	Language                     string                 `json:"language"`
	EventTypeID                  uuid.UUID              `json:"event_type_id"`
	EventType                    *EventTypeResponse     `json:"event_type,omitempty"`
	EventConfig                  *EventConfigResponse   `json:"event_config,omitempty"`
	Config                       *EventConfigResponse   `json:"config,omitempty"`
	OrganizerName                string                 `json:"organizer_name"`
	OrganizerEmail               string                 `json:"organizer_email"`
	OrganizerPhone               string                 `json:"organizer_phone"`
	MaxGuests                    *int                   `json:"max_guests"`
	AllowGuestAccess             bool                   `json:"allow_guest_access"`
	SlugLocked                   bool                   `json:"slug_locked"`
	IsActive                     bool                   `json:"is_active"`
	PendingMomentCount           int64                  `json:"pending_moment_count"`
	GuestSummary                 *GuestSummary          `json:"guest_summary,omitempty"`
	GuestShareSummary            *GuestShareSummary     `json:"guest_share_summary,omitempty"`
	EventSections                []EventSectionResponse `json:"event_sections"`
	CreatedAt                    time.Time              `json:"created_at"`
	UpdatedAt                    time.Time              `json:"updated_at"`
}

type StudioWorkspaceResponse struct {
	Event    EventResponse          `json:"event"`
	Config   EventConfigResponse    `json:"config"`
	Sections []EventSectionResponse `json:"sections"`
}

type EventDashboardMetrics struct {
	Total         int `json:"total"`
	Active        int `json:"active"`
	Upcoming      int `json:"upcoming"`
	PastActive    int `json:"past_active"`
	TotalCapacity int `json:"total_capacity"`
}

type EventDashboardOverview struct {
	Metrics               EventDashboardMetrics `json:"metrics"`
	NextEvent             *EventResponse        `json:"next_event"`
	NextEventGuestSummary *GuestSummary         `json:"next_event_guest_summary,omitempty"`
	ActiveEvents          []EventResponse       `json:"active_events"`
}

type EventNotification struct {
	ID            uuid.UUID  `json:"id"`
	Name          string     `json:"name"`
	Identifier    string     `json:"identifier"`
	EventDateTime time.Time  `json:"event_date_time"`
	Timezone      string     `json:"timezone"`
	IsActive      bool       `json:"is_active"`
	ClientID      *uuid.UUID `json:"client_id,omitempty"`
	EventTypeID   uuid.UUID  `json:"event_type_id"`
}

type EventListQuery struct {
	Page     int
	PageSize int
	Search   string
	Filter   string
	Now      time.Time
}

type EventListCounts struct {
	All      int `json:"all"`
	Upcoming int `json:"upcoming"`
	Today    int `json:"today"`
	Past     int `json:"past"`
}

type EventListPage struct {
	Data       []EventResponse `json:"data"`
	Total      int             `json:"total"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
	TotalPages int             `json:"total_pages"`
	Counts     EventListCounts `json:"counts"`
}

func NewEventResponse(event *models.Event) EventResponse {
	if event == nil {
		return EventResponse{}
	}

	response := EventResponse{
		ID:                        event.ID,
		ClientID:                  event.ClientID,
		Name:                      event.Name,
		Identifier:                event.Identifier,
		Description:               event.Description,
		CoverImageURL:             event.CoverImageURL,
		CoverImageURL2:            event.CoverImageURL2,
		CoverVariants:             NewPublicMediaVariants(event.CoverVariants),
		CoverPendingURL:           event.CoverPendingURL,
		CoverProcessingStatus:     event.CoverProcessingStatus,
		CoverProcessingJobID:      event.CoverProcessingJobID,
		CoverProcessingGeneration: event.CoverProcessingGeneration,
		CoverProcessingError:      event.CoverProcessingError,
		CustomDomain:              event.CustomDomain,
		Address:                   event.Address,
		SecondAddress:             event.SecondAddress,
		MusicURL:                  event.MusicUrl,
		EventDateTime:             event.EventDateTime,
		Timezone:                  event.Timezone,
		Language:                  event.Language,
		EventTypeID:               event.EventTypeID,
		OrganizerName:             event.OrganizerName,
		OrganizerEmail:            event.OrganizerEmail,
		OrganizerPhone:            event.OrganizerPhone,
		MaxGuests:                 event.MaxGuests,
		AllowGuestAccess:          event.AllowGuestAccess,
		SlugLocked:                event.SlugLocked,
		IsActive:                  event.IsActive,
		PendingMomentCount:        event.PendingMomentCount,
		CreatedAt:                 event.CreatedAt,
		UpdatedAt:                 event.UpdatedAt,
	}

	if event.Client != nil {
		client := NewClientSummaryResponse(*event.Client)
		response.Client = &client
	}
	if event.EventType.ID != uuid.Nil || event.EventType.Name != "" {
		eventType := NewEventTypeResponse(event.EventType)
		response.EventType = &eventType
	}
	if event.EventConfig.ID != uuid.Nil {
		config := NewEventConfigResponse(&event.EventConfig, event.ID)
		response.EventConfig = &config
		response.Config = &config
	}

	return response
}

func NewEventResponses(events []models.Event) []EventResponse {
	response := make([]EventResponse, 0, len(events))
	for i := range events {
		response = append(response, NewEventResponse(&events[i]))
	}
	return response
}
