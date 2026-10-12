package events

import (
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/models"
	"events-stocks/services/applications"
	eventsService "events-stocks/services/events"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopeCaptureRepo records the scope each events list receives.
type scopeCaptureRepo struct {
	mockEventsRepo
	calls      int
	clientID   *uuid.UUID
	caller     *dtos.EventCallerAccess
	allEvents  bool
	clientList *uuid.UUID
}

func (r *scopeCaptureRepo) record(clientID *uuid.UUID, caller *dtos.EventCallerAccess) {
	r.calls++
	r.clientID = clientID
	r.caller = caller
}

func (r *scopeCaptureRepo) GetEventsForUser(access dtos.EventCallerAccess) ([]models.Event, error) {
	r.record(nil, &access)
	return []models.Event{}, nil
}

func (r *scopeCaptureRepo) GetAllEventsForDashboard() ([]models.Event, error) {
	r.calls++
	r.allEvents = true
	return []models.Event{}, nil
}

func (r *scopeCaptureRepo) GetEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	r.calls++
	r.clientList = &clientID
	return []models.Event{}, nil
}

func (r *scopeCaptureRepo) ListEventPage(clientID *uuid.UUID, caller *dtos.EventCallerAccess, _ dtos.EventListQuery) ([]models.Event, int, dtos.EventListCounts, error) {
	r.record(clientID, caller)
	return []models.Event{}, 0, dtos.EventListCounts{}, nil
}

func (r *scopeCaptureRepo) GetEventDashboardOverview(clientID *uuid.UUID, caller *dtos.EventCallerAccess, _ time.Time) (dtos.EventDashboardOverview, error) {
	r.record(clientID, caller)
	return dtos.EventDashboardOverview{}, nil
}

func (r *scopeCaptureRepo) ListEventNotifications(clientID *uuid.UUID, caller *dtos.EventCallerAccess, _ time.Time) ([]dtos.EventNotification, error) {
	r.record(clientID, caller)
	return []dtos.EventNotification{}, nil
}

type eventListEndpoint struct {
	name    string
	path    string
	handler func(echo.Context) error
}

var eventListEndpoints = []eventListEndpoint{
	{name: "unpaged list", path: "/api/events", handler: ListEvents},
	{name: "paged list", path: "/api/events?page=1&page_size=20", handler: ListEvents},
	{name: "dashboard overview", path: "/api/events/dashboard", handler: GetDashboardOverview},
	{name: "notifications", path: "/api/events/notifications", handler: GetEventNotifications},
}

func useScopeCaptureRepo(t *testing.T) *scopeCaptureRepo {
	t.Helper()
	repo := &scopeCaptureRepo{}
	previous := eventSvc
	eventSvc = eventsService.NewEventService(repo, nil)
	t.Cleanup(func() { eventSvc = previous })
	return repo
}

func useListUser(t *testing.T, user *models.User, allowedClient *uuid.UUID) {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) { return user, nil },
		CheckAccessRecursive: func(_ uuid.UUID, clientID uuid.UUID) (bool, string) {
			if allowedClient != nil && clientID == *allowedClient {
				return true, "EVENT_MANAGER"
			}
			return false, ""
		},
	})
	t.Cleanup(restore)
}

func eventiAppSession(applicationID uuid.UUID) *applications.Session {
	return &applications.Session{Application: models.Application{ID: applicationID, Code: "eventiapp", IsActive: true}}
}

func TestEventListsScopeNonRootPortfolioToRequestApplication(t *testing.T) {
	for _, endpoint := range eventListEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			repo := useScopeCaptureRepo(t)
			user := &models.User{ID: uuid.Must(uuid.NewV4()), IsActive: true}
			useListUser(t, user, nil)
			applicationID := uuid.Must(uuid.NewV4())
			c, rec := newEchoCtx(http.MethodGet, endpoint.path, "")
			c.Set("cognito_sub", "list-scope-sub")
			c.Set(applicationaccess.ContextKey, eventiAppSession(applicationID))

			require.NoError(t, endpoint.handler(c))

			assert.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 1, repo.calls)
			assert.Nil(t, repo.clientID)
			assert.False(t, repo.allEvents, "a non-root caller must never get the platform portfolio")
			require.NotNil(t, repo.caller)
			assert.Equal(t, dtos.EventCallerAccess{UserID: user.ID, ApplicationID: applicationID}, *repo.caller)
		})
	}
}

func TestEventListsFailClosedWithoutApplicationSession(t *testing.T) {
	sessions := map[string]*applications.Session{
		"missing session":             nil,
		"session without application": {Application: models.Application{Code: "eventiapp"}},
	}
	for _, endpoint := range eventListEndpoints {
		for label, session := range sessions {
			t.Run(endpoint.name+"/"+label, func(t *testing.T) {
				repo := useScopeCaptureRepo(t)
				useListUser(t, &models.User{ID: uuid.Must(uuid.NewV4()), IsActive: true}, nil)
				c, rec := newEchoCtx(http.MethodGet, endpoint.path, "")
				c.Set("cognito_sub", "list-scope-sub")
				if session != nil {
					c.Set(applicationaccess.ContextKey, session)
				}

				require.NoError(t, endpoint.handler(c))

				assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
				assert.Zero(t, repo.calls, "no events query may run without the request application")
			})
		}
	}
}

func TestEventListsKeepPlatformPortfolioForRoots(t *testing.T) {
	for _, endpoint := range eventListEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			repo := useScopeCaptureRepo(t)
			useListUser(t, &models.User{ID: uuid.Must(uuid.NewV4()), IsActive: true, IsRoot: true}, nil)
			c, rec := newEchoCtx(http.MethodGet, endpoint.path, "")
			c.Set("cognito_sub", "list-scope-sub")
			c.Set(applicationaccess.ContextKey, eventiAppSession(uuid.Must(uuid.NewV4())))

			require.NoError(t, endpoint.handler(c))

			assert.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 1, repo.calls)
			assert.Nil(t, repo.caller)
			assert.Nil(t, repo.clientID)
		})
	}
}

func TestEventListsWithClientIDKeepOrganizationAuthorization(t *testing.T) {
	for _, endpoint := range eventListEndpoints {
		t.Run(endpoint.name+"/allowed", func(t *testing.T) {
			repo := useScopeCaptureRepo(t)
			clientID := uuid.Must(uuid.NewV4())
			useListUser(t, &models.User{ID: uuid.Must(uuid.NewV4()), IsActive: true}, &clientID)
			c, rec := newEchoCtx(http.MethodGet, withClientID(endpoint.path, clientID), "")
			c.Set("cognito_sub", "list-scope-sub")
			c.Set(applicationaccess.ContextKey, eventiAppSession(uuid.Must(uuid.NewV4())))

			require.NoError(t, endpoint.handler(c))

			assert.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 1, repo.calls)
			assert.Nil(t, repo.caller)
			if repo.clientList != nil {
				assert.Equal(t, clientID, *repo.clientList)
			} else {
				require.NotNil(t, repo.clientID)
				assert.Equal(t, clientID, *repo.clientID)
			}
		})
		t.Run(endpoint.name+"/denied", func(t *testing.T) {
			repo := useScopeCaptureRepo(t)
			useListUser(t, &models.User{ID: uuid.Must(uuid.NewV4()), IsActive: true}, nil)
			c, rec := newEchoCtx(http.MethodGet, withClientID(endpoint.path, uuid.Must(uuid.NewV4())), "")
			c.Set("cognito_sub", "list-scope-sub")
			c.Set(applicationaccess.ContextKey, eventiAppSession(uuid.Must(uuid.NewV4())))

			require.NoError(t, endpoint.handler(c))

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Zero(t, repo.calls)
		})
	}
}

func withClientID(path string, clientID uuid.UUID) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "client_id=" + clientID.String()
}
