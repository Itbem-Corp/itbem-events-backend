package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	eventsService "events-stocks/services/events"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type apiErrorBody struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

func decodeAPIError(t *testing.T, raw []byte) apiErrorBody {
	t.Helper()
	var body apiErrorBody
	require.NoError(t, json.Unmarshal(raw, &body))
	return body
}

// setRootAuthWithEvent is setRootAuth with a stored event the update handlers
// can validate (name set, linked to clientID).
func setRootAuthWithEvent(t *testing.T, c echoContextSetter, clientID *uuid.UUID) {
	t.Helper()
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: id, Name: "Evento", ClientID: clientID, IsActive: true}, nil
		},
	})
	t.Cleanup(restore)
}

type echoContextSetter interface{ Set(string, interface{}) }

func useEventService(t *testing.T, repo *mockEventsRepo) {
	t.Helper()
	orig := eventSvc
	eventSvc = eventsService.NewEventService(repo, &mockCacheRepo{})
	t.Cleanup(func() { eventSvc = orig })
}

// EVT-2 / SET-15: a name taken inside the organization is the caller's
// conflict, not a server error.
func TestCreateEvent_DuplicateNameReturns409WithSpanishMessage(t *testing.T) {
	useEventService(t, &mockEventsRepo{
		CreateEventFunc: func(event *models.Event) error {
			return errors.New(`ERROR: duplicate key value violates unique constraint "idx_events_client_name_live" (SQLSTATE 23505)`)
		},
	})
	clientID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPost, "/events", `{"name":"Boda Ana","client_id":"`+clientID.String()+`"}`)
	setRootAuth(t, c)

	require.NoError(t, CreateEvent(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	body := decodeAPIError(t, rec.Body.Bytes())
	assert.Equal(t, eventErrorNameTaken, body.Error)
	assert.Equal(t, "Ya existe un evento con ese nombre en esta organización.", body.Message)
}

// EVT-5 / EVT-6: an identifier conflict on update reached the client as a
// 500 "record not found"; it is a 409 now.
func TestUpdateEvent_DuplicateIdentifierReturns409(t *testing.T) {
	useEventService(t, &mockEventsRepo{
		UpdateEventFunc: func(event *models.Event) error {
			return errors.New(`ERROR: duplicate key value violates unique constraint "idx_events_identifier_live" (SQLSTATE 23505)`)
		},
	})
	eventID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/events/"+eventID.String(), `{"identifier":"boda-ana"}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuthWithEvent(t, c, nil)

	require.NoError(t, UpdateEvent(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	body := decodeAPIError(t, rec.Body.Bytes())
	assert.Equal(t, eventErrorIdentifierTaken, body.Error)
	assert.Equal(t, "Esa URL del evento ya está en uso. Elige otra.", body.Message)
}

func TestUpdateEvent_OtherDatabaseErrorsStay500(t *testing.T) {
	useEventService(t, &mockEventsRepo{
		UpdateEventFunc: func(event *models.Event) error { return errors.New("connection reset") },
	})
	eventID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/events/"+eventID.String(), `{"name":"X"}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuthWithEvent(t, c, nil)

	require.NoError(t, UpdateEvent(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestCreateAndUpdateEvent_InvalidTimezoneReturns400(t *testing.T) {
	useEventService(t, &mockEventsRepo{
		CreateEventFunc: func(event *models.Event) error { t.Fatal("must not store"); return nil },
		UpdateEventFunc: func(event *models.Event) error { t.Fatal("must not store"); return nil },
	})
	clientID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPost, "/events", `{"name":"X","client_id":"`+clientID.String()+`","timezone":"Foo/Bar"}`)
	setRootAuth(t, c)
	require.NoError(t, CreateEvent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	body := decodeAPIError(t, rec.Body.Bytes())
	assert.Equal(t, "Zona horaria inválida", body.Message)
	assert.Equal(t, eventErrorInvalidTimezone, body.Error)

	eventID := uuid.Must(uuid.NewV4())
	c, rec = newEchoCtx(http.MethodPut, "/events/"+eventID.String(), `{"timezone":"Mars/Base"}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuthWithEvent(t, c, nil)
	require.NoError(t, UpdateEvent(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, eventErrorInvalidTimezone, decodeAPIError(t, rec.Body.Bytes()).Error)
}

// EVT-4: client_id null on update must keep the organization.
func TestUpdateEvent_NullClientKeepsTheOrganization(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	var saved *models.Event
	useEventService(t, &mockEventsRepo{
		UpdateEventFunc: func(event *models.Event) error { saved = event; return nil },
	})
	eventID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/events/"+eventID.String(), `{"name":"Renombrado","client_id":null}`)
	c.SetParamNames("id")
	c.SetParamValues(eventID.String())
	setRootAuthWithEvent(t, c, &clientID)

	require.NoError(t, UpdateEvent(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, saved)
	require.NotNil(t, saved.ClientID)
	assert.Equal(t, clientID, *saved.ClientID)
	assert.Equal(t, "Renombrado", saved.Name)
}

// Review S4: a vanished row is matched by sentinel (gorm.ErrRecordNotFound
// from gormrepository.Update), not by its message.
func TestRespondEventWriteErrorMapsRecordNotFoundSentinel(t *testing.T) {
	c, rec := newEchoCtx(http.MethodPut, "/api/events/x", "")
	require.NoError(t, respondEventWriteError(c, fmt.Errorf("update event: %w", gorm.ErrRecordNotFound), "Error updating event"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
