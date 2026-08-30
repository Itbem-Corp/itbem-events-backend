# Deterministic release Gatekeeper

`internal/releasegate` is the only domain component allowed to turn structured
delivery evidence into an `allowed` or `blocked` merge/release decision. It
does not call a model, GitHub, AWS, Git, a shell, or a deployment provider.
Agent prose may explain a decision but cannot create, remove, or waive a
reason code.

## Exact subject

Every evaluation starts from an order-independent revision matrix containing
the configured GitHub repository, configured target branch, and exact head SHA
for every affected repository. The Gatekeeper hashes that matrix. Composite
test, compatibility, migration, dependency, environment, and recovery evidence
must carry the same matrix digest. Advancing any repository or changing a
configured branch invalidates that evidence.

The approval subject also binds the action (`merge` or `release`), resolved
hierarchical policy digest, whether every target branch is actually protected,
its canonical required-check set, GitHub integration identity for pinned
checks, and recovery classification. A human approval for a previous SHA
matrix, action, policy, protection state, check producer, or recovery posture
cannot be reused.

## Required evidence

For each repository, the current evaluation requires:

- exact-head branch protection and mergeability evidence;
- every required check passing for that head SHA;
- an approved review for that head SHA by an actor other than the author, with
  no unresolved change request;
- a non-empty Vault revision reconciled to that head SHA;
- a passing secret scan and zero high or critical findings.

For the coordinated change set, it also requires:

- a resolved policy and every configured test kind passing for the exact
  revision matrix (an intentionally empty required-test list is valid only in
  a resolved policy, such as a review-only project);
- passing compatibility, migration, dependency-order, and environment
  evidence for the matrix;
- an evaluated recovery classification: `rollback`, `roll_forward`,
  `expand_contract`, or `irreversible`;
- explicit human approval when recovery is irreversible;
- for both `merge` and `release`, a current approval from a trusted human
  identity bound to the computed subject digest.

Missing, malformed, duplicate, stale, failed, or contradictory evidence blocks
the decision. Strict JSON decoding rejects unknown fields and trailing
documents, so repository or PR text cannot inject an `override` property.

## Integration boundary

This package deliberately does not claim evidence provenance. The control
plane must assemble it from its append-only ledger, approved Vault revisions,
GitHub's current head/protection/check/review state, QA evidence, and trusted
human identities. Repository content is data and must never populate policy or
approval fields.

The Linux release worker is not a policy or Vault authority. It returns only
fresh, repository-scoped GitHub evidence. During the authenticated callback,
the AWS control plane discards every worker-supplied `policy` and `vault`
property, rereads the work item's project, the latest immutable Vault revision
for each repository, all applicable approved policy layers, and their latest
append-only decisions inside the ledger transaction. It verifies each Vault
manifest against its stored SHA-256 and onboarding provenance, then reconciles
its repository SHA to the exact release matrix.

The callback also does not trust the worker to choose that matrix. PostgreSQL
rebuilds it from the approved repository-impact plan, the newest GitHub App
publication for every repository marked `changes`, and the exact one-shot
human publication grant consumed by each record. Publication persists the
GitHub default branch read before the PR was created, not the temporary
`itbem-agent/*` head branch. The fresh GitHub PR adapter must return that same
repository, target branch, and head SHA; any difference aborts the callback.
Forged or stale test, security, compatibility, migration, dependency,
environment, or recovery claims are cleared at this boundary until their
dedicated control-plane evidence ledgers resolve them.

QA is the first of those dedicated ledgers. When QA is enqueued, its
`AutomationTask` stores the canonical revision-matrix digest and receives the
same control-plane candidate. The isolated QA runner projects only task ID,
matrix digest, preview result, repository execution order, reviewed worktree
identity, and bounded validation/QA pass/fail observations. Raw commands,
output, URLs, screenshots and model prose remain encrypted private artifacts.
Each command also carries a test identity such as `unit`, `contract`, or `e2e`
from the operator-owned workspace registry; a task or model cannot rename it.
The callback accepts the projection only for that task and digest, then appends
`delivery.qa.observed.v2` idempotently. Immediately before evaluation, the
control plane selects the newest exact-matrix event, verifies the completed
task and event timestamp, requires the complete approved workspace set and
reviewed worktree branches, and maps each workspace through its consumed
publication grant to the published GitHub repository. A required test passes
only if it was observed passing in every repository whose effective policy
requires it. Missing labels, stale matrices, another repository's result, or
failed commands remain normal `required_test_*` blockers. The security floor is
produced by two reserved operator-owned QA command identities:
`security:secrets` and `security:high-critical`. They execute in each exact
reviewed worktree on the isolated Linux QA plane; configuration may select an
appropriate scanner for each stack without allowing the model to rename the
result. A missing scanner produces no security event and remains
`security_evidence_missing`; a failed secret scan or high/critical scan becomes
blocking evidence. The completed exact-matrix QA callback appends
`delivery.security.observed.v1`, and the resolver accepts only its newest event,
binds every workspace and reviewed branch through the consumed publication
grant, and supplies each exact remote repository/SHA to the Gatekeeper. Raw
commands, output, finding details, and model prose are excluded. This path uses
neither GitHub Actions nor GitHub Advanced Security.

Environment readiness is a separate providerless release observation. Before
enqueue, the control plane resolves the approved workflow, environment and
explicit secret/variable reference names for every exact repository/SHA. The
worker confirms the workflow through the Contents API pinned with `ref=<SHA>`,
confirms the environment, and compares only required names. The callback is
bound to the release task and matrix and appends
`delivery.environment.observed.v1`. Immediately before Gatekeeper evaluation,
the control plane verifies the completed task/timestamp and requires every
observed workflow, environment, SHA and reference list to equal freshly
resolved policy. Missing names become `environment_not_ready`; changed policy
or SHA is stale evidence. Secret values and the complete GitHub inventory are
never persisted.

Compatibility and migration safety use the same exact-matrix QA ledger without
trusting model prose. Operator configuration reserves
`assurance:compatibility` and `assurance:migrations`; each identity must run in
every reviewed worktree. The resolver emits a passed matrix only when every
repository passes, emits failed when any repository fails, and leaves the
Gatekeeper evidence missing when any command is absent.

Dependency readiness is derived by the control plane, never asserted by a
model. It reloads the frozen repository topology, rejects malformed, missing,
duplicate, or cyclic edges, and verifies that the exact QA repository execution
order places each changed dependency before its consumer. It also reloads every
declared work-item prerequisite and requires it to be `released`. The resulting
matrix is failed for a valid but unsatisfied order/prerequisite and invalid
control-plane data aborts evaluation.

Recovery classification comes only from each repository's effective,
independently approved policy. The control plane selects a conservative
composite posture in this order: `rollback` → `roll_forward` →
`expand_contract` → `irreversible`, binds it to the exact matrix, and never
accepts the release worker's classification. Any irreversible component makes
the composite irreversible; its separate exact-subject human approval remains
mandatory and is not inferred from policy approval. Environment readiness is
still unresolved and blocking.

Policy is evaluated independently for every repository using the
platform → organization → project → repository → bounded change-set override
hierarchy. The composite digest binds the repository, effective policy digest,
resolved state, action authorization, and exact target-branch authorization.
Required test kinds are the canonical union across the matrix, while their
repository-specific requirements are retained for QA resolution. Missing policy,
an unauthorized action, or a target branch outside policy remains a structured
blocked decision; malformed, tampered, ambiguous, or over-limit persisted
evidence aborts the callback without appending authority.

The release worker refreshes the published pull request through a
repository-scoped GitHub App token. It requires the PR to remain open and
non-draft, binds the matrix to its current head SHA and actual base branch, and
projects only decisive approvals/change requests. A separate bounded read then
combines classic branch protection with all active repository/organization
rulesets and reads both App check runs and legacy commit statuses for that exact
head SHA. Missing permissions, non-200 responses, pagination, over-limit
results, malformed producer identities, an unprotected branch, or a changed SHA
all fail closed. Only GitHub's `success`, `neutral`, and `skipped` check-run
conclusions (and `success` legacy statuses) project as passing evidence.

The GitHub App installation therefore needs read access to repository metadata,
contents, checks, commit statuses, pull requests, and branch rules/protection.
The token remains restricted to one repository and is never serialized into
the Gatekeeper input, ledger, result, or logs.

An `allowed` decision is not itself a merge or deployment capability. The
future DevOps executor must:

1. persist the evaluated input, decision, matrix digest, subject digest, and
   reason codes in the event ledger;
2. fetch fresh authoritative evidence and re-evaluate immediately before the
   external effect;
3. deduplicate that effect by change-set/action/subject digest;
4. refuse force merge or any bypass path;
5. record the exact resulting merge/deployment SHA and then perform the
   separate post-release health, smoke/canary, and recovery verification.

Until that adapter and persistence exist, the Gatekeeper package grants no
GitHub or production authority.
