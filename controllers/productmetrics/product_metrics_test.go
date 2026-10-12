package productmetrics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// membershipHooks answers CheckAccessRecursive from a fixed org -> role map,
// so a client_id outside the map is an organization the caller cannot reach.
func membershipHooks(t *testing.T, roles map[uuid.UUID]string) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		CheckAccessRecursive: func(_ uuid.UUID, clientID uuid.UUID) (bool, string) {
			role, ok := roles[clientID]
			return ok, role
		},
		GetClientByID: func(clientID uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: clientID, Code: "cafettonhouse"}, nil
		},
	})
	t.Cleanup(restore)
}

func requireFailure(t *testing.T, err error, status int, message string) {
	t.Helper()
	require.Error(t, err)
	failure, ok := err.(*authz.Failure)
	require.True(t, ok, "expected *authz.Failure, got %T", err)
	assert.Equal(t, status, failure.Status)
	if message != "" {
		assert.Equal(t, message, failure.Message)
	}
}

func TestAuthorizePortfolioScope(t *testing.T) {
	member := &models.User{ID: uuid.Must(uuid.NewV4())}
	operationalRoot := &models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}
	ownClient := uuid.Must(uuid.NewV4())
	otherClient := uuid.Must(uuid.NewV4())

	t.Run("eventiapp analytics roles cannot read organization metrics", func(t *testing.T) {
		for _, role := range []string{"EVENT_MANAGER", "ANALYST", "EDITOR"} {
			membershipHooks(t, map[uuid.UUID]string{ownClient: role})
			requireFailure(t, authorizePortfolioScope(member, "eventiapp", ownClient, true), http.StatusForbidden, "Access denied")
		}
	})
	t.Run("eventiapp owner and inherited owner can", func(t *testing.T) {
		for _, role := range []string{"Owner", "INHERITED_OWNER", "Admin", "INHERITED_ADMIN"} {
			membershipHooks(t, map[uuid.UUID]string{ownClient: role})
			assert.NoError(t, authorizePortfolioScope(member, "eventiapp", ownClient, true), role)
		}
	})
	t.Run("cafettonhouse keeps analytics roles", func(t *testing.T) {
		membershipHooks(t, map[uuid.UUID]string{ownClient: "EDITOR"})
		portalMember := &models.User{ID: member.ID, AuthTenantCode: "cafettonhouse"}
		assert.NoError(t, authorizePortfolioScope(portalMember, "cafettonhouse", ownClient, true))
	})
	t.Run("itbem keeps analytics roles", func(t *testing.T) {
		membershipHooks(t, map[uuid.UUID]string{ownClient: "ANALYST"})
		assert.NoError(t, authorizePortfolioScope(member, "itbem", ownClient, true))
	})
	t.Run("no organization for a non-root", func(t *testing.T) {
		membershipHooks(t, map[uuid.UUID]string{ownClient: "Owner"})
		err := authorizePortfolioScope(member, "eventiapp", uuid.Nil, false)
		requireFailure(t, err, http.StatusForbidden, "Organization required")
		assert.Equal(t, "Select an organization to view metrics", err.(*authz.Failure).Detail)
	})
	t.Run("operational root without organization", func(t *testing.T) {
		assert.NoError(t, authorizePortfolioScope(operationalRoot, "eventiapp", uuid.Nil, false))
	})
	t.Run("operational root with organization", func(t *testing.T) {
		assert.NoError(t, authorizePortfolioScope(operationalRoot, "eventiapp", otherClient, true))
	})
	t.Run("another organization's client_id is denied", func(t *testing.T) {
		membershipHooks(t, map[uuid.UUID]string{ownClient: "EVENT_MANAGER"})
		requireFailure(t, authorizePortfolioScope(member, "eventiapp", otherClient, true), http.StatusForbidden, "Access denied")
		membershipHooks(t, map[uuid.UUID]string{ownClient: "Owner"})
		requireFailure(t, authorizePortfolioScope(member, "eventiapp", otherClient, true), http.StatusForbidden, "Access denied")
		membershipHooks(t, map[uuid.UUID]string{ownClient: "EDITOR"})
		requireFailure(t, authorizePortfolioScope(member, "itbem", otherClient, true), http.StatusForbidden, "Access denied")
	})
}

func TestPortfolioRespondsWithAuthorizationFailure(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser:             func(string) (*models.User, error) { return &models.User{ID: userID}, nil },
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "EVENT_MANAGER" },
	})
	t.Cleanup(restore)

	tests := []struct {
		name    string
		query   string
		message string
		detail  string
	}{
		{name: "organization", query: "?client_id=" + clientID.String(), message: "Access denied", detail: "Your organization role cannot perform this action"},
		{name: "no organization", query: "", message: "Organization required", detail: "Select an organization to view metrics"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			recorder := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/metrics/portfolio"+test.query, nil), recorder)
			c.Set("cognito_sub", "member-sub")
			c.Set("tenant_code", "eventiapp")

			require.NoError(t, Portfolio(c))

			assert.Equal(t, http.StatusForbidden, recorder.Code)
			var body struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
				Error   string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			assert.Equal(t, http.StatusForbidden, body.Status)
			assert.Equal(t, test.message, body.Message)
			assert.Equal(t, test.detail, body.Error)
		})
	}
}
