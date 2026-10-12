package productmetrics

import (
	"net/http"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	productmetricshttp "events-stocks/internal/observability/productmetricshttp"
	"events-stocks/internal/products"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/models"
	"events-stocks/services/applications"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
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

// productTotal is the exact number of distinct active users per product in
// the window. Summing summaries[].active_users double-counts a user who is
// active in several organizations of the same product (ADM-3).
type productTotal struct {
	TenantCode  string `json:"tenant_code"`
	ActiveUsers int64  `json:"active_users"`
}

type portfolioResponse struct {
	From          time.Time        `json:"from"`
	To            time.Time        `json:"to"`
	Summaries     []metricSummary  `json:"summaries"`
	Timeline      []dailyPoint     `json:"timeline"`
	ProductTotals []productTotal   `json:"product_totals"`
	Inventory     inventorySummary `json:"inventory"`
}

// portfolioScope is the data boundary of one portfolio request. Every part of
// the response (summaries, timeline, product totals, inventory) goes through
// it. Only a primary root without an organization sees every product; any
// other view, an organization view included, is limited to the session's
// product, so an EventiApp session never receives ITBEM rows even for an
// organization that also has ITBEM traffic (ROLES-113).
type portfolioScope struct {
	tenant       string
	clientID     uuid.UUID
	hasClient    bool
	crossProduct bool
}

func newPortfolioScope(tenant string, clientID uuid.UUID, hasClient, primaryRoot bool) portfolioScope {
	return portfolioScope{
		tenant:       strings.ToLower(strings.TrimSpace(tenant)),
		clientID:     clientID,
		hasClient:    hasClient,
		crossProduct: primaryRoot && !hasClient,
	}
}

// apply filters a metrics table aliased as alias.
func (scope portfolioScope) apply(query *gorm.DB, alias string) *gorm.DB {
	if scope.hasClient {
		query = query.Where(alias+".client_id = ?", scope.clientID)
	}
	if !scope.crossProduct {
		query = query.Where(alias+".tenant_code = ?", scope.tenant)
	}
	return query
}

// productTotalsQuery counts DISTINCT users per product over the window with
// the same scope as the rest of the portfolio.
func productTotalsQuery(db *gorm.DB, from, to time.Time, scope portfolioScope) *gorm.DB {
	query := db.Table("product_active_user_daily active").
		Select("active.tenant_code, COUNT(DISTINCT active.user_id) AS active_users").
		Where("active.day BETWEEN ? AND ?", from, to).
		Group("active.tenant_code").
		Order("active.tenant_code ASC")
	return scope.apply(query, "active")
}

func summariesQuery(db *gorm.DB, from, to time.Time, scope portfolioScope) *gorm.DB {
	query := db.Table("product_metric_daily metrics").
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
	return scope.apply(query, "metrics")
}

func timelineQuery(db *gorm.DB, from, to time.Time, scope portfolioScope) *gorm.DB {
	// For an organization-scoped view the daily active users must be limited
	// to that organization too; otherwise the product-wide count leaked into
	// an org admin's chart.
	activeScope := ""
	activeArgs := []interface{}{}
	if scope.hasClient {
		activeScope = " AND active.client_id = ?"
		activeArgs = append(activeArgs, scope.clientID)
	}
	query := db.Table("product_metric_daily metrics").
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
			   AND active.tenant_code = metrics.tenant_code`+activeScope+`) AS active_users
		`, activeArgs...).
		Where("metrics.day BETWEEN ? AND ?", from, to).
		Group("metrics.day, metrics.tenant_code").
		Order("metrics.day ASC, metrics.tenant_code ASC")
	return scope.apply(query, "metrics")
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
	if err := authorizePortfolioScope(user, tenant, clientID, hasClient); err != nil {
		return authz.Respond(c, err)
	}
	if hasClient {
		// A member may only read organizations their application session can
		// enter (another product's portal tree is never in it).
		if session, ok := c.Get(applicationaccess.ContextKey).(*applications.Session); ok && session != nil &&
			!user.IsPlatformAdmin() && !session.AllowsOrganization(clientID) {
			return utils.Error(c, http.StatusForbidden, "La organización no está habilitada para esta aplicación", "ORGANIZATION_NOT_ENABLED")
		}
		productmetricshttp.ScopeOrganization(c, clientID)
	}
	scope := newPortfolioScope(tenant, clientID, hasClient, user.IsPrimaryRoot())

	summaries := make([]metricSummary, 0)
	if err := summariesQuery(configuration.DB, from, to, scope).Scan(&summaries).Error; err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Metrics unavailable", err.Error())
	}
	timeline := make([]dailyPoint, 0)
	if err := timelineQuery(configuration.DB, from, to, scope).Scan(&timeline).Error; err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Metrics unavailable", err.Error())
	}
	productTotals := make([]productTotal, 0)
	if err := productTotalsQuery(configuration.DB, from, to, scope).Scan(&productTotals).Error; err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Metrics unavailable", err.Error())
	}

	inventory := loadInventory(scope)
	return utils.Success(c, http.StatusOK, "Product metrics", portfolioResponse{
		From: from, To: to, Summaries: summaries, Timeline: timeline, ProductTotals: productTotals, Inventory: inventory,
	})
}

// authorizePortfolioScope decides who may read portfolio metrics. With an
// organization, the caller needs analytics:view on it, or metrics:view when the
// product reserves metrics for organization Owner/Admin (EventiApp). Without
// one, only platform roots may read the cross-organization portfolio.
func authorizePortfolioScope(user *models.User, tenant string, clientID uuid.UUID, hasClient bool) error {
	if hasClient {
		capability := authz.CapabilityAnalyticsView
		if products.ModuleRequiresOrganizationAdmin(tenant, "metrics") {
			capability = authz.CapabilityMetricsView
		}
		return authz.RequireClientCapability(user, clientID, capability)
	}
	if user == nil || !user.IsPlatformAdmin() {
		return &authz.Failure{Status: http.StatusForbidden, Message: "Organization required", Detail: "Select an organization to view metrics"}
	}
	return nil
}

func loadInventory(scope portfolioScope) inventorySummary {
	var result inventorySummary
	clients := configuration.DB.Table("clients").Where("deleted_at IS NULL AND is_active = true")
	users := configuration.DB.Table("client_members").
		Joins("JOIN clients ON clients.id = client_members.client_id AND clients.deleted_at IS NULL").
		Where("client_members.is_active = true")
	events := configuration.DB.Table("events").Where("deleted_at IS NULL")
	if scope.hasClient {
		// Child organizations that are another product's portal (e.g. ITBEM
		// under the EventiApp platform organization) are not counted.
		clients = clients.Where(`clients.id = ? OR (clients.parent_id = ? AND NOT EXISTS (
			SELECT 1 FROM applications other
			WHERE LOWER(other.code) = LOWER(clients.code) AND LOWER(other.code) <> ?))`,
			scope.clientID, scope.clientID, scope.tenant)
		users = users.Where("client_members.client_id = ?", scope.clientID)
		events = events.Where("client_id = ?", scope.clientID)
	} else if !scope.crossProduct {
		tenant := scope.tenant
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
