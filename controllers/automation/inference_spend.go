package automation

import (
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// Receipt accounting is separate from execution projections: an initial call,
// repair and billable rejection each count once, including older task runs.
const taskInferenceSpendSQL = `SELECT provider, model, currency, pricing_basis,
 COUNT(*) AS calls,
 COUNT(*) FILTER (WHERE resolved_at IS NOT NULL AND status IN ('accepted','rejected')) AS observed_calls,
 COUNT(*) FILTER (WHERE NOT (resolved_at IS NOT NULL AND status IN ('accepted','rejected') AND pricing_basis <> '' AND pricing_basis <> 'unpriced')) AS unknown_cost_calls,
 COALESCE(SUM(input_tokens) FILTER (WHERE resolved_at IS NOT NULL AND status IN ('accepted','rejected')),0) AS verified_input_tokens,
 COALESCE(SUM(output_tokens) FILTER (WHERE resolved_at IS NOT NULL AND status IN ('accepted','rejected')),0) AS verified_output_tokens,
 COALESCE(SUM(input_cost_micros) FILTER (WHERE resolved_at IS NOT NULL AND status IN ('accepted','rejected') AND pricing_basis <> '' AND pricing_basis <> 'unpriced'),0) AS verified_input_cost_micros,
 COALESCE(SUM(output_cost_micros) FILTER (WHERE resolved_at IS NOT NULL AND status IN ('accepted','rejected') AND pricing_basis <> '' AND pricing_basis <> 'unpriced'),0) AS verified_output_cost_micros,
 COALESCE(SUM(cached_cost_micros) FILTER (WHERE resolved_at IS NOT NULL AND status IN ('accepted','rejected') AND pricing_basis <> '' AND pricing_basis <> 'unpriced'),0) AS verified_cached_cost_micros,
 COALESCE(SUM(cache_write_cost_micros) FILTER (WHERE resolved_at IS NOT NULL AND status IN ('accepted','rejected') AND pricing_basis <> '' AND pricing_basis <> 'unpriced'),0) AS verified_cache_write_cost_micros,
 COALESCE(SUM(total_cost_micros) FILTER (WHERE resolved_at IS NOT NULL AND status IN ('accepted','rejected') AND pricing_basis <> '' AND pricing_basis <> 'unpriced'),0) AS verified_cost_micros,
 CASE WHEN COUNT(*) FILTER (WHERE NOT (resolved_at IS NOT NULL AND status IN ('accepted','rejected') AND pricing_basis <> '' AND pricing_basis <> 'unpriced')) = 0 THEN SUM(total_cost_micros) ELSE NULL END AS complete_cost_micros
 FROM automation_inference_receipts WHERE automation_task_id = ?
 GROUP BY provider, model, currency, pricing_basis ORDER BY provider, model, currency, pricing_basis`

type taskInferenceSpend struct {
	Provider                     string `json:"provider"`
	Model                        string `json:"model"`
	Currency                     string `json:"currency"`
	PricingBasis                 string `json:"pricing_basis"`
	Calls                        int64  `json:"calls"`
	ObservedCalls                int64  `json:"observed_calls"`
	UnknownCostCalls             int64  `json:"unknown_cost_calls"`
	VerifiedInputTokens          int64  `json:"verified_input_tokens"`
	VerifiedOutputTokens         int64  `json:"verified_output_tokens"`
	VerifiedCostMicros           int64  `json:"verified_cost_microusd"`
	VerifiedInputCostMicros      int64  `json:"verified_input_cost_microusd"`
	VerifiedOutputCostMicros     int64  `json:"verified_output_cost_microusd"`
	VerifiedCachedCostMicros     int64  `json:"verified_cached_cost_microusd"`
	VerifiedCacheWriteCostMicros int64  `json:"verified_cache_write_cost_microusd"`
	CompleteCostMicros           *int64 `json:"complete_cost_microusd"`
}

// GetTaskInferenceSpend exposes numeric accounting only through normal root
// authorization. It never loads private captures or changes immutable receipts.
func GetTaskInferenceSpend(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	c.Response().Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.FromString(c.Param("id"))
	if err != nil || id == uuid.Nil {
		return utils.Error(c, 400, "Invalid task", "")
	}
	if configuration.DB == nil {
		return utils.Error(c, 503, "Accounting unavailable", "")
	}
	rows := make([]taskInferenceSpend, 0)
	if err := configuration.DB.WithContext(c.Request().Context()).Raw(taskInferenceSpendSQL, id).Scan(&rows).Error; err != nil {
		return utils.Error(c, 503, "Accounting unavailable", "")
	}
	return utils.Success(c, 200, "Task inference accounting", map[string]any{
		"task_id": id, "source": "gateway_receipts", "scope": "all_task_runs",
		"groups": rows, "legacy_execution_costs_included": false,
	})
}
