package services

// ValidationError representa un error de validación de archivo (tipo o tamaño).
type ValidationError struct {
	Msg string
}

func (e ValidationError) Error() string {
	return e.Msg
}

// IsValidationError detecta si un error es de tipo ValidationError.
func IsValidationError(err error) bool {
	_, ok := err.(ValidationError)
	return ok
}
