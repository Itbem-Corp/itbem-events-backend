package applications

import (
	"errors"
	"testing"

	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const eventiAppModules = `["home","events","organizations","metrics"]`

func emptyOrganizationRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "name", "code", "logo", "access_role"})
}

func TestGrantOnlyUserReceivesEventAccessSession(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplicationWithModules(mock, "eventiapp", true, eventiAppModules)
	expectOrganizations(mock, appID, userID, emptyOrganizationRows())
	expectNoUserApplicationPolicy(mock, userID, appID)
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})
	service.ConfigureEventClientAccess(func(id uuid.UUID) (int, error) {
		assert.Equal(t, userID, id)
		return 2, nil
	})

	session, err := service.Resolve("client-sub", "eventiapp")

	require.NoError(t, err)
	assert.Empty(t, session.Organizations)
	require.NotNil(t, session.EventAccess)
	assert.Equal(t, 2, session.EventAccess.AssignedEvents)
	assert.Equal(t, []string{"analytics:view", "checkin:run", "events:manage", "events:view", "guests:manage"}, session.EventAccess.Capabilities)
	for _, capability := range session.EventAccess.Capabilities {
		assert.Contains(t, session.Capabilities, capability)
	}
	assert.NotContains(t, session.Capabilities, "events:create")
	assert.NotContains(t, session.Capabilities, "events:delete")
	assert.NotContains(t, session.Capabilities, "metrics:view")
	assert.NotContains(t, session.Capabilities, "members:manage")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGrantOnlyUserDeniedWhenFlagOff(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplicationWithModules(mock, "eventiapp", true, eventiAppModules)
	expectOrganizations(mock, appID, userID, emptyOrganizationRows())
	expectNoUserApplicationPolicy(mock, userID, appID)
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})

	_, err := service.Resolve("client-sub", "eventiapp")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrApplicationAccessDenied)
	assert.Contains(t, err.Error(), "no eligible organization")
}

func TestEventAccessIgnoredForProductsWithoutEvents(t *testing.T) {
	for _, code := range []string{"cafettonhouse", "itbem"} {
		db, mock := applicationServiceTestDB(t)
		userID := uuid.Must(uuid.NewV4())
		appID := expectApplicationWithModules(mock, code, code == "itbem", `["home","organizations"]`)
		expectOrganizations(mock, appID, userID, emptyOrganizationRows())
		expectNoUserApplicationPolicy(mock, userID, appID)
		service := NewSessionService(db, func(string) (*models.User, error) {
			return &models.User{ID: userID, IsActive: true}, nil
		})
		called := false
		service.ConfigureEventClientAccess(func(uuid.UUID) (int, error) { called = true; return 3, nil })

		_, err := service.Resolve("client-sub", code)

		require.Error(t, err, code)
		assert.ErrorIs(t, err, ErrApplicationAccessDenied)
		assert.False(t, called, code)
	}
}

func TestEventAccessCountErrorFailsClosed(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplicationWithModules(mock, "eventiapp", true, eventiAppModules)
	expectOrganizations(mock, appID, userID, emptyOrganizationRows())
	expectNoUserApplicationPolicy(mock, userID, appID)
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})
	service.ConfigureEventClientAccess(func(uuid.UUID) (int, error) { return 0, errors.New("db down") })

	_, err := service.Resolve("client-sub", "eventiapp")

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrApplicationAccessDenied, "a dependency failure is a 503, not a deny")
}

func TestPlatformAdminNeverGetsEventAccess(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplicationWithModules(mock, "eventiapp", true, eventiAppModules)
	expectPlatformOrganizations(mock, emptyOrganizationRows())
	expectNoUserApplicationPolicy(mock, userID, appID)
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true, IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	})
	called := false
	service.ConfigureEventClientAccess(func(uuid.UUID) (int, error) { called = true; return 1, nil })

	session, err := service.Resolve("root-sub", "eventiapp")

	require.NoError(t, err)
	assert.Nil(t, session.EventAccess)
	assert.False(t, called)
}

func TestPolicyIntersectsEventClientCapabilities(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplicationWithModules(mock, "eventiapp", true, eventiAppModules)
	expectOrganizations(mock, appID, userID, emptyOrganizationRows())
	mock.ExpectQuery(`SELECT \* FROM "user_application_policies"`).WithArgs(userID, appID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "application_id", "capabilities", "is_active"}).
			AddRow(uuid.Must(uuid.NewV4()), userID, appID, `["session:view","events:view"]`, true))
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})
	service.ConfigureEventClientAccess(func(uuid.UUID) (int, error) { return 1, nil })

	session, err := service.Resolve("client-sub", "eventiapp")

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"session:view", "events:view"}, session.Capabilities)
}

func TestInactivePolicyDeniesEventClient(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplicationWithModules(mock, "eventiapp", true, eventiAppModules)
	expectOrganizations(mock, appID, userID, emptyOrganizationRows())
	mock.ExpectQuery(`SELECT \* FROM "user_application_policies"`).WithArgs(userID, appID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "application_id", "capabilities", "is_active"}).
			AddRow(uuid.Must(uuid.NewV4()), userID, appID, `["session:view"]`, false))
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})
	service.ConfigureEventClientAccess(func(uuid.UUID) (int, error) { return 1, nil })

	_, err := service.Resolve("client-sub", "eventiapp")

	assert.ErrorIs(t, err, ErrApplicationAccessDenied)
}

func TestInvalidateCognitoSubDropsCachedSession(t *testing.T) {
	service := NewSessionService(nil, nil)
	service.cache[applicationSessionCacheKey("eventiapp", "sub-1")] = cachedSession{session: &Session{}}
	service.cache[applicationSessionCacheKey("eventiapp", "sub-2")] = cachedSession{session: &Session{}}
	service.InvalidateCognitoSub("sub-1")
	assert.Len(t, service.cache, 1)
	var nilService *SessionService
	nilService.InvalidateCognitoSub("sub-1")
}
