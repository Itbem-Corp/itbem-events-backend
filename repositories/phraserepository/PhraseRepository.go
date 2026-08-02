package phraserepository

import (
	"context"
	"errors"
	"strings"

	"events-stocks/configuration"
	"events-stocks/models"
)

// Repository adapts the persisted phrase catalog to the application port.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

func (*Repository) ListByEventType(ctx context.Context, eventType string) ([]string, error) {
	return ListByEventType(ctx, eventType)
}

func ListByEventType(ctx context.Context, eventType string) ([]string, error) {
	if configuration.DB == nil {
		return nil, errors.New("phrase repository is not initialized")
	}
	var phrases []string
	err := configuration.DB.WithContext(ctx).
		Model(&models.EventPhrase{}).
		Where("event_type = ?", strings.ToUpper(strings.TrimSpace(eventType))).
		Order("created_at ASC, id ASC").
		Pluck("phrase", &phrases).Error
	return phrases, err
}
