package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type EventConfig struct {
	ID                          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	IsPublic                    bool
	IsAuthPreview               bool
	AllowUploads                bool
	AllowMessages               bool
	AuthPasswordPreview         string
	NotifyOnMomentUpload        bool // Notificar cuando se suba un momento
	DesignTemplateID            uuid.UUID
	DesignTemplate              DesignTemplate `gorm:"foreignKey:DesignTemplateID"`
	ActiveFrom                  time.Time
	ActiveUntil                 *time.Time
	DefaultWelcomeMessage       string
	DefaultMomentRequestMessage string
	DefaultThankYouMessage      string
	DefaultGuestSignatureTitle  string
	ShowCountdown               bool
	ShowRSVPSection             bool
	ShowEventLocation           bool
	ShowSecondLocation          bool
	ShowHostsSection            bool
	ShowPhotoGallery            bool
	ShowMomentWall              bool
	ShowContactSection          bool
	ShowHeader                  bool
	ShowFooter                  bool
	ShowEventSchedule           bool
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	DeletedAt                   gorm.DeletedAt `gorm:"index"`
}
