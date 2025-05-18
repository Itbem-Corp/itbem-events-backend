package colorrepository

import (
	"errors"
	"strings"
)

var (
	ErrColorNameExists        = errors.New("a color with this name already exists")
	ErrColorPatternNameExists = errors.New("a color pattern with this name already exists")
	ErrColorPaletteNameExists = errors.New("a color palette with this name already exists")
)

func checkDuplicateName(err error) error {
	if strings.Contains(err.Error(), "duplicate key value") &&
		strings.Contains(err.Error(), "idx_color_palettes_name") {
		return ErrColorPaletteNameExists
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
