package redisrepository

import (
	"context"
	"errors"
	"events-stocks/configuration"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestDecrementNeverCreatesOrMakesUploadCounterNegative(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previous := configuration.RedisClient
	configuration.RedisClient = client
	t.Cleanup(func() {
		configuration.RedisClient = previous
		_ = client.Close()
	})

	ctx := context.Background()
	count, err := Decrement(ctx, "missing")
	if err != nil {
		t.Fatalf("decrement missing key: %v", err)
	}
	if count != 0 || server.Exists("missing") {
		t.Fatalf("missing counter decrement = %d, exists=%v; want 0,false", count, server.Exists("missing"))
	}

	server.Set("upload", "2")
	server.SetTTL("upload", time.Hour)
	count, err = Decrement(ctx, "upload")
	if err != nil {
		t.Fatalf("decrement existing key: %v", err)
	}
	value, getErr := server.Get("upload")
	if getErr != nil {
		t.Fatalf("read decremented counter: %v", getErr)
	}
	if count != 1 || value != "1" {
		t.Fatalf("counter after decrement = %d/%q, want 1", count, value)
	}
	if ttl := server.TTL("upload"); ttl != time.Hour {
		t.Fatalf("counter TTL = %v, want %v", ttl, time.Hour)
	}

	count, err = Decrement(ctx, "upload")
	if err != nil {
		t.Fatalf("decrement final slot: %v", err)
	}
	if count != 0 || server.Exists("upload") {
		t.Fatalf("final decrement = %d, exists=%v; want 0,false", count, server.Exists("upload"))
	}
}

func TestDeleteKeysByPatternScansAndUnlinksInBatches(t *testing.T) {
	type scanResult struct {
		keys   []string
		cursor uint64
	}
	results := []scanResult{
		{keys: []string{"cache:a", "cache:b"}, cursor: 17},
		{cursor: 29},
		{keys: []string{"cache:c"}},
	}

	var (
		scanCursors []uint64
		unlinked    [][]string
	)
	err := deleteKeysByPattern(
		context.Background(),
		"cache:*",
		func(_ context.Context, cursor uint64, pattern string, count int64) ([]string, uint64, error) {
			if pattern != "cache:*" {
				t.Fatalf("pattern = %q, want cache:*", pattern)
			}
			if count != deletePatternScanCount {
				t.Fatalf("SCAN count = %d, want %d", count, deletePatternScanCount)
			}
			scanCursors = append(scanCursors, cursor)
			result := results[len(scanCursors)-1]
			return result.keys, result.cursor, nil
		},
		func(_ context.Context, keys ...string) error {
			unlinked = append(unlinked, append([]string(nil), keys...))
			return nil
		},
	)
	if err != nil {
		t.Fatalf("deleteKeysByPattern returned error: %v", err)
	}
	if want := []uint64{0, 17, 29}; !reflect.DeepEqual(scanCursors, want) {
		t.Fatalf("SCAN cursors = %v, want %v", scanCursors, want)
	}
	if want := [][]string{{"cache:a", "cache:b"}, {"cache:c"}}; !reflect.DeepEqual(unlinked, want) {
		t.Fatalf("UNLINK batches = %v, want %v", unlinked, want)
	}
}

func TestDeleteKeysByPatternReturnsScanErrorWithoutUnlink(t *testing.T) {
	wantErr := errors.New("scan failed")
	unlinked := false
	err := deleteKeysByPattern(
		context.Background(),
		"cache:*",
		func(context.Context, uint64, string, int64) ([]string, uint64, error) {
			return nil, 0, wantErr
		},
		func(context.Context, ...string) error {
			unlinked = true
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if unlinked {
		t.Fatal("UNLINK called after SCAN failure")
	}
}

func TestDeleteKeysByPatternCapsUnlinkBatchWhenScanExceedsCountHint(t *testing.T) {
	keys := make([]string, deletePatternUnlinkBatchSize+1)
	for index := range keys {
		keys[index] = "cache:key"
	}
	var batchSizes []int
	err := deleteKeysByPattern(
		context.Background(),
		"cache:*",
		func(context.Context, uint64, string, int64) ([]string, uint64, error) {
			return keys, 0, nil
		},
		func(_ context.Context, batch ...string) error {
			batchSizes = append(batchSizes, len(batch))
			return nil
		},
	)
	if err != nil {
		t.Fatalf("deleteKeysByPattern returned error: %v", err)
	}
	if want := []int{deletePatternUnlinkBatchSize, 1}; !reflect.DeepEqual(batchSizes, want) {
		t.Fatalf("UNLINK batch sizes = %v, want %v", batchSizes, want)
	}
}

func TestDeleteKeysByPatternStopsOnUnlinkError(t *testing.T) {
	wantErr := errors.New("unlink failed")
	scanCalls := 0
	err := deleteKeysByPattern(
		context.Background(),
		"cache:*",
		func(context.Context, uint64, string, int64) ([]string, uint64, error) {
			scanCalls++
			return []string{"cache:a"}, 42, nil
		},
		func(context.Context, ...string) error { return wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if scanCalls != 1 {
		t.Fatalf("SCAN calls = %d, want 1", scanCalls)
	}
}
