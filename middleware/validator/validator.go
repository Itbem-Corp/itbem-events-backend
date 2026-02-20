package validator

import (
	"errors"
	"fmt"
	"github.com/go-playground/validator/v10"
	"reflect"
	"strings"
)

// CustomValidator implementa echo.Validator usando go-playground/validator
type CustomValidator struct {
	v *validator.Validate
}

func New() *CustomValidator {
	v := validator.New()

	// Usa el nombre del tag `json` en los mensajes de error en lugar del nombre del campo Go
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" || name == "" {
			return fld.Name
		}
		return name
	})

	return &CustomValidator{v: v}
}

// Validate implementa echo.Validator
func (cv *CustomValidator) Validate(i interface{}) error {
	if err := cv.v.Struct(i); err != nil {
		var errs validator.ValidationErrors
		if errors.As(err, &errs) {
			messages := make([]string, 0, len(errs))
			for _, e := range errs {
				messages = append(messages, formatError(e))
			}
			return fmt.Errorf("%s", strings.Join(messages, "; "))
		}
		return err
	}
	return nil
}

func formatError(e validator.FieldError) string {
	field := e.Field()
	switch e.Tag() {
	case "required":
		return fmt.Sprintf("%s is required", field)
	case "email":
		return fmt.Sprintf("%s must be a valid email address", field)
	case "min":
		return fmt.Sprintf("%s must be at least %s characters", field, e.Param())
	case "max":
		return fmt.Sprintf("%s must be at most %s characters", field, e.Param())
	case "oneof":
		return fmt.Sprintf("%s must be one of: %s", field, e.Param())
	case "uuid4":
		return fmt.Sprintf("%s must be a valid UUID", field)
	default:
		return fmt.Sprintf("%s failed validation: %s", field, e.Tag())
	}
}
