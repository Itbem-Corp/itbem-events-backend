//go:build integration

package eventsrepository

// Real-PostgreSQL tests for the organization product boundary (backend
// review M6, security review #2). They use the disposable container of
// postgres_integration_test.go when that suite runs (its TestMain sets
// configuration.DB), else start their own (boundaryDB). Nothing touches the
// local stack database; without Docker the test is skipped.

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/clientrepository"
	"events-stocks/services/applications"
)

func boundarySuffix() string {
	return uuid.Must(uuid.NewV4()).String()[:8]
}

// boundaryDB keeps the shared suite database when another integration suite
// of this package already set configuration.DB, and otherwise starts a
// disposable PostgreSQL 16 container for this test and migrates it.
func boundaryDB(t *testing.T) {
	t.Helper()
	if configuration.DB != nil {
		return
	}
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("Docker not available; organization boundary PostgreSQL test not run")
	}
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(cleanupCtx)
	})
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	require.NoError(t, configuration.MigrateModelsForTest(db))
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = nil })
}

// The migration does not run the seeds, so the product catalog rows the
// boundary depends on are created here when missing (idempotent).
func ensureClientWithCode(t *testing.T, code string, parentID *uuid.UUID) uuid.UUID {
	t.Helper()
	var client models.Client
	if err := configuration.DB.Where("LOWER(code) = ?", code).First(&client).Error; err == nil {
		return client.ID
	}
	var clientType models.ClientType
	if err := configuration.DB.First(&clientType).Error; err != nil {
		clientType = models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "QA", Code: "QA-boundary-" + boundarySuffix()}
		require.NoError(t, configuration.DB.Create(&clientType).Error)
	}
	client = models.Client{ID: uuid.Must(uuid.NewV4()), Name: code, Code: code, ClientTypeID: clientType.ID, ParentID: parentID, IsActive: true}
	require.NoError(t, configuration.DB.Omit("ClientType", "Parent").Create(&client).Error)
	return client.ID
}

func clientIDByCode(t *testing.T, code string) uuid.UUID {
	t.Helper()
	return ensureClientWithCode(t, code, nil)
}

func roleIDByCode(t *testing.T, code string) uuid.UUID {
	t.Helper()
	var role models.ClientRole
	if err := configuration.DB.Where("code = ?", code).First(&role).Error; err == nil {
		return role.ID
	}
	hierarchy := map[string]int{"Owner": 1, "Admin": 2, "Member": 7}[code]
	role = models.ClientRole{ID: uuid.Must(uuid.NewV4()), Name: code, Code: code, Hierarchy: hierarchy, IsActive: true}
	require.NoError(t, configuration.DB.Create(&role).Error)
	return role.ID
}

func applicationIDByCode(t *testing.T, code string) uuid.UUID {
	t.Helper()
	var application models.Application
	if err := configuration.DB.Where("LOWER(code) = ?", code).First(&application).Error; err == nil {
		return application.ID
	}
	application = models.Application{ID: uuid.Must(uuid.NewV4()), Code: code, Name: code, IsActive: true, AllowsPlatformAdmin: code != "cafettonhouse"}
	require.NoError(t, configuration.DB.Create(&application).Error)
	return application.ID
}

func createBoundaryClient(t *testing.T, parentID *uuid.UUID, label, suffix string, active bool) uuid.UUID {
	t.Helper()
	var clientType models.ClientType
	require.NoError(t, configuration.DB.First(&clientType).Error)
	client := models.Client{
		ID: uuid.Must(uuid.NewV4()), Name: "QA " + label + " " + suffix, Code: "qa-" + label + "-" + suffix,
		ClientTypeID: clientType.ID, ParentID: parentID, IsActive: true,
	}
	require.NoError(t, configuration.DB.Omit("ClientType", "Parent").Create(&client).Error)
	if !active {
		// is_active has a GORM default of true, so false must be written explicitly.
		require.NoError(t, configuration.DB.Model(&models.Client{}).Where("id = ?", client.ID).Update("is_active", false).Error)
	}
	return client.ID
}

func createBoundaryUser(t *testing.T, suffix, label string) *models.User {
	t.Helper()
	user := models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "qa-" + label + "-" + suffix, Email: "qa-" + label + "-" + suffix + "@example.test", IsActive: true}
	require.NoError(t, configuration.DB.Create(&user).Error)
	return &user
}

func addBoundaryMembership(t *testing.T, userID, clientID, roleID, applicationID uuid.UUID) {
	t.Helper()
	member := models.ClientMember{ID: uuid.Must(uuid.NewV4()), ClientID: clientID, UserID: userID, ClientRoleID: roleID, IsActive: true}
	require.NoError(t, configuration.DB.Omit("Client", "User", "ClientRole").Create(&member).Error)
	access := models.ClientMemberApplication{ID: uuid.Must(uuid.NewV4()), ClientMemberID: member.ID, ApplicationID: applicationID, IsActive: true}
	// A membership hook may already have enabled the application: keep it active.
	require.NoError(t, configuration.DB.Omit("ClientMember", "Application").
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client_member_id"}, {Name: "application_id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{"is_active": true}),
		}).Create(&access).Error)
}

func sessionOrganizationIDs(t *testing.T, user *models.User, product string) map[uuid.UUID]string {
	t.Helper()
	service := applications.NewSessionService(configuration.DB, func(string) (*models.User, error) { return user, nil })
	session, err := service.Resolve(user.CognitoSub, product)
	require.NoError(t, err)
	out := map[uuid.UUID]string{}
	for _, organization := range session.Organizations {
		out[organization.ID] = organization.AccessRole
	}
	return out
}

// An Owner of the EventiApp platform organization (parent of the ITBEM and
// Cafetton House portals) gets the EventiApp descendants as workspaces, never
// another product's portal or its subtree, and inheritance stops at inactive
// or deleted organizations.
func TestSessionInheritanceStaysInsideTheApplicationOnPostgres(t *testing.T) {
	boundaryDB(t)
	suffix := boundarySuffix()
	root := clientIDByCode(t, "eventiapp")
	itbemPortal := ensureClientWithCode(t, "itbem", &root)
	cafettonPortal := ensureClientWithCode(t, "cafettonhouse", &root)
	applicationIDByCode(t, "itbem")
	applicationIDByCode(t, "cafettonhouse")
	owner := roleIDByCode(t, "Owner")
	member := roleIDByCode(t, "Member")
	eventiApp := applicationIDByCode(t, "eventiapp")

	childActive := createBoundaryClient(t, &root, "child-active", suffix, true)
	childInactive := createBoundaryClient(t, &root, "child-inactive", suffix, false)
	underInactive := createBoundaryClient(t, &childInactive, "under-inactive", suffix, true)
	childDeleted := createBoundaryClient(t, &root, "child-deleted", suffix, true)
	underDeleted := createBoundaryClient(t, &childDeleted, "under-deleted", suffix, true)
	require.NoError(t, configuration.DB.Delete(&models.Client{}, "id = ?", childDeleted).Error)
	itbemCustomer := createBoundaryClient(t, &itbemPortal, "itbem-customer", suffix, true)
	cafettonCustomer := createBoundaryClient(t, &cafettonPortal, "cafetton-customer", suffix, true)

	platformOwner := createBoundaryUser(t, suffix, "platform-owner")
	addBoundaryMembership(t, platformOwner.ID, root, owner, eventiApp)

	orgs := sessionOrganizationIDs(t, platformOwner, "eventiapp")
	assert.Contains(t, orgs, root)
	assert.Equal(t, "INHERITED_Owner", orgs[childActive], "in-product inheritance still works")
	for name, id := range map[string]uuid.UUID{
		"itbem portal": itbemPortal, "cafetton portal": cafettonPortal,
		"itbem customer": itbemCustomer, "cafetton customer": cafettonCustomer,
		"inactive child": childInactive, "child of an inactive org": underInactive,
		"deleted child": childDeleted, "child of a deleted org": underDeleted,
	} {
		assert.NotContains(t, orgs, id, name)
	}

	// The same boundary on organization checks by id (authz / client service).
	reach, err := clientrepository.ReachableClientIDsInApplication(platformOwner.ID, "eventiapp",
		[]uuid.UUID{childActive, itbemPortal, cafettonPortal, itbemCustomer, cafettonCustomer})
	require.NoError(t, err)
	assert.True(t, reach[childActive])
	assert.False(t, reach[itbemPortal])
	assert.False(t, reach[cafettonPortal])
	assert.False(t, reach[itbemCustomer])
	assert.False(t, reach[cafettonCustomer])

	// A deactivated direct organization hands nothing down either.
	inactiveSeed := createBoundaryClient(t, &root, "inactive-seed", suffix, false)
	underSeed := createBoundaryClient(t, &inactiveSeed, "under-seed", suffix, true)
	plain := createBoundaryClient(t, &root, "plain", suffix, true)
	seedOwner := createBoundaryUser(t, suffix, "seed-owner")
	addBoundaryMembership(t, seedOwner.ID, inactiveSeed, owner, eventiApp)
	addBoundaryMembership(t, seedOwner.ID, plain, member, eventiApp)
	seedOrgs := sessionOrganizationIDs(t, seedOwner, "eventiapp")
	assert.Contains(t, seedOrgs, plain)
	assert.NotContains(t, seedOrgs, inactiveSeed)
	assert.NotContains(t, seedOrgs, underSeed)
}
