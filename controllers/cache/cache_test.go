package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	"github.com/labstack/echo/v4"
)

type recordingCacheStore struct {
	deleteContext context.Context
	flushContext  context.Context
}

type requestContextKey struct{}

func (store *recordingCacheStore) DeleteKey(ctx context.Context, _ string) error {
	store.deleteContext = ctx
	return nil
}

func (store *recordingCacheStore) FlushAll(ctx context.Context) error {
	store.flushContext = ctx
	return nil
}

func primaryRootContext(t *testing.T, request *http.Request) echo.Context {
	t.Helper()
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
		},
	})
	t.Cleanup(restore)

	e := echo.New()
	c := e.NewContext(request, httptest.NewRecorder())
	c.Set("cognito_sub", "primary-root")
	return c
}

func TestCacheFlushHandlersUseRequestContext(t *testing.T) {
	previousStore := store
	t.Cleanup(func() { store = previousStore })

	cacheStore := &recordingCacheStore{}
	InitCacheController(cacheStore)

	keyContext := context.WithValue(context.Background(), requestContextKey{}, "delete")
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/cache/flush/events", nil).WithContext(keyContext)
	deleteContext := primaryRootContext(t, deleteRequest)
	deleteContext.SetParamNames("key")
	deleteContext.SetParamValues("events")
	if err := FlushKey(deleteContext); err != nil {
		t.Fatalf("FlushKey returned an error: %v", err)
	}
	if cacheStore.deleteContext != keyContext {
		t.Fatal("FlushKey did not propagate the request context to the cache store")
	}

	allContext := context.WithValue(context.Background(), requestContextKey{}, "flush-all")
	allRequest := httptest.NewRequest(http.MethodDelete, "/cache/flush-all", nil).WithContext(allContext)
	if err := FlushAll(primaryRootContext(t, allRequest)); err != nil {
		t.Fatalf("FlushAll returned an error: %v", err)
	}
	if cacheStore.flushContext != allContext {
		t.Fatal("FlushAll did not propagate the request context to the cache store")
	}
}

// The DELETE routes and the deprecated GET aliases share these handlers, so
// both stay primary-root-only: Root 2 and standard users never reach Redis.
func TestCacheFlushHandlersRequirePrimaryRoot(t *testing.T) {
	previousStore := store
	t.Cleanup(func() { store = previousStore })

	for name, requester := range map[string]models.User{
		"operational root":                {RootLevel: models.RootLevelOperational},
		"legacy is_root operational root": {IsRoot: true, RootLevel: models.RootLevelOperational},
		"standard user":                   {},
	} {
		t.Run(name, func(t *testing.T) {
			cacheStore := &recordingCacheStore{}
			InitCacheController(cacheStore)
			restore := authz.ReplaceHooksForTest(authz.Hooks{
				SyncUser: func(string) (*models.User, error) {
					user := requester
					return &user, nil
				},
			})
			t.Cleanup(restore)

			for _, handler := range []echo.HandlerFunc{FlushKey, FlushAll} {
				recorder := httptest.NewRecorder()
				c := echo.New().NewContext(httptest.NewRequest(http.MethodDelete, "/api/cache/flush-all", nil), recorder)
				c.Set("cognito_sub", "cache-test-sub")
				c.SetParamNames("key")
				c.SetParamValues("events")
				if err := handler(c); err != nil {
					t.Fatalf("handler returned an error: %v", err)
				}
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
				}
			}
			if cacheStore.deleteContext != nil || cacheStore.flushContext != nil {
				t.Fatal("a denied flush must not reach the cache store")
			}
		})
	}
}
