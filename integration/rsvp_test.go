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
	invitationsCtrl "events-stocks/controllers/invitations"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
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
		EventTypeID: eventType.ID,
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
