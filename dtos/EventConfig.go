package dtos

import (
	"bytes"
	"encoding/json"
	"errors"
	"events-stocks/models"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

type EventConfigResponse struct {
	ID                          uuid.UUID               `json:"id"`
	EventID                     uuid.UUID               `json:"event_id"`
	IsPublic                    bool                    `json:"is_public"`
	IsAuthPreview               bool                    `json:"is_auth_preview"`
	AllowUploads                bool                    `json:"allow_uploads"`
	AllowMessages               bool                    `json:"allow_messages"`
	AuthPasswordPreview         string                  `json:"auth_password_preview"`
	NotifyOnMomentUpload        bool                    `json:"notify_on_moment_upload"`
	DesignTemplateID            *uuid.UUID              `json:"design_template_id"`
	DesignTemplate              *DesignTemplateResponse `json:"design_template,omitempty"`
	ColorPaletteID              *uuid.UUID              `json:"color_palette_id"`
	ColorPalette                *ColorPaletteResponse   `json:"color_palette,omitempty"`
	FontSetID                   *uuid.UUID              `json:"font_set_id"`
	FontSet                     *FontSetResponse        `json:"font_set,omitempty"`
	ActiveFrom                  *time.Time              `json:"active_from,omitempty"`
	ActiveUntil                 *time.Time              `json:"active_until,omitempty"`
	DefaultWelcomeMessage       string                  `json:"default_welcome_message"`
	DefaultMomentRequestMessage string                  `json:"default_moment_request_message"`
	DefaultThankYouMessage      string                  `json:"default_thank_you_message"`
	DefaultGuestSignatureTitle  string                  `json:"default_guest_signature_title"`
	ShowCountdown               bool                    `json:"show_countdown"`
	ShowRSVPSection             bool                    `json:"show_rsvp_section"`
	ShowEventLocation           bool                    `json:"show_event_location"`
	ShowSecondLocation          bool                    `json:"show_second_location"`
	ShowHostsSection            bool                    `json:"show_hosts_section"`
	ShowPhotoGallery            bool                    `json:"show_photo_gallery"`
	ShowMomentWall              bool                    `json:"show_moment_wall"`
	MomentsWallPublished        bool                    `json:"moments_wall_published"`
	VisibilityConfigured        bool                    `json:"visibility_configured"`
	ShareUploadsEnabled         bool                    `json:"share_uploads_enabled"`
	MaxUploadsPerGuest          int                     `json:"max_uploads_per_guest"`
	AutoApproveUploads          bool                    `json:"auto_approve_uploads"`
	ShowContactSection          bool                    `json:"show_contact_section"`
	ShowHeader                  bool                    `json:"show_header"`
	ShowFooter                  bool                    `json:"show_footer"`
	ShowEventSchedule           bool                    `json:"show_event_schedule"`
	CreatedAt                   time.Time               `json:"created_at"`
	UpdatedAt                   time.Time               `json:"updated_at"`
}

func NewEventConfigResponse(config *models.EventConfig, eventID uuid.UUID) EventConfigResponse {
	if config == nil {
		return EventConfigResponse{ID: eventID, EventID: eventID}
	}
	config = config.WithVisibilityDefaults()
	if eventID == uuid.Nil {
		eventID = config.ID
	}

	return EventConfigResponse{
		ID:                          config.ID,
		EventID:                     eventID,
		IsPublic:                    config.IsPublic,
		IsAuthPreview:               config.IsAuthPreview,
		AllowUploads:                config.AllowUploads,
		AllowMessages:               config.AllowMessages,
		AuthPasswordPreview:         config.NormalizedAuthPasswordPreview(),
		NotifyOnMomentUpload:        config.NotifyOnMomentUpload,
		DesignTemplateID:            config.DesignTemplateID,
		DesignTemplate:              designTemplateResponsePtr(config.DesignTemplate),
		ColorPaletteID:              config.ColorPaletteID,
		ColorPalette:                colorPaletteResponsePtr(config.ColorPalette),
		FontSetID:                   config.FontSetID,
		FontSet:                     fontSetResponsePtr(config.FontSet),
		ActiveFrom:                  eventConfigResponseTime(config.ActiveFrom),
		ActiveUntil:                 eventConfigResponseTimePtr(config.ActiveUntil),
		DefaultWelcomeMessage:       config.DefaultWelcomeMessage,
		DefaultMomentRequestMessage: config.DefaultMomentRequestMessage,
		DefaultThankYouMessage:      config.DefaultThankYouMessage,
		DefaultGuestSignatureTitle:  config.DefaultGuestSignatureTitle,
		ShowCountdown:               config.ShowCountdown,
		ShowRSVPSection:             config.ShowRSVPSection,
		ShowEventLocation:           config.ShowEventLocation,
		ShowSecondLocation:          config.ShowSecondLocation,
		ShowHostsSection:            config.ShowHostsSection,
		ShowPhotoGallery:            config.ShowPhotoGallery,
		ShowMomentWall:              config.ShowMomentWall,
		MomentsWallPublished:        config.ShowMomentWall,
		VisibilityConfigured:        config.VisibilityConfigured,
		ShareUploadsEnabled:         config.ShareUploadsEnabled,
		MaxUploadsPerGuest:          config.MaxUploadsPerGuest,
		AutoApproveUploads:          config.AutoApproveUploads,
		ShowContactSection:          config.ShowContactSection,
		ShowHeader:                  config.ShowHeader,
		ShowFooter:                  config.ShowFooter,
		ShowEventSchedule:           config.ShowEventSchedule,
		CreatedAt:                   config.CreatedAt,
		UpdatedAt:                   config.UpdatedAt,
	}
}

func eventConfigResponseTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func eventConfigResponseTimePtr(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	return value
}

type EventConfigPatch map[string]json.RawMessage

func (p EventConfigPatch) ApplyTo(config *models.EventConfig) error {
	if config == nil {
		return errors.New("event config is nil")
	}

	for rawField, raw := range p {
		field := normalizeEventConfigPatchField(rawField)
		if rawField != field {
			if _, hasCanonicalField := p[field]; hasCanonicalField {
				continue
			}
		}
		if isReadOnlyEventConfigField(field) {
			continue
		}
		visibilityField := isEventConfigVisibilityField(field)

		switch field {
		case "is_public":
			if err := decodeBool(raw, &config.IsPublic); err != nil {
				return fieldError(field, err)
			}
		case "is_auth_preview":
			if err := decodeBool(raw, &config.IsAuthPreview); err != nil {
				return fieldError(field, err)
			}
		case "allow_uploads":
			if err := decodeBool(raw, &config.AllowUploads); err != nil {
				return fieldError(field, err)
			}
		case "allow_messages":
			if err := decodeBool(raw, &config.AllowMessages); err != nil {
				return fieldError(field, err)
			}
		case "auth_password_preview":
			var value string
			if err := decodeString(raw, &value); err != nil {
				return fieldError(field, err)
			}
			config.AuthPasswordPreview = strings.TrimSpace(value)
		case "notify_on_moment_upload":
			if err := decodeBool(raw, &config.NotifyOnMomentUpload); err != nil {
				return fieldError(field, err)
			}
		case "design_template_id":
			if err := decodeUUIDPointer(raw, &config.DesignTemplateID); err != nil {
				return fieldError(field, err)
			}
		case "color_palette_id":
			if err := decodeUUIDPointer(raw, &config.ColorPaletteID); err != nil {
				return fieldError(field, err)
			}
		case "font_set_id":
			if err := decodeUUIDPointer(raw, &config.FontSetID); err != nil {
				return fieldError(field, err)
			}
		case "active_from":
			if err := decodeTime(raw, &config.ActiveFrom); err != nil {
				return fieldError(field, err)
			}
		case "active_until":
			if err := decodeTimePointer(raw, &config.ActiveUntil); err != nil {
				return fieldError(field, err)
			}
		case "default_welcome_message", "welcome_message":
			if err := decodeString(raw, &config.DefaultWelcomeMessage); err != nil {
				return fieldError(field, err)
			}
		case "default_moment_request_message", "moment_message":
			if err := decodeString(raw, &config.DefaultMomentRequestMessage); err != nil {
				return fieldError(field, err)
			}
		case "default_thank_you_message", "thank_you_message":
			if err := decodeString(raw, &config.DefaultThankYouMessage); err != nil {
				return fieldError(field, err)
			}
		case "default_guest_signature_title", "guest_signature_title":
			if err := decodeString(raw, &config.DefaultGuestSignatureTitle); err != nil {
				return fieldError(field, err)
			}
		case "show_countdown":
			if err := decodeBool(raw, &config.ShowCountdown); err != nil {
				return fieldError(field, err)
			}
		case "show_rsvp_section", "show_rsvp":
			if err := decodeBool(raw, &config.ShowRSVPSection); err != nil {
				return fieldError(field, err)
			}
		case "show_event_location", "show_location":
			if err := decodeBool(raw, &config.ShowEventLocation); err != nil {
				return fieldError(field, err)
			}
		case "show_second_location":
			if err := decodeBool(raw, &config.ShowSecondLocation); err != nil {
				return fieldError(field, err)
			}
		case "show_hosts_section":
			if err := decodeBool(raw, &config.ShowHostsSection); err != nil {
				return fieldError(field, err)
			}
		case "show_photo_gallery", "show_gallery":
			if err := decodeBool(raw, &config.ShowPhotoGallery); err != nil {
				return fieldError(field, err)
			}
		case "show_moment_wall", "show_wall":
			if err := decodeBool(raw, &config.ShowMomentWall); err != nil {
				return fieldError(field, err)
			}
		case "share_uploads_enabled":
			if err := decodeBool(raw, &config.ShareUploadsEnabled); err != nil {
				return fieldError(field, err)
			}
		case "max_uploads_per_guest":
			if err := decodeNonNegativeInt(raw, &config.MaxUploadsPerGuest); err != nil {
				return fieldError(field, err)
			}
		case "auto_approve_uploads":
			if err := decodeBool(raw, &config.AutoApproveUploads); err != nil {
				return fieldError(field, err)
			}
		case "show_contact_section", "show_contact":
			if err := decodeBool(raw, &config.ShowContactSection); err != nil {
				return fieldError(field, err)
			}
		case "show_header":
			if err := decodeBool(raw, &config.ShowHeader); err != nil {
				return fieldError(field, err)
			}
		case "show_footer":
			if err := decodeBool(raw, &config.ShowFooter); err != nil {
				return fieldError(field, err)
			}
		case "show_event_schedule", "show_schedule":
			if err := decodeBool(raw, &config.ShowEventSchedule); err != nil {
				return fieldError(field, err)
			}
		default:
			return fmt.Errorf("unknown event config field: %s", field)
		}

		if visibilityField {
			config.VisibilityConfigured = true
		}
	}

	if sharedUploadsEnabled, ok, err := eventConfigPatchBoolValue(p, "share_uploads_enabled"); err != nil {
		return fieldError("share_uploads_enabled", err)
	} else if ok && sharedUploadsEnabled {
		allowUploads, hasAllowUploads, err := eventConfigPatchBoolValue(p, "allow_uploads")
		if err != nil {
			return fieldError("allow_uploads", err)
		}
		showMomentWall, hasShowMomentWall, err := eventConfigPatchBoolValue(p, "show_moment_wall")
		if err != nil {
			return fieldError("show_moment_wall", err)
		}

		if (!hasAllowUploads || allowUploads) && (!hasShowMomentWall || !showMomentWall) {
			config.AllowUploads = true
			config.ShowMomentWall = false
			config.VisibilityConfigured = true
		}
	}

	if !config.AllowUploads || config.ShowMomentWall {
		config.ShareUploadsEnabled = false
	}

	if eventConfigActiveRangeInvalid(config) {
		return fieldError("active_until", errors.New("must be after active_from"))
	}

	return nil
}

func eventConfigActiveRangeInvalid(config *models.EventConfig) bool {
	if config == nil || config.ActiveUntil == nil || config.ActiveUntil.IsZero() {
		return false
	}
	if config.ActiveFrom.IsZero() || config.ActiveFrom.Year() <= 1970 {
		return false
	}
	return !config.ActiveUntil.After(config.ActiveFrom)
}

func eventConfigPatchBoolValue(p EventConfigPatch, canonicalField string) (bool, bool, error) {
	if raw, ok := p[canonicalField]; ok {
		var value bool
		if err := decodeBool(raw, &value); err != nil {
			return false, true, err
		}
		return value, true, nil
	}

	for rawField, raw := range p {
		if normalizeEventConfigPatchField(rawField) != canonicalField {
			continue
		}
		var value bool
		if err := decodeBool(raw, &value); err != nil {
			return false, true, err
		}
		return value, true, nil
	}

	return false, false, nil
}

func normalizeEventConfigPatchField(field string) string {
	switch field {
	case "id", "ID", "Id":
		return "id"
	case "eventId", "eventID", "EventID", "EventId":
		return "event_id"
	case "isPublic", "IsPublic":
		return "is_public"
	case "isAuthPreview", "IsAuthPreview":
		return "is_auth_preview"
	case "allowUploads", "AllowUploads":
		return "allow_uploads"
	case "allowMessages", "AllowMessages":
		return "allow_messages"
	case "authPasswordPreview", "AuthPasswordPreview":
		return "auth_password_preview"
	case "notifyOnMomentUpload", "NotifyOnMomentUpload":
		return "notify_on_moment_upload"
	case "designTemplateId", "designTemplateID", "DesignTemplateID", "DesignTemplateId":
		return "design_template_id"
	case "designTemplate", "DesignTemplate":
		return "design_template"
	case "colorPaletteId", "colorPaletteID", "ColorPaletteID", "ColorPaletteId":
		return "color_palette_id"
	case "colorPalette", "ColorPalette":
		return "color_palette"
	case "fontSetId", "fontSetID", "FontSetID", "FontSetId":
		return "font_set_id"
	case "fontSet", "FontSet":
		return "font_set"
	case "activeFrom", "ActiveFrom":
		return "active_from"
	case "activeUntil", "ActiveUntil":
		return "active_until"
	case "defaultWelcomeMessage", "DefaultWelcomeMessage":
		return "default_welcome_message"
	case "defaultMomentRequestMessage", "DefaultMomentRequestMessage":
		return "default_moment_request_message"
	case "defaultThankYouMessage", "DefaultThankYouMessage":
		return "default_thank_you_message"
	case "defaultGuestSignatureTitle", "DefaultGuestSignatureTitle":
		return "default_guest_signature_title"
	case "welcomeMessage", "WelcomeMessage":
		return "default_welcome_message"
	case "momentMessage", "MomentMessage":
		return "default_moment_request_message"
	case "thankYouMessage", "ThankYouMessage":
		return "default_thank_you_message"
	case "guestSignatureTitle", "GuestSignatureTitle":
		return "default_guest_signature_title"
	case "showCountdown", "ShowCountdown":
		return "show_countdown"
	case "showRSVPSection", "showRsvpSection", "ShowRSVPSection", "ShowRsvpSection":
		return "show_rsvp_section"
	case "showRSVP", "showRsvp", "ShowRSVP", "ShowRsvp":
		return "show_rsvp_section"
	case "showEventLocation", "ShowEventLocation":
		return "show_event_location"
	case "showLocation", "ShowLocation":
		return "show_event_location"
	case "showSecondLocation", "ShowSecondLocation":
		return "show_second_location"
	case "showHostsSection", "ShowHostsSection":
		return "show_hosts_section"
	case "showPhotoGallery", "ShowPhotoGallery":
		return "show_photo_gallery"
	case "showGallery", "ShowGallery":
		return "show_photo_gallery"
	case "showMomentWall", "ShowMomentWall":
		return "show_moment_wall"
	case "showWall", "ShowWall":
		return "show_moment_wall"
	case "momentsWallPublished", "MomentsWallPublished":
		return "show_moment_wall"
	case "moments_wall_published":
		return "show_moment_wall"
	case "shareUploadsEnabled", "ShareUploadsEnabled", "sharedUploadsEnabled", "SharedUploadsEnabled", "shared_uploads_enabled":
		return "share_uploads_enabled"
	case "visibilityConfigured", "VisibilityConfigured":
		return "visibility_configured"
	case "maxUploadsPerGuest", "MaxUploadsPerGuest":
		return "max_uploads_per_guest"
	case "autoApproveUploads", "AutoApproveUploads":
		return "auto_approve_uploads"
	case "showContactSection", "ShowContactSection":
		return "show_contact_section"
	case "showContact", "ShowContact":
		return "show_contact_section"
	case "showHeader", "ShowHeader":
		return "show_header"
	case "showFooter", "ShowFooter":
		return "show_footer"
	case "showEventSchedule", "ShowEventSchedule":
		return "show_event_schedule"
	case "showSchedule", "ShowSchedule":
		return "show_event_schedule"
	case "createdAt", "CreatedAt":
		return "created_at"
	case "updatedAt", "UpdatedAt":
		return "updated_at"
	case "deletedAt", "DeletedAt":
		return "deleted_at"
	default:
		return field
	}
}

func isReadOnlyEventConfigField(field string) bool {
	switch field {
	case "id", "event_id", "created_at", "updated_at", "deleted_at",
		"design_template", "color_palette", "font_set", "visibility_configured":
		return true
	default:
		return false
	}
}

func isEventConfigVisibilityField(field string) bool {
	switch field {
	case "show_countdown",
		"show_rsvp_section",
		"show_event_location",
		"show_second_location",
		"show_hosts_section",
		"show_photo_gallery",
		"show_moment_wall",
		"show_contact_section",
		"show_header",
		"show_footer",
		"show_event_schedule":
		return true
	default:
		return false
	}
}

func fieldError(field string, err error) error {
	return fmt.Errorf("%s: %w", field, err)
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func decodeBool(raw json.RawMessage, dest *bool) error {
	if isJSONNull(raw) {
		*dest = false
		return nil
	}
	return json.Unmarshal(raw, dest)
}

func decodeString(raw json.RawMessage, dest *string) error {
	if isJSONNull(raw) {
		*dest = ""
		return nil
	}
	return json.Unmarshal(raw, dest)
}

func decodeUUIDPointer(raw json.RawMessage, dest **uuid.UUID) error {
	if isJSONNull(raw) {
		*dest = nil
		return nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		*dest = nil
		return nil
	}

	id, err := uuid.FromString(value)
	if err != nil {
		return err
	}
	if id == uuid.Nil {
		*dest = nil
		return nil
	}
	*dest = &id
	return nil
}

func decodeTime(raw json.RawMessage, dest *time.Time) error {
	if isJSONNull(raw) {
		*dest = time.Time{}
		return nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		*dest = time.Time{}
		return nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return err
	}
	*dest = parsed
	return nil
}

func decodeTimePointer(raw json.RawMessage, dest **time.Time) error {
	if isJSONNull(raw) {
		*dest = nil
		return nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		*dest = nil
		return nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return err
	}
	*dest = &parsed
	return nil
}

func decodeNonNegativeInt(raw json.RawMessage, dest *int) error {
	if isJSONNull(raw) {
		*dest = 0
		return nil
	}

	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		if value < 0 {
			return errors.New("must be greater than or equal to 0")
		}
		*dest = value
		return nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		*dest = 0
		return nil
	}

	parsed, err := strconv.Atoi(text)
	if err != nil {
		return err
	}
	if parsed < 0 {
		return errors.New("must be greater than or equal to 0")
	}
	*dest = parsed
	return nil
}
