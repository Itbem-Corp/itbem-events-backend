package colors

import (
	"context"
	"errors"
	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockColorCacheRepo struct {
	GetKeyFunc     func(ctx context.Context, key string) (string, error)
	SaveKeyFunc    func(ctx context.Context, key string, value string, ttl time.Duration) error
	InvalidateFunc func(resource string, key string) error
}

func (m *mockColorCacheRepo) GetKey(ctx context.Context, key string) (string, error) {
	if m.GetKeyFunc != nil {
		return m.GetKeyFunc(ctx, key)
	}
	return "", errors.New("cache miss")
}

func (m *mockColorCacheRepo) SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error {
	if m.SaveKeyFunc != nil {
		return m.SaveKeyFunc(ctx, key, value, ttl)
	}
	return nil
}

func (m *mockColorCacheRepo) Invalidate(resource string, key string) error {
	if m.InvalidateFunc != nil {
		return m.InvalidateFunc(resource, key)
	}
	return nil
}

func (m *mockColorCacheRepo) DeleteKeysByPattern(ctx context.Context, pattern string) error {
	return nil
}
func (m *mockColorCacheRepo) DeleteKey(ctx context.Context, key string) error          { return nil }
func (m *mockColorCacheRepo) Increment(ctx context.Context, key string) (int64, error) { return 0, nil }
func (m *mockColorCacheRepo) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return nil
}
func (m *mockColorCacheRepo) FlushAll(ctx context.Context) error { return nil }

var _ ports.CacheRepository = (*mockColorCacheRepo)(nil)

type mockColorRepo struct {
	CreateColorFunc          func(color *models.Color) error
	UpdateColorFunc          func(color *models.Color) error
	DeleteColorFunc          func(id uuid.UUID) error
	GetColorByIDFunc         func(id uuid.UUID) (*models.Color, error)
	ListColorsFunc           func() ([]models.Color, error)
	CreateMultipleColorsFunc func(colors []models.Color) error
	CreatePaletteFunc        func(palette *models.ColorPalette) error
	UpdatePaletteFunc        func(palette *models.ColorPalette) error
	DeletePaletteFunc        func(id uuid.UUID) error
	GetColorPaletteByIDFunc  func(id uuid.UUID) (*models.ColorPalette, error)
	ListColorPalettesFunc    func() ([]models.ColorPalette, error)
	CreatePatternFunc        func(pattern *models.ColorPalettePattern) error
	UpdatePatternFunc        func(pattern *models.ColorPalettePattern) error
	DeletePatternFunc        func(id uuid.UUID) error
	GetColorPatternByIDFunc  func(id uuid.UUID) (*models.ColorPalettePattern, error)
	ListAllPatternsFunc      func() ([]models.ColorPalettePattern, error)
}

func (m *mockColorRepo) CreateColor(color *models.Color) error {
	if m.CreateColorFunc != nil {
		return m.CreateColorFunc(color)
	}
	return nil
}
func (m *mockColorRepo) UpdateColor(color *models.Color) error {
	if m.UpdateColorFunc != nil {
		return m.UpdateColorFunc(color)
	}
	return nil
}
func (m *mockColorRepo) DeleteColor(id uuid.UUID) error {
	if m.DeleteColorFunc != nil {
		return m.DeleteColorFunc(id)
	}
	return nil
}
func (m *mockColorRepo) GetColorByID(id uuid.UUID) (*models.Color, error) {
	if m.GetColorByIDFunc != nil {
		return m.GetColorByIDFunc(id)
	}
	return &models.Color{ID: id}, nil
}
func (m *mockColorRepo) ListColors() ([]models.Color, error) {
	if m.ListColorsFunc != nil {
		return m.ListColorsFunc()
	}
	return nil, nil
}
func (m *mockColorRepo) CreateMultipleColors(colors []models.Color) error {
	if m.CreateMultipleColorsFunc != nil {
		return m.CreateMultipleColorsFunc(colors)
	}
	return nil
}
func (m *mockColorRepo) CreatePalette(palette *models.ColorPalette) error {
	if m.CreatePaletteFunc != nil {
		return m.CreatePaletteFunc(palette)
	}
	return nil
}
func (m *mockColorRepo) UpdatePalette(palette *models.ColorPalette) error {
	if m.UpdatePaletteFunc != nil {
		return m.UpdatePaletteFunc(palette)
	}
	return nil
}
func (m *mockColorRepo) DeletePalette(id uuid.UUID) error {
	if m.DeletePaletteFunc != nil {
		return m.DeletePaletteFunc(id)
	}
	return nil
}
func (m *mockColorRepo) GetColorPaletteByID(id uuid.UUID) (*models.ColorPalette, error) {
	if m.GetColorPaletteByIDFunc != nil {
		return m.GetColorPaletteByIDFunc(id)
	}
	return &models.ColorPalette{ID: id}, nil
}
func (m *mockColorRepo) ListColorPalettes() ([]models.ColorPalette, error) {
	if m.ListColorPalettesFunc != nil {
		return m.ListColorPalettesFunc()
	}
	return nil, nil
}
func (m *mockColorRepo) CreatePattern(pattern *models.ColorPalettePattern) error {
	if m.CreatePatternFunc != nil {
		return m.CreatePatternFunc(pattern)
	}
	return nil
}
func (m *mockColorRepo) UpdatePattern(pattern *models.ColorPalettePattern) error {
	if m.UpdatePatternFunc != nil {
		return m.UpdatePatternFunc(pattern)
	}
	return nil
}
func (m *mockColorRepo) DeletePattern(id uuid.UUID) error {
	if m.DeletePatternFunc != nil {
		return m.DeletePatternFunc(id)
	}
	return nil
}
func (m *mockColorRepo) GetColorPatternByID(id uuid.UUID) (*models.ColorPalettePattern, error) {
	if m.GetColorPatternByIDFunc != nil {
		return m.GetColorPatternByIDFunc(id)
	}
	return &models.ColorPalettePattern{ID: id}, nil
}
func (m *mockColorRepo) ListAllPatterns() ([]models.ColorPalettePattern, error) {
	if m.ListAllPatternsFunc != nil {
		return m.ListAllPatternsFunc()
	}
	return nil, nil
}

var _ ports.ColorRepository = (*mockColorRepo)(nil)

func TestColorService_ListColorPalettes_UsesNamedCacheKeyAndTTL(t *testing.T) {
	paletteID := uuid.Must(uuid.NewV4())
	repo := &mockColorRepo{
		ListColorPalettesFunc: func() ([]models.ColorPalette, error) {
			return []models.ColorPalette{{ID: paletteID, Name: "Dorada"}}, nil
		},
	}
	var getKey string
	var saveKey string
	var savedTTL time.Duration
	cache := &mockColorCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			getKey = key
			return "", errors.New("cache miss")
		},
		SaveKeyFunc: func(ctx context.Context, key string, value string, ttl time.Duration) error {
			saveKey = key
			savedTTL = ttl
			return nil
		},
	}

	result, err := NewColorService(repo, cache).ListColorPalettes()

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, paletteID, result[0].ID)
	assert.Equal(t, "all:"+utils.RedisColorPalettesKey, getKey)
	assert.Equal(t, "all:"+utils.RedisColorPalettesKey, saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisColorPalettesKey], savedTTL)
}

func TestColorService_ListColorPalettePatterns_UsesNamedCacheKeyAndTTL(t *testing.T) {
	patternID := uuid.Must(uuid.NewV4())
	repo := &mockColorRepo{
		ListAllPatternsFunc: func() ([]models.ColorPalettePattern, error) {
			return []models.ColorPalettePattern{{ID: patternID, Key: "primary"}}, nil
		},
	}
	var getKey string
	var saveKey string
	var savedTTL time.Duration
	cache := &mockColorCacheRepo{
		GetKeyFunc: func(ctx context.Context, key string) (string, error) {
			getKey = key
			return "", errors.New("cache miss")
		},
		SaveKeyFunc: func(ctx context.Context, key string, value string, ttl time.Duration) error {
			saveKey = key
			savedTTL = ttl
			return nil
		},
	}

	result, err := NewColorService(repo, cache).ListColorPalettePatterns()

	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, patternID, result[0].ID)
	assert.Equal(t, "all:"+utils.RedisColorPalettePatternsKey, getKey)
	assert.Equal(t, "all:"+utils.RedisColorPalettePatternsKey, saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisColorPalettePatternsKey], savedTTL)
}

func TestColorService_PalettePatternMutationsInvalidatePatternsAndPalettes(t *testing.T) {
	var invalidated []string
	cache := &mockColorCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			invalidated = append(invalidated, key+":"+resource)
			return nil
		},
	}
	svc := NewColorService(&mockColorRepo{}, cache)
	patternID := uuid.Must(uuid.NewV4())

	require.NoError(t, svc.CreateColorPalettePattern(&models.ColorPalettePattern{ID: patternID}))
	require.NoError(t, svc.UpdateColorPalettePattern(&models.ColorPalettePattern{ID: patternID}))
	require.NoError(t, svc.DeleteColorPalettePattern(patternID))

	assert.Equal(t, []string{
		"all:" + utils.RedisColorPalettePatternsKey,
		"all:" + utils.RedisColorPalettesKey,
		"all:" + utils.RedisColorPalettePatternsKey,
		"all:" + utils.RedisColorPalettesKey,
		"all:" + utils.RedisColorPalettePatternsKey,
		"all:" + utils.RedisColorPalettesKey,
	}, invalidated)
}

func TestColorService_ColorMutationsInvalidateDependentCatalogs(t *testing.T) {
	var invalidated []string
	cache := &mockColorCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			invalidated = append(invalidated, key+":"+resource)
			return nil
		},
	}
	svc := NewColorService(&mockColorRepo{}, cache)

	require.NoError(t, svc.CreateColor(&models.Color{ID: uuid.Must(uuid.NewV4())}))

	assert.Equal(t, []string{
		"all:" + utils.RedisColorsServiceKey,
		"all:" + utils.RedisColorPalettesKey,
		"all:" + utils.RedisColorPalettePatternsKey,
	}, invalidated)
}

func TestColorService_MutationsAllowNilCacheAndIgnoreInvalidationErrors(t *testing.T) {
	paletteID := uuid.Must(uuid.NewV4())
	require.NoError(t, NewColorService(&mockColorRepo{}, nil).UpdateColorPalette(&models.ColorPalette{ID: paletteID}))

	cache := &mockColorCacheRepo{
		InvalidateFunc: func(resource string, key string) error {
			return errors.New("redis unavailable")
		},
	}
	err := NewColorService(&mockColorRepo{}, cache).UpdateColorPalettePattern(&models.ColorPalettePattern{ID: uuid.Must(uuid.NewV4())})
	require.NoError(t, err)
}
