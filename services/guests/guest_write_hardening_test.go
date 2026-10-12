package guests

import (
	"strings"
	"testing"

	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// dryRunCreateRecorder returns a transactor that runs the transaction body on a
// DryRun GORM session (no SQL reaches a database) and records the table of
// every INSERT GORM builds, associations included.
func dryRunCreateRecorder(t *testing.T) (*mockTransactor, *[]string) {
	t.Helper()
	conn, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: conn}), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		DryRun:                 true,
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)

	var tables []string
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:record_insert", func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "INSERT INTO") {
			tables = append(tables, tx.Statement.Table)
		}
	}))
	return &mockTransactor{TransactionFunc: func(fn func(tx *gorm.DB) error) error { return fn(db) }}, &tables
}

func TestCreateGuestsWithInvitations_DoesNotInsertNestedAssociations(t *testing.T) {
	tx, tables := dryRunCreateRecorder(t)
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(uuid.UUID, int) (string, error) { return uuid.Must(uuid.NewV4()).String()[:8], nil },
	}
	svc := newGuestService(&mockGuestRepo{}, accessToken, nil, tx)
	eventID := uuid.Must(uuid.NewV4())
	otherEventID := uuid.Must(uuid.NewV4())
	tableID := uuid.Must(uuid.NewV4())
	statusID := uuid.Must(uuid.NewV4())
	guests := []models.Guest{{
		EventID: eventID, FirstName: "Ana", GuestStatusID: statusID,
		Table:       &models.EventTable{ID: tableID, EventID: otherEventID, Name: "Injected"},
		GuestStatus: models.GuestStatus{ID: statusID, Code: "vip", Label: "VIP"},
	}}

	require.NoError(t, svc.CreateGuestsWithInvitations(guests))

	assert.Contains(t, *tables, "guests", "the guest insert must still be built")
	assert.NotContains(t, *tables, "tables")
	assert.NotContains(t, *tables, "guest_statuses")
}

func TestUpdateGuest_MaxGuestsSyncIgnoresInvitationIDOnUpdatedObject(t *testing.T) {
	storedInvitationID := uuid.Must(uuid.NewV4())
	foreignInvitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, InvitationID: &storedInvitationID}, nil
		},
	}
	var lookedUp []uuid.UUID
	invitations := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			lookedUp = append(lookedUp, id)
			return &models.Invitation{ID: id, EventID: eventID, MaxGuests: 1}, nil
		},
		UpdateInvitationFunc: func(*models.Invitation) error { return nil },
	}
	svc := newGuestService(repo, nil, nil, nil, invitations)

	err := svc.UpdateGuest(&models.Guest{
		ID: uuid.Must(uuid.NewV4()), EventID: eventID, FirstName: "Ana", InvitationID: &foreignInvitationID, MaxGuests: 5,
	})

	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{storedInvitationID}, lookedUp)
}

// A guest whose stored invitation belongs to another event (a cross-event link
// left by the old PUT) never syncs max_guests into that invitation; the guest
// row itself is still saved.
func TestUpdateGuest_MaxGuestsSyncSkipsInvitationOfAnotherEvent(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	foreignInvitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	guestSaved := false
	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, InvitationID: &foreignInvitationID, MaxGuests: 1}, nil
		},
		UpdateGuestFunc: func(*models.Guest) error { guestSaved = true; return nil },
	}
	invitations := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: id, EventID: uuid.Must(uuid.NewV4()), MaxGuests: 2}, nil
		},
		UpdateInvitationFunc: func(*models.Invitation) error {
			t.Fatal("an invitation of another event must not be written")
			return nil
		},
	}
	svc := newGuestService(repo, nil, nil, nil, invitations)

	err := svc.UpdateGuest(&models.Guest{ID: guestID, EventID: eventID, FirstName: "Ana", InvitationID: &foreignInvitationID, MaxGuests: 9})

	require.NoError(t, err)
	assert.True(t, guestSaved)
}

// The regression twin: the guest's own invitation still follows max_guests.
func TestUpdateGuest_MaxGuestsSyncUpdatesInvitationOfSameEvent(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	repo := &mockGuestRepo{
		GetGuestByIDFunc: func(id uuid.UUID) (*models.Guest, error) {
			return &models.Guest{ID: id, EventID: eventID, InvitationID: &invitationID, MaxGuests: 1}, nil
		},
	}
	var written []models.Invitation
	invitations := &mockInvitationRepo{
		GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
			return &models.Invitation{ID: id, EventID: eventID, MaxGuests: 1}, nil
		},
		UpdateInvitationFunc: func(invitation *models.Invitation) error { written = append(written, *invitation); return nil },
	}
	svc := newGuestService(repo, nil, nil, nil, invitations)

	require.NoError(t, svc.UpdateGuest(&models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, FirstName: "Ana", MaxGuests: 4}))

	require.Len(t, written, 1)
	assert.Equal(t, invitationID, written[0].ID)
	assert.Equal(t, 4, written[0].MaxGuests)
}

// CreateGuestsWithInvitations owns both references: every guest gets its new
// invitation and no RSVP token id from the caller.
func TestCreateGuestsWithInvitations_ClearsCallerReferences(t *testing.T) {
	accessToken := &mockAccessTokenRepo{
		GeneratePrettyTokenFunc: func(uuid.UUID, int) (string, error) { return uuid.Must(uuid.NewV4()).String()[:8], nil },
	}
	svc := newGuestService(&mockGuestRepo{}, accessToken, nil, nil)
	foreign := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	guests := []models.Guest{
		{EventID: eventID, FirstName: "Ana", InvitationID: &foreign, RSVPTokenID: &foreign},
		{EventID: eventID, FirstName: "Bob", RSVPTokenID: &foreign},
	}

	require.NoError(t, svc.CreateGuestsWithInvitations(guests))

	for _, guest := range guests {
		require.NotNil(t, guest.InvitationID, guest.FirstName)
		assert.NotEqual(t, foreign, *guest.InvitationID, guest.FirstName)
		assert.Nil(t, guest.RSVPTokenID, guest.FirstName)
	}
}
