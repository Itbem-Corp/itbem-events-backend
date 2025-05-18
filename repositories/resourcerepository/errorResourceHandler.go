package resourcerepository

import (
	"errors"
	"strings"
)

var (
	ErrColorNameExists        = errors.New("a Resource with this name already exists")
	ErrResourceTypeNameExists = errors.New("a Resource Type with this name already exists")
)

func checkDuplicateName(err error) error {
	if strings.Contains(err.Error(), "duplicate key value") &&
		strings.Contains(err.Error(), "idx_color_palettes_name") {
		return ErrResourceTypeNameExists
	}
	return nil
}

func ValidateError(err error) error {
	validators := []func(error) error{
		checkDuplicateName,
	}

	for _, validate := range validators {
		if valErr := validate(err); valErr != nil {
			return valErr
		}
	}
	return err
}
