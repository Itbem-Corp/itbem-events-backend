# Auditable model screening

This offline scorer evaluates the twenty server-owned synthetic cases for each
of the three frozen candidates. It neither contacts providers nor reads credentials,
admits tasks, retries calls, or executes generated code.

Run after collecting all sixty outcomes through the authorized gateway:

```sh
python scripts/score_model_evaluation.py \
  --corpus scripts/model-evaluation-screening-corpus.json \
  --evidence /private/path/evidence.json --output /private/path/score.json
```

Evidence contains `batch` (status, budget_microusd, reservation_microusd) and
`calls`. Each call binds case_id, candidate, task_id, prompt_sha256, status,
receipt_status, actual_provider/model, sealed_routes_json, final_answer,
finish_reason, gateway_latency_ms and result_error when present. Accepted calls
require run_id and receipt_id. The prompt hash is SHA-256 of the trimmed shared
system instruction, two newlines, and trimmed case prompt.

Ledger fields are total_cost_microusd, input_tokens, output_tokens,
cached_input_tokens, cache_write_tokens and reasoning_tokens. Use null or omit
unavailable measurements. Never infer zero from an ambiguous receipt.
Only accepted/rejected receipt measurements enter verified subtotals. If any
cost or usage is unknown, corresponding full totals/averages are null and the
unknown counts remain visible. Positive observed latencies alone enter median
and nearest-rank p95; the sample count is reported.

Errors remain in the twenty-case denominator. Truncation and unexpected actual
routes cannot count as successes. Accepted sealed routes must match the frozen
candidate and reasoning configuration. Duplicate/missing case bindings, altered
prompts, absent task identities and missing accepted receipt identities fail closed.

Final answers must come from sanitized audited storage and must exclude private
reasoning. Keep evidence files private; commit neither real provider payloads nor
production outcome files. The report omits answer text.

The JSON corpus here includes expected answers for scoring. Admission continues
to use the server corpus; a CI test verifies that prompts match. Tests use clearly
synthetic outcomes, not claimed model results. Twenty cases are a screening,
not certification or multi-file implementation validation. MiniMax costs are
API-equivalent estimates, not subscription invoices.
