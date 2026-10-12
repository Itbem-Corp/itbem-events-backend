package eventmembers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/internal/products/eventiapp"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capabilitiesFor(t *testing.T, hooks authz.Hooks) (int, map[string]interface{}) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(hooks)
	t.Cleanup(restore)
	e := echo.New()
	recorder := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/events/x/capabilities", nil), recorder)
	c.SetParamNames("id")
	c.SetParamValues(uuid.Must(uuid.NewV4()).String())
	c.Set("cognito_sub", "sub")
	c.Set("tenant_code", "eventiapp")
	require.NoError(t, Capabilities(c))
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return recorder.Code, body
}

func baseHooks(orgRole string, granted bool, memberErr error) authz.Hooks {
	orgID := uuid.Must(uuid.NewV4())
	return authz.Hooks{
		SyncUser:        func(string) (*models.User, error) { return &models.User{ID: uuid.Must(uuid.NewV4())}, nil },
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) { return &models.Event{ID: id, ClientID: &orgID}, nil },
		CheckAccessRecursiveE: func(uuid.UUID, uuid.UUID) (bool, string, error) {
			return orgRole != "", orgRole, nil
		},
		GetEventMemberRole: func(uuid.UUID, uuid.UUID) (string, bool, error) { return "", false, memberErr },
		HasEventClientGrant: func(uuid.UUID, uuid.UUID) (bool, error) {
			return granted, nil
		},
		ResolveClientProduct: func(uuid.UUID) (string, error) { return "eventiapp", nil },
	}
}

func TestCapabilitiesForEventClient(t *testing.T) {
	eventiapp.SetEventClientAccess(true)
	t.Cleanup(func() { eventiapp.SetEventClientAccess(false) })
	status, body := capabilitiesFor(t, baseHooks("", true, nil))
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, map[string]interface{}{
		"event:manage": true, "event:delete": false, "guest:manage": true, "checkin:run": true,
		"analytics:view": true, "members:manage": false, "event:clients:manage": false,
	}, body["data"])
}

func TestCapabilitiesEventClientsManageFollowsFlag(t *testing.T) {
	eventiapp.SetEventClientAccess(true)
	_, body := capabilitiesFor(t, baseHooks("EVENT_MANAGER", false, nil))
	assert.Equal(t, true, body["data"].(map[string]interface{})["event:clients:manage"])

	eventiapp.SetEventClientAccess(false)
	_, body = capabilitiesFor(t, baseHooks("EVENT_MANAGER", false, nil))
	assert.Equal(t, false, body["data"].(map[string]interface{})["event:clients:manage"])
}

func TestCapabilitiesDependencyFailureIs500(t *testing.T) {
	status, body := capabilitiesFor(t, baseHooks("EDITOR", false, errors.New("db down")))
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, "Authorization error", body["message"])
}
