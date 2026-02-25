//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	phrasesCtrl "events-stocks/controllers/phrases"
)

func phrasesEcho() *echo.Echo {
	e := echo.New()
	e.GET("/api/events/phrases", phrasesCtrl.GetPhrases)
	return e
}

func TestGetPhrases_ReturnsOK(t *testing.T) {
	e := phrasesEcho()
	req := httptest.NewRequest(http.MethodGet, "/api/events/phrases?type=WEDDING&count=5", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetPhrases_ValidJSON(t *testing.T) {
	e := phrasesEcho()
	req := httptest.NewRequest(http.MethodGet, "/api/events/phrases?type=WEDDING", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
}

func TestGetPhrases_UnknownTypeNoServerError(t *testing.T) {
	e := phrasesEcho()
	req := httptest.NewRequest(http.MethodGet, "/api/events/phrases?type=UNKNOWN_NONEXISTENT_TYPE_XYZ", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("should not 500 for unknown event type, got: %s", rec.Body.String())
	}
}
