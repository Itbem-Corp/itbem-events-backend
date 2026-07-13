package events

import (
	"context"
	"events-stocks/services/ports"
	"events-stocks/utils"
)

func invalidateEventsCache(cache ports.CacheRepository) error {
	if cache == nil {
		return nil
	}
	_ = cache.DeleteKeysByPattern(context.Background(), "*:"+utils.RedisServiceEventsKey)
	return nil
}
