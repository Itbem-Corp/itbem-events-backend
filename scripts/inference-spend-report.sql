-- Run using an existing authorized database session. No credentials or content
-- are exported. Receipt costs are immutable historical values, never repriced.
-- Gateway and legacy projections are separate: never add their totals blindly.
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';

WITH windows AS (
 SELECT 'all_time' AS period, NULL::timestamptz AS from_at
 UNION ALL SELECT 'last_30_days', CURRENT_TIMESTAMP - INTERVAL '30 days'
), evidence AS (
 SELECT windows.period, receipt.*,
   COALESCE(usage_json->'cached_input_tokens', usage_json->'cache_read_input_tokens', usage_json->'cache_read_tokens', usage_json->'prompt_cache_hit_tokens', usage_json#>'{prompt_tokens_details,cached_tokens}', usage_json#>'{input_tokens_details,cached_tokens}') AS reported_cache,
   receipt.resolved_at IS NOT NULL AND receipt.status IN ('accepted','rejected') AS observed,
   receipt.resolved_at IS NOT NULL AND receipt.status IN ('accepted','rejected')
     AND BTRIM(receipt.pricing_basis) NOT IN ('','unpriced') AS priced
 FROM automation_inference_receipts receipt CROSS JOIN windows
 WHERE receipt.created_at <= CURRENT_TIMESTAMP
   AND (windows.from_at IS NULL OR receipt.created_at >= windows.from_at)
)
SELECT period, provider, model, currency, pricing_basis,
 COUNT(*) AS receipt_count,
 COUNT(*) FILTER (WHERE status = 'ambiguous') AS ambiguous_count,
 COUNT(*) FILTER (WHERE NOT observed) AS unknown_usage_count,
 COUNT(*) FILTER (WHERE NOT priced) AS unknown_cost_count,
 COUNT(*) FILTER (WHERE observed AND COALESCE(jsonb_typeof(reported_cache), '') <> 'number') AS unknown_cache_input_count,
 COUNT(*) FILTER (WHERE observed AND CASE WHEN jsonb_typeof(reported_cache) = 'number' THEN reported_cache::text::numeric <> cached_input_tokens ELSE FALSE END) AS inconsistent_cache_count,
 COALESCE(SUM(input_tokens) FILTER (WHERE observed),0) AS observed_input_tokens,
 COALESCE(SUM(output_tokens) FILTER (WHERE observed),0) AS observed_output_tokens,
 COALESCE(SUM(cached_input_tokens) FILTER (WHERE observed),0) AS recorded_cached_input_tokens,
 COALESCE(SUM(cache_write_tokens) FILTER (WHERE observed),0) AS recorded_cache_write_tokens,
 COALESCE(SUM(reasoning_tokens) FILTER (WHERE observed),0) AS recorded_reasoning_tokens,
 COALESCE(SUM(input_cost_micros) FILTER (WHERE priced),0) AS verified_uncached_input_cost_microusd,
 COALESCE(SUM(cached_cost_micros) FILTER (WHERE priced),0) AS verified_cached_input_cost_microusd,
 COALESCE(SUM(cache_write_cost_micros) FILTER (WHERE priced),0) AS verified_cache_write_cost_microusd,
 COALESCE(SUM(output_cost_micros) FILTER (WHERE priced),0) AS verified_output_cost_microusd,
 COALESCE(SUM(total_cost_micros) FILTER (WHERE priced),0) AS verified_total_cost_microusd,
 CASE WHEN COUNT(*) FILTER (WHERE NOT priced) = 0 THEN SUM(total_cost_micros) END AS complete_total_cost_microusd,
 COUNT(*) FILTER (WHERE priced AND total_cost_micros <> input_cost_micros + cached_cost_micros + cache_write_cost_micros + output_cost_micros) AS inconsistent_cost_count,
 MIN(created_at) AS first_receipt_at, MAX(created_at) AS last_receipt_at
FROM evidence GROUP BY period, provider, model, currency, pricing_basis
ORDER BY period, provider, model, currency, pricing_basis;

-- Intermediate/failed calls absent from the old portfolio. NOT EXISTS avoids
-- join fan-out and counts each receipt once, including billable rejections.
SELECT currency, COUNT(*) AS unprojected_receipt_count,
 COALESCE(SUM(receipt.total_cost_micros),0) AS unprojected_verified_cost_microusd
FROM automation_inference_receipts receipt
WHERE receipt.created_at <= CURRENT_TIMESTAMP AND receipt.resolved_at IS NOT NULL
 AND receipt.status IN ('accepted','rejected') AND BTRIM(receipt.pricing_basis) NOT IN ('','unpriced')
 AND NOT EXISTS (SELECT 1 FROM automation_executions execution WHERE execution.inference_receipt_id = receipt.id)
 AND NOT EXISTS (SELECT 1 FROM automation_tool_executions execution WHERE execution.inference_receipt_id = receipt.id)
GROUP BY currency;

-- Unbound historical rows cannot be proven disjoint from receipts. Report
-- separately; a NULL binding is not sufficient evidence to add to gateway cost.
SELECT 'legacy_agent' AS source, currency, COUNT(*) AS unbound_count, COALESCE(SUM(total_cost_micros),0) AS recorded_cost_microusd
FROM automation_executions WHERE inference_receipt_id IS NULL AND completed_at <= CURRENT_TIMESTAMP GROUP BY currency
UNION ALL
SELECT 'legacy_tool', currency, COUNT(*), COALESCE(SUM(total_cost_micros),0)
FROM automation_tool_executions WHERE inference_receipt_id IS NULL AND completed_at <= CURRENT_TIMESTAMP GROUP BY currency;

COMMIT;
