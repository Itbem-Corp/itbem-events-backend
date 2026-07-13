package services

import "errors"

// ValidationError representa un error de validación de archivo (tipo o tamaño).
type ValidationError struct {
	Msg string
}

func (e ValidationError) Error() string {
	return e.Msg
}

// IsValidationError detecta si un error es de tipo ValidationError.
func IsValidationError(err error) bool {
	if err == nil {
		return false
	}
	var validationErr ValidationError
	if errors.As(err, &validationErr) {
		return true
	}
	var validationErrPtr *ValidationError
	return errors.As(err, &validationErrPtr)
}
