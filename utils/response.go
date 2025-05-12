package utils

import (
	"github.com/labstack/echo/v4"
)

type APIResponse struct {
	Status  int         `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`  // Solo aparece si tiene valor
	Error   string      `json:"error,omitempty"` // Solo aparece si tiene valor
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
