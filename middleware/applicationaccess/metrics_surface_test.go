package applicationaccess

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"events-stocks/models"
	"events-stocks/services/applications"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// configureEventiAppSession wires Require to a real SessionService whose
// database returns one EventiApp membership with the given role.
func configureEventiAppSession(t *testing.T, role string) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	applicationID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "applications"`)).
		WithArgs("eventiapp", 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "code", "name", "product_label", "modules",
			"allows_platform_admin", "is_active", "created_at", "updated_at",
		}).AddRow(applicationID, "eventiapp", "EventiApp", "Event operations", `["home","events","metrics"]`, true, true, now, now))
	mock.ExpectQuery(`SELECT DISTINCT\s+clients.id`).
		WithArgs(applicationID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "code", "logo", "access_role"}).
			AddRow(uuid.Must(uuid.NewV4()), "Organization", "organization", "", role))
	mock.ExpectQuery(`SELECT \* FROM "user_application_policies"`).
		WithArgs(userID, applicationID, 1).
		WillReturnError(gorm.ErrRecordNotFound)

	previous := sessionService
	Configure(applications.NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	}))
	t.Cleanup(func() { sessionService = previous })
}

func serveThroughRequire(t *testing.T, method, path string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	e := echo.New()
	recorder := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(method, path, nil), recorder)
	c.Set("cognito_sub", "member-sub")
	c.Set("tenant_code", "eventiapp")
	reached := false
	err := Require(func(c echo.Context) error {
		reached = true
		return c.NoContent(http.StatusOK)
	})(c)
	require.NoError(t, err)
	return recorder, reached
}

func TestEventiAppMetricsSurfaceDeniedWithoutMetricsView(t *testing.T) {
	for _, role := range []string{"EVENT_MANAGER", "EDITOR", "ANALYST"} {
		t.Run(role, func(t *testing.T) {
			configureEventiAppSession(t, role)

			recorder, reached := serveThroughRequire(t, http.MethodGet, "/api/metrics/portfolio")

			assert.False(t, reached)
			assert.Equal(t, http.StatusForbidden, recorder.Code)
			var body struct {
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			assert.Equal(t, "Application capability denied", body.Message)
		})
	}
}

func TestEventiAppMetricsSurfaceAllowsOrganizationAdmins(t *testing.T) {
	for _, role := range []string{"Owner", "Admin"} {
		t.Run(role, func(t *testing.T) {
			configureEventiAppSession(t, role)

			recorder, reached := serveThroughRequire(t, http.MethodGet, "/api/metrics/portfolio")

			assert.True(t, reached)
			assert.Equal(t, http.StatusOK, recorder.Code)
		})
	}
}

// R3 regression: event analytics keep their events:view surface gate for the
// analytics roles that lost /api/metrics.
func TestEventAnalyticsSurfaceStillOpenForAnalyticsRoles(t *testing.T) {
	for _, role := range []string{"EVENT_MANAGER", "EDITOR", "ANALYST"} {
		t.Run(role, func(t *testing.T) {
			configureEventiAppSession(t, role)

			recorder, reached := serveThroughRequire(t, http.MethodGet, "/api/events/"+uuid.Must(uuid.NewV4()).String()+"/analytics")

			assert.True(t, reached)
			assert.Equal(t, http.StatusOK, recorder.Code)
		})
	}
}
