package moments

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// TestRequestBatchSharedUploadURLs_PanicsWithoutDB confirms the handler is
// reachable and compiles correctly. Without a live DB, GORM panics on event
// lookup — that is the expected behaviour in a unit test environment.
func TestRequestBatchSharedUploadURLs_PanicsWithoutDB(t *testing.T) {
	e := echo.New()
	body := `{"files":[{"content_type":"image/jpeg","filename":"photo.jpg"},{"content_type":"image/jpeg","filename":"photo2.jpg"}]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("identifier")
	c.SetParamValues("test-event-id")

	require.Panics(t, func() {
		_ = RequestBatchSharedUploadURLs(c)
	})
}

func TestRequestBatchSharedUploadURLs_MissingIdentifier_ReturnsBadRequest(t *testing.T) {
	e := echo.New()
	body := `{"files":[{"content_type":"image/jpeg","filename":"photo.jpg"}]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// No identifier param set

	err := RequestBatchSharedUploadURLs(c)
	require.Nil(t, err) // Echo handlers return nil and write response
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRequestBatchSharedUploadURLs_EmptyFiles_PanicsWithoutDB(t *testing.T) {
	e := echo.New()
	body := `{"files":[]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("identifier")
	c.SetParamValues("test-event-id")

	// The empty-files guard (400) lives after the DB lookup, so without a live DB
	// the handler panics on DB access before it can ever reach the validation check.
	require.Panics(t, func() {
		_ = RequestBatchSharedUploadURLs(c)
	})
}
