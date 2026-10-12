package utils

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
)

// WriteFailedCode is the stable "error" code of a 500 from a failed database
// write. The cause is logged, never sent: since gormrepository.Update returns
// the driver error, err.Error() can carry Postgres constraint and column text.
const WriteFailedCode = "write_failed"

// WriteFailedMessage is the Spanish message sent with WriteFailedCode.
const WriteFailedMessage = "No se pudieron guardar los cambios. Inténtalo de nuevo."

// WriteFailed logs a failed write (operation, method, route, error) and
// answers 500 with WriteFailedCode and a Spanish message.
func WriteFailed(c echo.Context, operation string, err error) error {
	slog.Error("write failed", "operation", operation, "method", c.Request().Method, "route", c.Path(), "error", err)
	return Error(c, http.StatusInternalServerError, WriteFailedMessage, WriteFailedCode)
}

type APIResponse struct {
	Status  int         `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// Respuesta exitosa
func Success(c echo.Context, status int, message string, data interface{}) error {
	return c.JSON(status, APIResponse{
		Status:  status,
		Message: message,
		Data:    data,
	})
}

// Respuesta con error
func Error(c echo.Context, status int, message string, err string) error {
	return c.JSON(status, APIResponse{
		Status:  status,
		Message: message,
		Error:   err,
	})
}

func ErrorWithData(c echo.Context, status int, message string, err string, data interface{}) error {
	return c.JSON(status, APIResponse{
		Status:  status,
		Message: message,
		Data:    data,
		Error:   err,
	})
}
