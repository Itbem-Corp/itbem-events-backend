//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
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
	redisrepository "events-stocks/repositories/redisrepository"
	"events-stocks/seeds"
	invitationsSvc "events-stocks/services/invitations"
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
	if !dockerAvailable() {
		fmt.Println("SKIP: Docker not available — enable WSL integration in Docker Desktop")
		os.Exit(0)
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
		panic("could not start postgres container: " + err.Error())
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("could not get connection string: " + err.Error())
	}
	testPostgresConnectionString = connStr

	db, err := gorm.Open(gormpostgres.Open(connStr), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		panic("gorm open failed: " + err.Error())
	}
	configuration.DB = db

	// uuid_generate_v4() is used as the default for UUID primary keys
	configuration.DB.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`)

	// Migrate all models (same set as production)
	if err := configuration.DB.AutoMigrate(configuration.GetAllModels()...); err != nil {
		panic("migration failed: " + err.Error())
	}

	// Seed GuestStatus reference data (pending / confirmed / declined)
	seeds.SeedGuestStatus(configuration.DB)

	// ── Miniredis (in-memory Redis, no container needed) ──────────────────────
	mr, err := miniredis.Run()
	if err != nil {
		panic("could not start miniredis: " + err.Error())
	}
	defer mr.Close()
	configuration.RedisClient = redis.NewClient(&redis.Options{Addr: mr.Addr()})

	os.Exit(m.Run())
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
	assert.Equal(t, "success", resp["status"])
	assert.Equal(t, "RSVP confirmed", resp["message"])

	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok, "data field missing or wrong type in: %s", rec.Body.String())
	assert.Equal(t, "confirmed", data["rsvp_status"])
	assert.Equal(t, float64(2), data["rsvp_guest_count"]) // JSON numbers decode as float64
	assert.NotEmpty(t, data["rsvp_at"])

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
