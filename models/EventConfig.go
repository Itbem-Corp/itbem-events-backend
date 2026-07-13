package models

import (
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type EventConfig struct {
	ID                          uuid.UUID       `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	IsPublic                    bool            `json:"is_public"`
	IsAuthPreview               bool            `json:"is_auth_preview"`
	AllowUploads                bool            `json:"allow_uploads"`
	AllowMessages               bool            `json:"allow_messages"`
	AuthPasswordPreview         string          `json:"auth_password_preview"`
	NotifyOnMomentUpload        bool            `json:"notify_on_moment_upload"`
	DesignTemplateID            *uuid.UUID      `gorm:"type:uuid;index" json:"design_template_id"`
	DesignTemplate              *DesignTemplate `gorm:"foreignKey:DesignTemplateID" json:"design_template,omitempty"`
	ColorPaletteID              *uuid.UUID      `gorm:"type:uuid;index" json:"color_palette_id"`
	ColorPalette                *ColorPalette   `gorm:"foreignKey:ColorPaletteID" json:"color_palette,omitempty"`
	FontSetID                   *uuid.UUID      `gorm:"type:uuid;index" json:"font_set_id"`
	FontSet                     *FontSet        `gorm:"foreignKey:FontSetID" json:"font_set,omitempty"`
	ActiveFrom                  time.Time       `json:"active_from"`
	ActiveUntil                 *time.Time      `json:"active_until,omitempty"`
	DefaultWelcomeMessage       string          `json:"default_welcome_message"`
	DefaultMomentRequestMessage string          `json:"default_moment_request_message"`
	DefaultThankYouMessage      string          `json:"default_thank_you_message"`
	DefaultGuestSignatureTitle  string          `json:"default_guest_signature_title"`
	ShowCountdown               bool            `json:"show_countdown"`
	ShowRSVPSection             bool            `json:"show_rsvp_section"`
	ShowEventLocation           bool            `json:"show_event_location"`
	ShowSecondLocation          bool            `json:"show_second_location"`
	ShowHostsSection            bool            `json:"show_hosts_section"`
	ShowPhotoGallery            bool            `json:"show_photo_gallery"`
	ShowMomentWall              bool            `json:"show_moment_wall"`
	VisibilityConfigured        bool            `gorm:"default:false" json:"visibility_configured"`
	ShareUploadsEnabled         bool            `json:"share_uploads_enabled"` // allow QR-code uploads without personal token
	// MaxUploadsPerGuest is the per-IP upload limit for this event (default 30).
	// Set to 0 to use the global default. Enables freemium differentiation.
	MaxUploadsPerGuest int `gorm:"default:30" json:"max_uploads_per_guest"`
	// AutoApproveUploads automatically approves all incoming moments without admin review.
	// Useful for trusted events or internal corporate gatherings.
	AutoApproveUploads bool           `gorm:"default:false" json:"auto_approve_uploads"`
	ShowContactSection bool           `json:"show_contact_section"`
	ShowHeader         bool           `json:"show_header"`
	ShowFooter         bool           `json:"show_footer"`
	ShowEventSchedule  bool           `json:"show_event_schedule"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	DeletedAt          gorm.DeletedAt `gorm:"index" json:"-"`
}

func (c *EventConfig) HasAnyVisibilityFlag() bool {
	if c == nil {
		return false
	}
	return c.ShowCountdown ||
		c.ShowRSVPSection ||
		c.ShowEventLocation ||
		c.ShowSecondLocation ||
		c.ShowHostsSection ||
		c.ShowPhotoGallery ||
		c.ShowMomentWall ||
		c.ShowContactSection ||
		c.ShowHeader ||
		c.ShowFooter ||
		c.ShowEventSchedule
}

func (c *EventConfig) ApplyVisibilityDefaults() {
	if c == nil || c.VisibilityConfigured || c.HasAnyVisibilityFlag() {
		return
	}
	c.ShowCountdown = true
	c.ShowRSVPSection = true
	c.ShowEventLocation = true
	c.ShowSecondLocation = true
	c.ShowHostsSection = true
	c.ShowPhotoGallery = true
	c.ShowContactSection = true
	c.ShowHeader = true
	c.ShowFooter = true
	c.ShowEventSchedule = true
	c.ShowMomentWall = !c.AllowUploads
}

func (c *EventConfig) WithVisibilityDefaults() *EventConfig {
	if c == nil || c.VisibilityConfigured || c.HasAnyVisibilityFlag() {
		return c
	}
	normalized := *c
	normalized.ApplyVisibilityDefaults()
	return &normalized
}

func (c *EventConfig) NormalizedAuthPasswordPreview() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.AuthPasswordPreview)
}

func (c *EventConfig) HasAuthPasswordPreview() bool {
	return c.NormalizedAuthPasswordPreview() != ""
}
