package eventclients

import (
	"errors"
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/eventclientrepository"

	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakeStore struct {
	liveUsers      []eventclientrepository.LiveUser
	orgClients     map[uuid.UUID]bool
	memberships    map[uuid.UUID]bool
	owners         map[uuid.UUID]*uuid.UUID
	grantErr       error
	grants         []uuid.UUID // event ids granted in order
	revokes        []uuid.UUID
	revokeResult   bool
	revokeAllCount int64
	created        bool
	transactions   int
}

func (f *fakeStore) Transaction(fn func(tx *gorm.DB) error) error {
	f.transactions++
	return fn(nil)
}
func (f *fakeStore) ListActiveForEvent(uuid.UUID) ([]eventclientrepository.EventClientRow, error) {
	return nil, nil
}
func (f *fakeStore) ActiveGrantForEvent(eventID, userID uuid.UUID) (*eventclientrepository.EventClientRow, error) {
	for _, id := range f.grants {
		if id == eventID {
			return &eventclientrepository.EventClientRow{UserID: userID}, nil
		}
	}
	return nil, nil
}
func (f *fakeStore) ListOrganizationClients(uuid.UUID, uuid.UUID, string, int) ([]eventclientrepository.CandidateRow, error) {
	return nil, nil
}
func (f *fakeStore) IsOrganizationClient(_ uuid.UUID, userID uuid.UUID) (bool, error) {
	return f.orgClients[userID], nil
}
func (f *fakeStore) ListOrganizationEventsForClient(uuid.UUID, uuid.UUID, string, int, int) ([]eventclientrepository.AssignmentRow, int, error) {
	return nil, 0, nil
}
func (f *fakeStore) EventOrganizations(ids []uuid.UUID) (map[uuid.UUID]*uuid.UUID, error) {
	result := map[uuid.UUID]*uuid.UUID{}
	for _, id := range ids {
		if owner, ok := f.owners[id]; ok {
			result[id] = owner
		}
	}
	return result, nil
}
func (f *fakeStore) FindLiveUsersByEmail(string, int) ([]eventclientrepository.LiveUser, error) {
	return f.liveUsers, nil
}
func (f *fakeStore) HasMembershipInOrganizationTree(_ uuid.UUID, userID uuid.UUID) (bool, error) {
	return f.memberships[userID], nil
}
func (f *fakeStore) Grant(_ *gorm.DB, eventID, _, _, _ uuid.UUID) (bool, *models.EventClientGrant, error) {
	if f.grantErr != nil {
		return false, nil, f.grantErr
	}
	f.grants = append(f.grants, eventID)
	return f.created, &models.EventClientGrant{EventID: eventID}, nil
}
func (f *fakeStore) Revoke(_ *gorm.DB, eventID, _, _ uuid.UUID) (bool, error) {
	f.revokes = append(f.revokes, eventID)
	return f.revokeResult, nil
}
func (f *fakeStore) RevokeAllForOrganizationUser(*gorm.DB, uuid.UUID, uuid.UUID, uuid.UUID) (int64, error) {
	return f.revokeAllCount, nil
}
func (f *fakeStore) ListActiveForOrganization(uuid.UUID, int, int) ([]eventclientrepository.OrganizationGrantRow, int, error) {
	return nil, 0, nil
}
func (f *fakeStore) CountActiveForUserInOrganization(uuid.UUID, uuid.UUID) (int, error) {
	return len(f.grants), nil
}

type fakeIdentity struct {
	users       map[uuid.UUID]*models.User
	auth        map[string]*dtos.AuthUser
	inviteErr   error
	invited     *models.User
	inviteCalls int
	tenant      string
	synced      string
	deleted     []string
	resent      []string
}

func (f *fakeIdentity) InviteUserForTenant(email, first, last, tenant string) (*models.User, error) {
	f.inviteCalls++
	f.tenant = tenant
	if f.inviteErr != nil {
		return nil, f.inviteErr
	}
	f.invited = &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "new-sub", Email: email, IsActive: true}
	return f.invited, nil
}
func (f *fakeIdentity) SyncUser(sub string) (*models.User, error) {
	f.synced = sub
	user := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: sub, IsActive: true}
	f.users[user.ID] = user
	return user, nil
}
func (f *fakeIdentity) DeleteFullAccount(sub string) error {
	f.deleted = append(f.deleted, sub)
	return nil
}
func (f *fakeIdentity) GetUserByID(id uuid.UUID) (*models.User, error) {
	if user, ok := f.users[id]; ok {
		return user, nil
	}
	if f.invited != nil && f.invited.ID == id {
		return f.invited, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeIdentity) GetAuthUser(username string) (*dtos.AuthUser, error) {
	if user, ok := f.auth[username]; ok {
		return user, nil
	}
	return nil, errors.New("UserNotFoundException")
}
func (f *fakeIdentity) ResendInvitation(username string) error {
	f.resent = append(f.resent, username)
	return nil
}

type fakeOrganizations struct {
	product string
	access  map[uuid.UUID]bool
}

func (f *fakeOrganizations) CheckAccessRecursiveE(userID, _ uuid.UUID) (bool, string, error) {
	return f.access[userID], "Owner", nil
}
func (f *fakeOrganizations) ResolveClientProduct(uuid.UUID) (string, error) { return f.product, nil }

type harness struct {
	store       *fakeStore
	identity    *fakeIdentity
	orgs        *fakeOrganizations
	service     *Service
	event       *models.Event
	actor       *models.User
	invalidated []string
	manageable  map[uuid.UUID]bool
}

func newHarness() *harness {
	orgID := uuid.Must(uuid.NewV4())
	h := &harness{
		store:      &fakeStore{orgClients: map[uuid.UUID]bool{}, memberships: map[uuid.UUID]bool{}, owners: map[uuid.UUID]*uuid.UUID{}, created: true, revokeResult: true},
		identity:   &fakeIdentity{users: map[uuid.UUID]*models.User{}, auth: map[string]*dtos.AuthUser{}},
		orgs:       &fakeOrganizations{product: "eventiapp", access: map[uuid.UUID]bool{}},
		event:      &models.Event{ID: uuid.Must(uuid.NewV4()), ClientID: &orgID},
		actor:      &models.User{ID: uuid.Must(uuid.NewV4()), IsActive: true},
		manageable: map[uuid.UUID]bool{},
	}
	h.service = NewService(h.store, h.identity, h.orgs,
		func(_ *models.User, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
			result := map[uuid.UUID]bool{}
			for _, id := range ids {
				allowed, ok := h.manageable[id]
				result[id] = !ok || allowed
			}
			return result, nil
		},
		func(sub string) { h.invalidated = append(h.invalidated, sub) })
	return h
}

func (h *harness) request() Request {
	return Request{Actor: h.actor, RequestID: "req-1", TenantCode: "eventiapp"}
}

func (h *harness) existingUser(email string, verified bool) *models.User {
	user := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "sub-" + email, Email: email, IsActive: true}
	h.identity.users[user.ID] = user
	h.store.liveUsers = append(h.store.liveUsers, eventclientrepository.LiveUser{ID: user.ID, CognitoSub: user.CognitoSub, IsActive: true})
	h.identity.auth[user.CognitoSub] = &dtos.AuthUser{Sub: user.CognitoSub, Email: email, IsActive: true, EmailVerified: verified}
	return user
}

func ptr(value string) *string { return &value }

func emailBody(email string) dtos.EventClientCreateRequest {
	return dtos.EventClientCreateRequest{Email: ptr(email), FirstName: ptr("Ana"), LastName: ptr("López")}
}

func TestCreateInvitesNewAccountWithTenant(t *testing.T) {
	h := newHarness()
	response, created, err := h.service.Create(h.request(), h.event, emailBody(" Ana@Ejemplo.mx "))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, AccountCreated, response.Account)
	assert.Equal(t, "eventiapp", h.identity.tenant)
	assert.Equal(t, []string{"new-sub"}, h.invalidated)
}

func TestCreateRollsBackFreshAccountWhenGrantFails(t *testing.T) {
	h := newHarness()
	h.store.grantErr = errors.New("db down")
	_, _, err := h.service.Create(h.request(), h.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrGrantFailed, err)
	assert.Equal(t, []string{"new-sub"}, h.identity.deleted)
}

func TestCreateExistingVerifiedAccount(t *testing.T) {
	h := newHarness()
	h.existingUser("ana@ejemplo.mx", true)
	h.store.created = false
	response, created, err := h.service.Create(h.request(), h.event, emailBody("ana@ejemplo.mx"))
	require.NoError(t, err)
	assert.False(t, created)
	assert.True(t, response.AlreadyAssigned)
	assert.Equal(t, AccountExisting, response.Account)
	assert.Zero(t, h.identity.inviteCalls)
}

func TestCreateUnverifiedOrChangedEmailIsUnavailable(t *testing.T) {
	h := newHarness()
	h.existingUser("ana@ejemplo.mx", false)
	_, _, err := h.service.Create(h.request(), h.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrUnavailable, err)
	assert.Empty(t, h.store.grants)

	g := newHarness()
	user := g.existingUser("ana@ejemplo.mx", true)
	g.identity.auth[user.CognitoSub].Email = "otra@ejemplo.mx"
	_, _, err = g.service.Create(g.request(), g.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrUnavailable, err)
}

func TestCreateAmbiguousEmailIsUnavailable(t *testing.T) {
	h := newHarness()
	h.existingUser("ana@ejemplo.mx", true)
	h.existingUser("ana@ejemplo.mx", true)
	_, _, err := h.service.Create(h.request(), h.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrUnavailable, err)
}

func TestCreateUsernameExistsResolvesCognitoAccountAndSyncs(t *testing.T) {
	h := newHarness()
	h.identity.inviteErr = &types.UsernameExistsException{}
	h.identity.auth["ana@ejemplo.mx"] = &dtos.AuthUser{Sub: "ana@ejemplo.mx", Subject: "real-sub", Email: "ana@ejemplo.mx", IsActive: true, EmailVerified: true}
	response, created, err := h.service.Create(h.request(), h.event, emailBody("ana@ejemplo.mx"))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, AccountExisting, response.Account)
	assert.Equal(t, "real-sub", h.identity.synced)
	assert.Empty(t, h.identity.deleted, "an existing account is never rolled back")
}

func TestCreateUsernameExistsUnresolvableIsUnavailable(t *testing.T) {
	h := newHarness()
	h.identity.inviteErr = errors.New("UsernameExistsException: An account with the given email already exists.")
	_, _, err := h.service.Create(h.request(), h.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrUnavailable, err)
}

func TestCreateOtherInviteErrorIsInviteFailed(t *testing.T) {
	h := newHarness()
	h.identity.inviteErr = errors.New("throttled")
	_, _, err := h.service.Create(h.request(), h.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrInviteFailed, err)
}

func TestCreateEligibilityRules(t *testing.T) {
	h := newHarness()
	self := h.existingUser("yo@ejemplo.mx", true)
	h.actor = self
	_, _, err := h.service.Create(h.request(), h.event, emailBody("yo@ejemplo.mx"))
	assert.Equal(t, ErrSelf, err)

	for _, mutate := range []func(*harness, *models.User){
		func(_ *harness, u *models.User) { u.IsRoot = true; u.RootLevel = models.RootLevelPrimary },
		func(_ *harness, u *models.User) { u.IsActive = false },
	} {
		g := newHarness()
		target := g.existingUser("ana@ejemplo.mx", true)
		mutate(g, target)
		_, _, err := g.service.Create(g.request(), g.event, emailBody("ana@ejemplo.mx"))
		assert.Equal(t, ErrUnavailable, err)
	}

	member := newHarness()
	target := member.existingUser("ana@ejemplo.mx", true)
	member.store.memberships[target.ID] = true
	_, _, err = member.service.Create(member.request(), member.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrIsOrganizationMember, err)

	inherited := newHarness()
	target = inherited.existingUser("ana@ejemplo.mx", true)
	inherited.orgs.access[target.ID] = true
	_, _, err = inherited.service.Create(inherited.request(), inherited.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrIsOrganizationMember, err)
}

func TestCreateOutsideEventiAppNeverInvites(t *testing.T) {
	h := newHarness()
	h.orgs.product = "cafettonhouse"
	_, _, err := h.service.Create(h.request(), h.event, emailBody("ana@ejemplo.mx"))
	assert.Equal(t, ErrUnavailable, err)
	assert.Zero(t, h.identity.inviteCalls)
}

func TestCreateValidatesBodyShape(t *testing.T) {
	h := newHarness()
	for _, body := range []dtos.EventClientCreateRequest{
		{},
		{Email: ptr("ana@ejemplo.mx"), FirstName: ptr("Ana"), LastName: ptr("López"), UserID: ptr(uuid.Must(uuid.NewV4()).String())},
		{Email: ptr("ana@ejemplo.mx")},
		{Email: ptr("ana@ejemplo.mx"), FirstName: ptr("A"), LastName: ptr("López")},
		{UserID: ptr("not-a-uuid")},
	} {
		_, _, err := h.service.Create(h.request(), h.event, body)
		assert.Equal(t, ErrInvalidRequest, err)
	}
	_, _, err := h.service.Create(h.request(), h.event, emailBody("no-es-correo"))
	assert.Equal(t, ErrInvalidEmail, err)
}

func TestCreateByUserIDRequiresOrganizationClient(t *testing.T) {
	h := newHarness()
	stranger := uuid.Must(uuid.NewV4())
	_, _, err := h.service.Create(h.request(), h.event, dtos.EventClientCreateRequest{UserID: ptr(stranger.String())})
	assert.Equal(t, ErrNotFound, err)

	known := h.existingUser("ana@ejemplo.mx", true)
	h.store.orgClients[known.ID] = true
	response, _, err := h.service.Create(h.request(), h.event, dtos.EventClientCreateRequest{UserID: ptr(known.ID.String())})
	require.NoError(t, err)
	assert.Equal(t, AccountExisting, response.Account)
}

func TestRevokeNotFoundWhenNothingActive(t *testing.T) {
	h := newHarness()
	h.store.revokeResult = false
	assert.Equal(t, ErrNotFound, h.service.Revoke(h.request(), h.event.ID, uuid.Must(uuid.NewV4())))

	g := newHarness()
	user := g.existingUser("ana@ejemplo.mx", true)
	require.NoError(t, g.service.Revoke(g.request(), g.event.ID, user.ID))
	assert.Equal(t, []string{user.CognitoSub}, g.invalidated)
}

func TestUpdateAssignmentsValidatesBeforeWriting(t *testing.T) {
	h := newHarness()
	client := h.existingUser("ana@ejemplo.mx", true)
	h.store.orgClients[client.ID] = true
	sameOrg := uuid.Must(uuid.NewV4())
	otherOrg := uuid.Must(uuid.NewV4())
	foreign := uuid.Must(uuid.NewV4())
	h.store.owners[sameOrg] = h.event.ClientID
	h.store.owners[otherOrg] = &foreign

	_, err := h.service.UpdateAssignments(h.request(), h.event, client.ID, dtos.EventClientAssignmentsRequest{Assign: []string{sameOrg.String(), otherOrg.String()}})
	var domain *Error
	require.ErrorAs(t, err, &domain)
	assert.Equal(t, 422, domain.Status)
	assert.Equal(t, map[string]interface{}{"event_ids": []uuid.UUID{otherOrg}}, domain.Data)
	assert.Empty(t, h.store.grants)

	h.manageable[sameOrg] = false
	_, err = h.service.UpdateAssignments(h.request(), h.event, client.ID, dtos.EventClientAssignmentsRequest{Assign: []string{sameOrg.String()}})
	assert.ErrorIs(t, err, ErrForbidden)
	assert.Empty(t, h.store.grants)

	_, err = h.service.UpdateAssignments(h.request(), h.event, client.ID, dtos.EventClientAssignmentsRequest{Assign: []string{sameOrg.String()}, Unassign: []string{sameOrg.String()}})
	assert.Equal(t, ErrInvalidRequest, err)
	_, err = h.service.UpdateAssignments(h.request(), h.event, client.ID, dtos.EventClientAssignmentsRequest{})
	assert.Equal(t, ErrInvalidRequest, err)
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = uuid.Must(uuid.NewV4()).String()
	}
	_, err = h.service.UpdateAssignments(h.request(), h.event, client.ID, dtos.EventClientAssignmentsRequest{Assign: tooMany})
	assert.Equal(t, ErrInvalidRequest, err)

	stranger := uuid.Must(uuid.NewV4())
	_, err = h.service.UpdateAssignments(h.request(), h.event, stranger, dtos.EventClientAssignmentsRequest{Assign: []string{h.event.ID.String()}})
	assert.Equal(t, ErrNotFound, err)
}

func TestUpdateAssignmentsAppliesAllInOneTransaction(t *testing.T) {
	h := newHarness()
	client := h.existingUser("ana@ejemplo.mx", true)
	h.store.orgClients[client.ID] = true
	a, b := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	h.store.owners[a] = h.event.ClientID
	h.store.owners[b] = h.event.ClientID
	response, err := h.service.UpdateAssignments(h.request(), h.event, client.ID, dtos.EventClientAssignmentsRequest{Assign: []string{a.String()}, Unassign: []string{b.String()}})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{a}, response.Assigned)
	assert.Equal(t, []uuid.UUID{b}, response.Unassigned)
	assert.Equal(t, 1, h.store.transactions)
	assert.Equal(t, []string{client.CognitoSub}, h.invalidated)
}

func TestRevokeOrganizationUser(t *testing.T) {
	h := newHarness()
	_, err := h.service.RevokeOrganizationUser(h.request(), *h.event.ClientID, uuid.Must(uuid.NewV4()))
	assert.Equal(t, ErrNotFound, err)
	h.store.revokeAllCount = 3
	count, err := h.service.RevokeOrganizationUser(h.request(), *h.event.ClientID, uuid.Must(uuid.NewV4()))
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestResendInvitationOnlyForPendingAccounts(t *testing.T) {
	h := newHarness()
	user := h.existingUser("ana@ejemplo.mx", true)
	assert.Equal(t, ErrNotFound, h.service.ResendInvitation(h.request(), h.event.ID, user.ID))

	h.store.grants = append(h.store.grants, h.event.ID)
	h.identity.auth[user.CognitoSub].Status = "CONFIRMED"
	assert.Equal(t, ErrInvitationNotPending, h.service.ResendInvitation(h.request(), h.event.ID, user.ID))

	h.identity.auth[user.CognitoSub].Status = "FORCE_CHANGE_PASSWORD"
	h.identity.auth[user.CognitoSub].Username = "ana@ejemplo.mx"
	require.NoError(t, h.service.ResendInvitation(h.request(), h.event.ID, user.ID))
	assert.Equal(t, []string{"ana@ejemplo.mx"}, h.identity.resent)
}

func TestUnavailableCausesShareOneBody(t *testing.T) {
	// Root, inactive, ambiguous, unverified and wrong-product all return the
	// same *Error value, so the HTTP body is byte-identical.
	assert.Equal(t, "event_client_unavailable", ErrUnavailable.Code)
	assert.Equal(t, 409, ErrUnavailable.Status)
}
