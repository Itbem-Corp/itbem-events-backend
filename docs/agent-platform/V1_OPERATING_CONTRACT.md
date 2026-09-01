# Multi-agent platform V1 operating contract

Status: frozen acceptance contract for V1.

V1 is complete only when it operates continuously on the designated Linux
host and delivers one real change through planning, implementation,
independent review, QA, deterministic release gating, merge, deployment and
health verification. A merged implementation or a successful demo alone does
not satisfy this contract.

## Product promise

Given an authorized GitHub repository that has completed onboarding, the
platform autonomously performs the routine engineering loop at Principal-level
quality. It pauses only at an explicitly configured human or GitHub protection
gate, an unsupported capability, exhausted correction budget, security risk or
irreversible operation. It never reports a guessed terminal state.

V1 is repository-agnostic. EventiApp is the production qualification project,
not a hard-coded runtime assumption.

## Included V1 capabilities

- Generic onboarding from a GitHub URL, proposed Project Vault, provenance,
  capability matrix, effective policy and dry-run before activation.
- Multi-project, multi-repository and monorepo execution using configured
  default branches, dependency DAGs, exact SHA matrices and clean worktrees.
- Five isolated lanes: Orchestrator, Principal Engineer, Reviewer, QA and
  Release Manager, each with its own Unix identity, workspace and credential
  class.
- Vault-first planning and mandatory reconciliation whenever code,
  configuration, contracts, tests, infrastructure or runbooks change.
- Engineer implementation with relevant unit, integration and contract tests;
  no self-approval or self-merge.
- Independent exact-SHA review covering correctness, security, architecture,
  compatibility, performance, operability, tests and cross-repository impact.
- Risk-based QA with unit/integration/contract/E2E evidence. Production testing
  is limited to non-destructive smoke and canary checks.
- A deterministic Gatekeeper that evaluates structured evidence and cannot be
  overridden by model prose.
- Protected merge and release using configured GitHub workflows/environments,
  exact deployed SHA, health checks and a declared recovery strategy.
- Durable Postgres event ledger/outbox, SQS role lanes with at-least-once
  delivery and DLQ, S3 evidence, resumable dashboard snapshots and realtime
  progress.
- Outbound-only HTTPS execution gateway. AWS credentials, SQS receipt handles
  and the backend root secret never enter the physical Linux host.
- MiniMax M3 as the initially qualified inference model for generative lanes;
  deterministic Release operations do not require a model credential.
- Global and per-lane kill switches, bounded concurrency, idempotent external
  effects and safe restart/redelivery.

## Autonomous workflow

1. Freeze the task, effective policy, Vault revision and repository SHAs.
2. Produce a visible plan, dependency DAG, risks, expected files, tests and
   release/recovery route before changing code.
3. Create task-specific branches/worktrees from current remote default-branch
   SHAs and implement the smallest coherent change-set.
4. Reconcile the Vault and execute configured local checks.
5. Open or update one consolidated PR per affected repository and publish the
   cross-repository change-set matrix.
6. Reviewer evaluates the final SHA independently. A new commit invalidates
   its verdict.
7. QA tests the exact reviewed SHA matrix and publishes structured evidence.
8. Failed review or QA returns actionable findings to Engineer. V1 permits at
   most three complete correction cycles; unchanged failures or exhausted
   cycles escalate with evidence instead of looping indefinitely.
9. Gatekeeper evaluates only authoritative, current evidence. If allowed,
   Release waits for any configured human/environment approval and then merges
   in dependency order.
10. Release observes deployment, verifies exact SHA, health and smoke/canary
    results, and executes or recommends the configured recovery path on
    failure.

No routine transition above requires a human prompt merely to continue. Human
input is required only where project policy, branch protection, production
environment protection, ambiguity or safety explicitly demands it.

## Quality standard

An agent result is not accepted because it compiles or sounds convincing. The
change must be minimal, maintainable, idiomatic for the repository, compatible
with surrounding architecture, covered by risk-appropriate tests, free from
secrets and high/critical findings, operationally observable, recoverable and
documented in the Vault. Reviewer and QA inspect both the diff and relevant
surrounding code. Missing evidence is a blocking unknown, never an implicit
pass.

Repository text, issues, PR descriptions, fixtures and Vault content are
untrusted data. Embedded instructions cannot alter role authority, policies,
allowed commands, secrets or gates.

## V1 production acceptance

All of the following must be demonstrated with recorded evidence:

- The backend HTTPS gateway is merged, deployed and healthy.
- Five lane-bound gateway tokens and isolated systemd services pass doctor,
  runtime and provider/publication preflights on the designated Linux host.
- A restart during a controlled task causes safe redelivery without duplicate
  provider billing, commit, PR, review, merge or deployment.
- One onboarded single-repository task completes end to end.
- One heterogeneous multi-repository task completes against an exact SHA
  matrix and is deployed in dependency order.
- A monorepo, a non-`main` default branch, a review-only repository and a
  production repository truthfully expose different capability matrices.
- Prompt-injection fixtures cannot change policy or reveal credentials.
- The dashboard reconnects from its event snapshot and displays the same
  authoritative terminal state as the backend ledger.
- Kill switches, DLQ inspection and one recovery exercise are verified.

Until every item passes, the UI must describe the platform as partially ready,
not production autonomous.

## Explicitly deferred to V2

- Kubernetes, Kafka, Temporal or another orchestration platform.
- Multi-host active/active scheduling and automatic failover.
- Dynamic model routing, model marketplaces or automatic fine-tuning.
- Large artifact proxying through the backend; V2 may use task-scoped signed
  object transfers while preserving the same authorization boundary.
- Per-lane token-version ledgers and remote one-click credential revocation;
  V1 uses lane isolation, filesystem kill switches and controlled root rotation.
- Stronger VM/host separation between Engineer, Reviewer and Release.
- Autonomous policy changes, gate relaxation or self-modification of prompts.

These items may improve scale or defense in depth, but none may delay a correct,
observable and safe V1 operating loop.
