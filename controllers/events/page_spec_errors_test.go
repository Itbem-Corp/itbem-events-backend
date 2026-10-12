package events

import (
	"net/http"
	"net/http/httptest"
	"testing"

	eventsService "events-stocks/services/events"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PUB-33/43/74, GST-36, EVT-93: the public site branches on these codes.
func TestRespondPageSpecAccessErrorCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{eventsService.ErrPageSpecTokenExpired, http.StatusUnauthorized, PageSpecTokenExpiredCode},
		{eventsService.ErrPageSpecTokenNotFound, http.StatusUnauthorized, PageSpecTokenInvalidCode},
		{eventsService.ErrPageSpecEventGone, http.StatusGone, PageSpecEventGoneCode},
		{eventsService.ErrPageSpecNotPublic, http.StatusForbidden, PageSpecNotPublicCode},
		{eventsService.ErrPageSpecInactive, http.StatusForbidden, PageSpecNotPublicCode},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		handled, err := respondPageSpecAccessError(c, tc.err)
		require.True(t, handled)
		require.NoError(t, err)
		assert.Equal(t, tc.status, rec.Code)
		assert.Contains(t, rec.Body.String(), `"code":"`+tc.code+`"`)
		assert.Contains(t, rec.Body.String(), `"error":"`+tc.code+`"`)
	}
}
