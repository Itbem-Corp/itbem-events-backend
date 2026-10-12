package guests

import (
	"encoding/json"
	"net/http"
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"
	guestsService "events-stocks/services/guests"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type seatsCall struct {
	tableID uuid.UUID
	exclude []uuid.UUID
	seats   int
}

// stubTableSeats makes every table hold room seats and records each check.
func stubTableSeats(t *testing.T, room int) *[]seatsCall {
	t.Helper()
	var calls []seatsCall
	original := guestTableSeatsFit
	guestTableSeatsFit = func(eventID, tableID uuid.UUID, exclude []uuid.UUID, seats int) error {
		calls = append(calls, seatsCall{tableID: tableID, exclude: exclude, seats: seats})
		if seats > room {
			return &dtos.TableOverCapacityError{Tables: []dtos.TableOverCapacity{{TableID: tableID, Name: "Mesa", Capacity: room, Seats: seats}}}
		}
		return nil
	}
	t.Cleanup(func() { guestTableSeatsFit = original })
	return &calls
}

func assertTableOverCapacity(t *testing.T, code int, body string, tableID uuid.UUID) {
	t.Helper()
	assert.Equal(t, http.StatusConflict, code, body)
	var response dtos.TableOverCapacityResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response), body)
	assert.Equal(t, "table_over_capacity", response.Code)
	assert.Equal(t, []uuid.UUID{tableID}, response.Data.TableIDs)
	assert.Contains(t, response.Message, "no tiene lugar suficiente")
}

// SEAT-1 on PUT /guests/:id: moving a guest to a table where its party does
// not fit is a 409; its own stored seats are excluded from the count.
func TestUpdateGuest_TableWithoutRoomIs409(t *testing.T) {
	f := newUpdateGuestFixture(t)
	table := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.stored.EventID, Name: "Mesa 1", Capacity: 4}
	useGuestTables(t, table)
	calls := stubTableSeats(t, 2)
	f.stored.RSVPGuestCount = 3
	f.repo.existing.RSVPGuestCount = 3

	code, body := f.put(t, `{"table_id":"`+table.ID.String()+`"}`)
	assertTableOverCapacity(t, code, body, table.ID)
	assert.Nil(t, f.repo.updated)
	require.Len(t, *calls, 1)
	assert.Equal(t, 3, (*calls)[0].seats)
	assert.Equal(t, []uuid.UUID{f.guestID}, (*calls)[0].exclude)
}

func TestUpdateGuest_TableWithRoomIsSaved(t *testing.T) {
	f := newUpdateGuestFixture(t)
	table := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.stored.EventID, Name: "Mesa 1", Capacity: 4}
	useGuestTables(t, table)
	stubTableSeats(t, 4)
	code, body := f.put(t, `{"table_id":"`+table.ID.String()+`"}`)
	assert.Equal(t, http.StatusOK, code, body)
}

// A seated guest whose party grows must still fit; edits that do not add
// seats at the same table are not checked.
func TestUpdateGuest_GrowingPartyAtItsTableMustFit(t *testing.T) {
	f := newUpdateGuestFixture(t)
	tableID := uuid.Must(uuid.NewV4())
	f.stored.TableID = &tableID
	f.repo.existing.TableID = &tableID
	f.stored.MaxGuests = 6
	f.repo.existing.MaxGuests = 6
	f.stored.RSVPGuestCount = 2
	f.repo.existing.RSVPGuestCount = 2
	calls := stubTableSeats(t, 3)

	code, body := f.put(t, `{"phone":"5512345678"}`)
	assert.Equal(t, http.StatusOK, code, body)
	assert.Empty(t, *calls)

	code, body = f.put(t, `{"guests_count":5}`)
	assertTableOverCapacity(t, code, body, tableID)
}

// Declined guests take no seat (same rule as the dashboard).
func TestUpdateGuest_DeclinedGuestTakesNoSeat(t *testing.T) {
	f := newUpdateGuestFixture(t)
	table := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.stored.EventID, Name: "Mesa 1", Capacity: 4}
	useGuestTables(t, table)
	calls := stubTableSeats(t, 0)
	code, body := f.put(t, `{"table_id":"`+table.ID.String()+`","rsvp_status":"declined"}`)
	assert.Equal(t, http.StatusOK, code, body)
	require.Len(t, *calls, 1)
	assert.Equal(t, 0, (*calls)[0].seats)
}

// POST /guests/bulk sums the parties going to each table.
func TestCreateGuests_SumsPartiesPerTable(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()
	eventID := uuid.Must(uuid.NewV4())
	table := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: eventID, Name: "Mesa 1", Capacity: 4}
	useGuestTables(t, table)
	calls := stubTableSeats(t, 4)

	body := `[{"first_name":"Ana","event_id":"` + eventID.String() + `","table_id":"` + table.ID.String() + `","guests_count":2},
		{"first_name":"Bob","event_id":"` + eventID.String() + `","table_id":"` + table.ID.String() + `","guests_count":3}]`
	c, rec := newEchoCtx(http.MethodPost, "/guests/bulk", body)
	setAuthzContext(t, c, uuid.Nil, nil)
	require.NoError(t, CreateGuests(c))
	assertTableOverCapacity(t, rec.Code, rec.Body.String(), table.ID)
	require.Len(t, *calls, 1)
	assert.Equal(t, 5, (*calls)[0].seats)
}

func TestCreateGuest_TableWithoutRoomIs409(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()
	eventID := uuid.Must(uuid.NewV4())
	table := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: eventID, Name: "Mesa 1", Capacity: 1}
	useGuestTables(t, table)
	stubTableSeats(t, 1)

	body := `{"first_name":"Ana","event_id":"` + eventID.String() + `","table_id":"` + table.ID.String() + `","max_guests":2}`
	c, rec := newEchoCtx(http.MethodPost, "/guests", body)
	setAuthzContext(t, c, uuid.Nil, nil)
	require.NoError(t, CreateGuest(c))
	assertTableOverCapacity(t, rec.Code, rec.Body.String(), table.ID)
}
