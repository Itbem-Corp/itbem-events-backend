# ITBEM Go AI worker

`cmd/itbem-ai-agent` is the local execution plane for ITBEM-only automation.
It polls the private SQS queue, reads only task-scoped S3 inputs, calls the
configured model provider, writes AES-256 encrypted output back to the private
output bucket, and reports lifecycle state to the backend callback.

It is intentionally not a deployment agent: it cannot merge, deploy, read
repository secrets, or execute model-provided shell commands. It may stage,
commit, publish one reviewed branch and create its pull request only when a
GitHub App identity and a matching short-lived human publication grant are both
present.

## Continuous single-queue operation

### Durable delivery execution

New delivery implementation inputs enable `agent_execution`. The worker can
read bounded, redacted files from frozen registered repositories, apply an
incremental diff in its task worktree, run operator-owned validation/acceptance
commands, and feed observed failures back to the model for repair. It cannot
choose a shell command, publish, deploy, merge, or approve its own work.

The hard limits are **six model calls, 256 KiB per provider request, and fifteen
minutes elapsed per task**, including recovery time. Budget admission reserves
all six calls. Every extra call has separate usage and private request/response
references in the tool ledger. Outputs are bounded while streaming. The review
fingerprint hashes the entire binary Git diff, not truncated command logs.

An encrypted `automation/<task-id>/agent-checkpoint.json` retains the input
digest, original run ID, messages, call results, pending request, and completed
action index. Restart resumes the same worktree and audit run. A stored response
is reused; a request with an uncertain outcome is **not called again**. It blocks
for investigation and reconciliation with provider billing. Checkpoint storage
outages also stop inference. This is at-most-once automatic inference after an
ambiguous interruption, not a promise of exactly-once external execution.

For non-implementation operations, the worker also writes the small encrypted
`automation/<task-id>/provider-intent.json` immediately after the canonical
request and immediately before inference. If a redelivery sees that intent but
no durable result, it records an explicit `outcome uncertain` failure and
consumes no provider call. This closes the crash window between provider
completion and result/callback persistence; an operator must reconcile the
private request/provider account before authorizing a fresh task.

Before inference, every changed repository must have `validation_commands`,
and every work-item acceptance criterion must match an operator-owned check:

```json
{
  "acceptance_checks": [
    {
      "criterion": "The approved endpoint rejects unauthorized requests",
      "command": ["go", "test", "./controllers/example", "-run", "TestUnauthorized"]
    }
  ]
}
```

This example is a registry fragment, not a command to run in this repository.
Configure actual project tests; never register a no-op just to pass a gate.
Missing coverage blocks **before** paying for inference. Commands must be safe
to rerun after interruption. Their code runs under the local worker OS account;
environment filtering is defense in depth, not a container security boundary.
Approved `files_impacted` entries must be exact relative paths (or
`workspace://id#relative/path` for multi-repository plans), not directory names.

The API owns a durable `DeliveryContinuation` dispatcher. It stores continuation
intent in the same transaction as creation/approval/rework and links at most one
automation task to each intent. Decision epochs supersede stale results. It
promotes verified results into the next human review and prepares the delivery
summary, but never approves a human gate. Dependency waits reuse the completed
plan instead of paying for another plan.

Code approval waits for publication and preview readiness. A publication grant
queues only its exact authorized branch. Preview/CI readiness is attached via
the existing authorized change-set endpoint using the exact published repository,
branch and commit; the published provenance is preserved. All currently reviewed
changed repositories need a passed CI record and at least one valid preview URL
before QA is queued. These records are readiness reports, **not independent proof
that CI executed**: the worker then observes the preview and runs registered QA.
Deploying a preview still requires the project's deployment/CI integration;
this dispatcher does not create infrastructure or obtain deployment credentials.

Rollout requires the API's normal database migration (new continuation table,
work-item epoch/progress, task continuation unique index), matching API/worker
binaries, and configured private storage/queue/workspace tests. The dashboard
shows queued, blocked, waiting-for-preview, and human-review states. No cloud
resources, running workers, secrets, grants, or model selection are changed merely
by updating this code. Existing queued inputs without `agent_execution` retain
the legacy one-call implementation contract.

One local worker consumes the single private automation queue continuously.
Every message is validated against an operation allow-list before it can reach
the provider; SQS leases, idempotent callbacks, output reuse and visibility
heartbeats make redelivery safe. Set `ITBEM_AI_CONCURRENCY=1` for a strictly
serial local agent, or raise it only for independent workspaces with available
provider capacity. The worker never shares a writable worktree between tasks.
Workers can additionally declare a specialist capability contract with
`ITBEM_AI_CAPABILITIES`, a comma-separated list such as
`delivery.plan,delivery.qa`. A non-empty list is fail-closed: the worker checks
the operation before claiming the task and retains unsupported work for a
capable worker. On SQS, an incompatible delivery is deferred for a bounded 45
seconds before admission, so a specialist does not hold a lease or occupy a
provider slot for work it cannot execute. Transports without a visibility/defer
primitive retain the retry-safe path. An empty value keeps the explicit
generalist profile for local compatibility; it does not grant new server-side
permissions or change the workflow gates.
The transport is strict: it accepts exactly one schema-versioned JSON message,
with no unknown fields and a positive delivery attempt, before scheduling it.
Malformed messages do not consume a model call or acquire review priority;
they remain observable through the normal queue/DLQ policy.

`code.review` is a first-class, advisory PR-review operation. Review jobs are
scheduled ahead of ordinary queue work once received, but only by their
allow-listed operation (there is no caller-controlled priority). The queue
serves a bounded burst of three reviews before giving a waiting non-review job
a turn, so an active PR stream cannot starve QA, planning or implementation.
Within each bounded receive batch, ordinary operations use a round-robin lane
over the optional project identifier and decoded operation name; a flood from
one project, chat or QA operation therefore cannot monopolize a generalist's
local slots while another eligible project or operation waits. Legacy messages
without `project_id` retain operation-level lanes. This is a scheduling
fairness guarantee, not an authorization grant:
capabilities, leases, project admission and workflow gates remain authoritative.
Each review
must return a compact JSON record with a verdict, exact changed-file line
locations, severity, category, grounded evidence, a concrete recommendation,
test plan and coverage gaps. Malformed, ambiguous, fabricated-location or
"approve with findings" responses fail closed and stay privately auditable;
they cannot create a remote review, approve a PR, merge, publish or deploy.

To keep reviews meaningful, submit one task per immutable PR/diff revision.
The private input must include `delivery.repository_ref` (`github://owner/repo`),
distinct 40-character `base_sha`/`head_sha` values, and a bounded
`changed_files` list, trusted `changed_line_ranges` from the frozen patch, the
immutable unified `patch`, and its SHA-256 in `patch_sha256`. Every line range
declares `side: "head"` for additions or `side: "base"` for removals. Findings
must quote evidence from that exact changed side and range; a matching sentence
elsewhere in the diff is not enough.
The worker rejects missing, mutable, duplicate, secret-like or traversal-shaped
paths before calling a provider, and rejects findings outside the changed line
ranges after it returns. When a new commit arrives, enqueue a new review task
rather than reusing the old conclusion. The control plane remains the source
of truth for task state, human decisions, publication grants and final merge
policy.

GitHub redelivery is idempotent for the same `repository + PR + head SHA` and
never substitutes for retrying a failed provider result. An authorized
operator can explicitly create a fresh `code.review` run only from a terminal
failed review; it reuses the exact private frozen input, preserves the failed
run for audit, and cannot change the reviewed revision. This makes recovery
visible without allowing a webhook, queue redelivery or model response to
silently create extra billable reviews.

## Non-mutating product ideation

`product.ideate` is available from the quick Automation console for early
product work. It produces a bounded decision brief with alternative directions,
trade-offs, risks, a success signal, and a recommended first experiment. It
cannot create Delivery work items, inspect a workspace, modify code, or make a
release decision. Turning an idea into delivery work still begins with a human
request and the normal context, plan, code, QA, and release gates.

## Local run

1. Start the local control plane and configure the selected provider key only
   in its ignored local credential bundle or project-scoped Settings. Do not
   place provider API keys in the worker environment or `.env.ai.local`.
2. Copy `.env.ai.local.example` to `.env.ai.local`; configure
   `ITBEM_AI_GATEWAY_URL` for the local control plane. The worker fails closed
   without that gateway and receives only a short-lived, task/run/operation
   scoped inference capability, never a provider key.
3. Optionally register local repositories in `ITBEM_AI_WORKSPACES_JSON` in
   `.env.ai.local` (or in the deployment environment). References from tasks
   use `workspace://<id>` and cannot contain paths.
4. Run `scripts/Start-LocalAIAgent.ps1` from this backend repository.

### Agent harness and model evaluation

`scripts/Test-AgentHarness.ps1` is an offline regression runner. Its provider
fakes and local HTTP gateway tests exercise runtime contracts without loading
provider credentials, contacting a model vendor, or consuming a real queue.
Before starting its Go child, it temporarily clears provider keys, model and
endpoint overrides, provider selectors, and live-harness flags from that child
environment; the parent process values are restored afterward and never
printed. `scripts/Test-AgentHarnessIsolation.ps1` checks this boundary with
synthetic sentinels and a fake Go command, without invoking the real test suite.
The offline gate requires exactly one successful package completion and at least
one passing test for each of its six requested packages. Empty or truncated
streams fail even if the Go process exits zero; raw JSONL remains available for
investigation. A parsed stream also produces `offline-summary.json` with the
Go exit code, failing and skipped test identities, distinct passing tests,
passing test executions, and package evidence gaps, including on failure.
The explicit `passed` field includes exit status and evidence policy. Use
`-RequireNoSkips` for an offline gate that requires every test to execute; any
skip then records a failed verdict and returns a nonzero exit. This option
does not enable integrations or change their environment prerequisites. Malformed streams retain the raw log and fail without a success summary.
The three repetitions have a ten-minute per-package timeout; override with
`-TestTimeoutSeconds` (30–1800 seconds) when diagnosing runtime issues. Do not
reduce repetition count or treat timeout as success. The Windows CI isolation
job exercises empty, truncated, duplicate, no-test, malformed, and failing
streams on pushes and pull requests affecting the agent or harness. It also
verifies restoration of the invoking process environment after success and
failure, including variables that were originally absent.
The old `-LiveMiniMax` and `-LiveProvider` modes were retired because they
constructed provider clients in the test process and bypassed the central
inference gateway; those selectors now fail before Go starts. `-ScoreReportPath`
only replays an existing report and never triggers inference. The scorer requires
JSON objects for every present role and literal boolean execution outcomes.
Missing roles may be skipped with its explicit `-AllowPartial` option, but empty
reports and malformed present roles fail. `-AllowFailures` changes the exit code,
not the recorded verdict. Run `scripts/Test-HarnessSemantics.ps1` for synthetic
scorer regressions; these run in Windows CI alongside the isolation gate. Treat replay input
and output as sensitive because reports can contain prompts and model responses.

For a real model-quality evaluation, create and authorize a normal synthetic
work item in the platform and let an assigned worker call the configured
provider through the central gateway. This preserves the normal task/run lease,
provider policy, usage receipt, cost ledger, and redaction boundaries. The test
harness must not mint a capability, read a provider key, or call a vendor API.

For a dedicated workstation that should continuously serve the one automation
queue, use service mode instead of a fragile terminal/session wrapper:

```powershell
.\scripts\Start-LocalAIAgent.ps1 -KeepAlive
```

Service mode runs the same non-billable doctor before its first start. It exits
cleanly on a deliberate stop, and only restarts unexpected worker exits using
capped exponential backoff. A failed doctor does not enter the loop: correct
the workspace/provider/runtime configuration first. It never syncs a workspace
or makes a provider request on its own; managed repository synchronization
remains the explicit operator command below.

The launcher also owns a session-local Windows mutex while it is consuming.
Normal mode and `-KeepAlive` use that same mutex, so a second terminal cannot
silently start another worker against the same local queue. `-Doctor` and
`-SyncWorkspaces` remain concurrent, read-only/operator commands.

The paired dead-letter queue is never auto-replayed. Platform health reports
only its approximate depth, so an operator can inspect and explicitly decide
how to recover poisoned messages without silently re-running a stale review.

The only billable connectivity command is explicit and guarded:

```powershell
.\scripts\Start-LocalAIAgent.ps1 -ProviderSmoke
```

Before starting a delivery, run the non-billable local doctor:

```powershell
.\scripts\Start-LocalAIAgent.ps1 -Doctor
```

For a worker machine that serves several projects, keep a dedicated managed
base checkout per project (not a developer's active checkout) and synchronize
them before refreshing Delivery checkpoints:

```powershell
.\scripts\Start-LocalAIAgent.ps1 -SyncWorkspaces
```

This command clones a missing configured checkout, or fetches `origin`, safely
switches it to `base_branch` (default `main`) and fast-forwards it. It refuses
local changes or divergent history and never uses reset, rebase, pull or a
task-provided remote. Refresh the project's local context afterwards so a plan
freezes the resulting SHA. Every approved implementation still gets a distinct
`itbem-agent/<task-id>` worktree and branch under that project, so concurrent
tasks and separate projects never share a writable checkout.

It validates each registered Git checkout, its bounded capabilities and its
validation/QA harness. It also reports whether the GitHub App publication
identity is configured, without reading source excerpts, exposing a path,
calling a provider, or revealing a credential. A missing GitHub App disables
only remote publication; planning, isolated implementation and QA remain
available behind their normal human gates.

## Workspace registry example

```json
{
  "dashboard": {
    "path": "C:\\path\\to\\a\\dashboard-git-checkout",
	"repository_url": "https://github.com/example/dashboard.git",
	"base_branch": "main",
    "capabilities": ["repository:read", "repository:fetch", "worktree:create", "patch:apply"],
    "validation_commands": [["npm", "run", "lint"], ["npm", "run", "typecheck"]],
    "component_validation_commands": {
      "apps/dashboard": [["npm", "run", "test:dashboard"]],
      "packages/ui": [["npm", "run", "test:ui"]]
    },
    "qa_commands": [["npm", "run", "test:e2e"]],
    "qa_artifact_patterns": ["test-results/*.png"],
    "qa_semantic_command": ["node", "tools/stagehand-qa/run.mjs", "--url", "{preview_url}", "--output", "{artifact_path}"],
    "sandbox_runtime": "docker",
    "sandbox_image": "node:22-bookworm",
    "sandbox_network": "none",
    "sandbox_cpus": "2",
    "sandbox_memory": "2g",
    "sandbox_pids_limit": 256
  }
}
```

The registry is operator-owned. `repository_url` is only used by the explicit
managed-sync command; runtime task inputs can never choose it. Commands are arrays with allow-listed
executables; no shell, arbitrary path, task prompt, or model response can add
one. `component_validation_commands` is the optional monorepo map: when a
human-approved plan scopes a repository to an overlapping component root, the
harness runs that operator-owned command in addition to the repository-wide
suite and records its scope in validation evidence. It never executes a
component command for an unrelated scope. Implementation uses `git worktree` under the registered repository and
leaves a reviewable branch for the human code-review gate.

`qa_semantic_command` is optional. It runs the pinned, read-only Stagehand
probe only after a preview is healthy and preserves its JSON report and
screenshot as normal private QA evidence. See `docs/STAGEHAND_QA.md` for the
local provider configuration and operational boundaries.

The default MiniMax model is `MiniMax-M3`. `MINIMAX_MODEL` lets an operator
select another model such as `MiniMax-M2.7` when needed; M2.7 requests retain
its documented 2,048 completion-token bound.

Every validation/QA command runs with a reduced credential-free environment,
bounded output, a hard timeout, and (on Linux workers) a dedicated process
group that is killed on timeout so child test runners cannot leak into later
tasks. This is process-tree hygiene, not a hostile-code sandbox: a registered
workspace must still run inside the operator's container/VM boundary before it
is trusted with customer code. The API exposes three explicit admission knobs
for fleet backpressure: `AUTOMATION_GLOBAL_ACTIVE_LIMIT`,
`AUTOMATION_PROJECT_ACTIVE_LIMIT`, and `AUTOMATION_QUEUE_DEPTH_LIMIT`. When
configured, task creation takes a PostgreSQL advisory lock and fails with a
retryable 429 instead of allowing a burst to exhaust the worker fleet or grow
an unbounded durable queue.

For higher-risk repositories, set `sandbox_runtime` to `docker` and pin an
operator-approved image. Registered validation, QA, screenshot and semantic
commands then run with a read-only container root, only the reviewed worktree
mounted writable, a fixed non-root UID, dropped Linux capabilities,
no-new-privileges, bounded CPU/memory/PIDs/file descriptors, bounded tmpfs
caches and `network=none` by default. Set `sandbox_image_digest` to an
operator-pinned `sha256:` value for reproducible images; when present the
runner executes that exact image reference and readiness verifies it locally.
A browser QA
workspace may explicitly use `network=bridge` only when its operator-owned
runner needs to reach the recorded preview URL. Docker availability and image
pulling are operator prerequisites; the worker fails closed rather than
silently falling back to the host process when Docker mode is selected.

In deployed environments, invoke the Go binary directly and inject every
setting through the runtime environment or secret manager. It never reads a
`.env.ai.local` file itself.

### Isolated runtime integration checks

The LocalStack tests create unique task-owned buckets and queues, then remove
those resources. They require a loopback HTTP endpoint and synthetic AWS
credentials; they do not depend on pre-created development buckets. Use a
dedicated LocalStack instance with S3 and SQS enabled:

```powershell
$env:ITBEM_LOCALSTACK_E2E = '1'
$env:ITBEM_LOCALSTACK_ENDPOINT = 'http://127.0.0.1:<dedicated-port>'
go test ./internal/automationagent -run '^TestLocalStack' -count=2 -timeout 3m -v
```

The agent CI workflow provides its own LocalStack service pinned by digest and
runs both transport and durable-redelivery proofs twice. The latter deliberately
fails the first terminal callback and asserts that recovery reuses the durable
result without a second simulated provider call. Callbacks use the current
run-scoped capability protocol and result references bind the claimed run.

`ITBEM_LOCALSTACK_E2E=1` combined with `-short` fails rather than silently skipping
the requested proof. Similarly, an explicitly enabled Docker sandbox integration
fails when Docker is unavailable. For the Docker proof, enable
`ITBEM_DOCKER_SANDBOX_E2E=1` and run `TestDockerSandboxRoundTrip`; the fixture
requires its pinned Go image and verifies UID, capabilities, networking and
filesystem permissions. Ordinary offline runs still skip opt-in integrations.

The Linux integration CI job also preloads the pinned Go sandbox image and runs
Docker isolation and symlink-artifact rejection twice. It sets
`ITBEM_REQUIRE_SYMLINK_PROOF=1`, so a host unable to create a symlink fails that
required proof instead of skipping. Local Windows runs may still skip it when
that explicit requirement is absent. JSONL evidence from LocalStack and sandbox
steps is uploaded for seven days on success or failure. A piped log cannot hide
a test exit failure because both test steps enable shell `pipefail`.

The binary build job depends on both Windows harness validation and Linux
runtime integration. A failed, cancelled or skipped dependency prevents binary
artifact creation. Its own regression step repeats all six harness packages and
the agent command three times with shuffle seed 49207, preserving JSONL evidence
even on failure. The workflow filters include delivery and automation controllers
and runtime routes, so changes in those harness packages trigger the same gates.

Before cross-building binaries, CI also pins Node 22.23.1 and validates the
offline Stagehand runner's syntax and Node tests. Changes under
`tools/stagehand-qa/` trigger the workflow alongside the Go harness packages.
This gate uses synthetic HTTP fixtures and does not invoke browser inference.

### Verify Go execution evidence

`cmd/verify-test-evidence` reads `go test -json` output without executing tests
or inference. CI calls it after each integration/regression pipeline. It requires
one terminal pass per expected package, nonempty test execution, and the exact
configured repetition count for every observed test. It rejects malformed logs,
failures, events after package completion, missing proofs and skipped required
tests. Integration gates use its default no-skip policy and explicitly name both
required tests; the broad offline regression allows recorded optional skips.

```powershell
go run ./cmd/verify-test-evidence -log localstack-integration.jsonl -repetitions 2 -package events-stocks/internal/automationagent -test TestLocalStackTransportRoundTrip -test TestLocalStackRedeliveryReusesDurableResultWithoutProviderRepeat
```

The build regression also includes the verifier package itself. Updating a test
filter alone cannot turn missing integration cases into a successful evidence
gate: the required test names must still be observed passing twice. This validates
execution completeness, not model quality or cryptographic source provenance.

### Compare structured QA claims with runtime observations

`go run ./cmd/score-qa-grounding -observation observation.json -claims claims.json -output score.json` compares a schema-2 `qaevidence.Observation` with separately supplied model claims. Claims schema 1 contains task_id, matrix_digest, preview_passed, verdict and a commands array. Each command requires reference, explicit index, phase, kind and explicit passed. It must match a recorded command, and all recorded commands must be covered exactly once. Command order may differ. Verdict is passed only if preview and every observed command passed; otherwise it must be failed.

The command exits 0 for correspondence, 1 for invalid/mismatched input, and 2 for invocation/output errors. JSON retains failed verdicts and input SHA-256 hashes. The optional output file must be new: The command writes, syncs and closes a temporary file in the destination directory before publishing it with a hard link that cannot replace an existing path. This prevents partial final scores and protects prior evidence, input files and link aliases. The destination filesystem must support hard links; otherwise publication fails explicitly. Use a fresh score path per evaluation. Inputs are bounded to 64 KiB. Both QA observation and claims decoders reject duplicate object fields (including escaped names and case aliases), multiple documents and nesting beyond 64 levels before typed validation. Observations also require explicit preview results and explicit command index/result fields; omitted or null values cannot masquerade as observed false or zero. This evaluator checks supplied structured claims; it does not authenticate local files, judge prose, authorize a release or replace the runtime QA guard. Obtain observations from the trusted ledger. Standalone evaluation requires explicit structured claims; it never infers them from prose. No provider call is made.

Build AI Agent now includes qaevidence and score-qa-grounding in its three-repetition JSONL regression gate, with both package completions required by verify-test-evidence.

### Grounding the worker QA report

When QA captures a ledger observation, the worker requests an explicit top-level
`claims` object in the model report, using claims schema 1 above. The worker
validates the original model JSON before persistence sanitization can erase
ambiguous duplicate fields. It compares model-produced claims with the captured
observation before promoting the narrative to `structured_result`. Missing,
malformed, invented or mismatched claims withhold that structured report.

The private result retains the sanitized model response, usage, independently
observed execution and `qa_grounding` diagnostics: `score_kind` is
`structured_qa_grounding`, and `status` is `passed`, `failed` or `unavailable`.
The status records claims correspondence only. Separate `report_valid` and optional `report_error` fields record narrative acceptance; matching claims can have status passed while semantic/screenshot guards withhold the report. Historical contexts without a ledger remain unavailable for grounding; their
ordinary narrative validation still applies. Grounding success is correspondence,
not a passing QA verdict. Existing preview, semantic, screenshot, defect and
coverage guards still apply. A rejected narrative does not erase completed QA,
repeat a billable call, approve a gate or authorize release. Recovery reuses the
saved result and canonical observation. These checks do not evaluate prose truth
or certify model quality; deterministic worker fixtures use a fake provider.