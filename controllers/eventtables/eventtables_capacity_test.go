package eventtables

import (
	"encoding/json"
	"net/http"
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"
	eventsService "events-stocks/services/events"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useTableRepo(t *testing.T, repo *mockEventTableRepo) {
	t.Helper()
	original := tableSvc
	tableSvc = eventsService.NewEventTableService(repo, &mockTableCache{})
	t.Cleanup(func() { tableSvc = original })
}

func decodeOverCapacity(t *testing.T, body string) dtos.TableOverCapacityResponse {
	t.Helper()
	var response dtos.TableOverCapacityResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response), body)
	return response
}

// SEAT-1: a plan or bulk assign the repository refuses for capacity is a 409
// with code table_over_capacity, a Spanish message and the offending tables.
func TestSavePlan_OverCapacityIs409WithTables(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	useTableRepo(t, &mockEventTableRepo{
		savePlanFunc: func(uuid.UUID, dtos.SeatingPlanSaveRequest) ([]models.EventTable, error) {
			return nil, &dtos.TableOverCapacityError{Tables: []dtos.TableOverCapacity{{TableID: tableID, Name: "Mesa 1", Capacity: 8, Seats: 10}}}
		},
	})
	c, rec := newTableEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/tables/plan", `{"assignments":[]}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setTableAuthzContext(t, c, eventID)

	require.NoError(t, SavePlan(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	response := decodeOverCapacity(t, rec.Body.String())
	assert.Equal(t, 409, response.Status)
	assert.Equal(t, "table_over_capacity", response.Code)
	assert.Equal(t, "table_over_capacity", response.Error)
	assert.Equal(t, "La mesa «Mesa 1» (10 de 8 lugares) no tiene lugar suficiente.", response.Message)
	assert.Equal(t, []uuid.UUID{tableID}, response.Data.TableIDs)
	require.Len(t, response.Data.Tables, 1)
	assert.Equal(t, 10, response.Data.Tables[0].Seats)
}

func TestAssignTables_OverCapacityIs409(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	first, second := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	useTableRepo(t, &mockEventTableRepo{
		assignFunc: func(uuid.UUID, map[uuid.UUID]*uuid.UUID) error {
			return &dtos.TableOverCapacityError{Tables: []dtos.TableOverCapacity{
				{TableID: first, Name: "Mesa 1", Capacity: 4, Seats: 5},
				{TableID: second, Name: "Mesa 2", Capacity: 2, Seats: 3},
			}}
		},
	})
	body := `{"assignments":[{"guest_id":"` + uuid.Must(uuid.NewV4()).String() + `","table_id":"` + first.String() + `"}]}`
	c, rec := newTableEchoCtx(http.MethodPut, "/events/"+eventID.String()+"/tables/assign", body)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setTableAuthzContext(t, c, eventID)

	require.NoError(t, AssignTables(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	response := decodeOverCapacity(t, rec.Body.String())
	assert.Equal(t, []uuid.UUID{first, second}, response.Data.TableIDs)
	assert.Contains(t, response.Message, "Estas mesas no tienen lugar suficiente")
}

// Lowering a table's capacity under the seats taken is refused; raising it
// never runs the check.
func TestUpdateTable_CapacityBelowSeatsIs409(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	updates := 0
	useTableRepo(t, &mockEventTableRepo{
		getFunc: func(id uuid.UUID) (*models.EventTable, error) {
			return &models.EventTable{ID: id, EventID: eventID, Name: "Mesa 1", Capacity: 8}, nil
		},
		updateFunc: func(*models.EventTable) error { updates++; return nil },
	})
	checks := 0
	original := tableSeatsFit
	tableSeatsFit = func(gotEvent, gotTable uuid.UUID, capacity int) error {
		checks++
		assert.Equal(t, eventID, gotEvent)
		assert.Equal(t, tableID, gotTable)
		if capacity < 6 {
			return &dtos.TableOverCapacityError{Tables: []dtos.TableOverCapacity{{TableID: tableID, Name: "Mesa 1", Capacity: capacity, Seats: 6}}}
		}
		return nil
	}
	t.Cleanup(func() { tableSeatsFit = original })

	put := func(body string) int {
		c, rec := newTableEchoCtx(http.MethodPut, "/tables/"+tableID.String(), body)
		c.SetParamNames("id")
		c.SetParamValues(tableID.String())
		setTableAuthzContext(t, c, eventID)
		require.NoError(t, UpdateTable(c))
		return rec.Code
	}
	assert.Equal(t, http.StatusConflict, put(`{"capacity":4}`))
	assert.Equal(t, 0, updates)
	assert.Equal(t, http.StatusOK, put(`{"capacity":6}`))
	assert.Equal(t, http.StatusOK, put(`{"capacity":12}`))
	assert.Equal(t, 2, checks, "raising the capacity is not checked")
}
