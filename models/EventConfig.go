package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type EventConfig struct {
	ID                          uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	IsPublic                    bool           `json:"is_public"`
	IsAuthPreview               bool           `json:"is_auth_preview"`
	AllowUploads                bool           `json:"allow_uploads"`
	AllowMessages               bool           `json:"allow_messages"`
	AuthPasswordPreview         string         `json:"auth_password_preview"`
	NotifyOnMomentUpload        bool           `json:"notify_on_moment_upload"`
	DesignTemplateID            uuid.UUID      `gorm:"type:uuid;index" json:"design_template_id"`
	DesignTemplate              DesignTemplate `gorm:"foreignKey:DesignTemplateID" json:"design_template,omitempty"`
	ActiveFrom                  time.Time      `json:"active_from"`
	ActiveUntil                 *time.Time     `json:"active_until,omitempty"`
	DefaultWelcomeMessage       string         `json:"default_welcome_message"`
	DefaultMomentRequestMessage string         `json:"default_moment_request_message"`
	DefaultThankYouMessage      string         `json:"default_thank_you_message"`
	DefaultGuestSignatureTitle  string         `json:"default_guest_signature_title"`
	ShowCountdown               bool           `json:"show_countdown"`
	ShowRSVPSection             bool           `json:"show_rsvp_section"`
	ShowEventLocation           bool           `json:"show_event_location"`
	ShowSecondLocation          bool           `json:"show_second_location"`
	ShowHostsSection            bool           `json:"show_hosts_section"`
	ShowPhotoGallery            bool           `json:"show_photo_gallery"`
	ShowMomentWall              bool           `json:"show_moment_wall"`
	ShowContactSection          bool           `json:"show_contact_section"`
	ShowHeader                  bool           `json:"show_header"`
	ShowFooter                  bool           `json:"show_footer"`
	ShowEventSchedule           bool           `json:"show_event_schedule"`
	CreatedAt                   time.Time      `json:"created_at"`
	UpdatedAt                   time.Time      `json:"updated_at"`
	DeletedAt                   gorm.DeletedAt `gorm:"index" json:"-"`
}
