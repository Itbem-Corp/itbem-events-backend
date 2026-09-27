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

Install the runner once into an execution-plane directory owned by the
platform operator, never inside a customer or product repository. It may be
packaged from this source repository, but the deployed copy is an independent,
immutable release artifact:

```bash
install -d -m 0755 /opt/itbem-ai-agent/tools/stagehand-qa
cp tools/stagehand-qa/{run.mjs,package.json,package-lock.json} /opt/itbem-ai-agent/tools/stagehand-qa/
cd /opt/itbem-ai-agent/tools/stagehand-qa
npm ci --ignore-scripts
chown -R root:root /opt/itbem-ai-agent/tools/stagehand-qa
chmod -R a-w /opt/itbem-ai-agent/tools/stagehand-qa
sha256sum run.mjs
```

Record the resulting lowercase SHA-256 and the absolute path in the QA worker
configuration. Both values are non-secret, but they are operator-owned. The
worker refuses to pass `MINIMAX_API_KEY` to any runner outside that path or
whose digest differs. This protects the credential even when an onboarded
repository configures its own semantic QA command.

The pinned Stagehand release requires Node `^20.19.0` or `>=22.12.0`. The
runner verifies this before it opens a browser. It bounds Stagehand
initialization (35 seconds), the gateway inference request (30 seconds) and
browser shutdown (10 seconds). A timeout is a failed QA run, never an
indefinitely leased worker task or a silent pass.

Do not configure a provider API key, provider base URL, or provider model in
the Stagehand runner environment. During a delivery run, the active Go worker
injects only a short-lived inference capability scoped to the authenticated
task, run and `delivery.qa` operation, plus the gateway URL. The runner fails
closed if any part of that binding is missing. Provider/model routing and
credentials remain server-side in the ITBEM inference gateway and the
project-scoped credential bundle described in
[`AI_CREDENTIAL_BUNDLE.md`](AI_CREDENTIAL_BUNDLE.md); the worker and Stagehand
receive only the normalized completion. The local worker needs
`ITBEM_AI_GATEWAY_URL`, not `MINIMAX_API_KEY`, `OPENAI_API_KEY`,
`DEEPSEEK_API_KEY`, or `OPENROUTER_API_KEY`.

Browserbase is a separate optional browser-service credential, not an
inference-provider key. It must be explicitly selected with
`STAGEHAND_QA_ENV=BROWSERBASE` and its own `BROWSERBASE_API_KEY`; the pinned
runner passes only that value to the browser service. Otherwise, use
`STAGEHAND_QA_ENV=LOCAL` (the default).

## Workspace registration

Add the exact operator-owned runner path to the relevant workspace in
`ITBEM_AI_WORKSPACES_JSON`. It must match
`ITBEM_STAGEHAND_RUNNER_PATH` after resolving symlinks:

```json
{
  "qa_semantic_command": [
    "node", "/opt/itbem-ai-agent/tools/stagehand-qa/run.mjs",
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
Before each case screenshot the runner waits for a rendered document and two
stable layout/visibility samples (bounded to 1.5 seconds). This prevents a
transitioning shell from being recorded as a false before/after state even when
the DOM selector assertion has already passed.
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
absence of request failures. The report also includes a private, bounded
gateway-response excerpt when structured extraction is rejected, and token
metrics. The semantic review is sent to the ITBEM gateway with the run-scoped
capability and validated JSON contract. Stagehand remains responsible for the
live browser session, navigation guardrails, assertions and screenshots; the
configured `delivery.qa` route reviews only bounded, redacted browser-derived
evidence from that completed run. Provider keys and browser secrets are not
useful QA evidence and remain outside the request. The call is accounted
separately from browser-tool usage. The callback records it as a separate
immutable `stagehand` tool execution in the cost ledger using the provider and
model resolved by the gateway. The JSON report is private request/response
evidence; it is not exposed in activity feeds or public task summaries.

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

Every registered workspace uses the same operator-owned absolute runner path
(or a separately hashed runner packaged into that worker image). Commands
execute from the reviewed workspace, so relative runner paths are never
eligible to receive the MiniMax credential.
