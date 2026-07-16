package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	headerKey        = "Idempotency-Key"
	headerReplayed   = "Idempotency-Replayed"
	maxKeyLength     = 128
	maxBodyBytes     = 1 << 20
	maxResponseBytes = 8 << 20
	recordTTL        = 24 * time.Hour
	staleAfter       = 2 * time.Minute
)

var protectedRoutes = map[string]struct{}{
	"POST /api/events":                 {},
	"POST /api/events/:id/duplicate":   {},
	"POST /api/invitations/:id/resend": {},
	"POST /api/guests":                 {},
	"POST /api/guests/batch":           {},
	"POST /api/clients/invite":         {},
	"POST /api/clients/members":        {},
	"POST /api/users/invite":           {},
	"PUT /api/clients/:id/member-applications/:user_id/:application_code": {},
}

type captureWriter struct {
	http.ResponseWriter
	body     bytes.Buffer
	overflow bool
}

func (writer *captureWriter) Write(data []byte) (int, error) {
	if writer.body.Len() < maxResponseBytes {
		remaining := maxResponseBytes - writer.body.Len()
		if len(data) > remaining {
			_, _ = writer.body.Write(data[:remaining])
			writer.overflow = true
		} else {
			_, _ = writer.body.Write(data)
		}
	} else if len(data) > 0 {
		writer.overflow = true
	}
	return writer.ResponseWriter.Write(data)
}

// CriticalMutations provides opt-in backwards-compatible idempotency. Existing
// API clients continue to work without a key; first-party clients always send
// one for the protected routes.
func CriticalMutations(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		route := strings.ToUpper(c.Request().Method) + " " + c.Path()
		if _, protected := protectedRoutes[route]; !protected {
			return next(c)
		}

		key := strings.TrimSpace(c.Request().Header.Get(headerKey))
		if key == "" {
			c.Response().Header().Set("Idempotency-Status", "bypassed")
			return next(c)
		}
		if len(key) > maxKeyLength {
			return utils.Error(c, http.StatusBadRequest, "Invalid idempotency key", "Idempotency-Key must be at most 128 characters")
		}
		if configuration.DB == nil {
			return utils.Error(c, http.StatusServiceUnavailable, "Request safety unavailable", "Idempotency storage is unavailable")
		}

		body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxBodyBytes+1))
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
		}
		if len(body) > maxBodyBytes {
			return utils.Error(c, http.StatusRequestEntityTooLarge, "Request too large", "Idempotent JSON mutations are limited to 1 MB")
		}
		c.Request().Body = io.NopCloser(bytes.NewReader(body))

		hashInput := append([]byte(c.Request().URL.RawQuery+"\n"), body...)
		hash := sha256.Sum256(hashInput)
		scope := &models.IdempotencyRecord{
			TenantCode:  contextString(c, "tenant_code"),
			ActorSub:    contextString(c, "cognito_sub"),
			Method:      strings.ToUpper(c.Request().Method),
			Route:       c.Path(),
			Key:         key,
			RequestHash: hex.EncodeToString(hash[:]),
			State:       "processing",
			ExpiresAt:   time.Now().UTC().Add(recordTTL),
		}

		reserved, existing, reserveErr := reserve(configuration.DB, scope)
		if reserveErr != nil {
			return utils.Error(c, http.StatusServiceUnavailable, "Request safety unavailable", reserveErr.Error())
		}
		if !reserved {
			if existing.RequestHash != scope.RequestHash {
				return utils.Error(c, http.StatusConflict, "Idempotency key already used", "The same key cannot be reused with a different request body")
			}
			if existing.State == "completed" {
				c.Response().Header().Set(headerReplayed, "true")
				if existing.ContentType != "" {
					c.Response().Header().Set(echo.HeaderContentType, existing.ContentType)
				}
				return c.Blob(existing.StatusCode, existing.ContentType, existing.ResponseBody)
			}
			c.Response().Header().Set(echo.HeaderRetryAfter, "2")
			return utils.Error(c, http.StatusConflict, "Request already in progress", "Retry this request shortly with the same Idempotency-Key")
		}

		originalWriter := c.Response().Writer
		capture := &captureWriter{ResponseWriter: originalWriter}
		c.Response().Writer = capture
		handlerErr := next(c)
		c.Response().Writer = originalWriter

		status := c.Response().Status
		if handlerErr != nil || status < 200 || status >= 400 {
			configuration.DB.Delete(&models.IdempotencyRecord{}, "id = ?", scope.ID)
			return handlerErr
		}
		contentType := c.Response().Header().Get(echo.HeaderContentType)
		if capture.overflow {
			slog.Error("idempotency response exceeded replay limit", "id", scope.ID, "route", scope.Route)
			return handlerErr
		}
		if err := finalize(configuration.DB, scope.ID, status, contentType, capture.body.Bytes()); err != nil {
			slog.Error("idempotency response persistence failed", "id", scope.ID, "route", scope.Route, "error", err)
		}
		return handlerErr
	}
}

func reserve(db *gorm.DB, candidate *models.IdempotencyRecord) (bool, *models.IdempotencyRecord, error) {
	now := time.Now().UTC()
	_ = db.Where("expires_at < ?", now).Delete(&models.IdempotencyRecord{}).Error
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(candidate)
	if result.Error != nil {
		return false, nil, result.Error
	}
	if result.RowsAffected == 1 {
		return true, candidate, nil
	}

	var existing models.IdempotencyRecord
	err := db.Where(
		"tenant_code = ? AND actor_sub = ? AND method = ? AND route = ? AND key = ?",
		candidate.TenantCode, candidate.ActorSub, candidate.Method, candidate.Route, candidate.Key,
	).First(&existing).Error
	if err != nil {
		return false, nil, err
	}
	if existing.RequestHash == candidate.RequestHash &&
		existing.State == "processing" &&
		existing.UpdatedAt.Before(now.Add(-staleAfter)) {
		result := db.Model(&models.IdempotencyRecord{}).
			Where("id = ? AND state = ? AND updated_at = ?", existing.ID, "processing", existing.UpdatedAt).
			Updates(map[string]interface{}{
				"request_hash": candidate.RequestHash,
				"expires_at":   candidate.ExpiresAt,
				"updated_at":   now,
			})
		if result.Error != nil {
			return false, nil, result.Error
		}
		if result.RowsAffected == 1 {
			candidate.ID = existing.ID
			return true, candidate, nil
		}
	}
	return false, &existing, nil
}

func finalize(db *gorm.DB, id interface{}, status int, contentType string, responseBody []byte) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = db.Model(&models.IdempotencyRecord{}).
			Where("id = ?", id).
			Updates(map[string]interface{}{
				"state":         "completed",
				"status_code":   status,
				"content_type":  contentType,
				"response_body": responseBody,
				"expires_at":    time.Now().UTC().Add(recordTTL),
			}).Error
		if err == nil {
			return nil
		}
	}
	return err
}

func contextString(c echo.Context, key string) string {
	value, _ := c.Get(key).(string)
	return strings.ToLower(strings.TrimSpace(value))
}
