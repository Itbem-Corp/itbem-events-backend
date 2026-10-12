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
	require.NoError(t, productTotalsQuery(db, from, to, "", uuid.Nil, false, true).Scan(&totals).Error)

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
	require.NoError(t, productTotalsQuery(db, from, to, "eventiapp", uuid.Nil, false, false).Scan(&totals).Error)

	assert.Equal(t, []productTotal{{TenantCode: "eventiapp", ActiveUsers: 2}}, totals)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductTotalsQuery_ScopedToOrganization(t *testing.T) {
	db, mock := metricsMockDB(t)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	clientID := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(productTotalsSQL+`.* AND active\.client_id = \$3 GROUP BY`).
		WithArgs(from, to, clientID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_code", "active_users"}).AddRow("eventiapp", 1))

	var totals []productTotal
	require.NoError(t, productTotalsQuery(db, from, to, "eventiapp", clientID, true, true).Scan(&totals).Error)

	assert.Len(t, totals, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}
