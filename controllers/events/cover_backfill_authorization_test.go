package events

import (
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The cover backfill scans and requeues covers for every tenant, so it is a
// platform-governance operation reserved for a primary root (level 1).
func coverBackfillContext(t *testing.T, requester models.User, tenantCode string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := requester
			return &user, nil
		},
	})
	t.Cleanup(restore)
	c, rec := newEchoCtx(http.MethodPost, "/events/covers/backfill", `{"limit":1}`)
	c.Set("cognito_sub", "cover-backfill-test-sub")
	if tenantCode != "" {
		c.Set("tenant_code", tenantCode)
	}
	return c, rec
}

func TestBackfillEventCoversRejectsNonPrimaryRootsBeforeTouchingTheDatabase(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })

	tests := []struct {
		name       string
		requester  models.User
		tenantCode string
	}{
		{name: "operational root", requester: models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}},
		{name: "legacy root flag with operational level", requester: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelOperational}},
		{name: "standard user", requester: models.User{ID: uuid.Must(uuid.NewV4())}},
		{name: "primary root through another product", requester: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}, tenantCode: "cafettonhouse"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, rec := coverBackfillContext(t, test.requester, test.tenantCode)
			var handlerErr error
			require.NotPanics(t, func() { handlerErr = BackfillEventCovers(c) }, "the guard must reject before the global candidate query runs")
			require.NoError(t, handlerErr)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Contains(t, rec.Body.String(), "Primary platform administrator required")
		})
	}
}

func TestBackfillEventCoversStillRunsForPrimaryRoots(t *testing.T) {
	tests := []struct {
		name      string
		requester models.User
	}{
		{name: "primary root level", requester: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}},
		{name: "legacy root flag", requester: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
				DisableAutomaticPing: true,
				Logger:               logger.Default.LogMode(logger.Silent),
			})
			require.NoError(t, err)
			previousDB := configuration.DB
			configuration.DB = db
			t.Cleanup(func() { configuration.DB = previousDB })
			mock.ExpectQuery(`FROM "events"`).WillReturnRows(sqlmock.NewRows([]string{"id"}))

			c, rec := coverBackfillContext(t, test.requester, "")
			require.NoError(t, BackfillEventCovers(c))
			assert.Equal(t, http.StatusAccepted, rec.Code)
			assert.Contains(t, rec.Body.String(), `"candidates":0`)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
