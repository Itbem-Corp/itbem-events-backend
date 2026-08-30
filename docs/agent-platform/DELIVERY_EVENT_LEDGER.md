# Delivery event ledger

`delivery_events` is the append-only operational history for a delivery work
item. This first event contract records deterministic release Gatekeeper
evaluations; it does not execute a merge, deployment, rollback, or any other
external effect.

## Integrity and ordering

- Events have a monotonically increasing sequence scoped to one work item.
- The writer locks the parent work-item row while allocating the next sequence.
- A unique `(work_item_id, sequence)` index prevents ambiguous ordering.
- A canonical payload digest and unique dedupe key make an identical retry
  idempotent even when equivalent evidence arrived in a different order.
- PostgreSQL rejects every update or delete. A correction is a new event.
- The private payload contains the exact Gatekeeper input and decision. Its
  digest is verified before any read model is produced.

The API exposes a bounded, authorization-checked projection with the action,
change set, subject/matrix digests, state, stable reason codes, sequence, and
time. It never returns the exact evidence matrix, human or agent identities,
Vault revision IDs, or the private payload. A malformed row, mismatched work
item, or payload digest mismatch fails the complete read closed.

The existing SSE endpoint remains an invalidation stream rather than a second
copy of the work item. It observes only event count and latest occurrence time;
it never selects the private payload. Clients revalidate the authorized read
model when the revision changes.

## Authority boundary

There is intentionally no HTTP endpoint that accepts a Gatekeeper result from
the browser or an agent. A trusted control-plane path must assemble evidence
from authoritative providers and call the ledger writer. Repository, PR, and
model text remain untrusted data.

An `allowed` evaluation is evidence, not authority. Before merge/release is
implemented, the executor still needs a transactional outbox, an exact-subject
effect idempotency key, fresh authoritative re-evaluation immediately before
the effect, and a resulting-SHA event. Until then this ledger cannot change
GitHub or production.
