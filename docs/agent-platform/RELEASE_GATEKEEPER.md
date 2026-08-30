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
