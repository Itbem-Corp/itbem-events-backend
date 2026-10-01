# Common inference accounting and tuning

Every supported gateway route uses the same `services/automationcost` token
validation, normalization, immutable receipt and numeric diagnostic profile.
The model identifier does not select a separate accounting implementation.
Reported Stagehand inference usage uses the same input/output verification and
numeric allowlist before costing; its private report remains the audit artifact.
Unknown models remain unpriced unless the server-owned catalog supplies an
explicit model or provider rate; admission still requires a verified price.

## Read the right denominator

`diagnostics.usage` is recorded for accepted and billable rejected responses:

- `cache_hit_input_percent`: cached input divided by all input. This is not a
  fraction of total tokens or total cost.
- `output_total_percent`: output divided by total tokens.
- `reasoning_output_percent`: reported reasoning divided by output. Reasoning
  is already part of output, so never add it to output or bill it a second time.
- `answer_tokens`: output minus reported reasoning. When the provider does not
  report reasoning separately, both this field and reasoning stay null.
- `output_budget_percent` and `near_output_limit`: output compared with the
  request's admitted completion ceiling; the flag starts at 90%. This is an
  observation, not permission to retry or increase the ceiling.

Missing optional cache/reasoning counters remain null in this profile. Explicit
zero stays zero. Missing input/output, non-integer or non-finite counts,
contradictory aliases, impossible cache splits and reasoning exceeding output
cannot become accepted, verified accounting. Existing ambiguous-call handling
preserves the reservation and prevents automatic billable replay.

## Supported usage protocols

| Protocol | Input | Cache | Reasoning |
| --- | --- | --- | --- |
| Chat Completions (MiniMax, DeepSeek, OpenRouter, compatible future models) | `prompt_tokens` includes cache | `prompt_tokens_details.cached_tokens`; DeepSeek also reports `prompt_cache_hit_tokens` | `completion_tokens_details.reasoning_tokens` |
| Responses (OpenAI, compatible routes) | `input_tokens` includes cache | `input_tokens_details.cached_tokens` | `output_tokens_details.reasoning_tokens` |
| Native Messages (Anthropic, OpenCode Go Messages routes) | `input_tokens` excludes native cache read/write | add `cache_read_input_tokens` and `cache_creation_input_tokens` once to normalize total input | keep reported numeric reasoning if available; otherwise unknown |

Normalized aggregate input already includes cache. Reviews and plan repairs
normalize each call before summing so nested counters and mixed aliases survive.
If any call omits an optional counter, the aggregate omits that dimension rather
than presenting a partial sum as a verified total.
Final call/receipt identity survives a repair. Aggregates are audit summaries;
per-call gateway receipts remain the financial source of truth, including failed
and repaired attempts. Do not replace them with the last call's total or charge
an aggregate again. Historical immutable receipts are not rewritten by this fix.

## Reasoning control

Each diagnostic attempt also records `reasoning_wire_mode` and
`reasoning_wire_effort`, projected from the adapter's outgoing parameters.
Compare these with the requested `reasoning_enabled` / `reasoning_effort`.
`omitted` means provider defaults apply, not that reasoning was disabled.
This records transmitted controls; it cannot prove the provider honored them.

Omitting a parameter does not mean reasoning is disabled. DeepSeek defaults to
thinking enabled with high effort, so both `enabled` and `disabled` are explicit.
OpenRouter receives explicit `reasoning.enabled`, with effort only when enabled.
Models that mandate reasoning may reject an off request; do not silently enable
it or raise effort in response. MiniMax M3 retains its binary native control;
no fabricated effort levels are introduced.

OpenAI Responses, OpenCode Go and Anthropic have different model-specific
reasoning controls. The current adapters do not implement every combination
exposed by external catalogs. Treat a catalog's reasoning capability as model
metadata, not proof that a saved toggle/effort was transmitted. Qualify a new
adapter/model's wire request with a local HTTP fixture before enabling its route.
Do not add one universal `reasoning_effort` or cache flag to all providers.

## Tune by operation and measured quality

1. Use local fixtures to verify the exact admitted route, outgoing reasoning
   controls, numeric usage shape, truncation outcome and receipt identity.
2. Compare a frozen synthetic corpus using the isolated harness with its existing
   admission budget, sequential calls, output ceiling and no ambiguous replay.
   Keep policy/corpus revisions and all per-call receipts with each result.
3. Compare correctness, false positives, truncation, repair call count, output
   and reasoning tokens, cost per accepted result, and latency. A cache hit ratio
   alone does not qualify a route. Missing usage is unknown rather than zero.
4. Lower effort or completion ceilings per operation only after the lower-cost
   route still meets its quality gate. Do not globally reduce review output to
   the chat budget: output and reasoning usually share the same ceiling.
5. Keep static instructions at the start of the prompt, followed by dynamic
   task context. Do not change prompt boundaries, enable paid cache writes,
   extend cache TTL, warm caches with paid calls, or retry to improve cache stats
   without a separate measured admission decision.

Current ceilings remain: chat/plan/QA/summary 4096, ideation/implementation 8192,
review 32768 (further limited by model capability), publication 0. These are
ceilings, not targets. This change does not alter frozen routes, production
policies, model ordering, retry rules or admission budgets.

Primary provider references:
- https://api-docs.deepseek.com/guides/thinking_mode/
- https://api-docs.deepseek.com/api/create-chat-completion/
- https://platform.minimax.io/docs/api-reference/text-prompt-caching
- https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- https://openrouter.ai/docs/guides/best-practices/reasoning-tokens

## Operation optimization rollout

Segmented reviews now place the common instructions and bounded frozen test
patches before the segment index, permitted file list and segment-specific
support. This increases the reusable identical prefix without admitting a
finding outside the segment's annotated changed lines. Concise review wording
must preserve every distinct grounded defect; it is not a finding-count cap.
The existing single bounded repair and ambiguous-call replay protection remain.

`GET /automation/tasks/:id/inference-spend` uses normal primary-root authorization
and numeric-only, no-store responses. It aggregates **every receipt across all
task runs**, rather than the final execution projection. Accepted and resolved
billable rejections contribute verified input/output and stored historical costs.
Groups preserve provider, model, currency and pricing basis. Reserved, ambiguous
or unpriced calls leave the complete cost null while the verified subtotal remains
available. Unbound legacy execution costs are explicitly excluded; do not add
this report to the portfolio or present it as a provider invoice.

Candidate tuning must be qualified before changing production policy:

| Operation | Candidate to compare | Current ceiling retained until qualification |
| --- | --- | --- |
| Informational chat, extraction, summary | Reasoning off where supported; smaller output budget | 4096 |
| Assessment, QA | Lower supported effort; retain high for difficult cases | 4096 |
| Planning, ideation, implementation | Concise complete artifacts with operation-specific budgets | 4096 / 8192 |
| Exact-SHA review | Shared prefix and concise output first; lower effort only after defect retention passes | 32768, model-clamped |

M3 has binary reasoning, not low/medium/high effort. Providers that require
reasoning cannot be qualified with an unsupported off setting. All comparisons
must use server-owned sealed routes and normal admission. Do not mutate frozen
tasks or reuse a benchmark's budget to admit an additional comparison batch.

Use `scripts/compare_inference_efficiency.py --baseline baseline.json --proposed
proposed.json --output comparison.json` on scored reports from the same frozen
corpus. It rejects incomplete/different corpora and incomplete costs, requires all
twenty proposed cases to pass, and blocks lost successful cases, extra false
positives, invalid clean reviews, truncations, errors or route changes. It reports
cost reduction, output and p95 latency; it never automatically promotes a policy.
Twenty synthetic cases are still a screening, not production review certification.
Do not claim savings from local fixtures: provider calls, repair rate and realized
cache hits require a separately admitted live measurement through the gateway.

Output ceilings must also reach the provider in its native request field.
DeepSeek requires `max_tokens`, not OpenAI's `max_completion_tokens`
([official integration contract](https://api-docs.deepseek.com/quick_start/agent_integrations/oh_my_pi/)).
The shared adapter translates the existing bounded limit without changing the
sealed route, effort, quota or model catalogue checks. OpenAI Chat and OpenRouter
retain `max_completion_tokens`; Responses and Anthropic retain their existing
API-specific fields. HTTP contract tests cover DeepSeek thinking on/off at both
the 4096 screening and 32768 review limits and reject the unsupported alias.
One pre-fix review reported 35780 output tokens against a requested 32768 limit;
this is evidence of an ineffective request ceiling, not proof of future savings.
