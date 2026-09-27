package automation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const maxProviderUsageResponseBytes = 128 << 10

const (
	providerUsageStatusAvailable     = "available"
	providerUsageStatusNotConfigured = "not_configured"
	providerUsageStatusNotSupported  = "not_supported"
	providerUsageStatusUnavailable   = "unavailable"
	providerUsageStatusError         = "error"

	providerUsageErrorCredentialStore = "credential_storage_unavailable"
	providerUsageErrorNotConfigured   = "credential_not_configured"
	providerUsageErrorKeyUnavailable  = "credential_unavailable"
	providerUsageErrorProviderDown    = "provider_unavailable"
	providerUsageErrorAuth            = "provider_auth_failed"
	providerUsageErrorRejected        = "provider_request_rejected"
	providerUsageErrorInvalidResponse = "invalid_provider_response"
	providerUsageErrorManagementKey   = "separate_management_credential_required"
)

var providerUsageHTTPClient = &http.Client{
	Timeout: 12 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type providerUsageBalance struct {
	Total       string `json:"total"`
	Granted     string `json:"granted"`
	ToppedUp    string `json:"topped_up"`
	IsAvailable bool   `json:"is_available"`
}

type providerUsageWindow struct {
	Name      string       `json:"name"`
	Used      *json.Number `json:"used,omitempty"`
	Limit     *json.Number `json:"limit,omitempty"`
	Remaining *json.Number `json:"remaining,omitempty"`
	Unit      string       `json:"unit"`
	ResetAt   *time.Time   `json:"reset_at,omitempty"`
}

type providerUsageAccount struct {
	Provider        string                `json:"provider"`
	Status          string                `json:"status"`
	BillingModel    string                `json:"billing_model"`
	CredentialScope string                `json:"credential_scope"`
	ObservedAt      time.Time             `json:"observed_at"`
	Currency        string                `json:"currency,omitempty"`
	Balance         *providerUsageBalance `json:"balance,omitempty"`
	Windows         []providerUsageWindow `json:"windows"`
	ErrorCode       string                `json:"error_code,omitempty"`
}

type providerUsageResponse struct {
	ProjectID  string                 `json:"project_id"`
	ObservedAt *time.Time             `json:"observed_at"`
	Accounts   []providerUsageAccount `json:"accounts"`
}

// GetProjectProviderUsage returns the latest append-only observation for a
// project. Project membership/workspace scope is checked before reading it.
func GetProjectProviderUsage(c echo.Context) error {
	projectID, ok := parseProviderUsageProject(c)
	if !ok {
		return utils.Error(c, http.StatusBadRequest, "Invalid provider usage project", "")
	}
	if err := authorizeProjectCredential(c, projectID, false); err != nil || c.Response().Committed {
		return err
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Provider usage unavailable", "")
	}
	return readLatestProviderUsage(c, configuration.DB, projectID)
}

// RefreshProjectProviderUsage queries supported provider quota endpoints with
// the named project's own inference credential only, then appends a safe
// normalized snapshot. OpenRouter is intentionally unsupported until a
// separate management credential/API exists.
func RefreshProjectProviderUsage(c echo.Context) error {
	projectID, ok := parseProviderUsageProject(c)
	if !ok {
		return utils.Error(c, http.StatusBadRequest, "Invalid provider usage project", "")
	}
	if err := authorizeProjectCredential(c, projectID, true); err != nil || c.Response().Committed {
		return err
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Provider usage unavailable", "")
	}
	observedAt := time.Now().UTC()
	accounts := fetchProjectProviderUsage(c.Request().Context(), inferenceCredentials, providerUsageHTTPClient, projectID.String(), observedAt)
	captureID, err := uuid.NewV4()
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Provider usage could not be recorded", "")
	}
	if err := appendProviderUsageSnapshots(c.Request().Context(), configuration.DB, captureID, projectID, observedAt, accounts); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Provider usage could not be recorded", "")
	}
	return utils.Success(c, http.StatusOK, "Provider usage refreshed", providerUsageResponse{ProjectID: projectID.String(), ObservedAt: &observedAt, Accounts: accounts})
}

func parseProviderUsageProject(c echo.Context) (uuid.UUID, bool) {
	projectID, err := uuid.FromString(strings.TrimSpace(c.Param("projectId")))
	return projectID, err == nil && projectID != uuid.Nil
}

func readLatestProviderUsage(c echo.Context, db *gorm.DB, projectID uuid.UUID) error {
	response := providerUsageResponse{ProjectID: projectID.String(), Accounts: []providerUsageAccount{}}
	var latest models.AutomationProviderUsageSnapshot
	err := db.WithContext(c.Request().Context()).Where("project_id = ?", projectID).
		Order("observed_at DESC").Order("created_at DESC").Order("id DESC").First(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return utils.Success(c, http.StatusOK, "Provider usage obtained", response)
	}
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Provider usage unavailable", "")
	}
	response.ObservedAt = &latest.ObservedAt
	var rows []models.AutomationProviderUsageSnapshot
	if err := db.WithContext(c.Request().Context()).Where("project_id = ? AND capture_id = ?", projectID, latest.CaptureID).
		Order("provider ASC, currency ASC, id ASC").Find(&rows).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Provider usage unavailable", "")
	}
	for _, row := range rows {
		account, ok := accountFromSnapshot(row)
		if ok {
			response.Accounts = append(response.Accounts, account)
		}
	}
	return utils.Success(c, http.StatusOK, "Provider usage obtained", response)
}

func accountFromSnapshot(row models.AutomationProviderUsageSnapshot) (providerUsageAccount, bool) {
	account := providerUsageAccount{
		Provider: row.Provider, Status: row.Status, BillingModel: row.BillingModel,
		CredentialScope: row.CredentialScope, ObservedAt: row.ObservedAt,
		Currency: safeUsageCurrency(row.Currency), ErrorCode: safeProviderUsageErrorCode(row.ErrorCode),
		Windows: []providerUsageWindow{},
	}
	if !supportedProviderUsageProvider(account.Provider) || !safeProviderUsageStatus(account.Status) || !safeBillingModel(account.Provider, account.BillingModel) || !safeCredentialScope(account.CredentialScope) {
		return providerUsageAccount{}, false
	}
	if row.BalanceTotal != nil && row.BalanceGranted != nil && row.BalanceToppedUp != nil && row.BalanceIsAvailable != nil && account.Currency != "" {
		if !validProviderDecimal(*row.BalanceTotal) || !validProviderDecimal(*row.BalanceGranted) || !validProviderDecimal(*row.BalanceToppedUp) {
			return providerUsageAccount{}, false
		}
		account.Balance = &providerUsageBalance{Total: *row.BalanceTotal, Granted: *row.BalanceGranted, ToppedUp: *row.BalanceToppedUp, IsAvailable: *row.BalanceIsAvailable}
	}
	var windows []providerUsageWindow
	if json.Unmarshal([]byte(row.WindowsJSON), &windows) != nil {
		return providerUsageAccount{}, false
	}
	for _, window := range windows {
		if !safeUsageWindow(window) {
			return providerUsageAccount{}, false
		}
		account.Windows = append(account.Windows, window)
	}
	return account, true
}

func safeUsageWindow(window providerUsageWindow) bool {
	if len(window.Name) == 0 || len(window.Name) > 48 || strings.ContainsAny(window.Name, "\r\n") || !safeUsageWindowName(window.Name) {
		return false
	}
	switch window.Unit {
	case "quota_units", "percent":
	default:
		return false
	}
	for _, value := range []*json.Number{window.Used, window.Limit, window.Remaining} {
		if value != nil {
			parsed, err := strconv.ParseFloat(value.String(), 64)
			if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
				return false
			}
		}
	}
	if window.Unit == "quota_units" && (window.Used != nil || window.Remaining != nil) {
		return false
	}
	if window.Unit == "percent" && (window.Used != nil || window.Limit != nil) {
		return false
	}
	return true
}

func safeUsageWindowName(name string) bool {
	for _, suffix := range []string{"_interval", "_weekly"} {
		if strings.HasSuffix(name, suffix) && safeMiniMaxQuotaName(strings.TrimSuffix(name, suffix)) != "" {
			return true
		}
	}
	return false
}

func appendProviderUsageSnapshots(ctx context.Context, db *gorm.DB, captureID, projectID uuid.UUID, observedAt time.Time, accounts []providerUsageAccount) error {
	if db == nil || captureID == uuid.Nil || projectID == uuid.Nil || len(accounts) == 0 {
		return errors.New("provider usage snapshot is invalid")
	}
	rows := make([]models.AutomationProviderUsageSnapshot, 0, len(accounts))
	for _, account := range accounts {
		if !supportedProviderUsageProvider(account.Provider) || !safeProviderUsageStatus(account.Status) || !safeBillingModel(account.Provider, account.BillingModel) || !safeCredentialScope(account.CredentialScope) {
			return errors.New("provider usage account is invalid")
		}
		windows := account.Windows
		if windows == nil {
			windows = []providerUsageWindow{}
		}
		for _, window := range windows {
			if !safeUsageWindow(window) {
				return errors.New("provider usage window is invalid")
			}
		}
		windowsJSON, err := json.Marshal(windows)
		if err != nil {
			return errors.New("provider usage windows are invalid")
		}
		row := models.AutomationProviderUsageSnapshot{
			ID: uuid.Must(uuid.NewV4()), CaptureID: captureID, ProjectID: projectID,
			Provider: account.Provider, Status: account.Status, BillingModel: account.BillingModel,
			CredentialScope: account.CredentialScope, ObservedAt: observedAt.UTC(), Currency: safeUsageCurrency(account.Currency),
			WindowsJSON: string(windowsJSON), ErrorCode: safeProviderUsageErrorCode(account.ErrorCode), CreatedAt: time.Now().UTC(),
		}
		if account.Balance != nil {
			if row.Currency == "" || !validProviderDecimal(account.Balance.Total) || !validProviderDecimal(account.Balance.Granted) || !validProviderDecimal(account.Balance.ToppedUp) {
				return errors.New("provider usage balance is invalid")
			}
			total, granted, toppedUp, available := account.Balance.Total, account.Balance.Granted, account.Balance.ToppedUp, account.Balance.IsAvailable
			row.BalanceTotal, row.BalanceGranted, row.BalanceToppedUp, row.BalanceIsAvailable = &total, &granted, &toppedUp, &available
		}
		rows = append(rows, row)
	}
	return db.WithContext(ctx).Create(&rows).Error
}

func fetchProjectProviderUsage(ctx context.Context, resolver credentialResolver, client *http.Client, projectID string, observedAt time.Time) []providerUsageAccount {
	if client == nil {
		client = providerUsageHTTPClient
	}
	accounts := make([]providerUsageAccount, 0, 3)
	for _, provider := range []string{string(automationagent.ProviderDeepSeek), string(automationagent.ProviderMiniMax), string(automationagent.ProviderOpenRouter)} {
		accounts = append(accounts, fetchOneProjectProviderUsage(ctx, resolver, client, projectID, provider, observedAt.UTC())...)
	}
	return accounts
}

func fetchOneProjectProviderUsage(ctx context.Context, resolver credentialResolver, client *http.Client, projectID, provider string, observedAt time.Time) []providerUsageAccount {
	billingModel := "token"
	if provider == string(automationagent.ProviderMiniMax) {
		billingModel = "subscription_quota"
	} else if provider == string(automationagent.ProviderDeepSeek) {
		billingModel = "token"
	}
	account := providerUsageAccount{Provider: provider, Status: providerUsageStatusUnavailable, BillingModel: billingModel, CredentialScope: "project", ObservedAt: observedAt.UTC(), Windows: []providerUsageWindow{}}
	if provider == string(automationagent.ProviderOpenRouter) {
		account.Status = providerUsageStatusNotSupported
		account.BillingModel = "management_api_not_supported"
		account.CredentialScope = "separate_management_credential_required"
		account.ErrorCode = providerUsageErrorManagementKey
		return []providerUsageAccount{account}
	}
	projectResolver, ok := resolver.(projectCredentialResolver)
	if !ok || projectResolver == nil || strings.TrimSpace(projectID) == "" {
		account.ErrorCode = providerUsageErrorCredentialStore
		return []providerUsageAccount{account}
	}
	configured, err := projectResolver.HasAPIKeyForProject(ctx, projectID, provider)
	if err != nil {
		account.ErrorCode = providerUsageErrorCredentialStore
		return []providerUsageAccount{account}
	}
	if !configured {
		account.Status = providerUsageStatusNotConfigured
		account.ErrorCode = providerUsageErrorNotConfigured
		return []providerUsageAccount{account}
	}
	key, err := projectResolver.APIKeyForProject(ctx, projectID, provider)
	if err != nil || strings.TrimSpace(key) == "" {
		account.ErrorCode = providerUsageErrorKeyUnavailable
		return []providerUsageAccount{account}
	}
	endpoint, _ := providerUsageEndpoint(provider)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		account.ErrorCode = providerUsageErrorRejected
		return []providerUsageAccount{account}
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		account.ErrorCode = providerUsageErrorProviderDown
		return []providerUsageAccount{account}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		account.Status = providerUsageStatusError
		account.ErrorCode = providerUsageErrorRejected
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			account.ErrorCode = providerUsageErrorAuth
		}
		return []providerUsageAccount{account}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProviderUsageResponseBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxProviderUsageResponseBytes {
		account.Status = providerUsageStatusError
		account.ErrorCode = providerUsageErrorInvalidResponse
		return []providerUsageAccount{account}
	}
	var accounts []providerUsageAccount
	if provider == string(automationagent.ProviderDeepSeek) {
		accounts = parseDeepSeekUsage(raw, account)
	} else {
		accounts = parseMiniMaxTokenPlanUsage(raw, account)
	}
	if len(accounts) == 0 {
		account.Status = providerUsageStatusError
		account.ErrorCode = providerUsageErrorInvalidResponse
		return []providerUsageAccount{account}
	}
	return accounts
}

func providerUsageEndpoint(provider string) (string, bool) {
	switch provider {
	case string(automationagent.ProviderDeepSeek):
		return "https://api.deepseek.com/user/balance", true
	case string(automationagent.ProviderMiniMax):
		return "https://www.minimax.io/v1/token_plan/remains", true
	default:
		return "", false
	}
}

var decimalUsagePattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,31})(\.[0-9]{1,16})?$`)

func validProviderDecimal(value string) bool {
	value = strings.TrimSpace(value)
	if !decimalUsagePattern.MatchString(value) {
		return false
	}
	_, ok := new(big.Rat).SetString(value)
	return ok
}

func parseDeepSeekUsage(raw []byte, base providerUsageAccount) []providerUsageAccount {
	var response struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency        string `json:"currency"`
			TotalBalance    string `json:"total_balance"`
			GrantedBalance  string `json:"granted_balance"`
			ToppedUpBalance string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.BalanceInfos) == 0 || len(response.BalanceInfos) > 4 {
		return nil
	}
	accounts := make([]providerUsageAccount, 0, len(response.BalanceInfos))
	for _, info := range response.BalanceInfos {
		currency := safeUsageCurrency(info.Currency)
		if currency == "" || !validProviderDecimal(info.TotalBalance) || !validProviderDecimal(info.GrantedBalance) || !validProviderDecimal(info.ToppedUpBalance) {
			return nil
		}
		base.Currency = currency
		base.Balance = &providerUsageBalance{Total: info.TotalBalance, Granted: info.GrantedBalance, ToppedUp: info.ToppedUpBalance, IsAvailable: response.IsAvailable}
		base.Status = providerUsageStatusAvailable
		base.ErrorCode = ""
		accounts = append(accounts, base)
	}
	return accounts
}

func parseMiniMaxTokenPlanUsage(raw []byte, base providerUsageAccount) []providerUsageAccount {
	var response struct {
		ModelRemains []struct {
			ModelName                string      `json:"model_name"`
			IntervalTotal            json.Number `json:"current_interval_total_count"`
			IntervalRemainingPercent json.Number `json:"current_interval_remaining_percent"`
			IntervalResetMillis      json.Number `json:"end_time"`
			WeeklyTotal              json.Number `json:"current_weekly_total_count"`
			WeeklyRemainingPercent   json.Number `json:"current_weekly_remaining_percent"`
			WeeklyResetMillis        json.Number `json:"weekly_end_time"`
		} `json:"model_remains"`
		BaseResp struct {
			StatusCode json.Number `json:"status_code"`
		} `json:"base_resp"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.ModelRemains) == 0 || len(response.ModelRemains) > 16 || response.BaseResp.StatusCode != "0" {
		return nil
	}
	accounts := make([]providerUsageAccount, 1)
	base.Status = providerUsageStatusAvailable
	base.ErrorCode = ""
	for _, remain := range response.ModelRemains {
		model := safeMiniMaxQuotaName(remain.ModelName)
		if model == "" {
			return nil
		}
		interval, includeInterval, ok := miniMaxQuotaWindow(model+"_interval", remain.IntervalTotal, remain.IntervalRemainingPercent, remain.IntervalResetMillis)
		if !ok {
			return nil
		}
		weekly, includeWeekly, ok := miniMaxQuotaWindow(model+"_weekly", remain.WeeklyTotal, remain.WeeklyRemainingPercent, remain.WeeklyResetMillis)
		if !ok {
			return nil
		}
		if includeInterval {
			base.Windows = append(base.Windows, interval)
		}
		if includeWeekly {
			base.Windows = append(base.Windows, weekly)
		}
	}
	accounts[0] = base
	return accounts
}

func miniMaxQuotaWindow(name string, total, remainingPercent, resetMillis json.Number) (providerUsageWindow, bool, bool) {
	window := providerUsageWindow{Name: name, Unit: "quota_units"}
	if remainingPercent != "" {
		percent, err := strconv.ParseFloat(remainingPercent.String(), 64)
		if err != nil || percent < 0 || math.IsInf(percent, 0) || math.IsNaN(percent) {
			return providerUsageWindow{}, false, false
		}
		window.Unit = "percent"
		window.Remaining = jsonNumber(remainingPercent)
	} else if total != "" {
		limitValue, err := strconv.ParseFloat(total.String(), 64)
		if err != nil || limitValue < 0 || math.IsInf(limitValue, 0) || math.IsNaN(limitValue) {
			return providerUsageWindow{}, false, false
		}
		window.Limit = jsonNumber(total)
	} else {
		return providerUsageWindow{}, false, true
	}
	if at := miniMaxAbsoluteTime(resetMillis); at != nil {
		window.ResetAt = at
	}
	return window, true, true
}

func jsonNumber(value json.Number) *json.Number {
	encoded := value
	return &encoded
}

func miniMaxAbsoluteTime(resetMillis json.Number) *time.Time {
	if resetMillis != "" {
		millis, err := strconv.ParseInt(resetMillis.String(), 10, 64)
		if err == nil && millis > 0 {
			observed := time.UnixMilli(millis).UTC()
			if observed.Year() >= 2020 && observed.Year() <= 2200 {
				return &observed
			}
		}
	}
	return nil
}

func safeMiniMaxQuotaName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "general", "video", "image", "image-01", "speech", "music", "audio":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func safeUsageCurrency(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "CNY", "USD":
		return strings.ToUpper(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func safeProviderUsageErrorCode(raw string) string {
	switch raw {
	case providerUsageErrorCredentialStore, providerUsageErrorNotConfigured, providerUsageErrorKeyUnavailable,
		providerUsageErrorProviderDown, providerUsageErrorAuth, providerUsageErrorRejected,
		providerUsageErrorInvalidResponse, providerUsageErrorManagementKey:
		return raw
	default:
		return ""
	}
}

func supportedProviderUsageProvider(provider string) bool {
	switch provider {
	case string(automationagent.ProviderDeepSeek), string(automationagent.ProviderMiniMax), string(automationagent.ProviderOpenRouter):
		return true
	default:
		return false
	}
}

func safeProviderUsageStatus(status string) bool {
	switch status {
	case providerUsageStatusAvailable, providerUsageStatusNotConfigured, providerUsageStatusNotSupported,
		providerUsageStatusUnavailable, providerUsageStatusError:
		return true
	default:
		return false
	}
}

func safeBillingModel(provider, model string) bool {
	switch provider {
	case string(automationagent.ProviderDeepSeek):
		return model == "token"
	case string(automationagent.ProviderMiniMax):
		return model == "subscription_quota"
	case string(automationagent.ProviderOpenRouter):
		return model == "management_api_not_supported"
	default:
		return false
	}
}

func safeCredentialScope(scope string) bool {
	return scope == "project" || scope == "separate_management_credential_required"
}
