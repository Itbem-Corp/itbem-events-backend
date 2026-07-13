package guestrepository

import (
	"strings"
	"testing"

	"events-stocks/dtos"
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
