package userrepository

import (
	"strings"
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	sqlDB, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		DryRun:               true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return db
}

func buildSearch(t *testing.T, search string) (string, []interface{}) {
	t.Helper()
	var users []models.User
	stmt := applyAdminUsersListFilters(dryRunDB(t).Model(&models.User{}), dtos.AdminUsersListQuery{Search: search}).
		Find(&users).Statement
	return stmt.SQL.String(), stmt.Vars
}

// ADM-12: "Juan Pérez" must match first_name || ' ' || last_name.
func TestAdminUsersSearch_MatchesFullName(t *testing.T) {
	sql, vars := buildSearch(t, "  Juan   Pérez ")

	assert.Contains(t, sql, "LOWER(CONCAT_WS(' ', first_name, last_name)) LIKE")
	require.Len(t, vars, 4)
	for _, v := range vars {
		assert.Equal(t, "%juan pérez%", v)
	}
}

// ADM-12: LIKE wildcards in the input are searched literally.
func TestAdminUsersSearch_EscapesLikeWildcards(t *testing.T) {
	sql, vars := buildSearch(t, `50%_off\x`)

	assert.Equal(t, 4, strings.Count(sql, `ESCAPE '\'`))
	require.Len(t, vars, 4)
	assert.Equal(t, `%50\%\_off\\x%`, vars[0])
}

func TestAdminUsersSearch_EmptySearchAddsNoFilter(t *testing.T) {
	sql, vars := buildSearch(t, "   ")
	assert.NotContains(t, sql, "LIKE")
	assert.Empty(t, vars)
}

func TestEscapeLikePattern(t *testing.T) {
	assert.Equal(t, `a\%b\_c\\d`, escapeLikePattern(`a%b_c\d`))
	assert.Equal(t, "plain", escapeLikePattern("plain"))
}

// ROLES-94: Root 2's directory hides roots AND still honours "inactive".
func TestAdminUsersFilters_ExcludeRootsKeepsInactiveFilter(t *testing.T) {
	var users []models.User
	stmt := applyAdminUsersListFilters(dryRunDB(t).Model(&models.User{}), dtos.AdminUsersListQuery{
		Status:       "inactive",
		ExcludeRoots: true,
	}).Find(&users).Statement
	sql := stmt.SQL.String()

	assert.Contains(t, sql, "is_active =")
	assert.Contains(t, sql, "root_level =")
	assert.Contains(t, stmt.Vars, false)
}

func TestAdminUsersFilters_ActiveWithoutExcludeRootsHasNoRootClause(t *testing.T) {
	var users []models.User
	stmt := applyAdminUsersListFilters(dryRunDB(t).Model(&models.User{}), dtos.AdminUsersListQuery{Status: "active"}).
		Find(&users).Statement
	assert.Contains(t, stmt.SQL.String(), "is_active =")
	assert.NotContains(t, stmt.SQL.String(), "root_level")
}
