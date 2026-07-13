package utils

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuccessWritesAPIResponseEnvelope(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := Success(c, http.StatusOK, "loaded", map[string]any{"id": "event-1"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, float64(http.StatusOK), body["status"])
	assert.Equal(t, "loaded", body["message"])
	assert.Equal(t, map[string]any{"id": "event-1"}, body["data"])
	assert.NotContains(t, body, "error")
}

func TestErrorWritesMessageAndOmitsEmptyErrorDetail(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := Error(c, http.StatusBadRequest, "Missing token", "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, float64(http.StatusBadRequest), body["status"])
	assert.Equal(t, "Missing token", body["message"])
	assert.NotContains(t, body, "data")
	assert.NotContains(t, body, "error")
}

func TestErrorIncludesDetailWhenPresent(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := Error(c, http.StatusUnauthorized, "Invalid or expired token", "record not found")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, float64(http.StatusUnauthorized), body["status"])
	assert.Equal(t, "Invalid or expired token", body["message"])
	assert.Equal(t, "record not found", body["error"])
	assert.NotContains(t, body, "data")
}

func TestErrorWithDataWritesAPIResponseEnvelope(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := ErrorWithData(c, http.StatusTooManyRequests, "Upload limit reached", "", map[string]any{
		"uploads_remaining": 0,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, float64(http.StatusTooManyRequests), body["status"])
	assert.Equal(t, "Upload limit reached", body["message"])
	assert.Equal(t, map[string]any{"uploads_remaining": float64(0)}, body["data"])
	assert.NotContains(t, body, "error")
}
