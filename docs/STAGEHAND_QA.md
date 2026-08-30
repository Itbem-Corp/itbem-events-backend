# Stagehand semantic QA

Stagehand is an opt-in browser QA layer on top of the deterministic Delivery
QA harness. It never opens a human gate, deploys, publishes, or replaces the
existing responsive screenshot checks. Its default mode is read-only. A human
may explicitly approve an isolated test-account flow that fills reviewed test
values and performs only the reviewed clicks and assertions; that is the
bounded path for real browser E2E coverage.

## Budget admission

`delivery.qa` reserves both the delivery-agent summary and the separate
Stagehand browser/semantic model call before it is queued. The default
semantic reserve is 24,000 input tokens plus 4,096 output tokens,
deliberately conservative. Operators may tune the non-secret server values
`AUTOMATION_QA_SEMANTIC_INPUT_TOKEN_RESERVE` and
`AUTOMATION_QA_SEMANTIC_OUTPUT_TOKEN_RESERVE` from observed usage. The
immutable tool ledger records the actual Stagehand usage after execution.

## Local setup

Install the pinned runner once from this repository:

```powershell
Set-Location tools/stagehand-qa
npm install --ignore-scripts
```

The pinned Stagehand release requires Node `^20.19.0` or `>=22.12.0`. The
runner verifies this before it reads provider configuration or opens a browser.
It also bounds Stagehand initialization (35 seconds), the MiniMax HTTP request
(30 seconds) and browser shutdown (10 seconds). A timeout is a failed QA run,
never an indefinitely leased worker task or a silent pass.

In the ignored `.env.ai.local`, configure a dedicated key when possible. The
Go launcher imports this local file only for local development; deployed
workers receive the same values from their secret manager.

```dotenv
STAGEHAND_QA_ENV=LOCAL
STAGEHAND_QA_MODEL=MiniMax-M3
STAGEHAND_QA_BASE_URL=https://api.minimax.io/v1
STAGEHAND_QA_API_KEY=
```

`STAGEHAND_QA_API_KEY` may be omitted only when the worker already has its
`MINIMAX_API_KEY`. Likewise, `MINIMAX_MODEL` is used when
`STAGEHAND_QA_MODEL` is not set. Values are never sent as command arguments,
persisted in the work item, or returned to the dashboard. Browserbase is
optional and must be explicitly selected with `STAGEHAND_QA_ENV=BROWSERBASE`
plus its own API key.

The runner adds the `openai/` routing prefix required by Stagehand internally
when using the MiniMax-compatible endpoint, but retains the original MiniMax
model name in ITBEM's cost ledger.

## Workspace registration

Add this exact, operator-owned command to the relevant workspace in
`ITBEM_AI_WORKSPACES_JSON`:

```json
{
  "qa_semantic_command": [
    "node", "tools/stagehand-qa/run.mjs",
    "--url", "{preview_url}",
    "--output", "{artifact_path}",
    "--plan", "{qa_plan_path}"
  ]
}
```

The control plane validates the executable and requires exactly one preview
URL and artifact path placeholder; the plan placeholder is optional and may
appear only once. It reserves evidence capacity for the structured report,
landing screenshot and bounded case screenshots, stores them with normal
private QA evidence, and reports a nonzero Stagehand verdict as a QA failure
for human review.

## Approved browser E2E cases

The planner may include `browser_qa_cases` and a `browser_qa_mode` in the plan
that a human reviews before implementation. The worker compiles only that
already-approved part into a private local JSON file; neither the model nor a
task instruction can choose a command or alter it at QA time.

`read_only` allows only same-origin navigation plus visible-element and text
assertions. `approved_navigation` additionally permits a click, but only when
the reviewed step declares one selector and the exact same-origin path that
must result.
`approved_test_flow` supports an isolated test-account flow: `fill` uses only
a reviewed `ITBEM_QA_*` environment reference, and each `click` must be
followed by a reviewed assertion. Literal credentials never enter the plan,
report, command line, MiniMax request or dashboard. The runner rejects
arbitrary scripts,
cross-origin navigation and all other action types. Every case produces a
bounded before/after screenshot pair and every step has a pass/fail record in the private
report. An `approved_test_flow` click must have an immediate post-action
assertion (`assert_visible`, `assert_text`, or `assert_path`), so a successful
click alone is never treated as evidence of a successful flow.
At most three cases are allowed: that limit deliberately reserves storage for
the private report, desktop/mobile checks, and all six visual states without
letting generic test output displace human-review evidence.
Independently of those feature-specific cases, the runner opens the
preview at 412×915, detects horizontal overflow and captures a separate mobile
image; a failure is a QA failure for the human gate. A simple approved example
is:

```json
{
  "browser_qa_mode": "approved_navigation",
  "browser_qa_cases": [{
    "id": "public-login",
    "title": "Login entry is reachable",
    "steps": [
      {"kind": "navigate", "path": "/login"},
      {"kind": "assert_visible", "selector": "form"},
      {"kind": "assert_text", "text": "Iniciar sesión"}
    ]
  }]
}
```

The Stagehand report includes the bounded approved-plan instruction, sanitized
browser findings, completed browser-case results, responsive measurement and
bounded console/request-failure signals. Console errors, aborted requests, or
observed HTTP responses with a 4xx/5xx status make the verdict fail and
therefore require human review; an
unavailable optional browser observer is recorded but does not create a false
failure. When Stagehand's compact page API does not expose a response event,
the runner independently reads Chromium Performance Timing after each reviewed
step; the report declares its observed network source. If neither route is
available, the run cannot pass because absence of telemetry is not proof of
absence of request failures. The report also includes a private provider-response excerpt when structured
extraction is rejected, and token metrics. MiniMax is called through its
documented OpenAI-compatible Chat Completions endpoint with a validated JSON
prompt contract: Stagehand remains responsible for the live browser session,
navigation guardrails, assertions and screenshots, while MiniMax reviews only
bounded, redacted browser-derived evidence from that completed E2E run. Provider
keys and browser secrets are not useful QA evidence and remain outside the
request. Its request is accounted as a separate
semantic call and is added to—not substituted for—the browser-tool usage.
When it runs through the Delivery worker, the callback records the aggregate as
a separate immutable `stagehand` tool execution in the cost ledger. The JSON
report is the private request/response evidence referenced by that ledger entry;
it is not exposed in activity feeds or public task summaries.

Each entry in `calls` retains the private request body and provider response
needed to audit that exact inference. Provider reasoning traces are deliberately
excluded: they are not delivery evidence and never determine a human gate. The
report's `evidence.artifacts` manifest records every generated PNG with its
SHA-256, byte size, capture time, viewport and same-origin URL. The dashboard
can therefore verify the private S3 object it renders against the exact visual
artifact captured by the browser. A
structured-output parse failure is still recorded as a `failed` call with its
actual usage, so paid failed inferences cannot disappear from cost reporting.

If the provider returns prose instead of the requested schema, Stagehand still
keeps the screenshot and browser-derived page metadata and marks that semantic
portion as `degraded`. A run with no approved browser cases remains `blocked`.
When a deterministic approved browser plan has passed, the QA run can proceed
to its still-mandatory human QA gate, but the degradation remains visible in
private evidence; it is never converted into invented structured evidence.

For a workspace other than this backend repository, register an explicit
operator-owned absolute path to this pinned runner (or package the same runner
into that worker image). Commands execute from the reviewed workspace, so the
relative `tools/stagehand-qa/run.mjs` path above is only valid when that path
exists inside the reviewed checkout.
