package applications

import (
	"errors"
	"events-stocks/models"
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

func applicationServiceTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
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

func expectApplication(mock sqlmock.Sqlmock, code string, allowsPlatformAdmin bool) uuid.UUID {
	id := uuid.Must(uuid.NewV4())
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "applications"`)).
		WithArgs(code, 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "code", "name", "product_label", "modules",
			"allows_platform_admin", "is_active", "created_at", "updated_at",
		}).AddRow(id, code, "Application", "Product", `["home","organizations"]`, allowsPlatformAdmin, true, now, now))
	return id
}

func expectOrganizations(mock sqlmock.Sqlmock, applicationID, userID uuid.UUID, rows *sqlmock.Rows) {
	mock.ExpectQuery(`SELECT DISTINCT\s+clients.id`).
		WithArgs(applicationID, userID).
		WillReturnRows(rows)
}

func TestResolveAllowsPlatformAdminOnlyWhenApplicationEnablesIt(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplication(mock, "itbem", true)
	expectOrganizations(mock, appID, userID, sqlmock.NewRows([]string{"id", "name", "code", "logo", "access_role"}))
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsRoot: true, RootLevel: models.RootLevelPrimary, IsActive: true}, nil
	})

	session, err := service.Resolve("root-sub", "ITBEM")

	require.NoError(t, err)
	assert.Equal(t, "itbem", session.Application.Code)
	assert.Contains(t, session.Capabilities, "organizations:manage")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveDeniesIdentityWithoutApplicationMembership(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplication(mock, "cafettonhouse", false)
	expectOrganizations(mock, appID, userID, sqlmock.NewRows([]string{"id", "name", "code", "logo", "access_role"}))
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})

	_, err := service.Resolve("member-sub", "cafettonhouse")

	assert.True(t, errors.Is(err, ErrApplicationAccessDenied))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveReturnsExplicitOrganizationAccessAndCachesSession(t *testing.T) {
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	appID := expectApplication(mock, "cafettonhouse", false)
	expectOrganizations(mock, appID, userID, sqlmock.NewRows([]string{"id", "name", "code", "logo", "access_role"}).
		AddRow(clientID, "Cafetton House", "cafettonhouse", "", "Owner"))
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})

	first, err := service.Resolve("member-sub", "cafettonhouse")
	require.NoError(t, err)
	second, err := service.Resolve("member-sub", "cafettonhouse")
	require.NoError(t, err)

	require.Len(t, first.Organizations, 1)
	assert.Equal(t, clientID, first.Organizations[0].ID)
	assert.Same(t, first, second)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEffectiveApplicationCapabilitiesEnforcesRootCeilings(t *testing.T) {
	application := models.Application{
		Modules:             models.StringList{"home", "events", "users", "organizations"},
		AllowsPlatformAdmin: true,
	}
	tests := []struct {
		name       string
		user       *models.User
		want       []string
		notAllowed []string
	}{
		{
			name: "primary root governs platform and product",
			user: &models.User{RootLevel: models.RootLevelPrimary},
			want: []string{
				"audit:view",
				"events:create", "events:manage", "events:delete",
				"platform:users:manage", "platform:users:root-manage",
				"organizations:manage", "applications:manage", "members:manage",
			},
		},
		{
			name: "operational root supports but cannot govern",
			user: &models.User{RootLevel: models.RootLevelOperational},
			want: []string{
				"events:view", "guests:manage", "checkin:run", "analytics:view",
				"platform:users:view", "platform:users:support", "organizations:view", "members:manage",
			},
			notAllowed: []string{
				"audit:view",
				"events:create", "events:manage", "events:delete",
				"platform:users:manage", "platform:users:root-manage",
				"organizations:manage", "applications:manage",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := effectiveApplicationCapabilities(application, test.user, nil)
			for _, capability := range test.want {
				assert.Contains(t, got, capability)
			}
			for _, capability := range test.notAllowed {
				assert.NotContains(t, got, capability)
			}
		})
	}
}

func TestOrganizationCapabilitiesMatchRoleBoundaries(t *testing.T) {
	tests := []struct {
		role       string
		want       []string
		notAllowed []string
	}{
		{role: "Owner", want: []string{"organizations:manage", "members:manage", "events:create", "events:manage", "events:delete"}},
		{role: "Admin", want: []string{"organizations:manage", "members:manage", "events:create", "events:manage", "events:delete"}},
		{role: "EVENT_MANAGER", want: []string{"events:create", "events:manage", "guests:manage"}, notAllowed: []string{"events:delete", "members:manage"}},
		{role: "EDITOR", want: []string{"events:manage", "guests:manage", "analytics:view"}, notAllowed: []string{"events:create", "events:delete", "checkin:run"}},
		{role: "MEMBER", want: []string{"events:view", "guests:manage"}, notAllowed: []string{"events:create", "events:manage", "events:delete", "checkin:run", "analytics:view"}},
		{role: "CHECKIN", want: []string{"events:view", "checkin:run"}, notAllowed: []string{"events:manage", "guests:manage"}},
		{role: "ANALYST", want: []string{"events:view", "analytics:view"}, notAllowed: []string{"events:manage", "checkin:run"}},
		{role: "Guest", want: []string{"events:view"}, notAllowed: []string{"events:manage", "analytics:view"}},
		{role: "Viewer", want: []string{"events:view"}, notAllowed: []string{"events:manage", "analytics:view"}},
	}
	for _, test := range tests {
		t.Run(test.role, func(t *testing.T) {
			got := organizationCapabilities(test.role)
			for _, capability := range test.want {
				assert.Contains(t, got, capability)
			}
			for _, capability := range test.notAllowed {
				assert.NotContains(t, got, capability)
			}
		})
	}
}

func TestCustomerPortalNeverInheritsPlatformRootAuthority(t *testing.T) {
	application := models.Application{
		Modules:             models.StringList{"home", "events", "users"},
		AllowsPlatformAdmin: false,
	}
	organizations := []OrganizationAccess{{
		AccessRole:   "ANALYST",
		Capabilities: organizationCapabilities("ANALYST"),
	}}

	got := effectiveApplicationCapabilities(
		application,
		&models.User{RootLevel: models.RootLevelPrimary},
		organizations,
	)

	assert.Contains(t, got, "analytics:view")
	assert.NotContains(t, got, "events:manage")
	assert.NotContains(t, got, "platform:users:view")
	assert.NotContains(t, got, "organizations:manage")
}

func TestOrganizationCapabilitiesRespectProductModules(t *testing.T) {
	cafetton := models.Application{Modules: models.StringList{"home", "events", "users"}}
	itbem := models.Application{Modules: models.StringList{"home", "users", "organizations"}}

	cafettonOwner := applicationOrganizationCapabilities(cafetton, "OWNER")
	assert.Contains(t, cafettonOwner, "members:manage")
	assert.Contains(t, cafettonOwner, "events:manage")
	assert.NotContains(t, cafettonOwner, "organizations:manage")

	itbemOwner := applicationOrganizationCapabilities(itbem, "OWNER")
	assert.Contains(t, itbemOwner, "members:manage")
	assert.Contains(t, itbemOwner, "organizations:manage")
	assert.NotContains(t, itbemOwner, "events:view")
	assert.NotContains(t, itbemOwner, "events:manage")
}

func TestEventProductRootsCanManageTeamsOnlyWithinAuthorizedRequests(t *testing.T) {
	eventiapp := models.Application{
		Modules:             models.StringList{"home", "events", "metrics"},
		AllowsPlatformAdmin: true,
	}

	primary := effectiveApplicationCapabilities(
		eventiapp,
		&models.User{RootLevel: models.RootLevelPrimary},
		nil,
	)
	operational := effectiveApplicationCapabilities(
		eventiapp,
		&models.User{RootLevel: models.RootLevelOperational},
		nil,
	)

	assert.Contains(t, primary, "members:manage")
	assert.Contains(t, operational, "members:manage")
	assert.NotContains(t, operational, "events:manage")
	assert.NotContains(t, operational, "events:create")
	assert.NotContains(t, operational, "events:delete")
	assert.NotContains(t, primary, "organizations:manage")
}
