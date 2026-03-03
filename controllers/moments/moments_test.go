package moments

import (
	"context"
	"errors"
	"events-stocks/models"
	"events-stocks/services/ports"
	momentsService "events-stocks/services/moments"
	customValidator "events-stocks/middleware/validator"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEchoCtx(method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	e.Validator = customValidator.New()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockMomentRepo struct{}

func (m *mockMomentRepo) CreateMoment(obj *models.Moment) error                  { return nil }
func (m *mockMomentRepo) UpdateMoment(obj *models.Moment) error                  { return nil }
func (m *mockMomentRepo) DeleteMoment(id uuid.UUID) error                        { return nil }
func (m *mockMomentRepo) GetMomentByID(id uuid.UUID) (*models.Moment, error)     { return &models.Moment{ID: id}, nil }
func (m *mockMomentRepo) ListMoments() ([]models.Moment, error)                  { return nil, nil }
func (m *mockMomentRepo) ListByEventID(eventID uuid.UUID, approvedOnly bool) ([]models.Moment, error) { return nil, nil }
func (m *mockMomentRepo) UpdateMomentContent(id uuid.UUID, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) error {
	return nil
}
func (m *mockMomentRepo) ListForDashboard(eventID uuid.UUID) ([]models.Moment, error)              { return nil, nil }
func (m *mockMomentRepo) ListApprovedForWall(eventID uuid.UUID, page, limit int) ([]models.Moment, int64, error) { return nil, 0, nil }
func (m *mockMomentRepo) BulkUpdateApproval(ids []uuid.UUID, isApproved bool) error               { return nil }
func (m *mockMomentRepo) GetDistinctEventIDsByMomentIDs(ids []uuid.UUID) ([]uuid.UUID, error)    { return nil, nil }
func (m *mockMomentRepo) SummaryByEventIDs(eventIDs []uuid.UUID) ([]models.MomentSummary, error) { return nil, nil }
func (m *mockMomentRepo) BulkReorder(items []models.MomentOrderItem) error                      { return nil }

var _ ports.MomentRepository = (*mockMomentRepo)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(_, _ string) error                                    { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error           { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error)              { return "", errors.New("miss") }
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error   { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestDeleteMoment_InvalidUUID_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/moments/bad-id", "")
	c.SetParamNames("id")
	c.SetParamValues("bad-id")
	require.NoError(t, DeleteMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetMoment_InvalidUUID_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/moments/bad-id", "")
	c.SetParamNames("id")
	c.SetParamValues("bad-id")
	require.NoError(t, GetMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateMoment_InvalidBody_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/moments", `{invalid json}`)
	require.NoError(t, CreateMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateMoment_ValidBody_Returns201(t *testing.T) {
	svc := momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/moments", `{"name":"Wedding"}`)
	require.NoError(t, CreateMoment(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestUpdateMoment_InvalidBody_Returns400(t *testing.T) {
	orig := momentSvc
	momentSvc = nil
	defer func() { momentSvc = orig }()

	id := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/moments/"+id.String(), `{invalid json}`)
	c.SetParamNames("id")
	c.SetParamValues(id.String())
	require.NoError(t, UpdateMoment(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListMoments_Returns200(t *testing.T) {
	svc := momentsService.NewMomentService(&mockMomentRepo{}, &mockCacheRepo{})
	orig := momentSvc
	momentSvc = svc
	defer func() { momentSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/moments", "")
	require.NoError(t, ListMoments(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}
