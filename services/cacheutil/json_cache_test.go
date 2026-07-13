package cacheutil

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errCacheMiss = errors.New("cache miss")

type testCache struct {
	mu     sync.Mutex
	values map[string]string
	gets   atomic.Int64
	saves  atomic.Int64
}

func newTestCache() *testCache {
	return &testCache{values: make(map[string]string)}
}

func (c *testCache) Invalidate(resource string, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key+":"+resource)
	return nil
}

func (c *testCache) DeleteKeysByPattern(context.Context, string) error { return nil }

func (c *testCache) GetKey(_ context.Context, key string) (string, error) {
	c.gets.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	if !ok {
		return "", errCacheMiss
	}
	return value, nil
}

func (c *testCache) SaveKey(_ context.Context, key, value string, _ time.Duration) error {
	c.saves.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = value
	return nil
}

func TestGetOrLoadJSON_CollapsesConcurrentMisses(t *testing.T) {
	const callers = 32

	cache := newTestCache()
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	var loadOnce sync.Once
	var loadCalls atomic.Int64

	load := func() ([]string, error) {
		loadCalls.Add(1)
		loadOnce.Do(func() { close(loadStarted) })
		<-releaseLoad
		return []string{"premium", "fast"}, nil
	}

	start := make(chan struct{})
	results := make(chan []string, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			result, err := GetOrLoadJSON(context.Background(), cache, "events", time.Minute, load)
			results <- result
			errs <- err
		}()
	}

	close(start)
	select {
	case <-loadStarted:
	case <-time.After(time.Second):
		t.Fatal("loader did not start")
	}

	// Give every caller time to observe the miss and join the in-flight load.
	deadline := time.Now().Add(time.Second)
	for cache.gets.Load() < callers+1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(releaseLoad)

	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("caller %d returned error: %v", i, err)
		}
		result := <-results
		if fmt.Sprint(result) != "[premium fast]" {
			t.Fatalf("caller %d returned %v", i, result)
		}
	}

	if got := loadCalls.Load(); got != 1 {
		t.Fatalf("loader called %d times, want 1", got)
	}
	if got := cache.saves.Load(); got != 1 {
		t.Fatalf("cache saved %d times, want 1", got)
	}

	result, err := GetOrLoadJSON(context.Background(), cache, "events", time.Minute, func() ([]string, error) {
		t.Fatal("cache hit invoked loader")
		return nil, nil
	})
	if err != nil || fmt.Sprint(result) != "[premium fast]" {
		t.Fatalf("cache hit returned result=%v err=%v", result, err)
	}
}

func TestGetOrLoadJSON_DoesNotCacheLoaderErrors(t *testing.T) {
	cache := newTestCache()
	wantErr := errors.New("database unavailable")
	var loadCalls atomic.Int64

	load := func() ([]string, error) {
		loadCalls.Add(1)
		return nil, wantErr
	}

	for i := 0; i < 2; i++ {
		result, err := GetOrLoadJSON(context.Background(), cache, "events-error", time.Minute, load)
		if !errors.Is(err, wantErr) {
			t.Fatalf("call %d returned result=%v err=%v, want %v", i, result, err, wantErr)
		}
	}

	if got := loadCalls.Load(); got != 2 {
		t.Fatalf("loader called %d times, want retry on each request", got)
	}
	if got := cache.saves.Load(); got != 0 {
		t.Fatalf("cache saved %d failed loads, want 0", got)
	}
}

func TestGetOrLoadJSON_SeparatesCacheInstances(t *testing.T) {
	firstCache := newTestCache()
	secondCache := newTestCache()
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = GetOrLoadJSON(context.Background(), firstCache, "shared-key", time.Minute, func() (string, error) {
			close(firstStarted)
			<-release
			return "first", nil
		})
	}()
	go func() {
		defer wg.Done()
		_, _ = GetOrLoadJSON(context.Background(), secondCache, "shared-key", time.Minute, func() (string, error) {
			close(secondStarted)
			<-release
			return "second", nil
		})
	}()

	for name, started := range map[string]<-chan struct{}{
		"first":  firstStarted,
		"second": secondStarted,
	} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("%s cache loader was incorrectly coalesced", name)
		}
	}

	close(release)
	wg.Wait()
}
