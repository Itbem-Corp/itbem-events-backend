package app

import (
	"events-stocks/dtos"
	"events-stocks/models"
	authproviderrepository "events-stocks/repositories/authproviderrepository"
	usersService "events-stocks/services/users"

	"github.com/gofrs/uuid"
)

// eventClientIdentity adapts the user service and the auth provider to the
// event-client service. It adds no behaviour: invitations keep the standard
// tenant-branded Cognito flow and SyncUser stays the only local mirror path.
type eventClientIdentity struct {
	users *usersService.UserService
	auth  *authproviderrepository.AuthProviderRepo
}

func newEventClientIdentity(users *usersService.UserService, auth *authproviderrepository.AuthProviderRepo) *eventClientIdentity {
	return &eventClientIdentity{users: users, auth: auth}
}

func (i *eventClientIdentity) InviteUserForTenant(email, firstName, lastName, tenantCode string) (*models.User, error) {
	return i.users.InviteUserForTenant(email, firstName, lastName, tenantCode)
}

func (i *eventClientIdentity) SyncUser(cognitoSub string) (*models.User, error) {
	return i.users.SyncUser(cognitoSub)
}

func (i *eventClientIdentity) DeleteFullAccount(cognitoSub string) error {
	return i.users.DeleteFullAccount(cognitoSub)
}

func (i *eventClientIdentity) GetUserByID(id uuid.UUID) (*models.User, error) {
	return i.users.GetUserByID(id)
}

func (i *eventClientIdentity) GetAuthUser(username string) (*dtos.AuthUser, error) {
	return i.auth.GetUser(username, authproviderrepository.DefaultProvider)
}

func (i *eventClientIdentity) ResendInvitation(username string) error {
	return i.auth.ResendInvitation(username, authproviderrepository.DefaultProvider)
}
