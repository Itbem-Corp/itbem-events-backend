package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/models"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPErrorHandlerWritesAPIResponseForNotFound(t *testing.T) {
	e := newServer(&models.Config{})

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, float64(http.StatusNotFound), body["status"])
	assert.Equal(t, "Not Found", body["message"])
	assert.NotContains(t, body, "data")
	assert.NotContains(t, body, "error")
}

func TestHTTPErrorHandlerWritesAPIResponseForBodyLimit(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = handleHTTPError
	e.Use(middleware.BodyLimit("4B"))
	e.POST("/limited", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("too large"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, float64(http.StatusRequestEntityTooLarge), body["status"])
	assert.Equal(t, "Request Entity Too Large", body["message"])
	assert.NotContains(t, body, "data")
	assert.NotContains(t, body, "error")
}

func TestHTTPErrorMessageFallsBackToStatusText(t *testing.T) {
	message := httpErrorMessage(&echo.HTTPError{Code: http.StatusMethodNotAllowed})

	assert.Equal(t, "Method Not Allowed", message)
}
