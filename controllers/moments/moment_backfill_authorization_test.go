package moments

import (
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	momentsService "events-stocks/services/moments"
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

// The moment variant backfill scans and requeues media for every tenant, so
// it is a platform-governance operation reserved for a primary root (level 1).
func momentBackfillContext(t *testing.T, requester models.User, tenantCode string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := requester
			return &user, nil
		},
	})
	t.Cleanup(restore)
	c, rec := newEchoCtx(http.MethodPost, "/moments/backfill", `{"limit":1}`)
	c.Set("cognito_sub", "moment-backfill-test-sub")
	if tenantCode != "" {
		c.Set("tenant_code", tenantCode)
	}
	return c, rec
}

func TestBackfillMomentVariantsRejectsNonPrimaryRootsBeforeTouchingTheDatabase(t *testing.T) {
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
			c, rec := momentBackfillContext(t, test.requester, test.tenantCode)
			var handlerErr error
			require.NotPanics(t, func() { handlerErr = BackfillMomentVariants(c) }, "the guard must reject before the global candidate query runs")
			require.NoError(t, handlerErr)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Contains(t, rec.Body.String(), "Primary platform administrator required")
		})
	}
}

func TestBackfillMomentVariantsStillRunsForPrimaryRoots(t *testing.T) {
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
			mock.ExpectQuery(`FROM "moments"`).WillReturnRows(sqlmock.NewRows([]string{"id"}))

			previousSvc := momentSvc
			momentSvc = momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{})
			t.Cleanup(func() { momentSvc = previousSvc })

			c, rec := momentBackfillContext(t, test.requester, "")
			require.NoError(t, BackfillMomentVariants(c))
			assert.Equal(t, http.StatusAccepted, rec.Code)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
