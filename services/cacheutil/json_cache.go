package cacheutil

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"events-stocks/services/ports"
	"golang.org/x/sync/singleflight"
)

// jsonLoads collapses concurrent cache misses for the same cache, key and
// result type. A package-level group is safe here because completed calls are
// removed immediately by singleflight; it does not retain cache keys.
var jsonLoads singleflight.Group

type jsonLoadResult[T any] struct {
	value   T
	payload []byte
}

func GetOrLoadJSON[T any](
	ctx context.Context,
	cache ports.CacheRepository,
	key string,
	ttl time.Duration,
	load func() (T, error),
) (T, error) {
	if cache == nil {
		return load()
	}

	// Keep the hot path lock-free: cache hits never enter singleflight.
	if result, _, ok := readJSONCache[T](ctx, cache, key); ok {
		return result, nil
	}

	loaded, err, shared := jsonLoads.Do(jsonFlightKey[T](cache, key), func() (any, error) {
		// Another request may have filled the cache after our first read but
		// before this flight became the leader.
		if result, payload, ok := readJSONCache[T](ctx, cache, key); ok {
			return jsonLoadResult[T]{value: result, payload: payload}, nil
		}

		data, err := load()
		if err != nil {
			return nil, err
		}

		result := jsonLoadResult[T]{value: data}
		if payload, marshalErr := json.Marshal(data); marshalErr == nil {
			result.payload = payload
			_ = cache.SaveKey(ctx, key, string(payload), ttl)
		}
		return result, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}

	result, ok := loaded.(jsonLoadResult[T])
	if !ok {
		var zero T
		return zero, fmt.Errorf("cache load type mismatch for key %q", key)
	}

	// singleflight returns the same in-memory value to every waiter. Decode a
	// private copy when a result was shared so callers cannot race by mutating a
	// common slice, map or pointer. If a custom JSON implementation is not
	// round-trippable, preserve the previous fail-open behavior.
	if shared && len(result.payload) > 0 {
		var clone T
		if err := json.Unmarshal(result.payload, &clone); err == nil {
			return clone, nil
		}
	}

	return result.value, nil
}

func readJSONCache[T any](ctx context.Context, cache ports.CacheRepository, key string) (T, []byte, bool) {
	var result T
	cached, err := cache.GetKey(ctx, key)
	if err != nil || cached == "" {
		return result, nil, false
	}

	payload := []byte(cached)
	if err := json.Unmarshal(payload, &result); err != nil {
		return result, nil, false
	}
	return result, payload, true
}

func jsonFlightKey[T any](cache ports.CacheRepository, key string) string {
	// Production repositories and service test doubles use pointer receivers.
	// Including the pointer keeps independent cache backends from sharing work.
	var cachePointer uintptr
	value := reflect.ValueOf(cache)
	if value.IsValid() && value.Kind() == reflect.Pointer && !value.IsNil() {
		cachePointer = value.Pointer()
	}

	return fmt.Sprintf("%T@%x|%v|%q", cache, cachePointer, reflect.TypeFor[T](), key)
}
