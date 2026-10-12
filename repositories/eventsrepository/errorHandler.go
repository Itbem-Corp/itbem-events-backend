package eventsrepository

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrEventNameAlreadyExists: another live event of the same organization
	// already uses this name (configuration.EventClientNameLiveIndex).
	ErrEventNameAlreadyExists = errors.New("an event with this name already exists")
	// ErrEventIdentifierAlreadyExists: another live event already uses this
	// public identifier/slug (configuration.EventIdentifierLiveIndex).
	ErrEventIdentifierAlreadyExists = errors.New("an event with this identifier already exists")
)

// Index names: the live (partial) indexes and the legacy global ones, which
// stay recognized while a database is between schemas.
var (
	eventNameIndexes       = []string{"idx_events_client_name_live", "idx_events_name"}
	eventIdentifierIndexes = []string{"idx_events_identifier_live", "idx_events_identifier"}
)

func isUniqueViolationOn(err error, indexes []string) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	if !strings.Contains(message, "duplicate key value") && !strings.Contains(message, "SQLSTATE 23505") {
		return false
	}
	for _, index := range indexes {
		if strings.Contains(message, `"`+index+`"`) {
			return true
		}
	}
	return false
}

func CheckDuplicateEventName(err error) error {
	if isUniqueViolationOn(err, eventNameIndexes) {
		return fmt.Errorf("%w: %v", ErrEventNameAlreadyExists, err)
	}
	return nil
}

func CheckDuplicateEventIdentifier(err error) error {
	if isUniqueViolationOn(err, eventIdentifierIndexes) {
		return fmt.Errorf("%w: %v", ErrEventIdentifierAlreadyExists, err)
	}
	return nil
}

// ValidateError maps known constraint violations of the events table to
// sentinel errors (match them with errors.Is); anything else is returned as is.
func ValidateError(err error) error {
	if err == nil {
		return nil
	}
	validators := []func(error) error{
		CheckDuplicateEventName,
		CheckDuplicateEventIdentifier,
	}

	for _, validate := range validators {
		if valErr := validate(err); valErr != nil {
			return valErr
		}
	}
	return err
}
