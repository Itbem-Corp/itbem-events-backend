package eventsection

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"events-stocks/models"
	eventsService "events-stocks/services/events"
	"events-stocks/services/ports"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type conditionalSectionRepo struct {
	mockSectionRepo
	stored          models.EventSection
	conditionalErr  error
	conditionalSeen []time.Time
	plainUpdates    int
}

func (m *conditionalSectionRepo) UpdateEventSection(*models.EventSection) error {
	m.plainUpdates++
	return nil
}

func (m *conditionalSectionRepo) UpdateEventSectionIfUnmodified(obj *models.EventSection, expected time.Time) error {
	m.conditionalSeen = append(m.conditionalSeen, expected)
	if m.conditionalErr != nil {
		return m.conditionalErr
	}
	m.stored = *obj
	return nil
}

func (m *conditionalSectionRepo) GetEventSectionByID(uuid.UUID) (*models.EventSection, error) {
	current := m.stored
	return &current, nil
}

var _ ports.ConditionalEventSectionUpdater = (*conditionalSectionRepo)(nil)

func runSectionUpdate(t *testing.T, stored models.EventSection, repo *conditionalSectionRepo, body string) (int, map[string]interface{}) {
	t.Helper()
	setSectionRootAuth(t, map[uuid.UUID]models.EventSection{stored.ID: stored})
	orig := eventSectionSvc
	eventSectionSvc = eventsService.NewEventSectionService(repo, &mockSectionCache{})
	t.Cleanup(func() { eventSectionSvc = orig })

	c, rec := newSectionEchoCtx(http.MethodPut, "/sections/"+stored.ID.String(), body)
	c.Set("cognito_sub", "test-sub")
	c.SetParamNames("id")
	c.SetParamValues(stored.ID.String())
	require.NoError(t, UpdateSection(c))
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return rec.Code, payload
}

// STU-N3: a save based on a stale updated_at is rejected with 409, the
// current section is returned and nothing is written.
func TestUpdateSection_StaleExpectedUpdatedAtReturnsConflict(t *testing.T) {
	stored := models.EventSection{
		ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4()), Title: "Otra pestaña",
		Config: `{"text":"guardado en otra pestaña"}`, UpdatedAt: time.Date(2026, 10, 12, 10, 0, 5, 123456000, time.UTC),
	}
	repo := &conditionalSectionRepo{stored: stored}
	code, payload := runSectionUpdate(t, stored, repo,
		`{"config":{"text":"mío"},"expected_updated_at":"2026-10-12T10:00:01.000001Z"}`)

	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "section_conflict", payload["code"])
	assert.Equal(t, "section_conflict", payload["error"])
	assert.Equal(t, "Esta sección cambió en otra pestaña o por otra persona.", payload["message"])
	data, ok := payload["data"].(map[string]interface{})
	require.True(t, ok, "current section in data")
	assert.Equal(t, "Otra pestaña", data["title"])
	assert.Empty(t, repo.conditionalSeen)
	assert.Zero(t, repo.plainUpdates)
}

// Matching at microsecond precision: the stored value carries nanoseconds the
// database would not keep, the client echoes the microsecond value.
func TestUpdateSection_MatchingExpectedUpdatedAtSavesConditionally(t *testing.T) {
	stored := models.EventSection{
		ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4()), Config: `{}`,
		UpdatedAt: time.Date(2026, 10, 12, 10, 0, 5, 123456789, time.UTC),
	}
	repo := &conditionalSectionRepo{stored: stored}
	code, _ := runSectionUpdate(t, stored, repo,
		`{"config":{"text":"nuevo"},"expected_updated_at":"2026-10-12T04:00:05.123456-06:00"}`)

	assert.Equal(t, http.StatusOK, code)
	require.Len(t, repo.conditionalSeen, 1)
	assert.True(t, eventsService.SectionUpdatedAtMatches(stored.UpdatedAt, repo.conditionalSeen[0]))
	assert.Zero(t, repo.plainUpdates)
}

// The conditional UPDATE is the real guard: a concurrent save that lands
// between the pre-check and the write still gets 409.
func TestUpdateSection_ConcurrentWriteLosesWithConflict(t *testing.T) {
	stored := models.EventSection{
		ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4()), Config: `{}`,
		UpdatedAt: time.Date(2026, 10, 12, 10, 0, 5, 0, time.UTC),
	}
	repo := &conditionalSectionRepo{stored: stored, conditionalErr: ports.ErrEventSectionConflict}
	code, payload := runSectionUpdate(t, stored, repo,
		`{"config":{"text":"nuevo"},"expected_updated_at":"2026-10-12T10:00:05Z"}`)

	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "section_conflict", payload["code"])
	assert.NotNil(t, payload["data"])
}

func TestUpdateSection_WithoutExpectedUpdatedAtKeepsLastWriteWins(t *testing.T) {
	stored := models.EventSection{ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4()), Config: `{}`, UpdatedAt: time.Now()}
	repo := &conditionalSectionRepo{stored: stored}
	code, _ := runSectionUpdate(t, stored, repo, `{"config":{"text":"nuevo"}}`)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 1, repo.plainUpdates)
	assert.Empty(t, repo.conditionalSeen)
}

func TestUpdateSection_InvalidExpectedUpdatedAtIsBadRequest(t *testing.T) {
	stored := models.EventSection{ID: uuid.Must(uuid.NewV4()), EventID: uuid.Must(uuid.NewV4()), Config: `{}`}
	repo := &conditionalSectionRepo{stored: stored}
	code, payload := runSectionUpdate(t, stored, repo, `{"expected_updated_at":"ayer"}`)

	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "invalid_expected_updated_at", payload["error"])
}
