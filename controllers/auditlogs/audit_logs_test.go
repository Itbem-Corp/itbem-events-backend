package auditlogs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPositiveIntBoundsPagination(t *testing.T) {
	assert.Equal(t, 50, positiveInt("", 50, 100))
	assert.Equal(t, 50, positiveInt("-2", 50, 100))
	assert.Equal(t, 25, positiveInt("25", 50, 100))
	assert.Equal(t, 100, positiveInt("999", 50, 100))
}

type auditLogResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

func listAuditLogsAs(t *testing.T, user *models.User, organizationRole string) (int, auditLogResponse) {
	t.Helper()
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) { return user, nil },
		// The gate must not consult organization roles at all.
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) {
			return organizationRole != "", organizationRole
		},
	})
	t.Cleanup(restore)

	e := echo.New()
	recorder := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil), recorder)
	c.Set("cognito_sub", "caller-sub")
	c.Set("tenant_code", "eventiapp")
	require.NoError(t, List(c))
	var body auditLogResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return recorder.Code, body
}

func TestAuditLogsDenyEveryoneButPrimaryRoot(t *testing.T) {
	tests := []struct {
		name string
		user *models.User
		role string
	}{
		{name: "operational root", user: &models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}},
		{name: "owner", user: &models.User{ID: uuid.Must(uuid.NewV4())}, role: "Owner"},
		{name: "admin", user: &models.User{ID: uuid.Must(uuid.NewV4())}, role: "Admin"},
		{name: "event manager", user: &models.User{ID: uuid.Must(uuid.NewV4())}, role: "EVENT_MANAGER"},
		{name: "no memberships", user: &models.User{ID: uuid.Must(uuid.NewV4())}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, body := listAuditLogsAs(t, test.user, test.role)
			assert.Equal(t, http.StatusForbidden, status)
			assert.Equal(t, "Forbidden", body.Message)
			assert.Equal(t, "Primary platform administrator required", body.Error)
		})
	}
}

func TestAuditLogsPrimaryRootPassesTheGate(t *testing.T) {
	for _, user := range []*models.User{
		{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelPrimary},
		// Historical is_root accounts are treated as Root 1.
		{ID: uuid.Must(uuid.NewV4()), IsRoot: true},
	} {
		status, body := listAuditLogsAs(t, user, "")
		// With no database the handler stops right after the gate.
		assert.Equal(t, http.StatusServiceUnavailable, status)
		assert.Equal(t, "Audit trail unavailable", body.Message)
	}
}
