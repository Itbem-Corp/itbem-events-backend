package eventsrepository

import (
	"events-stocks/configuration"
	"events-stocks/dtos"
	"strings"
	"sync"
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

type capturedStatement struct {
	sql  string
	vars []interface{}
}

// captureEventStatements points the repository at a mock database that
// answers every query with no rows, and records each statement it runs. The
// test proves which predicates each list path binds, not what rows match
// (caller_access_scope_postgres_test.go covers the rows).
func captureEventStatements(t *testing.T) func() []capturedStatement {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	mock.MatchExpectationsInOrder(false)
	for range 32 {
		mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(nil))
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	var mu sync.Mutex
	statements := make([]capturedStatement, 0)
	record := func(tx *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		statements = append(statements, capturedStatement{
			sql:  tx.Statement.SQL.String(),
			vars: append([]interface{}(nil), tx.Statement.Vars...),
		})
	}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:capture_query", record))
	require.NoError(t, db.Callback().Row().After("gorm:row").Register("test:capture_row", record))

	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })

	return func() []capturedStatement {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedStatement(nil), statements...)
	}
}

// eventStatements keeps the statements that read the events table. The guest
// summary of the next event is a separate per-event query.
func eventStatements(statements []capturedStatement) []capturedStatement {
	result := make([]capturedStatement, 0, len(statements))
	for _, statement := range statements {
		if strings.Contains(statement.sql, `FROM "events"`) {
			result = append(result, statement)
		}
	}
	return result
}

func containsVar(vars []interface{}, expected uuid.UUID) bool {
	for _, value := range vars {
		switch typed := value.(type) {
		case uuid.UUID:
			if typed == expected {
				return true
			}
		case *uuid.UUID:
			if typed != nil && *typed == expected {
				return true
			}
		}
	}
	return false
}

// assertCallerAccessScoped checks the predicates SessionService.organizationAccess
// uses to build the session organizations: active membership, active and
// not deleted organization, a role, and an active member access row for the
// request application.
func assertCallerAccessScoped(t *testing.T, statement capturedStatement, access dtos.EventCallerAccess) {
	t.Helper()
	sql := strings.Join(strings.Fields(statement.sql), " ")
	for _, fragment := range []string{
		"FROM client_members cm",
		"cm.client_id = events.client_id",
		"cm.is_active = true",
		"JOIN clients c ON c.id = cm.client_id AND c.deleted_at IS NULL AND c.is_active = true",
		"JOIN client_roles cr ON cr.id = cm.client_role_id",
		"JOIN client_member_applications cma ON cma.client_member_id = cm.id AND cma.is_active = true AND cma.application_id = $",
		"cm.user_id = $",
	} {
		assert.Contains(t, sql, fragment, "caller-access predicate is missing %q in %s", fragment, sql)
	}
	assert.True(t, containsVar(statement.vars, access.ApplicationID), "request application must be bound: %s", sql)
	assert.True(t, containsVar(statement.vars, access.UserID), "caller must be bound: %s", sql)
}

func newCallerAccess() dtos.EventCallerAccess {
	return dtos.EventCallerAccess{
		UserID:        uuid.Must(uuid.NewV4()),
		ApplicationID: uuid.Must(uuid.NewV4()),
	}
}

func TestCallerAccessListsBindRequestApplicationOnEveryEventQuery(t *testing.T) {
	now := time.Date(2026, time.October, 11, 18, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		minimum int
		run     func(access dtos.EventCallerAccess) error
	}{
		{name: "unpaged list", minimum: 1, run: func(access dtos.EventCallerAccess) error {
			_, err := GetEventsForUser(access)
			return err
		}},
		{name: "paged list with filtered total", minimum: 3, run: func(access dtos.EventCallerAccess) error {
			_, _, _, err := ListEventPage(nil, &access, dtos.EventListQuery{Page: 1, PageSize: 20, Filter: "upcoming", Search: "boda", Now: now})
			return err
		}},
		{name: "dashboard overview", minimum: 3, run: func(access dtos.EventCallerAccess) error {
			_, err := GetEventDashboardOverview(nil, &access, now)
			return err
		}},
		{name: "notifications", minimum: 1, run: func(access dtos.EventCallerAccess) error {
			_, err := ListEventNotifications(nil, &access, now)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			statements := captureEventStatements(t)
			access := newCallerAccess()

			require.NoError(t, tc.run(access))

			captured := eventStatements(statements())
			require.GreaterOrEqual(t, len(captured), tc.minimum, "every events query must be captured")
			for _, statement := range captured {
				assertCallerAccessScoped(t, statement, access)
			}
		})
	}
}

func TestExplicitOrganizationScopeDoesNotUseCallerAccess(t *testing.T) {
	statements := captureEventStatements(t)
	clientID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.October, 11, 18, 0, 0, 0, time.UTC)

	_, _, _, err := ListEventPage(&clientID, nil, dtos.EventListQuery{Page: 1, PageSize: 20, Filter: "all", Now: now})
	require.NoError(t, err)
	_, err = GetEventDashboardOverview(&clientID, nil, now)
	require.NoError(t, err)
	_, err = ListEventNotifications(&clientID, nil, now)
	require.NoError(t, err)

	captured := eventStatements(statements())
	require.NotEmpty(t, captured)
	for _, statement := range captured {
		assert.Contains(t, statement.sql, "events.client_id = $")
		assert.NotContains(t, statement.sql, "client_member_applications")
		assert.True(t, containsVar(statement.vars, clientID))
	}
}

func TestPlatformScopeKeepsUnscopedEventLists(t *testing.T) {
	statements := captureEventStatements(t)
	now := time.Date(2026, time.October, 11, 18, 0, 0, 0, time.UTC)

	_, _, _, err := ListEventPage(nil, nil, dtos.EventListQuery{Page: 1, PageSize: 20, Filter: "all", Now: now})
	require.NoError(t, err)
	_, err = GetEventDashboardOverview(nil, nil, now)
	require.NoError(t, err)

	captured := eventStatements(statements())
	require.NotEmpty(t, captured)
	for _, statement := range captured {
		assert.NotContains(t, statement.sql, "client_members")
		assert.NotContains(t, statement.sql, "events.client_id = $")
	}
}
