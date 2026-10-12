package eventsrepository

import (
	"events-stocks/configuration"
	"events-stocks/dtos"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// callerAccessFixture is one organization of the caller-access matrix. Only
// "entitled" must reach the caller-access portfolio; every other row is a
// negative case of the session rules in SessionService.organizationAccess.
type callerAccessFixture struct {
	name            string
	orgActive       bool
	orgDeleted      bool
	parent          string // fixture name of the parent organization
	memberUser      string // "caller" or "other"; empty means no membership
	memberActive    bool
	accessApp       string // "request" or "other"; empty means no access row
	accessActive    bool
	eventDayOffset  int
	expectedVisible bool
}

var callerAccessFixtures = []callerAccessFixture{
	{name: "entitled", orgActive: true, memberUser: "caller", memberActive: true, accessApp: "request", accessActive: true, eventDayOffset: -1, expectedVisible: true},
	{name: "organization inactive", orgActive: false, memberUser: "caller", memberActive: true, accessApp: "request", accessActive: true, eventDayOffset: 1},
	{name: "organization deleted", orgActive: true, orgDeleted: true, memberUser: "caller", memberActive: true, accessApp: "request", accessActive: true, eventDayOffset: 1},
	{name: "no member access row", orgActive: true, memberUser: "caller", memberActive: true, eventDayOffset: 1},
	{name: "member access revoked", orgActive: true, memberUser: "caller", memberActive: true, accessApp: "request", accessActive: false, eventDayOffset: 1},
	{name: "access for another application", orgActive: true, memberUser: "caller", memberActive: true, accessApp: "other", accessActive: true, eventDayOffset: 1},
	{name: "membership inactive", orgActive: true, memberUser: "caller", memberActive: false, accessApp: "request", accessActive: true, eventDayOffset: 1},
	{name: "another user's organization", orgActive: true, memberUser: "other", memberActive: true, accessApp: "request", accessActive: true, eventDayOffset: 1},
	// Inherited Owner/Admin access is reached with an explicit client_id; the
	// caller-access portfolio lists direct memberships only, as before.
	{name: "child of the entitled organization", orgActive: true, parent: "entitled", eventDayOffset: 1},
}

// TestCallerAccessListsHideOrganizationsTheSessionWouldNotAllow runs the real
// SQL against PostgreSQL. It is opt-in so ordinary suites stay hermetic, and
// it only creates session-local TEMP tables (they shadow public tables of the
// same name and vanish with the connection), so it never writes to the
// tables of the database it connects to:
//
//	EVENTIAPP_REPOSITORY_TEST_DSN="host=127.0.0.1 port=<local port> user=postgres dbname=<db> sslmode=disable" \
//	go test ./repositories/eventsrepository -run CallerAccess -count=1
func TestCallerAccessListsHideOrganizationsTheSessionWouldNotAllow(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("EVENTIAPP_REPOSITORY_TEST_DSN"))
	if dsn == "" {
		t.Skip("EVENTIAPP_REPOSITORY_TEST_DSN is not configured")
	}
	if strings.Contains(dsn, "15432") {
		t.Fatal("refusing port 15432: it is the production read-only tunnel, use the local database")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// One connection: TEMP tables are visible only to the session that made
	// them, and the list functions run their queries concurrently.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	createCallerAccessTempSchema(t, db)

	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })

	now := time.Now().UTC().Truncate(time.Second)
	callerID := uuid.Must(uuid.NewV4())
	otherUserID := uuid.Must(uuid.NewV4())
	requestApplicationID := uuid.Must(uuid.NewV4())
	otherApplicationID := uuid.Must(uuid.NewV4())
	roleID := uuid.Must(uuid.NewV4())
	require.NoError(t, db.Exec(`INSERT INTO client_roles (id, code) VALUES (?, 'EVENT_MANAGER')`, roleID).Error)

	orgIDs := map[string]uuid.UUID{}
	eventIDs := map[string]uuid.UUID{}
	for _, fixture := range callerAccessFixtures {
		orgIDs[fixture.name] = uuid.Must(uuid.NewV4())
	}
	for _, fixture := range callerAccessFixtures {
		orgID := orgIDs[fixture.name]
		var parentID *uuid.UUID
		if fixture.parent != "" {
			parent := orgIDs[fixture.parent]
			parentID = &parent
		}
		var deletedAt *time.Time
		if fixture.orgDeleted {
			deletedAt = &now
		}
		require.NoError(t, db.Exec(
			`INSERT INTO clients (id, name, code, parent_id, is_active, deleted_at) VALUES (?, ?, ?, ?, ?, ?)`,
			orgID, fixture.name, "qa-"+orgID.String()[:8], parentID, fixture.orgActive, deletedAt,
		).Error)
		if fixture.memberUser != "" {
			memberID := uuid.Must(uuid.NewV4())
			userID := callerID
			if fixture.memberUser == "other" {
				userID = otherUserID
			}
			require.NoError(t, db.Exec(
				`INSERT INTO client_members (id, client_id, user_id, client_role_id, is_active) VALUES (?, ?, ?, ?, ?)`,
				memberID, orgID, userID, roleID, fixture.memberActive,
			).Error)
			if fixture.accessApp != "" {
				applicationID := requestApplicationID
				if fixture.accessApp == "other" {
					applicationID = otherApplicationID
				}
				require.NoError(t, db.Exec(
					`INSERT INTO client_member_applications (id, client_member_id, application_id, is_active) VALUES (?, ?, ?, ?)`,
					uuid.Must(uuid.NewV4()), memberID, applicationID, fixture.accessActive,
				).Error)
			}
		}
		eventID := uuid.Must(uuid.NewV4())
		eventIDs[fixture.name] = eventID
		require.NoError(t, db.Exec(
			`INSERT INTO events (id, name, identifier, event_date_time, timezone, is_active, max_guests, client_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 'UTC', true, 10, ?, ?, ?)`,
			eventID, fixture.name, "qa-"+eventID.String(), now.AddDate(0, 0, fixture.eventDayOffset), orgID, now, now,
		).Error)
	}

	expected := make([]uuid.UUID, 0, 1)
	for _, fixture := range callerAccessFixtures {
		if fixture.expectedVisible {
			expected = append(expected, eventIDs[fixture.name])
		}
	}
	access := dtos.EventCallerAccess{UserID: callerID, ApplicationID: requestApplicationID}

	t.Run("unpaged list", func(t *testing.T) {
		events, err := GetEventsForUser(access)
		require.NoError(t, err)
		ids := make([]uuid.UUID, 0, len(events))
		for _, event := range events {
			ids = append(ids, event.ID)
		}
		assert.ElementsMatch(t, expected, ids)
	})

	t.Run("paged list, counts and filtered total", func(t *testing.T) {
		events, total, counts, err := ListEventPage(nil, &access, dtos.EventListQuery{Page: 1, PageSize: 50, Filter: "all", Now: now})
		require.NoError(t, err)
		assert.Equal(t, 1, total)
		assert.Equal(t, dtos.EventListCounts{All: 1, Past: 1}, counts)
		require.Len(t, events, 1)
		assert.Equal(t, expected[0], events[0].ID)

		events, total, _, err = ListEventPage(nil, &access, dtos.EventListQuery{Page: 1, PageSize: 50, Filter: "upcoming", Now: now})
		require.NoError(t, err)
		assert.Zero(t, total, "future events of disallowed organizations must not be counted")
		assert.Empty(t, events)
	})

	t.Run("dashboard overview", func(t *testing.T) {
		overview, err := GetEventDashboardOverview(nil, &access, now)
		require.NoError(t, err)
		assert.Equal(t, dtos.EventDashboardMetrics{Total: 1, Active: 1, PastActive: 1, TotalCapacity: 10}, overview.Metrics)
		assert.Nil(t, overview.NextEvent, "a future event of a disallowed organization must not become the next event")
		require.Len(t, overview.ActiveEvents, 1)
		assert.Equal(t, expected[0], overview.ActiveEvents[0].ID)
	})

	t.Run("notifications", func(t *testing.T) {
		notifications, err := ListEventNotifications(nil, &access, now)
		require.NoError(t, err)
		ids := make([]uuid.UUID, 0, len(notifications))
		for _, notification := range notifications {
			ids = append(ids, notification.ID)
		}
		assert.ElementsMatch(t, expected, ids)
	})

	t.Run("other application sees no events of request-only organizations", func(t *testing.T) {
		otherAccess := dtos.EventCallerAccess{UserID: callerID, ApplicationID: otherApplicationID}
		events, err := GetEventsForUser(otherAccess)
		require.NoError(t, err)
		ids := make([]uuid.UUID, 0, len(events))
		for _, event := range events {
			ids = append(ids, event.ID)
		}
		assert.ElementsMatch(t, []uuid.UUID{eventIDs["access for another application"]}, ids)
	})

	t.Run("explicit organization and platform scopes are unchanged", func(t *testing.T) {
		inactiveOrg := orgIDs["organization inactive"]
		events, total, _, err := ListEventPage(&inactiveOrg, nil, dtos.EventListQuery{Page: 1, PageSize: 50, Filter: "all", Now: now})
		require.NoError(t, err)
		assert.Equal(t, 1, total)
		require.Len(t, events, 1)
		assert.Equal(t, eventIDs["organization inactive"], events[0].ID)

		_, total, _, err = ListEventPage(nil, nil, dtos.EventListQuery{Page: 1, PageSize: 50, Filter: "all", Now: now})
		require.NoError(t, err)
		assert.Equal(t, len(callerAccessFixtures), total)
	})
}

func createCallerAccessTempSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	statements := []string{
		`CREATE TEMP TABLE clients (
			id uuid PRIMARY KEY, name text, code text, parent_id uuid,
			is_active boolean NOT NULL DEFAULT true, deleted_at timestamptz
		)`,
		`CREATE TEMP TABLE client_roles (id uuid PRIMARY KEY, code text NOT NULL)`,
		`CREATE TEMP TABLE client_members (
			id uuid PRIMARY KEY, client_id uuid NOT NULL, user_id uuid NOT NULL,
			client_role_id uuid NOT NULL, is_active boolean NOT NULL DEFAULT true
		)`,
		`CREATE TEMP TABLE client_member_applications (
			id uuid PRIMARY KEY, client_member_id uuid NOT NULL, application_id uuid NOT NULL,
			is_active boolean NOT NULL DEFAULT true
		)`,
		`CREATE TEMP TABLE event_types (
			id uuid PRIMARY KEY, name text, created_at timestamptz, updated_at timestamptz, deleted_at timestamptz
		)`,
		`CREATE TEMP TABLE events (
			id uuid PRIMARY KEY, name text, identifier text, address text, organizer_name text,
			event_date_time timestamptz, timezone text, is_active boolean NOT NULL DEFAULT true,
			max_guests integer, client_id uuid, event_type_id uuid,
			created_at timestamptz, updated_at timestamptz, deleted_at timestamptz
		)`,
		`CREATE TEMP TABLE moments (
			id uuid PRIMARY KEY, event_id uuid, is_approved boolean, processing_status text, deleted_at timestamptz
		)`,
	}
	for _, statement := range statements {
		require.NoError(t, db.Exec(statement).Error)
	}
	names := []string{"clients", "client_roles", "client_members", "client_member_applications", "event_types", "events", "moments"}
	for _, name := range names {
		var schema string
		require.NoError(t, db.Raw(`SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass(?)`, name).Scan(&schema).Error)
		require.True(t, strings.HasPrefix(schema, "pg_temp"), "%s must resolve to the session TEMP table, got schema %q", name, schema)
	}
}
