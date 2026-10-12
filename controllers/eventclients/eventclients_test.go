package eventclients

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/internal/products/eventiapp"
	"events-stocks/models"
	eventclientsService "events-stocks/services/eventclients"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type route struct {
	name    string
	method  string
	handler echo.HandlerFunc
	params  []string
}

var eventRoutes = []route{
	{"list", http.MethodGet, List, []string{"id"}},
	{"candidates", http.MethodGet, Candidates, []string{"id"}},
	{"create", http.MethodPost, Create, []string{"id"}},
	{"revoke", http.MethodDelete, Revoke, []string{"id", "user_id"}},
	{"resend", http.MethodPost, ResendInvitation, []string{"id", "user_id"}},
	{"assignments", http.MethodGet, ListAssignments, []string{"id", "user_id"}},
	{"update assignments", http.MethodPatch, UpdateAssignments, []string{"id", "user_id"}},
}

var oversightRoutes = []route{
	{"oversight list", http.MethodGet, ListOrganizationGrants, []string{"id"}},
	{"oversight revoke", http.MethodDelete, RevokeOrganizationUser, []string{"id", "user_id"}},
}

func call(t *testing.T, r route, id uuid.UUID, tenant string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	e := echo.New()
	recorder := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(r.method, "/api/x", strings.NewReader(`{}`)), recorder)
	c.Request().Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	values := []string{id.String(), uuid.Must(uuid.NewV4()).String()}
	c.SetParamNames(r.params...)
	c.SetParamValues(values[:len(r.params)]...)
	c.Set("cognito_sub", "sub")
	if tenant != "" {
		c.Set("tenant_code", tenant)
	}
	require.NoError(t, r.handler(c))
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return recorder, body
}

func withFlag(t *testing.T, enabled bool) {
	t.Helper()
	previous := eventiapp.EventClientAccessEnabled()
	eventiapp.SetEventClientAccess(enabled)
	t.Cleanup(func() { eventiapp.SetEventClientAccess(previous) })
}

func TestAllRoutesAreNotFoundWhileFlagIsOff(t *testing.T) {
	withFlag(t, false)
	for _, r := range append(append([]route{}, eventRoutes...), oversightRoutes...) {
		recorder, body := call(t, r, uuid.Must(uuid.NewV4()), "eventiapp")
		assert.Equal(t, http.StatusNotFound, recorder.Code, r.name)
		assert.Equal(t, "event_client_access_disabled", body["error"], r.name)
		assert.Equal(t, "Función no disponible", body["message"], r.name)
	}
}

// authFixture configures authz with one actor, one event and one org role.
func authFixture(t *testing.T, actor *models.User, orgRole string, memberRole string, granted bool) uuid.UUID {
	t.Helper()
	orgID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser:        func(string) (*models.User, error) { copy := *actor; return &copy, nil },
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) { return &models.Event{ID: id, ClientID: &orgID}, nil },
		CheckAccessRecursiveE: func(_ uuid.UUID, clientID uuid.UUID) (bool, string, error) {
			if orgRole == "" || clientID != orgID {
				return false, "", nil
			}
			return true, orgRole, nil
		},
		GetEventMemberRole: func(uuid.UUID, uuid.UUID) (string, bool, error) {
			return memberRole, memberRole != "", nil
		},
		HasEventClientGrant:  func(uuid.UUID, uuid.UUID) (bool, error) { return granted, nil },
		ResolveClientProduct: func(uuid.UUID) (string, error) { return "eventiapp", nil },
		GetClientByID: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id, Code: "eventiapp"}, nil
		},
	})
	t.Cleanup(restore)
	_ = eventID
	return orgID
}

func TestEventRoutesRejectRolesWithoutClientManagement(t *testing.T) {
	withFlag(t, true)
	cases := []struct {
		name    string
		actor   *models.User
		role    string
		member  string
		granted bool
	}{
		{"editor", &models.User{ID: uuid.Must(uuid.NewV4())}, "EDITOR", "", false},
		{"checkin", &models.User{ID: uuid.Must(uuid.NewV4())}, "CHECKIN", "", false},
		{"analyst", &models.User{ID: uuid.Must(uuid.NewV4())}, "ANALYST", "", false},
		{"event client", &models.User{ID: uuid.Must(uuid.NewV4())}, "", "", true},
		{"root 2", &models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}, "", "", false},
		{"narrowed event manager", &models.User{ID: uuid.Must(uuid.NewV4())}, "EVENT_MANAGER", "EDITOR", false},
		{"other org manager", &models.User{ID: uuid.Must(uuid.NewV4())}, "", "", false},
	}
	for _, tt := range cases {
		authFixture(t, tt.actor, tt.role, tt.member, tt.granted)
		for _, r := range eventRoutes {
			recorder, _ := call(t, r, uuid.Must(uuid.NewV4()), "eventiapp")
			assert.Equal(t, http.StatusForbidden, recorder.Code, "%s / %s", tt.name, r.name)
		}
	}
}

func TestEventRoutesAllowManagersPastAuthorization(t *testing.T) {
	withFlag(t, true)
	previous := service
	service = nil
	t.Cleanup(func() { service = previous })
	for _, actor := range []struct {
		user *models.User
		role string
	}{
		{&models.User{ID: uuid.Must(uuid.NewV4())}, "EVENT_MANAGER"},
		{&models.User{ID: uuid.Must(uuid.NewV4())}, "Owner"},
		{&models.User{ID: uuid.Must(uuid.NewV4())}, "INHERITED_ADMIN"},
		{&models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelPrimary}, ""},
	} {
		authFixture(t, actor.user, actor.role, "", false)
		// Authorization passes; the nil service then answers 500, which
		// proves the request got past the capability check.
		recorder, body := call(t, eventRoutes[0], uuid.Must(uuid.NewV4()), "eventiapp")
		assert.Equal(t, http.StatusInternalServerError, recorder.Code, actor.role)
		assert.Equal(t, "event_client_internal_error", body["error"])
	}
}

func TestOversightRequiresOrganizationMembersManage(t *testing.T) {
	withFlag(t, true)
	for _, tt := range []struct {
		name  string
		actor *models.User
		role  string
	}{
		{"event manager", &models.User{ID: uuid.Must(uuid.NewV4())}, "EVENT_MANAGER"},
		{"editor", &models.User{ID: uuid.Must(uuid.NewV4())}, "EDITOR"},
		{"owner of another org", &models.User{ID: uuid.Must(uuid.NewV4())}, ""},
	} {
		authFixture(t, tt.actor, tt.role, "", false)
		for _, r := range oversightRoutes {
			recorder, _ := call(t, r, uuid.Must(uuid.NewV4()), "eventiapp")
			assert.Equal(t, http.StatusForbidden, recorder.Code, "%s / %s", tt.name, r.name)
		}
	}
}

func TestOversightIsNotFoundOutsideEventProducts(t *testing.T) {
	withFlag(t, true)
	for _, r := range oversightRoutes {
		recorder, body := call(t, r, uuid.Must(uuid.NewV4()), "cafettonhouse")
		assert.Equal(t, http.StatusNotFound, recorder.Code)
		assert.Equal(t, "event_client_access_disabled", body["error"])
	}
}

func TestUnavailableResponsesAreByteIdentical(t *testing.T) {
	render := func(err error) string {
		e := echo.New()
		recorder := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), recorder)
		require.NoError(t, respond(c, err))
		return recorder.Body.String()
	}
	first := render(eventclientsService.ErrUnavailable)
	assert.Equal(t, first, render(eventclientsService.ErrUnavailable))
	assert.Contains(t, first, "event_client_unavailable")

	internal := render(errors.New("pq: connection refused at 10.0.0.1"))
	assert.NotContains(t, internal, "10.0.0.1", "internal errors never leak details")
	forbidden := render(eventclientsService.ErrForbidden)
	assert.Contains(t, forbidden, "Access denied")
}
