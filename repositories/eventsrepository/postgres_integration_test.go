//go:build integration

package eventsrepository

// Real-PostgreSQL regression tests for the event lifecycle (EVT-1, EVT-2,
// EVT-6, STU-19). They start a disposable postgres:16 container with
// testcontainers and migrate it through configuration.MigrateModelsForTest,
// the same transactional path the API runs on boot, so the raw DDL integrity
// migrations are exercised too. Nothing touches the local stack database.
//
//	go test -tags integration ./repositories/eventsrepository -count=1
//
// Without Docker the suite prints SKIP and exits 0: that is "not run".

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/eventconfigrepository"
)

func TestMain(m *testing.M) {
	os.Exit(runPostgresSuite(m))
}

func runPostgresSuite(m *testing.M) int {
	if exec.Command("docker", "info").Run() != nil {
		fmt.Println("SKIP: Docker not available; event lifecycle PostgreSQL tests not run")
		return 0
	}
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Println("ERROR: could not start disposable PostgreSQL test container:", err)
		return 1
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(cleanupCtx)
	}()
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Println("ERROR: could not obtain disposable PostgreSQL connection:", err)
		return 1
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		fmt.Println("ERROR: could not open disposable PostgreSQL database:", err)
		return 1
	}
	previous := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previous }()
	if err := configuration.MigrateModelsForTest(db); err != nil {
		fmt.Println("ERROR: could not migrate disposable PostgreSQL database:", err)
		return 1
	}
	return m.Run()
}

func lifecycleSuffix() string {
	return uuid.Must(uuid.NewV4()).String()[:8]
}

func createLifecycleEvent(t *testing.T, clientID *uuid.UUID, name, identifier string) models.Event {
	t.Helper()
	event := models.Event{
		ID:            uuid.Must(uuid.NewV4()),
		ClientID:      clientID,
		Name:          name,
		Identifier:    identifier,
		EventDateTime: time.Now().Add(72 * time.Hour),
		Timezone:      "America/Mexico_City",
	}
	require.NoError(t, CreateEvent(&event))
	return event
}

// STU-19: the dashboard loads the config with its design associations, the
// patch changes only the *ID fields, and the save used to copy the loaded
// association IDs back, so the old template/palette stayed stored.
func TestUpdateEventConfigPersistsDesignChangesOverLoadedAssociations(t *testing.T) {
	db := configuration.DB
	suffix := lifecycleSuffix()

	templateA := models.DesignTemplate{ID: uuid.Must(uuid.NewV4()), Name: "A", Identifier: "qa-template-a-" + suffix, IsActive: true}
	templateB := models.DesignTemplate{ID: uuid.Must(uuid.NewV4()), Name: "B", Identifier: "qa-template-b-" + suffix, IsActive: true}
	paletteA := models.ColorPalette{ID: uuid.Must(uuid.NewV4()), Name: "palette-a-" + suffix}
	paletteB := models.ColorPalette{ID: uuid.Must(uuid.NewV4()), Name: "palette-b-" + suffix}
	require.NoError(t, db.Create(&templateA).Error)
	require.NoError(t, db.Create(&templateB).Error)
	require.NoError(t, db.Create(&paletteA).Error)
	require.NoError(t, db.Create(&paletteB).Error)

	event := createLifecycleEvent(t, nil, "Design switch "+suffix, "design-switch-"+suffix)
	config := models.EventConfig{ID: event.ID, DesignTemplateID: &templateA.ID, ColorPaletteID: &paletteA.ID, ActiveFrom: time.Now()}
	require.NoError(t, db.Create(&config).Error)

	loaded, err := eventconfigrepository.GetEventConfigByID(event.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.DesignTemplate, "the read path must load the association for this test to mean anything")
	require.NotNil(t, loaded.ColorPalette)

	loaded.DesignTemplateID = &templateB.ID
	loaded.ColorPaletteID = &paletteB.ID
	require.NoError(t, eventconfigrepository.UpdateEventConfig(loaded))

	var stored models.EventConfig
	require.NoError(t, db.First(&stored, "id = ?", event.ID).Error)
	require.NotNil(t, stored.DesignTemplateID)
	require.NotNil(t, stored.ColorPaletteID)
	assert.Equal(t, templateB.ID, *stored.DesignTemplateID)
	assert.Equal(t, paletteB.ID, *stored.ColorPaletteID)

	// "Usar plantilla": clearing the palette override must store NULL.
	reloaded, err := eventconfigrepository.GetEventConfigByID(event.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.ColorPalette)
	reloaded.ColorPaletteID = nil
	require.NoError(t, eventconfigrepository.UpdateEventConfig(reloaded))
	var cleared models.EventConfig
	require.NoError(t, db.First(&cleared, "id = ?", event.ID).Error)
	assert.Nil(t, cleared.ColorPaletteID)
	require.NotNil(t, cleared.DesignTemplateID)
	assert.Equal(t, templateB.ID, *cleared.DesignTemplateID)
}

// EVT-1: "Tipo de evento" is optional in the dashboard. A missing type must be
// stored as NULL on create, and clearing it on edit must store NULL too (the
// zero UUID used to violate fk_events_event_type and return 500).
func TestEventWithoutTypeIsStoredAsNullOnCreateAndUpdate(t *testing.T) {
	db := configuration.DB
	suffix := lifecycleSuffix()

	untyped := createLifecycleEvent(t, nil, "Sin tipo "+suffix, "sin-tipo-"+suffix)
	var typeIsNull bool
	require.NoError(t, db.Raw("SELECT event_type_id IS NULL FROM events WHERE id = ?", untyped.ID).Row().Scan(&typeIsNull))
	assert.True(t, typeIsNull)

	eventType := models.EventType{ID: uuid.Must(uuid.NewV4()), Name: "qa-type-" + suffix}
	require.NoError(t, db.Create(&eventType).Error)
	typed := models.Event{
		ID: uuid.Must(uuid.NewV4()), Name: "Con tipo " + suffix, Identifier: "con-tipo-" + suffix,
		EventTypeID: &eventType.ID, Timezone: "UTC",
	}
	require.NoError(t, CreateEvent(&typed))

	// The edit handler loads the event with EventType joined, then clears it.
	loaded, err := GetEventByIDRaw(typed.ID)
	require.NoError(t, err)
	require.Equal(t, eventType.ID, loaded.EventType.ID, "the join must load the association for this test to mean anything")
	loaded.EventTypeID = nil
	require.NoError(t, UpdateEvent(loaded))

	var cleared models.Event
	require.NoError(t, db.First(&cleared, "id = ?", typed.ID).Error)
	assert.Nil(t, cleared.EventTypeID)
}

func createLifecycleClient(t *testing.T, suffix, label string) uuid.UUID {
	t.Helper()
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "QA", Code: "QA-" + label + "-" + suffix}
	require.NoError(t, configuration.DB.Create(&clientType).Error)
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "QA " + label + " " + suffix, Code: "qa-" + label + "-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	require.NoError(t, configuration.DB.Omit("ClientType").Create(&client).Error)
	return client.ID
}

func eventIndexDefinition(t *testing.T, name string) string {
	t.Helper()
	var definition string
	configuration.DB.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, name).Scan(&definition)
	return definition
}

// EVT-2 / EVT-6 / SET-15: names are unique per organization among live
// events, identifiers platform-wide among live events, and a deleted event
// releases both.
func TestEventNameAndIdentifierUniquenessIsPerOrganizationAndIgnoresDeletedRows(t *testing.T) {
	suffix := lifecycleSuffix()
	orgA := createLifecycleClient(t, suffix, "A")
	orgB := createLifecycleClient(t, suffix, "B")
	name := "XV años de Sofía " + suffix

	createLifecycleEvent(t, &orgA, name, "xv-sofia-a-"+suffix)
	// Another organization may use the same name.
	createLifecycleEvent(t, &orgB, name, "xv-sofia-b-"+suffix)

	// The same organization may not.
	duplicate := models.Event{ID: uuid.Must(uuid.NewV4()), ClientID: &orgA, Name: name, Identifier: "xv-sofia-a2-" + suffix, Timezone: "UTC"}
	assert.ErrorIs(t, CreateEvent(&duplicate), ErrEventNameAlreadyExists)

	// A live identifier is unique across organizations.
	slugClash := models.Event{ID: uuid.Must(uuid.NewV4()), ClientID: &orgB, Name: "Otro " + suffix, Identifier: "xv-sofia-a-" + suffix, Timezone: "UTC"}
	assert.ErrorIs(t, CreateEvent(&slugClash), ErrEventIdentifierAlreadyExists)

	// Update maps the same violation (it used to be "record not found").
	other := createLifecycleEvent(t, &orgA, "Fiesta "+suffix, "fiesta-"+suffix)
	other.Name = name
	assert.ErrorIs(t, UpdateEvent(&other), ErrEventNameAlreadyExists)

	// Delete then recreate the same name and slug in the same organization.
	deleted := createLifecycleEvent(t, &orgA, "Boda Ana "+suffix, "boda-ana-"+suffix)
	require.NoError(t, DeleteEvent(deleted.ID))
	assert.False(t, IdentifierExists("boda-ana-"+suffix), "the slug generator must see the slug as free")
	createLifecycleEvent(t, &orgA, "Boda Ana "+suffix, "boda-ana-"+suffix)
}

func TestEventIdentityMigrationReplacesGlobalIndexesAndIsIdempotent(t *testing.T) {
	assert.Empty(t, eventIndexDefinition(t, "idx_events_name"), "the global name index must be dropped")
	assert.Empty(t, eventIndexDefinition(t, "idx_events_identifier"), "the global identifier index must be dropped")
	assert.Contains(t, eventIndexDefinition(t, configuration.EventClientNameLiveIndex), "(client_id, name) WHERE (deleted_at IS NULL)")
	assert.Contains(t, eventIndexDefinition(t, configuration.EventIdentifierLiveIndex), "(identifier) WHERE (deleted_at IS NULL)")

	// A database still at the previous schema: the old global indexes exist.
	// They are recreated without UNIQUE because the previous test stored the
	// same name in two organizations, which a global UNIQUE index rejects (the
	// reason a rollback to the old binary needs deduplication first).
	require.NoError(t, configuration.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_events_name ON events (name)`).Error)
	require.NoError(t, configuration.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_events_identifier ON events (identifier)`).Error)
	require.NoError(t, configuration.MigrateModelsForTest(configuration.DB))
	require.NoError(t, configuration.MigrateModelsForTest(configuration.DB), "a second boot must be a no-op")
	assert.Empty(t, eventIndexDefinition(t, "idx_events_name"))
	assert.Empty(t, eventIndexDefinition(t, "idx_events_identifier"))
	assert.NotEmpty(t, eventIndexDefinition(t, configuration.EventClientNameLiveIndex))
}
