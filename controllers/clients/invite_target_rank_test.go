package clients

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/models"
	clientService "events-stocks/services/clients"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rankedClientRepo gives chosen accounts an effective role in the
// organization (CheckAccessRecursive) without a direct membership row there,
// the way an Owner or Admin of a parent organization reaches a child one
// (INHERITED_OWNER / INHERITED_ADMIN). Membership writes are still counted on
// the embedded outsiderClientRepo.
type rankedClientRepo struct {
	*outsiderClientRepo
	access map[uuid.UUID]string
}

func (r *rankedClientRepo) CheckAccessRecursive(userID, _ uuid.UUID) (bool, string) {
	code, ok := r.access[userID]
	return ok, code
}

// withEffectiveRoles rewires the clients service so the given accounts hold
// the given effective role codes in the fixture organization. The fixture's
// cleanup restores the original service.
func (f memberPathFixture) withEffectiveRoles(access map[uuid.UUID]string) {
	clientSvc = clientService.NewClientService(&rankedClientRepo{outsiderClientRepo: f.repo, access: access}, f.roles, &mockClientTypeRepo{}, nil, nil, nil)
}

func (f memberPathFixture) inviteEmail(email string, roleID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodPost, "/clients/invite", fmt.Sprintf(`{"client_id":%q,"email":%q,"role_id":%q}`, f.clientID, email, roleID))
}

var (
	operationalRootRequester       = models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}
	legacyOperationalRootRequester = models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelOperational}
	legacyPrimaryRootRequester     = models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}
	primaryRootRequester           = models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelPrimary}
	organizationManagerRequester   = models.User{ID: uuid.Must(uuid.NewV4())}
)

// inviteTargets are the two ways InviteUser names an existing account: by
// user_id, and by email (the dashboard's invite form).
var inviteTargets = []struct {
	name    string
	request func(f memberPathFixture, roleID uuid.UUID) (echo.Context, *httptest.ResponseRecorder)
}{
	{name: "by user_id", request: func(f memberPathFixture, roleID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
		return f.inviteUser(f.targetID, roleID)
	}},
	{name: "by email", request: func(f memberPathFixture, roleID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
		return f.inviteEmail("member@example.com", roleID)
	}},
}

// Adding an existing account to an organization writes a direct membership,
// and a direct membership takes precedence over access inherited from a
// parent organization. So InviteUser must apply the same target ranking as
// every other member write: otherwise a Root 2 or an organization Admin could
// "invite" a parent organization's Owner or Admin as Member and demote them
// in this organization and every organization below it, which the demoted
// account cannot undo.
func TestInviteRefusesExistingAccountsRankedAtOrAboveTheRequester(t *testing.T) {
	cases := []struct {
		name            string
		requester       models.User
		requesterAccess string
		targetAccess    string
	}{
		{name: "operational root on inherited Owner", requester: operationalRootRequester, targetAccess: "INHERITED_OWNER"},
		{name: "legacy operational root on inherited Owner", requester: legacyOperationalRootRequester, targetAccess: "INHERITED_OWNER"},
		{name: "operational root on direct Owner", requester: operationalRootRequester, targetAccess: "Owner"},
		{name: "Admin on inherited Admin", requester: organizationManagerRequester, requesterAccess: "Admin", targetAccess: "INHERITED_ADMIN"},
		{name: "Admin on inherited Owner", requester: organizationManagerRequester, requesterAccess: "Admin", targetAccess: "INHERITED_OWNER"},
		{name: "inherited Admin on inherited Admin", requester: organizationManagerRequester, requesterAccess: "INHERITED_ADMIN", targetAccess: "INHERITED_ADMIN"},
		{name: "Owner on inherited Owner", requester: organizationManagerRequester, requesterAccess: "Owner", targetAccess: "INHERITED_OWNER"},
	}
	roles := []roleChoice{
		{name: "Member", role: func(f memberPathFixture) uuid.UUID { return f.memberRole }},
		{name: "Check-in", role: func(f memberPathFixture) uuid.UUID { return f.checkinRole }},
	}
	for _, tc := range cases {
		for _, role := range roles {
			for _, target := range inviteTargets {
				t.Run(tc.name+" as "+role.name+" "+target.name, func(t *testing.T) {
					fixture := newMemberPathFixture(t, tc.requester)
					access := map[uuid.UUID]string{fixture.targetID: tc.targetAccess}
					if tc.requesterAccess != "" {
						access[tc.requester.ID] = tc.requesterAccess
					}
					fixture.withEffectiveRoles(access)
					c, rec := target.request(fixture, role.role(fixture))

					require.NoError(t, InviteUser(c))

					assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
					assert.Contains(t, rec.Body.String(), "Permission Denied")
					assert.Zero(t, fixture.repo.writes, "no membership may be written")
				})
			}
		}
	}
}

// The ranking only refuses targets at or above the requester: a primary root
// still adds anyone, a Root 2 still adds anyone below Owner, and an
// organization manager still adds accounts that have no access there yet.
func TestInviteStillAddsExistingAccountsTheRequesterOutranks(t *testing.T) {
	cases := []struct {
		name            string
		requester       models.User
		requesterAccess string
		targetAccess    string
	}{
		{name: "legacy primary root on inherited Owner", requester: legacyPrimaryRootRequester, targetAccess: "INHERITED_OWNER"},
		{name: "primary root on inherited Owner", requester: primaryRootRequester, targetAccess: "INHERITED_OWNER"},
		{name: "operational root on inherited Admin", requester: operationalRootRequester, targetAccess: "INHERITED_ADMIN"},
		{name: "Admin on an account without access", requester: organizationManagerRequester, requesterAccess: "Admin"},
		{name: "inherited Owner on an account without access", requester: organizationManagerRequester, requesterAccess: "INHERITED_OWNER"},
	}
	for _, tc := range cases {
		for _, target := range inviteTargets {
			t.Run(tc.name+" "+target.name, func(t *testing.T) {
				fixture := newMemberPathFixture(t, tc.requester)
				access := map[uuid.UUID]string{}
				if tc.targetAccess != "" {
					access[fixture.targetID] = tc.targetAccess
				}
				if tc.requesterAccess != "" {
					access[tc.requester.ID] = tc.requesterAccess
				}
				fixture.withEffectiveRoles(access)
				c, rec := target.request(fixture, fixture.memberRole)

				require.NoError(t, InviteUser(c))

				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, 1, fixture.repo.writes)
			})
		}
	}
}

// An organization manager cannot use the invite to replace its own inherited
// access with a lower direct role.
func TestInviteRefusesTheRequestersOwnAccount(t *testing.T) {
	requester := organizationManagerRequester
	fixture := newMemberPathFixture(t, requester)
	self := requester
	self.Email = "manager@example.com"
	fixture.directory.users[requester.ID] = &self
	fixture.withEffectiveRoles(map[uuid.UUID]string{requester.ID: "INHERITED_ADMIN"})
	c, rec := fixture.inviteUser(requester.ID, fixture.memberRole)

	require.NoError(t, InviteUser(c))

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Zero(t, fixture.repo.writes, "no membership may be written")
}

// Only a primary root acts on platform-root accounts, so an organization
// Owner cannot add one to its organization either.
func TestOrganizationManagerCannotAddPlatformRootAccounts(t *testing.T) {
	targets := []struct {
		name   string
		target func(memberPathFixture) uuid.UUID
	}{
		{name: "primary root", target: func(f memberPathFixture) uuid.UUID { return f.primaryRootID }},
		{name: "operational root", target: func(f memberPathFixture) uuid.UUID { return f.operationalRootID }},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			fixture := newMemberPathFixture(t, organizationManagerRequester)
			fixture.withEffectiveRoles(map[uuid.UUID]string{organizationManagerRequester.ID: "Owner"})
			c, rec := fixture.inviteUser(target.target(fixture), fixture.memberRole)

			require.NoError(t, InviteUser(c))

			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Zero(t, fixture.repo.writes, "no membership may be written")
		})
	}
}
