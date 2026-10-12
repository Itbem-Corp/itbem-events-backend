package clientrepository

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestEvaluateApplicationAccessReasonsInOrder(t *testing.T) {
	entitled := applicationAccessRow{RoleCode: "EDITOR", TargetActive: true, TargetProduct: "eventiapp", ProductEnabled: true, MemberEntitled: true}
	access := evaluateApplicationAccess(entitled, "eventiapp")
	assert.Equal(t, ApplicationAccess{Allowed: true, Role: "EDITOR", Entitled: true}, access)

	cases := []struct {
		mutate func(*applicationAccessRow)
		reason string
	}{
		{func(row *applicationAccessRow) { row.TargetActive = false; row.TargetProduct = "itbem" }, "organization_inactive"},
		{func(row *applicationAccessRow) { row.TargetProduct = "cafettonhouse"; row.ProductEnabled = false }, "product_mismatch"},
		{func(row *applicationAccessRow) { row.TargetProduct = "" }, "product_mismatch"},
		{func(row *applicationAccessRow) { row.ProductEnabled = false; row.MemberEntitled = false }, "product_disabled"},
		{func(row *applicationAccessRow) { row.MemberEntitled = false }, "member_not_entitled"},
	}
	for _, tc := range cases {
		row := entitled
		tc.mutate(&row)
		access := evaluateApplicationAccess(row, "eventiapp")
		assert.True(t, access.Allowed, tc.reason)
		assert.False(t, access.Entitled, tc.reason)
		assert.Equal(t, tc.reason, access.Reason)
	}
}

func TestEvaluateApplicationAccessKeepsTheInheritanceRule(t *testing.T) {
	inherited := evaluateApplicationAccess(applicationAccessRow{RoleCode: "Owner", Level: 2, TargetActive: true, TargetProduct: "eventiapp", ProductEnabled: true, MemberEntitled: true}, "eventiapp")
	assert.Equal(t, ApplicationAccess{Allowed: true, Role: "INHERITED_OWNER", Entitled: true}, inherited)

	editorAbove := evaluateApplicationAccess(applicationAccessRow{RoleCode: "EDITOR", Level: 1, TargetActive: true, TargetProduct: "eventiapp", ProductEnabled: true, MemberEntitled: true}, "eventiapp")
	assert.Equal(t, ApplicationAccess{}, editorAbove)

	none := evaluateApplicationAccess(applicationAccessRow{}, "eventiapp")
	assert.Equal(t, ApplicationAccess{}, none)
}

func TestAccessQueriesAreDepthBounded(t *testing.T) {
	for _, query := range []string{organizationAccessSQL, applicationAccessSQL} {
		normalized := strings.Join(strings.Fields(query), " ")
		assert.Contains(t, normalized, "level < 31")
	}
}

// --- Opt-in Postgres harness (CLIENT_ACCESS_TEST_DSN). Every fixture lives
// in a transaction that always rolls back.

type accessFixture struct {
	t        *testing.T
	tx       *gorm.DB
	typeID   uuid.UUID
	roles    map[string]uuid.UUID
	userID   uuid.UUID
	eventi   uuid.UUID
	cafetton uuid.UUID
}

func newAccessFixture(t *testing.T) *accessFixture {
	dsn := os.Getenv("CLIENT_ACCESS_TEST_DSN")
	if dsn == "" {
		t.Skip("CLIENT_ACCESS_TEST_DSN is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })

	fixture := &accessFixture{t: t, tx: tx, roles: map[string]uuid.UUID{}}
	var eventiRoot, cafettonRoot models.Client
	if err := tx.Where("LOWER(code) = ?", "eventiapp").First(&eventiRoot).Error; err != nil {
		t.Skip("seeded EventiApp root is required")
	}
	if err := tx.Where("LOWER(code) = ?", "cafettonhouse").First(&cafettonRoot).Error; err != nil {
		t.Skip("seeded Cafetton root is required")
	}
	fixture.typeID, fixture.eventi, fixture.cafetton = eventiRoot.ClientTypeID, eventiRoot.ID, cafettonRoot.ID
	var roles []models.ClientRole
	require.NoError(t, tx.Find(&roles).Error)
	for _, role := range roles {
		fixture.roles[strings.ToUpper(role.Code)] = role.ID
	}
	fixture.userID = uuid.Must(uuid.NewV4())
	require.NoError(t, tx.Create(&models.User{ID: fixture.userID, CognitoSub: "qa-autotest-" + fixture.userID.String(), Email: "qa-autotest@example.invalid", IsActive: true}).Error)
	// The brand roots must be enabled for the positive cases.
	require.NoError(t, tx.Exec(`UPDATE client_applications SET is_active = true WHERE client_id IN (?, ?)`, fixture.eventi, fixture.cafetton).Error)
	return fixture
}

func (f *accessFixture) client(parent uuid.UUID, active bool) uuid.UUID {
	id := uuid.Must(uuid.NewV4())
	code := "qa-autotest-" + id.String()
	require.NoError(f.t, f.tx.Exec(
		`INSERT INTO clients (id, name, code, client_type_id, is_active, parent_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, NOW(), NOW())`,
		id, code, code, f.typeID, active, parent,
	).Error)
	return id
}

func (f *accessFixture) member(clientID uuid.UUID, role string) uuid.UUID {
	roleID, ok := f.roles[role]
	require.True(f.t, ok, "role %s is not seeded", role)
	member := models.ClientMember{ClientID: clientID, UserID: f.userID, ClientRoleID: roleID, IsActive: true}
	require.NoError(f.t, f.tx.Create(&member).Error)
	return member.ID
}

func (f *accessFixture) check(clientID uuid.UUID) ApplicationAccess {
	access, err := checkApplicationAccessRecursive(f.tx, f.userID, clientID, "EventiApp")
	require.NoError(f.t, err)
	allowed, role, err := checkAccessRecursiveE(f.tx, f.userID, clientID)
	require.NoError(f.t, err)
	assert.Equal(f.t, allowed, access.Allowed, "Allowed must follow CheckAccessRecursiveE")
	assert.Equal(f.t, role, access.Role, "Role must follow CheckAccessRecursiveE")
	return access
}

func TestApplicationAccessPostgresMatrix(t *testing.T) {
	f := newAccessFixture(t)

	org := f.client(f.eventi, true)
	memberID := f.member(org, "EDITOR")
	access := f.check(org)
	assert.Equal(t, ApplicationAccess{Allowed: true, Role: "EDITOR", Entitled: true}, access)

	require.NoError(t, f.tx.Exec(`UPDATE client_member_applications SET is_active = false WHERE client_member_id = ?`, memberID).Error)
	assert.Equal(t, "member_not_entitled", f.check(org).Reason)
	require.NoError(t, f.tx.Exec(`UPDATE client_member_applications SET is_active = true WHERE client_member_id = ?`, memberID).Error)

	require.NoError(t, f.tx.Exec(`UPDATE client_applications SET is_active = false WHERE client_id = ?`, f.eventi).Error)
	assert.Equal(t, "product_disabled", f.check(org).Reason)
	require.NoError(t, f.tx.Exec(`UPDATE client_applications SET is_active = true WHERE client_id = ?`, f.eventi).Error)

	inactive := f.client(f.eventi, false)
	f.member(inactive, "OWNER")
	access = f.check(inactive)
	assert.True(t, access.Allowed)
	assert.Equal(t, "organization_inactive", access.Reason)

	cafettonOrg := f.client(f.cafetton, true)
	f.member(cafettonOrg, "EDITOR")
	access = f.check(cafettonOrg)
	assert.True(t, access.Allowed)
	assert.Equal(t, "product_mismatch", access.Reason)

	parent := f.client(f.eventi, true)
	child := f.client(parent, true)
	f.member(parent, "OWNER")
	assert.Equal(t, ApplicationAccess{Allowed: true, Role: "INHERITED_OWNER", Entitled: true}, f.check(child))

	editorParent := f.client(f.eventi, true)
	editorChild := f.client(editorParent, true)
	f.member(editorParent, "EDITOR")
	assert.Equal(t, ApplicationAccess{}, f.check(editorChild))

	stranger := f.client(f.eventi, true)
	assert.Equal(t, ApplicationAccess{}, f.check(stranger))
}

func TestApplicationAccessPostgresTerminatesOnDeepAndCyclicTrees(t *testing.T) {
	f := newAccessFixture(t)

	top := f.client(f.eventi, true)
	f.member(top, "OWNER")
	current := top
	for level := 0; level < 40; level++ {
		current = f.client(current, true)
	}
	access := f.check(current)
	assert.False(t, access.Allowed, "a membership more than 31 levels up is outside the bounded walk")

	a := f.client(f.eventi, true)
	b := f.client(a, true)
	require.NoError(t, f.tx.Exec(`UPDATE clients SET parent_id = ? WHERE id = ?`, b, a).Error)
	access = f.check(b)
	assert.Equal(t, ApplicationAccess{}, access, fmt.Sprintf("cyclic tree must terminate: %+v", access))
}
