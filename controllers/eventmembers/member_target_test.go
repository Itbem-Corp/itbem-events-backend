package eventmembers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	clientsService "events-stocks/services/clients"
	eventmembersService "events-stocks/services/eventmembers"
	"events-stocks/services/ports"
	usersService "events-stocks/services/users"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var assignmentHierarchy = map[string]int{"OWNER": 1, "ADMIN": 2, "EDITOR": 4, "GUEST": 8}

// eventOrganization is the organization owning one event: effective role
// per user, stored accounts, and every event-assignment write.
type eventOrganization struct {
	clientID uuid.UUID
	eventID  uuid.UUID
	roles    map[uuid.UUID]string
	users    map[uuid.UUID]models.User
	writes   int
}

func (o *eventOrganization) add(roleCode string, user models.User) models.User {
	user.ID = uuid.Must(uuid.NewV4())
	o.users[user.ID] = user
	if roleCode != "" {
		o.roles[user.ID] = roleCode
	}
	return user
}

func (o *eventOrganization) checkAccess(userID, clientID uuid.UUID) (bool, string) {
	role, ok := o.roles[userID]
	return ok && clientID == o.clientID, role
}

type eventClientRepo struct {
	ports.ClientRepository
	organization *eventOrganization
}

func (r eventClientRepo) CheckAccessRecursive(userID, clientID uuid.UUID) (bool, string) {
	return r.organization.checkAccess(userID, clientID)
}
func (r eventClientRepo) GetMemberRole(uuid.UUID, uuid.UUID) (*models.ClientRole, error) {
	return nil, gorm.ErrRecordNotFound
}

type eventRoleRepo struct{ ports.ClientRoleRepository }

func (eventRoleRepo) GetByCode(code string) (*models.ClientRole, error) {
	hierarchy, ok := assignmentHierarchy[strings.ToUpper(code)]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &models.ClientRole{Code: code, Hierarchy: hierarchy}, nil
}

type eventUserRepo struct {
	ports.UserRepository
	organization *eventOrganization
}

func (r eventUserRepo) GetUserByID(id uuid.UUID) (*models.User, error) {
	user, ok := r.organization.users[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}

type assignmentRepo struct{ organization *eventOrganization }

func (r assignmentRepo) List(uuid.UUID) ([]models.EventMember, error) { return nil, nil }
func (r assignmentRepo) Upsert(eventID, userID uuid.UUID, role string) (*models.EventMember, error) {
	r.organization.writes++
	return &models.EventMember{EventID: eventID, UserID: userID, Role: role}, nil
}
func (r assignmentRepo) Remove(uuid.UUID, uuid.UUID) error { r.organization.writes++; return nil }

type organizationReader struct{}

func (organizationReader) GetClientDetails(uuid.UUID, uuid.UUID) (*models.Client, error) {
	return &models.Client{}, nil
}

func newEventOrganization(t *testing.T) *eventOrganization {
	t.Helper()
	organization := &eventOrganization{
		clientID: uuid.Must(uuid.NewV4()),
		eventID:  uuid.Must(uuid.NewV4()),
		roles:    map[uuid.UUID]string{},
		users:    map[uuid.UUID]models.User{},
	}
	clientsService.SetDefaultClientService(clientsService.NewClientService(eventClientRepo{organization: organization}, eventRoleRepo{}, nil, nil, nil, nil))
	t.Cleanup(func() { clientsService.SetDefaultClientService(nil) })
	usersService.SetDefaultUserService(usersService.NewUserService(eventUserRepo{organization: organization}, nil, nil))
	t.Cleanup(func() { usersService.SetDefaultUserService(nil) })
	previous := service
	InitEventMembersController(eventmembersService.NewEventMemberService(assignmentRepo{organization: organization}, organizationReader{}))
	t.Cleanup(func() { service = previous })
	return organization
}

func (o *eventOrganization) request(t *testing.T, requester models.User, method, body string, params ...string) *httptest.ResponseRecorder {
	t.Helper()
	t.Cleanup(authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := requester
			return &user, nil
		},
		CheckAccessRecursive: o.checkAccess,
		GetEventByIDRaw: func(eventID uuid.UUID) (*models.Event, error) {
			clientID := o.clientID
			return &models.Event{ID: eventID, ClientID: &clientID}, nil
		},
	}))
	req := httptest.NewRequest(method, "/api/events/"+o.eventID.String()+"/members", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("cognito_sub", "event-members-test-sub")
	names, values := []string{"id"}, []string{o.eventID.String()}
	for i := 0; i+1 < len(params); i += 2 {
		names, values = append(names, params[i]), append(values, params[i+1])
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	if method == http.MethodDelete {
		require.NoError(t, Remove(c))
	} else {
		require.NoError(t, Upsert(c))
	}
	return rec
}

func (o *eventOrganization) assign(t *testing.T, requester, target models.User) *httptest.ResponseRecorder {
	return o.request(t, requester, http.MethodPut, fmt.Sprintf(`{"user_id":%q,"role":"VIEWER"}`, target.ID))
}

func (o *eventOrganization) unassign(t *testing.T, requester models.User, targetID uuid.UUID) *httptest.ResponseRecorder {
	return o.request(t, requester, http.MethodDelete, "", "user_id", targetID.String())
}

func (o *eventOrganization) assertDenied(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Zero(t, o.writes, "no event assignment may change")
}

func (o *eventOrganization) assertWritten(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, o.writes)
	o.writes = 0
}

func TestEventAssignmentsProtectHigherRankedMembers(t *testing.T) {
	organization := newEventOrganization(t)
	owner := organization.add("OWNER", models.User{})
	coOwner := organization.add("OWNER", models.User{})
	admin := organization.add("ADMIN", models.User{})
	coAdmin := organization.add("ADMIN", models.User{})
	cases := []struct {
		name      string
		requester models.User
		target    models.User
	}{
		{name: "admin on owner", requester: admin, target: owner},
		{name: "admin on another admin", requester: admin, target: coAdmin},
		{name: "admin on itself", requester: admin, target: admin},
		{name: "owner on another owner", requester: owner, target: coOwner},
	}
	for _, test := range cases {
		t.Run(test.name+" assign", func(t *testing.T) {
			organization.assertDenied(t, organization.assign(t, test.requester, test.target))
		})
		t.Run(test.name+" remove", func(t *testing.T) {
			organization.assertDenied(t, organization.unassign(t, test.requester, test.target.ID))
		})
	}
}

func TestEventAssignmentsStillReachLowerRankedMembers(t *testing.T) {
	organization := newEventOrganization(t)
	owner := organization.add("OWNER", models.User{})
	admin := organization.add("ADMIN", models.User{})
	editor := organization.add("EDITOR", models.User{})

	organization.assertWritten(t, organization.assign(t, owner, admin))
	organization.assertWritten(t, organization.assign(t, admin, editor))
	organization.assertWritten(t, organization.unassign(t, admin, editor.ID))
	// A deleted account has no rank left to protect, so stale rows can be cleaned up.
	organization.assertWritten(t, organization.unassign(t, admin, uuid.Must(uuid.NewV4())))
}

func TestEventAssignmentsProtectPlatformRootAccounts(t *testing.T) {
	organization := newEventOrganization(t)
	owner := organization.add("OWNER", models.User{})
	operational := organization.add("", models.User{RootLevel: models.RootLevelOperational})
	for name, root := range map[string]models.User{
		"primary root":     organization.add("Guest", models.User{RootLevel: models.RootLevelPrimary}),
		"legacy is_root":   organization.add("Guest", models.User{IsRoot: true}),
		"operational root": organization.add("Guest", models.User{RootLevel: models.RootLevelOperational}),
	} {
		t.Run("owner on "+name, func(t *testing.T) {
			organization.assertDenied(t, organization.assign(t, owner, root))
		})
		t.Run("operational root on "+name, func(t *testing.T) {
			organization.assertDenied(t, organization.unassign(t, operational, root.ID))
		})
	}
}

func TestEventAssignmentsOperationalRootStopsAtOwner(t *testing.T) {
	organization := newEventOrganization(t)
	operational := organization.add("", models.User{RootLevel: models.RootLevelOperational})
	owner := organization.add("OWNER", models.User{})
	admin := organization.add("ADMIN", models.User{})

	organization.assertDenied(t, organization.assign(t, operational, owner))
	organization.assertWritten(t, organization.assign(t, operational, admin))
}

func TestEventAssignmentsPrimaryRootActsOnAnyMember(t *testing.T) {
	organization := newEventOrganization(t)
	primary := organization.add("", models.User{RootLevel: models.RootLevelPrimary})
	owner := organization.add("OWNER", models.User{})

	organization.assertWritten(t, organization.assign(t, primary, owner))
	organization.assertWritten(t, organization.unassign(t, primary, owner.ID))
}

// Managers of another organization never reach this event's team.
func TestEventAssignmentsDenyManagersOfAnotherOrganization(t *testing.T) {
	organization := newEventOrganization(t)
	editor := organization.add("EDITOR", models.User{})
	foreignOwner := models.User{ID: uuid.Must(uuid.NewV4())}

	organization.assertDenied(t, organization.assign(t, foreignOwner, editor))
}
