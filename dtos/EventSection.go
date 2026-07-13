package dtos

import (
	"bytes"
	"encoding/json"
	"errors"
	"events-stocks/models"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

type EventSectionPayload struct {
	Key                 string          `json:"key"`
	KeyPascal           string          `json:"Key"`
	Name                string          `json:"name"`
	NamePascal          string          `json:"Name"`
	Title               string          `json:"title"`
	TitlePascal         string          `json:"Title"`
	SectionTitle        string          `json:"sectionTitle"`
	SectionTitlePascal  string          `json:"SectionTitle"`
	Type                string          `json:"type"`
	TypePascal          string          `json:"Type"`
	ComponentType       string          `json:"component_type"`
	ComponentTypeAlt    string          `json:"componentType"`
	ComponentTypePascal string          `json:"ComponentType"`
	Config              json.RawMessage `json:"config"`
	ConfigPascal        json.RawMessage `json:"Config"`
	ContentJSON         json.RawMessage `json:"content_json"`
	ContentJSONAlt      json.RawMessage `json:"contentJson"`
	ContentJSONPascal   json.RawMessage `json:"ContentJSON"`
	Order               *int            `json:"order"`
	OrderPascal         *int            `json:"Order"`
	SortOrder           *int            `json:"sort_order"`
	SortOrderAlt        *int            `json:"sortOrder"`
	SortOrderPascal     *int            `json:"SortOrder"`
	IsVisible           *bool           `json:"is_visible"`
	IsVisibleAlt        *bool           `json:"isVisible"`
	IsVisiblePascal     *bool           `json:"IsVisible"`
}

type EventSectionResponse struct {
	ID            uuid.UUID       `json:"id"`
	EventID       uuid.UUID       `json:"event_id"`
	Key           string          `json:"key"`
	Name          string          `json:"name"`
	Title         string          `json:"title"`
	Type          string          `json:"type,omitempty"`
	ComponentType string          `json:"component_type"`
	Config        json.RawMessage `json:"config"`
	ContentJSON   json.RawMessage `json:"content_json,omitempty"`
	Order         int             `json:"order"`
	IsVisible     bool            `json:"is_visible"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func ApplyEventSectionPayload(section *models.EventSection, payload EventSectionPayload, isCreate bool) error {
	if section == nil {
		return errors.New("event section is nil")
	}

	if key := firstTrimmedString(payload.Key, payload.KeyPascal); key != "" {
		section.Key = key
	}

	title := firstTrimmedString(payload.Title, payload.TitlePascal, payload.SectionTitle, payload.SectionTitlePascal, payload.Name, payload.NamePascal)
	if title != "" {
		section.Title = title
	}

	componentType := firstTrimmedString(payload.ComponentType, payload.ComponentTypeAlt, payload.ComponentTypePascal, payload.Type, payload.TypePascal)
	if componentType != "" {
		section.ComponentType = componentType
	}

	if payload.Config != nil || payload.ConfigPascal != nil || payload.ContentJSON != nil || payload.ContentJSONAlt != nil || payload.ContentJSONPascal != nil {
		config, err := NormalizeEventSectionConfig(payload.Config, payload.ConfigPascal, payload.ContentJSON, payload.ContentJSONAlt, payload.ContentJSONPascal)
		if err != nil {
			return err
		}
		section.Config = config
	} else if isCreate && strings.TrimSpace(section.Config) == "" {
		section.Config = "{}"
	}

	if payload.Order != nil {
		section.Order = *payload.Order
	} else if payload.OrderPascal != nil {
		section.Order = *payload.OrderPascal
	} else if payload.SortOrder != nil {
		section.Order = *payload.SortOrder
	} else if payload.SortOrderAlt != nil {
		section.Order = *payload.SortOrderAlt
	} else if payload.SortOrderPascal != nil {
		section.Order = *payload.SortOrderPascal
	}
	if payload.IsVisible != nil {
		section.IsVisible = *payload.IsVisible
	} else if payload.IsVisibleAlt != nil {
		section.IsVisible = *payload.IsVisibleAlt
	} else if payload.IsVisiblePascal != nil {
		section.IsVisible = *payload.IsVisiblePascal
	} else if isCreate {
		section.IsVisible = true
	}

	return nil
}

func firstTrimmedString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func NewEventSectionResponse(section models.EventSection) EventSectionResponse {
	config := EventSectionConfigRaw(section.Config)
	return EventSectionResponse{
		ID:            section.ID,
		EventID:       section.EventID,
		Key:           section.Key,
		Name:          section.Title,
		Title:         section.Title,
		Type:          section.ComponentType,
		ComponentType: section.ComponentType,
		Config:        config,
		ContentJSON:   config,
		Order:         section.Order,
		IsVisible:     section.IsVisible,
		CreatedAt:     section.CreatedAt,
		UpdatedAt:     section.UpdatedAt,
	}
}

func NewEventSectionResponses(sections []models.EventSection) []EventSectionResponse {
	response := make([]EventSectionResponse, 0, len(sections))
	for _, section := range sections {
		response = append(response, NewEventSectionResponse(section))
	}
	return response
}

func NormalizeEventSectionConfig(values ...json.RawMessage) (string, error) {
	for _, raw := range values {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}

		if raw[0] == '"' {
			var encoded string
			if err := json.Unmarshal(raw, &encoded); err != nil {
				return "", err
			}
			encoded = strings.TrimSpace(encoded)
			if encoded == "" {
				return "{}", nil
			}
			if !json.Valid([]byte(encoded)) {
				return "", errors.New("config string must contain valid JSON")
			}
			return encoded, nil
		}

		if !json.Valid(raw) {
			return "", errors.New("config must be valid JSON")
		}
		return string(raw), nil
	}

	return "{}", nil
}

func EventSectionConfigRaw(config string) json.RawMessage {
	trimmed := strings.TrimSpace(config)
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("{}")
	}

	raw := []byte(trimmed)
	if !json.Valid(raw) {
		return json.RawMessage("{}")
	}

	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return json.RawMessage("{}")
		}
		encoded = strings.TrimSpace(encoded)
		if encoded == "" || encoded == "null" || !json.Valid([]byte(encoded)) {
			return json.RawMessage("{}")
		}
		return json.RawMessage(encoded)
	}

	return json.RawMessage(trimmed)
}
