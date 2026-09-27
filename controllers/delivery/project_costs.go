package delivery

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/utils"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	defaultProjectCostPageSize = 20
	maxProjectCostPageSize     = 100
	maxProjectCostCursorBytes  = 2048
)

type projectCostCursor struct {
	Version         int    `json:"v"`
	Scope           string `json:"scope"`
	TotalCostMicros int64  `json:"total_cost_microusd"`
	WorkItemID      string `json:"work_item_id"`
}

// deliveryCostLedgerUnion keeps every Delivery cost surface aligned with the
// immutable primary-agent and tool ledgers. It intentionally omits private
// request/response references so these aggregate endpoints remain safe.
const deliveryCostLedgerUnion = `SELECT id, automation_task_id, delivery_work_item_id, step_key, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, input_cost_micros, output_cost_micros, cached_cost_micros, cache_write_cost_micros, total_cost_micros, currency, pricing_basis, completed_at, 'agent' AS execution_kind, '' AS tool FROM automation_executions UNION ALL SELECT id, automation_task_id, delivery_work_item_id, step_key, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens, total_tokens, input_cost_micros, output_cost_micros, cached_cost_micros, cache_write_cost_micros, total_cost_micros, currency, pricing_basis, completed_at, 'tool' AS execution_kind, tool FROM automation_tool_executions`

const deliveryCostUnknownPricingBases = "('', 'legacy', 'unpriced')"

// Only explicit USD amounts with a concrete basis contribute to the USD
// subtotal. Custom catalog bases remain valid: automationcost accepts
// operator-defined basis labels when it records USD-per-token rates.
func deliveryCostPricingBasisIsUnknown(basis string) bool {
	switch strings.ToLower(strings.TrimSpace(basis)) {
	case "", "legacy", "unpriced":
		return true
	default:
		return false
	}
}

func deliveryCostRowHasVerifiedUSDPrice(currency, basis string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "USD") && !deliveryCostPricingBasisIsUnknown(basis)
}

func deliveryCostUnpricedCondition() string {
	return "(UPPER(BTRIM(COALESCE(execution.currency, ''))) <> 'USD' OR LOWER(BTRIM(COALESCE(execution.pricing_basis, ''))) IN " + deliveryCostUnknownPricingBases + ")"
}

func deliveryCostKnownUSDSum(column string) string {
	return "COALESCE(SUM(" + column + ") FILTER (WHERE NOT " + deliveryCostUnpricedCondition() + "), 0)"
}

func deliveryCostUnpricedCount() string {
	return "COUNT(*) FILTER (WHERE " + deliveryCostUnpricedCondition() + ")"
}

// projectCostSummary is an all-time delivery allocation view. Monthly budget
// enforcement is intentionally separate: a project should retain its full
// historical AI cost even after a billing month changes.
type projectCostSummary struct {
	Executions           int64 `json:"executions"`
	WorkItems            int64 `json:"work_items"`
	UnpricedExecutions   int64 `json:"unpriced_executions"`
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
	UnpricedExecutions   int64  `json:"unpriced_executions"`
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
	UnpricedExecutions   int64     `json:"unpriced_executions"`
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
	limit, err := projectCostPageSize(c.QueryParam("limit"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid project cost page", err.Error())
	}
	workItemScope := projectCostWorkItemCursorScope(projectID)
	cursor, err := decodeProjectCostCursor(c.QueryParam("cursor"), workItemScope)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid project cost cursor", "Cursor does not match the selected project")
	}
	base := configuration.DB.Table("("+deliveryCostLedgerUnion+") AS execution").
		Joins("JOIN delivery_work_items AS work_item ON work_item.id = execution.delivery_work_item_id AND work_item.deleted_at IS NULL").
		Where("work_item.project_id = ?", projectID)
	summary := projectCostSummary{}
	if err := base.Session(&gorm.Session{}).Select(projectCostSummarySelect()).Scan(&summary).Error; err != nil {
		return utilsError(c, err)
	}
	steps := make([]projectCostStep, 0)
	if err := base.Session(&gorm.Session{}).Select(projectCostStepSelect()).Group("execution.step_key, execution.execution_kind, execution.tool").Order(deliveryCostKnownUSDSum("execution.total_cost_micros") + " DESC").Scan(&steps).Error; err != nil {
		return utilsError(c, err)
	}
	workItems := make([]projectCostWorkItem, 0, limit+1)
	workItemQuery := base.Session(&gorm.Session{}).Select(projectCostWorkItemSelect()).Group("work_item.id, work_item.title")
	if cursor != nil {
		cursorID, _ := uuid.FromString(cursor.WorkItemID) // validated by decodeProjectCostCursor
		knownCost := deliveryCostKnownUSDSum("execution.total_cost_micros")
		workItemQuery = workItemQuery.Having(knownCost+" < ? OR ("+knownCost+" = ? AND work_item.id > ?)", cursor.TotalCostMicros, cursor.TotalCostMicros, cursorID)
	}
	if err := workItemQuery.Order(deliveryCostKnownUSDSum("execution.total_cost_micros") + " DESC, work_item.id ASC").Limit(limit + 1).Scan(&workItems).Error; err != nil {
		return utilsError(c, err)
	}
	nextCursor := ""
	if len(workItems) > limit {
		workItems = workItems[:limit]
		last := workItems[len(workItems)-1]
		nextCursor = encodeProjectCostCursor(projectCostCursor{Version: 1, Scope: workItemScope, TotalCostMicros: last.TotalCostMicros, WorkItemID: last.WorkItemID.String()})
	}
	return success(c, "Delivery project cost summary", map[string]any{
		"summary": summary, "by_step": steps, "by_work_item": workItems,
		"by_work_item_limit": limit, "by_work_item_next_cursor": nextCursor,
	})
}

func projectCostSummarySelect() string {
	return strings.Join([]string{
		"COUNT(*) AS executions",
		"COUNT(DISTINCT work_item.id) AS work_items",
		"COALESCE(SUM(execution.input_tokens), 0) AS input_tokens",
		"COALESCE(SUM(execution.output_tokens), 0) AS output_tokens",
		"COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens",
		"COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens",
		"COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens",
		"COALESCE(SUM(execution.total_tokens), 0) AS total_tokens",
		deliveryCostKnownUSDSum("execution.input_cost_micros") + " AS input_cost_micros",
		deliveryCostKnownUSDSum("execution.output_cost_micros") + " AS output_cost_micros",
		deliveryCostKnownUSDSum("execution.cached_cost_micros") + " AS cached_cost_micros",
		deliveryCostKnownUSDSum("execution.cache_write_cost_micros") + " AS cache_write_cost_micros",
		deliveryCostKnownUSDSum("execution.total_cost_micros") + " AS total_cost_micros",
		deliveryCostUnpricedCount() + " AS unpriced_executions",
	}, ", ")
}

func projectCostStepSelect() string {
	return strings.Join([]string{
		"COALESCE(NULLIF(execution.step_key, ''), 'execution') AS key",
		"execution.execution_kind", "execution.tool", "COUNT(*) AS executions",
		"COUNT(DISTINCT work_item.id) AS work_items",
		"COALESCE(SUM(execution.input_tokens), 0) AS input_tokens",
		"COALESCE(SUM(execution.output_tokens), 0) AS output_tokens",
		"COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens",
		"COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens",
		"COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens",
		"COALESCE(SUM(execution.total_tokens), 0) AS total_tokens",
		deliveryCostKnownUSDSum("execution.input_cost_micros") + " AS input_cost_micros",
		deliveryCostKnownUSDSum("execution.output_cost_micros") + " AS output_cost_micros",
		deliveryCostKnownUSDSum("execution.cached_cost_micros") + " AS cached_cost_micros",
		deliveryCostKnownUSDSum("execution.cache_write_cost_micros") + " AS cache_write_cost_micros",
		deliveryCostKnownUSDSum("execution.total_cost_micros") + " AS total_cost_micros",
		deliveryCostUnpricedCount() + " AS unpriced_executions",
	}, ", ")
}

func projectCostWorkItemSelect() string {
	return strings.Join([]string{
		"work_item.id AS work_item_id", "work_item.title AS work_item_title", "COUNT(*) AS executions",
		deliveryCostUnpricedCount() + " AS unpriced_executions",
		"COALESCE(SUM(execution.input_tokens), 0) AS input_tokens",
		"COALESCE(SUM(execution.output_tokens), 0) AS output_tokens",
		"COALESCE(SUM(execution.cached_input_tokens), 0) AS cached_input_tokens",
		"COALESCE(SUM(execution.cache_write_tokens), 0) AS cache_write_tokens",
		"COALESCE(SUM(execution.reasoning_tokens), 0) AS reasoning_tokens",
		"COALESCE(SUM(execution.total_tokens), 0) AS total_tokens",
		deliveryCostKnownUSDSum("execution.input_cost_micros") + " AS input_cost_micros",
		deliveryCostKnownUSDSum("execution.output_cost_micros") + " AS output_cost_micros",
		deliveryCostKnownUSDSum("execution.cached_cost_micros") + " AS cached_cost_micros",
		deliveryCostKnownUSDSum("execution.cache_write_cost_micros") + " AS cache_write_cost_micros",
		deliveryCostKnownUSDSum("execution.total_cost_micros") + " AS total_cost_micros",
	}, ", ")
}

func projectCostWorkItemCursorScope(projectID uuid.UUID) string {
	return "project-costs:" + projectID.String() + ":work-items"
}

func encodeProjectCostCursor(cursor projectCostCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeProjectCostCursor(raw, scope string) (*projectCostCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxProjectCostCursorBytes {
		return nil, fmt.Errorf("cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("cursor encoding is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cursor projectCostCursor
	if err := decoder.Decode(&cursor); err != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Version != 1 || cursor.Scope != scope {
		return nil, fmt.Errorf("cursor is invalid or belongs to another project")
	}
	id, err := uuid.FromString(cursor.WorkItemID)
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("cursor work item ID must be a UUID")
	}
	cursor.WorkItemID = id.String()
	return &cursor, nil
}

func projectCostPageSize(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultProjectCostPageSize, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 || value > maxProjectCostPageSize {
		return 0, fmt.Errorf("limit must be between 1 and %d", maxProjectCostPageSize)
	}
	return value, nil
}
