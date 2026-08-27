package productmetrics

import (
	"net/http"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	productmetricshttp "events-stocks/internal/observability/productmetricshttp"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

type metricSummary struct {
	TenantCode   string    `json:"tenant_code"`
	ClientID     uuid.UUID `json:"client_id"`
	ClientName   string    `json:"client_name"`
	Requests     int64     `json:"requests"`
	Mutations    int64     `json:"mutations"`
	Errors       int64     `json:"errors"`
	DurationMS   int64     `json:"duration_ms"`
	RequestBytes int64     `json:"request_bytes"`
	ActiveUsers  int64     `json:"active_users"`
}

type dailyPoint struct {
	Day          time.Time `json:"day"`
	TenantCode   string    `json:"tenant_code"`
	Requests     int64     `json:"requests"`
	Mutations    int64     `json:"mutations"`
	Errors       int64     `json:"errors"`
	DurationMS   int64     `json:"duration_ms"`
	RequestBytes int64     `json:"request_bytes"`
	ActiveUsers  int64     `json:"active_users"`
}

type inventorySummary struct {
	Organizations int64 `json:"organizations"`
	Users         int64 `json:"users"`
	Events        int64 `json:"events"`
}

type portfolioResponse struct {
	From      time.Time        `json:"from"`
	To        time.Time        `json:"to"`
	Summaries []metricSummary  `json:"summaries"`
	Timeline  []dailyPoint     `json:"timeline"`
	Inventory inventorySummary `json:"inventory"`
}

func Portfolio(c echo.Context) error {
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	from, to, err := metricWindow(c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid metric window", err.Error())
	}
	tenant, _ := c.Get("tenant_code").(string)
	tenant = strings.ToLower(strings.TrimSpace(tenant))
	clientID, hasClient, err := optionalUUID(c.QueryParam("client_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid organization", err.Error())
	}
	if hasClient {
		if err := authz.RequireClientCapability(user, clientID, authz.CapabilityAnalyticsView); err != nil {
			return authz.Respond(c, err)
		}
		productmetricshttp.ScopeOrganization(c, clientID)
	} else if !user.IsPlatformAdmin() {
		return utils.Error(c, http.StatusForbidden, "Organization required", "Select an organization to view metrics")
	}

	query := configuration.DB.Table("product_metric_daily metrics").
		Select(`
			metrics.tenant_code,
			metrics.client_id,
			COALESCE(clients.name, '') AS client_name,
			SUM(metrics.requests) AS requests,
			SUM(metrics.mutations) AS mutations,
			SUM(metrics.errors) AS errors,
			SUM(metrics.duration_ms) AS duration_ms,
			SUM(metrics.request_bytes) AS request_bytes,
			(SELECT COUNT(DISTINCT active.user_id)
			 FROM product_active_user_daily active
			 WHERE active.tenant_code = metrics.tenant_code
			   AND active.client_id = metrics.client_id
			   AND active.day BETWEEN ? AND ?) AS active_users
		`, from, to).
		Joins("LEFT JOIN clients ON clients.id = metrics.client_id").
		Where("metrics.day BETWEEN ? AND ?", from, to).
		Group("metrics.tenant_code, metrics.client_id, clients.name").
		Order("requests DESC").
		Limit(200)
	if hasClient {
		query = query.Where("metrics.client_id = ?", clientID)
	} else if !user.IsPrimaryRoot() {
		query = query.Where("metrics.tenant_code = ?", tenant)
	}
	summaries := make([]metricSummary, 0)
	if err := query.Scan(&summaries).Error; err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Metrics unavailable", err.Error())
	}

	timelineQuery := configuration.DB.Table("product_metric_daily metrics").
		Select(`
			metrics.day,
			metrics.tenant_code,
			SUM(metrics.requests) AS requests,
			SUM(metrics.mutations) AS mutations,
			SUM(metrics.errors) AS errors,
			SUM(metrics.duration_ms) AS duration_ms,
			SUM(metrics.request_bytes) AS request_bytes,
			(SELECT COUNT(DISTINCT active.user_id)
			 FROM product_active_user_daily active
			 WHERE active.day = metrics.day
			   AND active.tenant_code = metrics.tenant_code) AS active_users
		`).
		Where("metrics.day BETWEEN ? AND ?", from, to).
		Group("metrics.day, metrics.tenant_code").
		Order("metrics.day ASC, metrics.tenant_code ASC")
	if hasClient {
		timelineQuery = timelineQuery.Where("metrics.client_id = ?", clientID)
	} else if !user.IsPrimaryRoot() {
		timelineQuery = timelineQuery.Where("metrics.tenant_code = ?", tenant)
	}
	timeline := make([]dailyPoint, 0)
	if err := timelineQuery.Scan(&timeline).Error; err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Metrics unavailable", err.Error())
	}

	inventory := loadInventory(tenant, clientID, hasClient, user.IsPrimaryRoot())
	return utils.Success(c, http.StatusOK, "Product metrics", portfolioResponse{
		From: from, To: to, Summaries: summaries, Timeline: timeline, Inventory: inventory,
	})
}

func loadInventory(tenant string, clientID uuid.UUID, hasClient, crossProduct bool) inventorySummary {
	var result inventorySummary
	clients := configuration.DB.Table("clients").Where("deleted_at IS NULL AND is_active = true")
	users := configuration.DB.Table("client_members").
		Joins("JOIN clients ON clients.id = client_members.client_id AND clients.deleted_at IS NULL").
		Where("client_members.is_active = true")
	events := configuration.DB.Table("events").Where("deleted_at IS NULL")
	if hasClient {
		clients = clients.Where("clients.id = ? OR clients.parent_id = ?", clientID, clientID)
		users = users.Where("client_members.client_id = ?", clientID)
		events = events.Where("client_id = ?", clientID)
	} else if !crossProduct {
		clients = clients.Where("LOWER(clients.code) = ? OR clients.parent_id IN (SELECT id FROM clients WHERE LOWER(code) = ?)", tenant, tenant)
		users = users.Where("LOWER(clients.code) = ? OR clients.parent_id IN (SELECT id FROM clients WHERE LOWER(code) = ?)", tenant, tenant)
		events = events.Where("client_id IN (SELECT id FROM clients WHERE LOWER(code) = ? OR parent_id IN (SELECT id FROM clients WHERE LOWER(code) = ?))", tenant, tenant)
	}
	clients.Count(&result.Organizations)
	users.Distinct("client_members.user_id").Count(&result.Users)
	events.Count(&result.Events)
	return result
}

func metricWindow(fromRaw, toRaw string) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := to.AddDate(0, 0, -29)
	var err error
	if strings.TrimSpace(fromRaw) != "" {
		from, err = time.Parse("2006-01-02", fromRaw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if strings.TrimSpace(toRaw) != "" {
		to, err = time.Parse("2006-01-02", toRaw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if from.After(to) || to.Sub(from) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, echo.NewHTTPError(http.StatusBadRequest, "window must be between 1 and 367 days")
	}
	return from, to, nil
}

func optionalUUID(raw string) (uuid.UUID, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.Nil, false, nil
	}
	value, err := uuid.FromString(raw)
	return value, err == nil, err
}
