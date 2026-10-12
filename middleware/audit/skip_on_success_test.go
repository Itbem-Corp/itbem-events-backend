package audit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/configuration"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func auditContext(method, path string, marked bool) echo.Context {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(method, path, nil), httptest.NewRecorder())
	c.SetPath(path)
	if marked {
		SkipOnSuccess(c)
	}
	return c
}

func TestShouldPersistSkipsSuccessfulMarkedRenewal(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated} {
		c := auditContext(http.MethodPost, "/api/session/organization-context", true)
		assert.False(t, shouldPersist(c, http.MethodPost, status), status)
	}
	// Unmarked renewals (real switches) stay audited.
	c := auditContext(http.MethodPost, "/api/session/organization-context", false)
	assert.True(t, shouldPersist(c, http.MethodPost, http.StatusOK))
}

func TestShouldPersistIgnoresFlagOnOtherRoutes(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/events"},
		{method: http.MethodPut, path: "/api/events/:id"},
		{method: http.MethodDelete, path: "/api/clients/:id/members/:member_id"},
		{method: http.MethodPost, path: "/api/session/organization-context/extra"},
		// The same route with another method is not the renewal.
		{method: http.MethodPut, path: "/api/session/organization-context"},
	} {
		c := auditContext(test.method, test.path, true)
		assert.True(t, shouldPersist(c, test.method, http.StatusOK), "%s %s", test.method, test.path)
	}
}

func TestShouldPersistKeepsDenialsWhenMarked(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusInternalServerError, http.StatusServiceUnavailable,
	} {
		c := auditContext(http.MethodPost, "/api/session/organization-context", true)
		assert.True(t, shouldPersist(c, http.MethodPost, status), status)
	}
	denied := auditContext(http.MethodGet, "/api/metrics/portfolio", true)
	assert.True(t, shouldPersist(denied, http.MethodGet, http.StatusForbidden))
	read := auditContext(http.MethodGet, "/api/metrics/portfolio", false)
	assert.False(t, shouldPersist(read, http.MethodGet, http.StatusOK))
}

func TestSkipOnSuccessRequestedIsFalseByDefault(t *testing.T) {
	c := auditContext(http.MethodPost, "/api/session/organization-context", false)
	assert.False(t, SkipOnSuccessRequested(c))
	SkipOnSuccess(c)
	assert.True(t, SkipOnSuccessRequested(c))
}

// countAuditWrites points configuration.DB at a dry-run database and counts
// the audit rows Mutations tries to create.
func countAuditWrites(t *testing.T) *int {
	t.Helper()
	sqlDB, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing:   true,
		DryRun:                 true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	writes := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:count_audit_writes", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			writes++
		}
	}))
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })
	return &writes
}

func TestMutationsHonoursSkipOnlyForSuccessfulRenewals(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		mark   bool
		status int
		writes int
	}{
		{name: "marked renewal", method: http.MethodPost, path: "/api/session/organization-context", mark: true, status: http.StatusOK, writes: 0},
		{name: "unmarked switch", method: http.MethodPost, path: "/api/session/organization-context", mark: false, status: http.StatusOK, writes: 1},
		{name: "marked denial", method: http.MethodPost, path: "/api/session/organization-context", mark: true, status: http.StatusForbidden, writes: 1},
		{name: "marked error", method: http.MethodPost, path: "/api/session/organization-context", mark: true, status: http.StatusServiceUnavailable, writes: 1},
		{name: "marked mutation elsewhere", method: http.MethodPost, path: "/api/events", mark: true, status: http.StatusCreated, writes: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writes := countAuditWrites(t)
			c := auditContext(test.method, test.path, false)
			handler := Mutations(func(c echo.Context) error {
				if test.mark {
					SkipOnSuccess(c)
				}
				return c.NoContent(test.status)
			})

			require.NoError(t, handler(c))
			assert.Equal(t, test.writes, *writes)
		})
	}
}
