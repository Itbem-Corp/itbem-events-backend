package guestrepository

import (
	"strings"
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGuestSummaryQueryIsOneAggregateStatementWithEffectiveStatusPrecedence(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	eventID := uuid.Must(uuid.NewV4())
	var summary dtos.GuestSummary
	stmt := guestSummaryQuery(db, eventID).Scan(&summary).Statement
	sqlText := strings.ToLower(strings.Join(strings.Fields(stmt.SQL.String()), " "))

	assert.Contains(t, sqlText, "with guest_rollup as")
	assert.Contains(t, sqlText, "from guests")
	assert.Contains(t, sqlText, "left join guest_statuses")
	assert.Contains(t, sqlText, "left join invitations")
	assert.Contains(t, sqlText, "nullif(btrim(guests.rsvp_status), '')")
	assert.Contains(t, sqlText, "guest_statuses.code")
	assert.Contains(t, sqlText, "else 'pending'")
	assert.Contains(t, sqlText, "guests.rsvp_guest_count > 0")
	assert.Contains(t, sqlText, "invitations.max_guests > 0")
	assert.Contains(t, sqlText, "filter (where effective_status = 'confirmed')")
	assert.Contains(t, sqlText, "sum(party_size)")
	assert.NotContains(t, sqlText, "select guests.*")
	assert.NotContains(t, sqlText, "order by")
	assert.NotContains(t, sqlText, ";")
	assert.Equal(t, []interface{}{eventID}, stmt.Vars)
}

func TestGuestSummaryQueryRespectsEveryJoinedModelSoftDelete(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	var summary dtos.GuestSummary
	stmt := guestSummaryQuery(db, uuid.Must(uuid.NewV4())).Scan(&summary).Statement
	sqlText := strings.ToLower(strings.Join(strings.Fields(stmt.SQL.String()), " "))

	assert.Contains(t, sqlText, "guests.deleted_at is null")
	assert.Contains(t, sqlText, "guest_statuses.deleted_at is null")
	assert.Contains(t, sqlText, "invitations.deleted_at is null")
}

func TestGuestShareSummaryQueryIncludesFirstPendingRecipientInOneAggregate(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	eventID := uuid.Must(uuid.NewV4())
	var row guestShareSummaryRow
	stmt := guestShareSummaryQuery(db, eventID).Scan(&row).Statement
	sqlText := strings.ToLower(strings.Join(strings.Fields(stmt.SQL.String()), " "))

	assert.Contains(t, sqlText, "count(*) as total")
	assert.Contains(t, sqlText, "pending_with_email")
	assert.Contains(t, sqlText, "array_agg(guests.id order by guests.id desc)")
	assert.Contains(t, sqlText, "as first_pending_id")
	assert.Contains(t, sqlText, "as first_pending_email")
	assert.Contains(t, sqlText, "as first_pending_token")
	assert.Equal(t, []interface{}{eventID}, stmt.Vars)
}

func TestGuestDashboardSummariesQueryCombinesRSVPAndShareMetrics(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	eventID := uuid.Must(uuid.NewV4())
	var row guestDashboardSummariesRow
	stmt := guestDashboardSummariesQuery(db, eventID).Scan(&row).Statement
	sqlText := strings.ToLower(strings.Join(strings.Fields(stmt.SQL.String()), " "))

	assert.Contains(t, sqlText, "with guest_rollup as")
	assert.Contains(t, sqlText, "left join guest_statuses")
	assert.Contains(t, sqlText, "left join invitations")
	assert.Contains(t, sqlText, "left join invitation_access_tokens")
	assert.Contains(t, sqlText, "as total_attendees")
	assert.Contains(t, sqlText, "as pending_with_email")
	assert.Contains(t, sqlText, "as first_pending_token")
	assert.Equal(t, []interface{}{eventID}, stmt.Vars)
}

func TestAnalyticsGuestsQueryKeepsLegacyTablesStorageForRollbackCompatibility(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	eventID := uuid.Must(uuid.NewV4())
	var rows []dtos.AnalyticsGuest
	stmt := analyticsGuestsQuery(db, eventID).Scan(&rows).Statement
	sqlText := strings.ToLower(strings.Join(strings.Fields(stmt.SQL.String()), " "))

	assert.Contains(t, sqlText, "left join tables on tables.id = guests.table_id")
	assert.Contains(t, sqlText, "btrim(tables.name)")
	assert.NotContains(t, sqlText, "event_tables")
	assert.Equal(t, []interface{}{eventID}, stmt.Vars)
}

// checkinGuestsWhereSQL renders the check-in list filter offline and returns its
// WHERE clause (lower-cased, whitespace collapsed) and the bound values.
func checkinGuestsWhereSQL(t *testing.T, query dtos.CheckinGuestsListQuery) (string, []interface{}) {
	t.Helper()
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	var total int64
	stmt := checkinGuestsBaseQuery(db, uuid.Must(uuid.NewV4()), query).Count(&total).Statement
	sqlText := strings.ToLower(strings.Join(strings.Fields(stmt.SQL.String()), " "))
	where := strings.Index(sqlText, " where ")
	require.GreaterOrEqual(t, where, 0, sqlText)
	return sqlText[where:], stmt.Vars
}

const (
	guestManagerSearchSQL = `lower(concat_ws(' ', guests.first_name, guests.last_name, guests.email, guests.phone, guests.table_number, "table".name)) like`
	doorGuestSearchSQL    = `lower(concat_ws(' ', guests.first_name, guests.last_name, guests.table_number, "table".name)) like`
)

// A check-in caller never sees email or phone, so the door search must not let
// it read them back from which guests match a substring ("@gmail.com", or the
// visible name followed by one more character of the email at a time).
func TestCheckinGuestsQuery_DoorSearchMatchesContactOnlyByCompleteValue(t *testing.T) {
	where, vars := checkinGuestsWhereSQL(t, dtos.CheckinGuestsListQuery{Search: " Ana@Gmail.com ", DoorSearch: true})

	assert.Contains(t, where, doorGuestSearchSQL)
	assert.NotContains(t, where, guestManagerSearchSQL)
	assert.Equal(t, 1, strings.Count(where, " like "), where)
	assert.Contains(t, where, "lower(btrim(guests.email)) = lower(")
	assert.NotContains(t, where, "guests.phone", "a search that is not a phone number never touches the phone")
	assert.Contains(t, vars, "%ana@gmail.com%")
	assert.Contains(t, vars, "Ana@Gmail.com")
}

func TestCheckinGuestsQuery_DoorSearchMatchesPhoneOnlyByFullNormalizedNumber(t *testing.T) {
	where, vars := checkinGuestsWhereSQL(t, dtos.CheckinGuestsListQuery{Search: "+52 (55) 1234-5678", DoorSearch: true})

	assert.Contains(t, where, doorGuestSearchSQL)
	assert.Equal(t, 1, strings.Count(where, " like "), where)
	assert.Contains(t, where, `regexp_replace(guests.phone, '[^0-9]', '', 'g') = $`)
	assert.Contains(t, vars, "525512345678")
	assert.NotContains(t, where, "guests.phone like")
}

func TestCheckinGuestsQuery_DoorSearchMatchesLikeWildcardsLiterally(t *testing.T) {
	where, vars := checkinGuestsWhereSQL(t, dtos.CheckinGuestsListQuery{Search: `Mesa_1%\`, DoorSearch: true})

	assert.Regexp(t, `like \$\d+ escape '[\\]'`, where)
	// _ % and \ are escaped with \ so they only match themselves.
	assert.Contains(t, vars, `%mesa\_1\%\\%`)
}

// The guest-manager pages and the CSV export are read by guest:manage, which
// already sees contact data: their substring search is unchanged.
func TestCheckinGuestsQuery_GuestManagerSearchKeepsSubstringOverContactFields(t *testing.T) {
	where, vars := checkinGuestsWhereSQL(t, dtos.CheckinGuestsListQuery{Search: "@gmail.com"})

	assert.Contains(t, where, guestManagerSearchSQL)
	assert.NotContains(t, where, "escape")
	assert.NotContains(t, where, "regexp_replace")
	assert.Contains(t, vars, "%@gmail.com%")
}

// A scanned QR is matched exactly and takes precedence over any search.
func TestCheckinGuestsQuery_QRIgnoresSearch(t *testing.T) {
	where, vars := checkinGuestsWhereSQL(t, dtos.CheckinGuestsListQuery{QR: "PRETTY42", Search: "ana", DoorSearch: true})

	assert.Contains(t, where, "cast(guests.id as text) = $")
	assert.Contains(t, where, "invitation_access_tokens.pretty_token = $")
	assert.NotContains(t, where, " like ")
	assert.Contains(t, vars, "PRETTY42")
}

func TestLockGuestsForStatusTransitionLocksLiveGuestsOfTheEventInIDOrder(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	eventID := uuid.Must(uuid.NewV4())
	ids := []uuid.UUID{uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())}
	var guests []models.Guest
	stmt := lockGuestsForStatusTransition(db, eventID, ids).Find(&guests).Statement
	sqlText := strings.ToLower(strings.Join(strings.Fields(stmt.SQL.String()), " "))

	assert.Contains(t, sqlText, "from \"guests\"")
	assert.Contains(t, sqlText, "event_id = $1 and id in ($2,$3)")
	assert.Contains(t, sqlText, "\"guests\".\"deleted_at\" is null")
	assert.Contains(t, sqlText, "order by id")
	assert.True(t, strings.HasSuffix(sqlText, "for update"), sqlText)
	assert.Equal(t, []interface{}{eventID, ids[0], ids[1]}, stmt.Vars)
}

func TestGuestStatusTransitionValuesWritesOnlyRSVPColumnsWithSQLNulls(t *testing.T) {
	statusID := uuid.Must(uuid.NewV4())
	guest := models.Guest{
		ID: uuid.Must(uuid.NewV4()), FirstName: "Ana", Email: "ana@example.com",
		GuestStatusID: statusID, RSVPStatus: "pending", RSVPMethod: "host",
	}

	values := guestStatusTransitionValues(&guest)

	assert.Equal(t, statusID, values["guest_status_id"])
	assert.Equal(t, "pending", values["rsvp_status"])
	assert.Equal(t, "host", values["rsvp_method"])
	rsvpAt, present := values["rsvp_at"]
	assert.True(t, present)
	assert.Nil(t, rsvpAt, "a nil pointer must reach the driver as an untyped NULL")
	for _, column := range []string{"first_name", "email", "event_id", "id", "table_id"} {
		assert.NotContains(t, values, column)
	}
}
