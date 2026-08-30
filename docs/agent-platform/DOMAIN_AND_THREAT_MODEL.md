# Multi-agent engineering platform: domain and threat model

Status: foundation contract. These invariants apply to every configured project
and repository; EventiApp is a validation ecosystem, not a special case.

## Authorities and trust boundaries

1. GitHub is authoritative for repository identity, refs, commits, pull
   requests, reviews and checks.
2. The control-plane database is authoritative for orchestration state,
   policies, event sequences, human gates and immutable Vault metadata.
3. S3 is authoritative for large private evidence and generated indexes. A
   database record stores only its scoped reference and digest.
4. Linux workers are replaceable execution capacity. Their local checkout,
   logs and model output are never authoritative state.
5. Repository files, issues, pull-request text, comments, docs and Vault source
   material are untrusted data. They cannot grant permissions, change policy,
   approve a gate or select credentials.

The deterministic Gatekeeper consumes structured evidence. Generative output
may propose work and explain evidence, but cannot set a terminal gate result.

## Core aggregate

```text
DeliveryProject
  ├─ repository onboarding runs (mutable review workflow, SHA-pinned)
  ├─ repository context (approved current checkpoints)
  ├─ Vault revisions (immutable, repository-scoped history)
  ├─ work items and dependency DAG
  ├─ plans / change sets / evidence / human gates
  └─ release record and recovery classification
```

An ecosystem is one project containing one or more repository contexts. No
domain rule assumes a branch named `main`, a particular stack, command, cloud,
domain or workflow. Those values come from inspected evidence plus approved
project/repository policy.

## Repository onboarding state machine

```text
requested -> inspected/proposed -> approved
     |              |
     └-> blocked    └-> superseded by a new SHA inspection
```

- Inspection accepts a strict GitHub repository URL, resolves the configured
  default branch and freezes its full commit SHA.
- Static inspection reads a bounded safe file inventory. Only allow-listed
  structured manifests may produce command proposals. It never executes code.
- The proposal publishes `ready`, `partially_ready` or `blocked` and an
  evidence-backed capability matrix. Missing evidence remains `unknown`.
- Approval repeats the expected SHA and is serialized at project scope. It
  publishes repository context and a new immutable Vault revision in one
  transaction.
- A changed default branch produces a new onboarding run. It never mutates the
  historical Vault evidence used by earlier work.

## Project Vault contract

The curated Vault and generated index are separate:

- A curated manifest is small, JSON, reviewable and versioned. Entries contain
  a stable key, kind, lifecycle (`active`, `deprecated`, `removed`), structured
  value and provenance (`source`, path, SHA, confidence).
- A generated code/search index is disposable and belongs in object storage.
  It cannot overwrite curated decisions.
- Repository onboarding creates repository-scoped revisions. The effective
  project Vault is the latest approved revision of every configured repository
  plus future project-scoped decisions.
- Reconciliation creates another revision. Database and application guards
  reject update/delete of published revisions.
- Secrets, credential values, raw environment files and arbitrary repository
  prose do not belong in a Vault manifest.

Every work item must freeze the Vault revision IDs and repository SHAs it used.
If code contradicts the Vault, code/evidence wins for the current SHA and a
Vault reconciliation becomes a required gate before merge.

## Role separation

| Role | May do | Must not do |
|---|---|---|
| Engineer | Plan a cross-repo DAG, create clean worktrees, implement, test, update Vault, open PRs | Approve/review/merge its own changes |
| Reviewer | Inspect exact head SHA, surrounding code, Vault and cross-repo compatibility; comment/approve/request changes | Modify code, deploy, or preserve approval after SHA changes |
| QA | Run approved unit/integration/contract/E2E matrix in isolated environments and publish evidence | Approve code or merge; run destructive production tests |
| DevOps/Release | Observe CI/CD, releases, health and recovery; submit operational changes by PR | Change product logic or bypass gates |
| Gatekeeper | Evaluate structured policy and current evidence deterministically | Invoke a model to waive a failed/missing gate |

Worker identities, system users, credentials, queues and workspaces are isolated
per role. GitHub App installation tokens are minted just in time. Production
deployments use GitHub Actions OIDC and protected environments rather than a
permanent local production credential.

## Merge/release predicate

Merge is allowed only when all predicates are true for the final head SHA:

- reviewed SHA equals current head SHA and approval is independent;
- branch protection, conflicts and required checks are valid/current;
- Vault reconciliation is complete;
- required unit, integration, contract and E2E checks are green for the exact
  single- or multi-repo SHA matrix;
- secrets and high/critical security findings are absent;
- migrations, compatibility, ordered dependencies and environment readiness
  are evaluated;
- recovery is classified as rollback, roll-forward, expand/contract or
  irreversible, with required human approvals.

Post-merge, release success requires workflow completion, exact deployed SHA,
health and non-destructive smoke/canary evidence. A green build is not proof of
a healthy deployment.

## Threats and required controls

| Threat | Control |
|---|---|
| Prompt injection in README/issue/PR/Vault source | Treat as data; structured parsers and allow-lists only; no policy/tool authority from content |
| Stale approval after push | Approval and all evidence keyed by exact SHA; invalidate on head change |
| Dirty or wrong-base checkout | `fetch --prune`; resolve configured default branch; new clean worktree from remote SHA; never mutate dirty worktrees |
| Cross-repo partial validation | Change-set ID, dependency DAG and immutable SHA matrix; invalidate composite evidence when any ref advances |
| Duplicate queue delivery | At-least-once consumers with lease, idempotency key and external-effect deduplication |
| Agent self-approval | Separate identities and Gatekeeper checks for actor independence |
| Credential exfiltration | Per-role least privilege, short-lived tokens, redaction, outbound policy, no secrets in prompts/Vault/evidence |
| Malicious build/test | Static onboarding first; command execution only after human approval, allow-list and sandbox isolation |
| Lost/reordered realtime state | Append-only event ledger, transactional outbox, aggregate sequence, resumable snapshots/SSE |
| False release success | Verify exact deployed SHA, health, smoke/canary and recovery evidence |
| Runaway automation | Project budgets, iteration limits, leases, kill switches, DLQ and human escalation |

## Execution topology

- AWS control plane: existing backend/Postgres, SQS multi-lane queues and DLQ,
  S3 evidence/Vault indexes and CloudWatch telemetry.
- Linux execution plane: outbound-only systemd services for poller, engineer,
  reviewer, QA and release workers. Services use distinct OS users, environment
  files, worktree roots and resource limits.
- GitHub runners remain isolated from model workers. A worker can observe a
  workflow, but deployments occur through repository CI/CD policy.

The platform must survive worker restart after any durable transition without
losing a job or repeating a GitHub/AWS effect.

## Delivery sequence

1. Generic onboarding and immutable Vault foundation.
2. Policy hierarchy and capability dry-runs.
3. Isolated Linux roles/systemd and multi-lane queue/DAG.
4. Independent Engineer/Reviewer and SHA invalidation.
5. QA composite environments and deterministic Gatekeeper.
6. DevOps/release/recovery automation.
7. Event ledger/outbox and realtime dashboard.
8. Security, failure/restart, prompt-injection and staging validation.

Each step ships as a small independently reviewable PR. Later steps may extend
the schema but cannot weaken the invariants above.
