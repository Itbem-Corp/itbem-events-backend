# Controlled synthetic prefix evaluation

The `synthetic-prefix-cache-20-2026-10-01-v1` server-owned profile adds a matched comparison after the original screening. The original corpus, task IDs, receipts, answers and scoring remain unchanged. Admission accepts only these two embedded versions; it never accepts client prompts, routes, expected answers or larger budgets.

Ten review cases are each evaluated with the same reference protocol and case evidence under two conditions. The shared condition reuses one opaque partition label before the reference. The control condition uses a distinct, equally long label for each pair before the reference. The reference is useful synthetic review guidance, not repeated filler. The first five cases contain defects and the last five are clean. Within-pair order alternates between control-first and shared-first. Each case is sent unchanged to MiniMax M3 with binary reasoning, DeepSeek Flash high and GPT-6 Luna high. Dispatch remains serial across all 60 calls.

The control is **not guaranteed cold**: providers can reuse earlier message segments or already have matching context. The shared condition is **not guaranteed a hit**: routing, account namespaces, cache thresholds and retention are provider-controlled. Reference bytes or word counts are not native token counts. Cached input counters provide evidence of observed reuse; total input alone does not establish the number of eligible visible prefix tokens. No provider-specific request cache switches are assumed supported or added by this experiment.

## Admission and execution

The normal root dashboard chooses the profile, admits a batch, then dispatches through task leases and the central gateway. Selecting or admitting a profile does not itself execute inference. The server retains the maximum of 60 calls, concurrency one, 4096 completion tokens, a USD 1 conservative API-equivalent budget, immutable price/route/prompt snapshots, encrypted input, and one admission per actor/version. A new version cannot run while another batch is active. Failed, completed or halted batches cannot be automatically replaced with a new UUID for the same actor/version. An ambiguous billed outcome stops execution and must not be retried automatically.

The worker scope must be rebuilt for the new admitted batch's exact 60 UUIDs and compared with the server export before starting the isolated orchestration worker. Leave the review worker stopped. Existing authorization for the first 60-call screening was consumed: a new billable batch requires its own bounded authorization. Do not run this profile merely because its UI is deployed. No global policy updates or provider calls outside the gateway are part of this procedure.

## Offline analysis

`scripts/build_cache_evaluation_corpus.py` deterministically builds the embedded prompts and the separate offline answer key from the reference and the original ten review cases. Only IDs and prompts are embedded. `scripts/analyze_cache_evaluation.py` verifies the trusted corpus and prompt hashes, sealed candidate reasoning routes, accepted identities, actual attribution, format and exact answers. Incomplete calls remain in the denominator.

Run after exporting all normal dashboard receipts and final answers:

```sh
python scripts/analyze_cache_evaluation.py \
  --corpus scripts/model-evaluation-cache-corpus.json \
  --evidence /path/to/new-batch-final-evidence.json \
  --output /path/to/new-batch-cache-report.json
```

The report includes all ten observations per condition per model, including the first shared request's warmup. It compares matched quality and input cost, distinguishes output cost changes and total cost changes, and compares native read/write counters. It reconstructs uncached input, cached input, cache-write and output components using each receipt's immutable historical rates and the ledger's integer rounding; each reconstructed total must match the ledger. Changed rate snapshots within a candidate invalidate cost comparison. Missing native counters remain unknown. Conservative ledger operands for missing reads/writes do not turn those counters into observed zeros.

`qualified_with_complete_accounting` is true only for a completed run with all pairs correct, known read/write counters, more shared cached tokens, lower shared input cost and lower total cost. Positive observed reads with missing write counters remain useful partial evidence but cannot satisfy that flag. An output increase that erases input savings also prevents it. Even a positive result is a limited serial synthetic observation, not a statistical certification, invoice reconciliation, guaranteed cache rate or a decision to change production model policies. MiniMax subscription quota remains API-equivalent accounting, not a cash invoice.

Tests use synthetic completions only: Python verifies paired scoring, accounting, absence of answer keys in embedded inputs and deterministic generation; Go verifies immutable version resolution, matching evidence, balanced order and full worker-envelope budget; PostgreSQL integration verifies normal admission, concurrent dispatch serialization and refusal to repeat a halted profile under a fresh ID.
