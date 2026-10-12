package moments

import (
	"net/http"
	"sort"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	momentsService "events-stocks/services/moments"
	resourcesService "events-stocks/services/resources"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// purgeMomentRepo is a moment repository whose rows can be soft-deleted and
// that answers the purge's reference check from the live rows.
type purgeMomentRepo struct {
	*mockMomentRepo
	live    map[uuid.UUID]models.Moment
	deleted map[uuid.UUID]models.Moment
}

func newPurgeMomentRepo(moments ...models.Moment) *purgeMomentRepo {
	repo := &purgeMomentRepo{mockMomentRepo: &mockMomentRepo{}, live: map[uuid.UUID]models.Moment{}, deleted: map[uuid.UUID]models.Moment{}}
	for _, moment := range moments {
		repo.live[moment.ID] = moment
	}
	return repo
}

func (r *purgeMomentRepo) GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	moment, ok := r.live[id]
	if !ok {
		return &models.Moment{}, gorm.ErrRecordNotFound
	}
	return &moment, nil
}

func (r *purgeMomentRepo) GetMomentsByIDs(ids []uuid.UUID) ([]models.Moment, error) {
	var out []models.Moment
	for _, id := range ids {
		if moment, ok := r.live[id]; ok {
			out = append(out, moment)
		}
	}
	return out, nil
}

func (r *purgeMomentRepo) DeleteMoment(id uuid.UUID) error {
	r.deleted[id] = r.live[id]
	delete(r.live, id)
	return nil
}

func (r *purgeMomentRepo) BulkDeleteMoments(ids []uuid.UUID) error {
	for _, id := range ids {
		_ = r.DeleteMoment(id)
	}
	return nil
}

func (r *purgeMomentRepo) GetMomentByIDUnscoped(id uuid.UUID) (*models.Moment, error) {
	if moment, ok := r.deleted[id]; ok {
		return &moment, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *purgeMomentRepo) LiveMomentKeyReferences(eventID uuid.UUID, excludeIDs []uuid.UUID, keys []string) (map[string]bool, error) {
	excluded := map[uuid.UUID]bool{}
	for _, id := range excludeIDs {
		excluded[id] = true
	}
	referenced := map[string]bool{}
	for _, moment := range r.live {
		if excluded[moment.ID] || moment.EventID == nil || *moment.EventID != eventID {
			continue
		}
		for _, key := range keys {
			if moment.ContentURL == key || moment.ProcessingInputKey == key || moment.ThumbnailURL == key {
				referenced[key] = true
			}
		}
	}
	return referenced, nil
}

func (r *purgeMomentRepo) BeginMediaProcessingJob(uuid.UUID, uuid.UUID, string, string) (int64, error) {
	return 0, nil
}

func (r *purgeMomentRepo) ApplyMediaProcessingUpdate(uuid.UUID, uuid.UUID, string, int64, []string, string, string, string, string, int64, int64, int64, models.MediaVariants) (bool, error) {
	return true, nil
}

// usePurgeFixture wires the repo and a recording object store, and runs every
// purge synchronously.
func usePurgeFixture(t *testing.T, repo *purgeMomentRepo) *mockObjectStorage {
	t.Helper()
	storage := &mockObjectStorage{}
	origSvc, origRes, origRun := momentSvc, adminResSvc, runMomentMediaPurge
	momentSvc = momentsService.NewMomentService(repo, &mockCacheRepo{})
	adminResSvc = resourcesService.NewResourceService(&models.Config{AwsBucketName: "events-bucket"}, resourcesService.ResourceServiceDeps{Storage: storage})
	runMomentMediaPurge = func(purge func()) { purge() }
	t.Cleanup(func() { momentSvc, adminResSvc, runMomentMediaPurge = origSvc, origRes, origRun })
	return storage
}

func authorizeAsRootFor(t *testing.T, c interface{ Set(string, interface{}) }, repo *purgeMomentRepo) {
	t.Helper()
	c.Set("cognito_sub", "test-sub")
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) { return &models.Event{ID: id}, nil },
		GetMomentByID:   func(id uuid.UUID) (*models.Moment, error) { return repo.GetMomentByID(id) },
	})
	t.Cleanup(restore)
}

func processedMoment(eventID uuid.UUID) models.Moment {
	id := uuid.Must(uuid.NewV4())
	prefix := "moments/" + eventID.String() + "/"
	return models.Moment{
		ID: id, EventID: &eventID, MediaBucket: "events-bucket",
		ContentURL:         prefix + "photos/" + id.String() + ".webp",
		ProcessingInputKey: prefix + "raw/" + id.String() + ".jpg",
		ThumbnailURL:       prefix + "photos/" + id.String() + "-thumb.webp",
		MediaVariants: models.MediaVariants{
			{ObjectKey: prefix + "photos/" + id.String() + "-640.webp", Width: 640, Format: "webp"},
		},
	}
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// MOM-3: deleting a moment removes every object it owns, after the delete.
func TestDeleteMoment_PurgesEveryObjectOfTheMoment(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	moment := processedMoment(eventID)
	repo := newPurgeMomentRepo(moment)
	storage := usePurgeFixture(t, repo)

	c, rec := newEchoCtx(http.MethodDelete, "/moments/"+moment.ID.String(), "")
	c.SetParamNames("id")
	c.SetParamValues(moment.ID.String())
	authorizeAsRootFor(t, c, repo)
	require.NoError(t, DeleteMoment(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, sortedStrings([]string{
		moment.ContentURL, moment.ProcessingInputKey, moment.ThumbnailURL, moment.MediaVariants[0].ObjectKey,
	}), sortedStrings(storage.deletedObjects))
	_, stillLive := repo.live[moment.ID]
	assert.False(t, stillLive, "the moment is no longer served")
}

// MOM-3 bulk ("Rechazar todos"): every deleted moment is purged, a raw object
// shared with a duplicate that stays alive is kept, and keys outside the
// moment's event are never touched.
func TestBulkDeleteMoments_PurgesButKeepsSharedAndForeignKeys(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	first, second := processedMoment(eventID), processedMoment(eventID)
	survivor := processedMoment(eventID)
	survivor.ProcessingInputKey = first.ProcessingInputKey // duplicate of first's upload
	foreign := "moments/" + uuid.Must(uuid.NewV4()).String() + "/photos/x.webp"
	second.ThumbnailURL = foreign
	external := "https://cdn.example.com/legacy.jpg"
	second.ContentURL = external
	repo := newPurgeMomentRepo(first, second, survivor)
	storage := usePurgeFixture(t, repo)

	body := `{"ids":["` + first.ID.String() + `","` + second.ID.String() + `"]}`
	c, rec := newEchoCtx(http.MethodDelete, "/moments/bulk", body)
	authorizeAsRootFor(t, c, repo)
	require.NoError(t, BulkDeleteMoments(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, storage.deletedObjects, first.ProcessingInputKey, "shared with a live duplicate")
	assert.NotContains(t, storage.deletedObjects, foreign)
	assert.Contains(t, storage.deletedObjects, first.ContentURL)
	assert.Contains(t, storage.deletedObjects, second.ProcessingInputKey)
	for _, key := range storage.deletedObjects {
		assert.Contains(t, key, "moments/"+eventID.String()+"/")
	}
}

// MOM-4: a callback for a deleted moment is a 200 no-op (no DLQ retries), and
// the objects the late job wrote are removed.
func TestUpdateMomentContent_DeletedMomentIsANoOpAndPurgesTheOutput(t *testing.T) {
	t.Setenv("INTERNAL_API_SECRET", "secret")
	eventID := uuid.Must(uuid.NewV4())
	moment := processedMoment(eventID)
	moment.ContentURL = moment.ProcessingInputKey
	moment.ThumbnailURL = ""
	moment.MediaVariants = nil
	repo := newPurgeMomentRepo(moment)
	require.NoError(t, repo.DeleteMoment(moment.ID))
	storage := usePurgeFixture(t, repo)

	output := "moments/" + eventID.String() + "/photos/" + moment.ID.String() + ".webp"
	variant := "moments/" + eventID.String() + "/photos/" + moment.ID.String() + "-320.webp"
	body := `{"event_id":"` + eventID.String() + `","job_id":"j","generation":1,"object_key":"` + output + `","processing_status":"done",
		"media_variants":[{"object_key":"` + variant + `","width":320,"format":"webp"}]}`
	c, rec := newEchoCtx(http.MethodPut, "/moments/"+moment.ID.String()+"/content", body)
	c.Request().Header.Set("X-Internal-Secret", "secret")
	c.SetParamNames("id")
	c.SetParamValues(moment.ID.String())
	require.NoError(t, UpdateMomentContent(c))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"ignored":true`)
	assert.ElementsMatch(t, []string{moment.ProcessingInputKey, output, variant}, storage.deletedObjects)
}

// A callback for an id that never existed is also a 200 no-op.
func TestUpdateMomentContent_UnknownMomentIsANoOp(t *testing.T) {
	t.Setenv("INTERNAL_API_SECRET", "secret")
	repo := newPurgeMomentRepo()
	storage := usePurgeFixture(t, repo)
	id := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/moments/"+id.String()+"/content", `{"processing_status":"failed","error_message":"boom"}`)
	c.Request().Header.Set("X-Internal-Secret", "secret")
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	require.NoError(t, UpdateMomentContent(c))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, storage.deletedObjects)
}
