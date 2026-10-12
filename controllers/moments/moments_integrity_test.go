package moments

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	momentsService "events-stocks/services/moments"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// createMomentFixture wires POST /moments with a recording repository and an
// in-memory set of guests and invitations for the reference checks.
type createMomentFixture struct {
	eventID     uuid.UUID
	created     []models.Moment
	guests      map[uuid.UUID]models.Guest
	invitations map[uuid.UUID]models.Invitation
}

func newCreateMomentFixture(t *testing.T) *createMomentFixture {
	t.Helper()
	f := &createMomentFixture{
		eventID:     uuid.Must(uuid.NewV4()),
		guests:      map[uuid.UUID]models.Guest{},
		invitations: map[uuid.UUID]models.Invitation{},
	}
	svc := momentsService.NewMomentService(&mockMomentRepo{
		CreateMomentFunc: func(moment *models.Moment) error {
			f.created = append(f.created, *moment)
			return nil
		},
	}, &mockCacheRepo{})
	origSvc, origGuest, origInvitation := momentSvc, momentGuestByID, momentInvitationByID
	momentSvc = svc
	momentGuestByID = func(id uuid.UUID) (*models.Guest, error) {
		if guest, ok := f.guests[id]; ok {
			return &guest, nil
		}
		return nil, gorm.ErrRecordNotFound
	}
	momentInvitationByID = func(id uuid.UUID) (*models.Invitation, error) {
		if invitation, ok := f.invitations[id]; ok {
			return &invitation, nil
		}
		return nil, gorm.ErrRecordNotFound
	}
	t.Cleanup(func() {
		momentSvc, momentGuestByID, momentInvitationByID = origSvc, origGuest, origInvitation
	})
	return f
}

func (f *createMomentFixture) post(t *testing.T, body string) (int, string) {
	t.Helper()
	c, rec := newEchoCtx(http.MethodPost, "/moments", body)
	setRootAuth(t, c)
	require.NoError(t, CreateMoment(c))
	return rec.Code, rec.Body.String()
}

func (f *createMomentFixture) event() string { return f.eventID.String() }

func assertErrorEnvelope(t *testing.T, code int, body string, message, errorCode string) {
	t.Helper()
	assert.Equal(t, http.StatusBadRequest, code, body)
	var envelope struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope), body)
	assert.Equal(t, message, envelope.Message)
	assert.Equal(t, errorCode, envelope.Error)
}

func TestCreateMoment_RejectsContentKeyOfAnotherEvent(t *testing.T) {
	f := newCreateMomentFixture(t)
	other := uuid.Must(uuid.NewV4()).String()

	code, body := f.post(t, `{"event_id":"`+f.event()+`","content_url":"moments/`+other+`/x.jpg"}`)

	assertErrorEnvelope(t, code, body, "Archivo inválido", "moment_content_outside_event")
	assert.Empty(t, f.created)
}

// Keys of the event itself, legacy or under an organization namespace, are
// accepted for both the content and the thumbnail.
func TestCreateMoment_AcceptsKeysOfTheSameEvent(t *testing.T) {
	org := uuid.Must(uuid.NewV4()).String()
	for _, key := range []string{
		"moments/{event}/photos/a.webp",
		"moments/{event}/raw/a.jpg",
		"organizations/" + org + "/moments/{event}/raw/a.jpg",
		"organizations/" + org + "/moments/{event}/videos/a-thumb.webp",
	} {
		t.Run(key, func(t *testing.T) {
			f := newCreateMomentFixture(t)
			resolved := replaceEvent(key, f.event())

			code, body := f.post(t, `{"event_id":"`+f.event()+`","content_url":"`+resolved+`","thumbnail_url":"`+resolved+`"}`)

			require.Equal(t, http.StatusCreated, code, body)
			require.Len(t, f.created, 1)
			assert.Equal(t, resolved, f.created[0].ContentURL)
			assert.Equal(t, resolved, f.created[0].ThumbnailURL)
		})
	}
}

func TestCreateMoment_RejectsKeysOutsideTheEvent(t *testing.T) {
	other := uuid.Must(uuid.NewV4()).String()
	org := uuid.Must(uuid.NewV4()).String()
	for _, key := range []string{
		"private/secret.jpg",
		"moments/" + other + "/photos/a.webp",
		"organizations/" + org + "/moments/" + other + "/raw/a.jpg",
		"resources/{event}/a.jpg",
		"events/{event}/covers/a.webp",
		"moments/{event}/",
		"moments/{event}",
		"/moments/{event}/raw/a.jpg",
		"moments/{event}/../" + other + "/a.jpg",
		`moments/{event}\raw\a.jpg`,
		"MOMENTS/{event}/raw/a.jpg",
		"tmp/moments/{event}/raw/a.jpg",
		"organizations/not-a-uuid/moments/{event}/raw/a.jpg",
		"organizations/{" + org + "}/moments/{event}/raw/a.jpg",
		"organizations/" + org + "/extra/moments/{event}/raw/a.jpg",
		"https://bucket.s3.amazonaws.com/moments/{event}/raw/a.jpg",
		"//cdn.example.com/moments/{event}/raw/a.jpg",
	} {
		for _, field := range []string{"content_url", "thumbnail_url"} {
			t.Run(field+"="+key, func(t *testing.T) {
				f := newCreateMomentFixture(t)
				raw, err := json.Marshal(replaceEvent(key, f.event()))
				require.NoError(t, err)

				code, body := f.post(t, `{"event_id":"`+f.event()+`","`+field+`":`+string(raw)+`}`)

				assertErrorEnvelope(t, code, body, "Archivo inválido", "moment_content_outside_event")
				assert.Empty(t, f.created)
			})
		}
	}
}

// Server-owned processing state, media variants and nested association
// objects in the body never reach the created row.
func TestCreateMoment_ClearsServerOwnedFields(t *testing.T) {
	f := newCreateMomentFixture(t)
	other := uuid.Must(uuid.NewV4()).String()
	body := `{"event_id":"` + f.event() + `","title":"Brindis","description":"Hola","content_type":"image/webp",
		"content_url":"moments/` + f.event() + `/photos/a.webp","is_approved":true,"order":3,
		"id":"` + other + `","processing_status":"done","processing_job_id":"job","processing_generation":7,
		"processing_input_key":"private/x.jpg","error_message":"boom","processing_duration_ms":5,
		"original_size_bytes":10,"optimized_size_bytes":5,"media_bucket":"other-bucket",
		"media_variants":[{"object_key":"private/x.webp","width":320,"format":"webp","bytes":1}],
		"guest":{"first_name":"Injected","event_id":"` + other + `"},
		"invitation":{"event_id":"` + other + `","max_guests":40},
		"moment_type":{"Name":"injected"}}`

	code, resp := f.post(t, body)

	require.Equal(t, http.StatusCreated, code, resp)
	require.Len(t, f.created, 1)
	got := f.created[0]
	assert.Equal(t, uuid.Nil, got.ID)
	assert.Equal(t, "Brindis", got.Title)
	assert.Equal(t, "Hola", got.Description)
	assert.Equal(t, "image/webp", got.ContentType)
	assert.True(t, got.IsApproved)
	assert.Equal(t, 3, got.Order)
	assert.Empty(t, got.ProcessingStatus)
	assert.Empty(t, got.ProcessingJobID)
	assert.Zero(t, got.ProcessingGeneration)
	assert.Empty(t, got.ProcessingInputKey)
	assert.Empty(t, got.ErrorMessage)
	assert.Zero(t, got.ProcessingDurationMs)
	assert.Zero(t, got.OriginalSizeBytes)
	assert.Zero(t, got.OptimizedSizeBytes)
	assert.Empty(t, got.MediaBucket)
	assert.Empty(t, got.MediaVariants)
	assert.Nil(t, got.Guest)
	assert.Equal(t, models.Invitation{}, got.Invitation)
	assert.Equal(t, models.MomentType{}, got.MomentType)
	assert.NotContains(t, resp, `"processing_status":"done"`)
}

func TestCreateMoment_RejectsGuestOfAnotherEvent(t *testing.T) {
	f := newCreateMomentFixture(t)
	foreignGuest := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4())}
	f.guests[foreignGuest.ID] = foreignGuest

	code, body := f.post(t, `{"event_id":"`+f.event()+`","guest_id":"`+foreignGuest.ID.String()+`"}`)

	assertErrorEnvelope(t, code, body, "Referencia inválida", "reference_outside_event")
	assert.Empty(t, f.created)
}

func TestCreateMoment_RejectsUnknownGuestOrInvitation(t *testing.T) {
	for _, field := range []string{"guest_id", "invitation_id"} {
		t.Run(field, func(t *testing.T) {
			f := newCreateMomentFixture(t)

			code, body := f.post(t, `{"event_id":"`+f.event()+`","`+field+`":"`+uuid.Must(uuid.NewV4()).String()+`"}`)

			assertErrorEnvelope(t, code, body, "Referencia inválida", "reference_outside_event")
			assert.Empty(t, f.created)
		})
	}
}

func TestCreateMoment_RejectsInvitationOfAnotherEvent(t *testing.T) {
	f := newCreateMomentFixture(t)
	foreignInvitation := models.Invitation{ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4())}
	f.invitations[foreignInvitation.ID] = foreignInvitation

	code, body := f.post(t, `{"event_id":"`+f.event()+`","invitation_id":"`+foreignInvitation.ID.String()+`"}`)

	assertErrorEnvelope(t, code, body, "Referencia inválida", "reference_outside_event")
	assert.Empty(t, f.created)
}

func TestCreateMoment_KeepsGuestAndInvitationOfTheSameEvent(t *testing.T) {
	f := newCreateMomentFixture(t)
	guest := models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.eventID}
	invitation := models.Invitation{ID: uuid.Must(uuid.NewV4()), EventID: f.eventID}
	f.guests[guest.ID] = guest
	f.invitations[invitation.ID] = invitation

	code, body := f.post(t, `{"event_id":"`+f.event()+`","guest_id":"`+guest.ID.String()+`","invitation_id":"`+invitation.ID.String()+`"}`)

	require.Equal(t, http.StatusCreated, code, body)
	require.Len(t, f.created, 1)
	require.NotNil(t, f.created[0].GuestID)
	require.NotNil(t, f.created[0].InvitationID)
	assert.Equal(t, guest.ID, *f.created[0].GuestID)
	assert.Equal(t, invitation.ID, *f.created[0].InvitationID)
}

// The nil UUID is "no reference", as an absent key is.
func TestCreateMoment_NilReferencesAreDropped(t *testing.T) {
	f := newCreateMomentFixture(t)

	code, body := f.post(t, `{"event_id":"`+f.event()+`","guest_id":"`+uuid.Nil.String()+`","invitation_id":"`+uuid.Nil.String()+`"}`)

	require.Equal(t, http.StatusCreated, code, body)
	require.Len(t, f.created, 1)
	assert.Nil(t, f.created[0].GuestID)
	assert.Nil(t, f.created[0].InvitationID)
}

// A caller without event:manage on the event gets the authorization denial
// before any reference is looked up.
func TestCreateMoment_DeniedCallerLooksUpNothing(t *testing.T) {
	f := newCreateMomentFixture(t)
	momentGuestByID = func(uuid.UUID) (*models.Guest, error) {
		t.Fatal("references must not be resolved for a denied caller")
		return nil, nil
	}
	c, rec := newEchoCtx(http.MethodPost, "/moments", `{"event_id":"`+f.event()+`","guest_id":"`+uuid.Must(uuid.NewV4()).String()+`"}`)
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) { return &models.User{ID: uuid.Must(uuid.NewV4())}, nil },
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			clientID := uuid.Must(uuid.NewV4())
			return &models.Event{ID: id, ClientID: &clientID}, nil
		},
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) { return false, "" },
		GetEventMemberRole:   func(uuid.UUID, uuid.UUID) (string, bool, error) { return "", false, nil },
	})
	t.Cleanup(restore)

	require.NoError(t, CreateMoment(c))

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Empty(t, f.created)
}

// PUT /moments/:id binds only title, description, approval and order: the
// media keys, processing state and references of the stored moment stay.
func TestUpdateMoment_IgnoresMediaKeysProcessingAndReferences(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	momentID := uuid.Must(uuid.NewV4())
	stored := models.Moment{
		ID: momentID, EventID: &eventID, GuestID: &guestID, Title: "Antes",
		ContentURL: "moments/" + eventID.String() + "/photos/a.webp", ProcessingStatus: "done",
	}
	var saved *models.Moment
	svc := momentsService.NewMomentService(&mockMomentRepo{
		UpdateMomentFunc: func(moment *models.Moment) error { copied := *moment; saved = &copied; return nil },
	}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	t.Cleanup(func() { momentSvc = orig })
	other := uuid.Must(uuid.NewV4()).String()
	c, rec := newEchoCtx(http.MethodPut, "/moments/"+momentID.String(), `{"title":"Después",
		"content_url":"private/x.jpg","thumbnail_url":"private/t.webp","processing_status":"pending",
		"processing_input_key":"private/x.jpg","guest_id":"`+other+`","invitation_id":"`+other+`","event_id":"`+other+`",
		"media_variants":[{"object_key":"private/x.webp","width":320,"format":"webp"}]}`)
	c.SetParamNames("id")
	c.SetParamValues(momentID.String())
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) { return &models.Event{ID: id}, nil },
		GetMomentByID: func(uuid.UUID) (*models.Moment, error) {
			copied := stored
			return &copied, nil
		},
	})
	t.Cleanup(restore)

	require.NoError(t, UpdateMoment(c))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, saved)
	assert.Equal(t, "Después", saved.Title)
	assert.Equal(t, stored.ContentURL, saved.ContentURL)
	assert.Empty(t, saved.ThumbnailURL)
	assert.Equal(t, "done", saved.ProcessingStatus)
	assert.Empty(t, saved.ProcessingInputKey)
	assert.Equal(t, &eventID, saved.EventID)
	assert.Equal(t, &guestID, saved.GuestID)
	assert.Nil(t, saved.InvitationID)
	assert.Empty(t, saved.MediaVariants)
}

func replaceEvent(key, eventID string) string {
	return strings.ReplaceAll(key, "{event}", eventID)
}
