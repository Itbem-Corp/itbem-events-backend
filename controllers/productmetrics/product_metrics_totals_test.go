package productmetrics

import (
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func metricsMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return db, mock
}

var productTotalsSQL = regexp.QuoteMeta(`SELECT active.tenant_code, COUNT(DISTINCT active.user_id) AS active_users FROM product_active_user_daily active WHERE `) + `\(?active\.day BETWEEN`

// ADM-3: the per-product active count is one DISTINCT over users, not a sum
// of per-organization distinct counts.
func TestProductTotalsQuery_CrossProductCountsDistinctUsersPerProduct(t *testing.T) {
	db, mock := metricsMockDB(t)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(productTotalsSQL+`.*GROUP BY "active"."tenant_code" ORDER BY active\.tenant_code ASC`).
		WithArgs(from, to).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_code", "active_users"}).
			AddRow("eventiapp", 3).AddRow("itbem", 1))

	var totals []productTotal
	require.NoError(t, productTotalsQuery(db, from, to, newPortfolioScope("", uuid.Nil, false, true)).Scan(&totals).Error)

	assert.Equal(t, []productTotal{{TenantCode: "eventiapp", ActiveUsers: 3}, {TenantCode: "itbem", ActiveUsers: 1}}, totals)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductTotalsQuery_ScopedToTenantForNonPrimaryRoot(t *testing.T) {
	db, mock := metricsMockDB(t)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(productTotalsSQL+`.* AND active\.tenant_code = \$3 GROUP BY`).
		WithArgs(from, to, "eventiapp").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_code", "active_users"}).AddRow("eventiapp", 2))

	var totals []productTotal
	require.NoError(t, productTotalsQuery(db, from, to, newPortfolioScope("eventiapp", uuid.Nil, false, false)).Scan(&totals).Error)

	assert.Equal(t, []productTotal{{TenantCode: "eventiapp", ActiveUsers: 2}}, totals)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductTotalsQuery_ScopedToOrganization(t *testing.T) {
	db, mock := metricsMockDB(t)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	clientID := uuid.Must(uuid.NewV4())
	// ROLES-113: an organization view stays in the session product even for a
	// primary root.
	mock.ExpectQuery(productTotalsSQL+`.* AND active\.client_id = \$3 AND active\.tenant_code = \$4 GROUP BY`).
		WithArgs(from, to, clientID, "eventiapp").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_code", "active_users"}).AddRow("eventiapp", 1))

	var totals []productTotal
	require.NoError(t, productTotalsQuery(db, from, to, newPortfolioScope("eventiapp", clientID, true, true)).Scan(&totals).Error)

	assert.Len(t, totals, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ROLES-113: summaries and timeline of an organization view are limited to
// the session product, so an EventiApp session never gets tenant_code=itbem.
func TestPortfolioQueries_OrganizationViewIsLimitedToSessionProduct(t *testing.T) {
	db, mock := metricsMockDB(t)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	clientID := uuid.Must(uuid.NewV4())
	scope := newPortfolioScope(" EventiApp ", clientID, true, false)

	mock.ExpectQuery(`(?s)FROM product_metric_daily metrics LEFT JOIN clients.*metrics\.client_id = \$5 AND metrics\.tenant_code = \$6 GROUP BY`).
		WithArgs(from, to, from, to, clientID, "eventiapp", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_code", "requests"}).AddRow("eventiapp", 4))
	var summaries []metricSummary
	require.NoError(t, summariesQuery(db, from, to, scope).Scan(&summaries).Error)

	mock.ExpectQuery(`(?s)FROM product_metric_daily metrics WHERE.*metrics\.client_id = \$4 AND metrics\.tenant_code = \$5 GROUP BY`).
		WithArgs(clientID, from, to, clientID, "eventiapp").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_code", "requests"}).AddRow("eventiapp", 4))
	var timeline []dailyPoint
	require.NoError(t, timelineQuery(db, from, to, scope).Scan(&timeline).Error)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPortfolioScope_OnlyPrimaryRootWithoutOrganizationIsCrossProduct(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	assert.True(t, newPortfolioScope("eventiapp", uuid.Nil, false, true).crossProduct)
	assert.False(t, newPortfolioScope("eventiapp", clientID, true, true).crossProduct)
	assert.False(t, newPortfolioScope("eventiapp", uuid.Nil, false, false).crossProduct)
}
