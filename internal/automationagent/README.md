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

The release lane also accepts `delivery.release_gate`, a providerless operation
that evaluates a control-plane-built exact revision candidate and returns a
bounded structured handoff. It cannot invent a human approval: the signed API
callback binds the authenticated task requester and appends the immutable
Gatekeeper event after re-evaluating the same subject.

Remote publication resolves the repository's GitHub App installation with an
App assertion, verifies that installation is in the configured allow-list, and
mints a short-lived token restricted to that one repository. This supports
authorized repositories across organizations without accidentally selecting
the first configured installation or falling back to a user PAT/SSH key.

Before a release-gate callback, the release worker also rereads every published
PR with that repository-scoped token. A changed head, closed/draft PR,
ambiguous change set or unbounded review history fails the run. Current base
branch and decisive reviews replace stored mutable claims. The worker then
combines classic protection and all active rulesets for that base branch, and
collects bounded App check runs plus legacy commit statuses for the exact head.
An unprotected branch, missing permission, truncated response, wrong SHA, or
malformed integration identity fails closed instead of being treated as an
empty requirement set.

The worker deliberately does not read PostgreSQL and cannot assert project
policy or Vault state. On callback, the control plane replaces those candidate
fields from its append-only policy/Vault stores, computes one canonical
multi-repository policy digest, validates every Vault manifest and reconciles
each one to the exact GitHub head before it binds the authenticated human
requester to the Gatekeeper subject.

Publication records the GitHub default branch observed before creating the PR.
At release time the control plane reconstructs every repository/target/SHA from
the approved impact matrix, the GitHub App publication row, and its consumed
human grant, then requires the fresh PR read to match it exactly. The callback
also removes worker-supplied QA, security, compatibility, migration,
dependency, environment, and recovery claims; those fields remain blocking
until separate immutable evidence resolvers attach them.

For `delivery.qa`, the control plane fixes the current multi-repository matrix
digest on the task before enqueue. The runner's callback handoff contains only
that task/matrix identity, preview status, repository execution order, reviewed
worktree references, operator-owned test identities, and validation/QA
pass/fail flags. It excludes commands, output, URLs, screenshots and the
model-authored QA narrative. A task or model cannot label its own result. The
backend strictly validates the handoff and appends `delivery.qa.observed.v2`;
changing any release SHA leaves the historical QA event intact but makes it
ineligible for the new matrix. The release resolver also binds every workspace
and reviewed worktree branch to its exact GitHub App publication before a
named test can satisfy that repository's policy.

The isolated QA lane also runs the operator-configured commands identified as
`security:secrets` and `security:high-critical` in every exact reviewed
worktree. These identities are fixed by workspace configuration; the model
cannot create or rename them. The completed exact-matrix callback promotes only
their bounded pass/fail state into `delivery.security.observed.v1`. Missing
commands yield no event, and failed scans become Gatekeeper blockers. Commands,
output and findings stay private. This uses neither GitHub Actions nor GitHub
Advanced Security and requires no additional GitHub App permission.

The same operator-owned registry may identify commands as
`assurance:compatibility` and `assurance:migrations`. Both must execute in every
reviewed worktree for their coordinated Gatekeeper matrix to exist. One failure
fails the matrix; one missing identity keeps the evidence missing. The handoff
still exposes only command identity and pass/fail, never command text or output.

## Continuous role-lane operation

Each long-lived worker declares one exact identity with `ITBEM_AI_ROLE` and
`ITBEM_AI_QUEUE_LANE`: `orchestrator/orchestration`,
`principal_engineer/engineering`, `reviewer/review`, `qa/qa`, or
`release_manager/release`. A cross-role or partial identity fails doctor and
startup. A message whose allow-listed operation belongs to another identity is
rejected before input retrieval, callback lease acquisition or provider use.
The empty identity remains accepted only while draining the retained combined
queue during migration.

The `release_manager/release` worker is deterministic and starts without any
model API key. Its provider/model heartbeat fields must remain empty. Every
other role, including the temporary combined worker, still requires its scoped
model provider because it can execute at least one inference operation. This
keeps GitHub publication authority and model credentials out of the same
release process unless a future reviewed operation explicitly needs both.

SQS leases, idempotent callbacks, output reuse and visibility heartbeats make
redelivery safe. Set `ITBEM_AI_CONCURRENCY=1` for a strictly serial role, or
raise it only for independent workspaces with available provider capacity. The
worker never shares a writable worktree between tasks.
The transport is strict: it accepts exactly one schema-versioned JSON message,
with no unknown fields and a positive delivery attempt, before scheduling it.
Malformed messages do not consume a model call or acquire review priority;
they remain observable through the normal queue/DLQ policy.

`code.review` is a first-class, advisory PR-review operation. Review jobs are
scheduled ahead of ordinary queue work once received, but only by their
allow-listed operation (there is no caller-controlled priority). The queue
serves a bounded burst of three reviews before giving a waiting non-review job
a turn, so an active PR stream cannot starve QA, planning or implementation.
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

1. Start the local control plane.
2. For local development, copy `.env.ai.local.example` to `.env.ai.local` and
   set the selected provider secret there (for example `MINIMAX_API_KEY`). By
   default the launcher gives that ignored local file priority over a stale
   Windows User/process variable; use `-PreferProcessEnvironment` only when
   an operator deliberately wants the process environment to win. Never
   commit the local file.
3. Optionally register local repositories in `ITBEM_AI_WORKSPACES_JSON` in
   `.env.ai.local` (or in the deployment environment). References from tasks
   use `workspace://<id>` and cannot contain paths.
4. Run `scripts/Start-LocalAIAgent.ps1` from this backend repository.

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
its approximate depth plus safe per-lane counters and role/lane heartbeats, so
an operator can inspect and explicitly decide how to recover poisoned messages
without silently re-running a stale review.

For the dedicated Linux host, use the reviewed unit, installer and activation
runbook in `deploy/systemd/`. The installer stages a SHA-addressed binary and
root-only configuration but intentionally never starts or enables a service.

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
    "validation_commands": [["npm", "run", "test:unit"], ["npm", "run", "test:contract"]],
    "validation_command_kinds": ["unit", "contract"],
    "qa_commands": [["npm", "run", "test:e2e"]],
    "qa_command_kinds": ["e2e"],
    "qa_artifact_patterns": ["test-results/*.png"],
    "qa_semantic_command": ["node", "tools/stagehand-qa/run.mjs", "--url", "{preview_url}", "--output", "{artifact_path}"]
  }
}
```

The registry is operator-owned. `repository_url` is only used by the explicit
managed-sync command; runtime task inputs can never choose it. Commands are arrays with allow-listed
executables; no shell, arbitrary path, task prompt, or model response can add
one. Each `*_command_kinds` list must be empty or have exactly one unique,
policy-compatible identity for every matching command. Empty lists preserve
legacy execution but cannot satisfy named release gates. Implementation uses
`git worktree` under the registered repository and
leaves a reviewable branch for the human code-review gate.

`qa_semantic_command` is optional. It runs the pinned, read-only Stagehand
probe only after a preview is healthy and preserves its JSON report and
screenshot as normal private QA evidence. See `docs/STAGEHAND_QA.md` for the
local provider configuration and operational boundaries.

The default MiniMax model is `MiniMax-M3`. `MINIMAX_MODEL` lets an operator
select another model such as `MiniMax-M2.7` when needed; M2.7 requests retain
its documented 2,048 completion-token bound.

In deployed environments, invoke the Go binary directly and inject every
setting through the runtime environment or secret manager. It never reads a
`.env.ai.local` file itself.
