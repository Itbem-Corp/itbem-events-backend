package phrases

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	gormrepo "events-stocks/repositories/gormrepository"
	redisrepository "events-stocks/repositories/redisrepository"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
)

const (
	phraseCacheTTL = 1 * time.Hour
	defaultCount   = 15
	maxCount       = 30
)

// GetPhrases handles GET /api/events/phrases?type=WEDDING&count=15
// Returns N random phrases for the given event type.
// Cached in Redis for 1 hour per (type, count) pair.
func GetPhrases(c echo.Context) error {
	ctx := c.Request().Context()

	eventType := strings.ToUpper(strings.TrimSpace(c.QueryParam("type")))
	if eventType == "" {
		eventType = "DEFAULT"
	}

	count, err := strconv.Atoi(c.QueryParam("count"))
	if err != nil || count <= 0 {
		count = defaultCount
	}
	if count > maxCount {
		count = maxCount
	}

	cacheKey := fmt.Sprintf("phrases:%s:%d", eventType, count)

	// Try Redis cache first
	if cached, err := redisrepository.GetKey(ctx, cacheKey); err == nil && cached != "" {
		var phrases []string
		if json.Unmarshal([]byte(cached), &phrases) == nil {
			return utils.Success(c, http.StatusOK, "Phrases retrieved", map[string]interface{}{
				"phrases": phrases,
			})
		}
	}

	// Fetch from DB using QueryOptions.Filters (map[string]interface{})
	var rows []models.EventPhrase
	err = gormrepo.GetList(&rows, gormrepo.QueryOptions{
		Filters: map[string]interface{}{"event_type": eventType},
	})
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to fetch phrases", err.Error())
	}

	// If no rows for this type, fallback to DEFAULT
	if len(rows) == 0 && eventType != "DEFAULT" {
		var fallback []models.EventPhrase
		err = gormrepo.GetList(&fallback, gormrepo.QueryOptions{
			Filters: map[string]interface{}{"event_type": "DEFAULT"},
		})
		if err != nil {
			slog.Error("phrases: fallback DEFAULT query failed", "error", err)
			return utils.Error(c, http.StatusInternalServerError, "Failed to fetch phrases", err.Error())
		}
		rows = fallback
	}

	// If still nothing, return empty
	if len(rows) == 0 {
		return utils.Success(c, http.StatusOK, "No phrases found", map[string]interface{}{
			"phrases": []string{},
		})
	}

	// Shuffle and pick N
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
	if count > len(rows) {
		count = len(rows)
	}
	selected := make([]string, count)
	for i := 0; i < count; i++ {
		selected[i] = rows[i].Phrase
	}

	// Cache result
	if data, err := json.Marshal(selected); err == nil {
		_ = redisrepository.SaveKey(ctx, cacheKey, string(data), phraseCacheTTL)
	}

	return utils.Success(c, http.StatusOK, "Phrases retrieved", map[string]interface{}{
		"phrases": selected,
	})
}
