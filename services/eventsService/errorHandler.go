package eventService

import (
	"errors"
	"strings"
)

var (
	ErrEventNameAlreadyExists = errors.New("an event with this name already exists")
)

func CheckDuplicateEventName(err error) error {
	if strings.Contains(err.Error(), "duplicate key value") &&
		strings.Contains(err.Error(), "idx_events_name") {
		return ErrEventNameAlreadyExists
	}
	return nil
}

func ValidateError(err error) error {
	validators := []func(error) error{
		CheckDuplicateEventName,
		// aquí puedes agregar más validaciones específicas del servicio
	}

	for _, validate := range validators {
		if valErr := validate(err); valErr != nil {
			return valErr
		}
	}
	return err
}
