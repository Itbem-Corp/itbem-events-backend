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
Only accepted/rejected receipt costs with a nonempty priced `pricing_basis`
enter verified cost subtotals. A missing or `unpriced` basis remains unknown.
Only accepted/rejected usage measurements enter verified token subtotals. If any
cost or usage is unknown, corresponding full totals/averages are null and the
unknown counts remain visible. Positive observed latencies alone enter median
and nearest-rank p95; the sample count is reported. Boolean, negative, nonnumeric
and nonfinite latencies are rejected rather than entering statistical results.

Token coverage is evaluated separately for every dimension. Missing cache or
reasoning measurements do not erase verified input/output tokens.
`verified_tokens` contains observed subtotals, `unknown_token_counts` counts
missing outcomes per dimension, and `total_tokens` is null only for dimensions
with incomplete coverage. Per-case and per-success averages follow the same
dimension-specific rule; unknown receipt outcomes remain in the denominator.

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

## Historical spend reconciliation

`scripts/inference-spend-report.sql` reads the per-call gateway ledger using an
existing authorized database session. It starts a repeatable-read, read-only
transaction with a 30-second statement timeout. It exports numeric accounting
and provider/model metadata, never prompts, answers or credentials. Do not put
database credentials in command arguments or committed files.

The report groups lifetime and the last thirty days by provider/model, currency
and historical pricing basis. It splits uncached input, cached input, cache
writes and output costs, preserves billable rejections and counts unresolved
or ambiguous outcomes as unknown. A complete monetary total is null when any
cost is unknown; verified spend remains an observed subtotal. Reasoning tokens
are part of output and must not be added again.

It also identifies receipts absent from both portfolio projections and reports
unbound legacy rows separately. Do not add legacy totals to receipt totals:
missing bindings do not prove disjoint calls. Missing or inconsistent native
cache evidence is counted explicitly; recorded cache tokens alone cannot prove
a complete cache-hit percentage. No historical row is changed or repriced.

`python3 scripts/test-inference-spend-postgres.py` validates the actual SQL with
synthetic PostgreSQL 16 rows. This test is required by lint and deployment CI.
Synthetic test output is not a production spending report.
