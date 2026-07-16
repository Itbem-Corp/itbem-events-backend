package auditlogs

import (
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

type auditLogPage struct {
	Data       []models.AuditLog `json:"data"`
	Total      int64             `json:"total"`
	Page       int               `json:"page"`
	PageSize   int               `json:"page_size"`
	TotalPages int               `json:"total_pages"`
}

// List exposes the append-only trail only to the primary platform
// administrator and scopes it to the application used for this session.
func List(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Audit trail unavailable", "")
	}

	tenant, _ := c.Get("tenant_code").(string)
	tenant = strings.ToLower(strings.TrimSpace(tenant))
	if tenant == "" {
		return utils.Error(c, http.StatusBadRequest, "Invalid application", "tenant context is missing")
	}

	page := positiveInt(c.QueryParam("page"), 1, 1_000_000)
	pageSize := positiveInt(c.QueryParam("page_size"), 50, 100)
	query := configuration.DB.Model(&models.AuditLog{}).Where("tenant_code = ?", tenant)

	if route := strings.TrimSpace(c.QueryParam("route")); route != "" {
		query = query.Where("route = ?", route)
	}
	if statusText := strings.TrimSpace(c.QueryParam("status")); statusText != "" {
		status, err := strconv.Atoi(statusText)
		if err != nil || status < 100 || status > 599 {
			return utils.Error(c, http.StatusBadRequest, "Invalid status", "status must be an HTTP status code")
		}
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Could not count audit records", err.Error())
	}

	records := make([]models.AuditLog, 0)
	if err := query.
		Order("occurred_at DESC, id DESC").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&records).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Could not list audit records", err.Error())
	}

	totalPages := 0
	if total > 0 {
		totalPages = (int(total) + pageSize - 1) / pageSize
	}
	return utils.Success(c, http.StatusOK, "Audit trail", auditLogPage{
		Data: records, Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages,
	})
}

func positiveInt(value string, fallback, maximum int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 {
		return fallback
	}
	if parsed > maximum {
		return maximum
	}
	return parsed
}
