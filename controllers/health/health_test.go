package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckUptimeAndResponse(t *testing.T) {
	t.Setenv("ENV", "prod")
	t.Setenv("SOURCE_REVISION", " revision ")
	start := time.Now()
	okDB := func() error { return nil }
	okRedis := func(context.Context) error { return nil }
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{{0, "0"}, {1900 * time.Millisecond, "1"}, {2 * time.Second, "2"}} {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/health", nil), rec)
		require.NoError(t, check(c, start, func() time.Time { return start.Add(tc.elapsed) }, okDB, okRedis))
		assert.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			Status  int                    `json:"status"`
			Message string                 `json:"message"`
			Data    map[string]interface{} `json:"data"`
		}
		decoder := json.NewDecoder(strings.NewReader(rec.Body.String()))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&body))
		assert.Equal(t, http.StatusOK, body.Status)
		assert.Equal(t, "Health check", body.Message)
		assert.Equal(t, json.Number(tc.want), body.Data["uptimeSeconds"])
		assert.Equal(t, "ok", body.Data["status"])
		assert.Equal(t, "production", body.Data["environment"])
		assert.Equal(t, "revision", body.Data["source_revision"])
		assert.Equal(t, start.Add(tc.elapsed).UTC().Format(time.RFC3339), body.Data["time"])
		assert.Equal(t, "ok", body.Data["db"])
		assert.Equal(t, "ok", body.Data["redis"])
	}

	for _, dbFails := range []bool{true, false} {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/health", nil), rec)
		pingDB := func() error {
			if dbFails {
				return errors.New("down")
			}
			return nil
		}
		pingRedis := func(context.Context) error {
			if !dbFails {
				return errors.New("down")
			}
			return nil
		}
		require.NoError(t, check(c, start, func() time.Time { return start }, pingDB, pingRedis))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var body struct {
			Data map[string]interface{} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "degraded", body.Data["status"])
		if dbFails {
			assert.Equal(t, "unreachable", body.Data["db"])
		} else {
			assert.Equal(t, "unreachable", body.Data["redis"])
		}
	}
}

func TestMarkProcessStartOnce(t *testing.T) {
	MarkProcessStart()
	first := processStart
	require.False(t, first.IsZero())
	MarkProcessStart()
	assert.Equal(t, first, processStart)
}

func TestHealthEnvironmentNormalizesDeploymentAliases(t *testing.T) {
	tests := map[string]string{
		"production": "production",
		" PROD ":     "production",
		"staging":    "staging",
		"Stage":      "staging",
		"local":      "local",
		"dev":        "dev",
		"":           "unknown",
		"preview":    "unknown",
	}
	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, expected, healthEnvironment(input))
		})
	}
}
