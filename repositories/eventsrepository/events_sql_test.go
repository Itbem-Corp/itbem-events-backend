package eventsrepository

import (
	"regexp"
	"testing"

	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func useMockDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() {
		configuration.DB = previous
		_ = sqlDB.Close()
	})
	return mock
}

func TestEventListSearchUsesAccentFold(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectQuery(regexp.QuoteMeta(`lower(translate(COALESCE(events.name, ''),`)).
		WithArgs("%nandu%", "%nandu%", "%nandu%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	var total int64
	err := applyEventListFilters(configuration.DB.Model(&models.Event{}), dtos.EventListQuery{Search: "Ñandú"}).Count(&total).Error
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
