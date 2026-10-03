# Autonomy deliveries

Status: implementation in progress. This checklist records executable gaps;
it does not declare the five-lane platform production autonomous.

## A — Preparation and authority

- [x] Audit the existing delegated policy and frozen authority snapshot.
- [x] Report end-to-end readiness without granting execution authority.
- [ ] Verify source acquisition, publication, independent review, QA and release prerequisites before activation.
- [ ] Keep unknown live GitHub/environment prerequisites explicit.

## B — Continuity and recovery

- [x] Locate the API-owned durable continuation dispatcher.
- [x] Reconcile callback-submitted phases without repeating their state transition.
- [x] Keep transient admission failures retryable without exhausting the correction budget (24-hour infrastructure deadline).
- [ ] Implement evidence-backed delegated decisions and bounded correction cycles.
- [ ] Prove recovery, concurrency, cancellation and stale-epoch rejection.

## C — Release and qualification

- [x] Locate exact-SHA review publication and deterministic release evidence.
- [ ] Connect delegated progression to authoritative independent evidence.
- [ ] Verify branch/environment protections through the configured identities.
- [ ] Qualify a complete five-lane delivery, restart and recovery.
- [ ] Record live deployed SHA and health; never treat a synthetic fixture as production qualification.

## Audit findings

The existing policy supports human and delegated gate modes, and an immutable
work-item snapshot binds repository revisions, Vault and policy digests.
The QA continuation now advances under delegated authority when the sealed
observation and independent exact-SHA review prove its current revision matrix.
Code decisions also advance from independently published PR reviews when the
task mandate delegates them. Plans still require human approval; there is no
complete delegated decision coordinator. Publication requires an exact temporary
grant, which the coordinator may issue under explicit frozen publication authority.
Final release
authorization checks a named human actor. These boundaries must be implemented
together with independent evidence, not removed to obtain apparent autonomy.
The Go runtime has a deterministic release observer, but no implemented GitHub
merge or workflow-dispatch adapter was found in this audit. Release execution
therefore needs its own authority-bound, idempotent implementation and failure
qualification; a successful observer result cannot stand in for a deployment.

Authenticated callbacks can submit implementation and QA directly under frozen
delegated authority. The continuation subsequently attempts the same transition
again, which can strand a successfully completed phase. Recovery must reconcile
the already-submitted state while preserving evidence checks and epoch fencing.

Before this change, admission retries counted all HTTP failures alike after specific budget
errors are handled. Temporary service outages can therefore permanently block
a continuation. Retry classification must distinguish infrastructure waits from
invalid input and exhausted budgets, and must not repeat completed inference.

## Implemented diagnostic boundary

`GET /automation/projects/:id/autonomy-readiness?repository=github://owner/repo`
requires project read access, a registered Vault with verified content, and the
existing effective-policy resolver. It returns actionable checks without keys,
installation IDs, tokens or approver identities. Local App configuration is
distinct from live access, branch protections and deployment qualification.
Until the complete delegated coordinator exists, activation is explicitly blocked.

Runtime preparation requires all workspaces to be sandbox-ready on one
non-draining implementation worker. Partial readiness from several hosts cannot
make one multi-repository task dispatchable.

The platform qualifier runs the named preparation and continuation recovery
fixtures using its existing required-test verifier. These checks prove those
components only; they do not satisfy the live V1 acceptance contract.

## Delegated QA increment

Only signed, source-receipt-bound QA tasks admitted under the frozen delegated
policy can decide QA automatically, and only when their work-item mandate does
not reserve `approve_qa` for a human. Missing mandates retain the existing human
default. The coordinator rechecks the sealed event,
task lifetime, current preview, changed repositories and target branches,
operator test identities, independent source receipts and the latest independent
`Bema Review / exact-sha` publication for the exact repository, PR and commit.
Model prose or a verdict cannot authorize a transition. Missing proof, failing
security checks and an unchanged failed revision matrix require escalation.

Passed QA creates a delegated QA gate and schedules the summary phase. A failed
functional check schedules implementation within the unchanged approved plan,
with at most three accumulated code/QA correction decisions. It allocates a new
approved plan version referencing the original approval and fresh execution
steps; old steps and evidence are retained. A changed definition or active plan
execution prevents a correction. Legacy tasks and human policies retain manual
QA decisions. Publication grants are revoked; this increment grants neither
merge nor deployment authority.

The gate, new plan/steps, epoch and next continuation commit in one transaction
under the work-item lock. CI runs a real disposable PostgreSQL test with race
detection covering concurrent replay, automatic success, fresh correction
execution, human policy, wrong source/PR/actor, corrupted JSONB evidence,
correction exhaustion and rollback when scheduling the next phase fails.
These are component qualifications, not evidence of live production autonomy.

## Delegated code and publication increment

The existing publication flow creates a PR while code review is pending. The
independent Reviewer inspects that immutable head before code can enter preview
and QA. This increment connects that flow; it does not invent a local review or
represent publication as code approval.

An implementation continuation with frozen delegated repository authority may
issue a 30-minute grant for each changed workspace only when its mandate does
not reserve `authorize_publication` for a human. Each grant binds the current
implementation, unchanged approved definition, frozen base, repository, branch,
diff SHA-256 and allowed target branches. Grant creation and all durable intents
share the completion transaction. Replayed completion creates no duplicate grant.

Before remote effects, a delegated Publisher executes operator-owned
`security:secrets` and `security:high-critical` commands in a Docker or Firecracker
sandbox, verifies that they did not change the diff, and renews control-plane
authority. Expired/revoked grants, stale epochs and changed local diffs reject
renewal. The worker checks expiry/renewal again before commit, push and PR creation.
Failures after a confirmed push retain partial effect evidence for reconciliation.

A durable `code_review` intent waits up to 24 hours for all published repositories,
their CI, consumed grants, exact current diffs and latest independent PR/SHA review.
It makes no model call. When the mandate permits `approve_code_review`, passing
reviews create one delegated gate and queue preview; verified preview then queues
QA. A functional change request can queue a fresh execution of the same approved
definition within the shared three-correction budget. Sealed private Reviewer
findings provide the actual correction context; missing/altered findings, unchanged
failed diffs, oversized context or high/critical security findings escalate.
Human mandates retain their decisions, and the plan, merge and release boundaries
remain in place.

The publication callback now accepts its admitted `code_review` state and retains
the grant's diff fingerprint, security receipts and actual target branch. CI tests
real PostgreSQL continuation/replay, rollback, correction and preview-to-QA, plus
revocation, expiry, stale epochs, changed diffs and rejected callback receipts.
These tests use synthetic worker/GitHub evidence and do not certify a live remote
publication or production deployment.
