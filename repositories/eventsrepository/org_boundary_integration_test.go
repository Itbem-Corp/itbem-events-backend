//go:build integration

package eventsrepository

// Real-PostgreSQL tests for the organization product boundary (backend
// review M6, security review #2), the timezone repair (M4) and the moment
// dedupe order (M7). They share the disposable container of
// postgres_integration_test.go; nothing touches the local stack database.

import (
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/clientrepository"
	"events-stocks/services/applications"
)

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
		clientType = models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "QA", Code: "QA-boundary-" + lifecycleSuffix()}
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
	suffix := lifecycleSuffix()
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

// Review M4: the boot repair blanks only zones PostgreSQL rejects; values it
// accepts (lower-case IANA, POSIX) are kept, and a second boot is a no-op.
func TestStartupTimezoneRepairKeepsZonesPostgresAccepts(t *testing.T) {
	suffix := lifecycleSuffix()
	org := createLifecycleClient(t, suffix, "TZK")
	bad := createLifecycleEvent(t, &org, "Mala "+suffix, "tz-mala-"+suffix)
	lower := createLifecycleEvent(t, &org, "Minúsculas "+suffix, "tz-lower-"+suffix)
	posix := createLifecycleEvent(t, &org, "Posix "+suffix, "tz-posix-"+suffix)
	require.NoError(t, configuration.DB.Exec(`UPDATE events SET timezone = 'Mars/Olympus' WHERE id = ?`, bad.ID).Error)
	require.NoError(t, configuration.DB.Exec(`UPDATE events SET timezone = 'america/mexico_city' WHERE id = ?`, lower.ID).Error)
	require.NoError(t, configuration.DB.Exec(`UPDATE events SET timezone = 'UTC+5' WHERE id = ?`, posix.ID).Error)

	require.NoError(t, configuration.MigrateModelsForTest(configuration.DB))
	require.NoError(t, configuration.MigrateModelsForTest(configuration.DB), "idempotent")

	zone := func(id uuid.UUID) string {
		var value string
		require.NoError(t, configuration.DB.Raw(`SELECT timezone FROM events WHERE id = ?`, id).Row().Scan(&value))
		return value
	}
	assert.Equal(t, "", zone(bad.ID))
	assert.Equal(t, "america/mexico_city", zone(lower.ID))
	assert.Equal(t, "UTC+5", zone(posix.ID))
}

// Review M7: the dedupe keeps the approved copy even when another copy has a
// legacy NULL is_approved (plain DESC sorts NULL first).
func TestMomentDedupeKeepsApprovedCopyOverLegacyNull(t *testing.T) {
	suffix := lifecycleSuffix()
	org := createLifecycleClient(t, suffix, "MOMD")
	event := createLifecycleEvent(t, &org, "Momentos "+suffix, "momentos-"+suffix)
	db := configuration.DB
	t.Cleanup(func() { db.Unscoped().Where("event_id = ?", event.ID).Delete(&models.Moment{}) })
	require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+models.MomentLiveContentURLIndex).Error)

	key := "moments/" + event.ID.String() + "/raw/" + uuid.Must(uuid.NewV4()).String() + ".jpg"
	legacy := models.Moment{ID: uuid.Must(uuid.NewV4()), EventID: &event.ID, ContentURL: key, ProcessingStatus: "done", CreatedAt: time.Now().Add(-2 * time.Hour)}
	approved := models.Moment{ID: uuid.Must(uuid.NewV4()), EventID: &event.ID, ContentURL: key, ProcessingStatus: "done", IsApproved: true}
	for _, moment := range []*models.Moment{&legacy, &approved} {
		require.NoError(t, db.Omit("Invitation", "MomentType", "Guest").Create(moment).Error)
	}
	require.NoError(t, db.Exec(`ALTER TABLE moments ALTER COLUMN is_approved DROP NOT NULL`).Error)
	require.NoError(t, db.Exec(`UPDATE moments SET is_approved = NULL WHERE id = ?`, legacy.ID).Error)

	require.NoError(t, configuration.MigrateModelsForTest(db))

	var live []models.Moment
	require.NoError(t, db.Where("event_id = ? AND content_url = ?", event.ID, key).Find(&live).Error)
	require.Len(t, live, 1)
	assert.Equal(t, approved.ID, live[0].ID, "the approved copy must survive")
}
