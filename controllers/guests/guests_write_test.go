package guests

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"events-stocks/dtos"
	"events-stocks/models"
	guestsService "events-stocks/services/guests"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useGuestTables replaces the table_id lookup with an in-memory table list.
func useGuestTables(t *testing.T, tables ...models.EventTable) {
	t.Helper()
	original := guestTableByID
	guestTableByID = func(id uuid.UUID) (*models.EventTable, error) {
		for i := range tables {
			if tables[i].ID == id {
				return &tables[i], nil
			}
		}
		return nil, gorm.ErrRecordNotFound
	}
	t.Cleanup(func() { guestTableByID = original })
}

type recordingGuestInvitationRepo struct {
	mockGuestInvitationRepo
	lookedUp []uuid.UUID
	updated  []models.Invitation
}

func (m *recordingGuestInvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	m.lookedUp = append(m.lookedUp, id)
	return &models.Invitation{ID: id, MaxGuests: 1}, nil
}

func (m *recordingGuestInvitationRepo) UpdateInvitation(invitation *models.Invitation) error {
	m.updated = append(m.updated, *invitation)
	return nil
}

type updateGuestFixture struct {
	repo        *captureUpdateGuestRepo
	invitations *recordingGuestInvitationRepo
	guestID     uuid.UUID
	stored      models.Guest
}

// newUpdateGuestFixture wires a stored guest whose server-owned columns are all
// set, so a test can prove the request body cannot change them.
func newUpdateGuestFixture(t *testing.T) *updateGuestFixture {
	t.Helper()
	guestID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	tokenID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	rsvpAt := time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC)
	stored := models.Guest{
		ID: guestID, EventID: uuid.Must(uuid.NewV4()), FirstName: "Ana", InvitationID: &invitationID,
		RSVPTokenID: &tokenID, CreatedAt: createdAt, RSVPAt: &rsvpAt, RSVPStatus: "confirmed", RSVPMethod: "web",
		GuestStatusID: statusID, StatusID: statusID, GuestStatus: models.GuestStatus{ID: statusID, Code: "CONFIRMED"},
	}
	existing := stored
	fixture := &updateGuestFixture{
		repo:        &captureUpdateGuestRepo{existing: &existing},
		invitations: &recordingGuestInvitationRepo{},
		guestID:     guestID,
		stored:      stored,
	}
	svc := guestsService.NewGuestService(fixture.repo, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{}, fixture.invitations)
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })
	return fixture
}

func (f *updateGuestFixture) put(t *testing.T, body string) (int, string) {
	t.Helper()
	c, rec := newEchoCtx(http.MethodPut, "/guests/"+f.guestID.String(), body)
	c.SetParamNames("id")
	c.SetParamValues(f.guestID.String())
	authzCopy := f.stored
	setAuthzContext(t, c, f.stored.EventID, &authzCopy)
	require.NoError(t, UpdateGuest(c))
	return rec.Code, rec.Body.String()
}

func TestUpdateGuest_IgnoresServerOwnedFieldsInBody(t *testing.T) {
	f := newUpdateGuestFixture(t)
	foreign := uuid.Must(uuid.NewV4()).String()
	body := `{"first_name":"Bea","id":"` + foreign + `","event_id":"` + foreign + `","eventId":"` + foreign + `",
		"invitation_id":"` + foreign + `","invitationId":"` + foreign + `","InvitationID":"` + foreign + `",
		"rsvp_token_id":"` + foreign + `","rsvpTokenId":"` + foreign + `",
		"created_at":"2020-01-01T00:00:00Z","updated_at":"2020-01-01T00:00:00Z",
		"rsvp_at":"2020-01-01T00:00:00Z","rsvpAt":"2020-01-01T00:00:00Z","pretty_token":"HACKED",
		"table":{"name":"Injected","event_id":"` + foreign + `"},"guest_status":{"code":"vip"},"status":{"code":"vip"}}`

	code, _ := f.put(t, body)

	require.Equal(t, http.StatusOK, code)
	require.NotNil(t, f.repo.updated)
	got := f.repo.updated
	assert.Equal(t, "Bea", got.FirstName)
	assert.Equal(t, f.guestID, got.ID)
	assert.Equal(t, f.stored.EventID, got.EventID)
	assert.Equal(t, f.stored.InvitationID, got.InvitationID)
	assert.Equal(t, f.stored.RSVPTokenID, got.RSVPTokenID)
	assert.Equal(t, f.stored.CreatedAt, got.CreatedAt)
	assert.Equal(t, f.stored.RSVPAt, got.RSVPAt)
	assert.Empty(t, got.PrettyToken)
	assert.Nil(t, got.Table)
	assert.Equal(t, "CONFIRMED", got.GuestStatus.Code)
	assert.Nil(t, got.Status)
}

func TestUpdateGuest_MaxGuestsSyncUsesStoredInvitation(t *testing.T) {
	f := newUpdateGuestFixture(t)
	foreign := uuid.Must(uuid.NewV4())

	code, body := f.put(t, `{"max_guests":5,"invitation_id":"`+foreign.String()+`","invitationId":"`+foreign.String()+`"}`)

	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, []uuid.UUID{*f.stored.InvitationID}, f.invitations.lookedUp)
	require.Len(t, f.invitations.updated, 1)
	assert.Equal(t, *f.stored.InvitationID, f.invitations.updated[0].ID)
	assert.Equal(t, 5, f.invitations.updated[0].MaxGuests)
}

func TestUpdateGuest_RejectsTableFromAnotherEvent(t *testing.T) {
	f := newUpdateGuestFixture(t)
	foreignTable := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4())}
	useGuestTables(t, foreignTable)

	code, body := f.put(t, `{"table_id":"`+foreignTable.ID.String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Nil(t, f.repo.updated)
}

func TestUpdateGuest_RejectsUnknownTable(t *testing.T) {
	f := newUpdateGuestFixture(t)
	useGuestTables(t)

	code, body := f.put(t, `{"tableId":"`+uuid.Must(uuid.NewV4()).String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Nil(t, f.repo.updated)
}

func TestUpdateGuest_RejectsMalformedTableID(t *testing.T) {
	f := newUpdateGuestFixture(t)

	code, body := f.put(t, `{"table_id":"mesa-4"}`)

	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Nil(t, f.repo.updated)
}

func TestUpdateGuest_AcceptsTableFromSameEvent(t *testing.T) {
	f := newUpdateGuestFixture(t)
	table := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.stored.EventID}
	useGuestTables(t, table)

	code, body := f.put(t, `{"table_id":"`+table.ID.String()+`"}`)

	require.Equal(t, http.StatusOK, code, body)
	require.NotNil(t, f.repo.updated.TableID)
	assert.Equal(t, table.ID, *f.repo.updated.TableID)
}

func TestUpdateGuest_EmptyOrNullTableIDClearsTable(t *testing.T) {
	for _, value := range []string{`""`, `null`} {
		t.Run(value, func(t *testing.T) {
			f := newUpdateGuestFixture(t)
			tableID := uuid.Must(uuid.NewV4())
			f.stored.TableID = &tableID
			f.repo.existing.TableID = &tableID

			code, body := f.put(t, `{"table_id":`+value+`}`)

			require.Equal(t, http.StatusOK, code, body)
			assert.Nil(t, f.repo.updated.TableID)
		})
	}
}

func TestUpdateGuest_NullScalarsLeaveStoredValues(t *testing.T) {
	f := newUpdateGuestFixture(t)
	f.stored.Notes = "Nota"
	f.repo.existing.Notes = "Nota"
	f.stored.RSVPGuestCount = 2
	f.repo.existing.RSVPGuestCount = 2

	f.stored.RSVPNotes = "Nota RSVP"
	f.repo.existing.RSVPNotes = "Nota RSVP"

	code, body := f.put(t, `{"first_name":null,"notes":null,"guests_count":null,"is_host":null,
		"firstName":null,"rsvpNotes":null,"statusId":null,"guestStatusId":null,"status_id":null}`)

	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "Ana", f.repo.updated.FirstName)
	assert.Equal(t, "Nota", f.repo.updated.Notes)
	assert.Equal(t, "Nota RSVP", f.repo.updated.RSVPNotes)
	assert.Equal(t, 2, f.repo.updated.RSVPGuestCount)
	assert.Equal(t, f.stored.GuestStatusID, f.repo.updated.GuestStatusID)
	assert.Equal(t, f.stored.GuestStatusID, f.repo.updated.StatusID)
}

func TestUpdateGuest_DashboardFormPayloadStillApplies(t *testing.T) {
	f := newUpdateGuestFixture(t)
	body := `{"first_name":"Ana","last_name":"López","email":"ana@example.com","phone":"+52 555","guests_count":3,
		"max_guests":4,"table_number":"4","table_id":"","dietary_restrictions":"Sin gluten","role":"Novia","notes":"VIP",
		"is_host":true,"nickname":"Anita","order":2,"image_url":"profiles/ana.webp","headline":"Hola","bio":"Bio",
		"signature":"Firma","event_id":"` + f.stored.EventID.String() + `"}`

	code, resp := f.put(t, body)

	require.Equal(t, http.StatusOK, code, resp)
	got := f.repo.updated
	assert.Equal(t, "López", got.LastName)
	assert.Equal(t, "ana@example.com", got.Email)
	assert.Equal(t, "+52 555", got.Phone)
	assert.Equal(t, 3, got.GuestsCount)
	assert.Equal(t, 3, got.RSVPGuestCount)
	assert.Equal(t, 4, got.MaxGuests)
	assert.Equal(t, "4", got.TableNumber)
	assert.Nil(t, got.TableID)
	assert.Equal(t, "Sin gluten", got.DietaryRestrictions)
	assert.Equal(t, "Novia", got.Role)
	assert.Equal(t, "VIP", got.Notes)
	assert.True(t, got.IsHost)
	assert.Equal(t, "Anita", got.Nickname)
	assert.Equal(t, 2, got.Order)
	assert.Equal(t, "profiles/ana.webp", got.ImageURL)
	assert.Equal(t, "Hola", got.Headline)
	assert.Equal(t, "Bio", got.Bio)
	assert.Equal(t, "Firma", got.Signature)
}

func TestUpdateGuest_SnakeGuestStatusIDUpdatesBothStatusFields(t *testing.T) {
	f := newUpdateGuestFixture(t)
	statusID := uuid.Must(uuid.NewV4())

	code, body := f.put(t, `{"guest_status_id":"`+statusID.String()+`","rsvp_status":"declined","rsvp_method":"host"}`)

	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, statusID, f.repo.updated.GuestStatusID)
	assert.Equal(t, statusID, f.repo.updated.StatusID)
	assert.Equal(t, "declined", f.repo.updated.RSVPStatus)
}

// Every spelling of the status id treats an empty string and the nil UUID as
// "not sent": the stored status stays, it is never zeroed.
func TestUpdateGuest_EmptyOrNilStatusIDKeepsStoredStatusUnderEveryKey(t *testing.T) {
	keys := []string{
		"status_id", "guest_status_id",
		"statusId", "statusID", "StatusID", "StatusId",
		"guestStatusId", "guestStatusID", "GuestStatusID", "GuestStatusId",
	}
	values := []string{`""`, `"   "`, `"` + uuid.Nil.String() + `"`}
	for _, key := range keys {
		for _, value := range values {
			t.Run(key+"="+value, func(t *testing.T) {
				f := newUpdateGuestFixture(t)

				code, body := f.put(t, `{"`+key+`":`+value+`}`)

				require.Equal(t, http.StatusOK, code, body)
				require.NotNil(t, f.repo.updated)
				assert.Equal(t, f.stored.GuestStatusID, f.repo.updated.GuestStatusID)
				assert.Equal(t, f.stored.GuestStatusID, f.repo.updated.StatusID)
			})
		}
	}
}

func TestUpdateGuest_MalformedSnakeStatusIDReturns400(t *testing.T) {
	for _, value := range []string{`"vip"`, `123`} {
		t.Run(value, func(t *testing.T) {
			f := newUpdateGuestFixture(t)

			code, body := f.put(t, `{"status_id":`+value+`}`)

			assert.Equal(t, http.StatusBadRequest, code, body)
			assert.Nil(t, f.repo.updated)
		})
	}
}

func TestCreateGuest_EmptySnakeStatusIDIsNotSent(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())

	code, guest, raw := createGuestForTest(t, `{"first_name":"Ana","status_id":"","guest_status_id":"","event_id":"`+eventID.String()+`"}`, eventID)

	require.Equal(t, http.StatusCreated, code, raw)
	assert.Equal(t, eventID, guest.EventID)
}

func TestUpdateGuest_WrongSnakeTypeReturns400(t *testing.T) {
	f := newUpdateGuestFixture(t)

	code, body := f.put(t, `{"first_name":123}`)

	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Nil(t, f.repo.updated)
}

func createGuestForTest(t *testing.T, body string, eventID uuid.UUID) (int, dtos.GuestResponse, string) {
	t.Helper()
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })

	c, rec := newEchoCtx(http.MethodPost, "/guests", body)
	setAuthzContext(t, c, eventID, nil)
	require.NoError(t, CreateGuest(c))
	var envelope struct {
		Data dtos.GuestResponse `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	return rec.Code, envelope.Data, rec.Body.String()
}

func TestCreateGuest_IgnoresServerOwnedFieldsAndNestedObjects(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	foreign := uuid.Must(uuid.NewV4()).String()
	body := `{"first_name":"Ana","event_id":"` + eventID.String() + `","id":"` + foreign + `",
		"rsvp_token_id":"` + foreign + `","rsvp_at":"2020-01-01T00:00:00Z","rsvpAt":"2020-01-01T00:00:00Z",
		"created_at":"2020-01-01T00:00:00Z","pretty_token":"HACKED",
		"table":{"id":"` + foreign + `","name":"Injected","event_id":"` + foreign + `"},
		"guest_status":{"id":"` + foreign + `","code":"vip","label":"VIP"}}`

	code, guest, raw := createGuestForTest(t, body, eventID)

	require.Equal(t, http.StatusCreated, code, raw)
	assert.Equal(t, uuid.Nil, guest.ID, "the database assigns the id")
	assert.Equal(t, eventID, guest.EventID)
	assert.Nil(t, guest.RSVPTokenID)
	assert.Nil(t, guest.RSVPAt)
	assert.True(t, guest.CreatedAt.IsZero())
	assert.Equal(t, "ABCD1234", guest.PrettyToken)
	assert.Nil(t, guest.Table)
	assert.Nil(t, guest.GuestStatus)
}

func TestCreateGuest_EmptyTableIDMeansNoTable(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())

	code, guest, raw := createGuestForTest(t, `{"first_name":"Ana","table_id":"","event_id":"`+eventID.String()+`"}`, eventID)

	require.Equal(t, http.StatusCreated, code, raw)
	assert.Nil(t, guest.TableID)
}

func TestCreateGuest_RejectsTableFromAnotherEvent(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	foreignTable := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4())}
	useGuestTables(t, foreignTable)

	code, _, raw := createGuestForTest(t, `{"first_name":"Ana","table_id":"`+foreignTable.ID.String()+`","event_id":"`+eventID.String()+`"}`, eventID)

	assert.Equal(t, http.StatusBadRequest, code, raw)
}

func TestCreateGuest_AcceptsTableFromSameEvent(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	table := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: eventID}
	useGuestTables(t, table)

	code, guest, raw := createGuestForTest(t, `{"first_name":"Ana","tableId":"`+table.ID.String()+`","eventId":"`+eventID.String()+`"}`, eventID)

	require.Equal(t, http.StatusCreated, code, raw)
	require.NotNil(t, guest.TableID)
	assert.Equal(t, table.ID, *guest.TableID)
}

// uniqueTokenRepo hands out distinct pretty tokens so batch creates of more
// than one guest terminate (mockTokenRepo always returns the same token).
type uniqueTokenRepo struct{ mockTokenRepo }

func (m *uniqueTokenRepo) GeneratePrettyToken(uuid.UUID, int) (string, error) {
	return uuid.Must(uuid.NewV4()).String()[:8], nil
}

func TestCreateGuests_RejectsTableFromAnotherEvent(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &uniqueTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	t.Cleanup(func() { guestSvc = orig })
	eventID := uuid.Must(uuid.NewV4())
	foreignTable := models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4())}
	useGuestTables(t, foreignTable)

	body := `[{"first_name":"Ana","event_id":"` + eventID.String() + `"},{"first_name":"Bob","event_id":"` + eventID.String() + `","table_id":"` + foreignTable.ID.String() + `"}]`
	c, rec := newEchoCtx(http.MethodPost, "/guests/batch", body)
	setAuthzContext(t, c, eventID, nil)

	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestCreateGuests_InvalidJSONReturns400(t *testing.T) {
	c, rec := newEchoCtx(http.MethodPost, "/guests/batch", `[{"first_name":}]`)
	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
