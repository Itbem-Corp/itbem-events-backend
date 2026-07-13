package models

import "github.com/gofrs/uuid"

const (
	DefaultDesignTemplateIdentifier = "editorial-romance"
	defaultDesignTemplateIDString   = "7d2ddf6f-4f58-4d63-9bf5-0ad4d8aa1001"
)

// DefaultDesignTemplateID is stable across environments so newly-created
// events can reference the seeded, production-ready starter design.
func DefaultDesignTemplateID() uuid.UUID {
	return uuid.Must(uuid.FromString(defaultDesignTemplateIDString))
}
