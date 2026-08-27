package delivery

import (
	"events-stocks/configuration"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// deliveryCostLedgerUnion keeps every Delivery cost surface aligned with the
// immutable primary-agent and tool ledgers. It intentionally omits private
// request/response references so these aggregate endpoints remain safe.
const deliveryCostLedgerUnion = `SELECT id, automation_task_id, delivery_work_item_id, step_key, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, input_cost_micros, output_cost_micros, cached_cost_micros, cache_write_cost_micros, total_cost_micros, completed_at, 'agent' AS execution_kind, '' AS tool FROM automation_executions UNION ALL SELECT id, automation_task_id, delivery_work_item_id, step_key, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, input_cost_micros, output_cost_micros, cached_cost_micros, cache_write_cost_micros, total_cost_micros, completed_at, 'tool' AS execution_kind, tool FROM automation_tool_executions`

// projectCostSummary is an all-time delivery allocation view. Monthly budget
// enforcement is intentionally separate: a project should retain its full
// historical AI cost even after a billing month changes.
type projectCostSummary struct {
	Executions           int64 `json:"executions"`
	WorkItems            int64 `json:"work_items"`
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CachedInputTokens    int64 `json:"cached_input_tokens"`
	CacheWriteTokens     int64 `json:"cache_write_tokens"`
	ReasoningTokens      int64 `json:"reasoning_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
	InputCostMicros      int64 `json:"input_cost_microusd"`
	OutputCostMicros     int64 `json:"output_cost_microusd"`
	CachedCostMicros     int64 `json:"cached_cost_microusd"`
	CacheWriteCostMicros int64 `json:"cache_write_cost_microusd"`
	TotalCostMicros      int64 `json:"total_cost_microusd"`
}

type projectCostStep struct {
	Key                  string `json:"key"`
	ExecutionKind        string `json:"execution_kind"`
	Tool                 string `json:"tool,omitempty"`
	Executions           int64  `json:"executions"`
	WorkItems            int64  `json:"work_items"`
	InputTokens          int64  `json:"input_tokens"`
	OutputTokens         int64  `json:"output_tokens"`
	CachedInputTokens    int64  `json:"cached_input_tokens"`
	CacheWriteTokens     int64  `json:"cache_write_tokens"`
	ReasoningTokens      int64  `json:"reasoning_tokens"`
	TotalTokens          int64  `json:"total_tokens"`
	InputCostMicros      int64  `json:"input_cost_microusd"`
	OutputCostMicros     int64  `json:"output_cost_microusd"`
	CachedCostMicros     int64  `json:"cached_cost_microusd"`
	CacheWriteCostMicros int64  `json:"cache_write_cost_microusd"`
	TotalCostMicros      int64  `json:"total_cost_microusd"`
}

type projectCostWorkItem struct {
	WorkItemID           uuid.UUID `json:"work_item_id"`
	WorkItemTitle        string    `json:"work_item_title"`
	Executions           int64     `json:"executions"`
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`
	CachedInputTokens    int64     `json:"cached_input_tokens"`
	CacheWriteTokens     int64     `json:"cache_write_tokens"`
	ReasoningTokens      int64     `json:"reasoning_tokens"`
	TotalTokens          int64     `json:"total_tokens"`
	InputCostMicros      int64     `json:"input_cost_microusd"`
	OutputCostMicros     int64     `json:"output_cost_microusd"`
	CachedCostMicros     int64     `json:"cached_cost_microusd"`
	CacheWriteCostMicros int64     `json:"cache_write_cost_microusd"`
	TotalCostMicros      int64     `json:"total_cost_microusd"`
}

// GetProjectCostSummary gives a project member its historical cost topology
// without exposing prompts, response bodies or object-store references.
func GetProjectCostSummary(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	base := configuration.DB.Table("("+deliveryCostLedgerUnion+") AS execution").
		Joins("JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id AND work_item.deleted_at IS NULL").
		Where("work_item.project_id = ?", projectID)
	summary := projectCostSummary{}
	if err := base.Session(&gorm.Session{}).Select("COUNT(*) AS executions, COUNT(DISTINCT work_item.id) AS work_items, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Scan(&summary).Error; err != nil {
		return utilsError(c, err)
	}
	steps := make([]projectCostStep, 0)
	if err := base.Session(&gorm.Session{}).Select("COALESCE(NULLIF(execution.step_key, ''), 'execution') AS key, execution.execution_kind, execution.tool, COUNT(*) AS executions, COUNT(DISTINCT work_item.id) AS work_items, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("execution.step_key, execution.execution_kind, execution.tool").Order("SUM(execution.total_cost_micros) DESC").Scan(&steps).Error; err != nil {
		return utilsError(c, err)
	}
	workItems := make([]projectCostWorkItem, 0)
	if err := base.Session(&gorm.Session{}).Select("work_item.id AS work_item_id, work_item.title AS work_item_title, COUNT(*) AS executions, COALESCE(SUM(execution.input_tokens), 0) AS input_tokens, COALESCE(SUM(execution.output_tokens), 0) AS output_tokens, COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens, COALESCE(SUM(execution.total_tokens), 0) AS total_tokens, COALESCE(SUM(execution.input_cost_micros), 0) AS input_cost_micros, COALESCE(SUM(execution.output_cost_micros), 0) AS output_cost_micros, COALESCE(SUM(execution.cached_cost_micros), 0) AS cached_cost_micros, COALESCE(SUM(execution.cache_write_cost_micros), 0) AS cache_write_cost_micros, COALESCE(SUM(execution.total_cost_micros), 0) AS total_cost_micros").Group("work_item.id, work_item.title").Order("SUM(execution.total_cost_micros) DESC, work_item.title ASC").Scan(&workItems).Error; err != nil {
		return utilsError(c, err)
	}
	return success(c, "Delivery project cost summary", map[string]any{"summary": summary, "by_step": steps, "by_work_item": workItems})
}
