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
However, completing a continuation still unconditionally sets
`waiting_for_user`; there is no complete delegated decision coordinator.
Publication still requires an exact temporary grant, and final release
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
