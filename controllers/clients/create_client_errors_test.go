package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	clientService "events-stocks/services/clients"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createErrorResponse(t *testing.T, err error) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/api/clients", nil), rec)
	require.NoError(t, respondCreateClientError(c, err))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec.Code, body
}

// ROLES-103: creation errors are no longer reported as an upload failure.
func TestRespondCreateClientError_MapsRuleErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: customers cannot have sub-clients", clientService.ErrClientHierarchyLimit), http.StatusBadRequest, ErrCodeClientHierarchyLimit},
		{fmt.Errorf("%w: agencies can only create customers", clientService.ErrClientChildTypeNotAllowed), http.StatusBadRequest, ErrCodeClientTypeNotAllowed},
		{fmt.Errorf("%w: x", clientService.ErrClientTypeInvalid), http.StatusBadRequest, ErrCodeClientTypeInvalid},
		{fmt.Errorf("%w: x", clientService.ErrClientPermissionDenied), http.StatusForbidden, ErrCodeClientPermissionDenied},
		{fmt.Errorf("%w: x", clientService.ErrClientParentNotFound), http.StatusNotFound, ErrCodeClientParentNotFound},
		{fmt.Errorf("%w: x", clientService.ErrClientCodeConflict), http.StatusConflict, ErrCodeClientCodeConflict},
		{errors.New("database is down"), http.StatusInternalServerError, "CLIENT_CREATE_FAILED"},
	}
	for _, tc := range cases {
		status, body := createErrorResponse(t, tc.err)
		assert.Equal(t, tc.status, status, tc.err.Error())
		assert.Equal(t, tc.code, body["error"], tc.err.Error())
		assert.NotContains(t, fmt.Sprint(body["message"]), "upload", tc.err.Error())
	}
}

func TestRespondCreateClientError_LogoFailureKeepsUploadMessage(t *testing.T) {
	status, body := createErrorResponse(t, fmt.Errorf("%w: failed to create client logo: %w", clientService.ErrClientLogoUpload, errors.New("s3 down")))
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, "Failed to create client", body["message"])
}
