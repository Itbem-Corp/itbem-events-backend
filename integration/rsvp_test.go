//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"events-stocks/configuration"
	guestsCtrl "events-stocks/controllers/guests"
	invitationsCtrl "events-stocks/controllers/invitations"
	momentsCtrl "events-stocks/controllers/moments"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	"events-stocks/repositories/eventtablerepository"
	"events-stocks/repositories/gormrepository"
	guestrepository "events-stocks/repositories/guestrepository"
	invitationaccesstokenrepository "events-stocks/repositories/invitationaccesstokenrepository"
	invitationlogrepository "events-stocks/repositories/invitationlogrepository"
	invitationrepository "events-stocks/repositories/invitationrepository"
	momentrepository "events-stocks/repositories/momentrepository"
	redisrepository "events-stocks/repositories/redisrepository"
	"events-stocks/seeds"
	guestsSvc "events-stocks/services/guests"
	invitationsSvc "events-stocks/services/invitations"
	momentsSvc "events-stocks/services/moments"
)

var testPostgresConnectionString string

// dockerAvailable returns true if the Docker daemon is reachable.
// On Windows with WSL2, enable Docker Desktop → Settings → Resources → WSL Integration → Ubuntu.
func dockerAvailable() bool {
	return exec.Command("docker", "info").Run() == nil
}

// TestMain spins up a real PostgreSQL container and a miniredis instance once
// for the entire integration test package. All tests share this infrastructure.
//
// Prerequisites:
//   - Docker daemon running and accessible from WSL
//   - Docker Desktop: Settings → Resources → WSL Integration → enable for Ubuntu
func TestMain(m *testing.M) {
	os.Exit(runIntegrationTests(m))
}

// runIntegrationTests owns the test-only infrastructure lifecycle. Keep this
// separate from TestMain so cleanup defers run before TestMain calls os.Exit.
func runIntegrationTests(m *testing.M) (exitCode int) {
	if !dockerAvailable() {
		fmt.Println("SKIP: Docker not available — enable WSL integration in Docker Desktop")
		return 0
	}

	ctx := context.Background()

	// ── PostgreSQL container ───────────────────────────────────────────────────
	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Println("ERROR: could not start disposable PostgreSQL test container")
		return 1
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := pgContainer.Terminate(cleanupCtx); err != nil {
			fmt.Println("ERROR: could not terminate disposable PostgreSQL test container")
			exitCode = 1
		}
	}()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Println("ERROR: could not obtain disposable PostgreSQL test connection")
		return 1
	}
	testPostgresConnectionString = connStr
	defer func() { testPostgresConnectionString = "" }()

	db, err := gorm.Open(gormpostgres.Open(connStr), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		fmt.Println("ERROR: could not open disposable PostgreSQL test database")
		return 1
	}
	configuration.DB = db
	defer func() { configuration.DB = nil }()
	rawDB, err := db.DB()
	if err != nil {
		fmt.Println("ERROR: could not acquire disposable PostgreSQL test connection pool")
		return 1
	}
	defer func() {
		if err := rawDB.Close(); err != nil {
			fmt.Println("ERROR: could not close disposable PostgreSQL test connection pool")
			exitCode = 1
		}
	}()

	// uuid_generate_v4() is used as the default for UUID primary keys
	if err := configuration.DB.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error; err != nil {
		fmt.Println("ERROR: could not prepare disposable PostgreSQL test database")
		return 1
	}

	// Migrate through the same ordered, transactional path used by production.
	// A single variadic AutoMigrate call lets GORM infer associations in an
	// unstable order on a fresh schema (notably DeliveryWorkItem and its
	// automation relation).
	if err := configuration.MigrateModelsForTest(configuration.DB); err != nil {
		fmt.Println("ERROR: could not migrate disposable PostgreSQL test database")
		return 1
	}

	// Seed GuestStatus reference data (pending / confirmed / declined)
	seeds.SeedGuestStatus(configuration.DB)

	// ── Miniredis (in-memory Redis, no container needed) ──────────────────────
	mr, err := miniredis.Run()
	if err != nil {
		fmt.Println("ERROR: could not start in-memory Redis test service")
		return 1
	}
	defer mr.Close()
	configuration.RedisClient = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() {
		if err := configuration.RedisClient.Close(); err != nil {
			fmt.Println("ERROR: could not close Redis test client")
			exitCode = 1
		}
		configuration.RedisClient = nil
	}()
	momentsSvc.SetDefaultMomentService(momentsSvc.NewMomentService(momentrepository.NewMomentRepo(), redisrepository.NewRedisRepo()))
	defer momentsSvc.SetDefaultMomentService(nil)

	return m.Run()
}

// TestRSVP_HappyPath_Confirmed validates the full RSVP happy path at the HTTP layer:
// guest sends a confirmed RSVP → DB is updated → invitation log is created.
func TestRSVP_HappyPath_Confirmed(t *testing.T) {
	db := configuration.DB

	// Unique suffix so uniqueIndex fields don't collide across runs
	suffix := uuid.Must(uuid.NewV4()).String()[:8]

	// ── Fixture setup ─────────────────────────────────────────────────────────

	// GuestStatus "pending" was seeded in TestMain
	var pendingStatus models.GuestStatus
	require.NoError(t, db.Where("code = ?", "pending").First(&pendingStatus).Error)

	// EventType
	eventType := models.EventType{
		ID:        uuid.Must(uuid.NewV4()),
		Name:      "wedding-" + suffix,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Create(&eventType).Error)

	// Event
	event := models.Event{
		ID:          uuid.Must(uuid.NewV4()),
		Name:        "Test Wedding " + suffix,
		Identifier:  "test-wedding-" + suffix,
		EventTypeID: &eventType.ID,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, db.Create(&event).Error)

	// Invitation
	invitationID := uuid.Must(uuid.NewV4())
	invitation := models.Invitation{
		ID:        invitationID,
		EventID:   event.ID,
		MaxGuests: 5,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Create(&invitation).Error)

	// InvitationAccessToken (PrettyToken is what the RSVP endpoint uses)
	prettyToken := "RSVP-" + suffix
	accessToken := models.InvitationAccessToken{
		ID:           uuid.Must(uuid.NewV4()),
		InvitationID: invitationID,
		Token:        uuid.Must(uuid.NewV4()).String(), // internal UUID token
		PrettyToken:  prettyToken,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, db.Create(&accessToken).Error)

	// Guest
	guest := models.Guest{
		ID:            uuid.Must(uuid.NewV4()),
		EventID:       event.ID,
		InvitationID:  &invitationID,
		FirstName:     "Ana",
		LastName:      "García",
		GuestStatusID: pendingStatus.ID,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	require.NoError(t, db.Create(&guest).Error)

	// Cleanup after the test (reverse insertion order)
	t.Cleanup(func() {
		db.Unscoped().Where("invitation_id = ? AND channel = ?", invitationID, "rsvp").Delete(&models.InvitationLog{})
		db.Unscoped().Where("id = ?", guest.ID).Delete(&models.Guest{})
		db.Unscoped().Where("id = ?", accessToken.ID).Delete(&models.InvitationAccessToken{})
		db.Unscoped().Where("id = ?", invitationID).Delete(&models.Invitation{})
		db.Unscoped().Where("id = ?", event.ID).Delete(&models.Event{})
		db.Unscoped().Where("id = ?", eventType.ID).Delete(&models.EventType{})
	})

	// ── Wire minimal Echo server for RSVP (no auth, no global middleware) ─────
	invRepo := invitationrepository.NewInvitationRepo()
	tokenRepo := invitationaccesstokenrepository.NewAccessTokenRepo()
	logRepo := invitationlogrepository.NewInvitationLogRepo()
	guestRepo := guestrepository.NewGuestRepo()
	redisRepo := redisrepository.NewRedisRepo()
	svc := invitationsSvc.NewInvitationService(invRepo, guestRepo, tokenRepo, logRepo, redisRepo)
	invitationsCtrl.InitInvitationsController(svc)

	e := echo.New()
	e.HideBanner = true
	e.Validator = customValidator.New()
	e.POST("/api/invitations/rsvp", invitationsCtrl.ConfirmRSVP)

	// ── HTTP request ──────────────────────────────────────────────────────────
	body := `{"pretty_token":"` + prettyToken + `","status":"confirmed","guest_count":2}`
	req := httptest.NewRequest(http.MethodPost, "/api/invitations/rsvp", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// ── Assert HTTP response ──────────────────────────────────────────────────
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, float64(http.StatusOK), resp["status"])
	assert.Equal(t, "RSVP confirmed", resp["message"])

	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok, "data field missing or wrong type in: %s", rec.Body.String())
	guestData, ok := data["guest"].(map[string]interface{})
	require.True(t, ok, "guest field missing or wrong type in: %s", rec.Body.String())
	assert.Equal(t, "confirmed", guestData["rsvp_status"])
	assert.Equal(t, float64(2), guestData["rsvp_guest_count"]) // JSON numbers decode as float64
	assert.NotEmpty(t, guestData["rsvp_at"])

	// ── Assert DB state ───────────────────────────────────────────────────────
	var updated models.Guest
	require.NoError(t, db.Where("id = ?", guest.ID).First(&updated).Error)
	assert.Equal(t, "confirmed", updated.RSVPStatus)
	assert.Equal(t, 2, updated.RSVPGuestCount)
	assert.Equal(t, "web", updated.RSVPMethod) // controller defaults to "web"
	assert.NotNil(t, updated.RSVPAt)
	assert.NotNil(t, updated.RSVPTokenID)

	var logCount int64
	db.Model(&models.InvitationLog{}).
		Where("invitation_id = ? AND channel = ?", invitationID, "rsvp").
		Count(&logCount)
	assert.Equal(t, int64(1), logCount, "expected 1 invitation_log row for the RSVP")
}

// doorCheckinFixture is an event with a pending guest, a guest who confirmed
// online, and a guest of another event.
type doorCheckinFixture struct {
	event, otherEvent         models.Event
	pending, confirmed        models.GuestStatus
	door, selfConfirmed, away models.Guest
	answeredAt                time.Time
}

func newDoorCheckinFixture(t *testing.T) doorCheckinFixture {
	t.Helper()
	db := configuration.DB
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	var f doorCheckinFixture
	require.NoError(t, db.Where("code = ?", "pending").First(&f.pending).Error)
	require.NoError(t, db.Where("code = ?", "confirmed").First(&f.confirmed).Error)
	eventType := models.EventType{ID: uuid.Must(uuid.NewV4()), Name: "checkin-" + suffix, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, db.Create(&eventType).Error)
	f.event = models.Event{ID: uuid.Must(uuid.NewV4()), Name: "Door " + suffix, Identifier: "door-" + suffix, EventTypeID: eventType.ID, IsActive: true}
	f.otherEvent = models.Event{ID: uuid.Must(uuid.NewV4()), Name: "Other " + suffix, Identifier: "other-" + suffix, EventTypeID: eventType.ID, IsActive: true}
	require.NoError(t, db.Create(&f.event).Error)
	require.NoError(t, db.Create(&f.otherEvent).Error)
	f.answeredAt = time.Date(2026, time.April, 2, 18, 30, 0, 0, time.UTC)
	f.door = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, FirstName: "Door", RSVPStatus: "pending", GuestStatusID: f.pending.ID}
	f.selfConfirmed = models.Guest{
		ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, FirstName: "Self", RSVPStatus: "confirmed", RSVPMethod: "web",
		RSVPAt: &f.answeredAt, GuestStatusID: f.pending.ID,
	}
	f.away = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.otherEvent.ID, FirstName: "Away", RSVPStatus: "pending", GuestStatusID: f.pending.ID}
	for _, guest := range []*models.Guest{&f.door, &f.selfConfirmed, &f.away} {
		require.NoError(t, db.Omit("Event", "Table", "Invitation", "GuestStatus").Create(guest).Error)
	}
	t.Cleanup(func() {
		db.Unscoped().Where("id IN ?", []uuid.UUID{f.door.ID, f.selfConfirmed.ID, f.away.ID}).Delete(&models.Guest{})
		db.Unscoped().Where("id IN ?", []uuid.UUID{f.event.ID, f.otherEvent.ID}).Delete(&models.Event{})
		db.Unscoped().Where("id = ?", eventType.ID).Delete(&models.EventType{})
	})
	return f
}

func reloadGuest(t *testing.T, id uuid.UUID) models.Guest {
	t.Helper()
	var guest models.Guest
	require.NoError(t, configuration.DB.Where("id = ?", id).First(&guest).Error)
	return guest
}

func assertNoDoorCheckinColumns(t *testing.T, guest models.Guest) {
	t.Helper()
	assert.Nil(t, guest.CheckedInAt)
	assert.Nil(t, guest.CheckedInByUserID)
	assert.Nil(t, guest.CheckinPreviousRSVPStatus)
	assert.Nil(t, guest.CheckinPreviousGuestStatusID)
	assert.Nil(t, guest.CheckinPreviousRSVPMethod)
	assert.Nil(t, guest.CheckinPreviousRSVPAt)
}

// undoConcurrently runs n undos of ids at once and counts successes and
// ErrCheckinNotUndoable refusals; any other error fails the test.
func undoConcurrently(t *testing.T, svc *guestsSvc.GuestService, eventID uuid.UUID, ids []uuid.UUID, n int) (succeeded, refused int) {
	t.Helper()
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.UndoGuestCheckins(eventID, ids)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, guestsSvc.ErrCheckinNotUndoable):
			refused++
		default:
			t.Errorf("unexpected undo error: %v", err)
		}
	}
	return succeeded, refused
}

// TestDoorCheckin_UndoRestoresThePreviousRSVP runs the door check-in and its
// undo through guestrepository.TransitionGuestStatuses on Postgres: the row
// locks, the provenance columns, concurrent undos and the event boundary.
func TestDoorCheckin_UndoRestoresThePreviousRSVP(t *testing.T) {
	f := newDoorCheckinFixture(t)
	svc := guestsSvc.NewGuestService(guestrepository.NewGuestRepo(), invitationaccesstokenrepository.NewAccessTokenRepo(), nil, nil)
	staff := uuid.Must(uuid.NewV4())
	both := []uuid.UUID{f.door.ID, f.selfConfirmed.ID}

	require.NoError(t, svc.CheckInGuests(f.event.ID, both, f.confirmed.ID, staff))

	door := reloadGuest(t, f.door.ID)
	assert.Equal(t, "confirmed", door.RSVPStatus)
	assert.Equal(t, "host", door.RSVPMethod)
	assert.Equal(t, f.confirmed.ID, door.GuestStatusID)
	require.NotNil(t, door.RSVPAt)
	require.NotNil(t, door.CheckedInAt)
	require.NotNil(t, door.CheckedInByUserID)
	assert.Equal(t, staff, *door.CheckedInByUserID)
	require.NotNil(t, door.CheckinPreviousRSVPStatus)
	assert.Equal(t, "pending", *door.CheckinPreviousRSVPStatus)
	require.NotNil(t, door.CheckinPreviousRSVPMethod)
	assert.Equal(t, "", *door.CheckinPreviousRSVPMethod)
	require.NotNil(t, door.CheckinPreviousGuestStatusID)
	assert.Equal(t, f.pending.ID, *door.CheckinPreviousGuestStatusID)
	assert.Nil(t, door.CheckinPreviousRSVPAt)

	self := reloadGuest(t, f.selfConfirmed.ID)
	assert.Equal(t, "host", self.RSVPMethod)
	require.NotNil(t, self.RSVPAt)
	assert.WithinDuration(t, f.answeredAt, *self.RSVPAt, time.Millisecond)
	require.NotNil(t, self.CheckinPreviousRSVPAt)
	assert.WithinDuration(t, f.answeredAt, *self.CheckinPreviousRSVPAt, time.Millisecond)

	// A repeated check-in keeps the first provenance.
	require.NoError(t, svc.CheckInGuests(f.event.ID, both, f.confirmed.ID, uuid.Must(uuid.NewV4())))
	again := reloadGuest(t, f.door.ID)
	assert.Equal(t, door.CheckedInAt.UnixMicro(), again.CheckedInAt.UnixMicro())
	assert.Equal(t, staff, *again.CheckedInByUserID)

	// A guest of another event fails the whole request and writes nothing.
	require.Error(t, svc.UndoGuestCheckins(f.event.ID, []uuid.UUID{f.door.ID, f.away.ID}))
	require.Error(t, svc.CheckInGuests(f.event.ID, []uuid.UUID{f.away.ID}, f.confirmed.ID, staff))
	assert.NotNil(t, reloadGuest(t, f.door.ID).CheckedInAt)
	assert.Equal(t, "pending", reloadGuest(t, f.away.ID).RSVPStatus)

	// Concurrent undos of the pending guest: every call succeeds, one restores.
	succeeded, refused := undoConcurrently(t, svc, f.event.ID, []uuid.UUID{f.door.ID}, 6)
	assert.Equal(t, 6, succeeded)
	assert.Zero(t, refused)
	door = reloadGuest(t, f.door.ID)
	assert.Equal(t, "pending", door.RSVPStatus)
	assert.Equal(t, "", door.RSVPMethod)
	assert.Equal(t, f.pending.ID, door.GuestStatusID)
	assert.Nil(t, door.RSVPAt)
	assertNoDoorCheckinColumns(t, door)

	// Concurrent undos of the self-confirmed guest: one gives the guest's own
	// answer back, the rest are refused because that answer is not a check-in.
	succeeded, refused = undoConcurrently(t, svc, f.event.ID, []uuid.UUID{f.selfConfirmed.ID}, 6)
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 5, refused)
	self = reloadGuest(t, f.selfConfirmed.ID)
	assert.Equal(t, "confirmed", self.RSVPStatus)
	assert.Equal(t, "web", self.RSVPMethod)
	assert.Equal(t, f.pending.ID, self.GuestStatusID)
	require.NotNil(t, self.RSVPAt)
	assert.WithinDuration(t, f.answeredAt, *self.RSVPAt, time.Millisecond)
	assertNoDoorCheckinColumns(t, self)
}

// TestDoorCheckin_ProvenanceColumnsAreAddedToAnExistingGuestsTable replays
// the deploy: AutoMigrate adds the nullable columns to a guests table that
// already has rows, without rewriting them.
func TestDoorCheckin_ProvenanceColumnsAreAddedToAnExistingGuestsTable(t *testing.T) {
	db := configuration.DB
	f := newDoorCheckinFixture(t)
	columns := []string{
		"checked_in_at", "checked_in_by_user_id", "checkin_previous_rsvp_status",
		"checkin_previous_guest_status_id", "checkin_previous_rsvp_method", "checkin_previous_rsvp_at",
	}
	t.Cleanup(func() { _ = db.AutoMigrate(&models.Guest{}) })
	for _, column := range columns {
		require.NoError(t, db.Migrator().DropColumn(&models.Guest{}, column))
	}

	require.NoError(t, db.AutoMigrate(&models.Guest{}))

	for _, column := range columns {
		var nullable string
		require.NoError(t, db.Raw(
			"SELECT is_nullable FROM information_schema.columns WHERE table_name = 'guests' AND column_name = ?", column,
		).Scan(&nullable).Error)
		assert.Equal(t, "YES", nullable, column)
	}
	self := reloadGuest(t, f.selfConfirmed.ID)
	assert.Equal(t, "Self", self.FirstName)
	assert.Equal(t, "confirmed", self.RSVPStatus)
	assert.Equal(t, "web", self.RSVPMethod)
	assertNoDoorCheckinColumns(t, self)
}

// referenceIntegrityFixture is two events, each with a guest, an invitation
// with its token and a table. Event A also has a soft-deleted table and a
// legacy guest still linked to event B's invitation (the shape the old PUT
// could leave behind).
type referenceIntegrityFixture struct {
	eventA, eventB           models.Event
	invitationA, invitationB models.Invitation
	tokenA, tokenB           models.InvitationAccessToken
	tableA, tableB, deletedA models.EventTable
	guestA, guestB, legacy   models.Guest
}

func newReferenceIntegrityFixture(t *testing.T) referenceIntegrityFixture {
	t.Helper()
	db := configuration.DB
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	var pending models.GuestStatus
	require.NoError(t, db.Where("code = ?", "pending").First(&pending).Error)
	eventType := models.EventType{ID: uuid.Must(uuid.NewV4()), Name: "integrity-" + suffix, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, db.Create(&eventType).Error)

	var f referenceIntegrityFixture
	f.eventA = models.Event{ID: uuid.Must(uuid.NewV4()), Name: "A " + suffix, Identifier: "integrity-a-" + suffix, EventTypeID: eventType.ID, IsActive: true}
	f.eventB = models.Event{ID: uuid.Must(uuid.NewV4()), Name: "B " + suffix, Identifier: "integrity-b-" + suffix, EventTypeID: eventType.ID, IsActive: true}
	require.NoError(t, db.Create(&f.eventA).Error)
	require.NoError(t, db.Create(&f.eventB).Error)
	f.invitationA = models.Invitation{ID: uuid.Must(uuid.NewV4()), EventID: f.eventA.ID, MaxGuests: 1}
	f.invitationB = models.Invitation{ID: uuid.Must(uuid.NewV4()), EventID: f.eventB.ID, MaxGuests: 2}
	require.NoError(t, db.Create(&f.invitationA).Error)
	require.NoError(t, db.Create(&f.invitationB).Error)
	f.tokenA = models.InvitationAccessToken{ID: uuid.Must(uuid.NewV4()), InvitationID: f.invitationA.ID, Token: uuid.Must(uuid.NewV4()).String(), PrettyToken: "IA-" + suffix}
	f.tokenB = models.InvitationAccessToken{ID: uuid.Must(uuid.NewV4()), InvitationID: f.invitationB.ID, Token: uuid.Must(uuid.NewV4()).String(), PrettyToken: "IB-" + suffix}
	require.NoError(t, db.Create(&f.tokenA).Error)
	require.NoError(t, db.Create(&f.tokenB).Error)
	f.tableA = models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.eventA.ID, Name: "Mesa A", Capacity: 8}
	f.tableB = models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.eventB.ID, Name: "Mesa B", Capacity: 8}
	f.deletedA = models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.eventA.ID, Name: "Mesa borrada", Capacity: 8}
	for _, table := range []*models.EventTable{&f.tableA, &f.tableB, &f.deletedA} {
		require.NoError(t, db.Omit("Event").Create(table).Error)
	}
	require.NoError(t, db.Delete(&models.EventTable{}, "id = ?", f.deletedA.ID).Error)
	f.guestA = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.eventA.ID, FirstName: "Ana", InvitationID: &f.invitationA.ID,
		RSVPTokenID: &f.tokenA.ID, TableID: &f.tableA.ID, MaxGuests: 1, GuestStatusID: pending.ID}
	f.guestB = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.eventB.ID, FirstName: "Beto", InvitationID: &f.invitationB.ID,
		RSVPTokenID: &f.tokenB.ID, TableID: &f.tableB.ID, MaxGuests: 2, GuestStatusID: pending.ID}
	f.legacy = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.eventA.ID, FirstName: "Legado", InvitationID: &f.invitationB.ID,
		MaxGuests: 2, GuestStatusID: pending.ID}
	for _, guest := range []*models.Guest{&f.guestA, &f.guestB, &f.legacy} {
		require.NoError(t, db.Omit("Event", "Table", "Invitation", "GuestStatus").Create(guest).Error)
	}

	events := []uuid.UUID{f.eventA.ID, f.eventB.ID}
	t.Cleanup(func() {
		db.Unscoped().Where("event_id IN ?", events).Delete(&models.Moment{})
		invitations := db.Model(&models.Invitation{}).Select("id").Where("event_id IN ?", events)
		db.Unscoped().Where("invitation_id IN (?)", invitations).Delete(&models.InvitationLog{})
		db.Unscoped().Where("invitation_id IN (?)", invitations).Delete(&models.InvitationAccessToken{})
		db.Unscoped().Where("event_id IN ?", events).Delete(&models.Guest{})
		db.Unscoped().Where("event_id IN ?", events).Delete(&models.Invitation{})
		db.Unscoped().Where("event_id IN ?", events).Delete(&models.EventTable{})
		db.Unscoped().Where("id IN ?", events).Delete(&models.Event{})
		db.Unscoped().Where("id = ?", eventType.ID).Delete(&models.EventType{})
	})
	return f
}

// newReferenceIntegrityServer wires the real guest and moment handlers,
// services and repositories behind a platform root caller.
func newReferenceIntegrityServer(t *testing.T) *echo.Echo {
	t.Helper()
	db := configuration.DB
	guestService := guestsSvc.NewGuestService(guestrepository.NewGuestRepo(), invitationaccesstokenrepository.NewAccessTokenRepo(),
		redisrepository.NewRedisRepo(), gormrepository.NewGormTransactor(), invitationrepository.NewInvitationRepo())
	guestsCtrl.InitGuestsController(guestService, nil)
	momentsCtrl.InitMomentsController(momentsSvc.NewMomentService(momentrepository.NewMomentRepo(), redisrepository.NewRedisRepo()))
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			var event models.Event
			return &event, db.Where("id = ?", id).First(&event).Error
		},
		GetGuestByID: guestrepository.GetGuestByID,
	})
	t.Cleanup(func() {
		restore()
		guestsCtrl.InitGuestsController(nil, nil)
		momentsCtrl.InitMomentsController(nil)
	})

	e := echo.New()
	e.HideBanner = true
	e.Validator = customValidator.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("cognito_sub", "integration-root")
			return next(c)
		}
	})
	e.POST("/api/guests", guestsCtrl.CreateGuest)
	e.POST("/api/guests/batch", guestsCtrl.CreateGuests)
	e.PUT("/api/guests/:id", guestsCtrl.UpdateGuest)
	e.POST("/api/moments", momentsCtrl.CreateMoment)
	return e
}

type integrityResponse struct {
	Status  int             `json:"status"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

func sendIntegrityRequest(t *testing.T, e *echo.Echo, method, path, body string) integrityResponse {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var resp integrityResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	require.Equal(t, rec.Code, resp.Status, rec.Body.String())
	return resp
}

func loadInvitation(t *testing.T, id uuid.UUID) models.Invitation {
	t.Helper()
	var invitation models.Invitation
	require.NoError(t, configuration.DB.Where("id = ?", id).First(&invitation).Error)
	return invitation
}

// TestReferenceIntegrity_GuestWritesStayInTheirEvent runs PUT /guests/:id and
// POST /guests(/batch) on Postgres: no request can relink a guest to another
// event's invitation, RSVP token or table (soft-deleted tables included), and
// max_guests only ever reaches an invitation of the guest's own event.
func TestReferenceIntegrity_GuestWritesStayInTheirEvent(t *testing.T) {
	f := newReferenceIntegrityFixture(t)
	e := newReferenceIntegrityServer(t)
	guestPath := "/api/guests/" + f.guestA.ID.String()

	resp := sendIntegrityRequest(t, e, http.MethodPut, guestPath, `{"max_guests":4,"invitation_id":"`+f.invitationB.ID.String()+
		`","invitationId":"`+f.invitationB.ID.String()+`","rsvp_token_id":"`+f.tokenB.ID.String()+`","RSVPTokenID":"`+f.tokenB.ID.String()+`"}`)
	require.Equal(t, http.StatusOK, resp.Status, resp.Message)
	var updated struct {
		PrettyToken string `json:"pretty_token"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &updated))
	assert.Equal(t, f.tokenA.PrettyToken, updated.PrettyToken, "the response carries the guest's own token")
	guest := reloadGuest(t, f.guestA.ID)
	assert.Equal(t, f.invitationA.ID, *guest.InvitationID)
	assert.Equal(t, f.tokenA.ID, *guest.RSVPTokenID)
	assert.Equal(t, 4, loadInvitation(t, f.invitationA.ID).MaxGuests)
	assert.Equal(t, 2, loadInvitation(t, f.invitationB.ID).MaxGuests, "event B's invitation is untouched")

	for name, tableID := range map[string]uuid.UUID{"other event": f.tableB.ID, "soft-deleted": f.deletedA.ID, "unknown": uuid.Must(uuid.NewV4())} {
		resp = sendIntegrityRequest(t, e, http.MethodPut, guestPath, `{"first_name":"Cambiada","table_id":"`+tableID.String()+`"}`)
		assert.Equal(t, http.StatusBadRequest, resp.Status, name)
		assert.Equal(t, "Mesa inválida", resp.Message, name)
		assert.Equal(t, "table_outside_event", resp.Error, name)
		guest = reloadGuest(t, f.guestA.ID)
		assert.Equal(t, "Ana", guest.FirstName, name)
		assert.Equal(t, f.tableA.ID, *guest.TableID, name)
	}

	resp = sendIntegrityRequest(t, e, http.MethodPut, guestPath, `{"first_name":"Ana María"}`)
	require.Equal(t, http.StatusOK, resp.Status, resp.Message)
	assert.Equal(t, f.tableA.ID, *reloadGuest(t, f.guestA.ID).TableID, "an absent table key keeps the table")
	resp = sendIntegrityRequest(t, e, http.MethodPut, guestPath, `{"table_id":""}`)
	require.Equal(t, http.StatusOK, resp.Status, resp.Message)
	assert.Nil(t, reloadGuest(t, f.guestA.ID).TableID)
	resp = sendIntegrityRequest(t, e, http.MethodPut, guestPath, `{"tableId":"`+f.tableA.ID.String()+`"}`)
	require.Equal(t, http.StatusOK, resp.Status, resp.Message)
	assert.Equal(t, f.tableA.ID, *reloadGuest(t, f.guestA.ID).TableID)

	// max_guests is not a guest column (it is read from the invitation), so
	// the only write it can cause is the invitation sync, which is skipped.
	resp = sendIntegrityRequest(t, e, http.MethodPut, "/api/guests/"+f.legacy.ID.String(), `{"max_guests":9}`)
	require.Equal(t, http.StatusOK, resp.Status, resp.Message)
	assert.Equal(t, 2, loadInvitation(t, f.invitationB.ID).MaxGuests, "a legacy cross-event link never syncs max_guests")

	countEventA := func(model any) int64 {
		var count int64
		require.NoError(t, configuration.DB.Model(model).Where("event_id = ?", f.eventA.ID).Count(&count).Error)
		return count
	}
	guestsBefore, invitationsBefore := countEventA(&models.Guest{}), countEventA(&models.Invitation{})
	event := f.eventA.ID.String()
	for name, tableID := range map[string]uuid.UUID{"other event": f.tableB.ID, "soft-deleted": f.deletedA.ID} {
		resp = sendIntegrityRequest(t, e, http.MethodPost, "/api/guests/batch", `[{"first_name":"Uno","event_id":"`+event+`","table_id":"`+f.tableA.ID.String()+`"},
			{"first_name":"Dos","event_id":"`+event+`","table_id":"`+tableID.String()+`"}]`)
		assert.Equal(t, http.StatusBadRequest, resp.Status, name)
		assert.Equal(t, "table_outside_event", resp.Error, name)
		assert.Equal(t, guestsBefore, countEventA(&models.Guest{}), name)
		assert.Equal(t, invitationsBefore, countEventA(&models.Invitation{}), name)
	}
	resp = sendIntegrityRequest(t, e, http.MethodPost, "/api/guests", `{"first_name":"Tres","event_id":"`+event+`","table_id":"`+f.tableB.ID.String()+`"}`)
	assert.Equal(t, http.StatusBadRequest, resp.Status)
	assert.Equal(t, "table_outside_event", resp.Error)
	assert.Equal(t, guestsBefore, countEventA(&models.Guest{}))

	resp = sendIntegrityRequest(t, e, http.MethodPost, "/api/guests/batch", `[{"first_name":"Cuatro","event_id":"`+event+`","table_id":"`+f.tableA.ID.String()+
		`","invitation_id":"`+f.invitationB.ID.String()+`","rsvp_token_id":"`+f.tokenB.ID.String()+`"}]`)
	require.Equal(t, http.StatusCreated, resp.Status, resp.Message)
	var created models.Guest
	require.NoError(t, configuration.DB.Where("event_id = ? AND first_name = ?", f.eventA.ID, "Cuatro").First(&created).Error)
	require.NotNil(t, created.InvitationID)
	assert.NotEqual(t, f.invitationB.ID, *created.InvitationID)
	assert.Equal(t, f.eventA.ID, loadInvitation(t, *created.InvitationID).EventID)
	assert.Nil(t, created.RSVPTokenID)
	assert.Equal(t, f.tableA.ID, *created.TableID)
}

// TestReferenceIntegrity_DashboardMomentStaysInItsEvent runs POST /moments on
// Postgres: foreign guests, invitations and media keys are refused, and the
// processing state, media variants and nested objects in the body never reach
// the database.
func TestReferenceIntegrity_DashboardMomentStaysInItsEvent(t *testing.T) {
	f := newReferenceIntegrityFixture(t)
	e := newReferenceIntegrityServer(t)
	event := f.eventA.ID.String()
	countMoments := func() int64 {
		var count int64
		require.NoError(t, configuration.DB.Model(&models.Moment{}).Where("event_id = ?", f.eventA.ID).Count(&count).Error)
		return count
	}

	for name, body := range map[string]string{
		"guest of event B":      `{"event_id":"` + event + `","guest_id":"` + f.guestB.ID.String() + `"}`,
		"invitation of event B": `{"event_id":"` + event + `","invitation_id":"` + f.invitationB.ID.String() + `"}`,
	} {
		resp := sendIntegrityRequest(t, e, http.MethodPost, "/api/moments", body)
		assert.Equal(t, http.StatusBadRequest, resp.Status, name)
		assert.Equal(t, "reference_outside_event", resp.Error, name)
	}
	for _, key := range []string{"moments/" + f.eventB.ID.String() + "/photos/x.webp", "private/secret.jpg"} {
		resp := sendIntegrityRequest(t, e, http.MethodPost, "/api/moments", `{"event_id":"`+event+`","content_url":"`+key+`"}`)
		assert.Equal(t, http.StatusBadRequest, resp.Status, key)
		assert.Equal(t, "moment_content_outside_event", resp.Error, key)
	}
	assert.Zero(t, countMoments())

	org := uuid.Must(uuid.NewV4()).String()
	key := "organizations/" + org + "/moments/" + event + "/photos/x.webp"
	resp := sendIntegrityRequest(t, e, http.MethodPost, "/api/moments", `{"event_id":"`+event+`","guest_id":"`+f.guestA.ID.String()+
		`","invitation_id":"`+f.invitationA.ID.String()+`","content_url":"`+key+`","title":"Brindis",
		"processing_status":"done","processing_input_key":"private/x.jpg",
		"media_variants":[{"object_key":"private/x.webp","width":320,"format":"webp","bytes":1}],
		"guest":{"first_name":"Inyectado","event_id":"`+f.eventB.ID.String()+`"},
		"invitation":{"event_id":"`+f.eventB.ID.String()+`","max_guests":40}}`)
	require.Equal(t, http.StatusCreated, resp.Status, resp.Message)
	var moment models.Moment
	require.NoError(t, configuration.DB.Where("event_id = ?", f.eventA.ID).First(&moment).Error)
	assert.Equal(t, key, moment.ContentURL)
	assert.Equal(t, f.guestA.ID, *moment.GuestID)
	assert.Equal(t, f.invitationA.ID, *moment.InvitationID)
	assert.Empty(t, moment.ProcessingStatus)
	assert.Empty(t, moment.ProcessingInputKey)
	assert.Empty(t, moment.MediaVariants)
	var injectedGuests, eventBInvitations int64
	require.NoError(t, configuration.DB.Model(&models.Guest{}).Where("first_name = ?", "Inyectado").Count(&injectedGuests).Error)
	require.NoError(t, configuration.DB.Model(&models.Invitation{}).Where("event_id = ?", f.eventB.ID).Count(&eventBInvitations).Error)
	assert.Zero(t, injectedGuests, "a nested guest object is not inserted")
	assert.Equal(t, int64(1), eventBInvitations, "a nested invitation object is not inserted")
}

// seatingCapacityFixture is an event with a table of capacity 4 holding a
// confirmed party of 2, an empty table of capacity 10, and unseated guests: a
// confirmed party of 3, a pending guest whose invitation allows 2 (counted as
// 2) and a declined guest with a stale party size (counted as 0).
type seatingCapacityFixture struct {
	event                                      models.Event
	table, roomy                               models.EventTable
	seated, partyOfThree, pendingTwo, declined models.Guest
}

func newSeatingCapacityFixture(t *testing.T) seatingCapacityFixture {
	t.Helper()
	db := configuration.DB
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	var pending, declined models.GuestStatus
	require.NoError(t, db.Where("code = ?", "pending").First(&pending).Error)
	require.NoError(t, db.Where("code = ?", "declined").First(&declined).Error)
	eventType := models.EventType{ID: uuid.Must(uuid.NewV4()), Name: "seating-" + suffix, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, db.Create(&eventType).Error)
	var f seatingCapacityFixture
	f.event = models.Event{ID: uuid.Must(uuid.NewV4()), Name: "Seating " + suffix, Identifier: "seating-" + suffix, EventTypeID: eventType.ID, IsActive: true}
	require.NoError(t, db.Create(&f.event).Error)
	invitationTwo := models.Invitation{ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, MaxGuests: 2}
	require.NoError(t, db.Create(&invitationTwo).Error)
	f.table = models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, Name: "Mesa 1", Capacity: 4}
	f.roomy = models.EventTable{ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, Name: "Mesa 2", Capacity: 10}
	for _, table := range []*models.EventTable{&f.table, &f.roomy} {
		require.NoError(t, db.Omit("Event").Create(table).Error)
	}
	f.seated = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, FirstName: "Sentado", TableID: &f.table.ID,
		RSVPStatus: "confirmed", RSVPGuestCount: 2, GuestStatusID: pending.ID}
	f.partyOfThree = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, FirstName: "Tres",
		RSVPStatus: "confirmed", RSVPGuestCount: 3, GuestStatusID: pending.ID}
	f.pendingTwo = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, FirstName: "Pendiente", InvitationID: &invitationTwo.ID,
		GuestStatusID: pending.ID}
	f.declined = models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.event.ID, FirstName: "Declinado",
		RSVPGuestCount: 5, GuestStatusID: declined.ID}
	for _, guest := range []*models.Guest{&f.seated, &f.partyOfThree, &f.pendingTwo, &f.declined} {
		require.NoError(t, db.Omit("Event", "Table", "Invitation", "GuestStatus").Create(guest).Error)
	}
	t.Cleanup(func() {
		db.Unscoped().Where("event_id = ?", f.event.ID).Delete(&models.Guest{})
		db.Unscoped().Where("event_id = ?", f.event.ID).Delete(&models.Invitation{})
		db.Unscoped().Where("event_id = ?", f.event.ID).Delete(&models.EventTable{})
		db.Unscoped().Where("id = ?", f.event.ID).Delete(&models.Event{})
		db.Unscoped().Where("id = ?", eventType.ID).Delete(&models.EventType{})
	})
	return f
}

func requireOverCapacity(t *testing.T, err error, tableID uuid.UUID, seats, capacity int) {
	t.Helper()
	var over *dtos.TableOverCapacityError
	require.ErrorAs(t, err, &over)
	require.Len(t, over.Tables, 1)
	assert.Equal(t, tableID, over.Tables[0].TableID)
	assert.Equal(t, seats, over.Tables[0].Seats)
	assert.Equal(t, capacity, over.Tables[0].Capacity)
}

func seatingTableOf(t *testing.T, guestID uuid.UUID) *uuid.UUID {
	t.Helper()
	return reloadGuest(t, guestID).TableID
}

func seatingStrPtr(value string) *string { return &value }

// SEAT-1 (strict capacity): the plan save and the bulk assign refuse to seat
// more people than a table holds, counting party sizes like the dashboard,
// and roll back the whole write.
func TestSeatingCapacity_PlanAndAssignAreStrict(t *testing.T) {
	f := newSeatingCapacityFixture(t)
	tableID := f.table.ID.String()

	// 2 seated + 3 = 5 > 4: refused, nothing moves.
	_, err := eventtablerepository.SaveSeatingPlan(f.event.ID, dtos.SeatingPlanSaveRequest{
		Assignments: []dtos.SeatingPlanAssignment{{GuestID: f.partyOfThree.ID.String(), TableID: &tableID}},
	})
	requireOverCapacity(t, err, f.table.ID, 5, 4)
	assert.Nil(t, seatingTableOf(t, f.partyOfThree.ID))

	err = eventtablerepository.AssignGuestsToTables(f.event.ID, map[uuid.UUID]*uuid.UUID{f.partyOfThree.ID: &f.table.ID})
	requireOverCapacity(t, err, f.table.ID, 5, 4)
	assert.Nil(t, seatingTableOf(t, f.partyOfThree.ID))

	// The pending guest counts its invitation max (2) and the declined guest 0:
	// 2 + 2 + 0 = 4 fits exactly.
	err = eventtablerepository.AssignGuestsToTables(f.event.ID, map[uuid.UUID]*uuid.UUID{
		f.pendingTwo.ID: &f.table.ID, f.declined.ID: &f.table.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, &f.table.ID, seatingTableOf(t, f.pendingTwo.ID))

	// The seated party moves out while the party of 3 moves in: 2 + 0 + 3 = 5 > 4.
	roomyID := f.roomy.ID.String()
	_, err = eventtablerepository.SaveSeatingPlan(f.event.ID, dtos.SeatingPlanSaveRequest{
		Assignments: []dtos.SeatingPlanAssignment{
			{GuestID: f.seated.ID.String(), TableID: &roomyID},
			{GuestID: f.partyOfThree.ID.String(), TableID: &tableID},
		},
	})
	requireOverCapacity(t, err, f.table.ID, 5, 4)
	assert.Equal(t, &f.table.ID, seatingTableOf(t, f.seated.ID), "rolled back")

	// Lowering the capacity under the seats taken is refused as well.
	_, err = eventtablerepository.SaveSeatingPlan(f.event.ID, dtos.SeatingPlanSaveRequest{
		Updated: []dtos.SeatingPlanUpdateTable{{ID: tableID, Name: "Mesa 1", Capacity: 3}},
	})
	requireOverCapacity(t, err, f.table.ID, 4, 3)

	// A table created in the plan is checked against its own capacity, and is
	// rolled back with the rest.
	_, err = eventtablerepository.SaveSeatingPlan(f.event.ID, dtos.SeatingPlanSaveRequest{
		Created:     []dtos.SeatingPlanCreateTable{{TempID: "tmp-1", Name: "Mesa chica", Capacity: 2}},
		Assignments: []dtos.SeatingPlanAssignment{{GuestID: f.partyOfThree.ID.String(), TableID: seatingStrPtr("tmp-1")}},
	})
	var over *dtos.TableOverCapacityError
	require.ErrorAs(t, err, &over)
	var created int64
	configuration.DB.Model(&models.EventTable{}).Where("event_id = ? AND name = ?", f.event.ID, "Mesa chica").Count(&created)
	assert.Zero(t, created)

	// The single-guest check counts the same way.
	requireOverCapacity(t, eventtablerepository.CheckSeatsFit(configuration.DB, f.event.ID, f.table.ID, nil, 1, 0), f.table.ID, 5, 4)
	require.NoError(t, eventtablerepository.CheckSeatsFit(configuration.DB, f.event.ID, f.table.ID, []uuid.UUID{f.seated.ID}, 2, 0))
	requireOverCapacity(t, eventtablerepository.CheckSeatsFit(configuration.DB, f.event.ID, f.table.ID, nil, 0, 3), f.table.ID, 4, 3)

	// The SeatingGuest projection reports the same party sizes.
	guests, err := guestrepository.ListSeatingGuestsByEventID(f.event.ID)
	require.NoError(t, err)
	sizes := map[uuid.UUID]int{}
	for _, guest := range guests {
		sizes[guest.ID] = guest.GuestsCount
	}
	assert.Equal(t, map[uuid.UUID]int{f.seated.ID: 2, f.partyOfThree.ID: 3, f.pendingTwo.ID: 2, f.declined.ID: 0}, sizes)
}

// A legacy table that is already over capacity does not block a save that
// adds nobody to it.
func TestSeatingCapacity_LegacyOverfilledTableDoesNotBlockUnrelatedSaves(t *testing.T) {
	f := newSeatingCapacityFixture(t)
	require.NoError(t, configuration.DB.Model(&models.Guest{}).Where("id = ?", f.partyOfThree.ID).Update("table_id", f.table.ID).Error)
	roomyID := f.roomy.ID.String()
	tableID := f.table.ID.String()
	_, err := eventtablerepository.SaveSeatingPlan(f.event.ID, dtos.SeatingPlanSaveRequest{
		Updated: []dtos.SeatingPlanUpdateTable{{ID: tableID, Name: "Mesa 1 renombrada", Capacity: 4}},
		Assignments: []dtos.SeatingPlanAssignment{
			{GuestID: f.pendingTwo.ID.String(), TableID: &roomyID},
			{GuestID: f.seated.ID.String(), TableID: &tableID},
		},
	})
	require.NoError(t, err)
}

// Two concurrent assigns that each fit alone but not together: the table row
// lock lets exactly one through.
func TestSeatingCapacity_ConcurrentAssignsCannotOverfill(t *testing.T) {
	f := newSeatingCapacityFixture(t)
	require.NoError(t, configuration.DB.Model(&models.EventTable{}).Where("id = ?", f.roomy.ID).Update("capacity", 4).Error)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, guestID := range []uuid.UUID{f.partyOfThree.ID, f.pendingTwo.ID} {
		wg.Add(1)
		go func(i int, guestID uuid.UUID) {
			defer wg.Done()
			errs[i] = eventtablerepository.AssignGuestsToTables(f.event.ID, map[uuid.UUID]*uuid.UUID{guestID: &f.roomy.ID})
		}(i, guestID)
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		var over *dtos.TableOverCapacityError
		require.ErrorAs(t, err, &over)
	}
	assert.Equal(t, 1, succeeded, "3 + 2 never both fit a table of 4")
	require.NoError(t, eventtablerepository.CheckSeatsFit(configuration.DB, f.event.ID, f.roomy.ID, nil, 0, 0))
}
