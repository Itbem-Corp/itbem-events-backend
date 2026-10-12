package eventtables

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	guestsService "events-stocks/services/guests"
	"events-stocks/services/ports"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTableEchoCtx(method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func setTableAuthzContext(t *testing.T, c echo.Context, eventID uuid.UUID) {
	t.Helper()
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			assert.Equal(t, eventID, id)
			return &models.Event{ID: id}, nil
		},
	})
	t.Cleanup(restore)
}

type mockEventTableRepo struct {
	createFunc   func(table *models.EventTable) error
	updateFunc   func(table *models.EventTable) error
	deleteFunc   func(id uuid.UUID) error
	getFunc      func(id uuid.UUID) (*models.EventTable, error)
	listFunc     func(eventID uuid.UUID) ([]models.EventTable, error)
	assignFunc   func(eventID uuid.UUID, assignments map[uuid.UUID]*uuid.UUID) error
	savePlanFunc func(eventID uuid.UUID, plan dtos.SeatingPlanSaveRequest) ([]models.EventTable, error)
}

func (m *mockEventTableRepo) CreateEventTable(table *models.EventTable) error {
	if m.createFunc != nil {
		return m.createFunc(table)
	}
	return nil
}

func (m *mockEventTableRepo) UpdateEventTable(table *models.EventTable) error {
	if m.updateFunc != nil {
		return m.updateFunc(table)
	}
	return nil
}

func (m *mockEventTableRepo) DeleteEventTable(id uuid.UUID) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(id)
	}
	return nil
}

func (m *mockEventTableRepo) GetEventTableByID(id uuid.UUID) (*models.EventTable, error) {
	if m.getFunc != nil {
		return m.getFunc(id)
	}
	return &models.EventTable{ID: id}, nil
}

func (m *mockEventTableRepo) ListEventTablesByEventID(eventID uuid.UUID) ([]models.EventTable, error) {
	if m.listFunc != nil {
		return m.listFunc(eventID)
	}
	return nil, nil
}

func (m *mockEventTableRepo) AssignGuestsToTables(eventID uuid.UUID, assignments map[uuid.UUID]*uuid.UUID) error {
	if m.assignFunc != nil {
		return m.assignFunc(eventID, assignments)
	}
	return nil
}

func (m *mockEventTableRepo) SaveSeatingPlan(eventID uuid.UUID, plan dtos.SeatingPlanSaveRequest) ([]models.EventTable, error) {
	if m.savePlanFunc != nil {
		return m.savePlanFunc(eventID, plan)
	}
	return nil, nil
}

var _ ports.EventTableRepository = (*mockEventTableRepo)(nil)

type mockTableCache struct{}

func (m *mockTableCache) Invalidate(_, _ string) error { return nil }
func (m *mockTableCache) DeleteKeysByPattern(_ context.Context, _ string) error {
	return nil
}
func (m *mockTableCache) GetKey(_ context.Context, _ string) (string, error) { return "", nil }
func (m *mockTableCache) SaveKey(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}

var _ ports.CacheRepository = (*mockTableCache)(nil)

func withTableService(t *testing.T, repo ports.EventTableRepository) {
	t.Helper()
	orig := tableSvc
	tableSvc = eventsService.NewEventTableService(repo, &mockTableCache{})
	t.Cleanup(func() { tableSvc = orig })
}

// seatingAuth describes the caller seen by the authz hooks: an organization
// role, an optional event assignment that may narrow it, or a platform level.
type seatingAuth struct {
	orgRole   string
	eventRole string
	rootLevel int
}

func setSeatingAuth(t *testing.T, c echo.Context, auth seatingAuth) {
	t.Helper()
	userID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: userID, RootLevel: auth.rootLevel}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, ClientID: &clientID}, nil
		},
		CheckAccessRecursive: func(gotUserID, gotClientID uuid.UUID) (bool, string) {
			if gotUserID != userID || gotClientID != clientID || auth.orgRole == "" {
				return false, ""
			}
			return true, auth.orgRole
		},
		GetEventMemberRole: func(uuid.UUID, uuid.UUID) (string, bool, error) {
			return auth.eventRole, auth.eventRole != "", nil
		},
	})
	t.Cleanup(restore)
}

// seatingGuestRepo serves the seating projection; the embedded interface is
// left nil because GetSeatingWorkspace must not call any other guest method.
type seatingGuestRepo struct {
	ports.GuestRepository
	called bool
	guests []dtos.SeatingGuest
}

func (m *seatingGuestRepo) ListSeatingGuestsByEventID(uuid.UUID) ([]dtos.SeatingGuest, error) {
	m.called = true
	return m.guests, nil
}

func runSeatingWorkspace(t *testing.T, auth seatingAuth) (*httptest.ResponseRecorder, *seatingGuestRepo) {
	t.Helper()
	eventID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	withTableService(t, &mockEventTableRepo{
		listFunc: func(id uuid.UUID) ([]models.EventTable, error) {
			return []models.EventTable{{ID: tableID, EventID: id, Name: "Mesa 1", Capacity: 8}}, nil
		},
	})
	guests := &seatingGuestRepo{guests: []dtos.SeatingGuest{{
		ID:         uuid.Must(uuid.NewV4()),
		FirstName:  "Ana",
		Email:      "ana.invitada@example.com",
		TableID:    &tableID,
		RSVPStatus: "confirmed",
	}}}
	previousGuestSvc := seatingGuestSvc
	seatingGuestSvc = guestsService.NewGuestService(guests, nil, nil, nil)
	t.Cleanup(func() { seatingGuestSvc = previousGuestSvc })

	c, rec := newTableEchoCtx(http.MethodGet, "/events/"+eventID.String()+"/seating-workspace", "")
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setSeatingAuth(t, c, auth)

	require.NoError(t, GetSeatingWorkspace(c))
	return rec, guests
}

// The seating workspace lists every guest's email and RSVP state, the same
// projection GET /guests/seating:<id> guards with guest:manage.
func TestGetSeatingWorkspace_RejectsCallersWithoutGuestManage(t *testing.T) {
	cases := map[string]seatingAuth{
		"viewer org role":            {orgRole: "Guest"},
		"analyst org role":           {orgRole: "ANALYST"},
		"check-in org role":          {orgRole: "CHECKIN"},
		"editor narrowed to viewer":  {orgRole: "EDITOR", eventRole: "VIEWER"},
		"member narrowed to checkin": {orgRole: "Member", eventRole: "CHECKIN"},
		"caller outside the org":     {},
	}
	for name, auth := range cases {
		t.Run(name, func(t *testing.T) {
			rec, guests := runSeatingWorkspace(t, auth)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.False(t, guests.called, "guest projection must not be loaded for a denied caller")
			assert.NotContains(t, rec.Body.String(), "ana.invitada@example.com")
		})
	}
}

func TestGetSeatingWorkspace_ReturnsGuestsToGuestManagers(t *testing.T) {
	cases := map[string]seatingAuth{
		"member org role":               {orgRole: "Member"},
		"editor org role":               {orgRole: "EDITOR"},
		"event manager org role":        {orgRole: "EVENT_MANAGER"},
		"owner org role":                {orgRole: "Owner"},
		"editor with editor event":      {orgRole: "EDITOR", eventRole: "EDITOR"},
		"operational platform root (2)": {rootLevel: models.RootLevelOperational},
	}
	for name, auth := range cases {
		t.Run(name, func(t *testing.T) {
			rec, guests := runSeatingWorkspace(t, auth)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.True(t, guests.called)
			assert.Contains(t, rec.Body.String(), "ana.invitada@example.com")
			assert.Contains(t, rec.Body.String(), `"name":"Mesa 1"`)
		})
	}
}

func TestCreateTable_AcceptsSortOrderAliasAndTrimsName(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	var captured *models.EventTable
	withTableService(t, &mockEventTableRepo{
		createFunc: func(table *models.EventTable) error {
			table.ID = tableID
			copied := *table
			captured = &copied
			return nil
		},
	})

	c, rec := newTableEchoCtx(http.MethodPost, "/events/"+eventID.String()+"/tables", `{"name":" Mesa VIP ","capacity":8,"sortOrder":3}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setTableAuthzContext(t, c, eventID)

	require.NoError(t, CreateTable(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, captured)
	assert.Equal(t, eventID, captured.EventID)
	assert.Equal(t, "Mesa VIP", captured.Name)
	assert.Equal(t, 8, captured.Capacity)
	assert.Equal(t, 3, captured.SortOrder)
	assert.Contains(t, rec.Body.String(), `"sort_order":3`)
}

func TestCreateTable_RejectsInvalidCapacity(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	called := false
	withTableService(t, &mockEventTableRepo{
		createFunc: func(table *models.EventTable) error {
			called = true
			return nil
		},
	})

	c, rec := newTableEchoCtx(http.MethodPost, "/events/"+eventID.String()+"/tables", `{"name":"Mesa 1","capacity":0}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setTableAuthzContext(t, c, eventID)

	require.NoError(t, CreateTable(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, called)
}

func TestUpdateTable_AcceptsSortOrderAliasAndValidatesName(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	var captured *models.EventTable
	withTableService(t, &mockEventTableRepo{
		getFunc: func(id uuid.UUID) (*models.EventTable, error) {
			return &models.EventTable{ID: id, EventID: eventID, Name: "Mesa 1", Capacity: 8, SortOrder: 1}, nil
		},
		updateFunc: func(table *models.EventTable) error {
			copied := *table
			captured = &copied
			return nil
		},
	})

	c, rec := newTableEchoCtx(http.MethodPut, "/tables/"+tableID.String(), `{"name":" Mesa Familia ","capacity":10,"sortOrder":4}`)
	c.SetParamNames("id")
	c.SetParamValues(tableID.String())
	setTableAuthzContext(t, c, eventID)

	require.NoError(t, UpdateTable(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "Mesa Familia", captured.Name)
	assert.Equal(t, 10, captured.Capacity)
	assert.Equal(t, 4, captured.SortOrder)
	assert.Contains(t, rec.Body.String(), `"sort_order":4`)
}

func TestAssignTables_AcceptsCamelAssignmentAliasesAndNullUnassign(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	unassignedGuestID := uuid.Must(uuid.NewV4())
	var captured map[uuid.UUID]*uuid.UUID
	withTableService(t, &mockEventTableRepo{
		assignFunc: func(id uuid.UUID, assignments map[uuid.UUID]*uuid.UUID) error {
			assert.Equal(t, eventID, id)
			captured = assignments
			return nil
		},
	})

	body := `{"assignments":[{"guestId":"` + guestID.String() + `","tableId":"` + tableID.String() + `"},{"guestId":"` + unassignedGuestID.String() + `","tableId":null}]}`
	c, rec := newTableEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/tables/assign", body)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setTableAuthzContext(t, c, eventID)

	require.NoError(t, AssignTables(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, captured)
	require.NotNil(t, captured[guestID])
	assert.Equal(t, tableID, *captured[guestID])
	assert.Nil(t, captured[unassignedGuestID])
}

func TestSavePlan_UsesOneTransactionalServiceCall(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	var captured dtos.SeatingPlanSaveRequest
	withTableService(t, &mockEventTableRepo{
		savePlanFunc: func(id uuid.UUID, plan dtos.SeatingPlanSaveRequest) ([]models.EventTable, error) {
			assert.Equal(t, eventID, id)
			captured = plan
			return []models.EventTable{{ID: uuid.Must(uuid.NewV4()), EventID: eventID, Name: "Mesa nueva", Capacity: 8}}, nil
		},
	})

	body := `{"created":[{"temp_id":"temp-1","name":" Mesa nueva ","capacity":8,"sort_order":1}],"updated":[],"deleted_ids":[],"assignments":[{"guest_id":"` + guestID.String() + `","table_id":"temp-1"}]}`
	c, rec := newTableEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/tables/plan", body)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setTableAuthzContext(t, c, eventID)

	require.NoError(t, SavePlan(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, captured.Created, 1)
	assert.Equal(t, "temp-1", captured.Created[0].TempID)
	require.Len(t, captured.Assignments, 1)
	assert.Equal(t, guestID.String(), captured.Assignments[0].GuestID)
	assert.Contains(t, rec.Body.String(), `"name":"Mesa nueva"`)
}
